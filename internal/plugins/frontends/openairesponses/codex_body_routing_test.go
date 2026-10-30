package openairesponses_test

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
	front "github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestIntegration_openaiCodexBodyModelOverridesRouteWithReasoningEffort proves that a
// client can manually route to the openai-codex backend with an arbitrary model and URI
// params by putting "openai-codex:<model>?reasoning_effort=<level>" in the request body
// model field. The selector must override the configured default route, and the
// reasoning_effort URI param must be converted into call.Options.ReasoningEffort (the
// canonical data structure the openai-codex backend reads to set reasoning effort).
func TestIntegration_openaiCodexBodyModelOverridesRouteWithReasoningEffort(t *testing.T) {
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
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
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
	mux.Handle("/v1/responses", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/v1/responses",
		strings.NewReader(`{"model":"openai-codex:gpt-5.5?reasoning_effort=low","input":"x"}`))
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

	if want := "openai-codex:gpt-5.5?reasoning_effort=low"; captured.Route.Selector != want {
		t.Fatalf("route selector %q, want %q", captured.Route.Selector, want)
	}
	if captured.Options.ReasoningEffort != "low" {
		t.Fatalf("call.Options.ReasoningEffort %q, want %q", captured.Options.ReasoningEffort, "low")
	}
}
