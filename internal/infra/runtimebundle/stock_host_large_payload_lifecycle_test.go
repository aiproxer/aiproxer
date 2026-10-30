package runtimebundle_test

// Task 4 (large-payload-lifecycle-correctness) serving witness: accepted replay
// must stay usable through receive-phase replacement on the REAL serving path
// (production assessor + production executor + standard openresponses frontend
// + actual replacement adapter).
//
// The sentinel backend's first OpenWire returns a live stream, then its first
// Recv fails with a recoverable pre-output error. The runtime must reopen the
// exact bytes from the still-owned replay source on a second wire attempt and
// deliver recovered output: two wire opens, zero canonical opens, source
// reclaimed afterwards. A failure AFTER committed content must create no
// replacement (Requirement 1.4). Memory-backed and disk-backed capture are
// distinct cases (Requirements 1.1, 1.2, 1.3, 3.3, 3.4, 1.4).
//
// A fake retry performed inside Execute is insufficient evidence, and an
// initial-open failure is not accepted evidence: the failure here occurs at
// RECV time after the first OpenWire has already returned.
//
// Production wire plans are single-candidate (assessment resolves one initial
// candidate), so the witness drives the production A-leg route-override seam:
// the universal openresponses lane takes an active admin override selector
// verbatim, and the failover expression yields the two real attempts. The
// assessor, executor, frontend, and both adapters stay production; only the
// admin control-plane input is scripted, and the replacement itself runs
// through the real opener, the real source reopen, and the real adapter body.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// lplcServeFlakyStream wraps a real adapter stream and injects one scripted
// receive failure. In flap-first mode it delivers prefix events (possibly
// none), then returns fail once, then delegates to the wrapped stream. In
// fail-after-commit mode it delegates until the wrapped stream delivers an
// output-committed event, then returns fail once on the following Recv.
type lplcServeFlakyStream struct {
	inner  aipapi.ManagedEventStream
	mu     sync.Mutex
	recvs  int
	prefix []aipapi.Event
	fail   error

	failAfterCommit bool
	committedSeen   bool
	failed          bool
}

func (s *lplcServeFlakyStream) Recv(ctx context.Context) (aipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failAfterCommit {
		if s.failed {
			return s.inner.Recv(ctx)
		}
		if s.committedSeen {
			s.failed = true
			return aipapi.Event{}, s.fail
		}
		ev, err := s.inner.Recv(ctx)
		if err == nil && aipapi.OutputCommitted(ev) {
			s.committedSeen = true
		}
		return ev, err
	}
	if s.recvs < len(s.prefix) {
		ev := s.prefix[s.recvs]
		s.recvs++
		return ev, nil
	}
	if s.recvs == len(s.prefix) {
		s.recvs++
		return aipapi.Event{}, s.fail
	}
	return s.inner.Recv(ctx)
}

func (s *lplcServeFlakyStream) Close() error {
	return s.inner.Close()
}

func (s *lplcServeFlakyStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	return s.inner.Cancel(ctx, cause)
}

// lplcServeSentinel carries serving-witness observation across the upstream
// stub and the backend OpenWire/Open wrappers.
type lplcServeSentinel struct {
	spoolDir string

	wireOpens atomic.Int32
	canonOpen atomic.Int32

	// flapOnce, when true, wraps only the first OpenWire stream with a
	// scripted receive failure; the replacement attempt opens cleanly.
	flapOnce bool
	// flapPrefix delivers synthetic content before the scripted failure
	// (unused by the committed case below, kept for documentation of the
	// flap-first mode); empty means the failure strikes before any output.
	flapPrefix []aipapi.Event
	// flapAfterCommit, when true, lets real adapter events through until an
	// output-committed event is delivered, then fails on the next Recv. This
	// is the post-content case: the failure must create no replacement.
	flapAfterCommit bool

	mu                    sync.Mutex
	upstreamBodies        []string
	spoolNonEmptyObserved []bool
}

func (s *lplcServeSentinel) recordUpstream(body string) {
	entries, _ := os.ReadDir(s.spoolDir)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upstreamBodies = append(s.upstreamBodies, body)
	s.spoolNonEmptyObserved = append(s.spoolNonEmptyObserved, len(entries) > 0)
}

func (s *lplcServeSentinel) bodies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.upstreamBodies...)
}

func (s *lplcServeSentinel) spoolObserved() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bool(nil), s.spoolNonEmptyObserved...)
}

// lplcServeOverrideReader scripts the admin control-plane input: every A-leg
// snapshot reports the same active failover selector.
type lplcServeOverrideReader struct {
	selector string
}

func (r *lplcServeOverrideReader) Snapshot(_ context.Context, aLegID string) (routeoverride.State, error) {
	return routeoverride.State{
		ALegID:    aLegID,
		Active:    true,
		Selector:  r.selector,
		Revision:  1,
		UpdatedAt: time.Now(),
	}, nil
}

func lplcServeUpstream(t *testing.T, sentinel *lplcServeSentinel) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			sentinel.recordUpstream(string(b))
		}
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o"}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/responses") {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_prod\",\"model\":\"gpt-4o\",\"status\":\"in_progress\"}}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello-from-upstream\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_prod\",\"model\":\"gpt-4o\",\"status\":\"completed\"}}\n\ndata: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
}

func lplcServeConfig(spoolDir, upstreamBaseURL string, memorySpoolBytes int) string {
	return fmt.Sprintf(`server:
  address: "127.0.0.1:0"
  large_payload_fast_path:
    enabled: true
    threshold_bytes: 1024
    memory_spool_bytes: %d
    max_inflight_spool_bytes: 1048576
    max_semantic_fact_bytes: 16384
    spool_dir: %q

routing:
  max_attempts: 3
  default_route: "wire-a:gpt-4o"

continuity:
  in_memory: true
  store: memory

logging:
  level: error
  format: text

diagnostics:
  enabled: false

plugins:
  frontends:
    - id: openresponses
      enabled: true
      config: {}
  backends:
    - kind: custom-openai-responses-compatible
      id: wire-a
      enabled: true
      config:
        backend_prefix: wire-a
        base_url: %q
    - kind: custom-openai-responses-compatible
      id: wire-b
      enabled: true
      config:
        backend_prefix: wire-b
        base_url: %q
  features:
    - id: tool-call-repair
      enabled: false
`, memorySpoolBytes, spoolDir, upstreamBaseURL+"/v1", upstreamBaseURL+"/v1")
}

func lplcServeBuildHost(t *testing.T, cfgPath string, sentinel *lplcServeSentinel) *runtimebundle.Host {
	t.Helper()
	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
		RegistrySetup: func(reg *pluginreg.Registry) error {
			return reg.WrapLifecycleBackend("custom-openai-responses-compatible", func(orig pluginreg.LifecycleBackendFactory) pluginreg.LifecycleBackendFactory {
				return func(instanceID string, n yaml.Node, upstreamHTTP *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
					res, err := orig(instanceID, n, upstreamHTTP, deps)
					if err != nil {
						return pluginreg.BackendBuildResult{}, err
					}
					origOpenWire := res.Backend.OpenWire
					if origOpenWire != nil {
						res.Backend.OpenWire = func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
							n := sentinel.wireOpens.Add(1)
							inner, err := origOpenWire(ctx, req)
							if err != nil || inner == nil || !sentinel.flapOnce || n != 1 {
								return inner, err
							}
							return &lplcServeFlakyStream{
								inner:           inner,
								prefix:          sentinel.flapPrefix,
								fail:            aipapi.RecoverablePreOutputError(errors.New("lplc sentinel: pre-output flap")),
								failAfterCommit: sentinel.flapAfterCommit,
							}, nil
						}
					}
					origOpen := res.Backend.Open
					if origOpen != nil {
						res.Backend.Open = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
							sentinel.canonOpen.Add(1)
							return origOpen(ctx, call, cand)
						}
					}
					return res, nil
				}
			})
		},
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)
	// Drive the production A-leg route-override seam: the universal lane takes
	// this failover selector verbatim, so the accepted turn plans two real
	// wire attempts. Assessment, execution, frontend, and adapters stay
	// production; only the admin control-plane input is scripted.
	hostActiveExecutor(t, host).RouteOverrideReader = &lplcServeOverrideReader{
		selector: "wire-a:gpt-4o | wire-b:gpt-4o",
	}
	return host
}

func lplcServeFailoverBody() string {
	padding := strings.Repeat("x", 1500)
	return fmt.Sprintf(`{"model":"gpt-4o","stream":true,"store":false,"input":"hello %s"}`, padding)
}

func lplcServeRequest(host *runtimebundle.Host, body string) (int, string, http.Header) {
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AIP-Route", "wire-a:gpt-4o")
	rec := httptest.NewRecorder()
	host.HTTPHandler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), rec.Header()
}

// lplcServeRunRecoveryCase drives one accepted replay through a receive-time
// pre-output recoverable error and asserts the full serving contract.
func lplcServeRunRecoveryCase(t *testing.T, memorySpoolBytes int, expectSpill bool) {
	t.Helper()

	sentinel := &lplcServeSentinel{flapOnce: true}
	upstream := lplcServeUpstream(t, sentinel)
	defer upstream.Close()

	spoolDir := t.TempDir()
	sentinel.spoolDir = spoolDir
	cfgPath := filepath.Join(t.TempDir(), "lplc-recovery.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(lplcServeConfig(spoolDir, upstream.URL, memorySpoolBytes)), 0o600))

	host := lplcServeBuildHost(t, cfgPath, sentinel)
	ex := hostActiveExecutor(t, host)

	code, respBody, hdr := lplcServeRequest(host, lplcServeFailoverBody())

	require.Equal(t, http.StatusOK, code, "recovered response must succeed, got %q", respBody)
	require.Contains(t, respBody, "hello-from-upstream", "surviving execution must deliver upstream content")
	require.Contains(t, respBody, "response.completed", "surviving execution must reach its terminal event")
	require.Contains(t, respBody, "data: [DONE]")
	require.Equal(t, 1, strings.Count(respBody, "event: response.completed"),
		"exactly one terminal completion: the outcome comes from the surviving execution, not the abandoned attempt")
	require.Equal(t, 1, strings.Count(respBody, "data: [DONE]"))
	require.Equal(t, int32(2), sentinel.wireOpens.Load(), "receive-phase replacement must open exactly two wire streams")
	require.Equal(t, int32(0), sentinel.canonOpen.Load(), "recovery must never fall back to canonical execution")

	bodies := sentinel.bodies()
	var posts []string
	for _, b := range bodies {
		if b != "" {
			posts = append(posts, b)
		}
	}
	require.Len(t, posts, 2, "both wire attempts must reach the upstream adapter")
	require.Equal(t, posts[0], posts[1], "replacement must reopen exact bytes from the owned source")
	require.Contains(t, posts[1], `"model":"gpt-4o"`, "reopened body must carry the exact requested model")

	bodiesAll := sentinel.bodies()
	obsAll := sentinel.spoolObserved()
	require.Len(t, obsAll, len(bodiesAll))
	for i, b := range bodiesAll {
		if b == "" {
			continue // inventory probe outside any capture lifetime
		}
		require.Equal(t, expectSpill, obsAll[i],
			"spool backing observed during upstream POST %d must match the configured backing", i)
	}

	entries, err := os.ReadDir(spoolDir)
	require.NoError(t, err)
	require.Empty(t, entries, "the owned source must be reclaimed after the served response")

	aLegID := hdr.Get("X-AIP-A-Leg-Id")
	require.NotEmpty(t, aLegID, "wire response must carry the authoritative A-leg")
	attempts, err := ex.Store.LoadAttempts(context.Background(), aLegID)
	require.NoError(t, err)
	require.Len(t, attempts, 2, "abandoned plus surviving attempts must both be recorded")
}

// TestLargePayloadLifecycle_AcceptedReplayReceiveReplacement proves
// Requirements 1.1–1.3 and 3.3–3.4 for both replay backings: the accepted
// source survives a receive-time pre-output failure and serves the replacement
// open with identical bytes. Memory-backed replay keeps the source in RAM;
// disk-spilled replay stays readable for the replacement open; both are
// reclaimed afterwards.
func TestLargePayloadLifecycle_AcceptedReplayReceiveReplacement(t *testing.T) {
	t.Parallel()

	t.Run("memory", func(t *testing.T) {
		t.Parallel()
		lplcServeRunRecoveryCase(t, 32768, false)
	})
	t.Run("disk", func(t *testing.T) {
		t.Parallel()
		lplcServeRunRecoveryCase(t, 64, true)
	})
}

// TestLargePayloadLifecycle_CommittedOutputBlocksReplacement proves Requirement
// 1.4 at the serving level: once content is committed, a subsequent failure
// creates no replacement attempt.
func TestLargePayloadLifecycle_CommittedOutputBlocksReplacement(t *testing.T) {
	t.Parallel()

	sentinel := &lplcServeSentinel{
		flapOnce:        true,
		flapAfterCommit: true,
	}
	upstream := lplcServeUpstream(t, sentinel)
	defer upstream.Close()

	spoolDir := t.TempDir()
	sentinel.spoolDir = spoolDir
	cfgPath := filepath.Join(t.TempDir(), "lplc-committed.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(lplcServeConfig(spoolDir, upstream.URL, 32768)), 0o600))

	host := lplcServeBuildHost(t, cfgPath, sentinel)

	code, respBody, _ := lplcServeRequest(host, lplcServeFailoverBody())

	require.Equal(t, http.StatusOK, code, "committed prefix must still yield HTTP 200, got %q", respBody)
	require.Contains(t, respBody, "hello-from-upstream", "committed content must reach the client")
	require.Equal(t, int32(1), sentinel.wireOpens.Load(), "no replacement may open after content commitment")
	require.Equal(t, int32(0), sentinel.canonOpen.Load(), "post-content failure must not fall back to canonical execution")

	var posts int
	for _, b := range sentinel.bodies() {
		if b != "" {
			posts++
		}
	}
	require.Equal(t, 1, posts, "only the initial attempt may reach the upstream adapter")

	entries, err := os.ReadDir(spoolDir)
	require.NoError(t, err)
	require.Empty(t, entries, "the owned source must still be reclaimed after a post-content failure")
}
