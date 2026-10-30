package config_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// billingCapability and usageLimitsCapability declare the matching capability
// so a test exercises the intended accounting semantics rather than the
// Enterprise capability gate (open-core-billing-split 2.3/4.1).
func billingCapability() config.CapabilitySet {
	return config.CapabilitySet{config.CapabilityBilling: true}
}

func usageLimitsCapability() config.CapabilitySet {
	return config.CapabilitySet{config.CapabilityUsageLimits: true}
}

// dualPlaneRuleConfig returns one valid dual-plane usage-limit rule; callers add
// it to a config whose authority is enabled.
func dualPlaneRuleConfig() config.AccountingAuthorityRuleConfig {
	return config.AccountingAuthorityRuleConfig{
		ID:             "customer.requests",
		Kind:           "quota",
		Mode:           "strict",
		Unit:           "requests",
		Limit:          10,
		Perspective:    "customer",
		LifecycleScope: "logical_request",
		Basis:          "frontend_ingress",
		Namespace:      "usage-authority/v2",
		Version:        "1",
		Match: config.AccountingAuthorityDimensionsConfig{
			Backend: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("backend-1")},
		},
	}
}

func accountingConfigWithRule() config.AccountingConfig {
	return config.AccountingConfig{
		Authority: config.AccountingAuthorityConfig{
			Enabled:        true,
			Mode:           "strict",
			Store:          "memory",
			StartupPosture: "fail_closed",
			Rules:          []config.AccountingAuthorityRuleConfig{dualPlaneRuleConfig()},
		},
	}
}

// TestValidateWithCapabilities_EnterpriseOnlyAccountingKeys is the 2.3/4.1 gate
// decision table: each Enterprise-only key is rejected without its capability
// (naming the missing one), accepted with it, and indifferent to a different
// capability. A config without Enterprise keys never requires a capability, and
// the open-source provider quota policy stays ungated.
func TestValidateWithCapabilities_EnterpriseOnlyAccountingKeys(t *testing.T) {
	t.Parallel()
	withPricing := func(c *config.Config) { c.Accounting.Pricing.Currency = "USD" }
	withBillingReports := func(c *config.Config) { c.Accounting.Billing.ReportsPath = "/admin/billing-custom" }
	withRules := func(c *config.Config) { c.Accounting = accountingConfigWithRule() }
	cases := []struct {
		name      string
		mutate    func(*config.Config)
		caps      config.CapabilitySet
		wantError string
	}{
		{name: "no enterprise keys requires no capability"},
		{
			name: "pricing rejected without billing", mutate: withPricing,
			wantError: `config: accounting.pricing requires AIProxer Enterprise (capability "billing")`,
		},
		{name: "pricing accepted with billing", mutate: withPricing, caps: billingCapability()},
		{
			name: "pricing rejected with only usage-limits", mutate: withPricing, caps: usageLimitsCapability(),
			wantError: `config: accounting.pricing requires AIProxer Enterprise (capability "billing")`,
		},
		{
			name: "pricing models are a request without currency", mutate: func(c *config.Config) {
				c.Accounting.Pricing.Models = []config.AccountingModelPriceConfig{{Backend: "b", Model: "m", InputPer1M: "1", OutputPer1M: "2"}}
			},
			wantError: `config: accounting.pricing requires AIProxer Enterprise (capability "billing")`,
		},
		{
			name: "billing reports path rejected without billing", mutate: withBillingReports,
			wantError: `config: accounting.billing requires AIProxer Enterprise (capability "billing")`,
		},
		{name: "billing reports path accepted with billing", mutate: withBillingReports, caps: billingCapability()},
		{
			name: "usage-limit rules rejected without usage-limits", mutate: withRules,
			wantError: `config: accounting.authority.rules requires AIProxer Enterprise (capability "usage-limits")`,
		},
		{name: "usage-limit rules accepted with usage-limits", mutate: withRules, caps: usageLimitsCapability()},
		{
			name: "usage-limit rules rejected with only billing", mutate: withRules, caps: billingCapability(),
			wantError: `config: accounting.authority.rules requires AIProxer Enterprise (capability "usage-limits")`,
		},
		{
			name: "disabled authority with non-empty rules is still a request", mutate: func(c *config.Config) {
				withRules(c)
				c.Accounting.Authority.Enabled = false
			},
			wantError: `config: accounting.authority.rules requires AIProxer Enterprise (capability "usage-limits")`,
		},
		{
			name: "provider quota policy stays open source", mutate: func(c *config.Config) {
				reset := time.Date(2026, time.January, 3, 11, 0, 0, 0, time.UTC)
				field := metering.ComponentKey{Direction: metering.DirectionNone, Component: "provider:utilization", Unit: metering.UnitPercent, SchemaID: "quota.v1"}
				c.Accounting.Authority.Quota = &config.AccountingQuotaConfig{
					ID: "provider-quota", Version: "2026-01", StoreID: "metering-1", TenantID: "tenant-1",
					ProviderAccountKey: "account-a", PoolID: "primary", WindowID: "minute", ResetAt: reset.Format(time.RFC3339),
					Freshness: "5m", Required: []metering.ComponentKey{field},
					Thresholds: []config.AccountingQuotaThresholdConfig{{
						Kind: string(authority.QuotaThresholdMaxUtilization), Field: field,
						ValueKind: string(authority.QuotaValuePercent), Value: "80",
					}},
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var cfg config.Config
			if tc.mutate != nil {
				tc.mutate(&cfg)
			}
			err := config.ValidateWithCapabilities(&cfg, tc.caps)
			if tc.wantError == "" {
				if err != nil {
					t.Fatalf("ValidateWithCapabilities(%v) = %v, want success", tc.caps, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want %q", err, tc.wantError)
			}
		})
	}
}

// TestValidateWithCapabilities_PresencePredicate covers the strict-YAML
// presence edge cases through the real decode path: omitted/null/empty blocks
// decode to zero and are not a request, while any non-empty monetary or rule
// value is.
func TestValidateWithCapabilities_PresencePredicate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		raw       string
		wantError string
	}{
		{name: "empty accounting block", raw: "accounting: {}\n"},
		{name: "empty rules", raw: "accounting:\n  authority:\n    rules: []\n"},
		{name: "enabled authority without rules", raw: "accounting:\n  authority:\n    enabled: true\n"},
		{
			name: "nonempty currency", raw: "accounting:\n  pricing:\n    currency: USD\n",
			wantError: `accounting.pricing requires AIProxer Enterprise (capability "billing")`,
		},
		{
			name: "nonempty reports path", raw: "accounting:\n  billing:\n    reports_path: /admin/billing-custom\n",
			wantError: `accounting.billing requires AIProxer Enterprise (capability "billing")`,
		},
		{
			name: "nonempty rules while disabled", raw: "accounting:\n  authority:\n    enabled: false\n    rules:\n      - id: r1\n",
			wantError: `accounting.authority.rules requires AIProxer Enterprise (capability "usage-limits")`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := config.LoadEffective(context.Background(), []byte(tc.raw), config.LoadEffectiveOptions{})
			if tc.wantError == "" {
				if err != nil {
					t.Fatalf("empty Enterprise block %q must not require a capability: %v", tc.raw, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("LoadEffective without capabilities = %v, want %q", err, tc.wantError)
			}
		})
	}
}

// TestLoadEffectiveCarriesCapabilities proves the effective-load pipeline
// consumes LoadEffectiveOptions.Capabilities: the same bytes fail closed
// without it and validate with it.
func TestLoadEffectiveCarriesCapabilities(t *testing.T) {
	t.Parallel()
	const raw = "accounting:\n  pricing:\n    currency: USD\n  billing:\n    reports_path: /admin/billing-custom\n"
	if _, err := config.LoadEffective(context.Background(), []byte(raw), config.LoadEffectiveOptions{}); err == nil {
		t.Fatal("LoadEffective without capabilities must fail closed")
	}
	if _, err := config.LoadEffective(context.Background(), []byte(raw), config.LoadEffectiveOptions{
		Capabilities: billingCapability(),
	}); err != nil {
		t.Fatalf("LoadEffective with billing capability: %v", err)
	}
}

// TestValidateWithCapabilities_LegacyRuleMigrationDiagnosticSurvives proves the
// retired monetary rule diagnostic still surfaces — with the index and
// migration guidance — once the usage-limits capability is declared.
func TestValidateWithCapabilities_LegacyRuleMigrationDiagnosticSurvives(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Accounting: accountingConfigWithRule()}
	cfg.Accounting.Authority.Rules = []config.AccountingAuthorityRuleConfig{{
		ID: "legacy.budget", Kind: "budget", Unit: "money_nano", Limit: 5,
	}}
	err := config.ValidateWithCapabilities(cfg, usageLimitsCapability())
	if err == nil {
		t.Fatal("expected retired monetary rule to fail validation")
	}
	if !strings.Contains(err.Error(), "accounting.authority.rules[0]") || !strings.Contains(err.Error(), "migration-required") {
		t.Fatalf("error = %v, want indexed migration-required diagnostic", err)
	}
	if strings.Contains(err.Error(), "requires AIProxer Enterprise") {
		t.Fatalf("capability error must not replace the migration diagnostic: %v", err)
	}
}
