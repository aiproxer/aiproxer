package aggregate_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase9Blocker5_AggregateNormalizesSubjectCorrelationPlacement(t *testing.T) {
	t.Parallel()

	for order, observations := range [][]metering.Observation{
		{blocker5AggregateObservation(blocker5AggregateSubjectCarried), blocker5AggregateObservation(blocker5AggregateCorrelationCarried)},
		{blocker5AggregateObservation(blocker5AggregateCorrelationCarried), blocker5AggregateObservation(blocker5AggregateSubjectCarried)},
	} {
		snapshot, err := aggregate.ApplyObservations(observations)
		if err != nil {
			t.Fatalf("order %d equivalent carrier placement rejected: %v", order, err)
		}
		if snapshot.Replayed != 1 || len(snapshot.Observations) != 1 {
			t.Fatalf("order %d snapshot=%+v, want one survivor and one replay", order, snapshot)
		}
		if len(snapshot.Measures) != 1 || snapshot.Measures[0].Value.CanonicalString() != "5/0" {
			t.Fatalf("order %d measures=%+v, want one value 5/0", order, snapshot.Measures)
		}
	}
}

type blocker5AggregateCarrier uint8

const (
	blocker5AggregateSubjectCarried blocker5AggregateCarrier = iota
	blocker5AggregateCorrelationCarried
)

func blocker5AggregateObservation(carrier blocker5AggregateCarrier) metering.Observation {
	value, err := metering.ParseDecimal("5")
	if err != nil {
		panic(err)
	}
	const storeID = "blocker5-aggregate-store"
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: storeID, TenantID: "blocker5-aggregate-tenant", ALegID: "blocker5-aggregate-a-leg",
		BillingCallID: "blocker5-aggregate-billing-call", CallID: "blocker5-aggregate-call", BLegID: "blocker5-aggregate-b-leg",
		AttemptID: "blocker5-aggregate-attempt", AttemptSeq: 3, ProviderAccountKey: "blocker5-aggregate-account",
	}
	correlation := metering.CorrelationV2{StoreID: storeID, BLegID: subject.BLegID}
	if carrier == blocker5AggregateSubjectCarried {
		correlation.ParentWorkID = "blocker5-aggregate-parent"
	} else {
		subject.TenantID = ""
		subject.ALegID = ""
		subject.BillingCallID = ""
		subject.CallID = ""
		subject.AttemptID = ""
		subject.AttemptSeq = 0
		subject.ProviderAccountKey = ""
		correlation = metering.CorrelationV2{
			StoreID: storeID, TenantID: "blocker5-aggregate-tenant", ALegID: "blocker5-aggregate-a-leg",
			BillingCallID: "blocker5-aggregate-billing-call", CallID: "blocker5-aggregate-call", BLegID: subject.BLegID,
			AttemptID: "blocker5-aggregate-attempt", AttemptSeq: 3, ProviderAccountKey: "blocker5-aggregate-account",
			ParentWorkID: "blocker5-aggregate-parent",
		}
	}
	now := time.Unix(22_000, 0).UTC()
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: "blocker5-aggregate-observation", SourceEventKey: "blocker5-aggregate-source", Revision: 1,
		StreamID: "blocker5-aggregate-stream", Sequence: 1, Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle: metering.LifecycleBackendAttempt, Subject: subject, Correlation: correlation,
		Semantics: metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "blocker5.aggregate.v1",
		Measures: []metering.Measure{{Key: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "blocker5.aggregate.v1"}, Value: &value, Quality: metering.QualityObserved}},
	}
}
