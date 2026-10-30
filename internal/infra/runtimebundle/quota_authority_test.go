package runtimebundle

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/stretchr/testify/require"
)

func TestQuotaRequestRegistration_CompilesExplicitGenerationPolicy(t *testing.T) {
	now := time.Date(2026, time.January, 3, 12, 0, 0, 0, time.UTC)
	reset := now.Add(-time.Hour)
	field := metering.ComponentKey{Direction: metering.DirectionNone, Component: "provider:utilization", Unit: metering.UnitPercent, SchemaID: "quota.v1"}
	cfg := &config.Config{Accounting: config.AccountingConfig{Authority: config.AccountingAuthorityConfig{
		Quota: &config.AccountingQuotaConfig{
			ID: "provider-quota", Version: "2026-01", StoreID: "metering-1", TenantID: "tenant-1", ProviderAccountKey: "account-a",
			PoolID: "primary", WindowID: "minute", ResetAt: reset.Format(time.RFC3339), Freshness: "5m",
			Required: []metering.ComponentKey{field},
		},
	}}}

	reg, err := quotaRequestRegistration(cfg, nil, func() time.Time { return now })
	require.NoError(t, err)
	require.NotNil(t, reg)
	require.Equal(t, authority.RequestPriorityQuotaBudgetRate, reg.Priority)
	require.Equal(t, "provider-quota:provider-quota", reg.Descriptor.ID)
	require.Equal(t, authority.StrengthRequired, reg.Descriptor.Postures[0].Strength)
	require.Equal(t, authority.FailureFailClosed, reg.Descriptor.Postures[0].FailureBehavior)
	provider, ok := reg.Provider.(interface {
		PolicyRef() authority.QuotaPolicyRef
	})
	require.True(t, ok)
	require.Equal(t, "2026-01", provider.PolicyRef().Version)
	require.NoError(t, reg.Validate())
}

func TestQuotaRequestRegistration_AbsentPolicyPreservesGenericAuthority(t *testing.T) {
	reg, err := quotaRequestRegistration(&config.Config{}, nil, time.Now)
	require.NoError(t, err)
	require.Nil(t, reg)
}

func TestQuotaRequestRegistration_IsAttachedAsRequestAuthoritySlot(t *testing.T) {
	now := time.Date(2026, time.January, 3, 12, 0, 0, 0, time.UTC)
	reset := now.Add(-time.Hour)
	field := metering.ComponentKey{Direction: metering.DirectionNone, Component: "provider:utilization", Unit: metering.UnitPercent, SchemaID: "quota.v1"}
	cfg := &config.Config{Accounting: config.AccountingConfig{Authority: config.AccountingAuthorityConfig{
		Quota: &config.AccountingQuotaConfig{
			ID: "provider-quota", Version: "2026-01", ProviderAccountKey: "account-a", PoolID: "primary", WindowID: "minute",
			ResetAt: reset.Format(time.RFC3339), Freshness: "5m", Required: []metering.ComponentKey{field},
		},
	}}}
	reg, err := quotaRequestRegistration(cfg, nil, func() time.Time { return now })
	require.NoError(t, err)
	var accounting runtime.AccountingRuntime
	require.NoError(t, attachAuthorityCoordinators(&accounting, ProductionOptions{RequestRegistrations: []authority.RequestRegistration{*reg}}))
	require.NotNil(t, accounting.RequestCoordinator)
	require.Len(t, accounting.RequestCoordinator.Slots, 1)
	require.Equal(t, reg.Descriptor.ID, accounting.RequestCoordinator.Slots[0].ID)
	require.Equal(t, authoritycoord.PriorityQuotaBudgetRate, accounting.RequestCoordinator.Slots[0].Class)
}
