package featurehost

// Composed production-path proof of the operator-declared funding decision
// (design §2, Requirement 2.3). It reuses the single composed production
// fixture of proxy_memory_production_execution_test.go: the process, the
// generation, the real feature worker, the real process background scheduler
// bound to the real generation executor runner, the real generic-core runtime
// executor and the recording backend are the production ones. Only the
// provider's content is stubbed.
//
// The proof observes admission, never money: it asserts which background
// operations reach a backend open, which terminal records carry a customer
// account, and that a client-supplied canonical extension can never select
// the funding mode. The combinatorial decisions of the gate itself live in
// internal/core/runtime/background_sponsorship_test.go; here each seam is
// proven once on the real path.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/stretchr/testify/require"
)

// TestProxyMemoryFundingDecisionOnTheProductionPath proves the two refusal
// seams of the funding decision on the real path:
//
//   - an operator who declared no funding mode has both phases refused before
//     any provider open, and the memory is not published;
//   - a customer-funded decision is refused on a host that publishes no
//     validated monetary authority, before any provider open: wired billing
//     ports are not a monetary host, so this composition's stub seams never
//     open a provider for money that cannot be charged.
func TestProxyMemoryFundingDecisionOnTheProductionPath(t *testing.T) {
	for _, tc := range []struct {
		name        string
		fundingMode string
	}{
		{name: "no declared mode is refused", fundingMode: ""},
		{
			name:        "customer funded is refused without a validated monetary authority",
			fundingMode: "customer_funded",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			composition := composeProxyMemoryProduction(t, tc.fundingMode, nil)

			require.Empty(t, composition.backend.observed(),
				"a refused background work must open no backend at all")
			calls, legs := composition.sink.snapshot()
			for _, c := range calls {
				require.NotEqual(t, usageevidence.WorkloadClassAuxiliary, c.Workload.Class,
					"a refused background work must not close a customer account")
			}
			for _, l := range legs {
				require.NotEqual(t, usageevidence.WorkloadClassAuxiliary, l.Workload.Class,
					"a refused background work must not meter a usage leg")
			}
			published, _, err := composition.store.PublishedSummary(context.Background(), proxymemory.Owner{
				TenantID: sentinelTenant, PrincipalID: sentinelPrincipalA,
				ProjectKey: proxymemory.ProjectKey(proxymemory.NormalizePath(sentinelProjectPath)),
			})
			if err == nil {
				require.Empty(t, published, "a refused background work publishes no memory")
			}
		})
	}
}

// TestProxyMemoryOperatorSponsoredRunsUsageOnly is the usage-only seam: the
// operator sponsors the background work, so the phases open the backend, the
// usage is metered, and no customer account is closed. The trusted owner of
// the captured session travels with the work, and the funding decision, the
// sponsor key and the account never travel on a canonical call.
func TestProxyMemoryOperatorSponsoredRunsUsageOnly(t *testing.T) {
	composition := composeProxyMemoryProduction(t, "operator_sponsored", nil)

	opens := composition.backend.observed()
	require.Len(t, opens, 2, "only the two phases may open the auxiliary backends")
	byRole := map[string]bool{}
	for _, o := range opens {
		require.False(t, byRole[o.role], "a phase must open the backend exactly once")
		byRole[o.role] = true
		// The trusted owner of the captured session travels with the
		// background work: the phase is attributed to the captured tenant and
		// principal, never to a public account field.
		require.Equal(t, sentinelPrincipalA, o.scope.PrincipalID.String())
		require.Equal(t, sentinelTenant, o.scope.TenantID.String())
		// The funding decision, the sponsor key and the account never travel
		// on a canonical call.
		for _, key := range o.extensionKeys {
			require.NotContains(t, key, "funding")
			require.NotContains(t, key, "sponsor")
			require.NotContains(t, key, "account")
		}
	}
	require.True(t, byRole[sentinelRolePhase1])
	require.True(t, byRole[sentinelRolePhase2])

	// The usage is metered, and no customer account is closed.
	calls, legs := composition.sink.snapshot()
	for _, c := range calls {
		require.NotEqual(t, usageevidence.WorkloadClassAuxiliary, c.Workload.Class,
			"an operator-sponsored phase must not close a customer account")
	}
	auxiliaryLegs := 0
	for _, l := range legs {
		if l.Workload.Class == usageevidence.WorkloadClassAuxiliary {
			auxiliaryLegs++
		}
	}
	require.Equal(t, 2, auxiliaryLegs, "every admitted phase meters its usage")

	published, _, err := composition.store.PublishedSummary(context.Background(), proxymemory.Owner{
		TenantID: sentinelTenant, PrincipalID: sentinelPrincipalA,
		ProjectKey: proxymemory.ProjectKey(proxymemory.NormalizePath(sentinelProjectPath)),
	})
	require.NoError(t, err)
	require.Equal(t, sentinelSummary, published, "an admitted phase publishes its memory")
}

// TestProxyMemoryFundingDecisionIgnoresClientSuppliedClaims is the negative
// trust-boundary case: a client-supplied canonical extension that claims a
// funding mode and a customer account must never select the funding mode of
// the background work. The operator's configuration is the only authority, so
// the background phases stay refused with zero provider opens.
func TestProxyMemoryFundingDecisionIgnoresClientSuppliedClaims(t *testing.T) {
	composition := composeProxyMemoryProduction(t, "", map[string]json.RawMessage{
		"aip.billing.funding_mode": json.RawMessage(`"customer_funded"`),
		"aip.billing.account_id":   json.RawMessage(`"acct:client-chosen"`),
	})

	require.Empty(t, composition.backend.observed(),
		"a client-supplied funding claim must not open a backend")
	calls, _ := composition.sink.snapshot()
	for _, c := range calls {
		require.NotEqual(t, "acct:client-chosen", c.AccountID,
			"a client-supplied account claim must never reach a terminal record")
	}
}

// TestProxyMemoryFundingModeVocabularyMatchesTheCore pins the feature-side
// funding vocabulary against the core's trusted funding decision, so the two
// closed lists cannot drift: the operator's configuration value and the core's
// mode value are the same token, and the composition maps every feature mode
// onto the matching core mode.
func TestProxyMemoryFundingModeVocabularyMatchesTheCore(t *testing.T) {
	require.Equal(t, string(execctx.SponsorshipModeCustomerFunded), string(proxymemory.FundingModeCustomerFunded))
	require.Equal(t, string(execctx.SponsorshipModeOperatorSponsored), string(proxymemory.FundingModeOperatorSponsored))
	for _, tc := range []struct {
		feature proxymemory.FundingMode
		core    execctx.SponsorshipMode
	}{
		{proxymemory.FundingModeUndeclared, execctx.SponsorshipModeUndeclared},
		{proxymemory.FundingModeCustomerFunded, execctx.SponsorshipModeCustomerFunded},
		{proxymemory.FundingModeOperatorSponsored, execctx.SponsorshipModeOperatorSponsored},
	} {
		cfg := proxymemory.DefaultConfig()
		cfg.FundingMode = tc.feature
		require.Equal(t, tc.core, proxyMemorySponsorship(cfg).Mode)
	}
	// An operator who declared no funding mode leaves the background work
	// undeclared, which the runtime refuses before any provider dispatch.
	cfg := proxymemory.DefaultConfig()
	decl := proxyMemorySponsorship(cfg)
	require.Equal(t, execctx.SponsorshipModeUndeclared, decl.Mode)
	require.False(t, decl.ModeDeclared())
}
