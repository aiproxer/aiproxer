package configreload_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	mgmtreload "github.com/aiproxer/aiproxer/internal/stdhttp"
	adminreload "github.com/aiproxer/aiproxer/internal/stdhttp/admin/configreload"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/internal/testkit/localstubreg"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"
)

// TestManagement_RemainsReachableAfterInvalidCandidate proves the management
// listener/handler is outside the swappable request plane (req 12.1, 13.1-13.2).
func TestManagement_RemainsReachableAfterInvalidCandidate(t *testing.T) {
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
		Compose:   mgmtreload.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bundle.Close() })

	coord := newFakeCoordinator("/fixed/config.yaml", func(context.Context, sdkreload.Trigger) sdkreload.Result {
		// Simulate rejected invalid candidate: last-good remains active.
		return sdkreload.Result{Category: sdkreload.ResultInvalid, ActiveGeneration: 1, ReasonCategory: "config_invalid"}
	})
	h, err := adminreload.NewHandler(adminreload.Options{
		Address:  "127.0.0.1:0",
		AuthMode: adminreload.AuthModeLocalTrust,
	}, coord)
	if err != nil {
		t.Fatal(err)
	}
	mgmt := httptest.NewServer(h.Mux())
	t.Cleanup(mgmt.Close)

	mgr := runtimehost.NewManager(2, nil)
	if err := mgr.Publish(mgr.PrepareRequestPlane("g1", bundle)); err != nil {
		t.Fatal(err)
	}

	// Invalid candidate attempt via management API.
	res, err := http.Post(mgmt.URL+adminreload.ReloadPath, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	assert.NoError(t, res.Body.Close())
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("invalid candidate status=%d", res.StatusCode)
	}

	// Management status remains reachable independently of request-plane generation.
	st, err := http.Get(mgmt.URL + adminreload.StatusPath)
	if err != nil {
		t.Fatal(err)
	}
	assert.NoError(t, st.Body.Close())
	if st.StatusCode != http.StatusOK {
		t.Fatalf("management status after invalid candidate: %d", st.StatusCode)
	}

	disp := runtimehost.NewGenerationDispatcher(mgr)
	for _, path := range []string{adminreload.ReloadPath, adminreload.StatusPath} {
		rr := httptest.NewRecorder()
		disp.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("data plane owns %s: %d", path, rr.Code)
		}
	}
}
