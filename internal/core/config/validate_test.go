package config_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func TestValidate_rejectsSQLiteWithTTL(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Continuity: config.ContinuityConfig{
			InMemory:   false,
			Store:      "sqlite",
			SQLitePath: ":memory:",
			TTL:        "1h",
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected error for ttl with sqlite store")
	}
}

func TestValidate_rejectsSQLiteWithMaxLegs(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Continuity: config.ContinuityConfig{
			InMemory:   false,
			Store:      "sqlite",
			SQLitePath: ":memory:",
			MaxLegs:    10,
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected error for max_legs with sqlite store")
	}
}

func TestValidate_rejectsMemoryWithNegativeMaxLegs(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Continuity: config.ContinuityConfig{
			InMemory: true,
			MaxLegs:  -1,
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("expected error for negative max_legs with memory store")
	}
	if !strings.Contains(err.Error(), "max_legs") {
		t.Fatalf("error: %v", err)
	}
}

func TestValidate_allowsMemoryZeroAndPositiveMaxLegs(t *testing.T) {
	t.Parallel()
	for _, limit := range []int{0, 42} {
		t.Run(fmt.Sprintf("max_legs_%d", limit), func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{
				Continuity: config.ContinuityConfig{
					InMemory: true,
					MaxLegs:  limit,
				},
				Plugins: config.PluginsConfig{
					Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
				},
			}
			if err := config.Validate(cfg); err != nil {
				t.Fatalf("max_legs=%d: %v", limit, err)
			}
		})
	}
}

// TestValidate_acceptsOmittedContinuityBlock proves the configuration an operator
// gets by omitting the continuity block entirely is valid: the store defaults to
// the process-local backing, and nothing about the default is refused.
func TestValidate_acceptsOmittedContinuityBlock(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("omitted continuity block must validate: %v", err)
	}
	if got := config.EffectiveContinuityStore(cfg.Continuity); got != config.ContinuityStoreMemory {
		t.Fatalf("effective store %q want %q", got, config.ContinuityStoreMemory)
	}
}

// TestValidate_rejectsUnparsableMemoryTTL proves the memory backing's eviction
// window is refused by the configuration validator, not only when the store is
// constructed. check-config and the serving runtime must refuse the same value.
func TestValidate_rejectsUnparsableMemoryTTL(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Continuity: config.ContinuityConfig{
			InMemory: true,
			TTL:      "not-a-duration",
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("expected error for an unparsable continuity ttl")
	}
	if !strings.Contains(err.Error(), "continuity.ttl") {
		t.Fatalf("error: %v", err)
	}
}

func TestValidate_rejectsNegativeMemoryTTL(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Continuity: config.ContinuityConfig{
			InMemory: true,
			TTL:      "-1h",
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("expected error for a negative continuity ttl")
	}
	if !strings.Contains(err.Error(), "continuity.ttl") {
		t.Fatalf("error: %v", err)
	}
}

func TestValidate_rejectsDuplicatePluginInstanceID(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "dup", Enabled: true},
				{ID: "dup", Enabled: false},
			},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected duplicate instance id error")
	}
}

func TestValidate_rejectsCircuitBreakerEnabledWithZeroThreshold(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing: config.RoutingConfig{
			Health: config.RoutingHealthConfig{
				CircuitBreaker: config.CircuitBreakerConfig{
					Enabled:          true,
					FailureThreshold: 0,
				},
			},
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected failure_threshold error")
	}
}

func TestValidate_rejectsCircuitBreakerNonPositiveOpenFor(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing: config.RoutingConfig{
			Health: config.RoutingHealthConfig{
				CircuitBreaker: config.CircuitBreakerConfig{
					Enabled:          true,
					FailureThreshold: 2,
					OpenFor:          "0s",
				},
			},
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected open_for positive duration error")
	}
}

func TestValidate_loggingDefaultsAndRejectsUnknownLevel(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "bogus"},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected unknown logging level error")
	}
	cfg2 := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg2); err != nil {
		t.Fatal(err)
	}
	if strings.ToLower(cfg2.Logging.Level) != "info" {
		t.Fatalf("level: got %q", cfg2.Logging.Level)
	}
	if strings.ToLower(cfg2.Logging.Format) != "json" {
		t.Fatalf("format: got %q", cfg2.Logging.Format)
	}
}

func TestValidate_rejectsUnknownLoggingFormat(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Logging: config.LoggingConfig{Level: "info", Format: "xml"},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected unknown format error")
	}
}

func TestValidate_rejectsAccessLogSkipPathNotAbsolute(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:              "info",
			Format:             "json",
			AccessLogSkipPaths: []string{"healthz"},
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected skip path error")
	}
}

func TestValidate_rejectsEmptyAccessLogSkipPathEntry(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Level:              "info",
			Format:             "json",
			AccessLogSkipPaths: []string{"/ok", "  "},
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected empty skip path error")
	}
}

func TestValidate_rejectsCircuitBreakerInvalidOpenFor(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing: config.RoutingConfig{
			Health: config.RoutingHealthConfig{
				CircuitBreaker: config.CircuitBreakerConfig{
					Enabled:          true,
					FailureThreshold: 2,
					OpenFor:          "not-a-duration",
				},
			},
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected open_for parse error")
	}
}

func TestValidate_rejectsDuplicateDiagnosticPaths(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Diagnostics: config.DiagnosticsConfig{
			HealthPath:   "/healthz",
			AttemptsPath: "/healthz",
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected duplicate diagnostic path error")
	}
}

func TestValidate_rejectsDiagnosticHTTPPathDotDotSegments(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Diagnostics: config.DiagnosticsConfig{
			HealthPath:   "/healthz",
			AttemptsPath: "/admin/../attempts",
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected diagnostics path .. segment error")
	}
}

func TestValidate_rejectsOverlappingDiagnosticPathPrefix(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Diagnostics: config.DiagnosticsConfig{
			HealthPath:    "/debug",
			InventoryPath: "/debug/inventory",
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected overlapping diagnostic path error")
	}
}

func TestValidate_rejectsPprofPathWithoutLeadingSlash(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Diagnostics: config.DiagnosticsConfig{
			PprofPath: "debug/pprof",
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected pprof_path must start with /")
	}
}

func TestValidate_rejectsSQLitePathQueryChars(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Continuity: config.ContinuityConfig{
			InMemory:   false,
			Store:      "sqlite",
			SQLitePath: "./data/x?bad=1",
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected sqlite_path invalid char error")
	}
}

func TestValidate_rejectsEmptySQLitePath(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Continuity: config.ContinuityConfig{
			InMemory:   false,
			Store:      "sqlite",
			SQLitePath: "   ",
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected sqlite_path required error")
	}
}

func TestValidate_rejectsShortDiagnosticsSharedSecret(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Diagnostics: config.DiagnosticsConfig{
			SharedSecret: "short",
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected shared_secret length error")
	}
}

func TestValidate_observabilityMetricsVsHealthPathOverlap(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Observability: config.ObservabilityConfig{
			Metrics: config.MetricsConfig{Enabled: true, Path: "/healthz"},
		},
		Diagnostics: config.DiagnosticsConfig{HealthPath: "/healthz"},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected overlap between metrics path and health path")
	}
}

func TestValidate_observabilityMetricsPathSlash(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Observability: config.ObservabilityConfig{
			Metrics: config.MetricsConfig{Enabled: true, Path: "metrics"},
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected metrics path must start with /")
	}
}

func TestValidate_observabilityTracingSampleRatioOutOfRange(t *testing.T) {
	t.Parallel()
	bad := 1.5
	cfg := &config.Config{
		Observability: config.ObservabilityConfig{
			Tracing: config.TracingConfig{Enabled: true, SampleRatio: &bad},
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected sample_ratio out of range error")
	}
}

func TestValidate_observabilityTracingSampleRatioZero(t *testing.T) {
	t.Parallel()
	bad := 0.0
	cfg := &config.Config{
		Observability: config.ObservabilityConfig{
			Tracing: config.TracingConfig{Enabled: true, SampleRatio: &bad},
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected sample_ratio > 0 error")
	}
}

func TestValidate_observabilityTracingSampleRatioHalfOK(t *testing.T) {
	t.Parallel()
	half := 0.5
	cfg := &config.Config{
		Observability: config.ObservabilityConfig{
			Tracing: config.TracingConfig{Enabled: true, SampleRatio: &half},
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestValidate_serverBadDuration(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Server: config.ServerConfig{
			ReadTimeout: "not-a-duration",
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected error for bad server.read_timeout")
	}
}

func TestValidate_serverNegativeMaxPendingWireEvents(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Server: config.ServerConfig{
			MaxPendingWireEvents: -1,
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected error for negative max_pending_wire_events")
	}
}

func TestValidate_httpClientBadDuration(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		HTTPClient: config.HTTPClientConfig{
			ClientTimeout: "not-a-duration",
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected invalid client_timeout")
	}
}

func TestEffectiveTrustEnvironmentProxy(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	if !cfg.EffectiveTrustEnvironmentProxy() {
		t.Fatal("default should trust environment proxy")
	}
	f := false
	cfg.HTTPClient.TrustEnvironmentProxy = &f
	if cfg.EffectiveTrustEnvironmentProxy() {
		t.Fatal("expected false when explicitly disabled")
	}
}
