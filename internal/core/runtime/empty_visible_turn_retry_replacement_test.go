package runtime

// Corrective replacement trigger for the empty-visible-turn retry (spec task 7;
// requirements 4.2-4.6, 5.2). An admitted decision claims the one-shot
// allowance before the fallible open, discards the held prefix, marks the
// correction pending, retires the empty attempt non-surfacing with its usage
// evidence retained, and runs exactly one corrective open through the existing
// prepare/register/swap publication sequence. The candidate records
// `corrected` once published or `failed` once otherwise; no discarded frame
// reaches the client, and a withdrawal keeps its cancellation claim.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	goruntime "runtime"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/require"
)

// emptyVisibleTurnRetryReplacementFixture drives the real receive loop with the
// gate enabled, a real replacement opener, and B1 installed as the attempt.
type emptyVisibleTurnRetryReplacementFixture struct {
	executor *Executor
	backend  *authorityOpenCounter
	aLegID   string
	logs     *bytes.Buffer
	sink     *emptyVisibleTurnRetryGateOffSink
	stream   *retryRecvStream
	attempt  *attemptSession
}

func newEmptyVisibleTurnRetryReplacementFixture(t *testing.T, script []aipapi.Event) *emptyVisibleTurnRetryReplacementFixture {
	t.Helper()
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, nil)
	ex.EmptyVisibleTurnRetry = EmptyVisibleTurnRetryPolicy{Enabled: true}
	logs := &bytes.Buffer{}
	ex.Log = slog.New(slog.NewJSONHandler(logs, nil))
	sink := &emptyVisibleTurnRetryGateOffSink{}
	ex.Metrics = sink
	stream := newEmptyVisibleTurnRetryComposedStream(t, ex, aLegID)
	testStoreInner(stream, &emptyVisibleTurnRetryReadCountStream{
		frames:    script,
		firstTail: errors.New("unexpected backend read after the empty finish"),
		laterTail: errors.New("unexpected backend read after the empty finish"),
	})
	return &emptyVisibleTurnRetryReplacementFixture{ex, backend, aLegID, logs, sink, stream, stream.attempt.snapshot()}
}

// scriptBackend installs the scripted successor stream the corrective open serves.
func (f *emptyVisibleTurnRetryReplacementFixture) scriptBackend(t *testing.T, script []aipapi.Event) {
	t.Helper()
	f.backend.openFn = func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		return aipapi.NewFixedEventStream(script), nil
	}
}

// drain reads to the terminal error and returns the observed frames in order.
func (f *emptyVisibleTurnRetryReplacementFixture) drain(ctx context.Context, t *testing.T) ([]aipapi.Event, error) {
	t.Helper()
	var released []aipapi.Event
	for range 32 {
		ev, err := f.stream.Recv(ctx)
		if err != nil {
			return released, err
		}
		released = append(released, ev)
	}
	t.Fatalf("receive loop did not end after 32 events")
	return nil, nil
}

// emptyVisibleTurnRetryReplacementB1 is the degenerate attempt: a clean empty
// finish carrying a reasoning frame and usage evidence.
func emptyVisibleTurnRetryReplacementB1() []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "thinking"},
		emptyVisibleTurnRetryGateOffUsage(3, 5, 8),
		{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
	}
}

// mustAttemptOutcome returns the persisted outcome recorded for one B-leg.
func (f *emptyVisibleTurnRetryReplacementFixture) mustAttemptOutcome(t *testing.T, bLegID string) aipapi.AttemptOutcome {
	t.Helper()
	attempts, err := f.executor.Store.LoadAttempts(context.Background(), f.aLegID)
	require.NoError(t, err)
	for _, record := range attempts {
		if record.BLegID == bLegID {
			return record.Outcome
		}
	}
	return ""
}

// TestEmptyVisibleTurnRetryReplacement_CorrectedTurnDeliversOnlySuccessorOutput
// pins both successor shapes: exactly one corrective open, only the successor's
// frames reach the client, the discarded attempt settles non-surfacing with its
// usage retained, and the accepted winner is observed exactly once.
func TestEmptyVisibleTurnRetryReplacement_CorrectedTurnDeliversOnlySuccessorOutput(t *testing.T) {
	t.Parallel()
	b1 := emptyVisibleTurnRetryReplacementB1()
	cases := []struct {
		name           string
		successor      []aipapi.Event
		wantEmptyTurns int
		wantDecisions  []emptyVisibleTurnRetryLoopDecision
	}{
		{
			name: "contentful successor",
			successor: []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: "answer"},
				emptyVisibleTurnRetryGateOffUsage(7, 11, 18),
				{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
			},
			wantDecisions: []emptyVisibleTurnRetryLoopDecision{{outcome: "corrected"}},
		},
		{
			name: "repeated empty successor is delivered as the accepted outcome",
			successor: []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				emptyVisibleTurnRetryGateOffUsage(7, 11, 18),
				{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
			},
			wantEmptyTurns: 1,
			wantDecisions: []emptyVisibleTurnRetryLoopDecision{
				{outcome: "corrected"},
				{outcome: "skipped", reason: "budget_spent"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newEmptyVisibleTurnRetryReplacementFixture(t, b1)
			f.scriptBackend(t, tc.successor)

			released, err := f.drain(context.Background(), t)
			require.Truef(t, err == nil || errors.Is(err, io.EOF), "Recv: %v", err)
			require.Equal(t, tc.successor, released, "only the corrective attempt's frames may reach the client")
			// Requirement 4.2: exactly one open; no keepalive re-plan.
			require.Equal(t, int64(1), f.backend.openCalls.Load(), "backend Open calls")
			successor := f.stream.attempt.snapshot()
			require.NotSame(t, f.attempt, successor, "the successor must have replaced the empty attempt")
			require.False(t, f.attempt.hasInner(), "the discarded attempt's stream must not survive retirement")
			require.True(t, f.stream.terminal.correctiveRetrySpent(), "the allowance must be claimed once the correction ran")

			// Requirements 4.4 and 5.1: both attempts keep their usage evidence;
			// the empty-turn signal counts only the accepted winner.
			require.Equal(t, int64(5), f.attempt.accountingSnapshot().OutputTokens, "discarded attempt provider usage retained")
			require.Equal(t, int64(11), successor.accountingSnapshot().OutputTokens, "successor provider usage")
			require.Equal(t, aipapi.AttemptSwallowedFailure, f.mustAttemptOutcome(t, f.attempt.bleg.BLegID), "discarded attempt outcome")
			require.Equal(t, aipapi.AttemptSuccess, f.mustAttemptOutcome(t, successor.bleg.BLegID), "successor attempt outcome")
			require.Equal(t, tc.wantEmptyTurns, f.sink.emptyTurnCount(), "empty-visible-turn count")
			if tc.wantEmptyTurns > 0 {
				require.Equal(t, "backend-1", f.sink.emptyTurnBackend(), "empty-visible-turn backend")
			}
			// Requirement 5.2: each candidate carries its own record.
			records := assertEmptyVisibleTurnRetryDecisions(t, f.logs, tc.wantDecisions...)
			require.Equal(t, f.attempt.bleg.BLegID, records[0]["b_leg_id"], "corrected record lineage")
			if len(records) == 2 {
				require.Equal(t, successor.bleg.BLegID, records[1]["b_leg_id"], "budget_spent record lineage")
			}
		})
	}
}

// TestEmptyVisibleTurnRetryReplacement_UnopenableCorrectiveAttemptFailsExplicitly
// pins requirement 4.6: an open that cannot publish ends the turn with the
// typed recoverable pre-output failure, never leaks the provider error, releases
// no held frame, and never re-plans.
func TestEmptyVisibleTurnRetryReplacement_UnopenableCorrectiveAttemptFailsExplicitly(t *testing.T) {
	t.Parallel()
	f := newEmptyVisibleTurnRetryReplacementFixture(t, emptyVisibleTurnRetryReplacementB1())
	providerErr := errors.New("provider refused the corrective open")
	f.backend.openFn = func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		return nil, providerErr
	}

	released, err := f.drain(context.Background(), t)
	require.Empty(t, released, "no frame of the discarded attempt may reach the client")
	var upstream *aipapi.UpstreamFailureError
	require.ErrorAs(t, err, &upstream, "terminal error must be the typed upstream failure")
	require.Equal(t, aipapi.PhasePreOutput, upstream.Phase)
	require.True(t, upstream.Recoverable)
	require.NotContains(t, err.Error(), providerErr.Error(), "the raw provider error must not leak")
	require.Equal(t, int64(1), f.backend.openCalls.Load(), "exactly one bounded corrective open")
	require.Nil(t, f.attempt.loadInner(), "the discarded attempt must not be readable after retirement")
	require.Equal(t, correctionHoldDiscarded, f.attempt.correctionHoldState(), "the held prefix is discarded, never released")
	require.True(t, f.stream.terminal.correctiveRetrySpent(), "the allowance must stay spent after a failed open (4.1)")
	require.Equal(t, int64(5), f.attempt.accountingSnapshot().OutputTokens, "discarded attempt usage retained")
	require.Equal(t, aipapi.AttemptSwallowedFailure, f.mustAttemptOutcome(t, f.attempt.bleg.BLegID), "discarded attempt outcome")
	// One `failed` record; a later receive cannot resurrect a corrective retry.
	assertEmptyVisibleTurnRetryDecisions(t, f.logs, emptyVisibleTurnRetryLoopDecision{outcome: "failed"})
	_, later := f.stream.Recv(context.Background())
	require.ErrorIs(t, later, io.EOF, "no corrective retry may follow the failure terminal")
}

// TestEmptyVisibleTurnRetryReplacement_ClosedWindowFailsWithoutPublication pins
// the closed window: when Close wins while the open is in flight, no successor
// publishes, no frame releases, the unregistered successor is disposed, and one
// `failed` record carries the typed cause.
func TestEmptyVisibleTurnRetryReplacement_ClosedWindowFailsWithoutPublication(t *testing.T) {
	t.Parallel()
	f := newEmptyVisibleTurnRetryReplacementFixture(t, emptyVisibleTurnRetryReplacementB1())
	successor := &closeReplacementRaceStream{}
	gate := newParkGate()
	gate.arm()
	t.Cleanup(gate.releaseAll)
	f.backend.openFn = func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		gate.park()
		return successor, nil
	}

	type replacementRecv struct {
		released []aipapi.Event
		err      error
	}
	done := make(chan replacementRecv, 1)
	go func() {
		var rec replacementRecv
		for range 16 {
			ev, err := f.stream.Recv(context.Background())
			if err != nil {
				rec.err = err
				break
			}
			rec.released = append(rec.released, ev)
		}
		done <- rec
	}()
	awaitParked(t, "the corrective open", gate)
	require.NoError(t, f.stream.Close())
	gate.releaseAll()

	var result replacementRecv
	select {
	case result = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the receive loop did not return after the closed publication window")
	}
	require.Empty(t, result.released, "no frame may reach the client after Close")
	var upstream *aipapi.UpstreamFailureError
	require.ErrorAs(t, result.err, &upstream, "terminal error")
	require.Equal(t, aipapi.PhasePreOutput, upstream.Phase)
	require.True(t, upstream.Recoverable)
	require.True(t, f.stream.terminal.finished(), "Close must stay the authoritative terminal")
	require.Same(t, f.attempt, f.stream.attempt.snapshot(), "the successor must not be published after Close")
	require.Equal(t, int32(1), successor.cancelCalls.Load(), "unregistered successor Cancel calls")
	require.Equal(t, int32(1), successor.closeCalls.Load(), "unregistered successor Close calls")
	require.Equal(t, correctionHoldDiscarded, f.attempt.correctionHoldState())
	assertEmptyVisibleTurnRetryDecisions(t, f.logs, emptyVisibleTurnRetryLoopDecision{outcome: "failed"})
}

// TestEmptyVisibleTurnRetryReplacement_PostPublicationFailureKeepsOrdinaryRecovery
// pins the bounded-mode boundary: after publication a recoverable pre-output
// failure takes the ordinary swallow/exclude/re-open rules - never a second
// corrective open, never the corrective typed failure, and no refunded
// allowance.
func TestEmptyVisibleTurnRetryReplacement_PostPublicationFailureKeepsOrdinaryRecovery(t *testing.T) {
	t.Parallel()
	f := newEmptyVisibleTurnRetryReplacementFixture(t, emptyVisibleTurnRetryReplacementB1())
	transient := aipapi.RecoverablePreOutputError(errors.New("transient successor failure"))
	f.backend.openFn = func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		return &emptyVisibleTurnRetryReadCountStream{
			frames:    []aipapi.Event{{Kind: aipapi.EventResponseStarted}},
			firstTail: transient,
			laterTail: transient,
		}, nil
	}

	released, err := f.drain(context.Background(), t)
	require.Len(t, released, 1, "only the successor's released lifecycle frame")
	require.Equal(t, aipapi.EventResponseStarted, released[0].Kind)
	require.Error(t, err, "the excluded single-candidate route must end the turn")
	var upstream *aipapi.UpstreamFailureError
	require.False(t, errors.As(err, &upstream), "a post-publication failure must not use the corrective failure")
	require.Equal(t, int64(1), f.backend.openCalls.Load(), "the corrective open only")
	assertEmptyVisibleTurnRetryDecisions(t, f.logs, emptyVisibleTurnRetryLoopDecision{outcome: "corrected"})
	require.True(t, f.stream.terminal.correctiveRetrySpent(), "a post-publication failure must not refund the allowance")
	require.Equal(t, aipapi.AttemptSwallowedFailure, f.mustAttemptOutcome(t, f.attempt.bleg.BLegID))
}

// emptyVisibleTurnRetryReplacementRetirementBarrier parks B1's backend Close so
// a withdrawal can land deterministically inside the retirement.
type emptyVisibleTurnRetryReplacementRetirementBarrier struct {
	*emptyVisibleTurnRetryReadCountStream
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
}

func newEmptyVisibleTurnRetryReplacementRetirementBarrier() *emptyVisibleTurnRetryReplacementRetirementBarrier {
	return &emptyVisibleTurnRetryReplacementRetirementBarrier{
		emptyVisibleTurnRetryReadCountStream: &emptyVisibleTurnRetryReadCountStream{
			frames:    emptyVisibleTurnRetryReplacementB1(),
			firstTail: errors.New("unexpected backend read after the empty finish"),
			laterTail: errors.New("unexpected backend read after the empty finish"),
		},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *emptyVisibleTurnRetryReplacementRetirementBarrier) Close() error {
	s.enteredOnce.Do(func() { close(s.entered) })
	<-s.release
	return nil
}

// TestEmptyVisibleTurnRetryReplacement_WithdrawalDuringRetirementFinalizesDecision
// pins requirement 5.2 when a withdrawal wins during the retirement: it keeps
// its Close, A-leg, or caller claim and error - never the fabricated failure -
// while the candidate finalizes one `failed` record, consumes the pending
// marker, and opens no successor.
func TestEmptyVisibleTurnRetryReplacement_WithdrawalDuringRetirementFinalizesDecision(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		attach  string // "aleg" or "caller"; empty means real Close
		wantErr error
	}{
		{"close during retirement keeps the EOF claim", "", io.EOF},
		{"shared A-leg cancellation during retirement keeps the cancellation claim", "aleg", leglifecycle.ErrALegCanceled},
		{"caller cancellation during retirement keeps the cancellation claim", "caller", context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			f := newEmptyVisibleTurnRetryReplacementFixture(t, emptyVisibleTurnRetryReplacementB1())
			barrier := newEmptyVisibleTurnRetryReplacementRetirementBarrier()
			var releaseOnce sync.Once
			releaseRetirement := func() { releaseOnce.Do(func() { close(barrier.release) }) }
			t.Cleanup(releaseRetirement)
			testStoreInner(f.stream, barrier)
			var aLegScope *leglifecycle.ALeg
			if tc.attach == "aleg" {
				coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
				aLegScope = coord.StartALeg("aleg-empty-visible-turn-retirement")
				f.stream.terminal.aLegEndAuthority = &aLegEndAuthority{scope: aLegScope, mode: aLegEndBase}
			}

			type recvOutcome struct {
				released []aipapi.Event
				err      error
			}
			recvDone := make(chan recvOutcome, 1)
			go func() {
				var rec recvOutcome
				for range 8 {
					ev, err := f.stream.Recv(ctx)
					if err != nil {
						rec.err = err
						break
					}
					rec.released = append(rec.released, ev)
				}
				recvDone <- rec
			}()
			select {
			case <-barrier.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("B1 retirement never reached the backend Close")
			}

			join := func() error { return nil }
			switch tc.attach {
			case "aleg":
				require.NoError(t, aLegScope.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelClientGone}))
			case "caller":
				cancel()
			default:
				closeDone := make(chan error, 1)
				go func() { closeDone <- f.stream.Close() }()
				deadline := time.Now().Add(5 * time.Second)
				for !f.stream.attempt.publicationIsClosed() && time.Now().Before(deadline) {
					goruntime.Gosched()
				}
				require.True(t, f.stream.attempt.publicationIsClosed(), "the real Close did not close the publication window")
				join = func() error { return <-closeDone }
			}
			releaseRetirement()

			var result recvOutcome
			select {
			case result = <-recvDone:
			case <-time.After(5 * time.Second):
				t.Fatal("Recv did not return after the withdrawal")
			}
			require.NoError(t, join(), "withdrawal")
			require.ErrorIs(t, result.err, tc.wantErr, "the withdrawal keeps its existing claim")
			var upstream *aipapi.UpstreamFailureError
			require.False(t, errors.As(result.err, &upstream), "a withdrawal must not be replaced by the fabricated corrective failure")
			require.Empty(t, result.released, "no frame of the withdrawn turn may reach the client")
			require.Zero(t, f.backend.openCalls.Load(), "the withdrawal claims the turn before the open")
			require.True(t, f.stream.terminal.correctiveRetrySpent(), "the withdrawal must not refund the allowance")
			require.False(t, f.stream.terminal.correctionPending(), "the accepted correction must consume its pending marker")
			require.Same(t, f.attempt, f.stream.attempt.snapshot(), "no successor may publish after the withdrawal")
			records := assertEmptyVisibleTurnRetryDecisions(t, f.logs, emptyVisibleTurnRetryLoopDecision{outcome: "failed"})
			require.Equal(t, f.attempt.bleg.BLegID, records[0]["b_leg_id"], "failed record lineage")
		})
	}
}

// TestEmptyVisibleTurnRetryReplacement_ALegCancellationDuringOpenKeepsTheCancellationClaim
// pins the terminal-observed withdrawal: the shared A-leg is canceled while the
// single corrective open is in flight, so the correction keeps that
// cancellation claim - never the fabricated upstream failure - records its one
// `failed` decision, and publishes no successor.
func TestEmptyVisibleTurnRetryReplacement_ALegCancellationDuringOpenKeepsTheCancellationClaim(t *testing.T) {
	t.Parallel()
	f := newEmptyVisibleTurnRetryReplacementFixture(t, emptyVisibleTurnRetryReplacementB1())
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aLeg := coord.StartALeg("aleg-empty-visible-turn-open-cancel")
	f.stream.terminal.aLegEndAuthority = &aLegEndAuthority{scope: aLeg, mode: aLegEndBase}
	gate := newParkGate()
	gate.arm()
	t.Cleanup(gate.releaseAll)
	f.backend.openFn = func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		gate.park()
		return nil, errors.New("the corrective open lost its upstream")
	}

	type openRecv struct {
		released []aipapi.Event
		err      error
	}
	done := make(chan openRecv, 1)
	go func() {
		var rec openRecv
		for range 16 {
			ev, err := f.stream.Recv(context.Background())
			if err != nil {
				rec.err = err
				break
			}
			rec.released = append(rec.released, ev)
		}
		done <- rec
	}()
	awaitParked(t, "the corrective open", gate)
	require.NoError(t, aLeg.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelClientGone}))
	gate.releaseAll()

	var result openRecv
	select {
	case result = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Recv did not return after the A-leg cancellation")
	}
	require.ErrorIs(t, result.err, leglifecycle.ErrALegCanceled, "the shared A-leg cancellation owns the turn")
	var upstream *aipapi.UpstreamFailureError
	require.False(t, errors.As(result.err, &upstream), "a withdrawal must not be replaced by the fabricated corrective failure")
	require.Empty(t, result.released, "no frame of the withdrawn turn may reach the client")
	require.True(t, f.stream.terminal.correctiveRetrySpent(), "the withdrawal must not refund the allowance")
	require.False(t, f.stream.terminal.correctionPending(), "the accepted correction must consume its pending marker")
	require.Same(t, f.attempt, f.stream.attempt.snapshot(), "no successor may publish after the withdrawal")
	assertEmptyVisibleTurnRetryDecisions(t, f.logs, emptyVisibleTurnRetryLoopDecision{outcome: "failed"})
}
