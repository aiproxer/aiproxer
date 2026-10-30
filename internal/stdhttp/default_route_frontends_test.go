package stdhttp

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// unifiedPolicyRoute is a single configured default_route used to prove all bundled HTTP
// frontends consume the same policy value when the client omits X-AIP-Route (task 10.4).
const unifiedPolicyRoute = "stub:unified-policy-model"

func testRegistryWithStdBundle(t *testing.T) *pluginreg.Registry {
	t.Helper()
	reg := pluginreg.NewRegistry()
	// Env-only credential contract (requirement 2.1): registration binds host
	// keys; backend YAML carries none.
	keys := standardplugins.UpstreamAPIKeys{
		OpenAI:    []string{"sk-test"},
		Anthropic: []string{"sk-test"},
		Gemini:    []string{"sk-test"},
	}
	if err := standardplugins.InstallStandardBundleOn(reg, keys); err != nil {
		t.Fatal(err)
	}
	return reg
}

func policyConfig() *config.Config {
	return &config.Config{
		Routing: config.RoutingConfig{
			DefaultRoute: unifiedPolicyRoute,
		},
	}
}

func TestOmittedRoute_openaiResponses_usesEffectiveDefaultRoute(t *testing.T) {
	t.Parallel()
	reg := testRegistryWithStdBundle(t)
	cfg := policyConfig()
	route := config.EffectiveDefaultRouteSelector(cfg, standardplugins.DefaultWireModel)
	if route != unifiedPolicyRoute {
		t.Fatalf("effective route %q", route)
	}
	var captured sync.Map
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "ok", &captured)
	mux := http.NewServeMux()
	if err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux: mux,
		Frontends: HTTPFrontendInput{
			Executor:             ex,
			DefaultRouteSelector: route,
			Plugins:              []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
			MaxRequestBodyBytes:  0,
			Registry:             reg,
		},
	}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"model":"gpt-4o-mini","stream":false,"input":[{"role":"user","content":"ping"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	v, ok := captured.Load("last")
	if !ok {
		t.Fatal("expected captured call")
	}
	call := testkit.MustAIPCall(t, v)
	if got := call.Route.Selector; got != unifiedPolicyRoute {
		t.Fatalf("route selector %q want %q", got, unifiedPolicyRoute)
	}
}

func TestBodyRoute_openaiResponses_usesMountedRoutePrefixes(t *testing.T) {
	t.Parallel()
	reg := testRegistryWithStdBundle(t)
	var captured sync.Map
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "ok", &captured)
	mux := http.NewServeMux()
	if err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux: mux,
		Frontends: HTTPFrontendInput{
			Executor:             ex,
			DefaultRouteSelector: unifiedPolicyRoute,
			Plugins:              []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
			RoutePrefixes:        []string{"stub"},
			MaxRequestBodyBytes:  0,
			Registry:             reg,
		},
	}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"model":"stub:gpt-5.5?reasoning_effort=low","stream":false,"input":[{"role":"user","content":"ping"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	v, ok := captured.Load("last")
	if !ok {
		t.Fatal("expected captured call")
	}
	call := testkit.MustAIPCall(t, v)
	if got := call.Route.Selector; got != "stub:gpt-5.5?reasoning_effort=low" {
		t.Fatalf("route selector %q", got)
	}
}

func TestOmittedRoute_openaiLegacy_usesEffectiveDefaultRoute(t *testing.T) {
	t.Parallel()
	reg := testRegistryWithStdBundle(t)
	cfg := policyConfig()
	route := config.EffectiveDefaultRouteSelector(cfg, standardplugins.DefaultWireModel)
	var captured sync.Map
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "ok", &captured)
	mux := http.NewServeMux()
	if err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux: mux,
		Frontends: HTTPFrontendInput{
			Executor:             ex,
			DefaultRouteSelector: route,
			Plugins:              []config.PluginConfig{{ID: "openai-legacy", Enabled: true}},
			MaxRequestBodyBytes:  0,
			Registry:             reg,
		},
	}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"model":"gpt-4o-mini","stream":false,"messages":[{"role":"user","content":"ping"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	v, ok := captured.Load("last")
	if !ok {
		t.Fatal("expected captured call")
	}
	call := testkit.MustAIPCall(t, v)
	if got := call.Route.Selector; got != unifiedPolicyRoute {
		t.Fatalf("route selector %q want %q", got, unifiedPolicyRoute)
	}
}

func TestOmittedRoute_anthropic_usesEffectiveDefaultRoute(t *testing.T) {
	t.Parallel()
	reg := testRegistryWithStdBundle(t)
	cfg := policyConfig()
	route := config.EffectiveDefaultRouteSelector(cfg, standardplugins.DefaultWireModel)
	var captured sync.Map
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "ok", &captured)
	mux := http.NewServeMux()
	if err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux: mux,
		Frontends: HTTPFrontendInput{
			Executor:             ex,
			DefaultRouteSelector: route,
			Plugins:              []config.PluginConfig{{ID: "anthropic", Enabled: true}},
			MaxRequestBodyBytes:  0,
			Registry:             reg,
		},
	}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"user","content":"ping"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	v, ok := captured.Load("last")
	if !ok {
		t.Fatal("expected captured call")
	}
	call := testkit.MustAIPCall(t, v)
	if got := call.Route.Selector; got != unifiedPolicyRoute {
		t.Fatalf("route selector %q want %q", got, unifiedPolicyRoute)
	}
}

func TestCompileCandidate_defaultBackendFromEffectiveRoute(t *testing.T) {
	t.Parallel()
	reg := testRegistryWithStdBundle(t)
	cfg := policyConfig()
	cfg.Continuity = config.ContinuityConfig{InMemory: true}
	_, cand := compileTestCandidate(t, cfg, reg)
	if cand.Executor().DefaultBackend != "stub" {
		t.Fatalf("DefaultBackend %q want stub", cand.Executor().DefaultBackend)
	}
}
