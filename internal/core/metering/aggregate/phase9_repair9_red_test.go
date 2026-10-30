package aggregate_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase9Repair9_ReplacementRemovesStaleInclusiveCoverage(t *testing.T) {
	t.Parallel()

	component := repair9ComponentKey()
	child := repair9ChargeObservation("repair9-stale-child", 2, metering.SemanticsDelta, "repair9-stale-stream",
		repair9Charge("child", &component, "4", metering.ChargeKindComponent, metering.PaymentPartyOperator))
	parent := repair9ChargeObservation("repair9-stale-parent", 1, metering.SemanticsCumulative, "repair9-stale-stream",
		repair9Charge("total", nil, "10", metering.ChargeKindAggregate, metering.PaymentPartyOperator))
	childRef, err := child.Ref(child.Subject.StoreID)
	if err != nil {
		t.Fatalf("child ref: %v", err)
	}
	parent.Charges[0].Covers = []metering.ChargeCoverageRef{{
		Ref:      metering.ChargeRef{StoreID: childRef.StoreID, ObservationID: childRef.ObservationID, Revision: childRef.Revision, ChargeItemID: "child"},
		Relation: metering.CoverageInclusive,
	}}
	parentRef, err := parent.Ref(parent.Subject.StoreID)
	if err != nil {
		t.Fatalf("parent ref: %v", err)
	}
	replacement := repair9ChargeObservation("repair9-stale-replacement", 3, metering.SemanticsReplacement, "repair9-stale-stream",
		repair9Charge("total", nil, "8", metering.ChargeKindAggregate, metering.PaymentPartyOperator))
	replacement.Supersedes = []metering.ObservationRef{parentRef}

	orders := [][]metering.Observation{
		{parent, child, replacement},
		{replacement, child, parent},
		{child, replacement, parent},
	}
	for order, observations := range orders {
		snapshot, applyErr := aggregate.ApplyObservations(observations)
		if applyErr != nil {
			t.Fatalf("order %d apply: %v", order, applyErr)
		}
		if len(snapshot.PendingCoverage) != 0 {
			t.Fatalf("order %d pending coverage=%+v, want stale edge audit-only", order, snapshot.PendingCoverage)
		}
		if got := repair9ChargeAmount(snapshot, "total"); got != "8/0" {
			t.Fatalf("order %d effective aggregate=%q, want 8/0", order, got)
		}
		if got := repair9ChargeAmount(snapshot, "child"); got != "4/0" {
			t.Fatalf("order %d effective child=%q, want 4/0 after replacement removed coverage", order, got)
		}
		if len(snapshot.Charges) != 2 {
			t.Fatalf("order %d effective charges=%+v, want aggregate and child", order, snapshot.Charges)
		}
	}
}

func TestPhase9Repair9_OneChargeToManyReplacementUsesChargeIndependentStream(t *testing.T) {
	t.Parallel()

	component := repair9ComponentKey()
	base := repair9ChargeObservation("repair9-one-base", 1, metering.SemanticsCumulative, "repair9-one-stream",
		repair9Charge("usage", &component, "10", metering.ChargeKindComponent, metering.PaymentPartyOperator))
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}
	replacement := repair9ChargeObservation("repair9-one-replacement", 2, metering.SemanticsReplacement, "repair9-one-stream",
		repair9Charge("usage", &component, "8", metering.ChargeKindComponent, metering.PaymentPartyOperator),
		repair9Charge("surcharge", &component, "2", metering.ChargeKindSurcharge, metering.PaymentPartyOperator))
	replacement.Supersedes = []metering.ObservationRef{baseRef}

	for order, observations := range [][]metering.Observation{
		{base, replacement},
		{replacement, base},
	} {
		snapshot, applyErr := aggregate.ApplyObservations(observations)
		if applyErr != nil {
			t.Fatalf("order %d apply: %v", order, applyErr)
		}
		if got := repair9ChargeAmount(snapshot, "usage"); got != "8/0" {
			t.Fatalf("order %d replacement usage=%q, want 8/0", order, got)
		}
		if got := repair9ChargeAmount(snapshot, "surcharge"); got != "2/0" {
			t.Fatalf("order %d successor surcharge=%q, want 2/0", order, got)
		}
		if len(snapshot.Charges) != 2 {
			t.Fatalf("order %d effective charges=%+v, want two successor items", order, snapshot.Charges)
		}
	}
}

func TestPhase9Repair9_NilBaseCorrectionRemainsIncomplete(t *testing.T) {
	t.Parallel()

	component := repair9ComponentKey()
	base := repair9ChargeObservation("repair9-nil-base", 1, metering.SemanticsCumulative, "repair9-nil-stream",
		repair9Charge("usage", &component, "", metering.ChargeKindComponent, metering.PaymentPartyOperator))
	base.Authority = metering.AuthorityUnavailableClaim
	baseRef, err := base.Ref(base.Subject.StoreID)
	if err != nil {
		t.Fatalf("base ref: %v", err)
	}
	correction := repair9ChargeObservation("repair9-nil-correction", 2, metering.SemanticsCorrection, "repair9-nil-stream",
		repair9Charge("usage", &component, "6", metering.ChargeKindComponent, metering.PaymentPartyOperator))
	correction.Supersedes = []metering.ObservationRef{baseRef}

	for order, observations := range [][]metering.Observation{
		{base, correction},
		{correction, base},
	} {
		snapshot, applyErr := aggregate.ApplyObservations(observations)
		if applyErr != nil {
			t.Fatalf("order %d apply: %v", order, applyErr)
		}
		charge := repair9ReducedCharge(t, snapshot, "usage")
		if charge == nil || charge.Complete || charge.Charge.Amount == nil || charge.Charge.Amount.CanonicalString() != "6/0" {
			t.Fatalf("order %d corrected charge=%+v, want incomplete amount 6/0", order, charge)
		}
		if snapshot.Complete || snapshot.Payable {
			t.Fatalf("order %d snapshot=%+v, want incomplete/non-payable", order, snapshot)
		}
	}
}

func TestPhase9Repair9_PartialReplacementRetainsCoverageDiagnostic(t *testing.T) {
	t.Parallel()

	parent := repair9ChargeObservation("repair9-partial-parent", 1, metering.SemanticsCumulative, "repair9-partial-stream",
		repair9Charge("total", nil, "10", metering.ChargeKindAggregate, metering.PaymentPartyOperator))
	parent.Charges[0].Covers = []metering.ChargeCoverageRef{{
		Ref:      metering.ChargeRef{StoreID: parent.Subject.StoreID, ObservationID: "repair9-partial-child", Revision: 1, ChargeItemID: "child"},
		Relation: metering.CoverageInclusive,
	}}
	parentRef, err := parent.Ref(parent.Subject.StoreID)
	if err != nil {
		t.Fatalf("parent ref: %v", err)
	}
	replacement := repair9ChargeObservation("repair9-partial-replacement", 2, metering.SemanticsReplacement, "repair9-partial-stream",
		repair9Charge("surcharge", nil, "3", metering.ChargeKindSurcharge, metering.PaymentPartyOperator))
	replacement.Supersedes = []metering.ObservationRef{parentRef}

	for order, observations := range [][]metering.Observation{
		{parent, replacement},
		{replacement, parent},
	} {
		snapshot, applyErr := aggregate.ApplyObservations(observations)
		if applyErr != nil {
			t.Fatalf("order %d apply: %v", order, applyErr)
		}
		if got := repair9ChargeAmount(snapshot, "total"); got != "10/0" {
			t.Fatalf("order %d retained aggregate=%q, want 10/0", order, got)
		}
		if got := repair9ChargeAmount(snapshot, "surcharge"); got != "3/0" {
			t.Fatalf("order %d replacement surcharge=%q, want 3/0", order, got)
		}
		if charge := repair9ReducedCharge(t, snapshot, "total"); charge == nil || charge.Complete {
			t.Fatalf("order %d aggregate=%+v, want incomplete retained charge", order, charge)
		}
		if len(snapshot.PendingCoverage) != 1 || snapshot.PendingCoverage[0].Ref.ObservationID != "repair9-partial-child" {
			t.Fatalf("order %d pending coverage=%+v, want unresolved child diagnostic", order, snapshot.PendingCoverage)
		}
		if snapshot.Complete || snapshot.Payable {
			t.Fatalf("order %d snapshot=%+v, want incomplete/non-payable", order, snapshot)
		}
	}
}

func repair9ComponentKey() metering.ComponentKey {
	return metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "repair9.v1"}
}

func repair9Decimal(value string) *metering.Decimal {
	if value == "" {
		return nil
	}
	decimal, err := metering.ParseDecimal(value)
	if err != nil {
		panic(err)
	}
	return &decimal
}

func repair9Charge(item string, component *metering.ComponentKey, amount string, kind metering.ChargeKind, payer metering.PaymentPartyKind) metering.ReportedCharge {
	return metering.ReportedCharge{ChargeItemID: item, Component: component, Amount: repair9Decimal(amount), Currency: repair9Currency(amount), Kind: kind, Payer: metering.PaymentParty{Kind: payer}}
}

func repair9Currency(amount string) string {
	if amount == "" {
		return ""
	}
	return "USD"
}

func repair9ChargeObservation(id string, sequence uint64, semantics, stream string, charges ...metering.ReportedCharge) metering.Observation {
	subject := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "repair9-store", ALegID: "repair9-a", BillingCallID: "repair9-call", BLegID: "repair9-b", AttemptID: "repair9-attempt"}
	now := time.Unix(9_000+int64(sequence), 0).UTC()
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event", Revision: 1,
		StreamID: stream, Sequence: sequence, Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
		Correlation: metering.CorrelationV2{StoreID: subject.StoreID, ALegID: subject.ALegID, BillingCallID: subject.BillingCallID, BLegID: subject.BLegID, AttemptID: subject.AttemptID},
		Semantics:   semantics, ObservedAt: now, ReceivedAt: now, MappingRef: "repair9.v1", Charges: charges,
	}
}

func repair9ChargeAmount(snapshot aggregate.SnapshotV2, item string) string {
	for _, charge := range snapshot.Charges {
		if charge.Charge.ChargeItemID == item && charge.Charge.Amount != nil {
			return charge.Charge.Amount.CanonicalString()
		}
	}
	return ""
}

func repair9ReducedCharge(t *testing.T, snapshot aggregate.SnapshotV2, item string) *aggregate.ReducedCharge {
	t.Helper()
	for i := range snapshot.Charges {
		if snapshot.Charges[i].Charge.ChargeItemID == item {
			return &snapshot.Charges[i]
		}
	}
	return nil
}
