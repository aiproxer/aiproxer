package stdhttp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
)

// TestStandardMiddlewareMountParity_ComposeStandardHTTPRouteSetAndStack freezes the
// generation-host route set and outer middleware behavior that Built→capability
// migration (tasks 3.1–3.5) must preserve. Assertions use deterministic HTTP
// outcomes only — no unstable internal stack snapshots.
func TestStandardMiddlewareMountParity_ComposeStandardHTTPRouteSetAndStack(t *testing.T) {
	t.Parallel()
	ps := newStdProcess(t)
	frontends := []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
		{ID: "openai-legacy", Enabled: true},
		{ID: "anthropic", Enabled: true},
		{ID: "gemini", Enabled: true},
	}
	cand := stubPlaneConfig(t, "parity", "parity-ok", "parity:stub-default", frontends)
	cand.Identity.Downstream.Server = identity.FieldPolicy{Mode: identity.ModeCustom, Value: "ParityGW"}

	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	h := bundle.Handler()
	if h == nil {
		t.Fatal("nil composed handler")
	}

	// Mount coverage: request-plane routes must not 404. Exact auth/decode status
	// may vary; StatusNotFound is the only hard failure for "mounted".
	for _, tc := range []struct {
		name string
		req  *http.Request
	}{
		{"healthz", httptest.NewRequest(http.MethodGet, "/healthz", nil)},
		{"models", httptest.NewRequest(http.MethodGet, "/v1/models", nil)},
		{"openai_responses", httptest.NewRequest(http.MethodPost, "/v1/responses", nil)},
		{"openai_legacy", httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)},
		{"anthropic", httptest.NewRequest(http.MethodPost, "/v1/messages", nil)},
		{"gemini", httptest.NewRequest(http.MethodPost, "/v1beta/models/m:generateContent", nil)},
	} {
		t.Run("mounted_"+tc.name, func(t *testing.T) {
			t.Parallel()
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, tc.req)
			if rr.Code == http.StatusNotFound {
				t.Fatalf("%s must be mounted on ComposeStandardHTTP graph", tc.name)
			}
		})
	}

	// Management reload routes stay outside the swappable generation request plane.
	for _, path := range []string{"/admin/config/reload", "/admin/config/status"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s must stay unmounted on request plane: %d", path, rr.Code)
		}
	}

	// Outer middleware: DownstreamServerMiddleware applies Server policy on responses.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if got := rr.Header().Get("Server"); got != "ParityGW" {
		t.Fatalf("Server=%q want ParityGW (outer DownstreamServerMiddleware)", got)
	}
}
