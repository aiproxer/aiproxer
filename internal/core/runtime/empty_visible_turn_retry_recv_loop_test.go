package runtime

// Receive-loop wiring and provider coexistence for the empty-visible-turn
// corrective retry (spec tasks 6 and 9; requirements 2.3, 2.4, 3.2-3.6, 4.7):
// the bounded prefix hold and its release/discard paths, the finish intercept,
// and the byte-identical wired terminal-decision provider.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/stretchr/testify/require"
)

// emptyVisibleTurnRetryLoopFixture drives the real receive loop with the retry
// gate enabled and the bounded decision records captured as JSON logs.
type emptyVisibleTurnRetryLoopFixture struct {
	executor *Executor
	aLegID   string
	logs     *bytes.Buffer
	stream   *retryRecvStream
	attempt  *attemptSession
}

func newEmptyVisibleTurnRetryLoopFixture(t *testing.T, script []aipapi.Event) *emptyVisibleTurnRetryLoopFixture {
	t.Helper()
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, nil)
	ex.EmptyVisibleTurnRetry = EmptyVisibleTurnRetryPolicy{Enabled: true}
	logs := &bytes.Buffer{}
	ex.Log = slog.New(slog.NewJSONHandler(logs, nil))
	ex.Metrics = &emptyVisibleTurnRetryDecisionSink{}
	base := newEmptyVisibleTurnRetryGateOffFixture(t, ex, aLegID, script)
	return &emptyVisibleTurnRetryLoopFixture{ex, aLegID, logs, base.stream, base.attempt}
}

// newEmptyVisibleTurnRetryComposedStream builds the composed-executor receive
// stream; the caller owns the gate, metrics, log, and backend opener.
func newEmptyVisibleTurnRetryComposedStream(t *testing.T, ex *Executor, aLegID string) *retryRecvStream {
	t.Helper()
	sel, err := routing.Parse("backend-1:model-1")
	require.NoError(t, err)
	bleg, err := ex.Store.NextBLeg(context.Background(), aLegID)
	require.NoError(t, err)
	stream := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{
				ID:         "request-empty-visible-turn-retry",
				Route:      aipapi.RouteIntent{Selector: "backend-1:model-1"},
				Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeStreaming},
				Messages:   testMinimalUserMessages(),
			},
			aLegID:  aLegID,
			traceID: "trace-empty-visible-turn-retry",
		}),
		recovery: &recoveryController{
			budget:   &attemptBudget{max: 3},
			sel:      sel,
			session:  &routing.SessionRoutingState{},
			excluded: map[string]struct{}{},
			rng:      routing.NewSeededRng(1),
		},
		attempt: testAttemptSlot(
			bleg,
			authorityCandidate(),
			testAuthorityLifecycle(ex, attemptAuthorityState{}, authorityCandidate()),
			newAttemptAccountingTracker(time.Unix(1, 0)),
		),
	}
	installTestTurnTerminal(stream)
	bindTestRuntimeOwners(stream, ex)
	return stream
}

// drainObservingHold reads to the end and reports the hold's phase at the
// first release (or at the error when nothing was released). A completed turn
// cleans the hold up, so only that boundary distinguishes a hold that retained
// frames from one that never armed.
func (f *emptyVisibleTurnRetryLoopFixture) drainObservingHold(ctx context.Context, t *testing.T) (released []aipapi.Event, phase correctionHoldState, err error) {
	t.Helper()
	for range 64 {
		var ev aipapi.Event
		ev, err = f.stream.Recv(ctx)
		if err != nil {
			if len(released) == 0 {
				phase = f.attempt.correctionHoldState()
			}
			return released, phase, err
		}
		released = append(released, ev)
		if len(released) == 1 {
			phase = f.attempt.correctionHoldState()
		}
	}
	t.Fatalf("receive loop did not end after 64 events")
	return nil, phase, nil
}

// emptyVisibleTurnRetryLoopDecision is one expected bounded decision record.
type emptyVisibleTurnRetryLoopDecision struct {
	outcome string
	reason  string
}

// assertEmptyVisibleTurnRetryDecisions asserts the bounded records, in order,
// with lineage, and returns them for further checks.
func assertEmptyVisibleTurnRetryDecisions(t *testing.T, logs *bytes.Buffer, want ...emptyVisibleTurnRetryLoopDecision) []map[string]any {
	t.Helper()
	records := emptyVisibleTurnRetryDecisionRecords(t, logs)
	require.Lenf(t, records, len(want), "decision records = %#v, want %v", records, want)
	for i, record := range records {
		reason, _ := record["reason"].(string)
		require.Equalf(t, want[i].outcome, record["outcome"], "decision record %d outcome", i)
		require.Equalf(t, want[i].reason, reason, "decision record %d reason", i)
		require.NotEmptyf(t, record["trace_id"], "decision record %d trace lineage", i)
		require.NotEmptyf(t, record["b_leg_id"], "decision record %d b-leg lineage", i)
	}
	return records
}

// emptyVisibleTurnRetryLoopReasoning builds a reasoning frame whose retained
// payload is exactly size bytes, so the byte bound can be driven to its edge.
func emptyVisibleTurnRetryLoopReasoning(size int) aipapi.Event {
	return aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: strings.Repeat("x", size-len(aipapi.EventReasoningDelta))}
}

// emptyVisibleTurnRetryReadCountStream reports its frames, then firstTail
// exactly once, then laterTail; a case can prove an ended stream is never read
// again.
type emptyVisibleTurnRetryReadCountStream struct {
	frames    []aipapi.Event
	reads     int
	firstTail error
	laterTail error
}

func (s *emptyVisibleTurnRetryReadCountStream) Recv(context.Context) (aipapi.Event, error) {
	s.reads++
	if s.reads <= len(s.frames) {
		return s.frames[s.reads-1], nil
	}
	if s.reads == len(s.frames)+1 {
		return aipapi.Event{}, s.firstTail
	}
	return aipapi.Event{}, s.laterTail
}

func (*emptyVisibleTurnRetryReadCountStream) Close() error { return nil }

func (*emptyVisibleTurnRetryReadCountStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{}
}

// emptyVisibleTurnRetryWithdrawStream withdraws the turn inside the numbered
// backend read, so a withdrawal deterministically races that read's result.
type emptyVisibleTurnRetryWithdrawStream struct {
	*emptyVisibleTurnRetryReadCountStream
	withdrawOnRead int
	withdraw       func()
}

func (s *emptyVisibleTurnRetryWithdrawStream) Recv(ctx context.Context) (aipapi.Event, error) {
	ev, err := s.emptyVisibleTurnRetryReadCountStream.Recv(ctx)
	if s.reads == s.withdrawOnRead {
		s.withdraw()
	}
	return ev, err
}

// emptyVisibleTurnRetryGateScriptedFixture pins the gate as requested over the
// identical backend; the gate-off control reuses the frozen characterization
// fixture.
func emptyVisibleTurnRetryGateScriptedFixture(t *testing.T, enabled bool, inner aipapi.ManagedEventStream) (*retryRecvStream, *attemptSession) {
	t.Helper()
	if enabled {
		f := newEmptyVisibleTurnRetryLoopFixture(t, []aipapi.Event{})
		testStoreInner(f.stream, inner)
		return f.stream, f.attempt
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, nil)
	base := newEmptyVisibleTurnRetryGateOffFixture(t, ex, aLegID, []aipapi.Event{})
	testStoreInner(base.stream, inner)
	return base.stream, base.attempt
}

// TestEmptyVisibleTurnRetryLoop_PrefixDissolve pins every dissolve trigger -
// first visible output (text or tool), the bytes bound, a declined finish -
// releasing the retained prefix in order, exactly once, with the hold phase as
// the retention observable.
func TestEmptyVisibleTurnRetryLoop_PrefixDissolve(t *testing.T) {
	t.Parallel()
	head := aipapi.Event{Kind: aipapi.EventResponseStarted}
	filler := emptyVisibleTurnRetryLoopReasoning(maxWithheldBytes - head.PayloadBytes())
	require.Equal(t, maxWithheldBytes-head.PayloadBytes(), filler.PayloadBytes(), "fixture filler payload")

	cases := []struct {
		name       string
		script     []aipapi.Event
		setup      func(t *testing.T, f *emptyVisibleTurnRetryLoopFixture)
		wantPhase  correctionHoldState
		wantTokens int64
		wantTurns  int
		wantDecide []emptyVisibleTurnRetryLoopDecision
	}{
		{
			name: "visible output releases lifecycle, reasoning, and usage before itself",
			script: []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventReasoningDelta, Delta: "thinking"},
				emptyVisibleTurnRetryGateOffUsage(3, 5, 8),
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: "hello"},
				{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
			},
			wantPhase: correctionHoldReleased, wantTokens: 5, wantTurns: 1,
		},
		{
			name: "the frame that outgrows the byte bound is released last, exactly once",
			script: []aipapi.Event{
				head, filler,
				{Kind: aipapi.EventReasoningDelta},
				{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
			},
			wantPhase: correctionHoldOverflowed, wantTurns: 1,
			wantDecide: []emptyVisibleTurnRetryLoopDecision{{outcome: "skipped", reason: "released_frames"}},
		},
		{
			name: "assembled tool output dissolves the hold before the lifecycle",
			script: []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventReasoningDelta, Delta: "thinking"},
				{Kind: aipapi.EventToolCallStarted, ToolCallID: "call-1", ToolName: "t"},
				{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call-1", Delta: `{}`},
				{Kind: aipapi.EventToolCallFinished, ToolCallID: "call-1"},
				{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
			},
			// A passing assembler dissolves the hold through its drain.
			setup: func(_ *testing.T, f *emptyVisibleTurnRetryLoopFixture) {
				f.attempt.toolFinal = newToolCallAssembler([]toolcall.Finalizer{mutFin{}}, 1024, []aipapi.ToolDef{{Name: "t"}})
			},
			wantPhase: correctionHoldReleased,
		},
		{
			name: "a declined clean empty finish releases the prefix and records one reason",
			script: []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventReasoningDelta, Delta: "thinking"},
				emptyVisibleTurnRetryGateOffUsage(3, 5, 8),
				{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
			},
			setup:     func(_ *testing.T, f *emptyVisibleTurnRetryLoopFixture) { f.attempt.cand.IsParallel = true },
			wantPhase: correctionHoldReleased, wantTokens: 5, wantTurns: 1,
			wantDecide: []emptyVisibleTurnRetryLoopDecision{{outcome: "skipped", reason: "parallel_race"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newEmptyVisibleTurnRetryLoopFixture(t, tc.script)
			if tc.setup != nil {
				tc.setup(t, f)
			}
			released, phase, err := f.drainObservingHold(context.Background(), t)
			require.Truef(t, err == nil || errors.Is(err, io.EOF), "Recv: %v", err)
			require.Equal(t, tc.script, released, "the released sequence must be the unchanged script")
			require.Equal(t, tc.wantPhase, phase, "hold phase at the first release")
			_, ok := f.attempt.popCorrectionHeld()
			require.False(t, ok, "a fully drained hold must own no frame")
			// Requirement 3.6: held frames are observed exactly once.
			require.Equal(t, tc.wantTokens, f.attempt.accountingSnapshot().OutputTokens, "accounted output tokens")
			if tc.wantTurns > 0 {
				records, err := f.executor.Store.LoadAttempts(context.Background(), f.aLegID)
				require.NoError(t, err)
				require.Lenf(t, records, tc.wantTurns, "attempt records = %#v", records)
				require.Equal(t, aipapi.AttemptSuccess, records[0].Outcome)
			}
			assertEmptyVisibleTurnRetryDecisions(t, f.logs, tc.wantDecide...)
		})
	}
}

// TestEmptyVisibleTurnRetryLoop_EndOfStreamReleasesOrDiscardsHeldPrefix pins
// the stream end: the held prefix releases before the original EOF or a backend
// transport failure, and the observed end is delivered unchanged without
// re-reading the ended backend (the cancellation discard schedule lives in the
// sibling test).
func TestEmptyVisibleTurnRetryLoop_EndOfStreamReleasesOrDiscardsHeldPrefix(t *testing.T) {
	t.Parallel()
	frames := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "thinking"},
	}
	backendErr := errors.New("backend read failed")
	cases := []struct {
		name         string
		script       []aipapi.Event
		firstTail    error
		wantReleased []aipapi.Event
		wantErr      error
		wantReads    int
		wantPhase    correctionHoldState
	}{
		{
			name:   "clean EOF releases the prefix then delivers the original end",
			script: frames, firstTail: io.EOF,
			wantReleased: frames, wantErr: io.EOF, wantReads: 3, wantPhase: correctionHoldReleased,
		},
		{
			// Unlike the EOF and prepared-finish branches, a plain backend read
			// error takes the non-cancellation handleError path, which must
			// release the held prefix before surfacing the original error.
			name:   "transport failure releases the prefix then surfaces the original error",
			script: frames, firstTail: backendErr,
			wantReleased: frames, wantErr: backendErr, wantReads: 3, wantPhase: correctionHoldReleased,
		},
		{
			name:      "an armed empty hold then end of stream is consumed once",
			firstTail: io.EOF,
			wantErr:   io.EOF, wantReads: 1, wantPhase: correctionHoldDiscarded,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inner := &emptyVisibleTurnRetryReadCountStream{
				frames:    tc.script,
				firstTail: tc.firstTail,
				laterTail: errors.New("unexpected backend read after the end of stream"),
			}
			stream, attempt := emptyVisibleTurnRetryGateScriptedFixture(t, true, inner)
			f := &emptyVisibleTurnRetryLoopFixture{stream: stream, attempt: attempt}
			released, phase, err := f.drainObservingHold(context.Background(), t)
			require.ErrorIs(t, err, tc.wantErr, "terminal error")
			require.Equal(t, tc.wantReleased, released, "client-visible sequence")
			require.Equal(t, tc.wantPhase, phase, "hold phase at the stream end")
			require.Equal(t, tc.wantReads, inner.reads, "backend reads")
			_, ok := attempt.popCorrectionHeld()
			require.False(t, ok, "the hold must own no frame after the stream ended")
			// The observed end is retained: a later receive must not re-read.
			_, later := stream.Recv(context.Background())
			require.ErrorIs(t, later, io.EOF, "later Recv must return the original end")
			require.Equal(t, tc.wantReads, inner.reads, "backend reads after a later Recv")
		})
	}
}

// TestEmptyVisibleTurnRetryLoop_CancellationDiscardsHeldPrefix pins the
// withdrawal fence for both authorities: a withdrawal landing while a backend
// read still returns its successful frame never releases the retained prefix,
// and neither the hold nor pending terminal work can resurrect.
func TestEmptyVisibleTurnRetryLoop_CancellationDiscardsHeldPrefix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		attachALeg bool
		wantErr    error
	}{
		{"caller cancellation during a successful read", false, context.Canceled},
		{"shared A-leg cancellation during a successful read", true, leglifecycle.ErrALegCanceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			var withdraw func()
			var aLeg *leglifecycle.ALeg
			if tc.attachALeg {
				coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
				aLeg = coord.StartALeg("aleg-empty-visible-turn-cancel")
				withdraw = func() {
					if err := aLeg.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelClientGone}); err != nil {
						t.Errorf("cancel A-leg: %v", err)
					}
				}
			} else {
				withdraw = cancel
			}
			// The withdrawal lands inside the second read, which still returns
			// its successful frame: the fence must respect the owner instead of
			// draining the retained prefix.
			inner := &emptyVisibleTurnRetryWithdrawStream{
				emptyVisibleTurnRetryReadCountStream: &emptyVisibleTurnRetryReadCountStream{
					frames:    []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventTextDelta, Delta: "answer"}},
					firstTail: context.Canceled,
					laterTail: context.Canceled,
				},
				withdrawOnRead: 2,
				withdraw:       withdraw,
			}
			stream, attempt := emptyVisibleTurnRetryGateScriptedFixture(t, true, inner)
			if tc.attachALeg {
				// Give the fixture's terminal the real A-leg scope.
				stream.terminal.aLegEndAuthority = &aLegEndAuthority{scope: aLeg, mode: aLegEndBase}
			}
			ev, err := stream.Recv(ctx)
			require.Empty(t, ev.Kind, "a canceled turn must release nothing")
			require.Error(t, err, "a canceled turn must end with its withdrawal")
			require.ErrorIs(t, err, tc.wantErr)
			require.Equal(t, 2, inner.reads, "reads (prefix, successful delta)")
			require.Equal(t, correctionHoldDiscarded, attempt.correctionHoldState(), "hold state")
			_, ok := attempt.popCorrectionHeld()
			require.False(t, ok, "cancellation must never release a retained frame")
			_, ok = attempt.takeDeferredTerminalWork()
			require.False(t, ok, "cancellation must discard pending terminal work")
			ev, _ = stream.Recv(context.Background())
			require.Empty(t, ev.Kind, "a later receive must not resurrect the turn")
			_, ok = attempt.popCorrectionHeld()
			require.False(t, ok, "a later receive must not resurrect the hold")
		})
	}
}

// TestEmptyVisibleTurnRetryLoop_ExecutorLegArmFence pins the interleaved
// continuation: the executor leg arms only when the hidden handoff transferred
// no outer visible prefix; after a real visible prefix the terminal is
// committed, the leg never arms, and the wrapper reasoning still reaches the
// client.
func TestEmptyVisibleTurnRetryLoop_ExecutorLegArmFence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		mode          string
		wantCommitted bool
		wantHold      correctionHoldState
		wantReasoning string
	}{
		{"hidden handoff arms the executor leg", "hidden", false, correctionHoldArmed, ""},
		{"outer visible prefix prevents arming", "visible", true, correctionHoldUnarmed, barrierAnswer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			events := append(barrierThinkerPrefix(), aipapi.Event{Kind: aipapi.EventResponseFinished})
			rig := newBarrierRig(t, tc.mode, &barrierInnerStream{events: events})
			// The continuation's terminal binds the generation-pinned gate at handoff.
			rig.ex.EmptyVisibleTurnRetry = EmptyVisibleTurnRetryPolicy{Enabled: true}
			gate := parkPreparedExecutorOpen(t, rig)

			done := make(chan recvResult, 1)
			go func() { done <- rig.drive(context.Background()) }()
			awaitParked(t, "the executor leg's first receive", gate)

			executor := barrierExecutorStream(t, rig)
			require.Equal(t, tc.wantCommitted, executor.terminal.committed(), "executor terminal commitment")
			require.Equal(t, tc.wantHold, executor.attempt.snapshot().correctionHoldState(), "executor-leg hold state at its first receive")

			gate.releaseAll()
			recv := awaitClosed(t, "the handoff Recv", done)
			require.ErrorIs(t, recv.err, io.EOF, "handoff Recv error")
			require.Equal(t, tc.wantReasoning, reasoningText(recv.events), "handoff reasoning")
		})
	}
}

// parkPreparedExecutorOpen parks the continuation opener's first backend
// receive, so the executor leg's terminal and hold can be observed exactly
// when the hold decision runs.
func parkPreparedExecutorOpen(t *testing.T, rig *barrierRig) *parkGate {
	t.Helper()
	gate := newParkGate()
	gate.arm()
	t.Cleanup(gate.releaseAll)
	base := rig.ex.Backends["backend-1"]
	base.Open = func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		return &barrierInnerStream{
			events:   []aipapi.Event{{Kind: aipapi.EventResponseStarted}},
			recvGate: gate,
		}, nil
	}
	rig.ex.Backends["backend-1"] = base
	return gate
}

// barrierExecutorStream returns the executor leg of a driven interleaved
// handoff, under the wrapper's own lock.
func barrierExecutorStream(t *testing.T, rig *barrierRig) *retryRecvStream {
	t.Helper()
	rig.stream.mu.Lock()
	defer rig.stream.mu.Unlock()
	if rig.stream.executor == nil {
		t.Fatal("the handoff must have opened an executor continuation")
	}
	return rig.stream.executor
}

// TestEmptyVisibleTurnRetryLoop_UncorrectableTurnsNeverArm pins the arm-time
// rails: a large-body/wire turn and a completion-gate turn can never be
// corrected, so the receive loop must not buffer a prefix it would only
// release again. The hold phase is sampled on the attempt's first backend read
// - after the per-attempt arm decision - and the script must reach the client
// unchanged.
func TestEmptyVisibleTurnRetryLoop_UncorrectableTurnsNeverArm(t *testing.T) {
	t.Parallel()
	completionGateContext := func() context.Context {
		return extensions.WithRequestRuntimeSnapshot(context.Background(), extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{
			FeaturePlanes: freezeBundle(testFeatureBundle{CompletionGates: []completion.Gate{emptyVisibleTurnRetryDecisionGate{}}}),
		}))
	}
	cases := []struct {
		name  string
		ctx   context.Context
		setup func(*retryRecvStream)
	}{
		{
			name: "wire path",
			ctx:  context.Background(),
			setup: func(s *retryRecvStream) {
				s.facts.wirePayload = &wireAttemptPayload{}
			},
		},
		{name: "completion gates", ctx: completionGateContext(), setup: func(*retryRecvStream) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			script := []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventReasoningDelta, Delta: "thinking"},
				{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
			}
			var stream *retryRecvStream
			holdAtFirstRead := correctionHoldArmed
			probe := &emptyVisibleTurnRetryFirstReadHoldProbe{
				emptyVisibleTurnRetryReadCountStream: &emptyVisibleTurnRetryReadCountStream{
					frames:    script,
					firstTail: io.EOF,
					laterTail: errors.New("unexpected backend read after the empty finish"),
				},
				probe: func() { holdAtFirstRead = stream.attempt.snapshot().correctionHoldState() },
			}
			stream, _ = emptyVisibleTurnRetryGateScriptedFixture(t, true, probe)
			tc.setup(stream)
			var released []aipapi.Event
			for {
				ev, err := stream.Recv(tc.ctx)
				if err != nil {
					require.ErrorIs(t, err, io.EOF, "terminal error")
					break
				}
				released = append(released, ev)
			}
			require.Equal(t, correctionHoldUnarmed, holdAtFirstRead, "an uncorrectable %s turn must never arm the prefix hold", tc.name)
			require.Equal(t, script, released, "an uncorrectable turn must release the script unchanged")
		})
	}
}

// TestEmptyVisibleTurnRetryLoop_FinishPreparationErrorIsPreserved pins the
// prepared finish's failure as pending terminal work: the held prefix releases
// first, the original refusal terminalizes with its type and cause intact, and
// the rejected backend is never read again.
func TestEmptyVisibleTurnRetryLoop_FinishPreparationErrorIsPreserved(t *testing.T) {
	t.Parallel()
	frames := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "incomplete", ToolName: mandatoryToolName},
		{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
	}
	unexpected := errors.New("unexpected backend read after rejected finish")
	inner := &emptyVisibleTurnRetryReadCountStream{frames: frames, firstTail: unexpected, laterTail: unexpected}
	stream, attempt := emptyVisibleTurnRetryGateScriptedFixture(t, true, inner)
	// The incomplete call leaves a pending requirement, so the finish's
	// preparation returns the typed refusal from the real assembler.
	attempt.toolFinal = newToolCallAssembler([]toolcall.Finalizer{newMandatoryExpansionFin()}, 0, mandatoryCatalog())

	ev, err := stream.Recv(context.Background())
	require.NoError(t, err)
	require.Equal(t, aipapi.EventResponseStarted, ev.Kind)
	_, err = stream.Recv(context.Background())
	var refusal *MandatoryBufferingError
	require.ErrorAs(t, err, &refusal, "the original mandatory-buffering refusal must be preserved")
	require.Equal(t, ReasonMandatoryBufferingIncomplete, refusal.Reason)
	require.Equal(t, 3, inner.reads, "reads (prefix, incomplete call, rejected finish)")
}

// Provider-unchanged proof (task 9; requirements 1.1, 1.2, 4.7): a wired
// terminal-decision provider disables the correction entirely and keeps its
// exclusive accepted-finish evaluation byte-identical for gate on and off,
// invoked exactly once, with gate-on adding only its one bounded decline
// record.

// emptyVisibleTurnRetryProviderCapture records every provider evaluation the
// existing chokepoint makes and returns one deterministic allow-stop decision.
type emptyVisibleTurnRetryProviderCapture struct {
	mu        sync.Mutex
	inputs    []terminaldecision.Input
	decisions []terminaldecision.Decision
}

func (c *emptyVisibleTurnRetryProviderCapture) decide(_ context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inputs = append(c.inputs, in)
	decision := terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "probe-allow-stop"}
	c.decisions = append(c.decisions, decision)
	return decision, nil
}

// emptyVisibleTurnRetryFirstReadHoldProbe samples the attempt's hold phase on
// the backend's first read: the receive loop has just made its per-attempt arm
// decision, so a provider-wired turn's hold must still be unarmed.
type emptyVisibleTurnRetryFirstReadHoldProbe struct {
	*emptyVisibleTurnRetryReadCountStream
	probe func()
}

func (s *emptyVisibleTurnRetryFirstReadHoldProbe) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.reads == 0 {
		s.probe()
	}
	return s.emptyVisibleTurnRetryReadCountStream.Recv(ctx)
}

// emptyVisibleTurnRetryProviderTurn is one completed scripted turn.
type emptyVisibleTurnRetryProviderTurn struct {
	executor            *Executor
	stream              *retryRecvStream
	attempt             *attemptSession
	aLegID              string
	bLegID              string
	logs                *bytes.Buffer
	backend             *authorityOpenCounter
	released            []aipapi.Event
	inputs              []terminaldecision.Input
	decisions           []terminaldecision.Decision
	holdBeforeFirstRead correctionHoldState
}

// runEmptyVisibleTurnRetryProviderTurn drives the script through the real loop
// with a wired provider and a real replacement opener (so a corrective open
// would be counted, not impossible).
func runEmptyVisibleTurnRetryProviderTurn(t *testing.T, enabled bool, script []aipapi.Event) *emptyVisibleTurnRetryProviderTurn {
	t.Helper()
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, nil)
	ex.EmptyVisibleTurnRetry = EmptyVisibleTurnRetryPolicy{Enabled: enabled}
	logs := &bytes.Buffer{}
	ex.Log = slog.New(slog.NewJSONHandler(logs, nil))
	stream := newEmptyVisibleTurnRetryComposedStream(t, ex, aLegID)
	capture := &emptyVisibleTurnRetryProviderCapture{}
	provider := terminalDecisionProviderFunc{id: "retry-provider-probe", fn: capture.decide}
	stream.terminal.terminalDecisionProvider = provider
	stream.terminal.terminalDecisionProviderID = provider.ID()
	stream.terminal.terminalDecisionProviderHasID = true
	inner := &emptyVisibleTurnRetryFirstReadHoldProbe{emptyVisibleTurnRetryReadCountStream: &emptyVisibleTurnRetryReadCountStream{
		frames:    script,
		firstTail: io.EOF,
		laterTail: errors.New("unexpected backend read after the empty finish"),
	}}
	testStoreInner(stream, inner)
	turn := &emptyVisibleTurnRetryProviderTurn{
		executor: ex, stream: stream, attempt: stream.attempt.snapshot(),
		aLegID: aLegID, bLegID: stream.attempt.snapshot().bleg.BLegID, logs: logs, backend: backend,
	}
	inner.probe = func() { turn.holdBeforeFirstRead = stream.attempt.snapshot().correctionHoldState() }
	for range 32 {
		ev, err := stream.Recv(context.Background())
		if err != nil {
			require.Truef(t, errors.Is(err, io.EOF), "Recv after %d released event(s): %v", len(turn.released), err)
			break
		}
		turn.released = append(turn.released, ev)
	}
	capture.mu.Lock()
	turn.inputs = append([]terminaldecision.Input(nil), capture.inputs...)
	turn.decisions = append([]terminaldecision.Decision(nil), capture.decisions...)
	capture.mu.Unlock()
	return turn
}

// normalizeEmptyVisibleTurnRetryProviderInput replaces only the per-run a/b-leg
// identifiers (relation-preserving) and the wall-clock Deadline; every other
// field must compare byte-for-byte.
func normalizeEmptyVisibleTurnRetryProviderInput(in terminaldecision.Input, aLegID, bLegID string) terminaldecision.Input {
	replace := func(s string) string {
		return strings.ReplaceAll(strings.ReplaceAll(s, aLegID, "a-leg"), bLegID, "b-leg")
	}
	in.Deadline = time.Time{}
	in.Candidate.Reference = replace(in.Candidate.Reference)
	in.Request.RequestID = replace(in.Request.RequestID)
	in.Request.TraceID = replace(in.Request.TraceID)
	in.Request.ALegID = replace(in.Request.ALegID)
	in.Request.BLegID = replace(in.Request.BLegID)
	in.Continuation.TrajectoryRef = replace(in.Continuation.TrajectoryRef)
	in.Evidence.Objective = replace(in.Evidence.Objective)
	in.Evidence.RecentText = replace(in.Evidence.RecentText)
	in.Evidence.CandidateText = replace(in.Evidence.CandidateText)
	in.Evidence.Lineage.TrajectoryRef = replace(in.Evidence.Lineage.TrajectoryRef)
	in.Evidence.Lineage.ParentRef = replace(in.Evidence.Lineage.ParentRef)
	in.Evidence.Lineage.ProgressRef = replace(in.Evidence.Lineage.ProgressRef)
	for i := range in.Evidence.Actions {
		in.Evidence.Actions[i].ItemID = replace(in.Evidence.Actions[i].ItemID)
		in.Evidence.Actions[i].CallID = replace(in.Evidence.Actions[i].CallID)
		in.Evidence.Actions[i].Name = replace(in.Evidence.Actions[i].Name)
	}
	return in
}

// TestEmptyVisibleTurnRetryProvider_EvaluationPathIsByteIdentical pins the
// conservative provider rule over the real path: the provider is invoked once,
// with byte-identical inputs and decisions in both generations, and the gate
// changes nothing it can observe.
func TestEmptyVisibleTurnRetryProvider_EvaluationPathIsByteIdentical(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		script       []aipapi.Event
		gateOnReason string
	}{
		{
			// Lifecycle, reasoning, and usage frames release before the finish,
			// so the released-frame guard is the first decline.
			name: "reasoning-only empty finish",
			script: []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventReasoningDelta, Delta: "thinking"},
				emptyVisibleTurnRetryGateOffUsage(3, 5, 8),
				{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
			},
			gateOnReason: "released_frames",
		},
		{
			// Nothing is produced before the finish: the provider rail declines.
			name:         "empty finish with no produced frame",
			script:       []aipapi.Event{{Kind: aipapi.EventResponseFinished, FinishReason: "stop"}},
			gateOnReason: "terminal_decision_provider",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			off := runEmptyVisibleTurnRetryProviderTurn(t, false, tc.script)
			on := runEmptyVisibleTurnRetryProviderTurn(t, true, tc.script)
			turns := []struct {
				name string
				turn *emptyVisibleTurnRetryProviderTurn
			}{{"gate-off", off}, {"gate-on", on}}

			require.Equal(t, off.decisions, on.decisions, "provider decisions")
			offInput := normalizeEmptyVisibleTurnRetryProviderInput(off.inputs[0], off.aLegID, off.bLegID)
			onInput := normalizeEmptyVisibleTurnRetryProviderInput(on.inputs[0], on.aLegID, on.bLegID)
			offJSON, err := json.Marshal(offInput)
			require.NoError(t, err)
			onJSON, err := json.Marshal(onInput)
			require.NoError(t, err)
			require.Truef(t, bytes.Equal(offJSON, onJSON), "provider input differs between gates:\n gate-off: %s\n gate-on:  %s", offJSON, onJSON)

			// Client-visible frames and usage accounting are untouched: the hold
			// is never introduced for a turn the correction cannot take.
			require.Equal(t, off.released, on.released, "gate-on client-visible events")
			require.Equal(t, tc.script, on.released, "the backend script must reach the client unchanged")
			require.Equal(t, correctionHoldUnarmed, on.holdBeforeFirstRead, "a provider-wired turn must not buffer")
			require.Equal(t, off.attempt.accountingSnapshot().OutputTokens, on.attempt.accountingSnapshot().OutputTokens, "accounted output tokens")

			// Requirement 4.7: no corrective open or claimed allowance in either
			// run; only gate-on adds its one bounded decline record.
			for _, tt := range turns {
				turn := tt.turn
				require.Lenf(t, turn.inputs, 1, "%s provider inputs", tt.name)
				require.Lenf(t, turn.decisions, 1, "%s provider decisions", tt.name)
				require.Equal(t, terminaldecision.CandidateCauseNormal, turn.inputs[0].Candidate.Cause, tt.name+" provider invocation must be the accepted-finish evaluation")
				require.Zero(t, turn.backend.openCalls.Load(), tt.name+" must not open a corrective attempt")
				require.False(t, turn.stream.terminal.correctiveRetrySpent(), tt.name+" must not claim the corrective allowance")
				// Both generations end on the same accepted normal finish.
				request, attempt := testTerminalOwners(turn.stream)
				for scope, term := range map[string]*streamTerminal{"request": request, "attempt": attempt} {
					outcome, ok := term.Owner().Outcome()
					require.Truef(t, ok, "%s %s terminal outcome must be observed", tt.name, scope)
					require.Equalf(t, sdkterminal.CommandNormalFinish, outcome.Command, "%s %s terminal outcome", tt.name, scope)
				}
				records, err := turn.executor.Store.LoadAttempts(context.Background(), turn.aLegID)
				require.NoError(t, err)
				require.Lenf(t, records, 1, "%s attempt records = %#v", tt.name, records)
				require.Equal(t, aipapi.AttemptSuccess, records[0].Outcome)
				require.Equal(t, "backend-1", records[0].BackendID)
			}
			assertEmptyVisibleTurnRetryDecisions(t, on.logs, emptyVisibleTurnRetryLoopDecision{outcome: "skipped", reason: tc.gateOnReason})
			assertEmptyVisibleTurnRetryDecisions(t, off.logs)
		})
	}
}
