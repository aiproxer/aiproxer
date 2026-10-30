package config_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

// controlPlaneQueryPathConfig returns a loopback-bound, otherwise-valid config with
// control-plane query enabled on a memory store, so path-validation failures are
// the only thing that can reject Validate.
func controlPlaneQueryPathConfig(path string) config.Config {
	return config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:8080"},
		ControlPlane: config.ControlPlaneConfig{
			Enabled: true,
			Query:   config.ControlPlaneQueryConfig{Enabled: true, PathPrefix: path},
		},
	}
}

func TestControlPlaneQueryPathRejectsDotDotSegments(t *testing.T) {
	t.Parallel()
	cfg := controlPlaneQueryPathConfig("/admin/cp/../x")
	err := config.Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "path_prefix") {
		t.Fatalf("expected path_prefix .. rejection, got %v", err)
	}
	if !strings.Contains(err.Error(), "..") {
		t.Fatalf("expected .. segment error, got %v", err)
	}
}

func TestControlPlaneQueryPathOverlapWithDiagnosticsHealthRejected(t *testing.T) {
	t.Parallel()
	cfg := controlPlaneQueryPathConfig("/healthz")
	cfg.Diagnostics = config.DiagnosticsConfig{HealthPath: "/healthz"}
	err := config.Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("expected overlap error vs diagnostics.health_path, got %v", err)
	}
}

func TestControlPlaneQueryPathOverlapWithMetricsRejected(t *testing.T) {
	t.Parallel()
	cfg := controlPlaneQueryPathConfig("/metrics")
	cfg.Observability = config.ObservabilityConfig{
		Metrics: config.MetricsConfig{Enabled: true, Path: "/metrics"},
	}
	err := config.Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("expected overlap error vs observability.metrics.path, got %v", err)
	}
}

func TestControlPlaneQueryPathPrefixOverlapWithDiagnosticsRejected(t *testing.T) {
	t.Parallel()
	// control-plane mount nested under a diagnostics prefix would shadow the
	// diagnostics route; the prefix-overlap branch must reject it.
	cfg := controlPlaneQueryPathConfig("/admin/cp")
	cfg.Diagnostics = config.DiagnosticsConfig{HealthPath: "/admin"}
	err := config.Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("expected prefix overlap error vs diagnostics.health_path, got %v", err)
	}
}

func TestControlPlaneQueryPathDistinctFromOtherMountsValidates(t *testing.T) {
	t.Parallel()
	cfg := controlPlaneQueryPathConfig("/admin/cp")
	cfg.Diagnostics = config.DiagnosticsConfig{HealthPath: "/healthz"}
	cfg.Observability = config.ObservabilityConfig{
		Metrics: config.MetricsConfig{Enabled: true, Path: "/metrics"},
	}
	if err := config.Validate(&cfg); err != nil {
		t.Fatalf("distinct control-plane path must validate, got %v", err)
	}
}

func TestControlPlaneQueryPathNotCheckedWhenQueryDisabled(t *testing.T) {
	t.Parallel()
	// When query is disabled the path is not mounted, so a path containing .. or
	// overlapping another route must NOT be rejected (it is irrelevant).
	cfg := config.Config{
		Server:      config.ServerConfig{Address: "127.0.0.1:8080"},
		Diagnostics: config.DiagnosticsConfig{HealthPath: "/healthz"},
		ControlPlane: config.ControlPlaneConfig{
			Enabled: true,
			Query:   config.ControlPlaneQueryConfig{Enabled: false, PathPrefix: "/healthz/../x"},
		},
	}
	if err := config.Validate(&cfg); err != nil {
		t.Fatalf("disabled query path must not be validated, got %v", err)
	}
}
