package config_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestRoutingOverrideAdmin_disabledByDefault(t *testing.T) {
	t.Parallel()
	var cfg config.RoutingConfig
	if cfg.OverrideAdmin.Enabled {
		t.Fatal("routing.override_admin.enabled must default to false")
	}
	if cfg.OverrideAdmin.PathPrefix != "" {
		t.Fatalf("empty default path_prefix, got %q", cfg.OverrideAdmin.PathPrefix)
	}
}

func TestRoutingOverrideAdmin_validateDisabledSkipsPath(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "b1", Enabled: true}}},
		Routing: config.RoutingConfig{
			OverrideAdmin: config.RoutingOverrideAdminConfig{
				Enabled:    false,
				PathPrefix: "not-a-path",
			},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("disabled override admin must not validate path: %v", err)
	}
}

func TestRoutingOverrideAdmin_validateEnabledNormalizesDefaults(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:8080"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "b1", Enabled: true}}},
		Routing: config.RoutingConfig{
			OverrideAdmin: config.RoutingOverrideAdminConfig{Enabled: true},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Routing.OverrideAdmin.PathPrefix != config.DefaultRoutingOverrideAdminPathPrefix {
		t.Fatalf("path: got %q", cfg.Routing.OverrideAdmin.PathPrefix)
	}
	if cfg.Routing.OverrideAdmin.MaxBodyBytes != config.DefaultRoutingOverrideAdminMaxBodyBytes {
		t.Fatalf("max body: got %d want %d", cfg.Routing.OverrideAdmin.MaxBodyBytes, config.DefaultRoutingOverrideAdminMaxBodyBytes)
	}
	if cfg.Routing.OverrideAdmin.MaxBodyBytes < int64(aipapi.MaxRouteSelectorBytes) {
		t.Fatal("max body must accommodate MaxRouteSelectorBytes")
	}
}

func TestValidateProtectedDiagnosticsPosture_overrideAdminRequiresSecretOffLoopback(t *testing.T) {
	t.Parallel()
	cfg := nonLoopbackServerCfg()
	cfg.Diagnostics.SharedSecret = ""
	cfg.Routing.OverrideAdmin.Enabled = true
	err := config.ValidateProtectedDiagnosticsPosture(cfg)
	if err == nil || !strings.Contains(err.Error(), "diagnostics.shared_secret") || !strings.Contains(err.Error(), "routing_override_admin") {
		t.Fatalf("want shared_secret error naming routing_override_admin, got %v", err)
	}
}

func TestValidateProtectedDiagnosticsPosture_overrideAdminAllowsLoopbackWithoutSecret(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:8080"},
		Routing: config.RoutingConfig{
			OverrideAdmin: config.RoutingOverrideAdminConfig{Enabled: true},
		},
	}
	if err := config.ValidateProtectedDiagnosticsPosture(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestValidateProtectedDiagnosticsPosture_overrideAdminAllowsNonLoopbackWithSecret(t *testing.T) {
	t.Parallel()
	cfg := nonLoopbackServerCfg()
	cfg.Diagnostics.SharedSecret = "twelve-chars-minimum-secret"
	cfg.Routing.OverrideAdmin.Enabled = true
	if err := config.ValidateProtectedDiagnosticsPosture(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestRoutingOverrideAdmin_validateRejectsFrontendPathCollision(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		prefix string
	}{
		{name: "v1", prefix: "/v1"},
		{name: "v1Nested", prefix: "/v1/routing-overrides"},
		{name: "v1beta", prefix: "/v1beta"},
		{name: "singleSegment", prefix: "/admin"},
		{name: "root", prefix: "/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{
				Server:     config.ServerConfig{Address: "127.0.0.1:8080"},
				Continuity: config.ContinuityConfig{InMemory: true},
				Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "b1", Enabled: true}}},
				Routing: config.RoutingConfig{
					OverrideAdmin: config.RoutingOverrideAdminConfig{Enabled: true, PathPrefix: tc.prefix},
				},
			}
			if err := config.Validate(cfg); err == nil {
				t.Fatalf("prefix %q must be rejected", tc.prefix)
			}
		})
	}
}

func TestRoutingOverrideAdmin_validateRejectsServeMuxPatternMeta(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		prefix string
	}{
		{name: "unclosedBrace", prefix: "/admin/{"},
		{name: "wildcardSegment", prefix: "/admin/{id}"},
		{name: "endWildcard", prefix: "/admin/{$}"},
		{name: "closingBrace", prefix: "/admin/foo}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{
				Server:     config.ServerConfig{Address: "127.0.0.1:8080"},
				Continuity: config.ContinuityConfig{InMemory: true},
				Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "b1", Enabled: true}}},
				Routing: config.RoutingConfig{
					OverrideAdmin: config.RoutingOverrideAdminConfig{Enabled: true, PathPrefix: tc.prefix},
				},
			}
			if err := config.Validate(cfg); err == nil {
				t.Fatalf("prefix %q must be rejected as a ServeMux pattern, not a literal path", tc.prefix)
			}
		})
	}
}

func TestRoutingOverrideAdmin_validateRejectsMountedPathCollision(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		mut  func(*config.Config)
	}{
		{
			name: "pprofExact",
			mut: func(c *config.Config) {
				c.Diagnostics.Enabled = true
				c.Diagnostics.PprofPath = "/debug/pprof"
				c.Routing.OverrideAdmin.PathPrefix = "/debug/pprof"
			},
		},
		{
			name: "pprofNested",
			mut: func(c *config.Config) {
				c.Diagnostics.Enabled = true
				c.Diagnostics.PprofPath = "/debug/pprof"
				c.Routing.OverrideAdmin.PathPrefix = "/debug/pprof/overrides"
			},
		},
		{
			name: "healthPrefix",
			mut: func(c *config.Config) {
				c.Diagnostics.HealthPath = "/healthz"
				c.Routing.OverrideAdmin.PathPrefix = "/healthz/overrides"
			},
		},
		{
			name: "accountingAdmin",
			mut: func(c *config.Config) {
				c.Accounting.Admin.Enabled = true
				c.Accounting.Admin.Path = "/admin/token-count"
				c.Routing.OverrideAdmin.PathPrefix = "/admin/token-count"
			},
		},
		{
			name: "billingReports",
			mut: func(c *config.Config) {
				c.Accounting.Billing.ReportsPath = "/admin/billing"
				c.Routing.OverrideAdmin.PathPrefix = "/admin/billing"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{
				Server:     config.ServerConfig{Address: "127.0.0.1:8080"},
				Continuity: config.ContinuityConfig{InMemory: true},
				Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "b1", Enabled: true}}},
				Routing: config.RoutingConfig{
					OverrideAdmin: config.RoutingOverrideAdminConfig{Enabled: true},
				},
			}
			tc.mut(cfg)
			err := config.Validate(cfg)
			if err == nil || !strings.Contains(err.Error(), "overlap") {
				t.Fatalf("prefix collision %s must be rejected as overlap, got %v", tc.name, err)
			}
		})
	}
}

func TestRoutingOverrideAdmin_validateDistinctFromMountedPaths(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:8080"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "b1", Enabled: true}}},
		Diagnostics: config.DiagnosticsConfig{
			Enabled:    true,
			HealthPath: "/healthz",
			PprofPath:  "/debug/pprof",
		},
		Accounting: config.AccountingConfig{
			Admin: config.AccountingAdminConfig{Enabled: true, Path: "/admin/token-count"},
		},
		Routing: config.RoutingConfig{
			OverrideAdmin: config.RoutingOverrideAdminConfig{Enabled: true, PathPrefix: "/admin/routing-overrides"},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("distinct override prefix must validate, got %v", err)
	}
}

func TestValidateProtectedDiagnosticsPosture_disabledOverrideAdminDoesNotRequireSecret(t *testing.T) {
	t.Parallel()
	cfg := nonLoopbackServerCfg()
	cfg.Diagnostics.SharedSecret = ""
	cfg.Routing.OverrideAdmin.Enabled = false
	if err := config.ValidateProtectedDiagnosticsPosture(cfg); err != nil {
		t.Fatal(err)
	}
}
