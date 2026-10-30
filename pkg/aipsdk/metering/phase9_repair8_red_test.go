package metering_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase9Repair8_SupersessionRejectsForeignSourceScopeInAnyOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*metering.Observation)
	}{
		{
			name: "stream id",
			mutate: func(observation *metering.Observation) {
				observation.StreamID = "repair8-stream-other"
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
				observation.Correlation.ProviderAccountKey = "repair8-account-other"
			},
		},
		{
			name: "provider charge context",
			mutate: func(observation *metering.Observation) {
				observation.Correlation.ProviderChargeID = "repair8-charge-other"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			parent := repair8Observation("repair8-parent-"+test.name, 1, metering.SemanticsCumulative)
			parentRef, err := parent.Ref(parent.Subject.StoreID)
			if err != nil {
				t.Fatalf("parent ref: %v", err)
			}
			correction := repair8Observation("repair8-correction-"+test.name, 2, metering.SemanticsCorrection)
			correction.Supersedes = []metering.ObservationRef{parentRef}
			test.mutate(&correction)

			for order, observations := range [][]metering.Observation{
				{parent, correction},
				{correction, parent},
			} {
				err := metering.ValidateSupersessionGraph(observations)
				if !errors.Is(err, metering.ErrInvalidRevision) {
					t.Fatalf("order %d error=%v, want ErrInvalidRevision for foreign source scope", order, err)
				}
			}
		})
	}
}

func TestPhase9Repair8_SupersessionAcceptsCompleteSameScopeInAnyOrder(t *testing.T) {
	t.Parallel()

	parent := repair8Observation("repair8-valid-parent", 1, metering.SemanticsCumulative)
	parentRef, err := parent.Ref(parent.Subject.StoreID)
	if err != nil {
		t.Fatalf("parent ref: %v", err)
	}
	correction := repair8Observation("repair8-valid-correction", 2, metering.SemanticsCorrection)
	correction.Supersedes = []metering.ObservationRef{parentRef}

	for order, observations := range [][]metering.Observation{
		{parent, correction},
		{correction, parent},
	} {
		if err := metering.ValidateSupersessionGraph(observations); err != nil {
			t.Fatalf("order %d same-scope edge: %v", order, err)
		}
	}
}

func repair8Observation(id string, sequence uint64, semantics string) metering.Observation {
	value, err := metering.ParseDecimal("5")
	if err != nil {
		panic(err)
	}
	subject := metering.SubjectRef{
		Kind: metering.SubjectBLeg, StoreID: "repair8-store", BLegID: "repair8-b-leg", AttemptID: "repair8-attempt",
	}
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event", Revision: 1,
		StreamID: "repair8-stream", Sequence: sequence, Origin: metering.OriginLocal,
		Acquisition: metering.AcquisitionLocalTokenizer, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendEgress,
		Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
		Correlation: metering.CorrelationV2{
			StoreID: subject.StoreID, BLegID: subject.BLegID, AttemptID: subject.AttemptID,
			ProviderAccountKey: "repair8-account", ProviderChargeID: "repair8-charge",
		},
		Semantics: semantics, ObservedAt: time.Unix(8_000, 0).UTC(), ReceivedAt: time.Unix(8_000, 0).UTC(),
		MappingRef: "repair8.v1", Measures: []metering.Measure{{
			Key:   metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "repair8.v1"},
			Value: &value, Quality: metering.QualityObserved, MethodRef: "repair8.v1",
		}},
	}
}
