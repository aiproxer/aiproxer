package metering_test

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase2ReviewV1BridgeDoesNotInventLineage(t *testing.T) {
	t.Parallel()

	fact := metering.Fact{
		FactID: "fact-with-request", StreamID: "stream-1", Sequence: 1,
		Kind: metering.FactKindDelta, Perspective: metering.PerspectiveOperator,
		Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Correlation: metering.Correlation{RequestID: "request-1"},
		Source:      metering.SourceProviderReported, Authority: metering.AuthorityAuthoritative,
		Presence: metering.PresencePresent,
		Quantities: []metering.Quantity{{
			Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 1, Present: true,
		}},
	}
	observation, err := metering.ObservationFromFact(fact)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Correlation.CallID != "" {
		t.Fatalf("legacy request identity must not be copied into CallID: %q", observation.Correlation.CallID)
	}
	if observation.Subject.Kind != metering.SubjectRequest || observation.Subject.RequestID != "request-1" {
		t.Fatalf("legacy subject must use the proven request identity: %+v", observation.Subject)
	}

	fact.Correlation = metering.Correlation{}
	if _, err := metering.ObservationFromFact(fact); !errors.Is(err, metering.ErrUnrepresentableV1) {
		t.Fatalf("legacy fact without a proven subject must fail closed, err=%v", err)
	}
}

func TestPhase2ReviewV1ProjectionLeavesParentWorkTraceAbsent(t *testing.T) {
	t.Parallel()

	observation := validV2Observation(t)
	observation.Measures = []metering.Measure{{
		Key: metering.ComponentKey{
			Direction: metering.DirectionOutput,
			Component: metering.ComponentOutputToken,
			Unit:      metering.UnitToken,
		},
		Value:   decimalPtr("1"),
		Quality: metering.QualityObserved,
	}}
	observation.Correlation.ParentWorkID = "parent-work-1"

	projected, err := metering.ProjectObservationToFact(observation)
	if err != nil {
		t.Fatal(err)
	}
	if projected.Correlation.TraceID != "" {
		t.Fatalf("ParentWorkID must not be fabricated as legacy TraceID: %q", projected.Correlation.TraceID)
	}
}

func TestPhase2ReviewNonDirectionalKeysRetainNone(t *testing.T) {
	t.Parallel()

	cases := []metering.ComponentKey{
		{Direction: metering.DirectionNone, Component: metering.ComponentRequest, Unit: metering.UnitCount},
		{Direction: metering.DirectionNone, Component: metering.ComponentToolQuery, Unit: metering.UnitCount},
		{Direction: metering.DirectionNone, Component: metering.ComponentCredit, Unit: metering.UnitCredit},
		{Direction: metering.DirectionNone, Component: metering.ComponentStorage, Unit: metering.UnitByteSecond},
		{Direction: metering.DirectionNone, Component: "provider:resource", Unit: metering.UnitSecond, SchemaID: "provider:resource:v1"},
		{Direction: metering.DirectionNone, Component: "provider:account", Unit: metering.UnitPercent, SchemaID: "provider:account:v1"},
	}
	for _, key := range cases {
		if err := key.Validate(); err != nil {
			t.Errorf("non-directional key rejected: %+v: %v", key, err)
		}
	}
}
