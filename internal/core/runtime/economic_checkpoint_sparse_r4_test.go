package runtime

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	_ "modernc.org/sqlite"
)

// R4 adversarial repair: sparse cumulative snapshots must not coalesce away
// still-effective fields. Presence is per-field for cumulative evidence, so a
// later revision that omits a component cannot erase an earlier known value.

const r4StoreID = "store-r4"

func r4SparseObservation(revision uint64, measures ...metering.Measure) metering.Observation {
	now := time.Unix(1_700_000_000+int64(revision), 0).UTC()
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: "obs-r4-sparse", SourceEventKey: "provider-usage-r4",
		Revision: revision, StreamID: "stream-r4", Sequence: revision,
		Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator,
		Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: r4StoreID, ALegID: "a-r4",
			BillingCallID: "call-r4", BLegID: "b-r4",
		},
		Correlation: metering.CorrelationV2{
			StoreID: r4StoreID, CallID: "call-r4", BillingCallID: "call-r4", ALegID: "a-r4", BLegID: "b-r4",
		},
		Semantics: metering.SemanticsCumulative, ObservedAt: now, ReceivedAt: now, MappingRef: "r4",
		Measures: measures,
	}
}

func r4Decimal(value string) *metering.Decimal {
	d := metering.Decimal{Coefficient: value, Scale: 0}
	return &d
}

func r4InputMeasure(value string) metering.Measure {
	return metering.Measure{
		Key:   metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentInputToken, Unit: metering.UnitToken},
		Value: r4Decimal(value), Quality: metering.QualityObserved,
	}
}

func r4OutputMeasure(value string) metering.Measure {
	return metering.Measure{
		Key:   metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken},
		Value: r4Decimal(value), Quality: metering.QualityObserved,
	}
}

func r4ImageMeasure(count string) metering.Measure {
	return metering.Measure{
		Key:   metering.ComponentKey{Direction: metering.DirectionOutput, Component: "image", Unit: metering.UnitImage},
		Value: r4Decimal(count), Quality: metering.QualityObserved,
	}
}

func r4UnavailableInputMeasure() metering.Measure {
	return metering.Measure{
		Key:     metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentInputToken, Unit: metering.UnitToken},
		Quality: metering.QualityUnavailable,
	}
}

// r4UnknownInputMeasure is a QualityUnknown measure that still carries a
// non-nil value. Under aggregate reduction such a measure is incomplete: it can
// contribute a reduced value, but ApplyObservations always marks the snapshot
// incomplete and measureIsIncomplete treats it as unusable for taint.
func r4UnknownInputMeasure(value string) metering.Measure {
	measure := r4InputMeasure(value)
	measure.Quality = metering.QualityUnknown
	return measure
}

func r4ProviderCharge(item, amount string) metering.ReportedCharge {
	return metering.ReportedCharge{
		ChargeItemID: item,
		Amount:       r4Decimal(amount),
		Currency:     "USD",
		Kind:         metering.ChargeKindAggregate,
		Payer:        metering.PaymentParty{Kind: metering.PaymentPartyOperator},
	}
}

var (
	r4InputKey  = metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentInputToken, Unit: metering.UnitToken}
	r4OutputKey = metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken}
	r4ImageKey  = metering.ComponentKey{Direction: metering.DirectionOutput, Component: "image", Unit: metering.UnitImage}
)

func r4OpenStore(t *testing.T, path string) *journalstore.DurableStore {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", durableCheckpointSQLiteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journalstore.NewDurableStore(context.Background(), bunDB, journalstore.DurableConfig{StoreID: r4StoreID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

type r4Reduced struct {
	input, output, image string
	charges              int
	complete             bool
}

func r4SnapshotValue(snapshot aggregate.SnapshotV2, key metering.ComponentKey) string {
	normalized, err := key.Normalize()
	if err != nil {
		return ""
	}
	for _, measure := range snapshot.Measures {
		if measure.Key.Equal(normalized) {
			return measure.Value.CanonicalString()
		}
	}
	return ""
}

func r4Reduce(t *testing.T, observations []metering.Observation) r4Reduced {
	t.Helper()
	snapshot, err := aggregate.ApplyObservations(observations)
	if err != nil {
		t.Fatalf("reduce %d observations: %v", len(observations), err)
	}
	return r4Reduced{
		input:    r4SnapshotValue(snapshot, r4InputKey),
		output:   r4SnapshotValue(snapshot, r4OutputKey),
		image:    r4SnapshotValue(snapshot, r4ImageKey),
		charges:  len(snapshot.Charges),
		complete: snapshot.Complete,
	}
}

func r4DurableObservations(t *testing.T, store *journalstore.DurableStore) []metering.Observation {
	t.Helper()
	page, err := store.ListObservations(context.Background(), journalstore.ObservationQuery{
		StoreID: r4StoreID, SubjectKind: metering.SubjectBLeg, SubjectID: "b-r4", Limit: 100,
	})
	if err != nil {
		t.Fatalf("list durable observations: %v", err)
	}
	return page.Observations
}

func r4Attempt(source *refinement41ObservationSource, sink metering.ObservationSink) *attemptSession {
	return &attemptSession{
		inner: source, observationSink: sink,
		now: func() time.Time { return time.Unix(1_700_000_100, 0).UTC() },
	}
}

func r4AssertReductionMatches(t *testing.T, original, durable []metering.Observation) {
	t.Helper()
	want := r4Reduce(t, original)
	got := r4Reduce(t, durable)
	if got != want {
		t.Fatalf("durable reduction drifted from original evidence\n original=%+v\n durable =%+v", want, got)
	}
}

// TestEconomicCheckpointSparseCumulativePreservesDisjointFieldsAfterRestart is
// the primary R4 counterexample: revision 1 carries only input, revision 2
// carries only output (plus a native media component). Both are queued before
// any flush. The durable reduction after a file-backed SQLite close/reopen must
// match the reduction over the original source evidence.
func TestEconomicCheckpointSparseCumulativePreservesDisjointFieldsAfterRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "r4-sparse.db")
	store := r4OpenStore(t, path)

	source := &refinement41ObservationSource{}
	attempt := r4Attempt(source, journalstore.NewObservationSink(store))
	rev1 := r4SparseObservation(1, r4InputMeasure("100"))
	rev2 := r4SparseObservation(2, r4OutputMeasure("20"), r4ImageMeasure("1"))
	source.queue(rev1, rev2)

	attempt.drainSidebandEvidence(ctx, recvTurnFacts{}, newResponsePipeline())
	if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("pre-terminal flush: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened := r4OpenStore(t, path)
	durable := r4DurableObservations(t, reopened)
	r4AssertReductionMatches(t, []metering.Observation{rev1, rev2}, durable)

	got := r4Reduce(t, durable)
	if got.input != "100/0" {
		t.Fatalf("durable input = %q, want preserved 100/0", got.input)
	}
	if got.output != "20/0" {
		t.Fatalf("durable output = %q, want 20/0", got.output)
	}
	if got.image != "1/0" {
		t.Fatalf("durable native media = %q, want 1/0", got.image)
	}
}

// TestEconomicCheckpointLateSparseRevisionSurvivesDurableHead covers the
// durable-head audit: a newer sparse revision is flushed first, then an older
// sparse revision carrying a different field arrives. The head fence must not
// discard the older revision's still-effective field.
func TestEconomicCheckpointLateSparseRevisionSurvivesDurableHead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "r4-reorder.db")
	store := r4OpenStore(t, path)
	attempt := r4Attempt(&refinement41ObservationSource{}, journalstore.NewObservationSink(store))

	rev1 := r4SparseObservation(1, r4InputMeasure("100"))
	rev2 := r4SparseObservation(2, r4OutputMeasure("20"))

	attempt.rememberEconomicObservationOnce(rev2)
	if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("flush newer revision: %v", err)
	}
	attempt.rememberEconomicObservationOnce(rev1)
	if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("flush reordered sparse revision: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened := r4OpenStore(t, path)
	durable := r4DurableObservations(t, reopened)
	r4AssertReductionMatches(t, []metering.Observation{rev1, rev2}, durable)
	if got := r4Reduce(t, durable); got.input != "100/0" || got.output != "20/0" {
		t.Fatalf("reordered durable reduction = %+v, want input 100/0 output 20/0", got)
	}
}

// TestEconomicCheckpointExplicitZeroOverridesCoalescedCumulative proves an
// explicitly present zero is authoritative and must override a prior value.
func TestEconomicCheckpointExplicitZeroOverridesCoalescedCumulative(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "r4-zero.db")
	store := r4OpenStore(t, path)
	source := &refinement41ObservationSource{}
	attempt := r4Attempt(source, journalstore.NewObservationSink(store))

	rev1 := r4SparseObservation(1, r4InputMeasure("100"))
	rev2 := r4SparseObservation(2, r4InputMeasure("0"))
	source.queue(rev1, rev2)
	attempt.drainSidebandEvidence(ctx, recvTurnFacts{}, newResponsePipeline())
	if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened := r4OpenStore(t, path)
	durable := r4DurableObservations(t, reopened)
	r4AssertReductionMatches(t, []metering.Observation{rev1, rev2}, durable)
	if got := r4Reduce(t, durable); got.input != "0/0" {
		t.Fatalf("explicit zero durable input = %q, want 0/0", got.input)
	}
}

// TestEconomicCheckpointUnavailableCumulativeFieldIsNotErased proves an
// explicitly unavailable field is durable evidence: coalescing must not silently
// drop it, and an older known value must not vanish.
func TestEconomicCheckpointUnavailableCumulativeFieldIsNotErased(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "r4-unavailable.db")
	store := r4OpenStore(t, path)
	source := &refinement41ObservationSource{}
	attempt := r4Attempt(source, journalstore.NewObservationSink(store))

	rev1 := r4SparseObservation(1, r4InputMeasure("100"))
	rev2 := r4SparseObservation(2, r4UnavailableInputMeasure())
	source.queue(rev1, rev2)
	attempt.drainSidebandEvidence(ctx, recvTurnFacts{}, newResponsePipeline())
	if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened := r4OpenStore(t, path)
	durable := r4DurableObservations(t, reopened)
	r4AssertReductionMatches(t, []metering.Observation{rev1, rev2}, durable)
	if got := r4Reduce(t, durable); got.input != "100/0" || got.complete {
		t.Fatalf("unavailable durable reduction = %+v, want input 100/0 incomplete", got)
	}
}

// TestEconomicCheckpointSparseCumulativePreservesProviderMoneyComponents proves
// provider-money charges reported by an earlier sparse cumulative revision are
// durable. Cumulative charges reduce one entry per revision, so coalescing must
// never collapse them.
func TestEconomicCheckpointSparseCumulativePreservesProviderMoneyComponents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "r4-money.db")
	store := r4OpenStore(t, path)
	source := &refinement41ObservationSource{}
	attempt := r4Attempt(source, journalstore.NewObservationSink(store))

	rev1 := r4SparseObservation(1, r4InputMeasure("100"))
	rev1.Charges = []metering.ReportedCharge{r4ProviderCharge("r4-fee", "5")}
	rev2 := r4SparseObservation(2, r4OutputMeasure("20"))
	rev2.Charges = []metering.ReportedCharge{r4ProviderCharge("r4-fee", "7")}
	source.queue(rev1, rev2)
	attempt.drainSidebandEvidence(ctx, recvTurnFacts{}, newResponsePipeline())
	if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened := r4OpenStore(t, path)
	durable := r4DurableObservations(t, reopened)
	r4AssertReductionMatches(t, []metering.Observation{rev1, rev2}, durable)
	got := r4Reduce(t, durable)
	if got.input != "100/0" || got.output != "20/0" {
		t.Fatalf("provider-money durable measures = %+v, want input 100/0 output 20/0", got)
	}
	if got.charges != 2 {
		t.Fatalf("provider-money durable charges = %d, want both revisions retained", got.charges)
	}
}

// TestEconomicCheckpointFullSnapshotSubsumesDisjointPendingRevisions verifies
// the safe coalescing path: a later complete cumulative snapshot removes the
// older disjoint sparse revisions it provably covers.
func TestEconomicCheckpointFullSnapshotSubsumesDisjointPendingRevisions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "r4-subsumed.db")
	store := r4OpenStore(t, path)
	source := &refinement41ObservationSource{}
	attempt := r4Attempt(source, journalstore.NewObservationSink(store))

	rev1 := r4SparseObservation(1, r4InputMeasure("100"))
	rev2 := r4SparseObservation(2, r4OutputMeasure("20"))
	rev3 := r4SparseObservation(3, r4InputMeasure("120"), r4OutputMeasure("25"))
	source.queue(rev1, rev2, rev3)
	attempt.drainSidebandEvidence(ctx, recvTurnFacts{}, newResponsePipeline())
	if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened := r4OpenStore(t, path)
	durable := r4DurableObservations(t, reopened)
	r4AssertReductionMatches(t, []metering.Observation{rev1, rev2, rev3}, durable)
	if got := r4Reduce(t, durable); got.input != "120/0" || got.output != "25/0" {
		t.Fatalf("subsumed durable reduction = %+v, want input 120/0 output 25/0", got)
	}
	if len(durable) > 1 {
		t.Fatalf("fully covered pending revisions = %d durable, want the subsuming revision only", len(durable))
	}
}

// r4ReopenedReduction closes the file-backed store, reopens the same SQLite
// file, and reduces exactly the durable observations. Expected values in the
// tests below are declared as literals rather than derived from the checkpoint
// admission/coalescing code under test.
func r4ReopenedReduction(t *testing.T, path string, store *journalstore.DurableStore) r4Reduced {
	t.Helper()
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened := r4OpenStore(t, path)
	return r4Reduce(t, r4DurableObservations(t, reopened))
}

// r4ReopenedObservations closes the file-backed store and reopens the same
// SQLite file so a regression can compare the durable reduction with the
// reduction of the original immutable source observations.
func r4ReopenedObservations(t *testing.T, path string, store *journalstore.DurableStore) []metering.Observation {
	t.Helper()
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened := r4OpenStore(t, path)
	return r4DurableObservations(t, reopened)
}

// TestEconomicCheckpointOrderingAndDiagnosticsPreserveSourceReduction is the
// permanent, independent expected-value regression for the R4 adversarial
// counterexamples. Each scenario drives the exact file-backed flush/close/reopen
// path and asserts the expected reduction as an explicit literal, so a future
// change cannot satisfy the test by generating its expected value from the
// checkpoint state it is validating.
func TestEconomicCheckpointOrderingAndDiagnosticsPreserveSourceReduction(t *testing.T) {
	t.Parallel()

	t.Run("pending_reordered", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		path := filepath.Join(t.TempDir(), "r4-pending-reordered.db")
		store := r4OpenStore(t, path)
		attempt := r4Attempt(&refinement41ObservationSource{}, journalstore.NewObservationSink(store))

		rev1 := r4SparseObservation(1, r4InputMeasure("100"))
		rev2 := r4SparseObservation(2, r4InputMeasure("200"))
		attempt.queueEconomicCheckpoint(rev2)
		attempt.queueEconomicCheckpoint(rev1)
		if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
			t.Fatalf("flush: %v", err)
		}

		got := r4ReopenedReduction(t, path, store)
		if got != (r4Reduced{input: "200/0", complete: true}) {
			t.Fatalf("durable reduction = %+v, want input 200/0 complete", got)
		}
	})

	t.Run("durable_intermediate", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		path := filepath.Join(t.TempDir(), "r4-durable-intermediate.db")
		store := r4OpenStore(t, path)
		attempt := r4Attempt(&refinement41ObservationSource{}, journalstore.NewObservationSink(store))

		rev1 := r4SparseObservation(1, r4InputMeasure("100"))
		rev2 := r4SparseObservation(2, r4InputMeasure("200"))
		rev3 := r4SparseObservation(3, r4OutputMeasure("20"))
		attempt.queueEconomicCheckpoint(rev1)
		if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
			t.Fatalf("flush rev1: %v", err)
		}
		attempt.queueEconomicCheckpoint(rev3)
		if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
			t.Fatalf("flush rev3: %v", err)
		}
		attempt.queueEconomicCheckpoint(rev2)
		if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
			t.Fatalf("flush rev2: %v", err)
		}

		got := r4ReopenedReduction(t, path, store)
		if got != (r4Reduced{input: "200/0", output: "20/0", complete: true}) {
			t.Fatalf("durable reduction = %+v, want input 200/0 output 20/0 complete", got)
		}
	})

	t.Run("unavailable_then_known", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		path := filepath.Join(t.TempDir(), "r4-unavailable-then-known.db")
		store := r4OpenStore(t, path)
		attempt := r4Attempt(&refinement41ObservationSource{}, journalstore.NewObservationSink(store))

		rev1 := r4SparseObservation(1, r4UnavailableInputMeasure())
		rev2 := r4SparseObservation(2, r4InputMeasure("200"))
		attempt.queueEconomicCheckpoint(rev1)
		attempt.queueEconomicCheckpoint(rev2)
		if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
			t.Fatalf("flush: %v", err)
		}

		got := r4ReopenedReduction(t, path, store)
		if got != (r4Reduced{input: "200/0", complete: false}) {
			t.Fatalf("durable reduction = %+v, want input 200/0 incomplete", got)
		}
	})
}

// TestEconomicCheckpointProviderMoneyRetainsExactAmountCurrencyAndRevision is an
// independent expected-value check: cumulative provider money from two sparse
// revisions must survive the pre-terminal checkpoint as two exact charges with
// unchanged amount, currency, charge-item identity, source observation id, and
// source revision.
func TestEconomicCheckpointProviderMoneyRetainsExactAmountCurrencyAndRevision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "r4-money-exact.db")
	store := r4OpenStore(t, path)
	source := &refinement41ObservationSource{}
	attempt := r4Attempt(source, journalstore.NewObservationSink(store))

	rev1 := r4SparseObservation(1, r4InputMeasure("100"))
	rev1.Charges = []metering.ReportedCharge{r4ProviderCharge("r4-fee", "5")}
	rev2 := r4SparseObservation(2, r4OutputMeasure("20"))
	rev2.Charges = []metering.ReportedCharge{r4ProviderCharge("r4-fee", "7")}
	source.queue(rev1, rev2)
	attempt.drainSidebandEvidence(ctx, recvTurnFacts{}, newResponsePipeline())
	if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened := r4OpenStore(t, path)
	snapshot, err := aggregate.ApplyObservations(r4DurableObservations(t, reopened))
	if err != nil {
		t.Fatalf("reduce durable: %v", err)
	}
	if len(snapshot.Charges) != 2 {
		t.Fatalf("durable charges = %d, want 2 (one per sparse revision)", len(snapshot.Charges))
	}
	wantAmounts := map[uint64]string{1: "5/0", 2: "7/0"}
	for _, charge := range snapshot.Charges {
		if charge.Charge.ChargeItemID != "r4-fee" {
			t.Fatalf("charge item = %q, want r4-fee", charge.Charge.ChargeItemID)
		}
		if charge.Charge.Currency != "USD" {
			t.Fatalf("charge revision %d currency = %q, want USD", charge.Revision, charge.Charge.Currency)
		}
		if charge.ObservationID != "obs-r4-sparse" {
			t.Fatalf("charge revision %d observation id = %q, want obs-r4-sparse", charge.Revision, charge.ObservationID)
		}
		want, ok := wantAmounts[charge.Revision]
		if !ok {
			t.Fatalf("unexpected durable charge revision %d", charge.Revision)
		}
		if charge.Charge.Amount == nil || charge.Charge.Amount.CanonicalString() != want {
			t.Fatalf("charge revision %d amount = %v, want %s", charge.Revision, charge.Charge.Amount, want)
		}
	}
}

// TestEconomicCheckpointSparseCumulativeAcrossFlushBoundaries exercises several
// flush boundaries with advancing sparse revisions.
func TestEconomicCheckpointSparseCumulativeAcrossFlushBoundaries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "r4-boundaries.db")
	store := r4OpenStore(t, path)
	attempt := r4Attempt(&refinement41ObservationSource{}, journalstore.NewObservationSink(store))

	rev1 := r4SparseObservation(1, r4InputMeasure("100"))
	rev2 := r4SparseObservation(2, r4OutputMeasure("20"))
	rev3 := r4SparseObservation(3, r4InputMeasure("150"))
	rev4 := r4SparseObservation(4, r4ImageMeasure("2"))

	attempt.rememberEconomicObservationOnce(rev1)
	if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("flush boundary 1: %v", err)
	}
	attempt.rememberEconomicObservationOnce(rev2)
	if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("flush boundary 2: %v", err)
	}
	attempt.rememberEconomicObservationOnce(rev3)
	attempt.rememberEconomicObservationOnce(rev4)
	if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("flush boundary 3: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened := r4OpenStore(t, path)
	durable := r4DurableObservations(t, reopened)
	r4AssertReductionMatches(t, []metering.Observation{rev1, rev2, rev3, rev4}, durable)
	got := r4Reduce(t, durable)
	if got.input != "150/0" || got.output != "20/0" || got.image != "2/0" {
		t.Fatalf("multi-boundary durable reduction = %+v, want input 150/0 output 20/0 image 2/0", got)
	}
}

// TestEconomicCheckpointUnknownQualityDiagnosticPreserved is the permanent R4
// regression for reviewer cases unknown_then_known and late_unknown. A
// QualityUnknown measure with a non-nil value is incomplete under aggregate
// reduction: it contributes a reduced value but always marks the snapshot
// incomplete, and measureIsIncomplete treats it as unusable for taint. The
// checkpoint reducer must therefore keep it as an unusable diagnostic instead
// of a usable field, so coalescing cannot drop it and a later known value
// cannot erase the incompleteness. Each case reopens the file-backed SQLite
// store and compares the durable reduction with the reduction of the original
// immutable observations.
func TestEconomicCheckpointUnknownQualityDiagnosticPreserved(t *testing.T) {
	t.Parallel()

	t.Run("unknown_then_known", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		path := filepath.Join(t.TempDir(), "r4-unknown-then-known.db")
		store := r4OpenStore(t, path)
		attempt := r4Attempt(&refinement41ObservationSource{}, journalstore.NewObservationSink(store))

		rev1 := r4SparseObservation(1, r4UnknownInputMeasure("100"))
		rev2 := r4SparseObservation(2, r4InputMeasure("200"))
		attempt.queueEconomicCheckpoint(rev1)
		attempt.queueEconomicCheckpoint(rev2)
		if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
			t.Fatalf("flush: %v", err)
		}

		durable := r4ReopenedObservations(t, path, store)
		r4AssertReductionMatches(t, []metering.Observation{rev1, rev2}, durable)
		if got := r4Reduce(t, durable); got != (r4Reduced{input: "200/0", complete: false}) {
			t.Fatalf("durable reduction = %+v, want input 200/0 incomplete", got)
		}
	})

	t.Run("late_unknown", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		path := filepath.Join(t.TempDir(), "r4-late-unknown.db")
		store := r4OpenStore(t, path)
		attempt := r4Attempt(&refinement41ObservationSource{}, journalstore.NewObservationSink(store))

		rev1 := r4SparseObservation(1, r4UnknownInputMeasure("100"))
		rev2 := r4SparseObservation(2, r4InputMeasure("200"))
		attempt.queueEconomicCheckpoint(rev2)
		if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
			t.Fatalf("flush known: %v", err)
		}
		attempt.queueEconomicCheckpoint(rev1)
		if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
			t.Fatalf("flush reordered unknown: %v", err)
		}

		durable := r4ReopenedObservations(t, path, store)
		r4AssertReductionMatches(t, []metering.Observation{rev1, rev2}, durable)
		if got := r4Reduce(t, durable); got != (r4Reduced{input: "200/0", complete: false}) {
			t.Fatalf("durable reduction = %+v, want input 200/0 incomplete", got)
		}
	})
}

// TestEconomicCheckpointUnknownValueAfterUnavailableRetainsNumericContribution
// is the permanent R4 regression for reviewer counterexample
// unknown_value_after_unavailable. Revision 3 marks the input field unavailable
// and is flushed before the reordered revision 2 arrives. Revision 2 is
// QualityUnknown but still carries the numeric value 200, which
// aggregate.ApplyObservations applies while keeping the snapshot incomplete. An
// unavailable diagnostic must not stand in for that numeric contribution: a
// durable head that treats any non-reducible measure as covered by a prior
// diagnostic drops revision 2 and leaves input 100. The durable reduction after
// a file-backed close/reopen must be input 200/0 with Complete=false, matching
// the reduction over the original immutable evidence.
func TestEconomicCheckpointUnknownValueAfterUnavailableRetainsNumericContribution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "r4-unknown-value-after-unavailable.db")
	store := r4OpenStore(t, path)
	attempt := r4Attempt(&refinement41ObservationSource{}, journalstore.NewObservationSink(store))

	rev1 := r4SparseObservation(1, r4InputMeasure("100"))
	rev3 := r4SparseObservation(3, r4UnavailableInputMeasure())
	rev2 := r4SparseObservation(2, r4UnknownInputMeasure("200"))

	attempt.queueEconomicCheckpoint(rev1)
	if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("flush known baseline: %v", err)
	}
	attempt.queueEconomicCheckpoint(rev3)
	if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("flush unavailable diagnostic: %v", err)
	}
	attempt.queueEconomicCheckpoint(rev2)
	if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("flush reordered unknown-value revision: %v", err)
	}

	durable := r4ReopenedObservations(t, path, store)
	r4AssertReductionMatches(t, []metering.Observation{rev1, rev2, rev3}, durable)
	if got := r4Reduce(t, durable); got != (r4Reduced{input: "200/0", complete: false}) {
		t.Fatalf("durable reduction = %+v, want input 200/0 incomplete", got)
	}
}

// TestEconomicCheckpointPresenceMatrixRegression keeps the present-field
// semantics that the unknown-quality repair must not regress: explicit zero is
// an authoritative present value, unavailable and absent fields stay
// incomplete diagnostics, and a reordered sparse known revision is admissible.
func TestEconomicCheckpointPresenceMatrixRegression(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		first     metering.Observation
		second    metering.Observation
		reordered bool
		want      r4Reduced
	}{
		{
			name:   "explicit_zero_overrides",
			first:  r4SparseObservation(1, r4InputMeasure("100")),
			second: r4SparseObservation(2, r4InputMeasure("0")),
			want:   r4Reduced{input: "0/0", complete: true},
		},
		{
			name:   "unavailable_then_known",
			first:  r4SparseObservation(1, r4UnavailableInputMeasure()),
			second: r4SparseObservation(2, r4InputMeasure("200")),
			want:   r4Reduced{input: "200/0", complete: false},
		},
		{
			name:   "absent_field_is_not_erased",
			first:  r4SparseObservation(1, r4InputMeasure("100")),
			second: r4SparseObservation(2, r4OutputMeasure("20")),
			want:   r4Reduced{input: "100/0", output: "20/0", complete: true},
		},
		{
			name:      "reordered_sparse_known",
			first:     r4SparseObservation(1, r4InputMeasure("100")),
			second:    r4SparseObservation(2, r4InputMeasure("200")),
			reordered: true,
			want:      r4Reduced{input: "200/0", complete: true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "r4-presence-matrix.db")
			store := r4OpenStore(t, path)
			attempt := r4Attempt(&refinement41ObservationSource{}, journalstore.NewObservationSink(store))

			if tc.reordered {
				attempt.queueEconomicCheckpoint(tc.second)
				attempt.queueEconomicCheckpoint(tc.first)
			} else {
				attempt.queueEconomicCheckpoint(tc.first)
				attempt.queueEconomicCheckpoint(tc.second)
			}
			if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
				t.Fatalf("flush: %v", err)
			}

			durable := r4ReopenedObservations(t, path, store)
			r4AssertReductionMatches(t, []metering.Observation{tc.first, tc.second}, durable)
			if got := r4Reduce(t, durable); got != tc.want {
				t.Fatalf("durable reduction = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// r4AssertCheckpointOrderBounded asserts the order indexes never outgrow their
// bounded maps and remain one-to-one with the entries they order. The invariant
// is what makes flush order deterministic without letting a coalescing stream
// grow receive-path bookkeeping.
func r4AssertCheckpointOrderBounded(t *testing.T, attempt *attemptSession) {
	t.Helper()
	attempt.checkpointMu.Lock()
	defer attempt.checkpointMu.Unlock()
	if len(attempt.checkpointPending) > maxPreTerminalEconomicCheckpointPending {
		t.Fatalf("pending map = %d, want <= %d", len(attempt.checkpointPending), maxPreTerminalEconomicCheckpointPending)
	}
	if len(attempt.checkpointDeferred) > maxPreTerminalEconomicCheckpointDeferred {
		t.Fatalf("deferred map = %d, want <= %d", len(attempt.checkpointDeferred), maxPreTerminalEconomicCheckpointDeferred)
	}
	if len(attempt.checkpointOrder) != len(attempt.checkpointPending) {
		t.Fatalf("pending order index = %d, want exactly pending map size %d", len(attempt.checkpointOrder), len(attempt.checkpointPending))
	}
	if len(attempt.checkpointDeferredOrder) != len(attempt.checkpointDeferred) {
		t.Fatalf("deferred order index = %d, want exactly deferred map size %d", len(attempt.checkpointDeferredOrder), len(attempt.checkpointDeferred))
	}
}

// TestEconomicCheckpointOrderBookkeepingStaysBounded is the permanent R4
// regression for reviewer TestReviewR4OrderBound. A coalesced revision replaces
// a bounded map entry; its order slot must be reused rather than appended, or a
// long stream of coalesced revisions grows the append-only order slices without
// bound even though the maps stay at capacity. The invariant must hold for the
// pending queue, the deferred retry queue, and the sparse/repaired coalescing
// cycles that momentarily use revision-qualified storage keys.
func TestEconomicCheckpointOrderBookkeepingStaysBounded(t *testing.T) {
	t.Parallel()

	t.Run("coalesced_pending", func(t *testing.T) {
		t.Parallel()
		sink := &refinement41ObservationSink{}
		attempt := r4Attempt(&refinement41ObservationSource{}, sink)
		for i := uint64(1); i <= 10000; i++ {
			attempt.queueEconomicCheckpoint(r4SparseObservation(i, r4InputMeasure("1")))
		}
		r4AssertCheckpointOrderBounded(t, attempt)
		if err := attempt.flushEconomicCheckpoints(context.Background(), true); err != nil {
			t.Fatalf("flush coalesced revisions: %v", err)
		}
		got := sink.observationsSnapshot()
		if len(got) != 1 || got[0].Revision != 10000 {
			t.Fatalf("coalesced durable writes = %d (want the newest revision only)", len(got))
		}
	})

	t.Run("coalesced_deferred", func(t *testing.T) {
		t.Parallel()
		sink := &refinement41ObservationSink{}
		attempt := r4Attempt(&refinement41ObservationSource{}, sink)
		// Fill the pending queue with distinct sources so the coalescing target
		// is admitted to the bounded deferred retry queue instead.
		for i := uint64(1); i <= maxPreTerminalEconomicCheckpointPending; i++ {
			filler := r4SparseObservation(i, r4InputMeasure("1"))
			filler.SourceEventKey = "r4-filler-" + strconv.FormatUint(i, 10)
			attempt.queueEconomicCheckpoint(filler)
		}
		for i := uint64(1); i <= 10000; i++ {
			target := r4SparseObservation(i, r4InputMeasure("2"))
			target.SourceEventKey = "r4-target"
			attempt.queueEconomicCheckpoint(target)
		}
		r4AssertCheckpointOrderBounded(t, attempt)
	})

	t.Run("sparse_coalescing_cycles", func(t *testing.T) {
		t.Parallel()
		sink := &refinement41ObservationSink{}
		attempt := r4Attempt(&refinement41ObservationSource{}, sink)
		revision := uint64(1)
		// Each cycle momentarily stores a sparse input revision and a disjoint
		// sparse output revision under revision-qualified keys before a full
		// snapshot coalesces them all. Stale qualified order slots must not
		// accumulate across cycles.
		for range 3000 {
			input := r4SparseObservation(revision, r4InputMeasure("1"))
			revision++
			output := r4SparseObservation(revision, r4OutputMeasure("2"))
			revision++
			full := r4SparseObservation(revision, r4InputMeasure("3"), r4OutputMeasure("4"))
			revision++
			attempt.queueEconomicCheckpoint(input)
			attempt.queueEconomicCheckpoint(output)
			attempt.queueEconomicCheckpoint(full)
		}
		r4AssertCheckpointOrderBounded(t, attempt)
		if err := attempt.flushEconomicCheckpoints(context.Background(), true); err != nil {
			t.Fatalf("flush sparse coalescing cycles: %v", err)
		}
		got := sink.observationsSnapshot()
		if len(got) != 1 || got[0].Revision != revision-1 {
			t.Fatalf("sparse cycle durable writes = %d (want the final full revision only)", len(got))
		}
	})
}

// TestEconomicCheckpointOrderBookkeepingBoundedOnFailingSink proves the order
// bookkeeping bound holds while the sink is unavailable and that a later retry
// still reduces the coalesced source to its newest value. A failed flush must
// not be the only thing that compacts the order indexes; otherwise the receive
// path grows order state for as long as the journal stays down.
func TestEconomicCheckpointOrderBookkeepingBoundedOnFailingSink(t *testing.T) {
	t.Parallel()
	sink := &refinement41ObservationSink{err: errors.New("journal unavailable")}
	attempt := r4Attempt(&refinement41ObservationSource{}, sink)
	for i := uint64(1); i <= 10000; i++ {
		attempt.queueEconomicCheckpoint(r4SparseObservation(i, r4InputMeasure("1")))
		if i%preTerminalEconomicCheckpointBatch == 0 {
			if err := attempt.flushEconomicCheckpoints(context.Background(), true); err == nil {
				t.Fatalf("flush with unavailable sink unexpectedly succeeded at revision %d", i)
			}
			r4AssertCheckpointOrderBounded(t, attempt)
		}
	}
	r4AssertCheckpointOrderBounded(t, attempt)

	sink.mu.Lock()
	sink.err = nil
	sink.mu.Unlock()
	if err := attempt.flushEconomicCheckpoints(context.Background(), true); err != nil {
		t.Fatalf("recovered flush: %v", err)
	}
	got := sink.observationsSnapshot()
	if len(got) != 1 || got[0].Revision != 10000 {
		t.Fatalf("recovered durable writes = %d (want the newest coalesced revision only)", len(got))
	}
	if got[0].Measures[0].Value == nil || got[0].Measures[0].Value.CanonicalString() != "1/0" {
		t.Fatalf("recovered durable value = %v, want 1/0", got[0].Measures[0].Value)
	}
}
