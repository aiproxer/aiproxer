//go:build integration

package runtimebundle_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	coreruntime "github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type promptContinuationAuth struct{}

func (promptContinuationAuth) Authenticate(context.Context, sdkauth.InboundCallMeta) (sdkauth.Decision, error) {
	return sdkauth.Decision{Outcome: sdkauth.OutcomeAllow, Principal: execview.PrincipalView{ID: "local-dev"}, Scope: &scope.PrincipalScopeView{Origin: scope.OriginClient, SubjectKind: scope.SubjectLocal, PrincipalID: scope.Known("local-dev")}}, nil
}

// Exercise real HTTP admission, generation publication and backend Open/OpenWire,
// not just the static census: steering must win before first-turn wire execution.
func TestModelSystemPrompt_HostReloadFrozenDecisionsAndWireFallback(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o"},{"id":"other"}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			var request struct {
				Stream bool `json:"stream"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			if !request.Stream {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"id":"chatcmpl-bootstrap","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"host-live"},"finish_reason":"stop"}]}`)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-bootstrap\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"host-live\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	base := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
  large_payload_fast_path:
    enabled: true
    threshold_bytes: 1024
    memory_spool_bytes: 32768
    max_inflight_spool_bytes: 1048576
    max_semantic_fact_bytes: 16384
    spool_dir: %q
routing:
  max_attempts: 3
  default_route: "wire-backend:gpt-4o"
continuity:
  in_memory: true
  store: memory
logging:
  level: debug
  format: text
diagnostics:
  enabled: false
observability:
  metrics:
    enabled: true
plugins:
  frontends:
    - id: openai-legacy
      enabled: true
      config: {}
  backends:
    - kind: custom-openai-legacy-compatible
      id: wire-backend
      enabled: true
      config:
        backend_prefix: wire-backend
        base_url: %q
  features:
    - id: tool-call-repair
      enabled: false
`, t.TempDir(), upstream.URL+"/v1")
	feature := func(pattern, text string) string {
		return fmt.Sprintf("    - id: model-system-prompt\n      enabled: true\n      config:\n        rules:\n          - id: first\n            model_pattern: %q\n            append: %q\n", pattern, text)
	}
	path := filepath.Join(t.TempDir(), "host.yaml")
	require.NoError(t, os.WriteFile(path, []byte(base), 0o600))
	var wire, canonical atomic.Int32
	var captured atomic.Pointer[aipapi.Call]
	var normalLogs syncLogBuffer
	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath: path, Mandatory: aipsdk.StandardDistributionRequirements(),
		LogWriter: &normalLogs, HandlerComposer: stdhttp.ComposeStandardHTTP,
		RegistrySetup: func(reg *pluginreg.Registry) error {
			return reg.WrapLifecycleBackend("custom-openai-legacy-compatible", func(orig pluginreg.LifecycleBackendFactory) pluginreg.LifecycleBackendFactory {
				return func(id string, node yaml.Node, client *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
					res, err := orig(id, node, client, deps)
					if err != nil {
						return res, err
					}
					open, openWire := res.Backend.Open, res.Backend.OpenWire
					res.Backend.Open = func(ctx context.Context, call aipapi.Call, candidate routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
						canonical.Add(1)
						clone := aipapi.CloneCall(call)
						captured.Store(&clone)
						return open(ctx, call, candidate)
					}
					res.Backend.OpenWire = func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
						wire.Add(1)
						return openWire(ctx, req)
					}
					return res, nil
				}
			})
		},
	})
	require.NoError(t, err)
	hostServeCleanup(t, host)
	type leg struct{ id, session, token string }
	histories := make(map[string][]map[string]string)
	requests := 0
	const original = "ORIGINAL-735-system\nexact bytes"
	const developer = "ORIGINAL-735-developer\tunchanged"
	send := func(model string, prior leg, large bool) leg {
		requests++
		padding := fmt.Sprintf("CLIENT-735-REQUEST-%d", requests)
		if large {
			padding += strings.Repeat("x", 1500)
		}
		messages := append([]map[string]string(nil), histories[prior.id]...)
		if prior.id == "" {
			messages = []map[string]string{{"role": "system", "content": original}, {"role": "system", "content": developer}}
		}
		messages = append(messages, map[string]string{"role": "user", "content": padding})
		raw, err := json.Marshal(map[string]any{"model": model, "stream": true, "messages": messages})
		require.NoError(t, err)
		body := string(raw)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if prior.id != "" {
			req.Header.Set("X-AIP-Session-ID", prior.session)
			req.Header.Set("X-AIP-Resume-Token", prior.token)
		}
		rec := httptest.NewRecorder()
		host.HTTPHandler().ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, "host must remain usable")
		require.Contains(t, rec.Body.String(), "host-live")
		require.NotContains(t, rec.Body.String(), "PRIVATE-735-")
		out := leg{rec.Header().Get("X-AIP-A-Leg-ID"), rec.Header().Get("X-AIP-Session-ID"), rec.Header().Get("X-AIP-Resume-Token")}
		require.NotEmpty(t, out.id)
		if prior.id != "" {
			require.Equal(t, prior.id, out.id)
		} else {
			require.NotEmpty(t, out.token)
		}
		histories[out.id] = messages
		if call := captured.Load(); call != nil && call.Session.ALegID == out.id {
			require.Len(t, call.Messages, len(messages), "backend must receive only this A-leg's client history")
			for i, msg := range messages {
				require.Equal(t, msg["content"], call.Messages[i].Parts[0].Text)
			}
		}
		return out
	}
	reload := func(raw string) sdkreload.Result {
		manager := runtimebundle.HostManager(host)
		prior := manager.Active()
		candidate := filepath.Join(t.TempDir(), "candidate.yaml")
		require.NoError(t, os.WriteFile(candidate, []byte(raw), 0o600))
		require.NoError(t, replaceTestFile(candidate, path))
		result := host.Reload(t.Context(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI, AcceptedAt: time.Now().UTC(), SafeActor: "bootstrap-test"})
		if result.Category == sdkreload.ResultPublished {
			// Wait for actual retirement; publication alone could leave cleanup pending.
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			_, err := manager.RetireGeneration(ctx, prior)
			require.True(t, err == nil || errors.Is(err, runtimehost.ErrAlreadyClosed), "retirement must finish")
			require.Equal(t, runtimehost.GenClosed, prior.Lifecycle())
		}
		return result
	}
	assertInstructions := func(hidden string) {
		call := captured.Load()
		require.NotNil(t, call)
		var want []aipapi.Message
		if hidden != "" {
			want = []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart(hidden)}}}
		}
		require.Equal(t, want, call.Instructions)
		require.NotEmpty(t, call.Messages)
		require.Equal(t, aipapi.Message{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart(original)}}, call.Messages[0])
		// Developer-role byte preservation is covered at the canonical runtime seam.
		require.Equal(t, aipapi.Message{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart(developer)}}, call.Messages[1])
	}
	// The pre-feature leg has real B-leg history, not fabricated attempt rows.
	preexisting := send("gpt-4o", leg{}, false)
	require.Equal(t, int32(1), canonical.Load())
	require.Equal(t, sdkreload.ResultPublished, reload(base+feature("^gpt-4o$", "PRIVATE-735-generation-one-e8c1f")).Category)
	ex := hostActiveExecutor(t, host)
	producer := ex.ConversationBootstrap
	require.NotNil(t, producer)
	var resolves atomic.Int32
	ex.ConversationBootstrap = func(ctx context.Context, id string, accepted *aipapi.Call, resolve func() (coreruntime.InitialModelIntent, error)) (string, error) {
		return producer(ctx, id, accepted, func() (coreruntime.InitialModelIntent, error) {
			resolves.Add(1)
			return resolve()
		})
	}
	matched := send("gpt-4o", leg{}, true)
	require.Zero(t, wire.Load(), "enabled bootstrap must prevent first-turn wire bypass")
	require.Equal(t, int32(2), canonical.Load(), "one canonical fallback Open")
	require.Equal(t, int32(1), resolves.Load(), "one atomic decision callback on fallback")
	assertInstructions("PRIVATE-735-generation-one-e8c1f")
	send("wire-backend:other", matched, true)
	assertInstructions("PRIVATE-735-generation-one-e8c1f")
	send("wire-backend:other", matched, true)
	assertInstructions("PRIVATE-735-generation-one-e8c1f")
	require.Equal(t, int32(1), resolves.Load(), "later model changes must not evaluate the decision again")
	// The real continuation frontend stores accepted client input, not the
	// projected backend call. Materializing its parent must not duplicate steering.
	var responseID string
	continuations := aipcont.NewMemoryStore()
	continuationHandler := openresponses.NewHandler(openresponses.HandlerConfig{AllowUnauthenticated: true, Authorizer: promptContinuationAuth{}, Executor: ex, ContinuationStore: continuations})
	for turn := range 2 {
		payload := map[string]any{"model": "wire-backend:gpt-4o", "input": fmt.Sprintf("CONTINUATION-735-client-%d", turn), "store": true}
		if responseID != "" {
			payload["previous_response_id"] = responseID
		}
		raw, err := json.Marshal(payload)
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		continuationHandler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String()+normalLogs.String())
		require.NotContains(t, rec.Body.String(), "PRIVATE-735-")
		var response struct {
			ID string `json:"id"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
		require.NotEmpty(t, response.ID)
		responseID = response.ID
		stored, err := continuations.Get(t.Context(), aipcont.Scope{PrincipalID: "local-dev"}, aipcont.ResponseID(responseID))
		require.NoError(t, err)
		truth, err := json.Marshal(stored)
		require.NoError(t, err)
		require.NotContains(t, string(truth), "PRIVATE-735-")
		require.Contains(t, string(truth), fmt.Sprintf("CONTINUATION-735-client-%d", turn))
		backend, err := json.Marshal(captured.Load())
		require.NoError(t, err)
		require.Equal(t, 1, strings.Count(string(backend), "PRIVATE-735-generation-one-e8c1f"))
		require.NotContains(t, string(backend), "CLIENT-735-REQUEST-")
		require.Contains(t, string(backend), "CONTINUATION-735-client-0")
		if turn > 0 {
			require.Contains(t, string(backend), "CONTINUATION-735-client-1")
		}
	}
	noMatch := send("wire-backend:other", leg{}, false)
	assertInstructions("")
	ambiguous := send("wire-backend:gpt-4o!wire-backend:other", leg{}, false)
	assertInstructions("")
	send("gpt-4o", preexisting, false)
	assertInstructions("")
	require.Equal(t, int32(5), resolves.Load(), "preexisting allocation must skip resolver; standalone continuation requests own independent A-legs")
	cases := []struct {
		name    string
		leg     leg
		outcome conversationview.BootstrapOutcome
		hidden  string
	}{
		{"matched", matched, conversationview.BootstrapMatched, "PRIVATE-735-generation-one-e8c1f"},
		{"no-match", noMatch, conversationview.BootstrapNoMatch, ""},
		{"ambiguous", ambiguous, conversationview.BootstrapAmbiguousSkip, ""},
		{"preexisting", preexisting, conversationview.BootstrapPreexistingSkip, ""},
	}
	store := runtimebundle.HostProcess(host).StandardFeatures.ConversationStore()
	bootstrap, ok := store.(conversationview.BootstrapStore)
	require.True(t, ok)
	checkFrozen := func() {
		for _, tc := range cases {
			send("wire-backend:other", tc.leg, true)
			assertInstructions(tc.hidden)
			result, err := bootstrap.BootstrapSteering(t.Context(), tc.leg.id, "model-system-prompt", func() (conversationview.BootstrapDecision, error) {
				t.Error("completed decision evaluated again: " + tc.name)
				return conversationview.BootstrapDecision{}, nil
			})
			require.NoError(t, err)
			require.True(t, result.Reused, tc.name)
			require.Equal(t, tc.outcome, result.Completion.Outcome, tc.name)
		}
	}
	require.Equal(t, sdkreload.ResultPublished, reload(base+feature(".*", "generation two")).Category)
	require.NotSame(t, ex, hostActiveExecutor(t, host))
	checkFrozen()
	send("wire-backend:other", leg{}, true)
	assertInstructions("generation two")
	lastGood := hostActiveExecutor(t, host)
	require.NotEqual(t, sdkreload.ResultPublished, reload(base+feature("[", "invalid candidate")).Category)
	require.Same(t, lastGood, hostActiveExecutor(t, host), "invalid candidate cannot replace last-good generation")
	send("gpt-4o", leg{}, true)
	assertInstructions("generation two")
	require.Equal(t, sdkreload.ResultPublished, reload(base).Category)
	removed := hostActiveExecutor(t, host)
	require.Nil(t, removed.ConversationBootstrap)
	assessor, ok := removed.LargeBodyAssessor.(*coreruntime.ProductionLargeBodyAssessor)
	require.True(t, ok)
	require.False(t, assessor.AuthorityGate.Census.Ports.SteeringWriterFactoryOccupied)
	checkFrozen()
	require.Zero(t, wire.Load(), "persisted resumed authority still blocks wire after producer removal")
	send("gpt-4o", leg{}, true)
	require.Equal(t, int32(1), wire.Load(), "fresh baseline remains wire-live after removal")
	// Removal must preserve empty markers too, even across re-enablement.
	require.Equal(t, sdkreload.ResultPublished, reload(base+feature(".*", "generation three")).Category)
	checkFrozen()
	require.Equal(t, int32(1), wire.Load())
	require.NotContains(t, normalLogs.String(), "PRIVATE-735-")
	require.NotContains(t, normalLogs.String(), "CLIENT-735-REQUEST-")
	require.NotContains(t, normalLogs.String(), "CONTINUATION-735-client-")
	require.NotContains(t, normalLogs.String(), "generation two")
	require.NotContains(t, normalLogs.String(), "generation three")
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("PRIVATE-735-generation-one-e8c1f")))
	require.NotContains(t, normalLogs.String(), digest)
	metrics, err := runtimebundle.HostProcess(host).Metrics.Registry.Gather()
	require.NoError(t, err)
	metricText := fmt.Sprintf("%v", metrics)
	require.NotContains(t, metricText, "PRIVATE-735-")
	require.NotContains(t, metricText, "CLIENT-735-REQUEST-")
	require.NotContains(t, metricText, "CONTINUATION-735-client-")
	require.NotContains(t, metricText, digest)
}
