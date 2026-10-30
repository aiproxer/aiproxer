package stdhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/internal/testkit/localstubreg"
	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"
)

func TestManagement_SurvivesGenerationSwap(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	if err := localstubreg.RegisterInProcess(reg); err != nil {
		t.Fatal(err)
	}
	base := &config.Config{
		Routing:     config.RoutingConfig{MaxAttempts: 3},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes:    1024,
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 4096,
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
	}
	if err := config.Validate(base); err != nil {
		t.Fatal(err)
	}
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

	cand := func(id, text string, frontends []config.PluginConfig) *config.Config {
		t.Helper()
		var n yaml.Node
		raw := "text: \"" + text + "\"\ninput_tokens: 1\noutput_tokens: 1\n"
		if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
			t.Fatal(err)
		}
		for n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
			n = *n.Content[0]
		}
		cfg := &config.Config{
			Routing:    config.RoutingConfig{MaxAttempts: 3, DefaultRoute: id + ":stub-default"},
			Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
			Server: config.ServerConfig{
				MaxRequestBodyBytes:    1024,
				MaxConcurrentDecodes:   4,
				MaxInflightDecodeBytes: 4096,
			},
			Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
			Plugins: config.PluginsConfig{
				Frontends: frontends,
				Backends:  []config.PluginConfig{{Kind: "local-stub", ID: id, Enabled: true, Config: n}},
			},
		}
		if err := config.Validate(cfg); err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cand("m1", "a", []config.PluginConfig{{ID: "openai-responses", Enabled: true}}),
		Compose:   ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bundle.Close() })

	coord := NewRefReloadCoordinator("/fixed/config.yaml", func(context.Context, ReloadTrigger) ReloadResult {
		return ReloadResult{Category: ReloadCategoryNoop, ActiveGeneration: 1}
	})
	mgmt := NewRefConfigReloadManagement(coord)
	mgmt.AuthMode = ManagementAuthNone
	srv := httptest.NewServer(mgmt.Handler())
	t.Cleanup(srv.Close)

	mgr := runtimehost.NewManager(2, nil)
	if err := mgr.Publish(mgr.PrepareRequestPlane("g1", bundle)); err != nil {
		t.Fatal(err)
	}
	next, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: cand("m2", "b", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
			{ID: "openai-legacy", Enabled: true},
		}),
		Compose: ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.Close() })
	if err := mgr.Publish(mgr.PrepareRequestPlane("g2", next)); err != nil {
		t.Fatal(err)
	}

	res, err := http.Get(srv.URL + ConfigStatusPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { assert.NoError(t, res.Body.Close()) }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("management status after generation swap: %d", res.StatusCode)
	}

	// Data-plane still lacks management routes.
	disp := runtimehost.NewGenerationDispatcher(mgr)
	for _, path := range []string{ConfigReloadPath, ConfigStatusPath} {
		rr := httptest.NewRecorder()
		disp.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("data plane owns %s: %d", path, rr.Code)
		}
	}
}
