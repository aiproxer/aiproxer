package replay_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/replay"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestDeduplicate_ReceiptRetryIsNoopButChangedPayloadConflicts(t *testing.T) {
	t.Parallel()
	base := replayObservation("event", "source", 1, "5")
	retry := base
	retry.ReceivedAt = retry.ReceivedAt.Add(time.Hour)
	result, err := replay.Deduplicate([]metering.Observation{retry, base})
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed != 1 || len(result.Observations) != 1 {
		t.Fatalf("retry result=%+v", result)
	}

	changed := base
	changed.Measures = []metering.Measure{replayMeasure("6")}
	if _, err := replay.Deduplicate([]metering.Observation{base, changed}); !errors.Is(err, replay.ErrIdentityConflict) {
		t.Fatalf("changed payload error=%v want ErrIdentityConflict", err)
	}
}

func TestDeduplicate_QuantityEqualityDoesNotDeduplicateDistinctEvents(t *testing.T) {
	t.Parallel()
	a := replayObservation("event-a", "source-a", 1, "5")
	b := replayObservation("event-b", "source-b", 2, "5")
	result, err := replay.Deduplicate([]metering.Observation{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed != 0 || len(result.Observations) != 2 {
		t.Fatalf("equal quantities were incorrectly deduplicated: %+v", result)
	}
}

func TestDeduplicate_IdentityRetainsIndependentOriginAndAcquisition(t *testing.T) {
	t.Parallel()
	provider := replayObservation("event", "same-source-key", 1, "5")
	local := provider
	local.Origin = metering.OriginLocal
	local.Acquisition = metering.AcquisitionLocalTokenizer
	result, err := replay.Deduplicate([]metering.Observation{provider, local})
	if err != nil {
		t.Fatal(err)
	}
	if result.Replayed != 0 || len(result.Observations) != 2 {
		t.Fatalf("origin/acquisition scopes were merged: %+v", result)
	}
}

func replayObservation(id, sourceEvent string, sequence uint64, value string) metering.Observation {
	subject := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "replay-store", BLegID: "b-leg", AttemptID: "attempt"}
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: sourceEvent, Revision: 1,
		StreamID: "replay-stream", Sequence: sequence, Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator,
		Boundary: metering.BoundaryBackendEgress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject: subject, Correlation: metering.CorrelationV2{StoreID: "replay-store", BLegID: "b-leg", AttemptID: "attempt", ProviderAccountKey: "account"},
		Semantics: metering.SemanticsDelta, ObservedAt: time.Unix(1, 0).UTC(), ReceivedAt: time.Unix(1, 0).UTC(), MappingRef: "replay.v1",
		Measures: []metering.Measure{replayMeasure(value)},
	}
}

func replayMeasure(value string) metering.Measure {
	decimal, err := metering.ParseDecimal(value)
	if err != nil {
		panic(err)
	}
	return metering.Measure{
		Key:   metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID},
		Value: &decimal, Quality: metering.QualityObserved, MethodRef: "replay.v1",
	}
}
