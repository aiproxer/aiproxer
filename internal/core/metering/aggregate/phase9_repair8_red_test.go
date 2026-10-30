package aggregate_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase9Repair8_MeasureSupersessionRejectsForeignSourceScopeInAnyOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*metering.Observation)
	}{
		{
			name: "stream id",
			mutate: func(observation *metering.Observation) {
				observation.StreamID = "repair8-measure-stream-other"
			},
		},
		{
			name: "acquisition",
			mutate: func(observation *metering.Observation) {
				observation.Acquisition = metering.AcquisitionLocalTransport
			},
		},
		{
			name: "provider account",
			mutate: func(observation *metering.Observation) {
				observation.Correlation.ProviderAccountKey = "repair8-measure-account-other"
			},
		},
		{
			name: "provider charge context",
			mutate: func(observation *metering.Observation) {
				observation.Correlation.ProviderChargeID = "repair8-measure-charge-other"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			parent := repair8AggregateObservation("repair8-measure-parent-"+test.name, "repair8-measure-stream", 1, metering.SemanticsCumulative, "5")
			parentRef, err := parent.Ref(parent.Subject.StoreID)
			if err != nil {
				t.Fatalf("parent ref: %v", err)
			}
			correction := repair8AggregateObservation("repair8-measure-correction-"+test.name, "repair8-measure-stream", 2, metering.SemanticsCorrection, "2")
			correction.Supersedes = []metering.ObservationRef{parentRef}
			test.mutate(&correction)

			for order, observations := range [][]metering.Observation{
				{parent, correction},
				{correction, parent},
			} {
				_, err := aggregate.ApplyObservations(observations)
				if !errors.Is(err, metering.ErrInvalidRevision) {
					t.Fatalf("order %d error=%v, want ErrInvalidRevision for foreign measure scope", order, err)
				}
			}
		})
	}
}

func TestPhase9Repair8_MeasureSameScopeCorrectionLeavesForeignSibling(t *testing.T) {
	t.Parallel()

	parent := repair8AggregateObservation("repair8-measure-valid-parent", "repair8-measure-stream", 1, metering.SemanticsCumulative, "5")
	parentRef, err := parent.Ref(parent.Subject.StoreID)
	if err != nil {
		t.Fatalf("parent ref: %v", err)
	}
	correction := repair8AggregateObservation("repair8-measure-valid-correction", "repair8-measure-stream", 2, metering.SemanticsCorrection, "2")
	correction.Supersedes = []metering.ObservationRef{parentRef}
	sibling := repair8AggregateObservation("repair8-measure-sibling", "repair8-measure-stream-other", 1, metering.SemanticsDelta, "9")

	key := parent.Measures[0].Key
	var want aggregate.SnapshotV2
	for order, observations := range [][]metering.Observation{
		{parent, sibling, correction},
		{correction, sibling, parent},
		{sibling, correction, parent},
	} {
		snapshot, err := aggregate.ApplyObservations(observations)
		if err != nil {
			t.Fatalf("order %d apply: %v", order, err)
		}
		if order == 0 {
			want = snapshot
		} else if !reflect.DeepEqual(snapshot, want) {
			t.Fatalf("order %d changed same-scope reduction:\n got=%+v\nwant=%+v", order, snapshot, want)
		}
		if got := snapshot.ValueFor(correction, key); got != "7/0" {
			t.Fatalf("order %d corrected value=%q, want 7/0", order, got)
		}
		if got := snapshot.ValueFor(sibling, key); got != "9/0" {
			t.Fatalf("order %d foreign sibling value=%q, want 9/0", order, got)
		}
	}
}

func TestPhase9Repair8_ProviderChargeSupersessionRejectsForeignChargeScopeInAnyOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*metering.Observation)
	}{
		{
			name: "stream id",
			mutate: func(observation *metering.Observation) {
				observation.StreamID = "repair8-charge-stream-other"
			},
		},
		{
			name: "acquisition",
			mutate: func(observation *metering.Observation) {
				observation.Acquisition = metering.AcquisitionProviderHeader
				observation.Origin = metering.OriginProvider
			},
		},
		{
			name: "provider account",
			mutate: func(observation *metering.Observation) {
				observation.Correlation.ProviderAccountKey = "repair8-charge-account-other"
			},
		},
		{
			name: "provider charge context",
			mutate: func(observation *metering.Observation) {
				observation.Correlation.ProviderChargeID = "repair8-charge-context-other"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			parent := repair8ProviderChargeObservation("repair8-charge-parent-"+test.name, "repair8-charge-stream", 1, metering.SemanticsCumulative, "5")
			parentRef, err := parent.Ref(parent.Subject.StoreID)
			if err != nil {
				t.Fatalf("parent ref: %v", err)
			}
			correction := repair8ProviderChargeObservation("repair8-charge-correction-"+test.name, "repair8-charge-stream", 2, metering.SemanticsCorrection, "2")
			correction.Supersedes = []metering.ObservationRef{parentRef}
			test.mutate(&correction)

			for order, observations := range [][]metering.Observation{
				{parent, correction},
				{correction, parent},
			} {
				_, err := aggregate.ApplyObservations(observations)
				if !errors.Is(err, metering.ErrInvalidRevision) {
					t.Fatalf("order %d error=%v, want ErrInvalidRevision for foreign provider charge scope", order, err)
				}
			}
		})
	}
}

func TestPhase9Repair8_ProviderChargeSameScopeCorrectionLeavesForeignSibling(t *testing.T) {
	t.Parallel()

	parent := repair8ProviderChargeObservation("repair8-charge-valid-parent", "repair8-charge-stream", 1, metering.SemanticsCumulative, "5")
	parentRef, err := parent.Ref(parent.Subject.StoreID)
	if err != nil {
		t.Fatalf("parent ref: %v", err)
	}
	correction := repair8ProviderChargeObservation("repair8-charge-valid-correction", "repair8-charge-stream", 2, metering.SemanticsCorrection, "2")
	correction.Supersedes = []metering.ObservationRef{parentRef}
	sibling := repair8ProviderChargeObservation("repair8-charge-sibling", "repair8-charge-stream-other", 1, metering.SemanticsDelta, "9")

	var want aggregate.SnapshotV2
	for order, observations := range [][]metering.Observation{
		{parent, sibling, correction},
		{correction, sibling, parent},
		{sibling, correction, parent},
	} {
		snapshot, err := aggregate.ApplyObservations(observations)
		if err != nil {
			t.Fatalf("order %d apply: %v", order, err)
		}
		if order == 0 {
			want = snapshot
		} else if !reflect.DeepEqual(snapshot, want) {
			t.Fatalf("order %d changed same-scope charge reduction:\n got=%+v\nwant=%+v", order, snapshot, want)
		}
		if got := repair8ChargeValue(snapshot, "repair8-charge-stream", "usage"); got != "2/0" {
			t.Fatalf("order %d corrected charge=%q, want 2/0", order, got)
		}
		if got := repair8ChargeValue(snapshot, "repair8-charge-stream-other", "usage"); got != "9/0" {
			t.Fatalf("order %d foreign sibling charge=%q, want 9/0", order, got)
		}
	}
}

func repair8AggregateObservation(id, stream string, sequence uint64, semantics, value string) metering.Observation {
	decimal, err := metering.ParseDecimal(value)
	if err != nil {
		panic(err)
	}
	subject := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "repair8-aggregate-store", BLegID: "repair8-aggregate-b-leg", AttemptID: "repair8-aggregate-attempt"}
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event", Revision: 1,
		StreamID: stream, Sequence: sequence, Origin: metering.OriginLocal, Acquisition: metering.AcquisitionLocalTokenizer,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendEgress,
		Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
		Correlation: metering.CorrelationV2{StoreID: subject.StoreID, BLegID: subject.BLegID, AttemptID: subject.AttemptID, ProviderAccountKey: "repair8-aggregate-account", ProviderChargeID: "repair8-aggregate-charge"},
		Semantics:   semantics, ObservedAt: time.Unix(8_100, 0).UTC(), ReceivedAt: time.Unix(8_100, 0).UTC(), MappingRef: "repair8.aggregate.v1",
		Measures: []metering.Measure{{Key: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "repair8.aggregate.v1"}, Value: &decimal, Quality: metering.QualityObserved, MethodRef: "repair8.aggregate.v1"}},
	}
}

func repair8ProviderChargeObservation(id, stream string, sequence uint64, semantics, value string) metering.Observation {
	decimal, err := metering.ParseDecimal(value)
	if err != nil {
		panic(err)
	}
	subject := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "repair8-charge-store", BLegID: "repair8-charge-b-leg", AttemptID: "repair8-charge-attempt"}
	component := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "repair8.charge.v1"}
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event", Revision: 1,
		StreamID: stream, Sequence: sequence, Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
		Correlation: metering.CorrelationV2{StoreID: subject.StoreID, BLegID: subject.BLegID, AttemptID: subject.AttemptID, ProviderAccountKey: "repair8-charge-account", ProviderChargeID: "repair8-charge-context"},
		Semantics:   semantics, ObservedAt: time.Unix(8_200, 0).UTC(), ReceivedAt: time.Unix(8_200, 0).UTC(), MappingRef: "repair8.charge.v1",
		Charges: []metering.ReportedCharge{{ChargeItemID: "usage", Component: &component, Amount: &decimal, Currency: "USD", Kind: metering.ChargeKindComponent}},
	}
}

func repair8ChargeValue(snapshot aggregate.SnapshotV2, stream, item string) string {
	for _, charge := range snapshot.Charges {
		if charge.Scope.StreamID == stream && charge.Charge.ChargeItemID == item && charge.Charge.Amount != nil {
			return charge.Charge.Amount.CanonicalString()
		}
	}
	return ""
}
