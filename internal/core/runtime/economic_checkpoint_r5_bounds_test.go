package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	_ "modernc.org/sqlite"
)

// R5-RUNTIME-B adversarial repair: the attempt-owned durable-head cache retains
// one entry per still-effective component key and supersession ref so a
// reordered sparse revision is never mistaken for redundant. Those maps were
// bounded only in the number of heads, so a provider reporting a novel
// component or ref key on every successful flush could grow per-head and total
// metadata for the entire attempt lifetime. The budgets below must cap both per
// head and in aggregate, must never invent coverage for a refused key/ref, and
// must keep the healthy >256-revision provider progression exact in the durable
// SQLite reduction.

const (
	r5bStoreID = "store-r5b"
	// r5bCheckpointCapacity mirrors the accepted pending+deferred queue bound.
	r5bCheckpointCapacity = maxPreTerminalEconomicCheckpointPending + maxPreTerminalEconomicCheckpointDeferred
)

func r5bObservationIdentity() coremetering.ObservationIdentity {
	now := time.Unix(1_700_100_000, 0).UTC()
	return coremetering.ObservationIdentity{
		StoreID: r5bStoreID, RequestID: "req-r5b", CallID: "call-r5b", BillingCallID: "call-r5b",
		ALegID: "a-r5b", BLegID: "b-r5b", AttemptID: "attempt-r5b", AttemptSeq: 1,
		ObservedAt: now, ReceivedAt: now,
	}
}

func r5bCumulativeObservation(sourceKey string, revision uint64, measures []metering.Measure, supersedes []metering.ObservationRef) metering.Observation {
	now := time.Unix(1_700_100_000+int64(revision), 0).UTC()
	return metering.Observation{
		Version:        metering.ObservationVersionV2,
		ID:             "obs-r5b-" + sourceKey + "-" + strconv.FormatUint(revision, 10),
		SourceEventKey: sourceKey, Revision: revision, StreamID: "stream-" + sourceKey, Sequence: revision,
		Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator,
		Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: r5bStoreID, ALegID: "a-r5b", BillingCallID: "call-r5b", BLegID: "b-r5b",
		},
		Correlation: metering.CorrelationV2{
			StoreID: r5bStoreID, CallID: "call-r5b", BillingCallID: "call-r5b", ALegID: "a-r5b", BLegID: "b-r5b",
		},
		Semantics: metering.SemanticsCumulative, ObservedAt: now, ReceivedAt: now, MappingRef: "r5b",
		Measures: measures, Supersedes: supersedes,
	}
}

// r5bNovelMeasure builds a valid, distinct component key for each index so a
// stream of revisions can introduce an unbounded number of novel keys.
func r5bNovelMeasure(index int, value string) metering.Measure {
	decimal := metering.Decimal{Coefficient: value, Scale: 0}
	return metering.Measure{
		Key: metering.ComponentKey{
			Direction: metering.DirectionOutput, Component: fmt.Sprintf("r5b-comp-%d", index),
			Unit: metering.UnitToken, SchemaID: "r5b.schema",
		},
		Value: &decimal, Quality: metering.QualityObserved,
	}
}

// r5bWideMeasure builds a valid component key whose canonical form is large:
// every allowed dimension is populated with the maximum-length value, so a
// stream of novel wide keys must hit the byte budget before the cardinality
// budget.
func r5bWideMeasure(index int) metering.Measure {
	dimensions := make([]metering.Dimension, 0, metering.MaxDimensions)
	for d := range metering.MaxDimensions {
		dimensions = append(dimensions, metering.Dimension{
			Name:  fmt.Sprintf("dim-%02d-%d", d, index),
			Value: strings.Repeat("v", metering.MaxDimensionValueBytes),
		})
	}
	decimal := metering.Decimal{Coefficient: "1", Scale: 0}
	return metering.Measure{
		Key: metering.ComponentKey{
			Direction: metering.DirectionOutput, Component: fmt.Sprintf("r5b-wide-%d", index),
			Unit: metering.UnitToken, SchemaID: "r5b.schema", Dimensions: dimensions,
		},
		Value: &decimal, Quality: metering.QualityObserved,
	}
}

func r5bLongSupersessionRef(index, width int) metering.ObservationRef {
	return metering.ObservationRef{
		StoreID:       r5bStoreID,
		ObservationID: fmt.Sprintf("%0*d", width, index+1),
		Revision:      1,
		PayloadHash:   strings.Repeat("a", width),
	}
}

func r5bAttempt(sink metering.ObservationSink) *attemptSession {
	return &attemptSession{
		observationSink: sink,
		now:             func() time.Time { return time.Unix(1_700_100_500, 0).UTC() },
	}
}

func r5bOpenStore(t *testing.T, path string) *journalstore.DurableStore {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", durableCheckpointSQLiteDSN(path))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatalf("new bun db: %v", err)
	}
	store, err := journalstore.NewDurableStore(context.Background(), bunDB, journalstore.DurableConfig{StoreID: r5bStoreID})
	if err != nil {
		t.Fatalf("new durable store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func r5bDurableObservations(t *testing.T, store *journalstore.DurableStore) []metering.Observation {
	t.Helper()
	page, err := store.ListObservations(context.Background(), journalstore.ObservationQuery{
		StoreID: r5bStoreID, SubjectKind: metering.SubjectBLeg, SubjectID: "b-r5b", Limit: 500,
	})
	if err != nil {
		t.Fatalf("list durable observations: %v", err)
	}
	return page.Observations
}

func r5bMediaKey() metering.ComponentKey {
	return metering.ComponentKey{
		Direction: metering.DirectionInput, Component: metering.ComponentImage,
		Unit: metering.UnitImage, SchemaID: "r5b.provider.schema",
	}
}

func r5bProviderMediaMoneyDraft(key string, count uint64) coremetering.ProviderEvidenceDraft {
	money := metering.Decimal{Coefficient: strconv.FormatUint(count*100, 10), Scale: 0}
	quantity := metering.Decimal{Coefficient: strconv.FormatUint(count, 10), Scale: 0}
	return coremetering.ProviderEvidenceDraft{
		SourceEventKey: key, StreamID: "provider.r5b.v2",
		Measures: []metering.Measure{{
			Key: r5bMediaKey(), Value: &quantity, Quality: metering.QualityObserved,
		}},
		Charges: []metering.ReportedCharge{{
			ChargeItemID: "charge:" + key, Amount: &money, Currency: "USD", Kind: metering.ChargeKindAggregate,
		}},
	}
}

type r5bReduced struct {
	media       string
	charges     int
	finalCharge string
	complete    bool
}

func r5bSnapshotValue(snapshot aggregate.SnapshotV2, key metering.ComponentKey) string {
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

func r5bReduce(t *testing.T, observations []metering.Observation) r5bReduced {
	t.Helper()
	snapshot, err := aggregate.ApplyObservations(observations)
	if err != nil {
		t.Fatalf("reduce %d observations: %v", len(observations), err)
	}
	reduced := r5bReduced{
		media:    r5bSnapshotValue(snapshot, r5bMediaKey()),
		charges:  len(snapshot.Charges),
		complete: snapshot.Complete,
	}
	var maxRevision uint64
	for _, charge := range snapshot.Charges {
		if charge.Charge.Amount == nil {
			continue
		}
		if charge.Revision >= maxRevision {
			maxRevision = charge.Revision
			reduced.finalCharge = charge.Charge.Amount.CanonicalString()
		}
	}
	return reduced
}

// r5bAssertSupersessionGraph proves the durable supersession refs form the exact
// immutable chain emitted by the provider buffer: each revision that carries a
// ref points at the immediately preceding revision of the same source.
func r5bAssertSupersessionGraph(t *testing.T, observations []metering.Observation) {
	t.Helper()
	bySource := make(map[string]map[uint64]string)
	for _, observation := range observations {
		if bySource[observation.SourceEventKey] == nil {
			bySource[observation.SourceEventKey] = make(map[uint64]string)
		}
		bySource[observation.SourceEventKey][observation.Revision] = observation.ID
	}
	for _, observation := range observations {
		for _, ref := range observation.Supersedes {
			if err := ref.Validate(); err != nil {
				t.Fatalf("supersession ref invalid: %v", err)
			}
			want := bySource[observation.SourceEventKey][observation.Revision-1]
			if ref.ObservationID != want {
				t.Fatalf("revision %d supersession ref %q does not resolve to previous revision %q", observation.Revision, ref.ObservationID, want)
			}
		}
	}
}

func r5bHeadAggregate(attempt *attemptSession) (heads, fields, refs, bytes int) {
	attempt.checkpointMu.Lock()
	defer attempt.checkpointMu.Unlock()
	heads = len(attempt.checkpointDurableHeads)
	for _, head := range attempt.checkpointDurableHeads {
		fields += len(head.fields)
		refs += len(head.supersedes)
		bytes += head.metadataBytes()
	}
	return heads, fields, refs, bytes
}

// TestEconomicCheckpointDurableHeadMetadataStaysBounded is the primary RED/GREEN
// bound probe. Many unique safe component keys and long supersession refs are
// flushed to one durable head, and many sources are flushed to the aggregate
// budget. Per-head and total retained entries/bytes must stay within their
// declared caps, a refused key must never be reported as covered, and admission
// of a reordered refused-key revision must fall back to the bounded queue rather
// than being silently ignored.
func TestEconomicCheckpointDurableHeadMetadataStaysBounded(t *testing.T) {
	t.Parallel()

	t.Run("per_head_fields_and_coverage_proof", func(t *testing.T) {
		t.Parallel()
		sink := &refinement41ObservationSink{}
		attempt := r5bAttempt(sink)
		const sourceKey = "r5b-head-fields"
		const revisions = 1000
		for i := range revisions {
			observation := r5bCumulativeObservation(sourceKey, uint64(i+1), []metering.Measure{r5bNovelMeasure(i, "1")}, nil)
			if admission := attempt.queueEconomicCheckpoint(observation); admission == economicCheckpointRejected {
				t.Fatalf("revision %d unexpectedly rejected", i+1)
			}
			if err := attempt.flushEconomicCheckpoints(context.Background(), true); err != nil {
				t.Fatalf("flush revision %d: %v", i+1, err)
			}
		}

		base := economicCheckpointPendingKey(r5bCumulativeObservation(sourceKey, revisions, nil, nil))
		attempt.checkpointMu.Lock()
		head := attempt.checkpointDurableHeads[base]
		attempt.checkpointMu.Unlock()

		if len(head.fields) > maxEconomicCheckpointHeadFields {
			t.Fatalf("per-head fields=%d, want <= %d", len(head.fields), maxEconomicCheckpointHeadFields)
		}
		if head.metadataBytes() > maxEconomicCheckpointHeadMetadataBytes {
			t.Fatalf("per-head metadata bytes=%d, want <= %d", head.metadataBytes(), maxEconomicCheckpointHeadMetadataBytes)
		}
		if !head.truncated {
			t.Fatalf("head fed %d novel keys was not marked coverage-truncated", revisions)
		}

		heads, fields, refs, bytes := r5bHeadAggregate(attempt)
		if heads > maxPreTerminalEconomicCheckpointDurableHeads {
			t.Fatalf("aggregate heads=%d, want <= %d", heads, maxPreTerminalEconomicCheckpointDurableHeads)
		}
		if fields > maxEconomicCheckpointTotalFields {
			t.Fatalf("aggregate fields=%d, want <= %d", fields, maxEconomicCheckpointTotalFields)
		}
		if refs > maxEconomicCheckpointTotalSupersedes {
			t.Fatalf("aggregate supersession refs=%d, want <= %d", refs, maxEconomicCheckpointTotalSupersedes)
		}
		if bytes > maxEconomicCheckpointTotalHeadMetadataBytes {
			t.Fatalf("aggregate metadata bytes=%d, want <= %d", bytes, maxEconomicCheckpointTotalHeadMetadataBytes)
		}

		// A key that could not be retained must never be reported as covered,
		// and the reordered revision must stay admissible to the bounded queue.
		refused := r5bCumulativeObservation(sourceKey, 50, []metering.Measure{r5bNovelMeasure(maxEconomicCheckpointHeadFields+40, "1")}, nil)
		if durableHeadCovers(head, refused) {
			t.Fatal("coverage-truncated durable head falsely covers a refused component key")
		}
		if admission := attempt.queueEconomicCheckpoint(refused); admission != economicCheckpointRetained {
			t.Fatalf("reordered refused-key revision admission=%v, want retained", admission)
		}
	})

	t.Run("per_head_component_key_bytes_stay_bounded", func(t *testing.T) {
		t.Parallel()
		sink := &refinement41ObservationSink{}
		attempt := r5bAttempt(sink)
		const sourceKey = "r5b-head-wide"
		const revisions = 200
		for i := range revisions {
			observation := r5bCumulativeObservation(sourceKey, uint64(i+1), []metering.Measure{r5bWideMeasure(i)}, nil)
			if admission := attempt.queueEconomicCheckpoint(observation); admission == economicCheckpointRejected {
				t.Fatalf("revision %d unexpectedly rejected", i+1)
			}
			if err := attempt.flushEconomicCheckpoints(context.Background(), true); err != nil {
				t.Fatalf("flush revision %d: %v", i+1, err)
			}
		}

		base := economicCheckpointPendingKey(r5bCumulativeObservation(sourceKey, revisions, nil, nil))
		attempt.checkpointMu.Lock()
		head := attempt.checkpointDurableHeads[base]
		attempt.checkpointMu.Unlock()

		if head.metadataBytes() > maxEconomicCheckpointHeadMetadataBytes {
			t.Fatalf("per-head metadata bytes=%d, want <= %d", head.metadataBytes(), maxEconomicCheckpointHeadMetadataBytes)
		}
		if !head.truncated {
			t.Fatalf("head fed %d wide keys was not marked coverage-truncated", revisions)
		}
		if len(head.fields) >= maxEconomicCheckpointHeadFields {
			t.Fatalf("wide-key byte budget did not bind before cardinality: fields=%d", len(head.fields))
		}

		_, _, _, bytes := r5bHeadAggregate(attempt)
		if bytes > maxEconomicCheckpointTotalHeadMetadataBytes {
			t.Fatalf("aggregate metadata bytes=%d, want <= %d", bytes, maxEconomicCheckpointTotalHeadMetadataBytes)
		}
	})

	// The supersession map can never be populated through the validated
	// cumulative path (validation forbids cumulative observations carrying
	// supersedes), but it is bounded defensively against a future writer.
	t.Run("supersession_ref_byte_budget", func(t *testing.T) {
		t.Parallel()
		limits := checkpointHeadLimits{
			fields:     maxEconomicCheckpointHeadFields,
			supersedes: maxEconomicCheckpointHeadSupersedes,
			bytes:      maxEconomicCheckpointHeadMetadataBytes,
		}
		var head checkpointDurableHead
		for i := range 200 {
			head = head.record(metering.Observation{
				Semantics:  metering.SemanticsReplacement,
				Revision:   uint64(i + 1),
				Supersedes: []metering.ObservationRef{r5bLongSupersessionRef(i, 900)},
			}, limits)
		}
		if len(head.supersedes) > maxEconomicCheckpointHeadSupersedes {
			t.Fatalf("per-head supersession refs=%d, want <= %d", len(head.supersedes), maxEconomicCheckpointHeadSupersedes)
		}
		if head.metadataBytes() > maxEconomicCheckpointHeadMetadataBytes {
			t.Fatalf("per-head metadata bytes=%d, want <= %d", head.metadataBytes(), maxEconomicCheckpointHeadMetadataBytes)
		}
		if !head.truncated {
			t.Fatal("long-ref head was not marked coverage-truncated")
		}
		if len(head.supersedes) >= maxEconomicCheckpointHeadSupersedes {
			t.Fatalf("supersession byte budget did not bind before cardinality: refs=%d", len(head.supersedes))
		}
	})

	t.Run("aggregate_metadata_stays_bounded", func(t *testing.T) {
		t.Parallel()
		sink := &refinement41ObservationSink{}
		attempt := r5bAttempt(sink)
		sources := maxEconomicCheckpointAggregateHeadSources + 1
		perSource := maxEconomicCheckpointHeadFields
		for i := range sources {
			measures := make([]metering.Measure, 0, perSource)
			for j := range perSource {
				measures = append(measures, r5bNovelMeasure(i*perSource+j, "1"))
			}
			observation := r5bCumulativeObservation(fmt.Sprintf("r5b-agg-%d", i), 1, measures, nil)
			if admission := attempt.queueEconomicCheckpoint(observation); admission == economicCheckpointRejected {
				t.Fatalf("source %d unexpectedly rejected", i)
			}
			if err := attempt.flushEconomicCheckpoints(context.Background(), true); err != nil {
				t.Fatalf("flush source %d: %v", i, err)
			}
		}

		heads, fields, refs, bytes := r5bHeadAggregate(attempt)
		if heads > maxPreTerminalEconomicCheckpointDurableHeads {
			t.Fatalf("aggregate heads=%d, want <= %d", heads, maxPreTerminalEconomicCheckpointDurableHeads)
		}
		if fields > maxEconomicCheckpointTotalFields {
			t.Fatalf("aggregate fields=%d, want <= %d", fields, maxEconomicCheckpointTotalFields)
		}
		if refs > maxEconomicCheckpointTotalSupersedes {
			t.Fatalf("aggregate supersession refs=%d, want <= %d", refs, maxEconomicCheckpointTotalSupersedes)
		}
		if bytes > maxEconomicCheckpointTotalHeadMetadataBytes {
			t.Fatalf("aggregate metadata bytes=%d, want <= %d", bytes, maxEconomicCheckpointTotalHeadMetadataBytes)
		}
		if fields != maxEconomicCheckpointTotalFields {
			t.Fatalf("aggregate fields=%d, want the cap to saturate at %d", fields, maxEconomicCheckpointTotalFields)
		}
	})
}

// r5bFlakySink models a durable checkpoint sink that is unavailable until it is
// explicitly healed. It implements metering.AtomicObservationSink.
type r5bFlakySink struct {
	mu       sync.Mutex
	healthy  bool
	appended []metering.Observation
}

func (s *r5bFlakySink) Append(ctx context.Context, observation metering.Observation) error {
	return s.AppendObservations(ctx, []metering.Observation{observation})
}

func (s *r5bFlakySink) AppendObservations(_ context.Context, observations []metering.Observation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.healthy {
		return errors.New("journal unavailable")
	}
	for _, observation := range observations {
		s.appended = append(s.appended, observation.Clone())
	}
	return nil
}

func (s *r5bFlakySink) heal() {
	s.mu.Lock()
	s.healthy = true
	s.mu.Unlock()
}

func (s *r5bFlakySink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.appended)
}

// TestEconomicCheckpointFailingSinkStaysBoundedAndSticky drives thousands of
// cumulative coalescing/retry admissions with an unavailable sink. The
// pending/deferred maps and their order indexes must stay bounded, the accepted
// queue must retain retry ownership and write exactly its capacity once the sink
// heals, the checkpoint-capacity rejection must become sticky R5-A capture loss,
// and a later successful flush must neither erase that loss nor let durable-head
// metadata grow past its aggregate budget.
func TestEconomicCheckpointFailingSinkStaysBoundedAndSticky(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sink := &r5bFlakySink{}
	attempt := r5bAttempt(sink)

	const overflow = 7
	feed := r5bCheckpointCapacity + overflow
	for i := range feed {
		observation := r5bCumulativeObservation(fmt.Sprintf("r5b-fail-%d", i), 1, []metering.Measure{r5bNovelMeasure(i, "1")}, nil)
		attempt.rememberEconomicEvidenceOnce(execbackend.EconomicEvidence{Observation: observation, Coverage: "complete"})
	}

	attempt.checkpointMu.Lock()
	pending := len(attempt.checkpointPending)
	deferred := len(attempt.checkpointDeferred)
	pendingOrder := len(attempt.checkpointOrder)
	deferredOrder := len(attempt.checkpointDeferredOrder)
	transientErr := attempt.checkpointErr
	attempt.checkpointMu.Unlock()

	if pending > maxPreTerminalEconomicCheckpointPending {
		t.Fatalf("pending map=%d, want <= %d", pending, maxPreTerminalEconomicCheckpointPending)
	}
	if deferred > maxPreTerminalEconomicCheckpointDeferred {
		t.Fatalf("deferred map=%d, want <= %d", deferred, maxPreTerminalEconomicCheckpointDeferred)
	}
	if pending+deferred != r5bCheckpointCapacity {
		t.Fatalf("accepted queue entries=%d, want %d", pending+deferred, r5bCheckpointCapacity)
	}
	if pendingOrder != pending {
		t.Fatalf("pending order index=%d, want exactly pending map size %d", pendingOrder, pending)
	}
	if deferredOrder != deferred {
		t.Fatalf("deferred order index=%d, want exactly deferred map size %d", deferredOrder, deferred)
	}
	if transientErr == nil {
		t.Fatal("capacity rejection did not surface a transient checkpoint error")
	}

	loss := attempt.evidenceCaptureLossSnapshot()
	if !loss.present {
		t.Fatal("checkpoint-capacity rejection left no sticky capture loss")
	}
	if loss.count != overflow {
		t.Fatalf("capture loss count=%d, want %d", loss.count, overflow)
	}
	if !loss.causes.has(evidenceCaptureLossCheckpointCapacity) {
		t.Fatalf("capture loss causes=%08b, want checkpoint-capacity bit", loss.causes)
	}

	sink.heal()
	if err := attempt.flushEconomicCheckpointsAtTerminal(ctx); err != nil {
		t.Fatalf("recovery flush: %v", err)
	}
	attempt.checkpointMu.Lock()
	pendingAfter := len(attempt.checkpointPending)
	deferredAfter := len(attempt.checkpointDeferred)
	transientAfter := attempt.checkpointErr
	attempt.checkpointMu.Unlock()
	if pendingAfter != 0 || deferredAfter != 0 {
		t.Fatalf("accepted queue after recovery flush=%d+%d, want drained", pendingAfter, deferredAfter)
	}
	if transientAfter != nil {
		t.Fatalf("transient checkpoint error survived successful flush: %v", transientAfter)
	}
	if got := sink.count(); got != r5bCheckpointCapacity {
		t.Fatalf("durable writes after recovery=%d, want exactly accepted capacity %d", got, r5bCheckpointCapacity)
	}
	if recovered := attempt.evidenceCaptureLossSnapshot(); !recovered.present || recovered.count != overflow {
		t.Fatalf("successful flush changed sticky capture loss: %+v", recovered)
	}

	// Healthy flushes must also respect the durable-head aggregate budget.
	sources := maxEconomicCheckpointAggregateHeadSources + 1
	for i := range sources {
		measures := make([]metering.Measure, 0, maxEconomicCheckpointHeadFields)
		for j := range maxEconomicCheckpointHeadFields {
			measures = append(measures, r5bNovelMeasure(100000+i*maxEconomicCheckpointHeadFields+j, "1"))
		}
		observation := r5bCumulativeObservation(fmt.Sprintf("r5b-fail-heal-%d", i), 1, measures, nil)
		attempt.rememberEconomicEvidenceOnce(execbackend.EconomicEvidence{Observation: observation, Coverage: "complete"})
		if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
			t.Fatalf("healed flush source %d: %v", i, err)
		}
	}

	heads, fields, refs, bytes := r5bHeadAggregate(attempt)
	if heads > maxPreTerminalEconomicCheckpointDurableHeads {
		t.Fatalf("aggregate heads=%d, want <= %d", heads, maxPreTerminalEconomicCheckpointDurableHeads)
	}
	if fields > maxEconomicCheckpointTotalFields {
		t.Fatalf("aggregate fields=%d, want <= %d", fields, maxEconomicCheckpointTotalFields)
	}
	if refs > maxEconomicCheckpointTotalSupersedes {
		t.Fatalf("aggregate supersession refs=%d, want <= %d", refs, maxEconomicCheckpointTotalSupersedes)
	}
	if bytes > maxEconomicCheckpointTotalHeadMetadataBytes {
		t.Fatalf("aggregate metadata bytes=%d, want <= %d", bytes, maxEconomicCheckpointTotalHeadMetadataBytes)
	}
	if cleared := attempt.evidenceCaptureLossSnapshot(); !cleared.present || cleared.count != overflow {
		t.Fatalf("healthy progression erased sticky capture loss: %+v", cleared)
	}
}

type r5bTokenReduced struct {
	input    string
	output   string
	complete bool
}

func r5bTokenKey(direction metering.FlowDirection, component string) metering.ComponentKey {
	return metering.ComponentKey{Direction: direction, Component: component, Unit: metering.UnitToken}
}

func r5bTokenReduce(t *testing.T, observations []metering.Observation) r5bTokenReduced {
	t.Helper()
	snapshot, err := aggregate.ApplyObservations(observations)
	if err != nil {
		t.Fatalf("reduce %d observations: %v", len(observations), err)
	}
	return r5bTokenReduced{
		input:    r5bSnapshotValue(snapshot, r5bTokenKey(metering.DirectionInput, metering.ComponentInputToken)),
		output:   r5bSnapshotValue(snapshot, r5bTokenKey(metering.DirectionOutput, metering.ComponentOutputToken)),
		complete: snapshot.Complete,
	}
}

// TestEconomicCheckpointBoundedHeadPreservesR4Corrections is the R4 regression
// that must keep passing under the new durable-head budgets: explicit zero is
// authoritative, an unavailable diagnostic is never erased, sparse disjoint
// fields are retained, and a reordered older sparse revision is admissible. Each
// case drives the file-backed flush/close/reopen path and asserts an explicit
// literal rather than deriving the expectation from the code under test.
func TestEconomicCheckpointBoundedHeadPreservesR4Corrections(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inputKey := r5bTokenKey(metering.DirectionInput, metering.ComponentInputToken)
	outputKey := r5bTokenKey(metering.DirectionOutput, metering.ComponentOutputToken)

	tokenMeasure := func(key metering.ComponentKey, value string) metering.Measure {
		decimal := metering.Decimal{Coefficient: value, Scale: 0}
		return metering.Measure{Key: key, Value: &decimal, Quality: metering.QualityObserved}
	}
	unavailableMeasure := func(key metering.ComponentKey) metering.Measure {
		return metering.Measure{Key: key, Quality: metering.QualityUnavailable}
	}

	cases := []struct {
		name  string
		steps [][]metering.Observation
		want  r5bTokenReduced
	}{
		{
			name: "explicit_zero",
			steps: [][]metering.Observation{
				{r5bCumulativeObservation("r5b-r4-zero", 1, []metering.Measure{tokenMeasure(inputKey, "100")}, nil)},
				{r5bCumulativeObservation("r5b-r4-zero", 2, []metering.Measure{tokenMeasure(inputKey, "0")}, nil)},
			},
			want: r5bTokenReduced{input: "0/0", complete: true},
		},
		{
			name: "unavailable_then_known",
			steps: [][]metering.Observation{
				{r5bCumulativeObservation("r5b-r4-unavailable", 1, []metering.Measure{unavailableMeasure(inputKey)}, nil)},
				{r5bCumulativeObservation("r5b-r4-unavailable", 2, []metering.Measure{tokenMeasure(inputKey, "200")}, nil)},
			},
			want: r5bTokenReduced{input: "200/0", complete: false},
		},
		{
			name: "sparse_disjoint",
			steps: [][]metering.Observation{{
				r5bCumulativeObservation("r5b-r4-sparse", 1, []metering.Measure{tokenMeasure(inputKey, "100")}, nil),
				r5bCumulativeObservation("r5b-r4-sparse", 2, []metering.Measure{tokenMeasure(outputKey, "20")}, nil),
			}},
			want: r5bTokenReduced{input: "100/0", output: "20/0", complete: true},
		},
		{
			name: "reordered_sparse",
			steps: [][]metering.Observation{
				{r5bCumulativeObservation("r5b-r4-reordered", 2, []metering.Measure{tokenMeasure(inputKey, "200")}, nil)},
				{r5bCumulativeObservation("r5b-r4-reordered", 1, []metering.Measure{tokenMeasure(inputKey, "100")}, nil)},
			},
			want: r5bTokenReduced{input: "200/0", complete: true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "r5b-r4-"+tc.name+".db")
			store := r5bOpenStore(t, path)
			attempt := r5bAttempt(journalstore.NewObservationSink(store))

			var original []metering.Observation
			for _, step := range tc.steps {
				for _, observation := range step {
					original = append(original, observation.Clone())
					if admission := attempt.queueEconomicCheckpoint(observation); admission == economicCheckpointRejected {
						t.Fatalf("checkpoint rejected %s revision %d", tc.name, observation.Revision)
					}
				}
				if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
					t.Fatalf("flush %s: %v", tc.name, err)
				}
			}

			if err := store.Close(); err != nil {
				t.Fatalf("close store: %v", err)
			}
			reopened := r5bOpenStore(t, path)
			durable := r5bDurableObservations(t, reopened)

			if want := r5bTokenReduce(t, original); r5bTokenReduce(t, durable) != want {
				t.Fatalf("%s durable reduction drifted from original evidence", tc.name)
			}
			if got := r5bTokenReduce(t, durable); got != tc.want {
				t.Fatalf("%s durable reduction=%+v, want %+v", tc.name, got, tc.want)
			}
		})
	}
}

// TestEconomicCheckpointHealthyProviderProgressionBeyondPending proves the real
// runtime checkpoint path carries a healthy provider buffer past its former
// 256-draft horizon. More than 300 sequential provider Add/Drain revisions must
// reach the true final native directional media and provider-money values in the
// reopened SQLite reduction, and the durable supersession graph must stay a
// valid immutable chain rather than collapsing at a lifetime observation cap.
func TestEconomicCheckpointHealthyProviderProgressionBeyondPending(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "r5b-provider.db")
	store := r5bOpenStore(t, path)

	buffer := coremetering.NewProviderEvidenceBuffer()
	buffer.BindEconomicEvidence(r5bObservationIdentity())

	attempt := r5bAttempt(journalstore.NewObservationSink(store))

	const revisions = 320
	original := make([]metering.Observation, 0, revisions)
	for revision := uint64(1); revision <= revisions; revision++ {
		buffer.Add(r5bProviderMediaMoneyDraft("provider.r5b.lifetime", revision))
		drained := buffer.DrainEconomicObservations()
		if len(drained) != 1 {
			t.Fatalf("revision %d: drained observations=%d, want 1", revision, len(drained))
		}
		original = append(original, drained[0].Clone())
		if admission := attempt.queueEconomicCheckpoint(drained[0]); admission == economicCheckpointRejected {
			t.Fatalf("revision %d: checkpoint rejected", revision)
		}
		// Flush in bounded batches so the test exercises the real checkpoint
		// path without one SQLite transaction per revision.
		if revision%preTerminalEconomicCheckpointBatch == 0 || revision == revisions {
			if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
				t.Fatalf("revision %d: flush: %v", revision, err)
			}
		}
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened := r5bOpenStore(t, path)
	durable := r5bDurableObservations(t, reopened)
	if len(durable) != revisions {
		t.Fatalf("durable observations=%d, want %d", len(durable), revisions)
	}

	want := r5bReduce(t, original)
	got := r5bReduce(t, durable)
	if got != want {
		t.Fatalf("durable reduction drifted from original evidence: want=%+v got=%+v", want, got)
	}
	if got.media != "320/0" {
		t.Fatalf("final native directional media=%q, want 320/0", got.media)
	}
	if got.finalCharge != "32000/0" {
		t.Fatalf("final provider money=%q, want 32000/0", got.finalCharge)
	}
	r5bAssertSupersessionGraph(t, durable)
}

// r5bCanonicalKeyOfSize builds a valid, per-index-distinct component key whose
// canonical JSON is exactly size bytes. It pads four dimension values, each
// within the 256-byte value bound, after asserting the achieved length so a test
// failure can never silently exercise the wrong byte width.
func r5bCanonicalKeyOfSize(index, size int) metering.Measure {
	key := metering.ComponentKey{
		Direction: metering.DirectionOutput,
		Component: fmt.Sprintf("r5b-size-%03d", index),
		Unit:      metering.UnitToken,
		SchemaID:  "r5b.schema",
		Dimensions: []metering.Dimension{
			{Name: "d0", Value: "v"},
			{Name: "d1", Value: "v"},
			{Name: "d2", Value: "v"},
			{Name: "d3", Value: "v"},
		},
	}
	base, err := key.Normalize()
	if err != nil {
		panic(fmt.Sprintf("normalize base key: %v", err))
	}
	remaining := size - len(base.CanonicalKey())
	if remaining < 0 {
		panic(fmt.Sprintf("canonical key size %d is below the base key width", size))
	}
	for i := range base.Dimensions {
		if remaining == 0 {
			break
		}
		add := min(remaining, metering.MaxDimensionValueBytes-1)
		base.Dimensions[i].Value = strings.Repeat("v", 1+add)
		remaining -= add
	}
	if remaining != 0 {
		panic(fmt.Sprintf("canonical key size %d exceeds the padable base key range", size))
	}
	normalized, err := base.Normalize()
	if err != nil {
		panic(fmt.Sprintf("normalize padded key: %v", err))
	}
	if got := len(normalized.CanonicalKey()); got != size {
		panic(fmt.Sprintf("canonical key width=%d, want %d", got, size))
	}
	decimal := metering.Decimal{Coefficient: "1", Scale: 0}
	return metering.Measure{Key: normalized, Value: &decimal, Quality: metering.QualityObserved}
}

// r5bStringHeaderBytes is the 64-bit Go runtime string header (two words). The
// charge model is a 64-bit layout argument; 32-bit Go uses smaller headers and
// pointers, so the same charge remains an upper bound there.
const r5bStringHeaderBytes = 16

func r5bFieldValueBytes() int { return int(unsafe.Sizeof(checkpointDurableField{})) }

// r5bGoMapStorageLowerBound is a provable lower bound, independent of the
// production charge, on the live storage of one Go map with entries string-keyed
// entries whose keys total keyBytes. Each retained entry occupies at least one
// table slot holding a string header and the value, the table keeps at least one
// control byte per slot, and the key backing arrays total at least keyBytes. It
// deliberately omits growth slack, map headers, the outer durable-head map, and
// the source-identity keys, so any excess over a budget is a genuine
// undercount.
func r5bGoMapStorageLowerBound(entries, keyBytes, valueBytes int) int {
	return entries*(r5bStringHeaderBytes+valueBytes+1) + keyBytes
}

// TestEconomicCheckpointDurableHeadChargeCoversGoMapStorage is the R5-B
// follow-up arithmetic gate: the per-entry charge must dominate the unavoidable
// Go map-slot storage of the key it prices. The former len(key)+64 charge equaled
// content+string header+value struct exactly, leaving zero for control bytes,
// growth slack, headers, allocator rounding, or the source-identity outer map,
// yet was compared against a budget that claimed exact real bytes.
func TestEconomicCheckpointDurableHeadChargeCoversGoMapStorage(t *testing.T) {
	t.Parallel()
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("charge model is a 64-bit layout argument")
	}
	valueBytes := r5bFieldValueBytes()
	if valueBytes < 48 {
		t.Fatalf("checkpointDurableField size=%d, want >= 48 on a 64-bit runtime", valueBytes)
	}
	for _, size := range []int{1, 16, 64, 448, 2048, 4096} {
		charge := checkpointDurableFieldEntryBytes(strings.Repeat("k", size))
		slotMinimum := size + r5bStringHeaderBytes + valueBytes + 1
		if charge < slotMinimum {
			t.Fatalf("field entry charge=%d for canonical key size %d, want >= unavoidable map-slot storage %d", charge, size, slotMinimum)
		}
	}
	// The reviewer's counterexample: 128 distinct 448-byte keys price to exactly
	// the per-head cap under len(key)+64, but the unavoidable map-slot storage of
	// those entries alone already exceeds the cap before any growth slack,
	// headers, allocator rounding, or the source identity is added.
	const wideSize, wideCount = 448, 128
	lower := r5bGoMapStorageLowerBound(wideCount, wideCount*wideSize, valueBytes)
	if lower <= maxEconomicCheckpointHeadMetadataBytes {
		t.Fatalf("128x448 unavoidable map storage lower bound=%d, want > per-head cap %d", lower, maxEconomicCheckpointHeadMetadataBytes)
	}
}

// TestEconomicCheckpointDurableHeadLiteralBudgetRejectsAtCeiling drives the
// actual queue/flush path with the reviewer's counterexample shapes. A valid key
// width is chosen so that a len(key)+64 charge prices a head (and sixteen heads)
// to exactly the nominal budget, so the former accounting admitted every key
// while real Go map storage exceeded the cap. The conservative charge must bind
// before that point, keep the charged budget within its cap, and never invent
// coverage for a refused key.
func TestEconomicCheckpointDurableHeadLiteralBudgetRejectsAtCeiling(t *testing.T) {
	t.Parallel()

	t.Run("wide_128_by_448_single_head", func(t *testing.T) {
		t.Parallel()
		sink := &refinement41ObservationSink{}
		attempt := r5bAttempt(sink)
		const sourceKey = "r5b-literal-head"
		const size, count = 448, 128
		for i := range count {
			observation := r5bCumulativeObservation(sourceKey, uint64(i+1), []metering.Measure{r5bCanonicalKeyOfSize(i, size)}, nil)
			if admission := attempt.queueEconomicCheckpoint(observation); admission == economicCheckpointRejected {
				t.Fatalf("revision %d unexpectedly rejected", i+1)
			}
			if err := attempt.flushEconomicCheckpoints(context.Background(), true); err != nil {
				t.Fatalf("flush revision %d: %v", i+1, err)
			}
		}

		base := economicCheckpointPendingKey(r5bCumulativeObservation(sourceKey, count, nil, nil))
		attempt.checkpointMu.Lock()
		head := attempt.checkpointDurableHeads[base]
		attempt.checkpointMu.Unlock()

		if count*(size+64) != maxEconomicCheckpointHeadMetadataBytes {
			t.Fatalf("counterexample arithmetic changed: %d*(%d+64) != %d", count, size, maxEconomicCheckpointHeadMetadataBytes)
		}
		if len(head.fields) >= count {
			t.Fatalf("head retained all %d keys priced exactly at the nominal cap; the conservative charge must refuse before the literal ceiling", count)
		}
		if head.metadataBytes() > maxEconomicCheckpointHeadMetadataBytes {
			t.Fatalf("per-head metadata bytes=%d, want <= %d", head.metadataBytes(), maxEconomicCheckpointHeadMetadataBytes)
		}
		if !head.truncated {
			t.Fatal("head was not marked coverage-truncated")
		}
		keyBytes := 0
		for key := range head.fields {
			keyBytes += len(key)
		}
		if lower := r5bGoMapStorageLowerBound(len(head.fields), keyBytes, r5bFieldValueBytes()); lower > maxEconomicCheckpointHeadMetadataBytes {
			t.Fatalf("retained fields unavoidable storage lower bound=%d, want <= %d", lower, maxEconomicCheckpointHeadMetadataBytes)
		}

		refused := r5bCumulativeObservation(sourceKey, 5, []metering.Measure{r5bCanonicalKeyOfSize(999, size)}, nil)
		if durableHeadCovers(head, refused) {
			t.Fatal("coverage-truncated durable head falsely covers a refused component key")
		}
	})

	t.Run("sixteen_wide_heads_aggregate", func(t *testing.T) {
		t.Parallel()
		sink := &refinement41ObservationSink{}
		attempt := r5bAttempt(sink)
		const size, perHead, heads = 448, 128, 16
		for h := range heads {
			sourceKey := fmt.Sprintf("r5b-literal-agg-%02d", h)
			for i := range perHead {
				observation := r5bCumulativeObservation(sourceKey, uint64(i+1), []metering.Measure{r5bCanonicalKeyOfSize(h*perHead+i, size)}, nil)
				if admission := attempt.queueEconomicCheckpoint(observation); admission == economicCheckpointRejected {
					t.Fatalf("head %d revision %d unexpectedly rejected", h, i+1)
				}
				if err := attempt.flushEconomicCheckpoints(context.Background(), true); err != nil {
					t.Fatalf("flush head %d revision %d: %v", h, i+1, err)
				}
			}
		}

		headsSeen, fields, refs, bytes := r5bHeadAggregate(attempt)
		if count := heads * perHead; count*(size+64) != maxEconomicCheckpointTotalHeadMetadataBytes {
			t.Fatalf("counterexample arithmetic changed: %d*(%d+64) != %d", count, size, maxEconomicCheckpointTotalHeadMetadataBytes)
		}
		if headsSeen != heads {
			t.Fatalf("durable heads=%d, want %d", headsSeen, heads)
		}
		if fields >= heads*perHead {
			t.Fatalf("aggregate retained %d fields; per-head and aggregate charges must refuse before %d", fields, heads*perHead)
		}
		if refs > maxEconomicCheckpointTotalSupersedes {
			t.Fatalf("aggregate supersession refs=%d, want <= %d", refs, maxEconomicCheckpointTotalSupersedes)
		}
		if bytes > maxEconomicCheckpointTotalHeadMetadataBytes {
			t.Fatalf("aggregate metadata bytes=%d, want <= %d", bytes, maxEconomicCheckpointTotalHeadMetadataBytes)
		}
	})
}

// r5bGoSizeClasses is the 64-bit Go runtime allocator's small-object size-class
// ladder up to 32KiB; larger allocations are page-rounded. It is independent of
// the production charge and lets the follow-up assertion measure the real
// backing a retained string key consumes instead of trusting the charge.
var r5bGoSizeClasses = []int{
	8, 16, 24, 32, 48, 64, 80, 96, 112, 128, 144, 160, 176, 192, 208, 224,
	240, 256, 288, 320, 352, 384, 416, 448, 480, 512, 576, 640, 704, 768,
	896, 1024, 1152, 1280, 1408, 1536, 1792, 2048, 2304, 2688, 3072, 3200,
	3456, 4096, 4864, 5376, 6144, 6528, 6784, 6912, 8192, 9472, 9728, 10240,
	10880, 12288, 13568, 14336, 16384, 18432, 19072, 20480, 21760, 24576,
	27264, 28672, 32768,
}

// r5bGoStringBackingBytes is the real allocation size class a Go string of n
// bytes occupies. A 4097-byte string therefore rounds to 4864, which is the
// 118.75% overshoot that made a len(key)+reserve charge undercount.
func r5bGoStringBackingBytes(n int) int {
	if n <= 0 {
		return 0
	}
	for _, class := range r5bGoSizeClasses {
		if n <= class {
			return class
		}
	}
	const page = 8192
	return ((n + page - 1) / page) * page
}

// r5bCanonicalKeyOfExactSize builds a valid, per-index-distinct component key
// whose canonical JSON is exactly size bytes. It pads dimension values across as
// many of the allowed dimensions as needed, so unlike r5bCanonicalKeyOfSize it
// reaches the multi-kilobyte widths that cross the allocator size-class
// boundary. It panics if size is not exactly achievable, so a test can never
// silently exercise the wrong byte width.
func r5bCanonicalKeyOfExactSize(index, size int) metering.Measure {
	base := metering.ComponentKey{
		Direction: metering.DirectionOutput,
		Component: fmt.Sprintf("r5b-large-%04d", index),
		Unit:      metering.UnitToken,
		SchemaID:  "r5b.schema",
	}
	for d := range metering.MaxDimensions {
		base.Dimensions = append(base.Dimensions, metering.Dimension{Name: fmt.Sprintf("d%02d", d), Value: "v"})
	}
	normalized, err := base.Normalize()
	if err != nil {
		panic(fmt.Sprintf("normalize base key: %v", err))
	}
	remaining := size - len(normalized.CanonicalKey())
	if remaining < 0 {
		panic(fmt.Sprintf("canonical key size %d is below the base key width", size))
	}
	for i := range normalized.Dimensions {
		if remaining == 0 {
			break
		}
		add := min(remaining, metering.MaxDimensionValueBytes-1)
		normalized.Dimensions[i].Value = strings.Repeat("v", 1+add)
		remaining -= add
	}
	if remaining != 0 {
		panic(fmt.Sprintf("canonical key size %d exceeds the padable range", size))
	}
	padded, err := normalized.Normalize()
	if err != nil {
		panic(fmt.Sprintf("normalize padded key: %v", err))
	}
	if got := len(padded.CanonicalKey()); got != size {
		panic(fmt.Sprintf("canonical key width=%d, want %d", got, size))
	}
	decimal := metering.Decimal{Coefficient: "1", Scale: 0}
	return metering.Measure{Key: padded, Value: &decimal, Quality: metering.QualityObserved}
}

// r5bHeadActualKeyBacking is the size-class allocation total of every retained
// string key in one head. It ignores map slots, headers and table slack, so it
// is a lower bound on real resident storage and must stay within the budget.
func r5bHeadActualKeyBacking(head checkpointDurableHead) int {
	total := 0
	for key := range head.fields {
		total += r5bGoStringBackingBytes(len(key))
	}
	for ref := range head.supersedes {
		total += r5bGoStringBackingBytes(len(ref))
	}
	return total
}

func r5bHeadActualBackingAggregate(attempt *attemptSession) int {
	attempt.checkpointMu.Lock()
	defer attempt.checkpointMu.Unlock()
	total := 0
	for _, head := range attempt.checkpointDurableHeads {
		total += r5bHeadActualKeyBacking(head)
	}
	return total
}

// TestEconomicCheckpointDurableHeadBackingStaysWithinBudget is the second
// adversarial follow-up: a len(key)+reserve charge undercounts the Go 1.26.6
// allocator's size-class rounding. A 4097-byte canonical key charges 4353 under
// len+256 but really occupies a 4864-byte class, so fourteen of them charge
// 60,942 (under the 64KiB cap) while their backing is 68,096 (over it), and
// sixteen such heads exceed the 1MiB aggregate. The enforceable charge must
// dominate each key's backing plus map overhead, keep both the per-head and
// aggregate caps, and never invent coverage for a refused key.
func TestEconomicCheckpointDurableHeadBackingStaysWithinBudget(t *testing.T) {
	t.Parallel()
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("charge model is a 64-bit layout argument")
	}

	const keySize = 4097
	if got := r5bGoStringBackingBytes(keySize); got != 4864 {
		t.Fatalf("Go allocator size class for %d bytes=%d, want 4864", keySize, got)
	}
	// Source-level budget proof: the per-entry charge must dominate the real
	// size-class backing plus the string header and value that live in the map
	// slot. Check the reviewer's width and the largest achievable key width.
	valueBytes := r5bFieldValueBytes()
	for _, size := range []int{2048, 4096, keySize, 4600} {
		charge := checkpointDurableFieldEntryBytes(strings.Repeat("k", size))
		minimum := r5bGoStringBackingBytes(size) + r5bStringHeaderBytes + valueBytes + 1
		if charge < minimum {
			t.Fatalf("field entry charge=%d for canonical key size %d, want >= real backing+slot %d", charge, size, minimum)
		}
	}

	t.Run("single_head_just_above_4096", func(t *testing.T) {
		t.Parallel()
		sink := &refinement41ObservationSink{}
		attempt := r5bAttempt(sink)
		const sourceKey = "r5b-backing-head"
		const feed = 40
		for i := range feed {
			observation := r5bCumulativeObservation(sourceKey, uint64(i+1), []metering.Measure{r5bCanonicalKeyOfExactSize(i, keySize)}, nil)
			if admission := attempt.queueEconomicCheckpoint(observation); admission == economicCheckpointRejected {
				t.Fatalf("revision %d unexpectedly rejected", i+1)
			}
			if err := attempt.flushEconomicCheckpoints(context.Background(), true); err != nil {
				t.Fatalf("flush revision %d: %v", i+1, err)
			}
		}

		base := economicCheckpointPendingKey(r5bCumulativeObservation(sourceKey, feed, nil, nil))
		attempt.checkpointMu.Lock()
		head := attempt.checkpointDurableHeads[base]
		attempt.checkpointMu.Unlock()

		if backing := r5bHeadActualKeyBacking(head); backing > maxEconomicCheckpointHeadMetadataBytes {
			t.Fatalf("per-head retained key backing=%d, want <= %d (fields=%d)", backing, maxEconomicCheckpointHeadMetadataBytes, len(head.fields))
		}
		if head.metadataBytes() > maxEconomicCheckpointHeadMetadataBytes {
			t.Fatalf("per-head charged metadata=%d, want <= %d", head.metadataBytes(), maxEconomicCheckpointHeadMetadataBytes)
		}
		if !head.truncated {
			t.Fatalf("head fed %d x %d-byte keys was not marked coverage-truncated", feed, keySize)
		}
		if len(head.fields) >= feed {
			t.Fatalf("head retained all %d keys; the enforced charge must refuse before the literal ceiling", feed)
		}

		refused := r5bCumulativeObservation(sourceKey, 5, []metering.Measure{r5bCanonicalKeyOfExactSize(feed+1, keySize)}, nil)
		if durableHeadCovers(head, refused) {
			t.Fatal("coverage-truncated durable head falsely covers a refused component key")
		}
	})

	t.Run("sixteen_wide_heads_aggregate", func(t *testing.T) {
		t.Parallel()
		sink := &refinement41ObservationSink{}
		attempt := r5bAttempt(sink)
		const heads = maxEconomicCheckpointAggregateHeadSources
		const feed = 40
		for h := range heads {
			sourceKey := fmt.Sprintf("r5b-backing-agg-%02d", h)
			for i := range feed {
				observation := r5bCumulativeObservation(sourceKey, uint64(i+1), []metering.Measure{r5bCanonicalKeyOfExactSize(h*feed+i, keySize)}, nil)
				if admission := attempt.queueEconomicCheckpoint(observation); admission == economicCheckpointRejected {
					t.Fatalf("head %d revision %d unexpectedly rejected", h, i+1)
				}
				if err := attempt.flushEconomicCheckpoints(context.Background(), true); err != nil {
					t.Fatalf("flush head %d revision %d: %v", h, i+1, err)
				}
			}
		}

		headsSeen, fields, refs, bytes := r5bHeadAggregate(attempt)
		if headsSeen != heads {
			t.Fatalf("durable heads=%d, want %d", headsSeen, heads)
		}
		if fields == 0 {
			t.Fatal("no durable fields were retained")
		}
		if fields >= heads*feed {
			t.Fatalf("aggregate retained %d fields; the enforced charge must refuse before %d", fields, heads*feed)
		}
		if refs > maxEconomicCheckpointTotalSupersedes {
			t.Fatalf("aggregate supersession refs=%d, want <= %d", refs, maxEconomicCheckpointTotalSupersedes)
		}
		if bytes > maxEconomicCheckpointTotalHeadMetadataBytes {
			t.Fatalf("aggregate charged metadata=%d, want <= %d", bytes, maxEconomicCheckpointTotalHeadMetadataBytes)
		}
		if backing := r5bHeadActualBackingAggregate(attempt); backing > maxEconomicCheckpointTotalHeadMetadataBytes {
			t.Fatalf("aggregate retained key backing=%d, want <= %d", backing, maxEconomicCheckpointTotalHeadMetadataBytes)
		}
	})
}
