package metering

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
)

// R8-B2 enforceable boundary: a changed payload under one explicit source
// revision is no longer a reducer identity conflict, because the explicit
// revision never reaches the reducer. The original payload, an exact duplicate,
// and a changed payload at the same revision are all rejected at Add as the same
// unsupported ordering identity, and the rejection stays sticky across drains.
func TestProviderEvidenceR8BChangedExplicitRevisionUnsupported(t *testing.T) {
	t.Parallel()
	const key = "provider.r8b2.changed"
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())

	b.Add(nativeExplicitMediaMoneyDraft(key, 2))
	b.Add(nativeExplicitMediaMoneyDraft(key, 2)) // exact duplicate of the explicit revision
	changed := nativeExplicitMediaMoneyDraft(key, 9)
	changed.Revision = 2 // changed payload under the same explicit revision
	b.Add(changed)

	observations := b.DrainEconomicObservations()
	marker, ok := r5UnavailableMarker(observations)
	if !ok || r5UnavailableCause(marker) != providerEvidenceLossUnsupportedOrderingIdentity {
		t.Fatalf("changed explicit revision must be visibly rejected: %+v", observations)
	}
	for _, observation := range observations {
		if observation.SourceEventKey == key {
			t.Fatalf("changed explicit revision leaked into accepted evidence: %+v", observation)
		}
	}
}

// R8-B2: a provider-supplied reversed Sequence cannot select an effective head,
// because it is rejected before it becomes an observation.
func TestProviderEvidenceR8BProviderSuppliedSequenceUnsupported(t *testing.T) {
	t.Parallel()
	const key = "provider.r8b2.sequence"
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())
	newer := nativeExplicitMediaMoneyDraft(key, 2)
	newer.Sequence = 1
	b.Add(newer)
	older := nativeExplicitMediaMoneyDraft(key, 1)
	older.Sequence = 9
	b.Add(older)

	observations := b.DrainEconomicObservations()
	marker, ok := r5UnavailableMarker(observations)
	if !ok || r5UnavailableCause(marker) != providerEvidenceLossUnsupportedOrderingIdentity {
		t.Fatalf("provider-supplied sequence must be visibly rejected: %+v", observations)
	}
	snapshot, err := aggregate.ApplyObservations(observations)
	if err != nil {
		t.Fatalf("apply rejected sequence evidence: %v", err)
	}
	if snapshot.Complete {
		t.Fatal("unsupported provider sequence must keep the reduction incomplete")
	}
}
