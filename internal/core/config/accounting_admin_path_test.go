package config_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

// accountingAdminPathConfig returns a loopback-bound, otherwise-valid config with
// both control-plane query and token-accounting admin enabled, so path-overlap
// failures are the only thing that can reject Validate. The admin path and
// control-plane path_prefix are set by the caller.
func accountingAdminPathConfig(adminPath, cpPath string) config.Config {
	return config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:8080"},
		Accounting: config.AccountingConfig{
			Admin: config.AccountingAdminConfig{
				Enabled: true,
				Path:    adminPath,
			},
		},
		ControlPlane: config.ControlPlaneConfig{
			Enabled: true,
			Query:   config.ControlPlaneQueryConfig{Enabled: true, PathPrefix: cpPath},
		},
	}
}

func TestAccountingAdminPathOverlapWithControlPlaneQueryRejected(t *testing.T) {
	t.Parallel()
	// Both surfaces mount on the same stdhttp mux; an exact duplicate must be
	// rejected by Validate instead of panicking inside mux.Handle at startup.
	cfg := accountingAdminPathConfig("/admin", "/admin")
	if err := config.Validate(&cfg); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("expected overlap error vs control_plane.query.path_prefix, got %v", err)
	}
}

func TestAccountingAdminPathOverlapWithDiagnosticsHealthRejected(t *testing.T) {
	t.Parallel()
	cfg := accountingAdminPathConfig("/healthz", "/cp")
	cfg.Diagnostics = config.DiagnosticsConfig{HealthPath: "/healthz"}
	if err := config.Validate(&cfg); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("expected overlap error vs diagnostics.health_path, got %v", err)
	}
}

func TestAccountingAdminPathPrefixOverlapWithControlPlaneQueryRejected(t *testing.T) {
	t.Parallel()
	// Admin mount nested under the control-plane prefix would shadow the
	// control-plane subtree mount; the prefix-overlap branch must reject it.
	cfg := accountingAdminPathConfig("/cp/admin", "/cp")
	if err := config.Validate(&cfg); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("expected prefix overlap error vs control_plane.query.path_prefix, got %v", err)
	}
}

func TestAccountingAdminPathDotDotRejected(t *testing.T) {
	t.Parallel()
	cfg := accountingAdminPathConfig("/admin/../cp", "/cp")
	if err := config.Validate(&cfg); err == nil || !strings.Contains(err.Error(), "..") {
		t.Fatalf("expected .. segment rejection for accounting.admin.path, got %v", err)
	}
}

func TestAccountingAdminPathDistinctFromOtherMountsValidates(t *testing.T) {
	t.Parallel()
	cfg := accountingAdminPathConfig("/admin/token-count", "/cp")
	cfg.Diagnostics = config.DiagnosticsConfig{HealthPath: "/healthz"}
	cfg.Observability = config.ObservabilityConfig{
		Metrics: config.MetricsConfig{Enabled: true, Path: "/metrics"},
	}
	if err := config.Validate(&cfg); err != nil {
		t.Fatalf("distinct admin path must validate, got %v", err)
	}
}

func TestAccountingAdminPathNotCheckedWhenDisabled(t *testing.T) {
	t.Parallel()
	// When admin is disabled the path is not mounted, so a path overlapping
	// another route must NOT be rejected (it is irrelevant).
	cfg := config.Config{
		Server:      config.ServerConfig{Address: "127.0.0.1:8080"},
		Diagnostics: config.DiagnosticsConfig{HealthPath: "/healthz"},
		Accounting: config.AccountingConfig{
			Admin: config.AccountingAdminConfig{Enabled: false, Path: "/healthz"},
		},
		ControlPlane: config.ControlPlaneConfig{
			Enabled: true,
			Query:   config.ControlPlaneQueryConfig{Enabled: true, PathPrefix: "/cp"},
		},
	}
	if err := config.Validate(&cfg); err != nil {
		t.Fatalf("disabled admin path must not be validated, got %v", err)
	}
}
