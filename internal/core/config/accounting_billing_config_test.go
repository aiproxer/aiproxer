package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAccountingBillingYAMLRoundTrip(t *testing.T) {
	t.Parallel()
	const y = `
accounting:
  billing:
        reports_path: /admin/billing-custom
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(y), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Accounting.Billing.ReportsPath != "/admin/billing-custom" {
		t.Fatalf("reports_path = %q", cfg.Accounting.Billing.ReportsPath)
	}
}
