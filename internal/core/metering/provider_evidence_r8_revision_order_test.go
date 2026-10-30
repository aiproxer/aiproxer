package metering

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// R8 enforceable boundary. The original R8 attempts tried to make a
// provider-supplied explicit source revision authoritative. That is unsafe: the
// SDK observation cannot carry source-vs-host revision provenance, so a late
// older explicit revision (rev2 then rev1) is observationally identical to a
// valid cumulative/delta update and could duplicate a provider charge. The
// capability is now unsupported at ProviderEvidenceBuffer.Add: every explicit
// Revision/Sequence draft drains only as a sticky unavailable loss marker, so no
// provider revision evidence is accepted and the reduction stays incomplete.
func TestProviderEvidenceR8ExplicitSourceRevisionUnsupported(t *testing.T) {
	t.Parallel()
	const key = "provider.r8.unsupported"
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())

	// The exact RED sequence: a newer revision is offered first, then a late
	// older revision. If either were accepted, the pair could duplicate a charge.
	b.Add(nativeExplicitMediaMoneyDraft(key, 2))
	b.Add(nativeExplicitMediaMoneyDraft(key, 1))

	observations := b.DrainEconomicObservations()
	if len(observations) != 1 {
		t.Fatalf("unsupported explicit revisions drained=%d, want one loss marker", len(observations))
	}
	marker := observations[0]
	if marker.Authority != sdkmetering.AuthorityUnavailableClaim {
		t.Fatalf("rejection authority=%q, want unavailable", marker.Authority)
	}
	if r5UnavailableCause(marker) != providerEvidenceLossUnsupportedOrderingIdentity {
		t.Fatalf("loss reason=%q, want %q", r5UnavailableCause(marker), providerEvidenceLossUnsupportedOrderingIdentity)
	}
	if len(marker.Charges) != 0 {
		t.Fatalf("unsupported revision must not carry provider charges: %+v", marker.Charges)
	}
	if marker.SourceEventKey == key {
		t.Fatal("loss marker must not reuse the rejected provider source key")
	}

	snapshot, err := aggregate.ApplyObservations(observations)
	if err != nil {
		t.Fatalf("apply unsupported-revision evidence: %v", err)
	}
	if snapshot.Complete {
		t.Fatal("unsupported explicit revision must keep the reduction incomplete")
	}
	if len(snapshot.Charges) != 0 {
		t.Fatalf("unsupported explicit revision produced effective charges: %+v", snapshot.Charges)
	}
}

// A late older explicit revision delivered after a newer one crossed a drain is
// rejected just the same. The rejection is visible on both drains and no
// provider observation is ever sealed.
func TestProviderEvidenceR8LateOlderExplicitRevisionUnsupportedAcrossDrains(t *testing.T) {
	t.Parallel()
	const key = "provider.r8.cross-drain"
	b := NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(testObservationIdentity())

	b.Add(nativeExplicitMediaMoneyDraft(key, 2))
	first := b.DrainEconomicObservations()
	if marker, ok := r5UnavailableMarker(first); !ok ||
		r5UnavailableCause(marker) != providerEvidenceLossUnsupportedOrderingIdentity {
		t.Fatalf("newer explicit revision must be visibly rejected: %+v", first)
	}

	// The previously unseen older revision must not be accepted as audit
	// evidence either: it is the same unsupported capability.
	b.Add(nativeExplicitMediaMoneyDraft(key, 1))
	second := b.DrainEconomicObservations()
	if marker, ok := r5UnavailableMarker(second); !ok ||
		r5UnavailableCause(marker) != providerEvidenceLossUnsupportedOrderingIdentity {
		t.Fatalf("late older explicit revision must be visibly rejected: %+v", second)
	}

	for _, observation := range append(append([]sdkmetering.Observation{}, first...), second...) {
		if observation.SourceEventKey == key {
			t.Fatalf("explicit source revision leaked into accepted evidence: %+v", observation)
		}
	}
}
