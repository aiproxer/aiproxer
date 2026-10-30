package config

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// ControlPlaneConfig controls the optional control-plane persistence, query,
// and event-ledger capability (spec control-plane-persistence-query-event-ledger).
// The capability is disabled by default; enabling recording or query exposure
// requires explicit typed configuration and startup validation here.
//
// Excluded enterprise features (billing, identity provisioning, policy engines,
// GUI, marketplace, provider forwarding, historical migration) intentionally
// have no configuration surface here (requirements 10.1–10.6).
type ControlPlaneConfig struct {
	Enabled            bool                        `yaml:"enabled"`
	Store              string                      `yaml:"store"`
	SQLitePath         string                      `yaml:"sqlite_path"`
	PostgresDSN        string                      `yaml:"postgres_dsn"`
	RecordingPolicy    string                      `yaml:"recording_policy"`
	RequiredCategories []string                    `yaml:"required_categories"`
	Query              ControlPlaneQueryConfig     `yaml:"query"`
	Retention          ControlPlaneRetentionConfig `yaml:"retention"`
	RedactionDefault   string                      `yaml:"redaction_default"`
}

// ControlPlaneQueryConfig controls protected operator query exposure. Query
// routes mount only when control-plane is enabled, query is enabled, and the
// diagnostics shared-secret posture allows protected surfaces.
type ControlPlaneQueryConfig struct {
	Enabled         bool   `yaml:"enabled"`
	PathPrefix      string `yaml:"path_prefix"`
	DefaultPageSize int    `yaml:"default_page_size"`
	MaxPageSize     int    `yaml:"max_page_size"`
	MaxTimeWindow   string `yaml:"max_time_window"`
}

// MaxTimeWindowDuration returns the effective query max time window as a
// [time.Duration] plus any validation error.
//
// Disabled-query semantics: when q.Enabled is false the query surface is not
// mounted, so an invalid MaxTimeWindow is ignored and (0, nil) is returned even
// if the value is unparseable.
//
// When q.Enabled is true, an empty MaxTimeWindow returns (0, nil) (meaning no
// bound; the query service applies its own defaults). A non-empty value must
// parse to a positive duration; invalid, zero, and negative durations return an
// error with the same wording used by [validateControlPlaneQuery] so callers
// that skip [Validate] (for example runtimebundle assembly fed an unvalidated
// config) still fail fast instead of silently degrading to an unbounded query
// service.
func (q ControlPlaneQueryConfig) MaxTimeWindowDuration() (time.Duration, error) {
	if !q.Enabled {
		return 0, nil
	}
	raw := strings.TrimSpace(q.MaxTimeWindow)
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("control_plane.query.max_time_window: invalid duration %q", raw)
	}
	if d <= 0 {
		return 0, fmt.Errorf("control_plane.query.max_time_window: duration must be positive")
	}
	return d, nil
}

// ControlPlaneRetentionConfig controls optional retention/redaction processing.
// No hidden background worker is started unless explicitly configured later.
type ControlPlaneRetentionConfig struct {
	Enabled bool   `yaml:"enabled"`
	Window  string `yaml:"window"`
}

// legalControlPlaneCategories is the set of lifecycle categories that may be
// marked as required recording (design "Configuration and Readiness Contract").
var legalControlPlaneCategories = []string{"auth", "session", "attempt", "usage", "accounting_authority", "policy", "audit"}

// validateControlPlane normalizes defaults and rejects invalid combinations
// for the control-plane capability (requirement 2.6, 2.9, 5.4, 5.5, 7.1, 7.4,
// 7.5, 7.6, 10.1–10.4).
func validateControlPlane(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	cp := &cfg.ControlPlane
	if !cp.Enabled {
		// When disabled, query exposure must not be requested; everything else
		// is ignored and runtimebundle reports a disabled capability.
		if cp.Query.Enabled {
			return fmt.Errorf("control_plane.enabled: must be true when control_plane.query.enabled is true")
		}
		if cp.Retention.Enabled {
			return fmt.Errorf("control_plane.enabled: must be true when control_plane.retention.enabled is true")
		}
		return nil
	}

	store := strings.ToLower(strings.TrimSpace(cp.Store))
	if store == "" {
		store = "memory"
		cp.Store = store
	}
	switch store {
	case "memory", "sqlite", "postgres":
	default:
		return fmt.Errorf("control_plane.store: want memory, sqlite, or postgres, got %q", cp.Store)
	}
	if store == "sqlite" && strings.TrimSpace(cp.SQLitePath) == "" {
		return fmt.Errorf("control_plane.sqlite_path: required when store is \"sqlite\"")
	}
	if store == "postgres" && strings.TrimSpace(cp.PostgresDSN) == "" {
		return fmt.Errorf("control_plane.postgres_dsn: required when store is \"postgres\"")
	}
	if store != "postgres" && strings.TrimSpace(cp.PostgresDSN) != "" {
		return fmt.Errorf("control_plane.postgres_dsn: may only be set when store is \"postgres\" (got %q)", store)
	}

	policy := strings.ToLower(strings.TrimSpace(cp.RecordingPolicy))
	if policy == "" {
		policy = "best_effort"
		cp.RecordingPolicy = policy
	}
	switch policy {
	case "best_effort", "required_pre_work":
	default:
		return fmt.Errorf("control_plane.recording_policy: want best_effort or required_pre_work, got %q", cp.RecordingPolicy)
	}
	// Durable policies require a durable store (design: required_pre_work fails
	// startup unless store readiness succeeds; memory is not durable).
	if policy == "required_pre_work" && store == "memory" {
		return fmt.Errorf("control_plane.recording_policy: required_pre_work requires a durable store (sqlite or postgres)")
	}

	for i, cat := range cp.RequiredCategories {
		c := strings.ToLower(strings.TrimSpace(cat))
		if !slices.Contains(legalControlPlaneCategories, c) {
			return fmt.Errorf("control_plane.required_categories: unknown category %q (want one of %s)", cat, strings.Join(legalControlPlaneCategories, ", "))
		}
		cp.RequiredCategories[i] = c
	}

	redaction := strings.ToLower(strings.TrimSpace(cp.RedactionDefault))
	if redaction == "" {
		redaction = "standard"
		cp.RedactionDefault = redaction
	}
	switch redaction {
	case "standard", "strict":
	default:
		return fmt.Errorf("control_plane.redaction_default: want standard or strict, got %q", cp.RedactionDefault)
	}

	if err := validateControlPlaneQuery(&cp.Query); err != nil {
		return err
	}
	return validateControlPlaneRetention(&cp.Retention)
}

func validateControlPlaneQuery(q *ControlPlaneQueryConfig) error {
	if !q.Enabled {
		return nil
	}
	path := strings.TrimSpace(q.PathPrefix)
	if path == "" {
		return fmt.Errorf("control_plane.query.path_prefix: required when query is enabled")
	}
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("control_plane.query.path_prefix: must start with /")
	}
	if q.DefaultPageSize == 0 {
		q.DefaultPageSize = 100
	}
	if q.MaxPageSize == 0 {
		q.MaxPageSize = 500
	}
	if q.DefaultPageSize <= 0 {
		return fmt.Errorf("control_plane.query.default_page_size: must be positive")
	}
	if q.MaxPageSize <= 0 {
		return fmt.Errorf("control_plane.query.max_page_size: must be positive")
	}
	if q.MaxPageSize < q.DefaultPageSize {
		return fmt.Errorf("control_plane.query.max_page_size: must be >= default_page_size (%d)", q.DefaultPageSize)
	}
	if _, err := q.MaxTimeWindowDuration(); err != nil {
		return err
	}
	return nil
}

func validateControlPlaneRetention(r *ControlPlaneRetentionConfig) error {
	if !r.Enabled {
		return nil
	}
	raw := strings.TrimSpace(r.Window)
	if raw == "" {
		return fmt.Errorf("control_plane.retention.window: required when retention is enabled")
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("control_plane.retention.window: invalid duration %q", raw)
	}
	if d <= 0 {
		return fmt.Errorf("control_plane.retention.window: duration must be positive")
	}
	return nil
}

// ControlPlaneQueryEffectivelyExposed reports whether the protected control-plane
// query surface is configured for exposure. Used by diagnostics posture checks
// and runtimebundle wiring to decide mounting and fail-closed behavior.
func ControlPlaneQueryEffectivelyExposed(cfg *Config) bool {
	if cfg == nil {
		return false
	}
	return cfg.ControlPlane.Enabled && cfg.ControlPlane.Query.Enabled &&
		strings.TrimSpace(cfg.ControlPlane.Query.PathPrefix) != ""
}
