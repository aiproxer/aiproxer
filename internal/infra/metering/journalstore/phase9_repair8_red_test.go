package journalstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase9Repair8_DurableReplayRejectsForeignSupersessionScope(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newSQLiteJournal(t)
	parent := repair8DurableObservation("repair8-durable-parent", "repair8-durable-stream", 1, metering.SemanticsCumulative)
	parentRef, err := parent.Ref(parent.Subject.StoreID)
	if err != nil {
		t.Fatalf("parent ref: %v", err)
	}
	correction := repair8DurableObservation("repair8-durable-correction", "repair8-durable-stream-other", 2, metering.SemanticsCorrection)
	correction.Supersedes = []metering.ObservationRef{parentRef}

	if err := store.AppendObservation(ctx, parent); err != nil {
		t.Fatalf("append parent: %v", err)
	}
	if err := store.AppendObservation(ctx, correction); err != nil {
		t.Fatalf("append correction: %v", err)
	}
	if err := store.RebuildObservationProjections(ctx); !errors.Is(err, metering.ErrInvalidRevision) {
		t.Fatalf("durable replay error=%v, want ErrInvalidRevision for foreign source scope", err)
	}
}

func TestPhase9Repair8_DurableReplayAcceptsLateSameScopeSupersession(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newSQLiteJournal(t)
	parent := repair8DurableObservation("repair8-durable-valid-parent", "repair8-durable-stream", 1, metering.SemanticsCumulative)
	parentRef, err := parent.Ref(parent.Subject.StoreID)
	if err != nil {
		t.Fatalf("parent ref: %v", err)
	}
	correction := repair8DurableObservation("repair8-durable-valid-correction", "repair8-durable-stream", 2, metering.SemanticsCorrection)
	correction.Supersedes = []metering.ObservationRef{parentRef}

	// The correction may be durable before its parent; replay must resolve the
	// same complete source scope once both revisions are present.
	if err := store.AppendObservation(ctx, correction); err != nil {
		t.Fatalf("append correction: %v", err)
	}
	if err := store.AppendObservation(ctx, parent); err != nil {
		t.Fatalf("append parent: %v", err)
	}
	if err := store.RebuildObservationProjections(ctx); err != nil {
		t.Fatalf("same-scope durable replay: %v", err)
	}
}

func repair8DurableObservation(id, stream string, sequence uint64, semantics string) metering.Observation {
	value, err := metering.ParseDecimal("5")
	if err != nil {
		panic(err)
	}
	subject := metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "sqlite-test", BLegID: "repair8-durable-b-leg", AttemptID: "repair8-durable-attempt"}
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event", Revision: 1,
		StreamID: stream, Sequence: sequence, Origin: metering.OriginLocal, Acquisition: metering.AcquisitionLocalTokenizer,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendEgress,
		Lifecycle: metering.LifecycleBackendAttempt, Subject: subject,
		Correlation: metering.CorrelationV2{StoreID: subject.StoreID, BLegID: subject.BLegID, AttemptID: subject.AttemptID, ProviderAccountKey: "repair8-durable-account", ProviderChargeID: "repair8-durable-charge"},
		Semantics:   semantics, ObservedAt: time.Unix(8_300, 0).UTC(), ReceivedAt: time.Unix(8_300, 0).UTC(), MappingRef: "repair8.durable.v1",
		Measures: []metering.Measure{{Key: metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImage, Unit: metering.UnitImage, SchemaID: "repair8.durable.v1"}, Value: &value, Quality: metering.QualityObserved, MethodRef: "repair8.durable.v1"}},
	}
}
