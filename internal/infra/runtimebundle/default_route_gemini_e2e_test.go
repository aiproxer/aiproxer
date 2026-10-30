package runtimebundle_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// TestDefaultRoute_GeminiRequestUsesTheDefaultRoute proves the Gemini frontend
// routes a client that named no route to the default route, and that the
// backend receives the model id it published.
//
// The frontend resolves its route from the URL-path model, so the route's
// origin is established where the route is chosen. The request then runs the
// default route, and the model the backend receives is the id it serves, not
// the registry's canonical id.
func TestDefaultRoute_GeminiRequestUsesTheDefaultRoute(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "gemini-test-key")

	var (
		generateCalls atomic.Int32
		lastModelPath atomic.Pointer[string]
		published     atomic.Pointer[string]
	)
	published.Store(new("gemini-2.0-flash"))

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1beta/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"models":[{"name":"models/%s","displayName":"Flash"}]}`, *published.Load())
			return
		}
		if strings.Contains(r.URL.Path, ":streamGenerateContent") {
			generateCalls.Add(1)
			path := r.URL.Path
			lastModelPath.Store(&path)
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}],\"role\":\"model\"}}],\"modelVersion\":\"gemini-2.0-flash\",\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2}}\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	cfg := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
routing:
  max_attempts: 1
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
    - id: gemini
      enabled: true
      config: {}
  backends:
    - kind: gemini
      id: gemini-backend
      enabled: true
      config:
        base_url: %q
`, upstream.URL)

	cfgPath := filepath.Join(t.TempDir(), "gemini-default-route-e2e.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	// The operator read model: the route the operator is told about.
	snap, err := runtimebundle.InspectRoutes(t.Context(), runtimebundle.InspectInput{
		ConfigPath: cfgPath,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	})
	if err != nil {
		t.Fatalf("InspectRoutes: %v", err)
	}
	const wantRoute = "gemini-backend:gemini-2.0-flash"
	if snap.EffectiveDefaultRoute != wantRoute {
		t.Fatalf("advertised default route = %q want %q", snap.EffectiveDefaultRoute, wantRoute)
	}
	// The Gemini backend kind has a canonical default model, so its default
	// route is that model, not a discovered one.
	if snap.EffectiveDefaultRouteSource != "canonical_default_model" {
		t.Fatalf("advertised default route source = %q", snap.EffectiveDefaultRouteSource)
	}

	// The upstream withdraws the model the generation's default route names.
	published.Store(new("gemini-2.5-pro"))
	registry := runtimebundle.HostModelRegistryRuntime(host)
	if registry == nil {
		t.Fatal("expected the active generation to own a model registry runtime")
	}
	registry.RunRefresh(t.Context())

	// A URL-path model with no route prefix falls back to the default route.
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/plain-model:generateContent", strings.NewReader(
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	host.HTTPHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if generateCalls.Load() == 0 {
		t.Fatal("the derived default route served no request")
	}
	path := lastModelPath.Load()
	if path == nil {
		t.Fatal("the backend received no request")
	}
	if !strings.Contains(*path, "/v1beta/models/gemini-2.0-flash:") {
		t.Fatalf("upstream path = %q want the model id the backend published", *path)
	}
	if strings.Contains(*path, "google/") {
		t.Fatalf("upstream path = %q: the canonical id must not be sent as the wire model", *path)
	}
}
