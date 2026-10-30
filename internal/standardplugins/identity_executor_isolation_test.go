package standardplugins_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
	refresponses "github.com/aiproxer/aiproxer/internal/refbackend/openairesponses"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

type capturedHeaders struct {
	mu       sync.Mutex
	uas      []string
	uaKeys   []bool
	referers []string
	titles   []string
}

func (c *capturedHeaders) observe(r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, present := r.Header["User-Agent"]
	c.uaKeys = append(c.uaKeys, present)
	c.uas = append(c.uas, r.Header.Get("User-Agent"))
	c.referers = append(c.referers, r.Header.Get("HTTP-Referer"))
	c.titles = append(c.titles, r.Header.Get("X-OpenRouter-Title"))
}

func (c *capturedHeaders) snapshot() (uas []string, uaKeys []bool, referers, titles []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.uas...), append([]bool(nil), c.uaKeys...), append([]string(nil), c.referers...), append([]string(nil), c.titles...)
}

func buildIdentityBackend(t *testing.T, factoryID, yamlText string, client *http.Client, g identity.Config, keys standardplugins.UpstreamAPIKeys) execbackend.Backend {
	t.Helper()
	if err := identity.Validate(&g); err != nil {
		t.Fatal(err)
	}
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, keys); err != nil {
		t.Fatal(err)
	}
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(yamlText), &node); err != nil {
		t.Fatal(err)
	}
	be, err := reg.BuildBackend(factoryID, node, client, pluginreg.BackendFactoryDeps{Identity: g})
	if err != nil {
		t.Fatalf("BuildBackend %s: %v", factoryID, err)
	}
	return be
}

func newIdentityIsolationExecutor(t *testing.T, backends map[string]execbackend.Backend) *runtime.Executor {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(42)
	ex.Backends = backends
	testkit.WireConformanceExecutorSecureSession(t, ex)
	return ex
}

func wrapOpenAsRecoverable(open func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error)) func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
	return func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		es, err := open(ctx, call, cand)
		if es != nil {
			_ = es.Close()
		}
		if err != nil {
			return nil, aipapi.RecoverablePreOutputError(err)
		}
		return nil, aipapi.RecoverablePreOutputError(errors.New("identity isolation: forced recoverable pre-output"))
	}
}

func isolationCall(selector, clientUA string) *aipapi.Call {
	return &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "id-iso"},
		Route:   aipapi.RouteIntent{Selector: selector},
		Invocation: aipapi.Invocation{
			Operation:       aipapi.OperationOpenAIResponses,
			DeliveryMode:    aipapi.DeliveryModeNonStreaming,
			TransportMode:   aipapi.TransportModeNonStreaming,
			ClientUserAgent: clientUA,
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
}

// ID-147-FO: ordered failover across two approved hosted backends with different UA policies.
func TestIdentityExecutor_ID147_orderedFailoverIsolatesUserAgent(t *testing.T) {
	t.Parallel()

	var first, second capturedHeaders
	failInner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"down"}}`, http.StatusServiceUnavailable)
	})
	okInner := refresponses.NewHandler(refresponses.Config{})

	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first.observe(r)
		failInner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv1.Close)
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		second.observe(r)
		okInner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv2.Close)

	be1 := buildIdentityBackend(
		t, openairesponses.ID,
		"base_url: "+srv1.URL+"/v1\n",
		srv1.Client(),
		identity.Config{Upstream: identity.UpstreamPolicy{
			UserAgent: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "FailoverFirst/1"},
		}},
		standardplugins.UpstreamAPIKeys{OpenAI: []string{"sk-a"}},
	)
	be1.Open = wrapOpenAsRecoverable(be1.Open)

	be2 := buildIdentityBackend(
		t, openairesponses.ID,
		"base_url: "+srv2.URL+"/v1\nidentity:\n  user_agent:\n    mode: drop\n",
		srv2.Client(),
		identity.Config{Upstream: identity.UpstreamPolicy{
			UserAgent: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "GlobalMustNotBleed/9"},
		}},
		standardplugins.UpstreamAPIKeys{OpenAI: []string{"sk-b"}},
	)

	ex := newIdentityIsolationExecutor(t, map[string]execbackend.Backend{
		"first":  be1,
		"second": be2,
	})
	s, err := ex.Execute(context.Background(), isolationCall("first:m|second:m", "ClientPassthrough/0"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = aipapi.Collect(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}

	uas1, keys1, _, _ := first.snapshot()
	uas2, keys2, _, _ := second.snapshot()
	if len(uas1) == 0 {
		t.Fatal("first backend received no HTTP request")
	}
	if len(uas2) == 0 {
		t.Fatal("second backend received no HTTP request")
	}
	for i, ua := range uas1 {
		if !keys1[i] || ua != "FailoverFirst/1" {
			t.Fatalf("first UA[%d] present=%v value=%q want FailoverFirst/1", i, keys1[i], ua)
		}
		if ua == "GlobalMustNotBleed/9" || ua == "ClientPassthrough/0" {
			t.Fatalf("first UA bled unexpected value %q", ua)
		}
	}
	for i := range uas2 {
		if keys2[i] {
			t.Fatalf("second drop policy leaked User-Agent=%q", uas2[i])
		}
	}
}

// ID-147-PR: parallel race two candidates with different UA policies.
// Scope: concurrent Open identity isolation on both legs (winner+loser both observed).
// Losing-branch cancel semantics: covered by runtime parallel_race_test; not re-proven here.
func TestIdentityExecutor_ID147_parallelRaceIsolatesUserAgent(t *testing.T) {
	t.Parallel()

	var aCap, bCap capturedHeaders
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseBoth := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseBoth)

	handler := func(captured *capturedHeaders) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			captured.observe(r)
			arrived <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			refresponses.NewHandler(refresponses.Config{}).ServeHTTP(w, r)
		})
	}

	srvA := httptest.NewServer(handler(&aCap))
	t.Cleanup(srvA.Close)
	srvB := httptest.NewServer(handler(&bCap))
	t.Cleanup(srvB.Close)

	beA := buildIdentityBackend(
		t, openairesponses.ID,
		"base_url: "+srvA.URL+"/v1\n",
		srvA.Client(),
		identity.Config{Upstream: identity.UpstreamPolicy{
			UserAgent: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "ParallelA/1"},
		}},
		standardplugins.UpstreamAPIKeys{OpenAI: []string{"sk-a"}},
	)
	beB := buildIdentityBackend(
		t, openairesponses.ID,
		"base_url: "+srvB.URL+"/v1\n",
		srvB.Client(),
		identity.Config{Upstream: identity.UpstreamPolicy{
			UserAgent: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "ParallelB/2"},
		}},
		standardplugins.UpstreamAPIKeys{OpenAI: []string{"sk-b"}},
	)

	ex := newIdentityIsolationExecutor(t, map[string]execbackend.Backend{
		"leg-a": beA,
		"leg-b": beB,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	done := make(chan struct{})
	var (
		col                 aipapi.Collected
		execErr, collectErr error
	)
	go func() {
		defer close(done)
		var s aipapi.EventStream
		s, execErr = ex.Execute(ctx, isolationCall("leg-a:m!leg-b:m", ""))
		if execErr != nil {
			return
		}
		col, collectErr = aipapi.Collect(ctx, s)
	}()

	deadline := time.After(5 * time.Second)
	for range 2 {
		select {
		case <-arrived:
		case <-deadline:
			cancel()
			releaseBoth()
			t.Fatal("timed out waiting for both parallel legs to arrive before release")
		case <-done:
			cancel()
			releaseBoth()
			t.Fatalf("execute finished before both legs arrived: execErr=%v collectErr=%v", execErr, collectErr)
		}
	}
	releaseBoth()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timed out waiting for execute/collect after release")
	}
	if execErr != nil {
		t.Fatal(execErr)
	}
	if collectErr != nil {
		t.Fatal(collectErr)
	}
	if col.Text.String() == "" {
		t.Fatal("expected winner text")
	}

	uasA, keysA, _, _ := aCap.snapshot()
	uasB, keysB, _, _ := bCap.snapshot()
	if len(uasA) == 0 || len(uasB) == 0 {
		t.Fatalf("both legs must be observed: leg-a=%d leg-b=%d", len(uasA), len(uasB))
	}
	for i, ua := range uasA {
		if !keysA[i] || ua != "ParallelA/1" {
			t.Fatalf("leg-a UA[%d]=%q present=%v", i, ua, keysA[i])
		}
	}
	for i, ua := range uasB {
		if !keysB[i] || ua != "ParallelB/2" {
			t.Fatalf("leg-b UA[%d]=%q present=%v (must not bleed ParallelA)", i, ua, keysB[i])
		}
	}
}

// ID-147-CR: pre-output credential retry keeps the same backend identity on each wire attempt.
func TestIdentityExecutor_ID147_credentialRetryPreservesBackendIdentity(t *testing.T) {
	t.Parallel()

	var captured capturedHeaders
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.observe(r)
		if n.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"invalid_api_key","type":"invalid_request_error"}}`)
			return
		}
		refresponses.NewHandler(refresponses.Config{}).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	be := buildIdentityBackend(t, openairesponses.ID, fmt.Sprintf(`
base_url: %s/v1
`, srv.URL), srv.Client(), identity.Config{
		Upstream: identity.UpstreamPolicy{
			UserAgent: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "CredRetryUA/1"},
		},
	}, standardplugins.UpstreamAPIKeys{OpenAI: []string{"sk-bad", "sk-good"}})

	ex := newIdentityIsolationExecutor(t, map[string]execbackend.Backend{"oa": be})
	s, err := ex.Execute(context.Background(), isolationCall("oa:m", "ClientMustNotAppear/9"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = aipapi.Collect(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	uas, keys, _, _ := captured.snapshot()
	if len(uas) < 2 {
		t.Fatalf("want >=2 credential attempts, got %d uas=%v", len(uas), uas)
	}
	for i, ua := range uas {
		if !keys[i] || ua != "CredRetryUA/1" {
			t.Fatalf("attempt[%d] UA present=%v value=%q want CredRetryUA/1", i, keys[i], ua)
		}
	}
}

// ID-147-PO: post-output failure must not open a failover backend (identity-bearing).
// Orchestration invariant covered elsewhere; this adds identity-scoped open counting.
func TestIdentityExecutor_ID147_noFailoverAfterOutputPreservesIdentityChoice(t *testing.T) {
	t.Parallel()

	var winnerCap capturedHeaders
	var failoverOpens atomic.Int32

	srvWin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		winnerCap.observe(r)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "ResponseWriter does not implement http.Flusher", http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\"}}\n\n")
		fl.Flush()
		_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\n")
		fl.Flush()
		// Truncate after committed output (no completed event).
	}))
	t.Cleanup(srvWin.Close)

	beWin := buildIdentityBackend(
		t, openairesponses.ID,
		"base_url: "+srvWin.URL+"/v1\n",
		srvWin.Client(),
		identity.Config{Upstream: identity.UpstreamPolicy{
			UserAgent: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "WinnerUA/1"},
		}},
		standardplugins.UpstreamAPIKeys{OpenAI: []string{"sk-win"}},
	)
	beFail := execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			failoverOpens.Add(1)
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: "should-not"},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}

	ex := newIdentityIsolationExecutor(t, map[string]execbackend.Backend{
		"win":  beWin,
		"fail": beFail,
	})
	call := isolationCall("win:m|fail:m", "")
	call.Invocation.DeliveryMode = aipapi.DeliveryModeStreaming
	call.Invocation.TransportMode = aipapi.TransportModeStreaming
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var sawText bool
	for {
		ev, rerr := s.Recv(ctx)
		if rerr != nil {
			if sawText {
				if aipapi.IsRecoverablePreOutput(rerr) {
					t.Fatalf("post-output error must not be recoverable-pre-output: %v", rerr)
				}
				break
			}
			if errors.Is(rerr, io.EOF) {
				t.Fatal("EOF before text")
			}
			t.Fatalf("recv: %v", rerr)
		}
		if ev.Kind == aipapi.EventTextDelta && strings.Contains(ev.Delta, "x") {
			sawText = true
		}
	}
	if !sawText {
		t.Fatal("expected committed text")
	}
	if failoverOpens.Load() != 0 {
		t.Fatalf("failover backend opened %d times after output", failoverOpens.Load())
	}
	uas, keys, _, _ := winnerCap.snapshot()
	if len(uas) == 0 || !keys[0] || uas[0] != "WinnerUA/1" {
		t.Fatalf("winner UA=%v keys=%v", uas, keys)
	}
}
