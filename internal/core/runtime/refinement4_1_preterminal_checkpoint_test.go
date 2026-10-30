package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	_ "modernc.org/sqlite"
)

type refinement41ObservationSource struct {
	mu      sync.Mutex
	pending []metering.Observation
}

func (s *refinement41ObservationSource) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, errors.New("refinement4.1 test stream has no client events")
}

func (*refinement41ObservationSource) Send(aipapi.Event) error { return nil }
func (*refinement41ObservationSource) Close() error            { return nil }
func (*refinement41ObservationSource) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func (s *refinement41ObservationSource) DrainEconomicObservations() []metering.Observation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]metering.Observation, len(s.pending))
	for i, observation := range s.pending {
		out[i] = observation.Clone()
	}
	s.pending = nil
	return out
}

func (s *refinement41ObservationSource) queue(observations ...metering.Observation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, observation := range observations {
		s.pending = append(s.pending, observation.Clone())
	}
}

type refinement41ObservationSink struct {
	mu        sync.Mutex
	appended  []metering.Observation
	err       error
	batchCall int
}

type refinement41InFlightFailureSink struct {
	refinement41ObservationSink
	started   chan struct{}
	release   chan struct{}
	blockOnce sync.Once
}

func (s *refinement41InFlightFailureSink) Append(ctx context.Context, observation metering.Observation) error {
	return s.refinement41ObservationSink.Append(ctx, observation)
}

func (s *refinement41InFlightFailureSink) AppendObservations(ctx context.Context, observations []metering.Observation) error {
	first := false
	s.blockOnce.Do(func() {
		close(s.started)
		first = true
	})
	if first {
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return errors.New("journal unavailable")
	}
	return s.refinement41ObservationSink.AppendObservations(ctx, observations)
}

// refinement41PartialGenericObservationSink models the pre-contract generic
// sink: its single-observation append can commit a prefix before returning an
// error. Retrying the whole checkpoint batch is therefore unsafe.
type refinement41PartialGenericObservationSink struct {
	mu           sync.Mutex
	appended     []metering.Observation
	failRevision uint64
	err          error
}

func (s *refinement41PartialGenericObservationSink) Append(_ context.Context, observation metering.Observation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failRevision == observation.Revision {
		return s.err
	}
	s.appended = append(s.appended, observation.Clone())
	return nil
}

func (s *refinement41PartialGenericObservationSink) observationsSnapshot() []metering.Observation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]metering.Observation, len(s.appended))
	for i, observation := range s.appended {
		out[i] = observation.Clone()
	}
	return out
}

// refinement41AmbiguousAtomicObservationSink models a retry-safe sink whose
// first transaction commits but whose acknowledgement is lost. Exact replay
// identity makes a subsequent batch retry a no-op.
type refinement41AmbiguousAtomicObservationSink struct {
	mu         sync.Mutex
	appended   []metering.Observation
	seen       map[string]struct{}
	batchCalls int
	failed     bool
}

func (s *refinement41AmbiguousAtomicObservationSink) Append(_ context.Context, observation metering.Observation) error {
	return s.AppendObservations(context.Background(), []metering.Observation{observation})
}

func (s *refinement41AmbiguousAtomicObservationSink) AppendObservations(_ context.Context, observations []metering.Observation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batchCalls++
	if s.seen == nil {
		s.seen = make(map[string]struct{})
	}
	for _, observation := range observations {
		key := observation.SourceEventIdentity() + "\x00" + observation.Fingerprint()
		if _, exists := s.seen[key]; exists {
			continue
		}
		s.seen[key] = struct{}{}
		s.appended = append(s.appended, observation.Clone())
	}
	if !s.failed {
		s.failed = true
		return errors.New("ambiguous commit acknowledgement")
	}
	return nil
}

func (s *refinement41AmbiguousAtomicObservationSink) observationsSnapshot() []metering.Observation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]metering.Observation, len(s.appended))
	for i, observation := range s.appended {
		out[i] = observation.Clone()
	}
	return out
}

func (s *refinement41AmbiguousAtomicObservationSink) batchCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.batchCalls
}

func (s *refinement41ObservationSink) Append(_ context.Context, observation metering.Observation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.appended = append(s.appended, observation.Clone())
	return nil
}

func (s *refinement41ObservationSink) AppendObservations(_ context.Context, observations []metering.Observation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.batchCall++
	for _, observation := range observations {
		s.appended = append(s.appended, observation.Clone())
	}
	return nil
}

func (s *refinement41ObservationSink) observationsSnapshot() []metering.Observation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]metering.Observation, len(s.appended))
	for i, observation := range s.appended {
		out[i] = observation.Clone()
	}
	return out
}

func (s *refinement41ObservationSink) batchCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.batchCall
}

func refinement41Observation(revision uint64, value string) metering.Observation {
	now := time.Unix(1_700_000_000+int64(revision), 0).UTC()
	quantity := metering.Decimal{Coefficient: value, Scale: 0}
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: "obs-refinement41", SourceEventKey: "provider-usage",
		Revision: revision, StreamID: "stream-refinement41", Sequence: revision,
		Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator,
		Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "store-refinement41", ALegID: "a-refinement41", BillingCallID: "call-refinement41", BLegID: "b-refinement41"},
		Correlation: metering.CorrelationV2{StoreID: "store-refinement41", CallID: "call-refinement41", BillingCallID: "call-refinement41", ALegID: "a-refinement41", BLegID: "b-refinement41"},
		Semantics:   metering.SemanticsCumulative, ObservedAt: now, ReceivedAt: now, MappingRef: "refinement4.1",
		Measures: []metering.Measure{{Key: metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "refinement4.1"}, Value: &quantity, Quality: metering.QualityObserved}},
	}
}

func refinement41Attempt(source *refinement41ObservationSource, sink metering.ObservationSink) *attemptSession {
	return &attemptSession{
		inner: source, observationSink: sink,
		now: func() time.Time { return time.Unix(1_700_000_100, 0).UTC() },
	}
}

func TestRefinement41PreTerminalCheckpointIsDurablyVisible(t *testing.T) {
	t.Parallel()
	source := &refinement41ObservationSource{}
	sink := &refinement41ObservationSink{}
	source.queue(refinement41Observation(1, "3"))
	attempt := refinement41Attempt(source, sink)

	attempt.drainSidebandEvidence(context.Background(), recvTurnFacts{}, newResponsePipeline())

	got := sink.observationsSnapshot()
	if len(got) != 1 {
		t.Fatalf("pre-terminal durable observations = %d, want 1", len(got))
	}
	if got[0].Revision != 1 || got[0].Measures[0].Value.CanonicalString() != "3/0" {
		t.Fatalf("pre-terminal observation = %+v, want revision 1/value 3", got[0])
	}
}

func TestRefinement41PreTerminalCheckpointIsQueryableFromDurableStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journalstore.NewDurableStore(ctx, bunDB, journalstore.DurableConfig{StoreID: "store-refinement41"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	source := &refinement41ObservationSource{}
	source.queue(refinement41Observation(1, "3"))
	attempt := refinement41Attempt(source, journalstore.NewObservationSink(store))
	attempt.drainSidebandEvidence(ctx, recvTurnFacts{}, newResponsePipeline())

	page, err := store.ListObservations(ctx, journalstore.ObservationQuery{
		StoreID: "store-refinement41", SubjectKind: metering.SubjectBLeg, SubjectID: "b-refinement41", Limit: 10,
	})
	if err != nil {
		t.Fatalf("query pre-terminal observation: %v", err)
	}
	if len(page.Observations) != 1 || page.Observations[0].Revision != 1 {
		t.Fatalf("durable pre-terminal observations = %+v, want revision 1", page.Observations)
	}
}

func TestRefinement41LocalBoundaryCheckpointIsDurablyVisibleBeforeTerminal(t *testing.T) {
	t.Parallel()
	boundary := coremetering.NewBoundaryAccumulator()
	boundary.PrepareCall(aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("input")}}}})
	boundary.MarkAttempted()
	boundary.MarkAccepted(true)
	source := &refinement41ObservationSource{}
	sink := &refinement41ObservationSink{}
	clock := time.Unix(1_700_000_100, 0).UTC()
	attempt := &attemptSession{
		inner: source, boundary: boundary, observationSink: sink,
		billingStoreID: "store-refinement41", billingCallID: usageevidence.BillingCallID("bc_0123456789abcdef0123456789abcdef"),
		bleg: b2bua.BLegRecord{ALegID: "a-refinement41", BLegID: "b-refinement41", Seq: 1},
		now:  func() time.Time { return clock },
	}

	attempt.drainSidebandEvidence(context.Background(), recvTurnFacts{}, newResponsePipeline())
	first := sink.observationsSnapshot()
	if len(first) != 3 {
		t.Fatalf("local pre-terminal observation writes = %d, want 3 boundary planes", len(first))
	}
	for _, observation := range first {
		if observation.Origin != metering.OriginLocal || observation.Revision != 1 {
			t.Fatalf("local pre-terminal observation = %+v, want local revision 1", observation)
		}
	}

	boundary.ObserveProviderEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "provider output"})
	clock = clock.Add(time.Second)
	attempt.drainSidebandEvidence(context.Background(), recvTurnFacts{}, newResponsePipeline())
	second := sink.observationsSnapshot()
	if len(second) != 4 {
		t.Fatalf("local changed checkpoint writes = %d, want one additional revision", len(second))
	}
	var sawRevisionTwo bool
	for _, observation := range second {
		if observation.Origin == metering.OriginLocal && observation.Boundary == metering.BoundaryBackendIngress && observation.Revision == 2 {
			sawRevisionTwo = true
		}
	}
	if !sawRevisionTwo {
		t.Fatalf("local provider-output checkpoint missing revision 2: %+v", second)
	}

	// Terminal assembly reuses the latest local heads and must not append the
	// unchanged pre-terminal revisions a second time.
	_ = attempt.drainLocalBoundaryObservations(clock.Add(time.Second))
	if err := attempt.flushEconomicCheckpointsAtTerminal(context.Background()); err != nil {
		t.Fatalf("terminal local checkpoint flush: %v", err)
	}
	if got := len(sink.observationsSnapshot()); got != 4 {
		t.Fatalf("terminal local checkpoint writes = %d, want no duplicate", got)
	}
}

func TestRefinement41PreTerminalCheckpointsCoalesceCumulativeSnapshots(t *testing.T) {
	t.Parallel()
	source := &refinement41ObservationSource{}
	sink := &refinement41ObservationSink{}
	attempt := refinement41Attempt(source, sink)

	observations := make([]metering.Observation, 0, maxPreTerminalEconomicCheckpointPending+8)
	for i := uint64(1); i <= maxPreTerminalEconomicCheckpointPending+8; i++ {
		observations = append(observations, refinement41Observation(i, fmt.Sprintf("%d", i)))
	}
	source.queue(observations...)
	attempt.drainSidebandEvidence(context.Background(), recvTurnFacts{}, newResponsePipeline())

	got := sink.observationsSnapshot()
	if len(got) > 2 {
		t.Fatalf("coalesced cumulative checkpoint writes = %d, want at most 2", len(got))
	}
	if len(got) == 0 || got[len(got)-1].Revision != uint64(len(observations)) {
		t.Fatalf("coalesced checkpoint head = %+v, want latest revision %d", got, len(observations))
	}
}

func TestRefinement41PreTerminalDeltaCheckpointsUseBoundedBatch(t *testing.T) {
	t.Parallel()
	source := &refinement41ObservationSource{}
	sink := &refinement41ObservationSink{}
	attempt := refinement41Attempt(source, sink)

	observations := make([]metering.Observation, 0, preTerminalEconomicCheckpointBatch*2)
	for i := uint64(1); i <= preTerminalEconomicCheckpointBatch*2; i++ {
		observation := refinement41Observation(i, "1")
		observation.Semantics = metering.SemanticsDelta
		observations = append(observations, observation)
	}
	source.queue(observations...)
	attempt.drainSidebandEvidence(context.Background(), recvTurnFacts{}, newResponsePipeline())

	if got := len(sink.observationsSnapshot()); got != len(observations) {
		t.Fatalf("delta checkpoint observations = %d, want %d", got, len(observations))
	}
	if got := sink.batchCalls(); got != 1 {
		t.Fatalf("delta checkpoint batch calls = %d, want one bounded batch", got)
	}
}

func TestRefinement41FullQueueFailedFlushRetainsAcceptedObservationForRecovery(t *testing.T) {
	t.Parallel()
	source := &refinement41ObservationSource{}
	sink := &refinement41ObservationSink{err: errors.New("journal unavailable")}
	attempt := refinement41Attempt(source, sink)

	initial := make([]metering.Observation, 0, maxPreTerminalEconomicCheckpointPending)
	for i := uint64(1); i <= maxPreTerminalEconomicCheckpointPending; i++ {
		observation := refinement41Observation(i, "1")
		observation.Semantics = metering.SemanticsDelta
		initial = append(initial, observation)
	}
	source.queue(initial...)
	attempt.drainSidebandEvidence(context.Background(), recvTurnFacts{}, newResponsePipeline())
	attempt.checkpointMu.Lock()
	failedPendingCount := len(attempt.checkpointPending)
	attempt.checkpointMu.Unlock()
	if failedPendingCount != maxPreTerminalEconomicCheckpointPending {
		t.Fatalf("failed checkpoint queue entries = %d, want full capacity %d retained", failedPendingCount, maxPreTerminalEconomicCheckpointPending)
	}

	// The source accepts the next observation only after the full queue's
	// flush has failed. This is the receive-path ordering that previously lost
	// the incoming observation.
	incoming := refinement41Observation(maxPreTerminalEconomicCheckpointPending+1, "1")
	incoming.Semantics = metering.SemanticsDelta
	source.queue(incoming)
	attempt.drainSidebandEvidence(context.Background(), recvTurnFacts{}, newResponsePipeline())

	attempt.checkpointMu.Lock()
	pendingCount := len(attempt.checkpointPending)
	deferredCount := len(attempt.checkpointDeferred)
	attempt.checkpointMu.Unlock()
	if pendingCount > maxPreTerminalEconomicCheckpointPending {
		t.Fatalf("failed-flush checkpoint queue grew to %d, want at most %d", pendingCount, maxPreTerminalEconomicCheckpointPending)
	}
	if deferredCount > maxPreTerminalEconomicCheckpointDeferred {
		t.Fatalf("failed-flush deferred checkpoint queue grew to %d, want at most %d", deferredCount, maxPreTerminalEconomicCheckpointDeferred)
	}
	if pendingCount+deferredCount > maxPreTerminalEconomicCheckpointPending+maxPreTerminalEconomicCheckpointDeferred {
		t.Fatalf("failed-flush checkpoint state grew to %d entries, want bounded total", pendingCount+deferredCount)
	}

	sink.mu.Lock()
	sink.err = nil
	sink.mu.Unlock()
	if err := attempt.flushEconomicCheckpoints(context.Background(), true); err != nil {
		t.Fatalf("recovered checkpoint flush: %v", err)
	}

	got := sink.observationsSnapshot()
	wantCount := len(initial) + 1
	if len(got) != wantCount {
		t.Fatalf("recovered checkpoint writes = %d, want %d accepted observations", len(got), wantCount)
	}
	if got := sink.batchCalls(); got != 1 {
		t.Fatalf("recovered checkpoint batch calls = %d, want one retry batch", got)
	}
	for i, observation := range got {
		wantRevision := uint64(i + 1)
		if observation.Revision != wantRevision {
			t.Fatalf("recovered checkpoint %d revision = %d, want %d", i, observation.Revision, wantRevision)
		}
	}
}

func TestRefinement41RejectedProviderObservationReplaysAfterCapacityRecovery(t *testing.T) {
	t.Parallel()
	sink := &refinement41InFlightFailureSink{started: make(chan struct{}), release: make(chan struct{})}
	attempt := refinement41Attempt(&refinement41ObservationSource{}, sink)

	for i := uint64(1); i <= maxPreTerminalEconomicCheckpointPending+maxPreTerminalEconomicCheckpointDeferred; i++ {
		observation := refinement41Observation(i, "1")
		observation.Semantics = metering.SemanticsDelta
		attempt.rememberEconomicObservationOnce(observation)
	}
	attempt.checkpointMu.Lock()
	queued := len(attempt.checkpointPending) + len(attempt.checkpointDeferred)
	attempt.checkpointMu.Unlock()
	if queued != maxPreTerminalEconomicCheckpointPending+maxPreTerminalEconomicCheckpointDeferred {
		t.Fatalf("in-flight checkpoint queue entries = %d, want 128", queued)
	}

	flushErr := make(chan error, 1)
	go func() { flushErr <- attempt.flushEconomicCheckpoints(context.Background(), true) }()
	select {
	case <-sink.started:
	case <-time.After(time.Second):
		t.Fatal("checkpoint flush did not become in-flight")
	}

	incoming := refinement41Observation(maxPreTerminalEconomicCheckpointPending+maxPreTerminalEconomicCheckpointDeferred+1, "1")
	incoming.Semantics = metering.SemanticsDelta
	attempt.rememberEconomicObservationOnce(incoming)
	attempt.checkpointMu.Lock()
	rejectionErr := attempt.checkpointErr
	attempt.checkpointMu.Unlock()
	if !errors.Is(rejectionErr, errEconomicCheckpointPendingLimit) {
		t.Fatalf("queue rejection error = %v, want bounded-capacity error", rejectionErr)
	}
	close(sink.release)
	if err := <-flushErr; err == nil || !strings.Contains(err.Error(), "journal unavailable") {
		t.Fatalf("in-flight failed flush error = %v, want journal unavailable", err)
	}

	// Recover the sink first so the original 128-entry batch can commit and
	// release bounded queue capacity.
	if err := attempt.flushEconomicCheckpoints(context.Background(), true); err != nil {
		t.Fatalf("capacity recovery flush: %v", err)
	}
	if got := len(sink.observationsSnapshot()); got != 128 {
		t.Fatalf("capacity recovery writes = %d, want 128", got)
	}

	// Replay the rejected observation after capacity is free. It must not be
	// suppressed by the evidence dedupe set that owns only admitted entries.
	attempt.rememberEconomicObservationOnce(incoming)
	if err := attempt.flushEconomicCheckpoints(context.Background(), true); err != nil {
		t.Fatalf("replayed checkpoint flush: %v", err)
	}
	got := sink.observationsSnapshot()
	wantCount := maxPreTerminalEconomicCheckpointPending + maxPreTerminalEconomicCheckpointDeferred + 1
	if len(got) != wantCount {
		t.Fatalf("replayed checkpoint writes = %d, want %d exactly once", len(got), wantCount)
	}
	var incomingWrites int
	for _, observation := range got {
		if observation.Revision == incoming.Revision {
			incomingWrites++
		}
	}
	if incomingWrites != 1 {
		t.Fatalf("replayed incoming observation writes = %d, want exactly one", incomingWrites)
	}
}

func TestRefinement41CheckpointCapacityRejectionLogsOnceThroughRuntimeDiagnostics(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	source := &refinement41ObservationSource{}
	sink := &refinement41ObservationSink{err: errors.New("journal unavailable")}
	attempt := refinement41Attempt(source, sink)
	attempt.bleg = b2bua.BLegRecord{ALegID: "a-refinement41", BLegID: "b-refinement41", Seq: 1}
	for i := uint64(1); i <= maxPreTerminalEconomicCheckpointPending+maxPreTerminalEconomicCheckpointDeferred; i++ {
		observation := refinement41Observation(i, "1")
		observation.Semantics = metering.SemanticsDelta
		attempt.rememberEconomicObservationOnce(observation)
	}

	pipeline := newResponsePipeline()
	pipeline.log = logger
	facts := recvTurnFacts{traceID: "trace-refinement41", aLegID: "a-refinement41"}
	firstRejected := refinement41Observation(maxPreTerminalEconomicCheckpointPending+maxPreTerminalEconomicCheckpointDeferred+1, "1")
	firstRejected.Semantics = metering.SemanticsDelta
	source.queue(firstRejected)
	attempt.drainSidebandEvidence(ctx, facts, pipeline)
	secondRejected := refinement41Observation(firstRejected.Revision+1, "1")
	secondRejected.Semantics = metering.SemanticsDelta
	source.queue(secondRejected)
	attempt.drainSidebandEvidence(ctx, facts, pipeline)

	logText := logs.String()
	if got := bytes.Count(logs.Bytes(), []byte(`"msg":"economic_checkpoint_capacity_rejected"`)); got != 1 {
		t.Fatalf("capacity rejection diagnostics = %d, want one coalesced signal: %s", got, logText)
	}
	if !strings.Contains(logText, `"reason":"checkpoint_capacity_exhausted"`) {
		t.Fatalf("capacity rejection reason missing from diagnostics: %s", logText)
	}
	if strings.Contains(logText, "provider-usage") {
		t.Fatalf("capacity rejection diagnostics leaked source-event identity: %s", logText)
	}

	var admittedLogs bytes.Buffer
	admittedSource := &refinement41ObservationSource{}
	admittedSink := &refinement41ObservationSink{}
	admittedAttempt := refinement41Attempt(admittedSource, admittedSink)
	admittedPipeline := newResponsePipeline()
	admittedPipeline.log = slog.New(slog.NewJSONHandler(&admittedLogs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	admitted := refinement41Observation(1, "1")
	admitted.Semantics = metering.SemanticsDelta
	admittedSource.queue(admitted)
	admittedAttempt.drainSidebandEvidence(ctx, facts, admittedPipeline)
	if strings.Contains(admittedLogs.String(), `"msg":"economic_checkpoint_capacity_rejected"`) {
		t.Fatalf("admitted checkpoint emitted capacity rejection diagnostic: %s", admittedLogs.String())
	}
}

func TestRefinement41LocalCumulativeHeadWaitsForBoundedQueueAdmission(t *testing.T) {
	t.Parallel()
	boundary := coremetering.NewBoundaryAccumulator()
	boundary.PrepareCall(aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("input")}}}})
	boundary.MarkAttempted()
	boundary.MarkAccepted(true)
	sink := &refinement41ObservationSink{err: errors.New("journal unavailable")}
	attempt := refinement41Attempt(&refinement41ObservationSource{}, sink)
	attempt.boundary = boundary
	attempt.billingStoreID = "store-refinement41"
	attempt.billingCallID = usageevidence.BillingCallID("bc_0123456789abcdef0123456789abcdef")
	attempt.bleg = b2bua.BLegRecord{ALegID: "a-refinement41", BLegID: "b-refinement41", Seq: 1}

	for i := uint64(1); i <= maxPreTerminalEconomicCheckpointPending+maxPreTerminalEconomicCheckpointDeferred; i++ {
		observation := refinement41Observation(i, "1")
		observation.Semantics = metering.SemanticsDelta
		attempt.rememberEconomicObservationOnce(observation)
	}
	identity := coremetering.ObservationIdentity{
		StoreID: "store-refinement41", CallID: attempt.billingCallID.String(), BillingCallID: attempt.billingCallID.String(),
		ALegID: "a-refinement41", BLegID: "b-refinement41", AttemptID: "b-refinement41", AttemptSeq: 1,
		ObservedAt: attempt.economicCheckpointNow(), ReceivedAt: attempt.economicCheckpointNow(),
	}
	local := boundary.Observations(identity)
	if got := attempt.versionLocalBoundaryObservations(local, false); len(got) != len(local) {
		t.Fatalf("local observations after rejected admission = %d, want %d", len(got), len(local))
	}
	attempt.checkpointMu.Lock()
	if got := len(attempt.localCheckpointHeads); got != 0 {
		t.Fatalf("local cumulative heads after rejected admission = %d, want 0", got)
	}
	attempt.checkpointMu.Unlock()

	sink.mu.Lock()
	sink.err = nil
	sink.mu.Unlock()
	if err := attempt.flushEconomicCheckpoints(context.Background(), true); err != nil {
		t.Fatalf("capacity recovery flush: %v", err)
	}
	if got := attempt.versionLocalBoundaryObservations(local, false); len(got) != len(local) {
		t.Fatalf("local observations after capacity recovery = %d, want %d", len(got), len(local))
	}
	attempt.checkpointMu.Lock()
	gotHeads := len(attempt.localCheckpointHeads)
	attempt.checkpointMu.Unlock()
	if gotHeads != len(local) {
		t.Fatalf("local cumulative heads after successful admission = %d, want %d", gotHeads, len(local))
	}
}

func TestRefinement41StaleCumulativeRevisionAfterSuccessfulFlushIsIgnored(t *testing.T) {
	t.Parallel()
	source := &refinement41ObservationSource{}
	sink := &refinement41ObservationSink{}
	attempt := refinement41Attempt(source, sink)

	source.queue(refinement41Observation(2, "2"))
	attempt.drainSidebandEvidence(context.Background(), recvTurnFacts{}, newResponsePipeline())
	if got := sink.observationsSnapshot(); len(got) != 1 || got[0].Revision != 2 {
		t.Fatalf("newer cumulative checkpoint writes = %+v, want revision 2", got)
	}

	// The older snapshot arrives after revision 2 has already been durably
	// accepted. It must be ignored rather than appended as a stale head.
	source.queue(refinement41Observation(1, "1"))
	attempt.drainSidebandEvidence(context.Background(), recvTurnFacts{}, newResponsePipeline())
	if err := attempt.flushEconomicCheckpoints(context.Background(), true); err != nil {
		t.Fatalf("stale cumulative checkpoint flush: %v", err)
	}

	got := sink.observationsSnapshot()
	if len(got) != 1 || got[0].Revision != 2 {
		t.Fatalf("stale cumulative checkpoint writes = %+v, want only durable revision 2", got)
	}
}

func TestRefinement41TerminalFlushesPendingOnceWithoutDuplicate(t *testing.T) {
	t.Parallel()
	source := &refinement41ObservationSource{}
	sink := &refinement41ObservationSink{}
	attempt := refinement41Attempt(source, sink)

	source.queue(refinement41Observation(1, "3"))
	attempt.drainSidebandEvidence(context.Background(), recvTurnFacts{}, newResponsePipeline())
	source.queue(refinement41Observation(2, "4"))
	attempt.drainSidebandEvidence(context.Background(), recvTurnFacts{}, newResponsePipeline())

	if err := attempt.flushEconomicCheckpoints(context.Background(), true); err != nil {
		t.Fatalf("terminal checkpoint flush: %v", err)
	}
	if err := attempt.flushEconomicCheckpoints(context.Background(), true); err != nil {
		t.Fatalf("duplicate terminal checkpoint flush: %v", err)
	}
	got := sink.observationsSnapshot()
	if len(got) != 2 {
		t.Fatalf("terminal checkpoint writes = %d, want 2 (one per durable revision)", len(got))
	}
}

func TestRefinement41ConcurrentCheckpointFlushDoesNotDuplicate(t *testing.T) {
	t.Parallel()
	source := &refinement41ObservationSource{}
	sink := &refinement41ObservationSink{}
	attempt := refinement41Attempt(source, sink)
	first := refinement41Observation(1, "3")
	second := refinement41Observation(2, "4")
	first.Semantics = metering.SemanticsDelta
	second.Semantics = metering.SemanticsDelta
	attempt.rememberEconomicObservationOnce(first)
	attempt.rememberEconomicObservationOnce(second)

	var wait sync.WaitGroup
	errCh := make(chan error, 2)
	for range 2 {
		wait.Go(func() {
			errCh <- attempt.flushEconomicCheckpoints(context.Background(), true)
		})
	}
	wait.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent checkpoint flush: %v", err)
		}
	}
	if got := len(sink.observationsSnapshot()); got != 2 {
		t.Fatalf("concurrent checkpoint writes = %d, want 2", got)
	}
	if got := sink.batchCalls(); got != 1 {
		t.Fatalf("concurrent checkpoint batch calls = %d, want one", got)
	}
}

func TestRefinement41GenericSinkWithoutAtomicBatchFailsClosedOnRetry(t *testing.T) {
	t.Parallel()
	source := &refinement41ObservationSource{}
	sink := &refinement41PartialGenericObservationSink{
		failRevision: 2,
		err:          errors.New("partial sink failure"),
	}
	attempt := refinement41Attempt(source, sink)
	first := refinement41Observation(1, "3")
	second := refinement41Observation(2, "4")
	first.Semantics = metering.SemanticsDelta
	second.Semantics = metering.SemanticsDelta
	attempt.rememberEconomicObservationOnce(first)
	attempt.rememberEconomicObservationOnce(second)

	if err := attempt.flushEconomicCheckpoints(context.Background(), true); err == nil || !strings.Contains(err.Error(), "requires atomic batch capability") {
		t.Fatalf("non-atomic generic sink flush error = %v, want atomic capability failure", err)
	}
	if got := len(sink.observationsSnapshot()); got != 0 {
		t.Fatalf("non-atomic generic sink writes = %d, want 0 after fail-closed capability check", got)
	}

	sink.mu.Lock()
	sink.failRevision = 0
	sink.err = nil
	sink.mu.Unlock()
	if err := attempt.flushEconomicCheckpoints(context.Background(), true); err == nil || !strings.Contains(err.Error(), "requires atomic batch capability") {
		t.Fatalf("non-atomic generic sink retry error = %v, want atomic capability failure", err)
	}

	if got := len(sink.observationsSnapshot()); got != 0 {
		t.Fatalf("non-atomic generic sink retry writes = %d, want 0", got)
	}
	attempt.checkpointMu.Lock()
	deferredCount := len(attempt.checkpointDeferred)
	pendingCount := len(attempt.checkpointPending)
	attempt.checkpointMu.Unlock()
	if pendingCount+deferredCount != 2 {
		t.Fatalf("non-atomic generic sink pending entries = %d, want 2 retained for a compatible sink", pendingCount+deferredCount)
	}
}

func TestRefinement41AtomicBatchSinkRetriesAmbiguousCommitWithoutDuplicate(t *testing.T) {
	t.Parallel()
	source := &refinement41ObservationSource{}
	sink := &refinement41AmbiguousAtomicObservationSink{}
	attempt := refinement41Attempt(source, sink)
	first := refinement41Observation(1, "3")
	second := refinement41Observation(2, "4")
	first.Semantics = metering.SemanticsDelta
	second.Semantics = metering.SemanticsDelta
	attempt.rememberEconomicObservationOnce(first)
	attempt.rememberEconomicObservationOnce(second)

	if err := attempt.flushEconomicCheckpoints(context.Background(), true); err == nil {
		t.Fatal("ambiguous atomic sink flush unexpectedly succeeded")
	}
	if err := attempt.flushEconomicCheckpoints(context.Background(), true); err != nil {
		t.Fatalf("retry after ambiguous atomic sink commit: %v", err)
	}

	got := sink.observationsSnapshot()
	if len(got) != 2 {
		t.Fatalf("ambiguous atomic sink retry writes = %d, want exactly 2", len(got))
	}
	if got[0].Revision != 1 || got[1].Revision != 2 {
		t.Fatalf("ambiguous atomic sink retry revisions = [%d %d], want [1 2]", got[0].Revision, got[1].Revision)
	}
	if got := sink.batchCallCount(); got != 2 {
		t.Fatalf("ambiguous atomic sink calls = %d, want initial append plus one idempotent retry", got)
	}
}

func TestRefinement41CheckpointCancellationRetainsPendingAndSinkErrorIsObservable(t *testing.T) {
	t.Parallel()
	source := &refinement41ObservationSource{}
	sink := &refinement41ObservationSink{err: errors.New("journal unavailable")}
	attempt := refinement41Attempt(source, sink)
	source.queue(refinement41Observation(1, "3"))
	attempt.drainSidebandEvidence(context.Background(), recvTurnFacts{}, newResponsePipeline())

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	err := attempt.flushEconomicCheckpoints(canceled, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled checkpoint flush error = %v, want context.Canceled", err)
	}
	if got := len(sink.observationsSnapshot()); got != 0 {
		t.Fatalf("canceled checkpoint writes = %d, want 0", got)
	}

	sink.mu.Lock()
	sink.err = errors.New("journal unavailable")
	sink.mu.Unlock()
	err = attempt.flushEconomicCheckpoints(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "journal unavailable") {
		t.Fatalf("sink checkpoint error = %v, want journal unavailable", err)
	}
}

func TestRefinement41PreTerminalCheckpointDoesNotMutateMoney(t *testing.T) {
	t.Parallel()
	source := &refinement41ObservationSource{}
	sink := &refinement41ObservationSink{}
	attempt := refinement41Attempt(source, sink)
	var finalizerCalls int
	attempt.finalizeBillingV2 = func(context.Context, execbackend.BillingFinalizationInput) (execbackend.BillingFinalizationResult, error) {
		finalizerCalls++
		return execbackend.BillingFinalizationResult{}, nil
	}
	source.queue(refinement41Observation(1, "3"))
	attempt.drainSidebandEvidence(context.Background(), recvTurnFacts{}, newResponsePipeline())
	if finalizerCalls != 0 {
		t.Fatalf("pre-terminal checkpoint invoked money finalizer %d times", finalizerCalls)
	}
	if got := len(sink.observationsSnapshot()); got != 1 {
		t.Fatalf("pre-terminal observation writes = %d, want 1", got)
	}
}

func TestRefinement41ExecutorPassesObservationSinkToAttempt(t *testing.T) {
	t.Parallel()
	sink := &refinement41ObservationSink{}
	executor := &Executor{AccountingRuntime: AccountingRuntime{MeteringObservationSink: sink}}
	attempt := executor.newAttemptSession(attemptSessionInput{})
	if attempt == nil {
		t.Fatal("executor did not create attempt")
	}
	if attempt.observationSink != sink {
		t.Fatalf("attempt observation sink = %p, want configured sink %p", attempt.observationSink, sink)
	}
	if executor.newLocalBoundaryAccumulator() == nil || !executor.localBoundaryCaptureRequired() {
		t.Fatal("configured V2 observation sink did not enable required local boundary capture")
	}
}
