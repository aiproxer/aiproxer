package config_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func TestControlPlaneDisabledByDefault(t *testing.T) {
	t.Parallel()
	var cfg config.Config
	if err := config.Validate(&cfg); err != nil {
		t.Fatalf("default config must validate with control-plane disabled: %v", err)
	}
	if cfg.ControlPlane.Enabled {
		t.Fatalf("control_plane.enabled must default to false")
	}
	if cfg.ControlPlane.Query.Enabled {
		t.Fatalf("control_plane.query.enabled must default to false")
	}
	if cfg.ControlPlane.Retention.Enabled {
		t.Fatalf("control_plane.retention.enabled must default to false")
	}
}

func TestControlPlaneEnabledMemoryDefaultsAndValidates(t *testing.T) {
	t.Parallel()
	cfg := config.Config{ControlPlane: config.ControlPlaneConfig{Enabled: true}}
	if err := config.Validate(&cfg); err != nil {
		t.Fatalf("enabled memory control-plane must validate: %v", err)
	}
	if cfg.ControlPlane.Store != "memory" {
		t.Fatalf("store must default to memory when enabled, got %q", cfg.ControlPlane.Store)
	}
	if cfg.ControlPlane.RecordingPolicy != "best_effort" {
		t.Fatalf("recording_policy must default to best_effort, got %q", cfg.ControlPlane.RecordingPolicy)
	}
	if cfg.ControlPlane.RedactionDefault != "standard" {
		t.Fatalf("redaction_default must default to standard, got %q", cfg.ControlPlane.RedactionDefault)
	}
}

func TestControlPlaneQueryDefaultsAppliedWhenEnabled(t *testing.T) {
	t.Parallel()
	cfg := config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:8080"},
		ControlPlane: config.ControlPlaneConfig{
			Enabled: true,
			Query:   config.ControlPlaneQueryConfig{Enabled: true, PathPrefix: "/admin/cp"},
		},
	}
	if err := config.Validate(&cfg); err != nil {
		t.Fatalf("enabled query control-plane must validate: %v", err)
	}
	if cfg.ControlPlane.Query.DefaultPageSize != 100 {
		t.Fatalf("default_page_size must default to 100, got %d", cfg.ControlPlane.Query.DefaultPageSize)
	}
	if cfg.ControlPlane.Query.MaxPageSize != 500 {
		t.Fatalf("max_page_size must default to 500, got %d", cfg.ControlPlane.Query.MaxPageSize)
	}
}

func TestControlPlaneRejectsInvalidStore(t *testing.T) {
	t.Parallel()
	cfg := config.Config{ControlPlane: config.ControlPlaneConfig{Enabled: true, Store: "redis"}}
	err := config.Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "control_plane.store") {
		t.Fatalf("expected control_plane.store error, got %v", err)
	}
}

func TestControlPlaneSQLiteRequiresPath(t *testing.T) {
	t.Parallel()
	cfg := config.Config{ControlPlane: config.ControlPlaneConfig{Enabled: true, Store: "sqlite"}}
	err := config.Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "sqlite_path") {
		t.Fatalf("expected sqlite_path required error, got %v", err)
	}
	cfg.ControlPlane.SQLitePath = "/var/lib/aip/controlplane.db"
	if err := config.Validate(&cfg); err != nil {
		t.Fatalf("sqlite with path must validate: %v", err)
	}
}

func TestControlPlanePostgresRequiresDSNAndForbidsDSNElsewhere(t *testing.T) {
	t.Parallel()
	cfg := config.Config{ControlPlane: config.ControlPlaneConfig{Enabled: true, Store: "postgres"}}
	if err := config.Validate(&cfg); err == nil || !strings.Contains(err.Error(), "postgres_dsn") {
		t.Fatalf("expected postgres_dsn required error, got %v", err)
	}
	cfg.ControlPlane.PostgresDSN = "postgres://u:p@h:5432/db"
	cfg.Database.MaxOpenConns = 8
	if err := config.Validate(&cfg); err != nil {
		t.Fatalf("postgres with dsn must validate: %v", err)
	}
	// dsn set while store is memory must be rejected
	mem := config.Config{ControlPlane: config.ControlPlaneConfig{Enabled: true, Store: "memory", PostgresDSN: "postgres://x"}}
	if err := config.Validate(&mem); err == nil || !strings.Contains(err.Error(), "postgres_dsn") {
		t.Fatalf("expected postgres_dsn-only-when-postgres error, got %v", err)
	}
}

func TestControlPlaneRequiredPreWorkRequiresDurableStore(t *testing.T) {
	t.Parallel()
	cfg := config.Config{ControlPlane: config.ControlPlaneConfig{Enabled: true, RecordingPolicy: "required_pre_work"}}
	err := config.Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "recording_policy") {
		t.Fatalf("expected required_pre_work requires durable store error, got %v", err)
	}
	cfg.ControlPlane.Store = "sqlite"
	cfg.ControlPlane.SQLitePath = "/var/lib/aip/cp.db"
	if err := config.Validate(&cfg); err != nil {
		t.Fatalf("required_pre_work with durable store must validate: %v", err)
	}
}

func TestControlPlaneRequiredCategoriesValidated(t *testing.T) {
	t.Parallel()
	cfg := config.Config{ControlPlane: config.ControlPlaneConfig{Enabled: true, RequiredCategories: []string{"auth", "bogus"}}}
	err := config.Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "required_categories") {
		t.Fatalf("expected required_categories error, got %v", err)
	}
	cfg.ControlPlane.RequiredCategories = []string{"auth", "session", "attempt", "usage", "accounting_authority", "policy", "audit"}
	if err := config.Validate(&cfg); err != nil {
		t.Fatalf("valid required_categories must validate: %v", err)
	}
}

func TestControlPlaneQueryRequiresPathAndDefaults(t *testing.T) {
	t.Parallel()
	cfg := config.Config{
		Server:       config.ServerConfig{Address: "127.0.0.1:8080"},
		ControlPlane: config.ControlPlaneConfig{Enabled: true, Query: config.ControlPlaneQueryConfig{Enabled: true}},
	}
	err := config.Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "path_prefix") {
		t.Fatalf("expected path_prefix required error, got %v", err)
	}
	cfg.ControlPlane.Query.PathPrefix = "/admin/control-plane"
	if err := config.Validate(&cfg); err != nil {
		t.Fatalf("query with path must validate: %v", err)
	}
}

func TestControlPlaneQueryRequiresAbsolutePath(t *testing.T) {
	t.Parallel()
	cfg := config.Config{ControlPlane: config.ControlPlaneConfig{Enabled: true, Query: config.ControlPlaneQueryConfig{Enabled: true, PathPrefix: "admin/cp"}}}
	err := config.Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "path_prefix") {
		t.Fatalf("expected path_prefix absolute error, got %v", err)
	}
}

func TestControlPlaneQueryMaxPageSizeMustBeGEQDefault(t *testing.T) {
	t.Parallel()
	cfg := config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:8080"},
		ControlPlane: config.ControlPlaneConfig{Enabled: true, Query: config.ControlPlaneQueryConfig{
			Enabled: true, PathPrefix: "/admin/cp", DefaultPageSize: 200, MaxPageSize: 100,
		}},
	}
	err := config.Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "max_page_size") {
		t.Fatalf("expected max_page_size >= default error, got %v", err)
	}
	cfg.ControlPlane.Query.MaxPageSize = 200
	if err := config.Validate(&cfg); err != nil {
		t.Fatalf("max==default must validate: %v", err)
	}
}

func TestControlPlaneQueryMaxPageSizePositive(t *testing.T) {
	t.Parallel()
	cfg := config.Config{ControlPlane: config.ControlPlaneConfig{Enabled: true, Query: config.ControlPlaneQueryConfig{
		Enabled: true, PathPrefix: "/admin/cp", MaxPageSize: -1,
	}}}
	err := config.Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "max_page_size") {
		t.Fatalf("expected max_page_size positive error, got %v", err)
	}
}

func TestControlPlaneQueryMaxTimeWindowParses(t *testing.T) {
	t.Parallel()
	cfg := config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:8080"},
		ControlPlane: config.ControlPlaneConfig{Enabled: true, Query: config.ControlPlaneQueryConfig{
			Enabled: true, PathPrefix: "/admin/cp", MaxTimeWindow: "not-a-duration",
		}},
	}
	err := config.Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "max_time_window") {
		t.Fatalf("expected max_time_window parse error, got %v", err)
	}
	cfg.ControlPlane.Query.MaxTimeWindow = "24h"
	if err := config.Validate(&cfg); err != nil {
		t.Fatalf("valid max_time_window must validate: %v", err)
	}
}

func TestControlPlaneQueryMaxTimeWindowZeroRejectedWhenEnabled(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"0", "0s", "0m"} {
		cfg := config.Config{
			Server: config.ServerConfig{Address: "127.0.0.1:8080"},
			ControlPlane: config.ControlPlaneConfig{Enabled: true, Query: config.ControlPlaneQueryConfig{
				Enabled: true, PathPrefix: "/admin/cp", MaxTimeWindow: raw,
			}},
		}
		err := config.Validate(&cfg)
		if err == nil || !strings.Contains(err.Error(), "max_time_window") {
			t.Fatalf("max_time_window=%q must be rejected when query enabled, got %v", raw, err)
		}
	}
}

func TestControlPlaneQueryMaxTimeWindowNegativeRejectedWhenEnabled(t *testing.T) {
	t.Parallel()
	cfg := config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:8080"},
		ControlPlane: config.ControlPlaneConfig{Enabled: true, Query: config.ControlPlaneQueryConfig{
			Enabled: true, PathPrefix: "/admin/cp", MaxTimeWindow: "-24h",
		}},
	}
	err := config.Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "max_time_window") {
		t.Fatalf("negative max_time_window must be rejected when query enabled, got %v", err)
	}
}

func TestControlPlaneQueryMaxTimeWindowEmptyAcceptedWhenEnabled(t *testing.T) {
	t.Parallel()
	cfg := config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:8080"},
		ControlPlane: config.ControlPlaneConfig{Enabled: true, Query: config.ControlPlaneQueryConfig{
			Enabled: true, PathPrefix: "/admin/cp", MaxTimeWindow: "",
		}},
	}
	if err := config.Validate(&cfg); err != nil {
		t.Fatalf("empty max_time_window must validate (means no bound), got %v", err)
	}
	if d, err := cfg.ControlPlane.Query.MaxTimeWindowDuration(); err != nil || d != 0 {
		t.Fatalf("empty max_time_window must resolve to 0 duration, got %v err=%v", d, err)
	}
}

func TestControlPlaneQueryMaxTimeWindowIgnoredWhenQueryDisabled(t *testing.T) {
	t.Parallel()
	cfg := config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:8080"},
		ControlPlane: config.ControlPlaneConfig{Enabled: true, Query: config.ControlPlaneQueryConfig{
			Enabled: false, PathPrefix: "/admin/cp", MaxTimeWindow: "not-a-duration",
		}},
	}
	if err := config.Validate(&cfg); err != nil {
		t.Fatalf("invalid max_time_window must be ignored when query disabled, got %v", err)
	}
}

func TestControlPlaneQueryMaxTimeWindowDurationParsesValid(t *testing.T) {
	t.Parallel()
	q := config.ControlPlaneQueryConfig{Enabled: true, MaxTimeWindow: "24h"}
	d, err := q.MaxTimeWindowDuration()
	if err != nil {
		t.Fatalf("MaxTimeWindowDuration for 24h: unexpected error: %v", err)
	}
	if d != 24*time.Hour {
		t.Fatalf("MaxTimeWindowDuration for 24h must be 24h, got %v", d)
	}
}

func TestControlPlaneQueryMaxTimeWindowDuration_InvalidRejectedWhenEnabled(t *testing.T) {
	t.Parallel()
	q := config.ControlPlaneQueryConfig{Enabled: true, MaxTimeWindow: "not-a-duration"}
	if _, err := q.MaxTimeWindowDuration(); err == nil {
		t.Fatal("enabled query with invalid max_time_window must return an error from MaxTimeWindowDuration")
	}
}

func TestControlPlaneQueryMaxTimeWindowDuration_ZeroRejectedWhenEnabled(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"0", "0s", "0m"} {
		q := config.ControlPlaneQueryConfig{Enabled: true, MaxTimeWindow: raw}
		if _, err := q.MaxTimeWindowDuration(); err == nil {
			t.Fatalf("enabled query with max_time_window=%q must return an error", raw)
		}
	}
}

func TestControlPlaneQueryMaxTimeWindowDuration_NegativeRejectedWhenEnabled(t *testing.T) {
	t.Parallel()
	q := config.ControlPlaneQueryConfig{Enabled: true, MaxTimeWindow: "-24h"}
	if _, err := q.MaxTimeWindowDuration(); err == nil {
		t.Fatal("enabled query with negative max_time_window must return an error")
	}
}

func TestControlPlaneQueryMaxTimeWindowDuration_IgnoredWhenDisabled(t *testing.T) {
	t.Parallel()
	q := config.ControlPlaneQueryConfig{Enabled: false, MaxTimeWindow: "not-a-duration"}
	d, err := q.MaxTimeWindowDuration()
	if err != nil {
		t.Fatalf("disabled query must ignore invalid max_time_window, got err=%v", err)
	}
	if d != 0 {
		t.Fatalf("disabled query must resolve to 0 duration, got %v", d)
	}
}

func TestControlPlaneRetentionRequiresWindow(t *testing.T) {
	t.Parallel()
	cfg := config.Config{ControlPlane: config.ControlPlaneConfig{Enabled: true, Retention: config.ControlPlaneRetentionConfig{Enabled: true}}}
	err := config.Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "retention.window") {
		t.Fatalf("expected retention.window required error, got %v", err)
	}
	cfg.ControlPlane.Retention.Window = "720h"
	if err := config.Validate(&cfg); err != nil {
		t.Fatalf("valid retention must validate: %v", err)
	}
}

func TestControlPlaneRedactionDefaultValidated(t *testing.T) {
	t.Parallel()
	cfg := config.Config{ControlPlane: config.ControlPlaneConfig{Enabled: true, RedactionDefault: "lax"}}
	err := config.Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "redaction_default") {
		t.Fatalf("expected redaction_default error, got %v", err)
	}
	cfg.ControlPlane.RedactionDefault = "strict"
	if err := config.Validate(&cfg); err != nil {
		t.Fatalf("strict redaction_default must validate: %v", err)
	}
}

func TestControlPlaneQueryRequiresControlPlaneEnabled(t *testing.T) {
	t.Parallel()
	cfg := config.Config{ControlPlane: config.ControlPlaneConfig{
		Enabled: false,
		Query:   config.ControlPlaneQueryConfig{Enabled: true, PathPrefix: "/admin/cp"},
	}}
	err := config.Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "control_plane.enabled") {
		t.Fatalf("expected query-without-enabled error, got %v", err)
	}
}

func TestControlPlaneExcludesEnterpriseFeatureConfigSurface(t *testing.T) {
	t.Parallel()
	forbidden := []string{"Billing", "Invoice", "Quota", "Allowance", "SpendCap", "RateLimit", "OAuth", "SAML", "SCIM", "UserDirectory", "PII", "PromptInjection", "Marketplace"}
	rt := reflect.TypeFor[config.ControlPlaneConfig]()
	for field := range rt.Fields() {
		name := field.Name
		for _, bad := range forbidden {
			if strings.Contains(name, bad) {
				t.Fatalf("ControlPlaneConfig field %s introduces excluded enterprise feature %q (requirement 10.1-10.4)", name, bad)
			}
		}
		// descend one level into nested config structs
		if field.Type.Kind() == reflect.Struct {
			nested := field.Type
			for field := range nested.Fields() {
				nname := field.Name
				for _, bad := range forbidden {
					if strings.Contains(nname, bad) {
						t.Fatalf("ControlPlaneConfig.%s field %s introduces excluded enterprise feature %q", name, nname, bad)
					}
				}
			}
		}
	}
}

func TestControlPlaneQueryProtectedByDiagnosticsPosture(t *testing.T) {
	t.Parallel()
	// query enabled on a non-loopback bind without diagnostics shared secret must fail closed
	cfg := config.Config{
		Server: config.ServerConfig{Address: "0.0.0.0:8080"},
		ControlPlane: config.ControlPlaneConfig{
			Enabled: true,
			Query:   config.ControlPlaneQueryConfig{Enabled: true, PathPrefix: "/admin/cp"},
		},
	}
	err := config.ValidateProtectedDiagnosticsPosture(&cfg)
	if err == nil {
		t.Fatalf("control-plane query on non-loopback without shared secret must fail closed")
	}
	if !strings.Contains(err.Error(), "shared_secret") {
		t.Fatalf("expected shared_secret posture error, got %v", err)
	}
}
