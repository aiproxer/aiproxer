package metering_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase9Repair9_SupersessionNormalizesSubjectAndCorrelationLineage(t *testing.T) {
	t.Parallel()

	parent := repair9LineageObservation("repair9-lineage-parent", 1, metering.SemanticsCumulative)
	parent.Subject.BillingCallID = "repair9-call"
	parent.Correlation.BillingCallID = ""
	parent.Subject.ProviderAccountKey = "repair9-account"
	parent.Correlation.ProviderAccountKey = ""
	parentRef, err := parent.Ref(parent.Subject.StoreID)
	if err != nil {
		t.Fatalf("parent ref: %v", err)
	}
	correction := repair9LineageObservation("repair9-lineage-correction", 2, metering.SemanticsCorrection)
	correction.Subject.BillingCallID = ""
	correction.Correlation.BillingCallID = "repair9-call"
	correction.Subject.ProviderAccountKey = ""
	correction.Correlation.ProviderAccountKey = "repair9-account"
	correction.Supersedes = []metering.ObservationRef{parentRef}

	for order, observations := range [][]metering.Observation{{parent, correction}, {correction, parent}} {
		if err := metering.ValidateSupersessionGraph(observations); err != nil {
			t.Fatalf("order %d same effective lineage error=%v", order, err)
		}
	}

	foreign := correction
	foreign.ID = "repair9-lineage-foreign"
	foreign.SourceEventKey = "repair9-lineage-foreign-event"
	foreign.Correlation.BillingCallID = "repair9-other-call"
	foreign.Supersedes = []metering.ObservationRef{parentRef}
	if err := metering.ValidateSupersessionGraph([]metering.Observation{parent, foreign}); !errors.Is(err, metering.ErrInvalidRevision) {
		t.Fatalf("foreign lineage error=%v, want ErrInvalidRevision", err)
	}
}

func repair9LineageObservation(id string, sequence uint64, semantics string) metering.Observation {
	value, err := metering.ParseDecimal("5")
	if err != nil {
		panic(err)
	}
	subject := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "repair9-lineage-store", ALegID: "repair9-a", BLegID: "repair9-b", AttemptID: "repair9-attempt"}
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event", Revision: 1,
		StreamID: "repair9-lineage-stream", Sequence: sequence, Origin: metering.OriginProvider,
		Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress,
		Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
		Correlation: metering.CorrelationV2{StoreID: subject.StoreID, ALegID: subject.ALegID, BLegID: subject.BLegID, AttemptID: subject.AttemptID},
		Semantics:   semantics, ObservedAt: repair9Time(sequence), ReceivedAt: repair9Time(sequence), MappingRef: "repair9.lineage.v1",
		Measures: []metering.Measure{{Key: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "repair9.lineage.v1"}, Value: &value, Quality: metering.QualityObserved}},
	}
}

func repair9Time(sequence uint64) time.Time {
	return time.Unix(9_000+int64(sequence), 0).UTC()
}
