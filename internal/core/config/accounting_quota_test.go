package config_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestAccountingQuotaConfig_CompilesOneExplicitPolicy(t *testing.T) {
	reset := time.Date(2026, time.January, 3, 11, 0, 0, 0, time.UTC)
	field := metering.ComponentKey{Direction: metering.DirectionNone, Component: "provider:utilization", Unit: metering.UnitPercent, SchemaID: "quota.v1"}
	cfg := &config.Config{Accounting: config.AccountingConfig{Authority: config.AccountingAuthorityConfig{
		Quota: &config.AccountingQuotaConfig{
			ID: "provider-quota", Version: "2026-01", StoreID: "metering-1", TenantID: "tenant-1",
			ProviderAccountKey: "account-a", PoolID: "primary", WindowID: "minute", ResetAt: reset.Format(time.RFC3339),
			Freshness: "5m", Required: []metering.ComponentKey{field},
			Thresholds: []config.AccountingQuotaThresholdConfig{{Kind: string(authority.QuotaThresholdMaxUtilization), Field: field, ValueKind: string(authority.QuotaValuePercent), Value: "80"}},
		},
	}}}
	require.NoError(t, config.Validate(cfg))
	policyCfg, err := cfg.Accounting.Authority.Quota.PolicyConfig()
	require.NoError(t, err)
	policy, err := authority.CompileQuotaPolicy(policyCfg)
	require.NoError(t, err)
	require.Equal(t, "provider-quota", policy.ID())
	require.Equal(t, "2026-01", policy.Version())
	require.Equal(t, reset, policy.Binding().ResetAt)
}

func TestAccountingQuotaConfig_YAMLUsesExactTypedFields(t *testing.T) {
	var cfg config.Config
	err := yaml.Unmarshal([]byte(`
accounting:
  authority:
    quota:
      id: provider-quota
      version: 2026-01
      store_id: metering-1
      provider_account_key: account-a
      pool_id: primary
      window_id: minute
      reset_at: "2026-01-03T11:00:00Z"
      freshness: 5m
      required:
        - direction: none
          component: provider:utilization
          unit: percent
          schema_id: quota.v1
      thresholds:
        - kind: max_utilization
          field:
            direction: none
            component: provider:utilization
            unit: percent
            schema_id: quota.v1
          value_kind: percent
          value: "80"
`), &cfg)
	require.NoError(t, err)
	require.NotNil(t, cfg.Accounting.Authority.Quota)
	require.NoError(t, config.Validate(&cfg))
	compiled, err := cfg.Accounting.Authority.Quota.PolicyConfig()
	require.NoError(t, err)
	require.Equal(t, "provider:utilization", compiled.Required[0].Component)
	require.Equal(t, "80/0", compiled.Thresholds[0].Value.CanonicalString())
}

func TestAccountingQuotaConfig_IsOptionalAndInvalidFieldsFailClosed(t *testing.T) {
	cfg := &config.Config{}
	require.NoError(t, config.Validate(cfg))

	cfg.Accounting.Authority.Quota = &config.AccountingQuotaConfig{ID: "provider-quota", Version: "2026-01"}
	require.Error(t, config.Validate(cfg))
}
