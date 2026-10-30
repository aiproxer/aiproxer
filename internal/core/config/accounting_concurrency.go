package config

import (
	"fmt"
	"strings"
	"time"

	concurrencydomain "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
)

const (
	DefaultConcurrencyLeaseTTL    = 60 * time.Second
	DefaultConcurrencyRenewBefore = 15 * time.Second
)

// ConcurrencyAuthorityConfig controls optional logical-request concurrency leases.
// Disabled by default (requirement 10.4 wiring is opt-in).
type ConcurrencyAuthorityConfig struct {
	Enabled     bool   `yaml:"enabled"`
	Store       string `yaml:"store"` // memory | sqlite | postgres
	StoreID     string `yaml:"store_id"`
	SQLitePath  string `yaml:"sqlite_path"`
	PostgresDSN string `yaml:"postgres_dsn"`
	LeaseTTL    string `yaml:"lease_ttl"`
	RenewBefore string `yaml:"renew_before"`
	// SnapshotVersion is the immutable config-backed concurrency policy version (11.5).
	// Empty defaults to "static" at source construction.
	SnapshotVersion string `yaml:"snapshot_version"`
	// AuxiliaryLeasePolicy controls whether auxiliary requests inherit the parent
	// lease (default) or acquire their own top-level slot (requirement 10.10).
	// Values: ""|"inherit" (default) | "acquire_own".
	AuxiliaryLeasePolicy string                           `yaml:"auxiliary_lease_policy"`
	Rules                []ConcurrencyAuthorityRuleConfig `yaml:"rules"`
}

// ConcurrencyAuthorityRuleConfig is one max-active-request lease rule.
type ConcurrencyAuthorityRuleConfig struct {
	ID                string                              `yaml:"id"`
	Mode              string                              `yaml:"mode"` // strict | advisory
	MaxActiveRequests int                                 `yaml:"max_active_requests"`
	Match             AccountingAuthorityDimensionsConfig `yaml:"match"`
	LeaseTTL          string                              `yaml:"lease_ttl"`
	RenewBefore       string                              `yaml:"renew_before"`
	FailureBehavior   string                              `yaml:"failure_behavior"` // fail_closed | fail_open
	Namespace         string                              `yaml:"namespace"`
	Version           string                              `yaml:"version"`
}

// LeaseTTLDuration returns the default lease TTL for rules that omit lease_ttl.
func (c ConcurrencyAuthorityConfig) LeaseTTLDuration() (time.Duration, error) {
	return parsePositiveDurationOrDefault(c.LeaseTTL, DefaultConcurrencyLeaseTTL, "accounting.concurrency.lease_ttl")
}

// RenewBeforeDuration returns the default renew-before offset.
func (c ConcurrencyAuthorityConfig) RenewBeforeDuration() (time.Duration, error) {
	return parsePositiveDurationOrDefault(c.RenewBefore, DefaultConcurrencyRenewBefore, "accounting.concurrency.renew_before")
}

func parsePositiveDurationOrDefault(raw string, def time.Duration, field string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration %q", field, raw)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s: duration must be positive", field)
	}
	return d, nil
}

// DomainRules converts validated concurrency config into domain rules.
func (c ConcurrencyAuthorityConfig) DomainRules() ([]concurrencydomain.Rule, error) {
	defaultTTL, err := c.LeaseTTLDuration()
	if err != nil {
		return nil, err
	}
	defaultRenew, err := c.RenewBeforeDuration()
	if err != nil {
		return nil, err
	}
	out := make([]concurrencydomain.Rule, 0, len(c.Rules))
	for i, rule := range c.Rules {
		id := strings.TrimSpace(rule.ID)
		if id == "" {
			return nil, fmt.Errorf("accounting.concurrency.rules[%d].id: required", i)
		}
		mode := concurrencydomain.RuleMode(strings.ToLower(strings.TrimSpace(rule.Mode)))
		if mode == "" {
			mode = concurrencydomain.RuleModeStrict
		}
		if !mode.IsKnown() {
			return nil, fmt.Errorf("accounting.concurrency.rules[%d].mode: want strict or advisory, got %q", i, rule.Mode)
		}
		if rule.MaxActiveRequests <= 0 {
			return nil, fmt.Errorf("accounting.concurrency.rules[%d].max_active_requests: must be > 0", i)
		}
		ttl := defaultTTL
		if strings.TrimSpace(rule.LeaseTTL) != "" {
			ttl, err = parsePositiveDurationOrDefault(rule.LeaseTTL, defaultTTL, fmt.Sprintf("accounting.concurrency.rules[%d].lease_ttl", i))
			if err != nil {
				return nil, err
			}
		}
		renew := defaultRenew
		if strings.TrimSpace(rule.RenewBefore) != "" {
			renew, err = parsePositiveDurationOrDefault(rule.RenewBefore, defaultRenew, fmt.Sprintf("accounting.concurrency.rules[%d].renew_before", i))
			if err != nil {
				return nil, err
			}
		}
		failBeh := concurrencydomain.FailureBehavior(strings.ToLower(strings.TrimSpace(rule.FailureBehavior)))
		if failBeh == "" {
			failBeh = concurrencydomain.FailureBehaviorFailClosed
		}
		if !failBeh.IsKnown() {
			return nil, fmt.Errorf("accounting.concurrency.rules[%d].failure_behavior: want fail_closed or fail_open, got %q", i, rule.FailureBehavior)
		}
		version := strings.TrimSpace(rule.Version)
		if version == "" {
			version = "v1"
		}
		out = append(out, concurrencydomain.Rule{
			ID:              id,
			Namespace:       strings.TrimSpace(rule.Namespace),
			Version:         version,
			Mode:            mode,
			Limit:           rule.MaxActiveRequests,
			Match:           concurrencyDimensionsFromConfig(rule.Match),
			LeaseTTL:        ttl,
			RenewBefore:     renew,
			FailureBehavior: failBeh,
		})
	}
	return out, nil
}

func concurrencyDimensionsFromConfig(m AccountingAuthorityDimensionsConfig) concurrencydomain.DimensionsMatcher {
	out := concurrencydomain.DimensionsMatcher{
		Principal:    concurrencyMatcher(m.Principal),
		Credential:   concurrencyMatcher(m.Credential),
		Tenant:       concurrencyMatcher(m.Tenant),
		Organization: concurrencyMatcher(m.Organization),
		Workspace:    concurrencyMatcher(m.Workspace),
		Project:      concurrencyMatcher(m.Project),
		Department:   concurrencyMatcher(m.Department),
		CostCenter:   concurrencyMatcher(m.CostCenter),
		Backend:      concurrencyMatcher(m.Backend),
		Model:        concurrencyMatcher(m.Model),
		Route:        concurrencyMatcher(m.Route),
	}
	if len(m.Labels) > 0 {
		out.Labels = make(map[string]concurrencydomain.DimensionMatcher, len(m.Labels))
		for key, matcher := range m.Labels {
			out.Labels[key] = concurrencyMatcher(matcher)
		}
	}
	return out
}

func concurrencyMatcher(m AccountingAuthorityDimensionMatcherConfig) concurrencydomain.DimensionMatcher {
	return concurrencydomain.DimensionMatcher{
		Value:        m.Value,
		MatchUnknown: m.MatchUnknown,
	}
}

func validateAccountingConcurrency(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	c := &cfg.Accounting.Concurrency
	if !c.Enabled {
		return nil
	}
	store := strings.ToLower(strings.TrimSpace(c.Store))
	switch store {
	case "", "memory":
		c.Store = "memory"
	case "sqlite":
		if strings.TrimSpace(c.SQLitePath) == "" {
			return fmt.Errorf("accounting.concurrency.sqlite_path: required when store is \"sqlite\"")
		}
	case "postgres":
		if strings.TrimSpace(c.PostgresDSN) == "" {
			return fmt.Errorf("accounting.concurrency.postgres_dsn: required when store is \"postgres\"")
		}
	default:
		return fmt.Errorf("accounting.concurrency.store: want memory, sqlite, or postgres, got %q", c.Store)
	}
	if strings.TrimSpace(c.StoreID) == "" {
		c.StoreID = "default"
	}
	if _, err := c.LeaseTTLDuration(); err != nil {
		return err
	}
	if _, err := c.RenewBeforeDuration(); err != nil {
		return err
	}
	if _, err := c.DomainRules(); err != nil {
		return err
	}
	if c.LeaseTTL == "" {
		c.LeaseTTL = DefaultConcurrencyLeaseTTL.String()
	}
	if c.RenewBefore == "" {
		c.RenewBefore = DefaultConcurrencyRenewBefore.String()
	}
	switch strings.ToLower(strings.TrimSpace(c.AuxiliaryLeasePolicy)) {
	case "", "inherit":
		c.AuxiliaryLeasePolicy = "inherit"
	case "acquire_own":
		c.AuxiliaryLeasePolicy = "acquire_own"
	default:
		return fmt.Errorf("accounting.concurrency.auxiliary_lease_policy: want inherit or acquire_own, got %q", c.AuxiliaryLeasePolicy)
	}
	return nil
}
