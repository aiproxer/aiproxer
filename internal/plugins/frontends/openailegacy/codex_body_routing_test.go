package openailegacy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	front "github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestIntegration_openaiCodexURIReasoningEffortOverridesBody proves that a reasoning_effort
// URI param on the model selector OVERRIDES the Chat Completions body's reasoning_effort
// field. URI params are explicit routing directives and take precedence over per-request
// body settings.
func TestIntegration_openaiCodexURIReasoningEffortOverridesBody(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var captured aipapi.Call
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(42)
	ex.Backends = map[string]execbackend.Backend{
		"openai-codex": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityReasoning, aipapi.CapabilityTools),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				captured = call
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "ok"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	testkit.WireConformanceExecutorSecureSession(t, ex)

	h := &front.Handler{Exec: ex, DefaultRouteSelector: "openai-codex:gpt-5.5", RoutePrefixes: routeselect.NewPrefixSet([]string{"openai-codex"})}
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"openai-codex:openai/gpt-5.4-mini?reasoning_effort=xhigh","reasoning_effort":"medium","stream":false,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := testkit.IntegrationHTTPClient(nil).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d body %s", res.StatusCode, string(b))
	}

	if captured.Options.ReasoningEffort != "xhigh" {
		t.Fatalf("call.Options.ReasoningEffort %q, want %q (URI param must override body field)", captured.Options.ReasoningEffort, "xhigh")
	}
}
