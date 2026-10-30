package aggregate_test

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// R8-B1: for one source event, the provider-supplied explicit source revision
// order is the effective ordering, not the provider-supplied Sequence number.
// A provider that emits revision 2 with Sequence=1 and then revision 1 with
// Sequence=2 must still reduce to revision 2 as the effective state, regardless
// of the (reversed) Sequence and regardless of the slice arrival order. The
// older revision stays in the immutable audit envelope, missing fields in the
// replacement do not erase the earlier present values, a present zero is a
// value, and the superseded charge collapses to one effective charge.
const r8b1SourceEventKey = "provider.r8b1.media-source"

func TestApplyObservations_ExplicitSourceRevisionOrderBeatsProviderSequence(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		order []int // 0 = newer revision 2, 1 = older revision 1
	}{
		{name: "newer revision observed first", order: []int{0, 1}},
		{name: "older revision observed first", order: []int{1, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rev1, rev2 := r8b1ExplicitRevisionObservations(t)
			byIndex := []metering.Observation{rev2, rev1}
			input := make([]metering.Observation, 0, len(tc.order))
			for _, index := range tc.order {
				input = append(input, byIndex[index])
			}

			if err := metering.ValidateSupersessionGraph(input); err != nil {
				t.Fatalf("supersession graph: %v", err)
			}

			snapshot, err := aggregate.ApplyObservations(input)
			if err != nil {
				t.Fatalf("apply explicit revisions: %v", err)
			}
			r8b1AssertEffectiveRevisionTwo(t, snapshot, rev1, rev2)
			r8b1AssertSingleEffectiveCharge(t, snapshot, rev2)
		})
	}
}

func TestApplyObservations_ExplicitSourceRevisionOrderIsArrivalInvariant(t *testing.T) {
	t.Parallel()

	rev1, rev2 := r8b1ExplicitRevisionObservations(t)
	forward, err := aggregate.ApplyObservations([]metering.Observation{rev1, rev2})
	if err != nil {
		t.Fatalf("apply forward: %v", err)
	}
	reversed, err := aggregate.ApplyObservations([]metering.Observation{rev2, rev1})
	if err != nil {
		t.Fatalf("apply reversed: %v", err)
	}
	mediaKey := r8b1MediaKey()
	if got, want := reversed.ValueFor(rev2, mediaKey), forward.ValueFor(rev2, mediaKey); got != want {
		t.Fatalf("effective media changed with arrival order: forward=%q reversed=%q", want, got)
	}
	if got, want := len(reversed.Charges), len(forward.Charges); got != want {
		t.Fatalf("effective charges changed with arrival order: forward=%d reversed=%d", want, got)
	}
}

// A changed payload under one immutable source revision stays a visible
// identity conflict even when Sequence is provider-supplied and reversed.
func TestApplyObservations_ExplicitRevisionConflictRemainsVisible(t *testing.T) {
	t.Parallel()

	_, rev2 := r8b1ExplicitRevisionObservations(t)
	conflict := rev2
	conflict.Measures = []metering.Measure{measure(r8b1MediaKey(), "99")}
	if _, err := aggregate.ApplyObservations([]metering.Observation{rev2, conflict}); !errors.Is(err, aggregate.ErrIdentityConflict) {
		t.Fatalf("altered same-revision payload error=%v, want ErrIdentityConflict", err)
	}
}

func r8b1ExplicitRevisionObservations(t *testing.T) (metering.Observation, metering.Observation) {
	t.Helper()

	// Revision 1 is the original cumulative provider snapshot: it carries the
	// native media field, a sibling field and a non-zero value for zeroKey.
	rev1 := observation("r8b1-rev1", "provider.r8b1.stream", 2, metering.SemanticsCumulative, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("b-r8b1"),
		measure(r8b1MediaKey(), "5"), measure(r8b1SiblingKey(), "9"), measure(r8b1ZeroKey(), "7"))
	rev1.SourceEventKey = r8b1SourceEventKey
	rev1.Revision = 1
	rev1.Charges = []metering.ReportedCharge{{ChargeItemID: "provider-total", Kind: metering.ChargeKindAggregate, Amount: decimal("1.00"), Currency: "USD"}}
	rev1Ref, err := rev1.Ref(rev1.Subject.StoreID)
	if err != nil {
		t.Fatal(err)
	}

	// Revision 2 is a replacement delivered with a lower Sequence. It carries
	// the new native media value and an explicit zero, but omits the sibling
	// field so present-field sparse reduction must retain revision 1's value.
	rev2 := observation("r8b1-rev2", "provider.r8b1.stream", 1, metering.SemanticsReplacement, metering.OriginProvider, metering.AcquisitionProviderResponse, blegSubject("b-r8b1"),
		measure(r8b1MediaKey(), "2"), measure(r8b1ZeroKey(), "0"))
	rev2.SourceEventKey = r8b1SourceEventKey
	rev2.Revision = 2
	rev2.Supersedes = []metering.ObservationRef{rev1Ref}
	rev2.Charges = []metering.ReportedCharge{{ChargeItemID: "provider-total", Kind: metering.ChargeKindAggregate, Amount: decimal("2.00"), Currency: "USD"}}
	return rev1, rev2
}

func r8b1AssertEffectiveRevisionTwo(t *testing.T, snapshot aggregate.SnapshotV2, rev1, rev2 metering.Observation) {
	t.Helper()

	if len(snapshot.Observations) != 2 {
		t.Fatalf("audit observations=%d, want both immutable revisions retained", len(snapshot.Observations))
	}
	if !r8b1AuditRetains(snapshot, rev1.ID) || !r8b1AuditRetains(snapshot, rev2.ID) {
		t.Fatalf("audit envelope lost a revision: %s / %s", rev1.ID, rev2.ID)
	}
	if !snapshot.Complete {
		t.Fatalf("valid explicit revisions made snapshot incomplete: unavailable=%v pending=%v", snapshot.Unavailable, snapshot.PendingSupersedes)
	}

	if got, want := snapshot.ValueFor(rev2, r8b1MediaKey()), canonicalDecimal(t, decimal("2")); got != want {
		t.Fatalf("effective native media=%q, want newer revision %q", got, want)
	}
	if got, want := snapshot.ValueFor(rev2, r8b1SiblingKey()), canonicalDecimal(t, decimal("9")); got != want {
		t.Fatalf("sparse sibling value=%q, want retained revision 1 value %q", got, want)
	}
	if got, want := snapshot.ValueFor(rev2, r8b1ZeroKey()), canonicalDecimal(t, decimal("0")); got != want {
		t.Fatalf("explicit zero=%q, want present zero %q", got, want)
	}

	media := r8b1FindMeasure(t, snapshot, rev2, r8b1MediaKey())
	if media.LastObservationID != rev2.ID || media.LastRevision != rev2.Revision {
		t.Fatalf("effective media head=%s/%d, want newer revision %s/%d",
			media.LastObservationID, media.LastRevision, rev2.ID, rev2.Revision)
	}
}

func r8b1AssertSingleEffectiveCharge(t *testing.T, snapshot aggregate.SnapshotV2, effective metering.Observation) {
	t.Helper()

	if len(snapshot.Charges) != 1 {
		t.Fatalf("effective charges=%d, want exactly one superseding charge: %+v", len(snapshot.Charges), snapshot.Charges)
	}
	charge := snapshot.Charges[0]
	if charge.ObservationID != effective.ID || charge.Revision != effective.Revision {
		t.Fatalf("effective charge source=%s/%d, want %s/%d", charge.ObservationID, charge.Revision, effective.ID, effective.Revision)
	}
	if charge.Charge.Amount == nil || charge.Charge.Amount.CanonicalString() != canonicalDecimal(t, decimal("2.00")) {
		t.Fatalf("effective charge amount=%+v, want newer 2.00", charge.Charge.Amount)
	}
}

func r8b1FindMeasure(t *testing.T, snapshot aggregate.SnapshotV2, observation metering.Observation, key metering.ComponentKey) aggregate.ReducedMeasure {
	t.Helper()

	scopeKey := aggregate.ScopeFor(observation).Key()
	normalized, err := key.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	for _, measure := range snapshot.Measures {
		if measure.Scope.Key() == scopeKey && measure.Key.CanonicalKey() == normalized.CanonicalKey() {
			return measure
		}
	}
	t.Fatalf("effective measure missing for scope %s key %s", scopeKey, normalized.CanonicalKey())
	return aggregate.ReducedMeasure{}
}

func r8b1AuditRetains(snapshot aggregate.SnapshotV2, observationID string) bool {
	for _, observation := range snapshot.Observations {
		if observation.ID == observationID {
			return true
		}
	}
	return false
}

func canonicalDecimal(t *testing.T, value *metering.Decimal) string {
	t.Helper()
	if value == nil {
		return ""
	}
	normalized, err := value.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	return normalized.CanonicalString()
}

func r8b1MediaKey() metering.ComponentKey {
	return metering.ComponentKey{
		Direction: metering.DirectionInput, Component: metering.ComponentImage,
		Unit: metering.UnitImage, SchemaID: "provider.r8b1.v1",
	}
}

func r8b1SiblingKey() metering.ComponentKey {
	return metering.ComponentKey{
		Direction: metering.DirectionInput, Component: metering.ComponentCacheReadInputToken,
		Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
	}
}

func r8b1ZeroKey() metering.ComponentKey {
	return metering.ComponentKey{
		Direction: metering.DirectionOutput, Component: metering.ComponentReasoningOutputToken,
		Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
	}
}
