package runtime

// Unit facts of the empty-visible-turn corrective retry: the bounded prefix
// hold (task 3; requirements 3.1, 3.3, 3.5, 3.6), the turn-scoped terminal
// facts (task 4; requirements 1.3, 2.3, 4.1), and the disabled gate (task 2).

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/require"
)

// emptyVisibleTurnRetryHoldPrep is one prepared frame as the hold receives it.
func emptyVisibleTurnRetryHoldPrep(ev aipapi.Event) recvEventPreparation {
	return recvEventPreparation{event: ev}
}

// emptyVisibleTurnRetryHoldText builds a text-delta frame whose retained
// payload is exactly size bytes, so the byte bound can be driven to its edge.
func emptyVisibleTurnRetryHoldText(size int) aipapi.Event {
	return aipapi.Event{Kind: aipapi.EventTextDelta, Delta: strings.Repeat("x", size-len(aipapi.EventTextDelta))}
}

// TestEmptyVisibleTurnRetryHold_BoundsReleaseInOrder pins both retention bounds
// and the overflow rule: past the bound every retained frame still reaches the
// release queue in order, followed exactly once by the frame that outgrew it.
func TestEmptyVisibleTurnRetryHold_BoundsReleaseInOrder(t *testing.T) {
	t.Parallel()
	half := emptyVisibleTurnRetryHoldText(maxWithheldBytes / 2)
	require.Equal(t, maxWithheldBytes/2, half.PayloadBytes(), "fixture frame payload")

	eventFrames, eventHeld, eventSizes := func() (frames []aipapi.Event, wantHeld []bool, wantSizes []int) {
		one := aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}
		for range maxWithheldEvents {
			frames = append(frames, one)
			wantHeld = append(wantHeld, true)
			wantSizes = append(wantSizes, one.PayloadBytes())
		}
		overflow := aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "overflow"}
		return append(frames, overflow), append(wantHeld, false), append(wantSizes, overflow.PayloadBytes())
	}()

	cases := []struct {
		name      string
		frames    []aipapi.Event
		wantHeld  []bool
		wantSizes []int
	}{
		{"event bound accepts maxWithheldEvents frames, the next is refused", eventFrames, eventHeld, eventSizes},
		{
			"a total of exactly the byte bound is accepted, the next byte is refused",
			[]aipapi.Event{half, emptyVisibleTurnRetryHoldText(maxWithheldBytes - maxWithheldBytes/2), {Kind: aipapi.EventTextDelta}},
			[]bool{true, true, false},
			[]int{maxWithheldBytes / 2, maxWithheldBytes - maxWithheldBytes/2, len(aipapi.EventTextDelta)},
		},
		{
			"one byte over the byte bound is refused",
			[]aipapi.Event{half, emptyVisibleTurnRetryHoldText(maxWithheldBytes - maxWithheldBytes/2 + 1)},
			[]bool{true, false},
			[]int{maxWithheldBytes / 2, maxWithheldBytes - maxWithheldBytes/2 + 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			attempt := &attemptSession{}
			attempt.armCorrectionHold()
			for i, ev := range tc.frames {
				require.Equalf(t, tc.wantHeld[i], attempt.holdCorrectionFrame(emptyVisibleTurnRetryHoldPrep(ev)),
					"hold %d (payload %d bytes)", i, ev.PayloadBytes())
			}
			require.Equal(t, correctionHoldOverflowed, attempt.correctionHoldState(), "state after exceeding a bound")
			for i, size := range tc.wantSizes {
				got, ok := attempt.popCorrectionHeld()
				require.Truef(t, ok, "pop %d must yield a frame: an overflow releases the earlier frames", i)
				require.Equalf(t, size, got.event.PayloadBytes(), "pop %d payload", i)
			}
			_, ok := attempt.popCorrectionHeld()
			require.False(t, ok, "every retained frame must pop exactly once")
			require.False(t, attempt.correctionHoldPending(), "a fully drained hold must own no frames")
		})
	}
}

// TestEmptyVisibleTurnRetryHold_ReleaseAndRearmGuards pins the release queue and
// its guards: an unarmed hold retains nothing, and a released hold never
// resumes retaining.
func TestEmptyVisibleTurnRetryHold_ReleaseAndRearmGuards(t *testing.T) {
	t.Parallel()
	unarmed := &attemptSession{}
	require.False(t, unarmed.holdCorrectionFrame(emptyVisibleTurnRetryHoldPrep(aipapi.Event{Kind: aipapi.EventResponseStarted})),
		"an unarmed hold must not retain a frame")

	attempt := &attemptSession{}
	require.Nil(t, attempt.toolCallAssembler(), "the hold must not depend on a tool-call assembler")
	attempt.armCorrectionHold()
	retained := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "thinking"},
	}
	for i, ev := range retained {
		require.Truef(t, attempt.holdCorrectionFrame(emptyVisibleTurnRetryHoldPrep(ev)), "frame %d must be retained", i)
	}
	trigger := aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "visible"}
	attempt.releaseCorrectionHold(emptyVisibleTurnRetryHoldPrep(trigger))
	require.Equal(t, correctionHoldReleased, attempt.correctionHoldState(), "state after release")
	got, ok := attempt.popCorrectionHeld()
	require.True(t, ok, "release must hand the first retained frame back")
	require.Equal(t, retained[0], got.event)
	for attempt.correctionHoldPending() {
		_, ok := attempt.popCorrectionHeld()
		require.True(t, ok, "every retained frame must drain")
	}
	require.False(t, attempt.holdCorrectionFrame(emptyVisibleTurnRetryHoldPrep(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "late"})),
		"a released hold must not resume retaining frames")
	require.False(t, attempt.correctionHoldPending(), "a refused frame must not be retained")
}

// TestEmptyVisibleTurnRetryHold_RetainsDeepCopies pins that the hold retains a
// deep copy: mutating the producer's backing storage after the hold cannot
// corrupt the frame later released to the client.
func TestEmptyVisibleTurnRetryHold_RetainsDeepCopies(t *testing.T) {
	t.Parallel()
	build := func() aipapi.Event {
		return aipapi.Event{
			Kind:      aipapi.EventTextDelta,
			Delta:     "held",
			Opaque:    []byte{1, 2, 3},
			Reasoning: &aipapi.ReasoningPart{Dialect: "test.dialect.v1", Text: "reason"},
			Item: &aipapi.Item{
				Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hi"}},
			},
		}
	}
	ev, want := build(), build()
	attempt := &attemptSession{}
	attempt.armCorrectionHold()
	require.True(t, attempt.holdCorrectionFrame(emptyVisibleTurnRetryHoldPrep(ev)), "fixture must retain one frame")
	ev.Opaque[0] = 9
	ev.Reasoning.Text = "mutated"
	ev.Item.Content[0].Text = "mutated"
	attempt.releaseCorrectionHold()
	got, ok := attempt.popCorrectionHeld()
	require.True(t, ok, "release must hand the retained frame back")
	require.Equal(t, want, got.event, "the hold must retain a deep copy")
}

// TestEmptyVisibleTurnRetryHold_LifecycleDiscardsWithoutRelease pins the shared
// cleanup: cancellation, close, replacement, and disposal free the queue without
// releasing a frame, and a discarded hold never releases, retains, or re-arms.
func TestEmptyVisibleTurnRetryHold_LifecycleDiscardsWithoutRelease(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		lifecycle func(t *testing.T, attempt *attemptSession)
	}{
		{"cancellation", func(_ *testing.T, attempt *attemptSession) {
			attempt.cancelViaLifecycle(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
		}},
		{"close", func(_ *testing.T, attempt *attemptSession) {
			_ = attempt.closeViaLifecycle()
		}},
		{"replacement", func(t *testing.T, attempt *attemptSession) {
			t.Helper()
			var slot attemptSlot
			slot.mu.Lock()
			slot.current = attempt
			slot.mu.Unlock()
			replacement := newAttemptSession(attemptSessionInput{})
			_, published := slot.swapIfOpen(&readyAttempt{session: replacement, state: readyStatePrepared})
			require.True(t, published, "fixture replacement must publish")
			// The receive loop's replacement cleanup for the retired attempt.
			clearAttemptToolState(newResponsePipeline(), attempt)
			require.False(t, slot.require().correctionHoldPending(), "the replacement attempt must not inherit the held prefix")
		}},
		{"disposal", func(_ *testing.T, attempt *attemptSession) {
			newReadyAttempt(attempt, pendingSelectionEffects{}).Dispose(context.Background(), errors.New("empty-visible-turn hold disposal"))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			attempt := newAttemptSession(attemptSessionInput{bleg: b2bua.BLegRecord{ALegID: "a-empty-turn-hold", BLegID: "b-empty-turn-hold", Seq: 1}})
			attempt.armCorrectionHold()
			require.True(t, attempt.holdCorrectionFrame(emptyVisibleTurnRetryHoldPrep(aipapi.Event{Kind: aipapi.EventResponseStarted})), "fixture must retain one frame")
			tc.lifecycle(t, attempt)
			check := func(stage string) {
				t.Helper()
				require.Equal(t, correctionHoldDiscarded, attempt.correctionHoldState(), stage)
				require.False(t, attempt.correctionHoldPending(), stage+" must free the queue")
				_, ok := attempt.popCorrectionHeld()
				require.False(t, ok, stage+" must never release a frame")
			}
			check(tc.name)
			attempt.releaseCorrectionHold(emptyVisibleTurnRetryHoldPrep(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "post-discard"}))
			check(tc.name + " after a late release")
		})
	}
}

// TestEmptyVisibleTurnRetryTerminal_RecvReturnLatchesFrameKind pins the release
// seam fact on the real receive loop: a canonical frame the real Recv returns
// to the client latches the turn's released-frame fact, while the synthetic
// keepalive does not.
func TestEmptyVisibleTurnRetryTerminal_RecvReturnLatchesFrameKind(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		frame       aipapi.Event
		wantLatched bool
	}{
		{"response lifecycle frame latches", aipapi.Event{Kind: aipapi.EventResponseStarted}, true},
		{"synthetic keepalive does not latch", stream.DefaultKeepaliveEvent(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, nil)
			fixture := newEmptyVisibleTurnRetryGateOffFixture(t, ex, aLegID, []aipapi.Event{tc.frame})
			term := fixture.stream.terminal
			require.False(t, term.releasedClientFrameLatched(), "a fresh turn must not report a released client frame")
			got, err := fixture.stream.Recv(context.Background())
			require.NoError(t, err)
			require.Equal(t, tc.frame, got)
			require.Equal(t, tc.wantLatched, term.releasedClientFrameLatched(), "releasedClientFrame after returning "+got.Kind)
		})
	}
}

// TestEmptyVisibleTurnRetryTerminal_UnreturnedFrameDoesNotLatch proves "only a
// release latches": the backend read succeeds with a canonical frame, but the
// caller's withdrawal lands inside that read and the receive-loop withdrawal
// fence ends the turn before dispatching it, so nothing reached the client.
func TestEmptyVisibleTurnRetryTerminal_UnreturnedFrameDoesNotLatch(t *testing.T) {
	t.Parallel()
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, nil)
	ex.EmptyVisibleTurnRetry = EmptyVisibleTurnRetryPolicy{Enabled: true}
	fixture := newEmptyVisibleTurnRetryGateOffFixture(t, ex, aLegID, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	inner := &emptyVisibleTurnRetryWithdrawStream{
		emptyVisibleTurnRetryReadCountStream: &emptyVisibleTurnRetryReadCountStream{
			frames:    []aipapi.Event{{Kind: aipapi.EventResponseStarted}},
			firstTail: errors.New("unexpected backend read after the withdrawn frame"),
			laterTail: errors.New("unexpected backend read after the withdrawn frame"),
		},
		withdrawOnRead: 1,
		withdraw:       cancel,
	}
	testStoreInner(fixture.stream, inner)

	ev, err := fixture.stream.Recv(ctx)
	require.Equal(t, 1, inner.reads, "the backend frame must have been read")
	require.Empty(t, ev.Kind, "a withdrawn receive must not return the read frame")
	require.ErrorIs(t, err, context.Canceled, "the withdrawal keeps its own claim")
	require.False(t, fixture.stream.terminal.releasedClientFrameLatched(),
		"a backend frame that was read but never returned must not latch")
}

// TestEmptyVisibleTurnRetryTerminal_TurnFactsSurviveAttemptReplacement proves
// the latch and allowance are turn-scoped: a fresh attempt neither clears the
// latch nor refunds the allowance, and a new turn owns its own allowance.
func TestEmptyVisibleTurnRetryTerminal_TurnFactsSurviveAttemptReplacement(t *testing.T) {
	t.Parallel()
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, nil)
	fixture := newEmptyVisibleTurnRetryGateOffFixture(t, ex, aLegID, []aipapi.Event{{Kind: aipapi.EventResponseStarted}})
	term := fixture.stream.terminal
	_, err := fixture.stream.Recv(context.Background())
	require.NoError(t, err)
	require.True(t, term.releasedClientFrameLatched(), "a returned response_started must latch the turn")
	require.True(t, term.claimCorrectiveRetry(), "the first claim of an unspent turn must win")
	require.False(t, term.claimCorrectiveRetry(), "the allowance is one-shot")

	emptyVisibleTurnRetryTerminalReplaceAttempt(t, fixture, []aipapi.Event{
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "replacement"},
	})
	require.True(t, term.releasedClientFrameLatched(), "the turn-scoped latch must survive the attempt replacement")
	require.False(t, term.claimCorrectiveRetry(), "replacing the attempt must never refund the spent allowance")
	first, err := fixture.stream.Recv(context.Background())
	require.NoError(t, err)
	require.Equal(t, aipapi.EventMessageStarted, first.Kind)
	require.True(t, term.releasedClientFrameLatched())
	require.False(t, newTurnTerminal().releasedClientFrameLatched(), "the latch must be turn-scoped, not global")
	require.True(t, newTurnTerminal().claimCorrectiveRetry(), "each logical turn owns its own unspent allowance")
}

// TestEmptyVisibleTurnRetryTerminal_GatePinnedPerTurn proves the gate is copied
// once into the turn's terminal: a generation change on the executor can never
// reach a turn already admitted under its own generation, while a later turn
// captures the new generation's value.
func TestEmptyVisibleTurnRetryTerminal_GatePinnedPerTurn(t *testing.T) {
	t.Parallel()
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, nil)
	ex.EmptyVisibleTurnRetry = EmptyVisibleTurnRetryPolicy{Enabled: true}
	fixture := newEmptyVisibleTurnRetryGateOffFixture(t, ex, aLegID, []aipapi.Event{{Kind: aipapi.EventResponseStarted}})
	term := fixture.stream.terminal
	require.True(t, term.emptyVisibleTurnRetryEnabled(), "an enabled generation must be captured")

	// A reload resolves a new generation value; the in-flight turn keeps its own.
	ex.EmptyVisibleTurnRetry = EmptyVisibleTurnRetryPolicy{Enabled: false}
	bindTurnTerminalRuntime(term, ex)
	require.True(t, term.emptyVisibleTurnRetryEnabled(), "rebinding must not change the pinned gate")
	later := newTurnTerminal()
	bindTurnTerminalRuntime(later, ex)
	require.False(t, later.emptyVisibleTurnRetryEnabled(), "a later turn must capture the reloaded gate")
}

// emptyVisibleTurnRetryTerminalReplaceAttempt publishes a fresh prepared attempt
// on the fixture's open slot and installs the next scripted backend stream.
func emptyVisibleTurnRetryTerminalReplaceAttempt(t *testing.T, fixture *emptyVisibleTurnRetryGateOffFixture, script []aipapi.Event) {
	t.Helper()
	replacement := newAttemptSession(attemptSessionInput{
		bleg:           b2bua.BLegRecord{ALegID: fixture.aLegID, BLegID: "b-leg-empty-turn-replacement", Seq: 2},
		cand:           authorityCandidate(),
		authority:      authorityLifecycle{},
		finalStreamObs: &extensions.FinalStreamObservationSession{},
	})
	_, published := fixture.stream.attempt.swapIfOpen(&readyAttempt{session: replacement, state: readyStatePrepared})
	require.True(t, published, "fixture replacement must publish on the open slot")
	fixture.stream.responsePipeline.resetForReplacement()
	testStoreInner(fixture.stream, aipapi.NewFixedEventStream(script))
}

// TestEmptyVisibleTurnRetryGateOffExplicitDisableCharacterization runs the
// frozen task-1 scripts over an executor whose generation-pinned gate is
// explicitly disabled and requires the identical behavior (requirement 1.1).
func TestEmptyVisibleTurnRetryGateOffExplicitDisableCharacterization(t *testing.T) {
	t.Parallel()
	for _, tc := range emptyVisibleTurnRetryGateOffCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &emptyVisibleTurnRetryGateOffSink{}
			ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, nil)
			ex.EmptyVisibleTurnRetry = EmptyVisibleTurnRetryPolicy{Enabled: false}
			ex.Metrics = sink
			runEmptyVisibleTurnRetryGateOffCase(t, ex, aLegID, sink, tc)
		})
	}
}
