package stdhttp

import (
	"context"
	"strings"
	"testing"

	coreconfig "github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

// ComposeStandardHTTP must reject unsafe diagnostics posture before composition.
//
//nolint:paralleltest // documents coupling to validateStartupSecurity ordering; keep sequential.
func TestComposeStandardHTTP_rejectsUnsafeDiagnosticsPostureBeforeCompose(t *testing.T) {
	ctx := context.Background()
	cfg := &coreconfig.Config{
		Access:     coreconfig.AccessConfig{Mode: "multi_user"},
		Auth:       coreconfig.AuthConfig{Handler: "remote", RequiredLevel: "api_key"},
		Server:     coreconfig.ServerConfig{Address: "10.0.0.1:0", AuthMode: coreconfig.AuthModeExternal},
		Routing:    coreconfig.RoutingConfig{MaxAttempts: 3},
		Continuity: coreconfig.ContinuityConfig{InMemory: true, Store: "memory"},
		Plugins: coreconfig.PluginsConfig{
			Backends: []coreconfig.PluginConfig{{ID: "b1", Enabled: true}},
		},
		Diagnostics: coreconfig.DiagnosticsConfig{
			Enabled:      true,
			HealthPath:   "/healthz",
			AttemptsPath: "/admin/attempts",
			SharedSecret: "",
		},
	}
	log := testkit.DiscardLogger()
	reg := pluginreg.NewRegistry()
	ex := runtime.TestExecutor()
	in := StandardHTTPInput{
		Core:      HTTPCoreInput{Executor: ex},
		Frontends: frontendInputForTest(cfg, ex, reg),
	}
	_, err := ComposeStandardHTTP(ctx, cfg, log, in)
	if err == nil || !strings.Contains(err.Error(), "diagnostics.shared_secret") {
		t.Fatalf("want posture error before compose, got %v", err)
	}
}
