package service

import "testing"

// TestChargeIdentity_DistinctHostAttemptsAreDistinctCharges is the accounting
// half of the two-attempt regression: when the provider reports no request id,
// the host attempt identity alone must distinguish two genuine provider
// charges. Collapsing them would merge two charges into one sideband record.
func TestChargeIdentity_DistinctHostAttemptsAreDistinctCharges(t *testing.T) {
	t.Parallel()
	first, firstSource := chargeIdentity("", "b-leg-1")
	second, secondSource := chargeIdentity("", "b-leg-2")
	if first == second {
		t.Fatalf("two host attempts share the charge identity %q", first)
	}
	if firstSource != identitySourceAttempt || secondSource != identitySourceAttempt {
		t.Fatalf("identity sources = %q/%q, want attempt", firstSource, secondSource)
	}
	if usageDedupeKey("acct-one", first) == usageDedupeKey("acct-one", second) {
		t.Fatal("two host attempts without a provider identity produce one accounting key")
	}
}
