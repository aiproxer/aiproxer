package stdhttp

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/openairesponses"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"gopkg.in/yaml.v3"
)

// TestStandardWiring_openaiResponses_runtimeBundle_roundTrip exercises the standard
// distribution wiring path: registry bundle install, runtime assembly, bundled OpenAI
// Responses frontend, and a single enabled backend instance talking to a local refbackend.
// Covers introduce-hexagonal-architecture tasks 7.1 and 7.2 (coexistence of baseline
// migration classes in one runnable path).
func TestStandardWiring_openaiResponses_runtimeBundle_roundTrip(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	reg := testRegistryWithStdBundle(t)

	backYAML := fmt.Sprintf(`base_url: %q
models:
  source: inline
  items:
    - canonical_id: openai/gpt-4o-mini
      native_id: gpt-4o-mini
`, srv.URL+"/v1")
	var backNode yaml.Node
	if err := yaml.Unmarshal([]byte(backYAML), &backNode); err != nil {
		t.Fatalf("backend yaml: %v", err)
	}

	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind:    "openai-responses",
				ID:      "oai-upstream-int",
				Enabled: true,
				Config:  backNode,
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}

	_, cand := compileTestCandidate(t, cfg, reg)
	if len(cand.Executor().Backends) != 1 {
		t.Fatalf("backends: got %d want 1", len(cand.Executor().Backends))
	}
	if _, ok := cand.Executor().Backends["oai-upstream-int"]; !ok {
		t.Fatal("expected backend instance oai-upstream-int")
	}

	route := config.EffectiveDefaultRouteSelector(cfg, standardplugins.DefaultWireModel)
	if want := "oai-upstream-int:gpt-4o-mini"; route != want {
		t.Fatalf("effective route %q want %q", route, want)
	}

	mux := http.NewServeMux()
	if err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux: mux,
		Frontends: HTTPFrontendInput{
			Executor:             cand.Executor(),
			DefaultRouteSelector: route,
			Plugins:              []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
			RoutePrefixes:        cand.RoutePrefixes(),
			MaxRequestBodyBytes:  0,
			Registry:             reg,
		},
	}); err != nil {
		t.Fatalf("MountBundledFrontends: %v", err)
	}

	// Subtests share mux and executor wiring; ServeHTTP on the mux is concurrency-safe.
	t.Run("non-streaming response", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"gpt-4o-mini","stream":false,"input":[{"role":"user","content":"ping"}]}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer sk-test")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		bodyStr := rr.Body.String()
		// Upstream may be invoked with streaming even when the client asked for a collected
		// response; refbackend then serves default SSE text ("stream-ok") or default JSON ("ok").
		if !strings.Contains(bodyStr, `"text":"ok"`) && !strings.Contains(bodyStr, `"text":"stream-ok"`) {
			t.Fatalf("response body missing refbackend assistant text: %s", bodyStr)
		}
	})

	t.Run("streaming response", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"gpt-4o-mini","stream":true,"input":[{"role":"user","content":"ping"}]}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer sk-test")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
			t.Fatalf("content-type %q want event-stream", ct)
		}
		if !strings.Contains(rr.Body.String(), "stream-ok") {
			t.Fatalf("sse body: %s", rr.Body.String())
		}
	})
}
