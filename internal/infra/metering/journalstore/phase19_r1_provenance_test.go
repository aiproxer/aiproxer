package journalstore_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// Open-source half of the phase19 R1 provenance vectors (task 3.5 source
// split). The real retail selector/rating monetary halves of both vectors
// (frozen observation refs, the 6100000000 nano customer charge, exactly two
// inference lines, full inference/composite ref resolution, correction rating
// lineage across the real rater) are preserved in
// enterprise/internal/preservation/journal_provenance_test.go. What remains
// here is the journal ownership contract: both originals persist, references
// resolve to the original immutable rows, and rating never mutates the stored
// payload.

var phase19R1ProvenanceDSN atomic.Int64

func phase19R1ProvenanceStore(t *testing.T) *journalstore.DurableStore {
	t.Helper()
	dsn := fmt.Sprintf("file:phase19r1-e2e-%d?mode=memory&cache=shared&_pragma=busy_timeout(5000)", phase19R1ProvenanceDSN.Add(1))
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journalstore.NewDurableStore(context.Background(), bunDB, journalstore.DurableConfig{StoreID: "store-1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func phase19R1Decimal(t *testing.T, value string) *metering.Decimal {
	t.Helper()
	d, err := metering.ParseDecimal(value)
	if err != nil {
		t.Fatal(err)
	}
	return &d
}

func phase19R1Observation(t *testing.T, callID usageevidence.BillingCallID, bLegID, id, origin string, measures ...metering.Measure) metering.Observation {
	t.Helper()
	acquisition := metering.AcquisitionProviderResponse
	if origin == metering.OriginLocal {
		acquisition = metering.AcquisitionLocalTransport
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-source", Revision: 1, StreamID: id + "-stream", Sequence: 1,
		Origin: origin, Acquisition: acquisition, Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator,
		Boundary: metering.BoundaryBackendEgress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-1", ALegID: "a-1", BillingCallID: callID.String(), BLegID: bLegID},
		Correlation: metering.CorrelationV2{StoreID: "store-1", CallID: callID.String(), BillingCallID: callID.String(), ALegID: "a-1", BLegID: bLegID},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "phase19r1:retail:v1", Measures: measures,
	}
}

func phase19R1Measure(t *testing.T, key metering.ComponentKey, quantity string) metering.Measure {
	t.Helper()
	return metering.Measure{Key: key, Value: phase19R1Decimal(t, quantity), Quality: metering.QualityObserved}
}

// A. Mixed provider observation (output token) plus local observation (same
// output plus a local-only image): both originals persist, and every stored
// row stays the immutable original — the local observation keeps its full
// two-measure payload and is never rewritten to a one-measure payload under
// the same identity.
func TestPhase19R1ProvenanceMixedPartialSelectionResolvesJournalRefs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	outputKey := metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	imageKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImageToken, Unit: metering.UnitToken, SchemaID: "openai.usage.v2"}
	providerObs := phase19R1Observation(t, callID, "b-winner", "provider-output", metering.OriginProvider,
		phase19R1Measure(t, outputKey, "200"))
	localObs := phase19R1Observation(t, callID, "b-winner", "local-output-image", metering.OriginLocal,
		phase19R1Measure(t, outputKey, "200"),
		phase19R1Measure(t, imageKey, "2"))

	store := phase19R1ProvenanceStore(t)
	if err := store.AppendObservation(ctx, providerObs); err != nil {
		t.Fatalf("append provider original: %v", err)
	}
	if err := store.AppendObservation(ctx, localObs); err != nil {
		t.Fatalf("append local original: %v", err)
	}

	// Both originals are frozen for audit, and each reference resolves to the
	// original immutable journal row.
	providerRef, err := providerObs.Ref(providerObs.Subject.StoreID)
	if err != nil {
		t.Fatalf("provider ref: %v", err)
	}
	storedProvider, err := store.GetObservationRef(ctx, providerRef)
	if err != nil {
		t.Fatalf("provider ref does not resolve to the persisted journal row: %v", err)
	}
	if len(storedProvider.Measures) != 1 {
		t.Fatalf("provider ref resolves to %d measures, want the original one-measure payload", len(storedProvider.Measures))
	}
	localRef, err := localObs.Ref(localObs.Subject.StoreID)
	if err != nil {
		t.Fatalf("local ref: %v", err)
	}
	storedLocal, err := store.GetObservationRef(ctx, localRef)
	if err != nil {
		t.Fatalf("local ref does not resolve to the persisted journal row: %v", err)
	}
	if len(storedLocal.Measures) != 2 {
		t.Fatalf("local ref resolves to %d measures, want the original full 2-measure observation", len(storedLocal.Measures))
	}
	if storedLocal.ID != "local-output-image" {
		t.Fatalf("local ref resolved %q, want local-output-image", storedLocal.ID)
	}
}

// B. Linked correction/supersedes variant: all three originals persist, the
// correction lineage names the exact original base ref, and the stored/base
// observations remain immutable.
func TestPhase19R1ProvenanceCorrectionLineageResolvesJournalRefs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	outputKey := metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
	imageKey := metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentImageToken, Unit: metering.UnitToken, SchemaID: "openai.usage.v2"}
	providerObs := phase19R1Observation(t, callID, "b-winner", "provider-output", metering.OriginProvider,
		phase19R1Measure(t, outputKey, "200"))
	localBase := phase19R1Observation(t, callID, "b-winner", "local-base", metering.OriginLocal,
		phase19R1Measure(t, outputKey, "200"),
		phase19R1Measure(t, imageKey, "1"))
	baseRef, err := localBase.Ref(localBase.Subject.StoreID)
	if err != nil {
		t.Fatalf("local base ref: %v", err)
	}
	correction := localBase.Clone()
	correction.ID = "local-correction"
	correction.SourceEventKey = "local-correction-source"
	correction.Sequence = localBase.Sequence + 1
	correction.Semantics = metering.SemanticsCorrection
	correction.Measures = []metering.Measure{phase19R1Measure(t, imageKey, "1")}
	correction.Supersedes = []metering.ObservationRef{baseRef}

	store := phase19R1ProvenanceStore(t)
	for _, observation := range []metering.Observation{providerObs, localBase, correction} {
		if err := store.AppendObservation(ctx, observation); err != nil {
			t.Fatalf("append %q: %v", observation.ID, err)
		}
	}

	storedCorrection, err := store.GetObservation(ctx, correction.ID, correction.Revision)
	if err != nil {
		t.Fatalf("journal correction: %v", err)
	}
	if len(storedCorrection.Supersedes) != 1 || !storedCorrection.Supersedes[0].Equal(baseRef) {
		t.Fatalf("correction lineage = %+v, want original base ref %+v", storedCorrection.Supersedes, baseRef)
	}
	storedBase, err := store.GetObservationRef(ctx, baseRef)
	if err != nil {
		t.Fatalf("base ref does not resolve to the persisted journal row: %v", err)
	}
	if len(storedBase.Measures) != 2 {
		t.Fatalf("base ref resolves to %d measures, want the original full 2-measure payload", len(storedBase.Measures))
	}
	if storedBase.ID != "local-base" {
		t.Fatalf("base ref resolved %q, want local-base", storedBase.ID)
	}
}
