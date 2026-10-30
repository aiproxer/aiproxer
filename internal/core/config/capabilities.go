package config

import (
	"fmt"
	"strings"
)

// CapabilitySet names the capabilities the process composition root provides to
// configuration validation. Config never imports a composition root or
// pkg/aipruntime, so the names travel as this explicit data input at load time;
// a name that is absent (or false) fails closed for the matching Enterprise-only
// keys. The set is treated as read-only once passed.
type CapabilitySet map[string]bool

const (
	// CapabilityBilling is the monetary billing composition capability
	// (accounting.pricing and accounting.billing configuration).
	CapabilityBilling = "billing"
	// CapabilityUsageLimits is the usage-limit (budget and limit enforcement)
	// capability (accounting.authority.rules configuration).
	CapabilityUsageLimits = "usage-limits"
)

// Has reports whether the composition root declared the capability.
func (s CapabilitySet) Has(name string) bool { return s[name] }

// validateEnterpriseOnlyAccountingKeys rejects Enterprise-only accounting keys
// unless the composition root declared the matching capability. Strict YAML
// decoding has no key-presence flag, so the predicate for each key is explicit:
// an omitted, null, empty, or zero block decodes to a zero value and is not a
// request, while any non-empty monetary or rule field is one. The predicate
// deliberately ignores accounting.authority.enabled on its own (provider quota
// windows and nonfinancial authority posture stay open source), and it applies
// to non-empty rules even while accounting.authority.enabled is false so a
// disabled-but-configured rule surface is never silently ignored.
func validateEnterpriseOnlyAccountingKeys(cfg *Config, capabilities CapabilitySet) error {
	if cfg == nil {
		return nil
	}
	a := &cfg.Accounting
	if accountingPricingRequested(a.Pricing) && !capabilities.Has(CapabilityBilling) {
		return enterpriseCapabilityError("accounting.pricing", CapabilityBilling)
	}
	if strings.TrimSpace(a.Billing.ReportsPath) != "" && !capabilities.Has(CapabilityBilling) {
		return enterpriseCapabilityError("accounting.billing", CapabilityBilling)
	}
	if len(a.Authority.Rules) > 0 && !capabilities.Has(CapabilityUsageLimits) {
		return enterpriseCapabilityError("accounting.authority.rules", CapabilityUsageLimits)
	}
	return nil
}

// accountingPricingRequested reports whether accounting.pricing carries any
// operator-supplied monetary value. An empty currency, catalog version, and
// model list decode the same as an absent block.
func accountingPricingRequested(p AccountingPricingConfig) bool {
	return strings.TrimSpace(p.Currency) != "" ||
		strings.TrimSpace(p.CatalogVersion) != "" ||
		len(p.Models) > 0
}

func enterpriseCapabilityError(key, capability string) error {
	return fmt.Errorf("config: %s requires AIProxer Enterprise (capability %q)", key, capability)
}
