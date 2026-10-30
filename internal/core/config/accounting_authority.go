package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// AccountingAuthorityConfig controls the optional usage-authority capability.
// It is disabled by default and only becomes visible when explicitly enabled.
type AccountingAuthorityConfig struct {
	Enabled            bool   `yaml:"enabled"`
	Mode               string `yaml:"mode"`
	Store              string `yaml:"store"`
	SQLitePath         string `yaml:"sqlite_path"`
	PostgresDSN        string `yaml:"postgres_dsn"`
	StartupPosture     string `yaml:"startup_posture"`
	UnknownAttribution string `yaml:"unknown_attribution"`
	EvaluationTimeout  string `yaml:"evaluation_timeout"`
	CleanupTimeout     string `yaml:"cleanup_timeout"`
	// SnapshotVersion is the immutable config-backed policy version (requirement 11.5).
	// Empty defaults to "static" at source construction.
	SnapshotVersion string                          `yaml:"snapshot_version"`
	Query           AccountingAuthorityQueryConfig  `yaml:"query"`
	Rules           []AccountingAuthorityRuleConfig `yaml:"rules"`
	// Quota is an optional provider account-window authority. Its presence is
	// the explicit opt-in; no telemetry is inferred as quota authority when it
	// is omitted.
	Quota *AccountingQuotaConfig `yaml:"quota"`
}

// AccountingQuotaConfig is the host-facing, YAML-friendly source form for one
// immutable provider quota policy. It contains only nonfinancial account-window
// identity, gauge fields, thresholds, and explicit telemetry failure actions.
type AccountingQuotaConfig struct {
	ID                 string                           `yaml:"id"`
	Version            string                           `yaml:"version"`
	Method             string                           `yaml:"method"`
	StoreID            string                           `yaml:"store_id"`
	TenantID           string                           `yaml:"tenant_id"`
	ProviderAccountKey string                           `yaml:"provider_account_key"`
	PoolID             string                           `yaml:"pool_id"`
	WindowID           string                           `yaml:"window_id"`
	ResetAt            string                           `yaml:"reset_at"`
	Freshness          string                           `yaml:"freshness"`
	Required           []metering.ComponentKey          `yaml:"required"`
	Thresholds         []AccountingQuotaThresholdConfig `yaml:"thresholds"`
	Failures           AccountingQuotaFailureConfig     `yaml:"failures"`
}

// AccountingQuotaThresholdConfig keeps exact decimal thresholds as text so
// YAML never routes an allowance comparison through floating point.
type AccountingQuotaThresholdConfig struct {
	Kind      string                `yaml:"kind"`
	Field     metering.ComponentKey `yaml:"field"`
	ValueKind string                `yaml:"value_kind"`
	Value     string                `yaml:"value"`
}

// AccountingQuotaFailureConfig makes every non-complete evidence posture
// independently configurable. Empty actions are normalized by the policy to
// fail closed.
type AccountingQuotaFailureConfig struct {
	Missing     string `yaml:"missing"`
	Stale       string `yaml:"stale"`
	Partial     string `yaml:"partial"`
	Unavailable string `yaml:"unavailable"`
	Mismatch    string `yaml:"mismatch"`
	Future      string `yaml:"future"`
}

// PolicyConfig parses the host-facing quota policy into the public immutable
// policy source form. CompileQuotaPolicy performs the final identity and
// nonfinancial policy validation.
func (q *AccountingQuotaConfig) PolicyConfig() (authority.QuotaPolicyConfig, error) {
	if q == nil {
		return authority.QuotaPolicyConfig{}, fmt.Errorf("accounting.authority.quota: policy is nil")
	}
	freshnessRaw := strings.TrimSpace(q.Freshness)
	if freshnessRaw == "" {
		return authority.QuotaPolicyConfig{}, fmt.Errorf("accounting.authority.quota.freshness: required")
	}
	freshness, err := time.ParseDuration(freshnessRaw)
	if err != nil {
		return authority.QuotaPolicyConfig{}, fmt.Errorf("accounting.authority.quota.freshness: invalid duration %q", q.Freshness)
	}
	resetRaw := strings.TrimSpace(q.ResetAt)
	if resetRaw == "" {
		return authority.QuotaPolicyConfig{}, fmt.Errorf("accounting.authority.quota.reset_at: required")
	}
	resetAt, err := time.Parse(time.RFC3339, resetRaw)
	if err != nil {
		return authority.QuotaPolicyConfig{}, fmt.Errorf("accounting.authority.quota.reset_at: invalid timestamp %q", q.ResetAt)
	}
	thresholds := make([]authority.QuotaThreshold, 0, len(q.Thresholds))
	for i, threshold := range q.Thresholds {
		valueRaw := strings.TrimSpace(threshold.Value)
		if valueRaw == "" {
			return authority.QuotaPolicyConfig{}, fmt.Errorf("accounting.authority.quota.thresholds[%d].value: required", i)
		}
		value, parseErr := metering.ParseDecimal(valueRaw)
		if parseErr != nil {
			return authority.QuotaPolicyConfig{}, fmt.Errorf("accounting.authority.quota.thresholds[%d].value: %w", i, parseErr)
		}
		thresholds = append(thresholds, authority.QuotaThreshold{
			Kind: authority.QuotaThresholdKind(strings.TrimSpace(threshold.Kind)), Field: threshold.Field,
			ValueKind: authority.QuotaValueKind(strings.TrimSpace(threshold.ValueKind)), Value: value,
		})
	}
	return authority.QuotaPolicyConfig{
		ID: strings.TrimSpace(q.ID), Version: strings.TrimSpace(q.Version), Method: strings.TrimSpace(q.Method),
		Binding: authority.QuotaBinding{
			StoreID: strings.TrimSpace(q.StoreID), TenantID: strings.TrimSpace(q.TenantID),
			ProviderAccountKey: strings.TrimSpace(q.ProviderAccountKey), PoolID: strings.TrimSpace(q.PoolID),
			WindowID: strings.TrimSpace(q.WindowID), ResetAt: resetAt.UTC(),
		},
		Freshness: freshness, Required: append([]metering.ComponentKey(nil), q.Required...), Thresholds: thresholds,
		Failures: authority.QuotaFailurePolicy{
			Missing: authority.QuotaFailureAction(strings.TrimSpace(q.Failures.Missing)), Stale: authority.QuotaFailureAction(strings.TrimSpace(q.Failures.Stale)),
			Partial: authority.QuotaFailureAction(strings.TrimSpace(q.Failures.Partial)), Unavailable: authority.QuotaFailureAction(strings.TrimSpace(q.Failures.Unavailable)),
			Mismatch: authority.QuotaFailureAction(strings.TrimSpace(q.Failures.Mismatch)), Future: authority.QuotaFailureAction(strings.TrimSpace(q.Failures.Future)),
		},
	}, nil
}

const (
	DefaultAccountingAuthorityEvaluationTimeout = 250 * time.Millisecond
	DefaultAccountingAuthorityCleanupTimeout    = 2 * time.Second
)

// EvaluationTimeoutDuration returns the bounded admission evaluation budget.
// The zero configuration value is deliberately normalized to the conservative
// default so enabling authority cannot accidentally restore unbounded waits.
func (a AccountingAuthorityConfig) EvaluationTimeoutDuration() (time.Duration, error) {
	raw := strings.TrimSpace(a.EvaluationTimeout)
	if raw == "" {
		return DefaultAccountingAuthorityEvaluationTimeout, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("accounting.authority.evaluation_timeout: invalid duration %q", raw)
	}
	if d <= 0 {
		return 0, fmt.Errorf("accounting.authority.evaluation_timeout: duration must be positive")
	}
	return d, nil
}

// CleanupTimeoutDuration returns the bounded budget for detached settlement,
// release, reconciliation, advisory usage, and compensation work.
func (a AccountingAuthorityConfig) CleanupTimeoutDuration() (time.Duration, error) {
	raw := strings.TrimSpace(a.CleanupTimeout)
	if raw == "" {
		return DefaultAccountingAuthorityCleanupTimeout, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("accounting.authority.cleanup_timeout: invalid duration %q", raw)
	}
	if d <= 0 {
		return 0, fmt.Errorf("accounting.authority.cleanup_timeout: duration must be positive")
	}
	return d, nil
}

// AccountingAuthorityQueryConfig controls the protected status and bounded
// query routes for authority state.
type AccountingAuthorityQueryConfig struct {
	Enabled         bool   `yaml:"enabled"`
	PathPrefix      string `yaml:"path_prefix"`
	DefaultPageSize int    `yaml:"default_page_size"`
	MaxPageSize     int    `yaml:"max_page_size"`
}

// AccountingAuthorityRuleConfig mirrors the pure domain rule shape with
// YAML-friendly primitives.
type AccountingAuthorityRuleConfig struct {
	ID                   string                              `yaml:"id"`
	Kind                 string                              `yaml:"kind"`
	Mode                 string                              `yaml:"mode"`
	Unit                 string                              `yaml:"unit"`
	Limit                int64                               `yaml:"limit"`
	Currency             string                              `yaml:"currency"`
	AuthorityRequirement string                              `yaml:"authority_requirement"`
	FailureBehavior      string                              `yaml:"failure_behavior"`
	Perspective          string                              `yaml:"perspective"`
	LifecycleScope       string                              `yaml:"lifecycle_scope"`
	Basis                string                              `yaml:"basis"`
	Namespace            string                              `yaml:"namespace"`
	Version              string                              `yaml:"version"`
	Window               AccountingAuthorityWindowConfig     `yaml:"window"`
	Match                AccountingAuthorityDimensionsConfig `yaml:"match"`
}

// AccountingAuthorityWindowConfig defines a fixed window using config-native
// duration and timestamp strings.
type AccountingAuthorityWindowConfig struct {
	Algorithm string `yaml:"algorithm"`
	Size      string `yaml:"size"`
	Anchor    string `yaml:"anchor"`
}

// AccountingAuthorityDimensionMatcherConfig preserves unknown versus known-empty
// attribution semantics for one dimension.
type AccountingAuthorityDimensionMatcherConfig struct {
	Value        scope.Value `yaml:"value"`
	MatchUnknown bool        `yaml:"match_unknown"`
}

// AccountingAuthorityDimensionsConfig carries all safe scope dimensions the
// authority may match against.
type AccountingAuthorityDimensionsConfig struct {
	Principal    AccountingAuthorityDimensionMatcherConfig            `yaml:"principal"`
	Credential   AccountingAuthorityDimensionMatcherConfig            `yaml:"credential"`
	Tenant       AccountingAuthorityDimensionMatcherConfig            `yaml:"tenant"`
	Organization AccountingAuthorityDimensionMatcherConfig            `yaml:"organization"`
	Workspace    AccountingAuthorityDimensionMatcherConfig            `yaml:"workspace"`
	Project      AccountingAuthorityDimensionMatcherConfig            `yaml:"project"`
	Department   AccountingAuthorityDimensionMatcherConfig            `yaml:"department"`
	CostCenter   AccountingAuthorityDimensionMatcherConfig            `yaml:"cost_center"`
	Backend      AccountingAuthorityDimensionMatcherConfig            `yaml:"backend"`
	Model        AccountingAuthorityDimensionMatcherConfig            `yaml:"model"`
	Route        AccountingAuthorityDimensionMatcherConfig            `yaml:"route"`
	Labels       map[string]AccountingAuthorityDimensionMatcherConfig `yaml:"labels"`
}

// AuthorityQueryEffectivelyExposed reports whether the protected authority
// surface is configured to mount.
func AuthorityQueryEffectivelyExposed(cfg *Config) bool {
	if cfg == nil {
		return false
	}
	return cfg.Accounting.Authority.Enabled &&
		cfg.Accounting.Authority.Query.Enabled &&
		strings.TrimSpace(cfg.Accounting.Authority.Query.PathPrefix) != ""
}

// The config-to-domain conversion and the rule-semantic validation now live in
// the Enterprise tree (enterprise/internal/infra/usageauthority/configsource).
// The open-source tree keeps the YAML surface, the provider quota window
// policy, the timeout helpers, the query surface, and the configuration shape
// validation only.

func validateAccountingAuthority(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	auth := &cfg.Accounting.Authority
	accountingCategoryRequired := false
	for _, category := range cfg.ControlPlane.RequiredCategories {
		if strings.EqualFold(strings.TrimSpace(category), "accounting_authority") {
			accountingCategoryRequired = true
			break
		}
	}
	if accountingCategoryRequired && !cfg.ControlPlane.Enabled {
		return fmt.Errorf("control_plane.enabled: must be true when accounting_authority is a required category")
	}
	if accountingCategoryRequired && strings.EqualFold(strings.TrimSpace(cfg.ControlPlane.RecordingPolicy), "required_pre_work") && !auth.Enabled {
		return fmt.Errorf("accounting.authority.enabled: must be true when accounting_authority is required under required_pre_work")
	}
	if auth.Quota != nil {
		if err := validateAccountingQuota(auth.Quota); err != nil {
			return err
		}
	}
	if !auth.Enabled {
		if auth.Query.Enabled {
			return fmt.Errorf("accounting.authority.enabled: must be true when accounting.authority.query.enabled is true")
		}
		return nil
	}
	store := strings.ToLower(strings.TrimSpace(auth.Store))
	if store == "" {
		store = "memory"
		auth.Store = store
	}
	switch store {
	case "memory", "sqlite", "postgres":
	default:
		return fmt.Errorf("accounting.authority.store: want memory, sqlite, or postgres, got %q", auth.Store)
	}
	if store == "sqlite" && strings.TrimSpace(auth.SQLitePath) == "" {
		return fmt.Errorf("accounting.authority.sqlite_path: required when store is \"sqlite\"")
	}
	if store == "postgres" && strings.TrimSpace(auth.PostgresDSN) == "" {
		return fmt.Errorf("accounting.authority.postgres_dsn: required when store is \"postgres\"")
	}
	if store != "postgres" && strings.TrimSpace(auth.PostgresDSN) != "" {
		return fmt.Errorf("accounting.authority.postgres_dsn: may only be set when store is \"postgres\" (got %q)", store)
	}
	mode := strings.ToLower(strings.TrimSpace(auth.Mode))
	if mode == "" {
		mode = "strict"
		auth.Mode = mode
	}
	switch mode {
	case "strict", "advisory":
	default:
		return fmt.Errorf("accounting.authority.mode: want strict or advisory, got %q", auth.Mode)
	}
	posture := strings.ToLower(strings.TrimSpace(auth.StartupPosture))
	if posture == "" {
		posture = "fail_closed"
		auth.StartupPosture = posture
	}
	switch posture {
	case "fail_open", "fail_closed":
	default:
		return fmt.Errorf("accounting.authority.startup_posture: want fail_open or fail_closed, got %q", auth.StartupPosture)
	}
	unknown := strings.ToLower(strings.TrimSpace(auth.UnknownAttribution))
	if unknown == "" {
		unknown = "preserve"
		auth.UnknownAttribution = unknown
	}
	switch unknown {
	case "preserve", "unknown", "known_empty":
	default:
		return fmt.Errorf("accounting.authority.unknown_attribution: want preserve, unknown, or known_empty, got %q", auth.UnknownAttribution)
	}
	evaluationTimeout, err := auth.EvaluationTimeoutDuration()
	if err != nil {
		return err
	}
	if strings.TrimSpace(auth.EvaluationTimeout) == "" {
		auth.EvaluationTimeout = evaluationTimeout.String()
	}
	cleanupTimeout, err := auth.CleanupTimeoutDuration()
	if err != nil {
		return err
	}
	if strings.TrimSpace(auth.CleanupTimeout) == "" {
		auth.CleanupTimeout = cleanupTimeout.String()
	}
	if auth.Query.Enabled {
		path := strings.TrimSpace(auth.Query.PathPrefix)
		if path == "" {
			return fmt.Errorf("accounting.authority.query.path_prefix: required when query is enabled")
		}
		if !strings.HasPrefix(path, "/") {
			return fmt.Errorf("accounting.authority.query.path_prefix: must start with /")
		}
		// Admin is only mounted when enabled; a leftover disabled path must not
		// block an authority query prefix that would otherwise be free.
		if cfg.Accounting.Admin.Enabled && strings.TrimSpace(cfg.Accounting.Admin.Path) != "" {
			adminPath := strings.TrimSuffix(strings.TrimSpace(cfg.Accounting.Admin.Path), "/")
			queryPath := strings.TrimSuffix(path, "/")
			if queryPath == adminPath || strings.HasPrefix(queryPath, adminPath+"/") {
				return fmt.Errorf("accounting.authority.query.path_prefix: overlap with accounting.admin.path")
			}
		}
		if strings.TrimSpace(cfg.Diagnostics.HealthPath) != "" && strings.TrimSuffix(path, "/") == strings.TrimSuffix(strings.TrimSpace(cfg.Diagnostics.HealthPath), "/") {
			return fmt.Errorf("accounting.authority.query.path_prefix: overlap with diagnostics.health_path")
		}
		if ControlPlaneQueryEffectivelyExposed(cfg) {
			cpPath := strings.TrimSuffix(strings.TrimSpace(cfg.ControlPlane.Query.PathPrefix), "/")
			queryPath := strings.TrimSuffix(path, "/")
			if queryPath == cpPath || strings.HasPrefix(queryPath, cpPath+"/") || strings.HasPrefix(cpPath, queryPath+"/") {
				return fmt.Errorf("accounting.authority.query.path_prefix: overlap with control_plane.query.path_prefix")
			}
		}
		if auth.Query.DefaultPageSize == 0 {
			auth.Query.DefaultPageSize = 100
		}
		if auth.Query.MaxPageSize == 0 {
			auth.Query.MaxPageSize = 100
		}
		if auth.Query.DefaultPageSize <= 0 {
			return fmt.Errorf("accounting.authority.query.default_page_size: must be positive")
		}
		if auth.Query.MaxPageSize <= 0 {
			return fmt.Errorf("accounting.authority.query.max_page_size: must be positive")
		}
		if auth.Query.MaxPageSize < auth.Query.DefaultPageSize {
			return fmt.Errorf("accounting.authority.query.max_page_size: must be >= default_page_size (%d)", auth.Query.DefaultPageSize)
		}
		if auth.Query.MaxPageSize > 100 {
			return fmt.Errorf("accounting.authority.query.max_page_size: must not exceed 100")
		}
		if strings.TrimSpace(cfg.Diagnostics.SharedSecret) == "" {
			return fmt.Errorf("accounting.authority.query.enabled: requires diagnostics.shared_secret")
		}
	}
	for i := range auth.Rules {
		rule := &auth.Rules[i]
		if retiredMonetaryAuthorityRule(*rule) {
			return fmt.Errorf("accounting.authority.rules[%d]: migration-required: monetary UsageAuthority rules are retired; use billing admission", i)
		}
		if mode == "advisory" && strings.ToLower(strings.TrimSpace(rule.Mode)) == "strict" {
			return fmt.Errorf("accounting.authority.rules[%d].mode: strict rules require accounting.authority.mode=strict", i)
		}
	}
	// The rule semantics (the per-rule domain validation and the
	// AuthorityConfig-level invariants, for example strict rules require
	// atomic backing) are owned by the Enterprise tree. The Enterprise
	// composition validates the rules before it builds the rule source and
	// the store, and fails the startup closed with the same error keys.
	return nil
}

func validateAccountingQuota(quota *AccountingQuotaConfig) error {
	policyConfig, err := quota.PolicyConfig()
	if err != nil {
		return err
	}
	if _, err := authority.CompileQuotaPolicy(policyConfig); err != nil {
		return fmt.Errorf("accounting.authority.quota: %w", err)
	}
	return nil
}

func retiredMonetaryAuthorityRule(rule AccountingAuthorityRuleConfig) bool {
	// Keep the legacy spellings explicit: budget, spend_cap, and money_nano
	// must fail migration-required rather than being reinterpreted as quota.
	normalize := func(value string) string {
		return strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(value)))
	}
	kind := normalize(rule.Kind)
	unit := normalize(rule.Unit)
	return kind == "budget" || kind == "spendcap" || unit == "moneynano"
}
