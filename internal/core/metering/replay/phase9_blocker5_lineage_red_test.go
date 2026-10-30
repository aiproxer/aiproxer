package replay

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase9Blocker5_DeduplicateNormalizesSubjectCorrelationPlacement(t *testing.T) {
	t.Parallel()

	subjectCarried := blocker5ReplayObservation(blocker5ReplaySubjectCarried)
	correlationCarried := blocker5ReplayObservation(blocker5ReplayCorrelationCarried)
	result, err := Deduplicate([]metering.Observation{subjectCarried, correlationCarried})
	if err != nil {
		t.Fatalf("equivalent carrier placement rejected: %v", err)
	}
	if result.Replayed != 1 || len(result.Observations) != 1 {
		t.Fatalf("replay result=%+v, want one survivor and one replay", result)
	}
}

type blocker5ReplayCarrier uint8

const (
	blocker5ReplaySubjectCarried blocker5ReplayCarrier = iota
	blocker5ReplayCorrelationCarried
)

func blocker5ReplayObservation(carrier blocker5ReplayCarrier) metering.Observation {
	value, err := metering.ParseDecimal("5")
	if err != nil {
		panic(err)
	}
	const storeID = "blocker5-replay-store"
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: storeID, TenantID: "blocker5-replay-tenant", ALegID: "blocker5-replay-a-leg",
		BillingCallID: "blocker5-replay-billing-call", CallID: "blocker5-replay-call", BLegID: "blocker5-replay-b-leg",
		AttemptID: "blocker5-replay-attempt", AttemptSeq: 3, ProviderAccountKey: "blocker5-replay-account",
	}
	correlation := metering.CorrelationV2{StoreID: storeID, BLegID: subject.BLegID}
	if carrier == blocker5ReplaySubjectCarried {
		correlation.ParentWorkID = "blocker5-replay-parent"
	} else {
		subject.TenantID = ""
		subject.ALegID = ""
		subject.BillingCallID = ""
		subject.CallID = ""
		subject.AttemptID = ""
		subject.AttemptSeq = 0
		subject.ProviderAccountKey = ""
		correlation = metering.CorrelationV2{
			StoreID: storeID, TenantID: "blocker5-replay-tenant", ALegID: "blocker5-replay-a-leg",
			BillingCallID: "blocker5-replay-billing-call", CallID: "blocker5-replay-call", BLegID: subject.BLegID,
			AttemptID: "blocker5-replay-attempt", AttemptSeq: 3, ProviderAccountKey: "blocker5-replay-account",
			ParentWorkID: "blocker5-replay-parent",
		}
	}
	now := time.Unix(21_000, 0).UTC()
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: "blocker5-replay-observation", SourceEventKey: "blocker5-replay-source", Revision: 1,
		StreamID: "blocker5-replay-stream", Sequence: 1, Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle: metering.LifecycleBackendAttempt, Subject: subject, Correlation: correlation,
		Semantics: metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "blocker5.replay.v1",
		Measures: []metering.Measure{{Key: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "blocker5.replay.v1"}, Value: &value, Quality: metering.QualityObserved}},
	}
}
