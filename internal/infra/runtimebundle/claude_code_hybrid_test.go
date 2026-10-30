package runtimebundle

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/billingbinding"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/internal/testkit/localstubreg"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const hybridBillingFrontendConfig = "claude_code_hybrid: {enabled: true, external_models: [{model: worker, route: 'stub:stub-default'}]}"

// Claim (#1513 post-split repair): authoritative process money ports, not admin
// mounts, exclude opaque hybrid handlers before composition/publication.
// Mutant: remove the compileGenerationCandidate compatibility check.
func TestClaudeCodeHybrid_ProcessBillingAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, billing  string
		hybrid, reject bool
	}{
		{"stock", "", true, false},
		{"generic-admin", "admin", true, false},
		{"external-without-admin", "external", true, true},
		{"disabled-with-billing", "external", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ps, _ := hybridBillingProcess(t, tc.billing, tc.hybrid)
			composed := false
			gen, err := CompileGeneration(t.Context(), GenerationCompileInput{
				Process: ps,
				Compose: func(_ context.Context, cfg *config.Config, _ *slog.Logger, _ httpcontract.StandardHTTPInput) (http.Handler, error) {
					composed = true
					return http.NotFoundHandler(), standardplugins.ValidateFrontendDeployment(cfg)
				},
			})
			if gen != nil {
				t.Cleanup(func() { _ = gen.Close() })
			}
			if tc.reject {
				require.ErrorContains(t, err, "hybrid cannot bypass an injected billing composition")
				require.Nil(t, gen)
				require.False(t, composed, "billed hybrid must fail before invoking the composer")
			} else {
				require.NoError(t, err)
				require.True(t, composed)
				require.NotNil(t, gen)
			}
		})
	}
}

// Claim (#1513): a reload cannot clear process-owned billing through candidate
// options, and a rejected hybrid candidate leaves the active generation usable.
func TestClaudeCodeHybrid_BilledReloadRetainsGeneration(t *testing.T) {
	ps, cfg := hybridBillingProcess(t, "external", false)
	compositions := 0
	compose := func(context.Context, *config.Config, *slog.Logger, httpcontract.StandardHTTPInput) (http.Handler, error) {
		compositions++
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }), nil
	}
	initial, err := CompileGeneration(t.Context(), GenerationCompileInput{Process: ps, Compose: compose})
	require.NoError(t, err)
	manager := runtimehost.NewManager(2, nil)
	active := manager.PrepareRequestPlane("billed", initial)
	require.NoError(t, manager.Publish(active))
	t.Cleanup(func() {
		manager.DetachActive()
		require.NoError(t, manager.ShutdownDetached(context.Background()))
	})
	candidate, err := freezeConfig(cfg)
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal([]byte(hybridBillingFrontendConfig), &candidate.Plugins.Frontends[0].Config))
	gen, err := CompileGeneration(t.Context(), GenerationCompileInput{
		Process: ps, Candidate: candidate, CandidateOpts: &BuildOptions{}, Compose: compose,
	})
	if gen != nil {
		t.Cleanup(func() { _ = gen.Close() })
	}
	require.ErrorContains(t, err, "hybrid cannot bypass an injected billing composition")
	require.Nil(t, gen)
	require.Equal(t, 1, compositions)
	lease, ok := manager.Acquire()
	require.True(t, ok)
	defer lease.Release()
	require.Same(t, active, lease.Generation())
	w := httptest.NewRecorder()
	lease.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	require.Equal(t, 204, w.Code)
	require.False(t, ps.Closed())
}

func hybridBillingProcess(t *testing.T, kind string, hybrid bool) (*ProcessServices, *config.Config) {
	t.Helper()
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0", WriteTimeout: "6m"},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Routing:    config.RoutingConfig{DefaultRoute: "stub:stub-default"},
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "anthropic", Enabled: true}},
			Backends:  []config.PluginConfig{{ID: "stub", Kind: "local-stub", Enabled: true}},
		},
	}
	if hybrid {
		require.NoError(t, yaml.Unmarshal([]byte(hybridBillingFrontendConfig), &cfg.Plugins.Frontends[0].Config))
	}
	require.NoError(t, config.Validate(cfg))
	reg := pluginreg.NewRegistry()
	require.NoError(t, standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}))
	require.NoError(t, localstubreg.RegisterInProcess(reg))
	var prod ProductionOptions
	switch kind {
	case "external":
		adapter, err := billingbinding.NewAdapter(testBinding())
		require.NoError(t, err)
		prod.BillingCreditGate, prod.BillingExposureAdmission, prod.BillingTerminalUsageSink, prod.BillingIdentity = adapter.Chokepoints()
	case "admin":
		prod.AdminMounts = []AdminMount{{Path: "/admin/unrelated", Handler: http.NotFoundHandler()}}
	}
	ps, err := NewProcessServices(t.Context(), ProcessServicesInput{Cfg: cfg, Log: testkit.DiscardLogger(), Opts: &BuildOptions{PluginRegistry: reg, Production: prod}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ps.Close()) })
	if kind == "external" {
		require.Empty(t, ps.opts.Production.AdminMounts, "storeless monetary binding has no reporting/admin mount")
	}
	return ps, cfg
}
