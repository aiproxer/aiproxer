package config_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func TestValidateProtectedDiagnosticsPosture_loopbackAllowsEmptySecretWithAttempts(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:8080"},
		Diagnostics: config.DiagnosticsConfig{
			Enabled:      true,
			HealthPath:   "/healthz",
			AttemptsPath: "/admin/attempts",
			SharedSecret: "",
		},
	}
	if err := config.ValidateProtectedDiagnosticsPosture(cfg); err != nil {
		t.Fatal(err)
	}
}

func nonLoopbackServerCfg() *config.Config {
	return &config.Config{
		Access:     config.AccessConfig{Mode: "multi_user"},
		Auth:       config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"},
		Server:     config.ServerConfig{Address: "10.0.0.1:8080", AuthMode: config.AuthModeExternal},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
}

func TestValidateProtectedDiagnosticsPosture_nonLoopbackRejectsEmptySecretWhenAttemptsExposed(t *testing.T) {
	t.Parallel()
	cfg := nonLoopbackServerCfg()
	cfg.Diagnostics = config.DiagnosticsConfig{
		Enabled:      true,
		AttemptsPath: "/admin/attempts",
		SharedSecret: "",
	}
	err := config.ValidateProtectedDiagnosticsPosture(cfg)
	if err == nil || !strings.Contains(err.Error(), "diagnostics.shared_secret") || !strings.Contains(err.Error(), "attempts") {
		t.Fatalf("want shared_secret error mentioning attempts, got %v", err)
	}
}

func TestValidateProtectedDiagnosticsPosture_nonLoopbackAllowsWithLongSecret(t *testing.T) {
	t.Parallel()
	cfg := nonLoopbackServerCfg()
	cfg.Diagnostics = config.DiagnosticsConfig{
		Enabled:      true,
		AttemptsPath: "/admin/attempts",
		SharedSecret: "twelve-chars-minimum-secret",
	}
	if err := config.ValidateProtectedDiagnosticsPosture(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestValidateProtectedDiagnosticsPosture_inventoryRouteTracePprofEachNamed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*config.Config)
		substr string
	}{
		{
			name: "inventory",
			mutate: func(c *config.Config) {
				c.Diagnostics.Enabled = true
				c.Diagnostics.InventoryPath = "/debug/inventory"
			},
			substr: "inventory",
		},
		{
			name: "model_inventory",
			mutate: func(c *config.Config) {
				c.ModelInventory.DiagnosticsPath = "/debug/model-registry"
			},
			substr: "model_inventory",
		},
		{
			name: "route_trace",
			mutate: func(c *config.Config) {
				c.Diagnostics.Enabled = true
				c.Diagnostics.RouteTracePath = "/debug/route-trace"
			},
			substr: "route_trace",
		},
		{
			name: "pprof",
			mutate: func(c *config.Config) {
				c.Diagnostics.Enabled = true
				c.Diagnostics.PprofPath = "/debug/pprof"
			},
			substr: "pprof",
		},
		{
			name: "metrics",
			mutate: func(c *config.Config) {
				c.Observability.Metrics.Enabled = true
				c.Observability.Metrics.Path = "/metrics"
			},
			substr: "metrics",
		},
		{
			name: "model_catalog",
			mutate: func(c *config.Config) {
				c.ModelCatalog.DiagnosticsPath = "/debug/models"
			},
			substr: "model_catalog",
		},
		{
			name: "secure_session_summaries",
			mutate: func(c *config.Config) {
				c.SecureSession.Enabled = config.BoolPtr(true)
				c.SecureSession.Store = "memory"
				c.SecureSession.TokenFingerprintKey = strings.Repeat("k", 32)
				c.SecureSession.DiagnosticsExposeSummaries = true
				c.SecureSession.DiagnosticsPathPrefix = "/debug/sessions"
				if c.Plugins.Backends == nil {
					c.Plugins = config.PluginsConfig{Backends: []config.PluginConfig{{ID: "b1", Enabled: true}}}
				}
			},
			substr: "secure_session_summaries",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := nonLoopbackServerCfg()
			cfg.Diagnostics.SharedSecret = ""
			tc.mutate(cfg)
			err := config.ValidateProtectedDiagnosticsPosture(cfg)
			if err == nil || !strings.Contains(err.Error(), "diagnostics.shared_secret") || !strings.Contains(err.Error(), tc.substr) {
				t.Fatalf("want error naming %s, got %v", tc.substr, err)
			}
		})
	}
}

func TestValidateProtectedDiagnosticsPosture_healthOnlyNeverRequiresSecret(t *testing.T) {
	t.Parallel()
	cfg := nonLoopbackServerCfg()
	cfg.Diagnostics = config.DiagnosticsConfig{
		Enabled:    true,
		HealthPath: "/healthz",
		// no attempts/inventory/route_trace/pprof
		AttemptsPath: "",
		SharedSecret: "",
	}
	if err := config.ValidateProtectedDiagnosticsPosture(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestValidateProtectedDiagnosticsPosture_wiredIntoValidate(t *testing.T) {
	t.Parallel()
	cfg := nonLoopbackServerCfg()
	cfg.Diagnostics = config.DiagnosticsConfig{
		Enabled:      true,
		HealthPath:   "/healthz",
		AttemptsPath: "/admin/attempts",
		SharedSecret: "",
	}
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "diagnostics.shared_secret") {
		t.Fatalf("want Validate to enforce posture, got %v", err)
	}
}
