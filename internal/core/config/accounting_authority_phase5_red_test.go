package config_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func TestPhase5RetiredMoneyRuleAliasesFailWithMigrationError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		kind string
		unit string
	}{
		{name: "budget", kind: "BUDGET", unit: "requests"},
		{name: "spend cap alias", kind: "spend-cap", unit: "requests"},
		{name: "money unit alias", kind: "quota", unit: "money-nano"},
		{name: "money camel alias", kind: "quota", unit: "moneyNano"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Accounting.Authority.Enabled = true
			cfg.Accounting.Authority.Store = "memory"
			cfg.Accounting.Authority.Rules = []config.AccountingAuthorityRuleConfig{{
				ID: "legacy.money", Kind: tc.kind, Unit: tc.unit, Limit: 10,
			}}
			err := config.ValidateWithCapabilities(cfg, usageLimitsCapability())
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "migration") {
				t.Fatalf("Validate error = %v, want explicit migration-required error", err)
			}
		})
	}
}
