// Deterministic admission, withdrawal, and memo-exactly-once regressions for
// the interleaved thinker wrapper (spec:
// .kiro/specs/agent-loop-explicit-completion-protocol, design Concurrency and
// Lifecycle / Pending Result Routing; requirements 6.3-6.7, 11.3, 11.5, 12.5).
//
// Every case drives the real wrapper, a real processor-owned turn, and a real
// memo store. Blocking happens in a decorator *around* the real turn and store,
// never in a fake recorder, so a behavior satisfied only by a private helper
// fails here. Every ordering assertion is a channel barrier, an atomic counter
// observed under the same state the assertion describes, or a recorded call
// sequence. No case sleeps to coordinate a race.
package runtime

import (
	"context"
	"errors"
	"io"
	goruntime "runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview/sdkadapter"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
)

// barrierGuard only turns a hang into a failure. Every ordering assertion in
// this file is a channel barrier, an atomic counter, or recorded call order.
const barrierGuard = 60 * time.Second

// errBarrierTail ends the thinker B-leg with a transport error so the wrapper
// stays in the thinker phase after the barrier case released it.
var errBarrierTail = errors.New("barrier thinker tail")

// barrierAnswer is the bounded content these cases admit before withdrawal.
const barrierAnswer = "barrier admitted plan"

// errBarrierDriveExhausted ends drive() when its receive budget is spent. It is
// deliberately NOT io.EOF: a case that expected clean completion must see the
// stream's own real EOF, never a fabricated cap.
var errBarrierDriveExhausted = errors.New("barrier fixture exhausted its receive budget")

// barrierDriveBudget bounds one drive() call. It is a hang guard only.
const barrierDriveBudget = 64

// errBarrierStoreFailure is a real memo-store failure, not a stub success.
var errBarrierStoreFailure = errors.New("barrier memo store failure")

// barrierExecutorAnswer is what the real continuation must answer with.
const barrierExecutorAnswer = "executor answer after barrier memo"

// parkGate is a one-shot two-party barrier. park blocks the calling goroutine
// after signalling entered; releaseAll lets it continue. It is the only
// synchronization primitive these cases use.
type parkGate struct {
	entered     chan struct{}
	release     chan struct{}
	armed       atomic.Bool
	parkOnce    sync.Once
	releaseOnce sync.Once
}

func newParkGate() *parkGate {
	return &parkGate{entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *parkGate) arm() { g.armed.Store(true) }

func (g *parkGate) park() {
	if !g.armed.Load() {
		return
	}
	g.parkOnce.Do(func() { close(g.entered) })
	<-g.release
}

func (g *parkGate) releaseAll() { g.releaseOnce.Do(func() { close(g.release) }) }

// barrierTurn decorates the real processor-owned turn. It blocks only at the
// boundaries under test and delegates every call to the real turn, so recorder
// mutation and memo capture stay feature-owned.
type barrierTurn struct {
	inner InterleavedTurn

	// parked counts admitted Observe calls currently inside this decorator. The
	// memo store reads it to detect a Finalize that overlapped an observation.
	parked atomic.Int32

	beforeObserve *parkGate
	afterObserve  *parkGate
	beforeFlush   *parkGate
	beforeFinal   *parkGate

	finalizeCalls   atomic.Int32
	finalizeInterup atomic.Int32
}

var _ InterleavedTurn = (*barrierTurn)(nil)

// contentEvent reports whether an event carries memo/visible content, so a case
// can park at the content boundary instead of on a bookkeeping event.
func contentEvent(ev aipapi.Event) bool {
	return ev.Kind == aipapi.EventTextDelta || ev.Kind == aipapi.EventReasoningDelta
}

func (t *barrierTurn) ObserveThinkerEvent(ev aipapi.Event) ([]aipapi.Event, error) {
	t.parked.Add(1)
	defer t.parked.Add(-1)
	if contentEvent(ev) {
		t.beforeObserve.park()
	}
	visibles, err := t.inner.ObserveThinkerEvent(ev)
	if err != nil {
		return nil, err
	}
	if contentEvent(ev) {
		// The real recorder has already mutated here; this gate proves the
		// post-call fence, not admission.
		t.afterObserve.park()
	}
	return slices.Clone(visibles), nil
}

func (t *barrierTurn) FlushVisible() []aipapi.Event {
	t.beforeFlush.park()
	return t.inner.FlushVisible()
}

func (t *barrierTurn) FinalizeThinkerStatus(ctx context.Context, interrupted, visibleCommitted bool) (InterleavedMemo, error) {
	t.finalizeCalls.Add(1)
	if interrupted {
		t.finalizeInterup.Add(1)
	}
	t.beforeFinal.park()
	return t.inner.FinalizeThinkerStatus(ctx, interrupted, visibleCommitted)
}

func (t *barrierTurn) FinalizeThinker(ctx context.Context) (InterleavedMemo, error) {
	t.finalizeCalls.Add(1)
	t.beforeFinal.park()
	return t.inner.FinalizeThinker(ctx)
}

func (t *barrierTurn) ShapeThinker(call aipapi.Call) (aipapi.Call, error) {
	return t.inner.ShapeThinker(call)
}

func (t *barrierTurn) ShapeExecutor(ctx context.Context, call aipapi.Call, memo InterleavedMemo) (aipapi.Call, error) {
	return t.inner.ShapeExecutor(ctx, call, InterleavedMemo(memo))
}

func (t *barrierTurn) Visible() bool     { return t.inner.Visible() }
func (t *barrierTurn) CanContinue() bool { return t.inner.CanContinue() }

func (t *barrierTurn) ShapeDiagnostics() (string, int) { return t.inner.ShapeDiagnostics() }

func (t *barrierTurn) CommitExecutor(ctx context.Context) (int, error) {
	return t.inner.CommitExecutor(ctx)
}

// barrierMemoStore decorates the real in-memory memo store. It counts every
// durable Put, records what was persisted, notes whether any Put overlapped an
// admitted Observe, and can block inside Put.
type barrierMemoStore struct {
	inner interleavedthinking.MemoStore
	turn  *barrierTurn

	putEntered chan struct{}
	putRelease chan struct{}
	putOnce    sync.Once
	releaseOne sync.Once
	blockNext  atomic.Bool

	puts  atomic.Int32
	mu    sync.Mutex
	saved []interleavedthinking.MemoState
	// failPut makes the durable Put fail with a real error after any barrier is
	// released. puts still counts the attempt, so a case can prove an attempt
	// count that a failing store cannot hide.
	failPut error
	// overlapping counts Puts that happened while a processor observation was
	// still admitted. Serialization requires this to stay zero.
	overlapping atomic.Int32
}

var _ interleavedthinking.MemoStore = (*barrierMemoStore)(nil)

func newBarrierMemoStore(turn *barrierTurn) *barrierMemoStore {
	return &barrierMemoStore{
		inner:      interleavedthinking.NewMemoStore(4096),
		turn:       turn,
		putEntered: make(chan struct{}),
		putRelease: make(chan struct{}),
	}
}

// BlockNextPut parks the next durable memo Put until releasePut is called.
func (s *barrierMemoStore) blockNextPut() { s.blockNext.Store(true) }

// failPuts makes every durable Put fail with a real error after its barrier is
// released. The attempt counter is incremented first, so a case can still prove
// an exact attempt count that a failing store cannot hide.
func (s *barrierMemoStore) failPuts(err error) { s.failPut = err }

func (s *barrierMemoStore) releasePut() { s.releaseOne.Do(func() { close(s.putRelease) }) }

func (s *barrierMemoStore) Put(ctx context.Context, scope interleavedthinking.Scope, st interleavedthinking.MemoState) (interleavedthinking.MemoRef, error) {
	s.puts.Add(1)
	if s.turn != nil && s.turn.parked.Load() > 0 {
		s.overlapping.Add(1)
	}
	if s.blockNext.Load() {
		s.putOnce.Do(func() { close(s.putEntered) })
		select {
		case <-s.putRelease:
		case <-ctx.Done():
			return interleavedthinking.MemoRef{}, ctx.Err()
		}
	}
	if s.failPut != nil {
		return interleavedthinking.MemoRef{}, s.failPut
	}
	ref, err := s.inner.Put(ctx, scope, st)
	if err != nil {
		return interleavedthinking.MemoRef{}, err
	}
	s.mu.Lock()
	s.saved = append(s.saved, st)
	s.mu.Unlock()
	return ref, nil
}

func (s *barrierMemoStore) Get(ctx context.Context, scope interleavedthinking.Scope, ref interleavedthinking.MemoRef) (interleavedthinking.MemoState, bool, error) {
	return s.inner.Get(ctx, scope, ref)
}

func (s *barrierMemoStore) Update(ctx context.Context, scope interleavedthinking.Scope, ref interleavedthinking.MemoRef, st interleavedthinking.MemoState) (interleavedthinking.MemoRef, error) {
	return s.inner.Update(ctx, scope, ref, st)
}

func (s *barrierMemoStore) Delete(ctx context.Context, scope interleavedthinking.Scope, ref interleavedthinking.MemoRef) error {
	return s.inner.Delete(ctx, scope, ref)
}

func (s *barrierMemoStore) LatestEntry(ctx context.Context, scope interleavedthinking.Scope) (interleavedthinking.MemoState, interleavedthinking.MemoRef, bool, error) {
	return s.inner.LatestEntry(ctx, scope)
}

func (s *barrierMemoStore) snapshots() []interleavedthinking.MemoState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.saved)
}

// barrierInnerStream is the thinker backend stream. It replays a fixed prefix
// and then ends with tail (io.EOF for a clean thinker finish). When recvGate is
// armed it parks the first receive, so a case can hold the wrapper inside the
// inner receive with the processor slot idle.
type barrierInnerStream struct {
	events []aipapi.Event
	tail   error
	idx    int

	recvGate *parkGate

	cancels atomic.Int32
	closes  atomic.Int32
}

func (s *barrierInnerStream) Recv(context.Context) (aipapi.Event, error) {
	if s.recvGate != nil {
		s.recvGate.park()
	}
	if s.idx < len(s.events) {
		ev := s.events[s.idx]
		s.idx++
		return ev, nil
	}
	if s.tail != nil {
		return aipapi.Event{}, s.tail
	}
	return aipapi.Event{}, io.EOF
}

func (s *barrierInnerStream) Close() error {
	s.closes.Add(1)
	return nil
}

func (s *barrierInnerStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	s.cancels.Add(1)
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

// barrierSteeringCapture installs a real steering writer over the authoritative
// in-memory conversation-view reference store, through the existing
// Executor.SteeringWriterFactory port. It counts every actual Put attempt that
// reaches the writer, records the real PutRequest and the real error, and can
// park inside Put. Nothing here fabricates a successful overlay mutation.
type barrierSteeringCapture struct {
	cv   *conversationview.ReferenceStore
	gate *parkGate

	puts atomic.Int32
	mu   sync.Mutex
	reqs []steering.PutRequest
	errs []error
}

type barrierSteeringWriter struct {
	steering.Writer
	owner *barrierSteeringCapture
}

var _ steering.Writer = (*barrierSteeringWriter)(nil)

func (w *barrierSteeringWriter) Put(ctx context.Context, req steering.PutRequest) (steering.State, error) {
	// The admission is counted before the barrier, so "entered" means the
	// wrapper actually started this external effect.
	w.owner.puts.Add(1)
	w.owner.gate.park()
	state, err := w.Writer.Put(ctx, req)
	w.owner.mu.Lock()
	w.owner.reqs = append(w.owner.reqs, req)
	w.owner.errs = append(w.owner.errs, err)
	w.owner.mu.Unlock()
	return state, err
}

// barrierConversationReader is a narrow reader over the same reference store.
type barrierConversationReader struct {
	store *conversationview.ReferenceStore
}

var _ conversationprojection.Reader = (*barrierConversationReader)(nil)

func (r *barrierConversationReader) Snapshot(ctx context.Context, aLegID string) (conversationprojection.Snapshot, error) {
	if err := r.store.CreateALeg(ctx, aLegID); err != nil {
		return conversationprojection.Snapshot{}, err
	}
	return r.store.Snapshot(ctx, aLegID)
}

// recorded returns the real Put requests and their real results, in order.
func (c *barrierSteeringCapture) recorded() ([]steering.PutRequest, []error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.reqs), slices.Clone(c.errs)
}

// barrierRig is one assembled interleaved wrapper over a real processor turn, a
// real memo store, and a real continuation backend.
type barrierRig struct {
	ex     *Executor
	from   *retryRecvStream
	stream *interleavedContinuationStream
	turn   *barrierTurn
	store  *barrierMemoStore
	inner  *barrierInnerStream

	memoSteering *barrierSteeringCapture

	execOpenGate *parkGate
	execOpens    atomic.Int32
	execAnswer   *barrierInnerStream
}

// installSteeringCapture wires the real steering writer onto the rig executor so
// memo overlay publication is a real external effect a case can count.
func (r *barrierRig) installSteeringCapture(t *testing.T) {
	t.Helper()
	capture := &barrierSteeringCapture{
		cv:   conversationview.NewReferenceStore(),
		gate: newParkGate(),
	}
	r.ex.ConversationViewReader = &barrierConversationReader{store: capture.cv}
	r.ex.SteeringWriterFactory = func(ctx context.Context, aLegID string, resolver SteeringWriterResolver) (steering.Writer, error) {
		if err := capture.cv.CreateALeg(ctx, aLegID); err != nil {
			return nil, err
		}
		var traj sdkadapter.TrajectoryResolver
		if resolver != nil {
			traj = sdkadapter.TrajectoryResolver(resolver)
		}
		writer, err := sdkadapter.NewWriter(capture.cv, aLegID, traj)
		if err != nil {
			return nil, err
		}
		return &barrierSteeringWriter{Writer: writer, owner: capture}, nil
	}
	r.memoSteering = capture
}

// barrierThinkerPrefix is one real thinker prefix: the memo body is admitted
// through the real recorder, then the stream stays open for the barrier case.
func barrierThinkerPrefix() []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: interleavedthinking.MemoOpenTag + barrierAnswer + interleavedthinking.MemoCloseTag},
	}
}

// barrierThinkerNoMemoPrefix carries lifecycle events only, so a real Finalize
// against it yields an empty memo while still attempting one durable Put.
func barrierThinkerNoMemoPrefix() []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
	}
}

func newBarrierRig(t *testing.T, mode string, inner aipapi.ManagedEventStream) *barrierRig {
	t.Helper()
	ex, from := setupInterleavedAuthorityContinuation(t, pendingThinkerAuthority(), mode)
	installTestTurnTerminal(from)
	from.terminal.setInterleavedThinker()
	bindTurnTerminalRuntime(from.terminal, ex)

	turn := &barrierTurn{
		beforeObserve: newParkGate(),
		afterObserve:  newParkGate(),
		beforeFlush:   newParkGate(),
		beforeFinal:   newParkGate(),
	}
	store := newBarrierMemoStore(turn)
	ex.InterleavedProcessor = NewTestInterleavedProcessor(t, interleavedthinking.Config{
		Instructions:          "think",
		StreamToClient:        mode,
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}, store)
	RegisterTestMemoStore(ex, store)

	real, err := ex.InterleavedProcessor.BeginTurn(context.Background(), InterleavedTurnInput{
		ALegID:         from.facts.aLegID,
		Selector:       from.facts.baseline.Route.Selector,
		Backend:        from.attempt.snapshot().cand.Primary.Backend,
		Model:          from.attempt.snapshot().cand.Primary.Model,
		RequestID:      from.facts.traceID,
		StreamToClient: mode,
	})
	requireNoError(t, err, "the barrier fixture must open a real interleaved turn")
	if real == nil {
		t.Fatal("the barrier fixture must have a processor-owned turn")
	}
	turn.inner = real

	if inner != nil {
		testStoreInner(from, inner)
	}

	rig := &barrierRig{
		ex:           ex,
		from:         from,
		turn:         turn,
		store:        store,
		execOpenGate: newParkGate(),
	}
	if inner != nil {
		if typed, ok := inner.(*barrierInnerStream); ok {
			rig.inner = typed
		}
	}

	// The continuation backend is the real runtime opener; it parks on Open only
	// when a case asks it to, and otherwise answers like a live backend.
	base := ex.Backends["backend-1"]
	base.Open = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		rig.execOpens.Add(1)
		rig.execOpenGate.park()
		answer := &barrierInnerStream{
			events: []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: barrierExecutorAnswer},
				{Kind: aipapi.EventResponseFinished},
			},
		}
		rig.execAnswer = answer
		return answer, nil
	}
	ex.Backends["backend-1"] = base

	if mode == "visible" {
		rig.stream = newVisibleInterleavedStream(from, turn, interleavedstate.State{})
	} else {
		rig.stream = newHiddenInterleavedStream(from, turn, interleavedstate.State{})
	}
	return rig
}

func requireNoError(t *testing.T, err error, msg string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", msg, err)
	}
}

// recvResult is one finished wrapper Recv.
type recvResult struct {
	events []aipapi.Event
	err    error
}

// drive reads the wrapper until it ends. It is only ever called on the test
// goroutine or on one goroutine joined by the case. Spending the receive
// budget is an explicit failure, never a fabricated io.EOF, so a success case
// proves the stream's own clean EOF.
func (r *barrierRig) drive(ctx context.Context) recvResult {
	var out recvResult
	for range barrierDriveBudget {
		ev, err := r.stream.Recv(ctx)
		if err != nil {
			out.err = err
			return out
		}
		out.events = append(out.events, ev)
	}
	out.err = errBarrierDriveExhausted
	return out
}

// reasoningText concatenates every reasoning delta the outer client received.
func reasoningText(events []aipapi.Event) string {
	var b []byte
	for _, ev := range events {
		if ev.Kind == aipapi.EventReasoningDelta {
			b = append(b, ev.Delta...)
		}
	}
	return string(b)
}

func assistantText(events []aipapi.Event) string {
	var b []byte
	for _, ev := range events {
		if ev.Kind == aipapi.EventTextDelta {
			b = append(b, ev.Delta...)
		}
	}
	return string(b)
}

func countKind(events []aipapi.Event, kind aipapi.EventKind) int {
	count := 0
	for _, ev := range events {
		if ev.Kind == kind {
			count++
		}
	}
	return count
}

// assertNoExecutor proves no continuation was ever admitted.
func (r *barrierRig) assertNoExecutor(t *testing.T, detail string) {
	t.Helper()
	r.stream.mu.Lock()
	executor := r.stream.executor
	phase := r.stream.phase
	r.stream.mu.Unlock()
	if executor != nil {
		t.Fatalf("%s: a continuation must never be admitted after withdrawal", detail)
	}
	if phase == interleavedPhaseExecutor {
		t.Fatalf("%s: phase must not become executor after withdrawal", detail)
	}
	if got := r.execOpens.Load(); got != 0 {
		t.Fatalf("%s: backend continuation opens = %d, want 0", detail, got)
	}
}

// awaitParked proves a barrier is held before a withdrawal starts, so the case
// never depends on which goroutine wins a race.
func awaitParked(t *testing.T, detail string, gate *parkGate) {
	t.Helper()
	select {
	case <-gate.entered:
	case <-time.After(barrierGuard):
		t.Fatalf("timed out waiting for %s", detail)
	}
}

// awaitFired proves a deterministic event channel fired. It only turns a hang
// into a failure; it never asserts an ordering by elapsed time.
func awaitFired(t *testing.T, detail string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(barrierGuard):
		t.Fatalf("timed out waiting for %s", detail)
	}
}

// awaitWithdrawal proves a competing Cancel/Close has closed processor admission
// while a barrier is still held. It waits on the wrapper's own authoritative
// predicate and yields rather than sleeping, so it either observes the flag or
// fails; it never asserts an ordering by elapsed time.
func (r *barrierRig) awaitWithdrawal(t *testing.T, detail string) {
	t.Helper()
	deadline := time.Now().Add(barrierGuard)
	for {
		if r.stream.withdrawalError() != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s to close processor admission", detail)
		}
		goruntime.Gosched()
	}
}

// awaitClosed proves a competing call returned, without inspecting what it did.
func awaitClosed[T any](t *testing.T, detail string, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(barrierGuard):
		t.Fatalf("timed out waiting for %s", detail)
		var zero T
		return zero
	}
}

// TestInterleavedThinkerAdmission_CloseDuringAdmittedObserveJoinsBeforeInterruptedFinalize
// pins the core withdrawal contract: a Close that starts while an Observe
// callback is admitted must not Finalize concurrently with it. The interrupted
// memo is captured exactly once, with the prefix admitted before withdrawal, and
// no continuation is admitted afterwards.
func TestInterleavedThinkerAdmission_CloseDuringAdmittedObserveJoinsBeforeInterruptedFinalize(t *testing.T) {
	t.Parallel()

	inner := &barrierInnerStream{events: barrierThinkerPrefix(), tail: errBarrierTail}
	rig := newBarrierRig(t, "hidden", inner)
	rig.turn.beforeObserve.arm()

	recvDone := make(chan recvResult, 1)
	go func() { recvDone <- rig.drive(context.Background()) }()
	awaitParked(t, "the admitted Observe barrier", rig.turn.beforeObserve)

	closeDone := make(chan error, 1)
	go func() { closeDone <- rig.stream.Close() }()
	// Close must still be joined on the admitted callback, so no Finalize and no
	// durable memo Put may happen while the barrier is held.
	rig.awaitWithdrawal(t, "Close")
	if got := rig.turn.finalizeCalls.Load(); got != 0 {
		t.Fatalf("Finalize calls while Observe was admitted = %d, want 0 (Close must join, not finalize concurrently)", got)
	}
	if got := rig.store.puts.Load(); got != 0 {
		t.Fatalf("durable memo Puts while Observe was admitted = %d, want 0", got)
	}

	rig.turn.beforeObserve.releaseAll()
	recv := awaitClosed(t, "the withdrawn Recv", recvDone)
	closeErr := awaitClosed(t, "the joined Close", closeDone)
	if closeErr != nil && !errors.Is(closeErr, context.Canceled) {
		t.Fatalf("Close after a joined withdrawal: %v", closeErr)
	}
	if recv.err == nil {
		t.Fatal("Recv must end with the withdrawal reason, not reasoning")
	}

	if got := rig.store.overlapping.Load(); got != 0 {
		t.Fatalf("durable memo Puts overlapping an admitted Observe = %d, want 0", got)
	}
	if got := rig.turn.finalizeCalls.Load(); got != 1 {
		t.Fatalf("Finalize calls = %d, want exactly 1 interrupted capture", got)
	}
	if got := rig.turn.finalizeInterup.Load(); got != 1 {
		t.Fatalf("interrupted Finalize calls = %d, want 1 (no relabeling second Finalize)", got)
	}
	saved := rig.store.snapshots()
	if len(saved) != 1 {
		t.Fatalf("persisted memo snapshots = %d, want exactly 1", len(saved))
	}
	if saved[0].Memo != barrierAnswer {
		t.Fatalf("persisted memo = %q, want the admitted prefix %q", saved[0].Memo, barrierAnswer)
	}
	if !saved[0].StreamInterrupted {
		t.Fatal("an interrupted capture must be persisted with StreamInterrupted=true")
	}
	rig.assertNoExecutor(t, "close during admitted Observe")
	if got := reasoningText(recv.events); got != "" {
		t.Fatalf("hidden mode must release no reasoning, got %q", got)
	}
}

// TestInterleavedThinkerAdmission_CancelDuringAdmittedObserveDefersTheInterruptedMemo
// is the Cancel counterpart. It proves the withdrawal request survives the
// admitted callback and is claimed exactly once afterwards.
func TestInterleavedThinkerAdmission_CancelDuringAdmittedObserveDefersTheInterruptedMemo(t *testing.T) {
	t.Parallel()

	inner := &barrierInnerStream{events: barrierThinkerPrefix(), tail: errBarrierTail}
	rig := newBarrierRig(t, "hidden", inner)
	rig.turn.beforeObserve.arm()

	recvDone := make(chan recvResult, 1)
	go func() { recvDone <- rig.drive(context.Background()) }()
	awaitParked(t, "the admitted Observe barrier", rig.turn.beforeObserve)

	cancelDone := make(chan aipapi.CancelResult, 1)
	go func() {
		cancelDone <- rig.stream.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	}()
	rig.awaitWithdrawal(t, "Cancel")
	if got := rig.turn.finalizeCalls.Load(); got != 0 {
		t.Fatalf("Finalize calls while Observe was admitted = %d, want 0", got)
	}

	rig.turn.beforeObserve.releaseAll()
	recv := awaitClosed(t, "the withdrawn Recv", recvDone)
	res := awaitClosed(t, "the joined Cancel", cancelDone)
	if res.Err != nil {
		t.Fatalf("Cancel after a joined withdrawal reported %v, want nil", res.Err)
	}
	if recv.err == nil {
		t.Fatal("Recv must end with the withdrawal reason")
	}
	if got := rig.store.overlapping.Load(); got != 0 {
		t.Fatalf("durable memo Puts overlapping an admitted Observe = %d, want 0", got)
	}
	if got := rig.turn.finalizeCalls.Load(); got != 1 {
		t.Fatalf("Finalize calls = %d, want exactly 1", got)
	}
	saved := rig.store.snapshots()
	if len(saved) != 1 || saved[0].Memo != barrierAnswer || !saved[0].StreamInterrupted {
		t.Fatalf("interrupted capture must persist the admitted prefix once, got %+v", saved)
	}
	rig.assertNoExecutor(t, "cancel during admitted Observe")
}

// TestInterleavedThinkerAdmission_PostRecorderBarrierSuppressesLateReasoning proves
// the post-call fence: once the real recorder has mutated, a withdrawal that
// arrives before Observe returns still suppresses the returned reasoning.
func TestInterleavedThinkerAdmission_PostRecorderBarrierSuppressesLateReasoning(t *testing.T) {
	t.Parallel()

	inner := &barrierInnerStream{events: barrierThinkerPrefix(), tail: errBarrierTail}
	rig := newBarrierRig(t, "visible", inner)
	rig.turn.afterObserve.arm()

	recvDone := make(chan recvResult, 1)
	go func() { recvDone <- rig.drive(context.Background()) }()
	awaitParked(t, "the post-recorder barrier", rig.turn.afterObserve)

	closeDone := make(chan error, 1)
	go func() { closeDone <- rig.stream.Close() }()
	rig.awaitWithdrawal(t, "Close")
	if got := rig.turn.finalizeCalls.Load(); got != 0 {
		t.Fatalf("Finalize calls while the recorder-mutated callback was still admitted = %d, want 0", got)
	}

	rig.turn.afterObserve.releaseAll()
	recv := awaitClosed(t, "the withdrawn Recv", recvDone)
	// A Close that joined the admitted callback has nothing left to report.
	_ = awaitClosed(t, "the joined Close", closeDone)

	if got := reasoningText(recv.events); got != "" {
		t.Fatalf("a post-recorder withdrawal must release no late reasoning, got %q", got)
	}
	if got := countKind(recv.events, aipapi.EventResponseStarted); got != 0 {
		t.Fatalf("response start events after withdrawal = %d, want 0", got)
	}
	if got := rig.store.overlapping.Load(); got != 0 {
		t.Fatalf("durable memo Puts overlapping an admitted Observe = %d, want 0", got)
	}
	if got := rig.turn.finalizeCalls.Load(); got != 1 {
		t.Fatalf("Finalize calls = %d, want exactly 1 interrupted capture", got)
	}
	saved := rig.store.snapshots()
	if len(saved) != 1 || saved[0].Memo != barrierAnswer || !saved[0].StreamInterrupted {
		t.Fatalf("interrupted capture must persist the admitted prefix once, got %+v", saved)
	}
	rig.assertNoExecutor(t, "post-recorder withdrawal")
}

// TestInterleavedThinkerAdmission_FlushBarrierWithdrawalCapturesOnce proves the
// EOF sanitizer flush shares the exclusive slot with Observe and Finalize, so a
// withdrawal during the flush joins it and finalizes exactly once.
func TestInterleavedThinkerAdmission_FlushBarrierWithdrawalCapturesOnce(t *testing.T) {
	t.Parallel()

	// The prefix stops inside the memo open tag, so the real sanitizer buffers
	// it and only the EOF flush can surface it. Nothing visible is released
	// before withdrawal, which makes "no reasoning after withdrawal" meaningful.
	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: interleavedthinking.MemoOpenTag[:len(interleavedthinking.MemoOpenTag)-2]},
	}
	rig := newBarrierRig(t, "visible", &barrierInnerStream{events: events, tail: io.EOF})
	rig.turn.beforeFlush.arm()

	recvDone := make(chan recvResult, 1)
	go func() { recvDone <- rig.drive(context.Background()) }()
	awaitParked(t, "the admitted FlushVisible barrier", rig.turn.beforeFlush)

	closeDone := make(chan error, 1)
	go func() { closeDone <- rig.stream.Close() }()
	rig.awaitWithdrawal(t, "Close")
	if got := rig.turn.finalizeCalls.Load(); got != 0 {
		t.Fatalf("Finalize calls while the flush was admitted = %d, want 0", got)
	}
	if got := rig.store.puts.Load(); got != 0 {
		t.Fatalf("durable memo Puts while the flush was admitted = %d, want 0", got)
	}

	rig.turn.beforeFlush.releaseAll()
	recv := awaitClosed(t, "the withdrawn Recv", recvDone)
	// A Close that joined the admitted flush has nothing left to report.
	_ = awaitClosed(t, "the joined Close", closeDone)

	if got := rig.store.overlapping.Load(); got != 0 {
		t.Fatalf("durable memo Puts overlapping admitted processor work = %d, want 0", got)
	}
	if got := rig.turn.finalizeCalls.Load(); got != 1 {
		t.Fatalf("Finalize calls = %d, want exactly 1 interrupted capture", got)
	}
	saved := rig.store.snapshots()
	if len(saved) != 1 {
		t.Fatalf("persisted memo snapshots = %d, want exactly 1", len(saved))
	}
	if !saved[0].StreamInterrupted {
		t.Fatal("an interrupted capture must be persisted with StreamInterrupted=true")
	}
	if got := reasoningText(recv.events); got != "" {
		t.Fatalf("no reasoning may be released after a withdrawal during the flush, got %q", got)
	}
	rig.assertNoExecutor(t, "withdrawal during flush")
}

// TestInterleavedThinkerAdmission_JoinTimeoutLeavesTheInterruptedMemoForTheRunningRecv
// proves the conservative budget boundary: when an admitted context-free
// callback outlives the established detached cleanup budget, the withdrawal
// returns without finalizing concurrently, leaves the interrupted memo owed,
// and the still-running Recv claims it exactly once when the callback finally
// returns.
func TestInterleavedThinkerAdmission_JoinTimeoutLeavesTheInterruptedMemoForTheRunningRecv(t *testing.T) {
	t.Parallel()

	inner := &barrierInnerStream{events: barrierThinkerPrefix(), tail: errBarrierTail}
	rig := newBarrierRig(t, "hidden", inner)
	rig.turn.beforeObserve.arm()

	recvDone := make(chan recvResult, 1)
	go func() { recvDone <- rig.drive(context.Background()) }()
	awaitParked(t, "the admitted Observe barrier", rig.turn.beforeObserve)

	// This Close deliberately waits the real 5s cancelLosersTimeout budget,
	// coordinated by the callback barrier rather than by a sleep.
	closeDone := make(chan error, 1)
	go func() { closeDone <- rig.stream.Close() }()
	closeErr := awaitClosed(t, "the budget-exhausted Close", closeDone)
	if closeErr == nil || !errors.Is(closeErr, context.DeadlineExceeded) {
		t.Fatalf("budget-exhausted Close error = %v, want a joined cleanup timeout", closeErr)
	}
	if got := rig.turn.finalizeCalls.Load(); got != 0 {
		t.Fatalf("Finalize calls after the join budget expired = %d, want 0 (no concurrent Finalize)", got)
	}
	if got := rig.store.puts.Load(); got != 0 {
		t.Fatalf("durable memo Puts after the join budget expired = %d, want 0", got)
	}

	rig.turn.beforeObserve.releaseAll()
	recv := awaitClosed(t, "the late Recv", recvDone)
	if recv.err == nil {
		t.Fatal("the late Recv must end with the withdrawal reason")
	}

	if got := rig.store.overlapping.Load(); got != 0 {
		t.Fatalf("durable memo Puts overlapping an admitted Observe = %d, want 0", got)
	}
	if got := rig.turn.finalizeCalls.Load(); got != 1 {
		t.Fatalf("Finalize calls = %d, want exactly 1 deferred interrupted capture", got)
	}
	saved := rig.store.snapshots()
	if len(saved) != 1 || saved[0].Memo != barrierAnswer || !saved[0].StreamInterrupted {
		t.Fatalf("the late callback must persist one consistent interrupted memo, got %+v", saved)
	}
	rig.assertNoExecutor(t, "join budget exhaustion")
}

// TestInterleavedThinkerAdmission_JoinHelperHonorsAnExplicitShortDeadline pins the
// private join boundary itself: an explicit short deadline bounds the wait, the
// admitted work keeps the slot, and no Finalize runs meanwhile.
func TestInterleavedThinkerAdmission_JoinHelperHonorsAnExplicitShortDeadline(t *testing.T) {
	t.Parallel()

	inner := &barrierInnerStream{events: barrierThinkerPrefix(), tail: errBarrierTail}
	rig := newBarrierRig(t, "hidden", inner)
	rig.turn.beforeObserve.arm()

	recvDone := make(chan recvResult, 1)
	go func() { recvDone <- rig.drive(context.Background()) }()
	awaitParked(t, "the admitted Observe barrier", rig.turn.beforeObserve)

	joinDone := make(chan error, 1)
	go func() {
		// The join helper takes an explicit context, so this case supplies a
		// deliberately short one instead of changing any production budget.
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		joinDone <- rig.stream.joinThinkerProcessor(ctx)
	}()
	if err := awaitClosed(t, "the bounded join", joinDone); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded join error = %v, want context.DeadlineExceeded", err)
	}
	if got := rig.turn.finalizeCalls.Load(); got != 0 {
		t.Fatalf("Finalize calls after a bounded join timeout = %d, want 0", got)
	}

	rig.turn.beforeObserve.releaseAll()
	awaitClosed(t, "the released Recv", recvDone)
}

// TestInterleavedThinkerAdmission_SimultaneousCancelAndCloseCaptureOneInterruptedMemo
// races both withdrawal owners against one admitted callback. Admission and the
// memo claim are exclusive, so exactly one interrupted capture may persist.
func TestInterleavedThinkerAdmission_SimultaneousCancelAndCloseCaptureOneInterruptedMemo(t *testing.T) {
	t.Parallel()

	inner := &barrierInnerStream{events: barrierThinkerPrefix(), tail: errBarrierTail}
	rig := newBarrierRig(t, "hidden", inner)
	rig.turn.beforeObserve.arm()

	recvDone := make(chan recvResult, 1)
	go func() { recvDone <- rig.drive(context.Background()) }()
	awaitParked(t, "the admitted Observe barrier", rig.turn.beforeObserve)

	cancelDone := make(chan aipapi.CancelResult, 1)
	closeDone := make(chan error, 1)
	start := make(chan struct{})
	go func() {
		<-start
		cancelDone <- rig.stream.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	}()
	go func() {
		<-start
		closeDone <- rig.stream.Close()
	}()
	close(start)
	// Both owners must be joined on the still-held callback before it is
	// released, so the case proves they race only for the admission claim.
	rig.awaitWithdrawal(t, "simultaneous withdrawal")
	if got := rig.turn.finalizeCalls.Load(); got != 0 {
		t.Fatalf("Finalize calls while Observe was admitted = %d, want 0", got)
	}
	rig.turn.beforeObserve.releaseAll()

	_ = awaitClosed(t, "the simultaneous Cancel", cancelDone)
	_ = awaitClosed(t, "the simultaneous Close", closeDone)
	recv := awaitClosed(t, "the withdrawn Recv", recvDone)
	if recv.err == nil {
		t.Fatal("Recv must end with the withdrawal reason")
	}

	if got := rig.store.overlapping.Load(); got != 0 {
		t.Fatalf("durable memo Puts overlapping an admitted Observe = %d, want 0", got)
	}
	if got := rig.turn.finalizeCalls.Load(); got != 1 {
		t.Fatalf("Finalize calls = %d, want exactly 1 under simultaneous withdrawal", got)
	}
	saved := rig.store.snapshots()
	if len(saved) != 1 || saved[0].Memo != barrierAnswer || !saved[0].StreamInterrupted {
		t.Fatalf("simultaneous withdrawal must persist one consistent interrupted memo, got %+v", saved)
	}
	rig.assertNoExecutor(t, "simultaneous cancellation and close")
}

// barrierLiveWithdrawal names the two real withdrawal sources that never call
// wrapper Cancel/Close, so the live output fence must still see them.
type barrierLiveWithdrawal struct {
	name string
	// apply performs the real withdrawal through an existing public port and
	// must fail the case if it did not take effect.
	apply func(t *testing.T, rig *barrierRig, caller context.CancelFunc)
}

// cancelRigCaller cancels the original Recv caller context of the rig.
func cancelRigCaller(rig *barrierRig, caller context.CancelFunc) {
	caller()
}

// cancelRigSharedALeg cancels the rig's real shared A-leg through the existing
// terminal lifecycle port. It is the same port wrapper Cancel/Close use.
func cancelRigSharedALeg(t *testing.T, rig *barrierRig, _ context.CancelFunc) {
	t.Helper()
	if err := rig.from.terminal.cancelALeg(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit}); err != nil {
		t.Fatalf("shared A-leg cancellation: %v", err)
	}
}

// assertNoLiveLeak asserts the client received no frame at all after a live
// withdrawal won, and that no lifecycle frame slipped out either.
func assertNoLiveLeak(t *testing.T, detail string, recv recvResult) {
	t.Helper()
	if got := countKind(recv.events, aipapi.EventResponseStarted); got != 0 {
		t.Fatalf("%s: response_started frames after withdrawal = %d, want 0", detail, got)
	}
	if got := countKind(recv.events, aipapi.EventMessageStarted); got != 0 {
		t.Fatalf("%s: message_started frames after withdrawal = %d, want 0", detail, got)
	}
	if got := countKind(recv.events, aipapi.EventReasoningDelta); got != 0 {
		t.Fatalf("%s: reasoning frames after withdrawal = %d, want 0", detail, got)
	}
	if got := assistantText(recv.events); got != "" {
		t.Fatalf("%s: assistant text after withdrawal = %q, want none", detail, got)
	}
	if recv.err == nil {
		t.Fatalf("%s: Recv must end with a withdrawal error, not a clean frame", detail)
	}
}

// TestInterleavedThinkerAdmission_LiveWithdrawalAfterAdmittedObserveReleasesNoOutput
// is the permanent regression for the reproduced leak: a withdrawal that wins
// while the admitted, context-free visible callback is still running - through
// the original caller context or the real shared A-leg, never wrapper
// Cancel/Close - must release no lifecycle frame and no late reasoning, and must
// still persist exactly one interrupted private memo.
func TestInterleavedThinkerAdmission_LiveWithdrawalAfterAdmittedObserveReleasesNoOutput(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"visible", "hidden"} {
		for _, w := range []barrierLiveWithdrawal{
			{name: "original caller", apply: func(t *testing.T, rig *barrierRig, caller context.CancelFunc) {
				t.Helper()
				cancelRigCaller(rig, caller)
			}},
			{name: "shared a-leg", apply: cancelRigSharedALeg},
		} {
			t.Run(mode+"/"+w.name, func(t *testing.T) {
				t.Parallel()

				ctx, caller := context.WithCancel(context.Background())
				defer caller()
				inner := &barrierInnerStream{events: barrierThinkerPrefix(), tail: errBarrierTail}
				rig := newBarrierRig(t, mode, inner)
				rig.installSteeringCapture(t)
				rig.turn.afterObserve.arm()

				recvDone := make(chan recvResult, 1)
				go func() { recvDone <- rig.drive(ctx) }()
				awaitParked(t, "the admitted post-recorder callback", rig.turn.afterObserve)

				// The wrapper's own sticky flags are deliberately untouched, so a
				// fence that only reads them cannot pass this case.
				if sticky := rig.stream.withdrawalError(); sticky != nil {
					t.Fatalf("live withdrawal must not set wrapper flags, got %v", sticky)
				}
				w.apply(t, rig, caller)
				switch w.name {
				case "original caller":
					if ctx.Err() == nil {
						t.Fatal("caller cancellation did not take effect")
					}
					if err := rig.stream.sharedCancellationErr(); err != nil {
						t.Fatalf("caller cancellation must not cancel the shared A-leg, got %v", err)
					}
				case "shared a-leg":
					if err := rig.stream.sharedCancellationErr(); err == nil {
						t.Fatal("shared A-leg cancellation did not take effect")
					}
				}

				rig.turn.afterObserve.releaseAll()
				recv := awaitClosed(t, "the withdrawn Recv", recvDone)

				assertNoLiveLeak(t, mode+" live withdrawal during admitted Observe", recv)
				if got := rig.turn.finalizeCalls.Load(); got != 1 {
					t.Fatalf("Finalize calls = %d, want exactly 1 interrupted capture", got)
				}
				if got := rig.turn.finalizeInterup.Load(); got != 1 {
					t.Fatalf("interrupted Finalize calls = %d, want 1", got)
				}
				saved := rig.store.snapshots()
				if len(saved) != 1 || saved[0].Memo != barrierAnswer || !saved[0].StreamInterrupted {
					t.Fatalf("live withdrawal must persist one interrupted admitted prefix, got %+v", saved)
				}
				if got := rig.memoSteering.puts.Load(); got != 1 {
					t.Fatalf("steering overlay Puts = %d, want 1 for the admitted interrupted capture", got)
				}
				if got := rig.execOpens.Load(); got != 0 {
					t.Fatalf("continuation backend opens = %d, want 0 after live withdrawal", got)
				}
			})
		}
	}
}

// TestInterleavedThinkerAdmission_LiveWithdrawalAfterAdmittedFlushReleasesNoOutput
// covers the same two live sources at the EOF sanitizer flush, which shares the
// exclusive slot with Observe and Finalize.
func TestInterleavedThinkerAdmission_LiveWithdrawalAfterAdmittedFlushReleasesNoOutput(t *testing.T) {
	t.Parallel()

	for _, w := range []barrierLiveWithdrawal{
		{name: "original caller", apply: func(t *testing.T, rig *barrierRig, caller context.CancelFunc) {
			t.Helper()
			cancelRigCaller(rig, caller)
		}},
		{name: "shared a-leg", apply: cancelRigSharedALeg},
	} {
		t.Run(w.name, func(t *testing.T) {
			t.Parallel()

			ctx, caller := context.WithCancel(context.Background())
			defer caller()
			// The prefix stops inside the memo open tag, so only the EOF flush can
			// surface the body; anything the client sees after withdrawal is a leak.
			events := []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: interleavedthinking.MemoOpenTag[:len(interleavedthinking.MemoOpenTag)-2]},
			}
			rig := newBarrierRig(t, "visible", &barrierInnerStream{events: events, tail: io.EOF})
			rig.turn.beforeFlush.arm()

			recvDone := make(chan recvResult, 1)
			go func() { recvDone <- rig.drive(ctx) }()
			awaitParked(t, "the admitted flush barrier", rig.turn.beforeFlush)

			if sticky := rig.stream.withdrawalError(); sticky != nil {
				t.Fatalf("live withdrawal must not set wrapper flags, got %v", sticky)
			}
			w.apply(t, rig, caller)

			rig.turn.beforeFlush.releaseAll()
			recv := awaitClosed(t, "the withdrawn Recv", recvDone)

			assertNoLiveLeak(t, "live withdrawal during admitted flush", recv)
			if got := rig.turn.finalizeCalls.Load(); got != 1 {
				t.Fatalf("Finalize calls = %d, want exactly 1 interrupted capture", got)
			}
			if got := rig.turn.finalizeInterup.Load(); got != 1 {
				t.Fatalf("interrupted Finalize calls = %d, want 1", got)
			}
			saved := rig.store.snapshots()
			if len(saved) != 1 || !saved[0].StreamInterrupted {
				t.Fatalf("live withdrawal must persist one interrupted memo, got %+v", saved)
			}
			if got := rig.execOpens.Load(); got != 0 {
				t.Fatalf("continuation backend opens = %d, want 0 after live withdrawal", got)
			}
		})
	}
}

// TestInterleavedThinkerAdmission_LiveWithdrawalBetweenQueuedLifecycleAndReasoning
// forces the physical queue-release seam, not just the callback seam: the
// lifecycle frames are admitted first, the live withdrawal lands, and only then
// is the reasoning frame offered to the client. A fix that only re-checks after
// Observe cannot pass, because Observe returned before the withdrawal.
func TestInterleavedThinkerAdmission_LiveWithdrawalBetweenQueuedLifecycleAndReasoning(t *testing.T) {
	t.Parallel()

	for _, w := range []barrierLiveWithdrawal{
		{name: "original caller", apply: func(t *testing.T, rig *barrierRig, caller context.CancelFunc) {
			t.Helper()
			cancelRigCaller(rig, caller)
		}},
		{name: "shared a-leg", apply: cancelRigSharedALeg},
	} {
		t.Run(w.name, func(t *testing.T) {
			t.Parallel()

			ctx, caller := context.WithCancel(context.Background())
			defer caller()
			rig := newBarrierRig(t, "visible", &barrierInnerStream{events: barrierThinkerPrefix(), tail: errBarrierTail})

			// Admit the real reasoning frame and its lifecycle prefix exactly as the
			// wrapper does, then hold delivery so the withdrawal lands between
			// queued lifecycle and the reasoning frame. This fixture step is
			// signature-independent, so the case exercises only the physical
			// queue-release seam rather than a private enqueue signature.
			rig.stream.mu.Lock()
			if !rig.stream.responseStarted {
				rig.stream.pending = append(
					rig.stream.pending,
					aipapi.Event{Kind: aipapi.EventResponseStarted},
					aipapi.Event{Kind: aipapi.EventMessageStarted},
				)
				rig.stream.responseStarted = true
			}
			rig.stream.pending = append(rig.stream.pending, aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: barrierAnswer})
			queued := len(rig.stream.pending)
			rig.stream.mu.Unlock()
			if queued != 3 {
				t.Fatalf("queued frames after one enqueue = %d, want lifecycle prefix plus reasoning", queued)
			}

			if sticky := rig.stream.withdrawalError(); sticky != nil {
				t.Fatalf("live withdrawal must not set wrapper flags, got %v", sticky)
			}
			w.apply(t, rig, caller)

			// The real wrapper Recv must refuse to release the queued frames.
			recvDone := make(chan recvResult, 1)
			go func() { recvDone <- rig.drive(ctx) }()
			recv := awaitClosed(t, "the withdrawn Recv", recvDone)

			assertNoLiveLeak(t, "live withdrawal between queued lifecycle and reasoning", recv)
			if got := rig.turn.finalizeCalls.Load(); got != 1 {
				t.Fatalf("Finalize calls = %d, want exactly 1 interrupted capture", got)
			}
			if got := rig.turn.finalizeInterup.Load(); got != 1 {
				t.Fatalf("interrupted Finalize calls = %d, want 1", got)
			}
			if got := rig.execOpens.Load(); got != 0 {
				t.Fatalf("continuation backend opens = %d, want 0 after live withdrawal", got)
			}
		})
	}
}

// TestInterleavedThinkerAdmission_LiveWithdrawalFencesExecutorAnswer is the
// executor-side seam: an answer frame the client withdrew against while the
// executor receive was blocked must not be handed out.
func TestInterleavedThinkerAdmission_LiveWithdrawalFencesExecutorAnswer(t *testing.T) {
	t.Parallel()

	ctx, caller := context.WithCancel(context.Background())
	defer caller()
	events := append(barrierThinkerPrefix(), aipapi.Event{Kind: aipapi.EventResponseFinished})
	rig := newBarrierRig(t, "hidden", &barrierInnerStream{events: events})

	// Block the executor leg after the memo is captured and the continuation is
	// open, so the withdrawal lands while an answer receive is in flight. Only the
	// real backend Open changes; the processor, store, and memo path stay real.
	answerGate := newParkGate()
	base := rig.ex.Backends["backend-1"]
	base.Open = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		rig.execOpens.Add(1)
		answer := &barrierInnerStream{
			events:   []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: barrierExecutorAnswer}},
			recvGate: answerGate,
		}
		rig.execAnswer = answer
		return answer, nil
	}
	rig.ex.Backends["backend-1"] = base
	answerGate.arm()

	recvDone := make(chan recvResult, 1)
	go func() { recvDone <- rig.drive(ctx) }()
	awaitParked(t, "the blocked executor answer receive", answerGate)

	caller()
	if ctx.Err() == nil {
		t.Fatal("caller cancellation did not take effect")
	}
	answerGate.releaseAll()
	recv := awaitClosed(t, "the withdrawn Recv", recvDone)

	if got := assistantText(recv.events); got != "" {
		t.Fatalf("a withdrawn executor answer must not reach the client, got %q", got)
	}
	if recv.err == nil {
		t.Fatal("Recv must end with a withdrawal error")
	}
}

// TestInterleavedThinkerAdmission_WithdrawalDuringAdmittedNormalFinalizeSuppressesContinuation
// proves that a normal Finalize already admitted before withdrawal consumes that
// withdrawal's deferred request. No second, relabeling Finalize runs, and the
// continuation is never opened, even though the durable memo store write already
// committed inside the admitted call.
func TestInterleavedThinkerAdmission_WithdrawalDuringAdmittedNormalFinalizeSuppressesContinuation(t *testing.T) {
	t.Parallel()

	events := append(barrierThinkerPrefix(), aipapi.Event{Kind: aipapi.EventResponseFinished})
	rig := newBarrierRig(t, "hidden", &barrierInnerStream{events: events})
	rig.installSteeringCapture(t)
	rig.store.blockNextPut()

	recvDone := make(chan recvResult, 1)
	go func() { recvDone <- rig.drive(context.Background()) }()
	select {
	case <-rig.store.putEntered:
	case <-time.After(barrierGuard):
		t.Fatal("timed out waiting for the normal memo Put")
	}
	if got := rig.turn.finalizeInterup.Load(); got != 0 {
		t.Fatalf("interrupted Finalize calls before withdrawal = %d, want 0", got)
	}

	cancelDone := make(chan aipapi.CancelResult, 1)
	go func() {
		cancelDone <- rig.stream.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	}()
	// The transition owner closes admission at Cancel entry, so the case waits
	// for that flag instead of racing it.
	rig.awaitWithdrawal(t, "the transition Cancel")
	// While the admitted normal Finalize is still parked, no interrupted capture
	// may run alongside it.
	if got := rig.turn.finalizeCalls.Load(); got != 1 {
		t.Fatalf("Finalize calls during the admitted normal capture = %d, want exactly 1", got)
	}

	rig.store.releasePut()
	recv := awaitClosed(t, "the suppressed Recv", recvDone)
	awaitClosed(t, "the transition Cancel", cancelDone)

	if recv.err == nil {
		t.Fatal("Recv must fail once withdrawal suppresses the continuation")
	}
	if got := rig.turn.finalizeCalls.Load(); got != 1 {
		t.Fatalf("Finalize calls = %d, want exactly 1 (the withdrawal request was consumed by the admitted capture)", got)
	}
	if got := rig.turn.finalizeInterup.Load(); got != 0 {
		t.Fatalf("a second, relabeling interrupted Finalize ran %d times, want 0", got)
	}
	saved := rig.store.snapshots()
	if len(saved) != 1 {
		t.Fatalf("persisted memo snapshots = %d, want exactly 1", len(saved))
	}
	if saved[0].StreamInterrupted {
		t.Fatal("an already-admitted normal capture must not be relabeled interrupted")
	}
	// The withdrawal won while the normal Finalize was parked, so no NEW normal
	// steering overlay may start after it. The already-admitted store write above
	// is a private effect this wrapper does not and cannot roll back.
	if got := rig.memoSteering.puts.Load(); got != 0 {
		t.Fatalf("normal steering overlay Puts admitted after withdrawal won = %d, want 0", got)
	}
	rig.assertNoExecutor(t, "withdrawal during an admitted normal Finalize")
}

// TestInterleavedThinkerAdmission_AdmittedNormalOverlayCompletesWithoutExecutorAdmission
// pins the other half of the overlay boundary: an overlay that was already
// admitted may finish its real store mutation, but the withdrawal that arrives
// while it runs still stops every later effect, so no continuation is opened.
func TestInterleavedThinkerAdmission_AdmittedNormalOverlayCompletesWithoutExecutorAdmission(t *testing.T) {
	t.Parallel()

	events := append(barrierThinkerPrefix(), aipapi.Event{Kind: aipapi.EventResponseFinished})
	rig := newBarrierRig(t, "hidden", &barrierInnerStream{events: events})
	rig.installSteeringCapture(t)
	rig.memoSteering.gate.arm()

	recvDone := make(chan recvResult, 1)
	go func() { recvDone <- rig.drive(context.Background()) }()
	awaitParked(t, "the admitted normal steering overlay", rig.memoSteering.gate)

	cancelDone := make(chan aipapi.CancelResult, 1)
	go func() {
		cancelDone <- rig.stream.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	}()
	rig.awaitWithdrawal(t, "the transition Cancel")
	rig.memoSteering.gate.releaseAll()

	recv := awaitClosed(t, "the suppressed Recv", recvDone)
	_ = awaitClosed(t, "the transition Cancel", cancelDone)

	if recv.err == nil {
		t.Fatal("Recv must fail once withdrawal suppresses the continuation")
	}
	if got := rig.memoSteering.puts.Load(); got != 1 {
		t.Fatalf("steering overlay Puts = %d, want exactly 1 already-admitted overlay", got)
	}
	reqs, errs := rig.memoSteering.recorded()
	if len(reqs) != 1 {
		t.Fatalf("recorded steering overlay requests = %d, want 1", len(reqs))
	}
	if errs[0] != nil {
		t.Fatalf("the already-admitted overlay must still reach the real store, got %v", errs[0])
	}
	if reqs[0].OverlayID != steering.OverlayID(interleavedthinking.MemoOverlayID) {
		t.Fatalf("overlay id = %q, want the real memo overlay id", reqs[0].OverlayID)
	}
	// The already-admitted overlay is not rolled back; nothing after it may run.
	if got := rig.turn.finalizeCalls.Load(); got != 1 {
		t.Fatalf("Finalize calls = %d, want exactly 1", got)
	}
	if got := rig.turn.finalizeInterup.Load(); got != 0 {
		t.Fatalf("an admitted normal capture must not be relabeled interrupted, got %d", got)
	}
	rig.assertNoExecutor(t, "withdrawal during an admitted normal overlay")
}

// TestInterleavedThinkerAdmission_ConsumedInterruptedMemoIsNotCapturedAgain proves
// a withdrawal's interrupted Finalize request stays consumed. A Close that
// already completed one interrupted Finalize - empty or failed - must not let
// the released inner receive trigger a second Finalize and a second durable Put
// attempt. It runs with duplicate withdrawal during terminal cleanup.
func TestInterleavedThinkerAdmission_ConsumedInterruptedMemoIsNotCapturedAgain(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		events []aipapi.Event
		fail   bool
	}{
		{name: "empty memo", events: barrierThinkerNoMemoPrefix(), fail: false},
		{name: "real store error", events: barrierThinkerPrefix(), fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			recvGate := newParkGate()
			recvGate.arm()
			inner := &barrierInnerStream{events: tc.events, tail: errBarrierTail, recvGate: recvGate}
			rig := newBarrierRig(t, "hidden", inner)
			rig.turn.beforeFinal.arm()
			if tc.fail {
				rig.store.failPuts(errBarrierStoreFailure)
			}

			recvDone := make(chan recvResult, 1)
			go func() { recvDone <- rig.drive(context.Background()) }()
			// The wrapper is inside the inner receive with the processor slot
			// idle: no Observe, Flush, or Finalize is admitted.
			awaitParked(t, "the blocked inner receive", recvGate)

			closeDone := make(chan error, 1)
			go func() { closeDone <- rig.stream.Close() }()
			// Close owns the only genuine interrupted request, and the real
			// Finalize is held inside it so a duplicate withdrawal can arrive
			// during that terminal cleanup.
			awaitParked(t, "the admitted interrupted Finalize", rig.turn.beforeFinal)

			cancelDone := make(chan aipapi.CancelResult, 1)
			go func() {
				cancelDone <- rig.stream.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
			}()
			rig.awaitWithdrawal(t, "the duplicate Cancel")

			rig.turn.beforeFinal.releaseAll()
			_ = awaitClosed(t, "the joined Close", closeDone)
			_ = awaitClosed(t, "the duplicate Cancel", cancelDone)

			if got := rig.turn.finalizeCalls.Load(); got != 1 {
				t.Fatalf("Finalize calls before the inner receive resumed = %d, want exactly 1 interrupted capture", got)
			}
			if got := rig.turn.finalizeInterup.Load(); got != 1 {
				t.Fatalf("interrupted Finalize calls = %d, want 1", got)
			}
			// The attempt counter sees a real store failure, not a silent skip.
			if got := rig.store.puts.Load(); got != 1 {
				t.Fatalf("durable memo Put attempts = %d, want exactly 1", got)
			}

			// Releasing the blocked inner receive now lets the real wrapper see
			// the withdrawal error on its next Recv.
			recvGate.releaseAll()
			recv := awaitClosed(t, "the released Recv", recvDone)
			if recv.err == nil {
				t.Fatal("Recv must end with the withdrawal reason")
			}

			if got := rig.turn.finalizeCalls.Load(); got != 1 {
				t.Fatalf("Finalize calls after the inner receive resumed = %d, want exactly 1 (the consumed request must stay consumed)", got)
			}
			if got := rig.turn.finalizeInterup.Load(); got != 1 {
				t.Fatalf("interrupted Finalize calls = %d, want 1 (no relabeled second capture)", got)
			}
			if got := rig.store.puts.Load(); got != 1 {
				t.Fatalf("durable memo Put attempts = %d, want exactly 1", got)
			}
			rig.assertNoExecutor(t, "a consumed interrupted memo")
		})
	}
}

// TestInterleavedThinkerAdmission_FailedAdmittedNormalFinalizeIsNotRelabeled proves
// an already-admitted normal Finalize that fails against the real store consumes
// the withdrawal's request: no interrupted Finalize, no second Put attempt, and
// no continuation may follow it.
func TestInterleavedThinkerAdmission_FailedAdmittedNormalFinalizeIsNotRelabeled(t *testing.T) {
	t.Parallel()

	events := append(barrierThinkerPrefix(), aipapi.Event{Kind: aipapi.EventResponseFinished})
	rig := newBarrierRig(t, "hidden", &barrierInnerStream{events: events})
	rig.installSteeringCapture(t)
	rig.store.failPuts(errBarrierStoreFailure)
	rig.store.blockNextPut()

	recvDone := make(chan recvResult, 1)
	go func() { recvDone <- rig.drive(context.Background()) }()
	awaitFired(t, "the admitted normal memo Put", rig.store.putEntered)

	cancelDone := make(chan aipapi.CancelResult, 1)
	go func() {
		cancelDone <- rig.stream.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	}()
	rig.awaitWithdrawal(t, "the transition Cancel")

	rig.store.releasePut()
	recv := awaitClosed(t, "the failed Recv", recvDone)
	_ = awaitClosed(t, "the transition Cancel", cancelDone)
	if recv.err == nil {
		t.Fatal("Recv must surface the real store failure")
	}

	if got := rig.turn.finalizeCalls.Load(); got != 1 {
		t.Fatalf("Finalize calls = %d, want exactly 1", got)
	}
	if got := rig.turn.finalizeInterup.Load(); got != 0 {
		t.Fatalf("a failed normal capture must not be relabeled interrupted, got %d interrupted Finalize calls", got)
	}
	if got := rig.store.puts.Load(); got != 1 {
		t.Fatalf("durable memo Put attempts = %d, want exactly 1", got)
	}
	if got := rig.memoSteering.puts.Load(); got != 0 {
		t.Fatalf("steering overlay Puts = %d, want 0 (a failed normal capture publishes no overlay)", got)
	}
	if saved := rig.store.snapshots(); len(saved) != 0 {
		t.Fatalf("persisted memo snapshots = %d, want 0 for a failing store", len(saved))
	}
	rig.assertNoExecutor(t, "a failed already-admitted normal Finalize")
}

// TestInterleavedThinkerAdmission_WithdrawalDuringAdmittedContinuationOpenAbortsIt
// proves the post-open abort boundary: once the continuation open is already
// admitted, withdrawal still disposes the opened stream, and the client never
// sees a normal handoff.
func TestInterleavedThinkerAdmission_WithdrawalDuringAdmittedContinuationOpenAbortsIt(t *testing.T) {
	t.Parallel()

	events := append(barrierThinkerPrefix(), aipapi.Event{Kind: aipapi.EventResponseFinished})
	rig := newBarrierRig(t, "hidden", &barrierInnerStream{events: events})
	rig.execOpenGate.arm()

	recvDone := make(chan recvResult, 1)
	go func() { recvDone <- rig.drive(context.Background()) }()
	awaitParked(t, "the admitted continuation open", rig.execOpenGate)

	closeDone := make(chan error, 1)
	go func() { closeDone <- rig.stream.Close() }()
	rig.awaitWithdrawal(t, "Close")
	rig.execOpenGate.releaseAll()
	recv := awaitClosed(t, "the aborted Recv", recvDone)
	_ = awaitClosed(t, "the competing Close", closeDone)

	if recv.err == nil {
		t.Fatal("Recv must fail after the post-open abort")
	}
	if got := assistantText(recv.events); got != "" {
		t.Fatalf("no normal handoff may reach the client after withdrawal, got %q", got)
	}
	if rig.execAnswer == nil {
		t.Fatal("the admitted continuation open must still be disposed by the existing abort owner")
	}
	rig.stream.mu.Lock()
	executor := rig.stream.executor
	rig.stream.mu.Unlock()
	if executor != nil {
		t.Fatal("an aborted continuation must never be assigned as the active stream")
	}
	if got := rig.turn.finalizeCalls.Load(); got != 1 {
		t.Fatalf("Finalize calls = %d, want exactly 1", got)
	}
}

// TestInterleavedThinkerAdmission_HiddenSuccessCapturesTheMemoOnceThenAnswers proves
// the success path through the real runtime: the memo is captured exactly once
// as a normal, non-interrupted memo, the actual executor answers, and the outer
// stream ends cleanly.
func TestInterleavedThinkerAdmission_HiddenSuccessCapturesTheMemoOnceThenAnswers(t *testing.T) {
	t.Parallel()

	events := append(barrierThinkerPrefix(), aipapi.Event{Kind: aipapi.EventResponseFinished})
	rig := newBarrierRig(t, "hidden", &barrierInnerStream{events: events})

	recv := rig.drive(context.Background())
	if !errors.Is(recv.err, io.EOF) {
		t.Fatalf("hidden success Recv error = %v, want a clean io.EOF", recv.err)
	}
	if got := assistantText(recv.events); got != barrierExecutorAnswer {
		t.Fatalf("client text = %q, want the real executor answer %q", got, barrierExecutorAnswer)
	}
	if got := reasoningText(recv.events); got != "" {
		t.Fatalf("hidden mode must release no reasoning, got %q", got)
	}
	if got := rig.turn.finalizeCalls.Load(); got != 1 {
		t.Fatalf("Finalize calls = %d, want exactly 1", got)
	}
	if got := rig.turn.finalizeInterup.Load(); got != 0 {
		t.Fatalf("interrupted Finalize calls on the success path = %d, want 0", got)
	}
	saved := rig.store.snapshots()
	if len(saved) != 1 {
		t.Fatalf("persisted memo snapshots = %d, want exactly 1", len(saved))
	}
	if saved[0].Memo != barrierAnswer {
		t.Fatalf("persisted memo = %q, want %q", saved[0].Memo, barrierAnswer)
	}
	if saved[0].StreamInterrupted {
		t.Fatal("a successful turn must not persist an interrupted memo")
	}
	if saved[0].VisibleToClient {
		t.Fatal("hidden mode must never mark the memo visible to the client")
	}
	if got := rig.execOpens.Load(); got != 1 {
		t.Fatalf("continuation backend opens = %d, want exactly 1", got)
	}
	if err := rig.stream.Close(); err != nil {
		t.Fatalf("outer Close after a successful turn: %v", err)
	}
}

// TestInterleavedThinkerAdmission_VisibleSuccessCapturesTheMemoOnceThenAnswers is
// the visible-mode success counterpart: sanitized reasoning reaches the client,
// the memo is captured exactly once, and the actual executor answer follows.
func TestInterleavedThinkerAdmission_VisibleSuccessCapturesTheMemoOnceThenAnswers(t *testing.T) {
	t.Parallel()

	events := append(barrierThinkerPrefix(), aipapi.Event{Kind: aipapi.EventResponseFinished})
	rig := newBarrierRig(t, "visible", &barrierInnerStream{events: events})

	recv := rig.drive(context.Background())
	if !errors.Is(recv.err, io.EOF) {
		t.Fatalf("visible success Recv error = %v, want a clean io.EOF", recv.err)
	}
	if got := assistantText(recv.events); got != barrierExecutorAnswer {
		t.Fatalf("client text = %q, want the real executor answer %q", got, barrierExecutorAnswer)
	}
	if got := reasoningText(recv.events); got != barrierAnswer {
		t.Fatalf("visible reasoning = %q, want the sanitized memo body %q", got, barrierAnswer)
	}
	if got := rig.turn.finalizeCalls.Load(); got != 1 {
		t.Fatalf("Finalize calls = %d, want exactly 1", got)
	}
	saved := rig.store.snapshots()
	if len(saved) != 1 {
		t.Fatalf("persisted memo snapshots = %d, want exactly 1", len(saved))
	}
	if saved[0].Memo != barrierAnswer || saved[0].StreamInterrupted {
		t.Fatalf("visible success must persist one non-interrupted memo, got %+v", saved[0])
	}
	if !saved[0].VisibleToClient {
		t.Fatal("a visible turn that already surfaced reasoning must mark the memo visible")
	}
	if got := rig.execOpens.Load(); got != 1 {
		t.Fatalf("continuation backend opens = %d, want exactly 1", got)
	}
}

// lateObserveErrorTurn preserves real feature-owned recorder mutation before
// parking and returning the fallible port's original error.
type lateObserveErrorTurn struct {
	interleavedthinking.Turn
	gate *parkGate
	err  error
}

func (t lateObserveErrorTurn) ObserveThinkerEvent(ev aipapi.Event) ([]aipapi.Event, error) {
	visible, err := t.Turn.ObserveThinkerEvent(ev)
	if err != nil {
		return nil, err
	}
	if contentEvent(ev) {
		t.gate.park()
		return nil, t.err
	}
	return visible, nil
}

func TestInterleavedThinkerAdmission_TimedOutCloseThenObserveErrorConsumesPendingMemo(t *testing.T) {
	t.Parallel()
	rig := newBarrierRig(t, "visible", &barrierInnerStream{events: barrierThinkerPrefix(), tail: errBarrierTail})
	observeErr := errors.New("admitted Observe failed after real recorder mutation")
	adapter, ok := rig.turn.inner.(*testInterleavedTurnAdapter)
	if !ok {
		t.Fatalf("real feature turn adapter = %T, want *testInterleavedTurnAdapter", rig.turn.inner)
	}
	adapter.inner = lateObserveErrorTurn{Turn: adapter.inner, gate: rig.turn.afterObserve, err: observeErr}
	rig.turn.afterObserve.arm()
	defer rig.turn.afterObserve.releaseAll()
	recvDone := make(chan recvResult, 1)
	go func() { recvDone <- rig.drive(context.Background()) }()
	awaitParked(t, "real recorder mutation before Observe error", rig.turn.afterObserve)
	closeDone := make(chan error, 1)
	go func() { closeDone <- rig.stream.Close() }()
	if err := awaitClosed(t, "actual five-second Close timeout", closeDone); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close = %v, want cleanup join deadline", err)
	}
	rig.stream.mu.Lock()
	pending, attempted := rig.stream.interruptedMemoPending, rig.stream.memoFinalizeAttempted
	rig.stream.mu.Unlock()
	if !pending || attempted {
		t.Fatalf("Close must arm an unconsumed memo: pending=%v attempted=%v", pending, attempted)
	}
	if got := rig.turn.finalizeCalls.Load(); got != 0 {
		t.Fatalf("Finalize before slot release = %d, want 0", got)
	}
	rig.turn.afterObserve.releaseAll()
	recv := awaitClosed(t, "late admitted Observe error", recvDone)
	if !errors.Is(recv.err, observeErr) {
		t.Errorf("Recv = %v, want original Observe error %v", recv.err, observeErr)
	}
	assertNoLiveLeak(t, "late failed callback after timed-out Close", recv)
	rig.assertNoExecutor(t, "late Observe error")
	if got := rig.turn.finalizeCalls.Load(); got != 1 {
		t.Errorf("Finalize = %d, want exactly one already-pending capture", got)
	}
	if got := rig.turn.finalizeInterup.Load(); got != 1 {
		t.Errorf("interrupted Finalize = %d, want 1", got)
	}
	rig.stream.mu.Lock()
	pending, attempted = rig.stream.interruptedMemoPending, rig.stream.memoFinalizeAttempted
	rig.stream.mu.Unlock()
	if pending || !attempted {
		t.Errorf("memo claim must stay consumed: pending=%v attempted=%v", pending, attempted)
	}
	saved := rig.store.snapshots()
	if len(saved) != 1 || saved[0].Memo != barrierAnswer || !saved[0].StreamInterrupted {
		t.Errorf("real stored interrupted prefix = %+v, want %q exactly once", saved, barrierAnswer)
	}
	if got := rig.store.overlapping.Load(); got != 0 {
		t.Errorf("store writes overlapping Observe = %d, want 0", got)
	}
	// Duplicate cleanup must not recreate the consumed request.
	_ = rig.stream.Close()
	_ = rig.stream.Close()
	if got := rig.turn.finalizeCalls.Load(); got != 1 {
		t.Errorf("Finalize after duplicate cleanup = %d, want 1", got)
	}
}

func TestInterleavedThinkerAdmission_ObserveErrorWithoutPendingMemoDoesNotArmCapture(t *testing.T) {
	t.Parallel()
	rig := newBarrierRig(t, "visible", &barrierInnerStream{events: barrierThinkerPrefix(), tail: errBarrierTail})
	observeErr := errors.New("Observe error without an armed interrupted memo")
	adapter, ok := rig.turn.inner.(*testInterleavedTurnAdapter)
	if !ok {
		t.Fatalf("real feature turn adapter = %T, want *testInterleavedTurnAdapter", rig.turn.inner)
	}
	adapter.inner = lateObserveErrorTurn{Turn: adapter.inner, gate: rig.turn.afterObserve, err: observeErr}
	// Exercise the admitted Observe boundary directly so existing outer receive
	// error cleanup does not become a new policy assertion.
	visible, err := rig.stream.observeThinkerEvent(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: barrierAnswer})
	if !errors.Is(err, observeErr) {
		t.Fatalf("Observe = %v, want original error", err)
	}
	if len(visible) != 0 {
		t.Fatalf("failed Observe returned visible output: %+v", visible)
	}
	rig.stream.mu.Lock()
	pending, attempted := rig.stream.interruptedMemoPending, rig.stream.memoFinalizeAttempted
	rig.stream.mu.Unlock()
	if pending || attempted {
		t.Fatalf("unrequested capture armed/attempted: pending=%v attempted=%v", pending, attempted)
	}
	if got := rig.turn.finalizeCalls.Load(); got != 0 {
		t.Fatalf("unrequested Finalize = %d, want 0", got)
	}
}
