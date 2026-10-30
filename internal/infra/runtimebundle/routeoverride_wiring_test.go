package runtimebundle_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func routeOverrideBaseConfig() *config.Config {
	return &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
	}
}

func TestProcessServices_wiresRouteOverrideStoreWhenAdminDisabled(t *testing.T) {
	t.Parallel()
	cfg := routeOverrideBaseConfig()
	ps, cand := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if ps.RouteOverrideStore == nil {
		t.Fatal("standard memory continuity must expose routeoverride.Store when admin HTTP is disabled")
	}
	ex := cand.Executor()
	if ex == nil || ex.RouteOverrideReader == nil {
		t.Fatal("executor must receive the process-owned override reader when admin HTTP is disabled")
	}
	if _, ok := routeoverride.AsStore(ps.RouteOverrideStore); !ok {
		t.Fatal("process override store must implement routeoverride.Store")
	}
}

func TestProcessServices_sqliteWiresRouteOverrideStore(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	cfg := routeOverrideBaseConfig()
	cfg.Continuity = config.ContinuityConfig{
		InMemory:   false,
		Store:      "sqlite",
		SQLitePath: filepath.Join(t.TempDir(), "continuity.db"),
	}
	ps, cand := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{PluginRegistry: reg})
	if ps.RouteOverrideStore == nil {
		t.Fatal("standard bun/sqlite continuity must expose routeoverride.Store")
	}
	if cand.Executor() == nil || cand.Executor().RouteOverrideReader == nil {
		t.Fatal("sqlite executor must receive the override reader")
	}
}

func TestCompileGeneration_adminDisabledOmitsHTTPButKeepsReader(t *testing.T) {
	t.Parallel()
	cfg := routeOverrideBaseConfig()
	ps, gen := mustProcessAndGeneration(t, cfg, nil)
	ex := runtimebundle.GenerationExecutorOf(gen)
	if ex == nil || ex.RouteOverrideReader == nil {
		t.Fatal("disabled admin HTTP must not remove the runtime reader")
	}
	if ps.RouteOverrideStore == nil {
		t.Fatal("process store must remain wired")
	}
	rr := httptest.NewRecorder()
	gen.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/admin/routing-overrides/a_1", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("disabled admin path status=%d want 404", rr.Code)
	}
}

func TestCompileGeneration_adminEnabledMountsHandlerOnGenerationHTTP(t *testing.T) {
	t.Parallel()
	const secret = "override-secret-12"
	cfg := routeOverrideBaseConfig()
	cfg.Diagnostics.SharedSecret = secret
	cfg.Routing.OverrideAdmin.Enabled = true
	ps, gen := mustProcessAndGeneration(t, cfg, nil)
	if ps.RouteOverrideStore == nil {
		t.Fatal("enabled admin still uses process-owned store")
	}
	ex := runtimebundle.GenerationExecutorOf(gen)
	if ex == nil || ex.RouteOverrideReader == nil {
		t.Fatal("enabled admin must keep the runtime reader")
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/routing-overrides/a_1", nil)
	rr := httptest.NewRecorder()
	gen.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("unauthenticated admin status=%d want 403 (proves generation handler mounted the protected surface)", rr.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/admin/routing-overrides/a_1", nil)
	req2.Header.Set(diag.HeaderDiagnosticsSecret, secret)
	rr2 := httptest.NewRecorder()
	gen.Handler().ServeHTTP(rr2, req2)
	if rr2.Code == http.StatusForbidden {
		t.Fatal("authenticated admin must not be forbidden")
	}
	if rr2.Code == http.StatusNotImplemented {
		t.Fatal("authenticated admin must reach the real generation handler, not a stub")
	}
	if rr2.Code != http.StatusNotFound {
		t.Fatalf("authenticated unknown A-leg status=%d want 404", rr2.Code)
	}
}

func TestCompileGeneration_disableHTTPLaterGenerationKeepsProcessStore(t *testing.T) {
	t.Parallel()
	const secret = "override-secret-12"
	on := routeOverrideBaseConfig()
	on.Diagnostics.SharedSecret = secret
	on.Routing.OverrideAdmin.Enabled = true
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  on,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: generationRegistry(t)},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	store := ps.RouteOverrideStore
	if store == nil {
		t.Fatal("expected process override store")
	}

	genOn, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: on,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration enabled: %v", err)
	}
	t.Cleanup(func() { _ = genOn.Close() })

	off := routeOverrideBaseConfig()
	off.Diagnostics.SharedSecret = secret
	off.Routing.OverrideAdmin.Enabled = false
	genOff, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: off,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration disabled: %v", err)
	}
	t.Cleanup(func() { _ = genOff.Close() })

	if ps.RouteOverrideStore != store {
		t.Fatal("process override store must stay process-scoped across generations")
	}
	onExec := runtimebundle.GenerationExecutorOf(genOn)
	offExec := runtimebundle.GenerationExecutorOf(genOff)
	if onExec == nil || offExec == nil || onExec.RouteOverrideReader == nil || offExec.RouteOverrideReader == nil {
		t.Fatal("both generations must keep the runtime reader")
	}

	rrOn := httptest.NewRecorder()
	genOn.Handler().ServeHTTP(rrOn, httptest.NewRequest(http.MethodGet, "/admin/routing-overrides/a_1", nil))
	if rrOn.Code != http.StatusForbidden {
		t.Fatalf("enabled generation status=%d want 403", rrOn.Code)
	}
	rrOff := httptest.NewRecorder()
	genOff.Handler().ServeHTTP(rrOff, httptest.NewRequest(http.MethodGet, "/admin/routing-overrides/a_1", nil))
	if rrOff.Code != http.StatusNotFound {
		t.Fatalf("disabled generation status=%d want 404", rrOff.Code)
	}
}

func TestCompileGeneration_adminEnabledWithoutStoreFails(t *testing.T) {
	t.Parallel()
	cfg := routeOverrideBaseConfig()
	cfg.Routing.OverrideAdmin.Enabled = true
	cfg.Server.Address = "127.0.0.1:0"
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  routeOverrideBaseConfig(),
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: generationRegistry(t)},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	ps.RouteOverrideStore = nil

	_, err = runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfg,
		Bus:       hooks.New(hooks.Config{}),
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err == nil || !strings.Contains(err.Error(), "routeoverride.Store") {
		t.Fatalf("want coherent missing-store assembly error, got %v", err)
	}
}

func TestGenerationSelectorValidator_usesCompileSelector(t *testing.T) {
	t.Parallel()
	cfg := routeOverrideBaseConfig()
	cfg.Routing.OverrideAdmin.Enabled = true
	cfg.Diagnostics.SharedSecret = "override-secret-12"
	_, gen := mustProcessAndGeneration(t, cfg, nil)
	ex := runtimebundle.GenerationExecutorOf(gen)
	if ex == nil {
		t.Fatal("expected generation executor")
	}
	v := runtimebundle.GenerationSelectorValidatorForTest(ex.SelectorAliases, ex.DefaultBackend, nil)
	if err := v.ValidateSelector(context.Background(), "openai:gpt-4"); err != nil {
		t.Fatalf("direct selector: %v", err)
	}
	if err := v.ValidateSelector(context.Background(), "   "); err == nil {
		t.Fatal("empty selector must fail generation preflight")
	}
	known := runtimebundle.GenerationSelectorValidatorForTest(ex.SelectorAliases, ex.DefaultBackend, map[string]struct{}{"openai": {}})
	if err := known.ValidateSelector(context.Background(), "openai:gpt-4"); err != nil {
		t.Fatalf("known backend: %v", err)
	}
	if err := known.ValidateSelector(context.Background(), "typo-backend:model"); err == nil {
		t.Fatal("unknown backend must fail generation preflight")
	}

	execResolver := routing.BackendExecutionResolverFunc(func(id string) (aipsdk.BackendExecutionClass, bool) {
		switch id {
		case "openai":
			return aipsdk.BackendExecutionInference, true
		case "acp":
			return aipsdk.BackendExecutionAgentRuntime, true
		default:
			return aipsdk.BackendExecutionUnknown, false
		}
	})

	knownWithACP := map[string]struct{}{"openai": {}, "acp": {}}
	safeValidator := runtimebundle.GenerationSelectorValidatorWithExecutionForTest(
		ex.SelectorAliases,
		ex.DefaultBackend,
		knownWithACP,
		execResolver,
		config.ExecutionCompositionSafe,
	)

	// Direct ACP allowed
	if err := safeValidator.ValidateSelector(context.Background(), "acp:claude-3-7-sonnet"); err != nil {
		t.Fatalf("direct ACP selector should be allowed, got: %v", err)
	}

	// Mixed failover rejected
	if err := safeValidator.ValidateSelector(context.Background(), "openai:gpt-4|acp:claude-3-7-sonnet"); err == nil {
		t.Fatal("mixed ACP failover selector must be rejected under safe policy")
	} else if !errors.Is(err, routing.ErrUnsafeExecutionComposition) {
		t.Fatalf("expected ErrUnsafeExecutionComposition, got: %v", err)
	}

	// Unrestricted allowed
	unrestrictedValidator := runtimebundle.GenerationSelectorValidatorWithExecutionForTest(
		ex.SelectorAliases,
		ex.DefaultBackend,
		knownWithACP,
		execResolver,
		config.ExecutionCompositionUnrestricted,
	)
	if err := unrestrictedValidator.ValidateSelector(context.Background(), "openai:gpt-4|acp:claude-3-7-sonnet"); err != nil {
		t.Fatalf("mixed ACP failover selector must be allowed under unrestricted policy, got: %v", err)
	}
}

func generationRegistry(t *testing.T) *pluginreg.Registry {
	t.Helper()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	return reg
}
