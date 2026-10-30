package runtimebundle_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/localstub"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	stdhttpauth "github.com/aiproxer/aiproxer/internal/stdhttp/auth"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
	"gopkg.in/yaml.v3"
)

func postResponsesAuth(t *testing.T, h http.Handler, model, bearer string) string {
	t.Helper()
	body := fmt.Appendf(nil, `{"model":%q,"stream":false,"input":[{"role":"user","content":"ping"}]}`, model)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rr.Code, rr.Body.String())
	}
	return rr.Body.String()
}

// Phase 6.3 certification: dynamic recomposition + frozen discovered compatibility
// matching validation filter Reload.*Dynamic|Generic|Discovered|NoInstall|NoWatcher.

func TestReloadDynamic_FrontendFeatureAuthRouteAliasModelLimitsRecompose(t *testing.T) {
	t.Parallel()
	keysOld := []config.AuthLocalAPIKeyRecord{{KeyID: "k1", PrincipalID: "user-a", Key: "dynamic-old-key-16chars"}}
	keysNew := []config.AuthLocalAPIKeyRecord{{KeyID: "k2", PrincipalID: "user-b", Key: "dynamic-new-key-16chars"}}

	oldCfg := policyBaseConfig(t, "dyn-old", "old-backend-text", keysOld)
	oldCfg.Plugins.Frontends = []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
		{ID: "openai-legacy", Enabled: false},
	}
	oldCfg.Plugins.Features = []config.PluginConfig{
		{ID: "sg", Kind: "secrets-guard", Enabled: true, Config: genYAMLNode(t, "action: log\n")},
	}
	oldCfg.ModelAliases = []config.ModelAliasConfig{{Pattern: `^friendly$`, Replacement: "dyn-old:stub-default"}}
	oldCfg.Server.MaxRequestBodyBytes = 2048
	oldCfg.Server.MaxPendingWireEvents = 11
	if err := config.Validate(oldCfg); err != nil {
		t.Fatalf("validate old: %v", err)
	}

	ps := policyProcess(t, oldCfg)

	newCfg := policyBaseConfig(t, "dyn-new", "new-backend-text", keysNew)
	newCfg.Plugins.Frontends = []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
		{ID: "anthropic", Enabled: true},
	}
	newCfg.Plugins.Features = []config.PluginConfig{
		{ID: "sg", Kind: "secrets-guard", Enabled: true, Config: genYAMLNode(t, "action: redact\n")},
	}
	newCfg.Plugins.Backends = append(newCfg.Plugins.Backends, config.PluginConfig{
		Kind: standardplugins.CustomOpenAIResponsesCompatibleID, ID: "dyn-generic", Enabled: true,
		Config: genYAMLNode(t, `
backend_prefix: dyngeneric
base_url: http://127.0.0.1:9/v1
models:
  source: inline
  items:
    - canonical_id: dyngeneric/static-model
      native_id: static-model
`),
	})
	newCfg.Routing.DefaultRoute = "dyn-new:stub-default"
	newCfg.ModelAliases = []config.ModelAliasConfig{{Pattern: `^friendly$`, Replacement: "dyn-new:stub-default"}}
	newCfg.Server.MaxRequestBodyBytes = 96
	newCfg.Server.MaxPendingWireEvents = 77
	if err := config.Validate(newCfg); err != nil {
		t.Fatalf("validate new: %v", err)
	}

	oldBundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: oldCfg, Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("compile old: %v", err)
	}
	newBundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: newCfg, Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("compile new: %v", err)
	}

	ids := map[string]bool{}
	newGB, ok := newBundle.(*runtimebundle.GenerationBundle)
	if !ok {
		t.Fatal("expected *runtimebundle.GenerationBundle")
	}
	for _, id := range newGB.BackendIDs() {
		ids[id] = true
	}
	if !ids["dyn-new"] || !ids["dyn-generic"] {
		t.Fatalf("new backends missing: %v", newGB.BackendIDs())
	}
	limitCand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Bus: hooks.New(hooks.Config{}), Candidate: newCfg,
	})
	if err != nil {
		t.Fatalf("compile limits candidate: %v", err)
	}
	t.Cleanup(func() { _ = limitCand.Close() })
	if limitCand.Executor().MaxPendingWireEvents != 77 {
		t.Fatalf("request-plane limit not recomposed: %d", limitCand.Executor().MaxPendingWireEvents)
	}

	m := runtimehost.NewManager(4, nil)
	disp := runtimehost.NewGenerationDispatcher(m)
	if err := m.Publish(m.PrepareRequestPlane("old", oldBundle)); err != nil {
		t.Fatal(err)
	}
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire old")
	}
	defer lease.Release()
	if err := m.Publish(m.PrepareRequestPlane("new", newBundle)); err != nil {
		t.Fatal(err)
	}

	oldBody := postResponsesAuth(t, lease.Handler(), "stub-default", "dynamic-old-key-16chars")
	if !strings.Contains(oldBody, "old-backend-text") || strings.Contains(oldBody, "new-backend-text") {
		t.Fatalf("old pinned handler mixed generations: %s", oldBody)
	}
	newBody := postResponsesAuth(t, disp, "stub-default", "dynamic-new-key-16chars")
	if !strings.Contains(newBody, "new-backend-text") || strings.Contains(newBody, "old-backend-text") {
		t.Fatalf("new dispatcher mixed generations: %s", newBody)
	}

	// Frontend mount recomposition: anthropic route present only on new generation.
	rrNew := httptest.NewRecorder()
	disp.ServeHTTP(rrNew, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`)))
	if rrNew.Code == http.StatusNotFound {
		t.Fatal("new generation must mount anthropic frontend")
	}

	// Auth key recomposition: old/new accept independent bearer keys.
	assertDynBearer := func(t *testing.T, providers []httpauth.Provider, bearer string, wantOK bool) {
		t.Helper()
		h := stdhttpauth.Middleware(nil, providers, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		gotOK := rr.Code == http.StatusOK
		if gotOK != wantOK {
			t.Fatalf("bearer %q status=%d wantOK=%v", bearer, rr.Code, wantOK)
		}
	}
	oldCand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Bus: hooks.New(hooks.Config{}), Candidate: oldCfg,
	})
	if err != nil {
		t.Fatalf("compile old auth: %v", err)
	}
	t.Cleanup(func() { _ = oldCand.Close() })
	newCand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Bus: hooks.New(hooks.Config{}), Candidate: newCfg,
	})
	if err != nil {
		t.Fatalf("compile new auth: %v", err)
	}
	t.Cleanup(func() { _ = newCand.Close() })
	assertDynBearer(t, runtimebundle.CandidateHTTPAuthProviders(oldCand), "dynamic-old-key-16chars", true)
	assertDynBearer(t, runtimebundle.CandidateHTTPAuthProviders(oldCand), "dynamic-new-key-16chars", false)
	assertDynBearer(t, runtimebundle.CandidateHTTPAuthProviders(newCand), "dynamic-new-key-16chars", true)
	assertDynBearer(t, runtimebundle.CandidateHTTPAuthProviders(newCand), "dynamic-old-key-16chars", false)

	// Request-limit recomposition: new generation rejects oversized body.
	big := fmt.Sprintf(`{"model":"stub-default","stream":false,"input":[{"role":"user","content":%q}]}`, strings.Repeat("x", 256))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(big))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer dynamic-new-key-16chars")
	rrLimit := httptest.NewRecorder()
	disp.ServeHTTP(rrLimit, req)
	if rrLimit.Code == http.StatusOK {
		t.Fatalf("new max_request_body_bytes=96 must reject oversized body, got %d", rrLimit.Code)
	}

	if ps.Closed() {
		t.Fatal("process services must remain open across dynamic recomposition")
	}
}

func TestReloadDynamic_DiscoveredFrozen_NoInstallNoWatcher(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterDiscoveredBackend("discovered-dyn-stub", func(n yaml.Node, _ *http.Client, _ pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return localstub.NewFromYAML(n)
	}, pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialNone}, pluginreg.BackendReloadPolicy{
		AllowsCandidateOverlap: true,
	}); err != nil {
		t.Fatal(err)
	}

	base := processBaseConfig()
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  base,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	cand := stubCandidateConfig(t, "discovered-dyn-stub", "discovered-text", "discovered-dyn-stub:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
	})
	cand.Plugins.Backends[0].Kind = "discovered-dyn-stub"
	cand.Plugins.Backends[0].ID = "discovered-dyn-stub"
	if err := config.Validate(cand); err != nil {
		t.Fatalf("validate: %v", err)
	}

	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: cand, Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("activate discovered kind: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	body := postResponses(t, bundle.Handler(), "stub-default")
	if !strings.Contains(body, "discovered-text") {
		t.Fatalf("discovered activation body=%s", body)
	}
	// No watcher: active generation changes only via explicit publish, never by FS touch.
	m := runtimehost.NewManager(4, nil)
	if err := m.Publish(m.PrepareRequestPlane("discovered", bundle)); err != nil {
		t.Fatal(err)
	}
	before := m.Active().ID()
	if m.Active().ID() != before {
		t.Fatal("active mutated without explicit publish/reload trigger")
	}
}
