package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestTurnTerminal_CommitmentIsIdempotentAndNotifiesSnapshottedAttempt(t *testing.T) {
	t.Parallel()
	turn := newTurnTerminal()
	first := newAttemptSession(attemptSessionInput{})
	second := newAttemptSession(attemptSessionInput{})
	first.authority.outputCommitted = &atomic.Bool{}
	second.authority.outputCommitted = &atomic.Bool{}

	turn.markCommitted(first)
	turn.markCommitted(first)
	if !turn.committed() {
		t.Fatal("turn must remain committed after repeated mark")
	}
	if first.authority.outputCommitted == nil || !first.authority.outputCommitted.Load() {
		t.Fatal("first attempt authority was not notified")
	}
	if second.authority.outputCommitted == nil || second.authority.outputCommitted.Load() {
		t.Fatal("unrelated replacement attempt must not be notified")
	}

	// A later idempotent mark must notify the currently snapshotted replacement
	// too, while the request-lifetime turn remains the sole commitment truth.
	turn.markCommitted(second)
	if second.authority.outputCommitted == nil || !second.authority.outputCommitted.Load() {
		t.Fatal("replacement attempt was not notified by idempotent mark")
	}
}

func TestTurnTerminal_AttemptTerminalLifetimeFollowsReplacement(t *testing.T) {
	t.Parallel()
	turn := newTurnTerminal()
	first := newAttemptSession(attemptSessionInput{})
	second := newAttemptSession(attemptSessionInput{})

	firstResult := testTurnTerminalize(context.Background(), turn, sdkterminal.CommandSwallowedAttempt, first,
		func() coreterm.AccumulatorSnapshot {
			return coreterm.NewAccumulatorSnapshot(nil, false)
		}, nil)
	if !firstResult.Won || !first.terminal.Owner().State().IsTerminal() {
		t.Fatalf("first attempt terminalize: result=%+v state=%q", firstResult, first.terminal.Owner().State())
	}
	secondResult := testTurnTerminalize(context.Background(), turn, sdkterminal.CommandSwallowedAttempt, second,
		func() coreterm.AccumulatorSnapshot {
			return coreterm.NewAccumulatorSnapshot(nil, false)
		}, nil)
	if !secondResult.Won || !second.terminal.Owner().State().IsTerminal() {
		t.Fatalf("replacement attempt terminalize: result=%+v state=%q", secondResult, second.terminal.Owner().State())
	}
	if turn.requestTerminal().Owner().State() != sdkterminal.StateOpen {
		t.Fatalf("attempt-only commands must not claim request owner, state=%q", turn.requestTerminal().Owner().State())
	}
}

func TestTurnTerminal_RequestAndAttemptScopeEffectsExactlyOnce(t *testing.T) {
	t.Parallel()
	turn := newTurnTerminal()
	attempt := newAttemptSession(attemptSessionInput{})
	var effects atomic.Int32
	effect := func(context.Context, coreterm.Outcome) error {
		effects.Add(1)
		return nil
	}
	snap := func() coreterm.AccumulatorSnapshot {
		return coreterm.NewAccumulatorSnapshot([]byte("snapshot"), false)
	}

	winner := testTurnTerminalize(context.Background(), turn, sdkterminal.CommandClose, attempt, snap, effect)
	if !winner.Won || winner.Err != nil {
		t.Fatalf("winner=%+v", winner)
	}
	loser := testTurnTerminalize(context.Background(), turn, sdkterminal.CommandClose, attempt, snap, effect)
	if loser.Won || loser.Err != nil {
		t.Fatalf("idempotent loser=%+v", loser)
	}
	if got := effects.Load(); got != 1 {
		t.Fatalf("composed effects=%d want exactly once", got)
	}
	if !attempt.terminal.Owner().State().IsTerminal() || !turn.requestTerminal().Owner().State().IsTerminal() {
		t.Fatalf("request=%q attempt=%q want terminal", turn.requestTerminal().Owner().State(), attempt.terminal.Owner().State())
	}
}

func TestTurnTerminal_AttemptErrorPropagatesAndSettledAttemptFallsBackToRequestEffects(t *testing.T) {
	t.Parallel()
	effectErr := errors.New("attempt effect failed")
	snap := func() coreterm.AccumulatorSnapshot {
		return coreterm.NewAccumulatorSnapshot(nil, false)
	}

	failedTurn := newTurnTerminal()
	failedAttempt := newAttemptSession(attemptSessionInput{})
	failed := testTurnTerminalize(context.Background(), failedTurn, sdkterminal.CommandClose, failedAttempt, snap,
		func(context.Context, coreterm.Outcome) error { return effectErr })
	if !failed.Won || !errors.Is(failed.Err, effectErr) {
		t.Fatalf("attempt error result=%+v want propagated error", failed)
	}
	if failedAttempt.terminal.Owner().State() != sdkterminal.StateFailed || failedTurn.requestTerminal().Owner().State() != sdkterminal.StateFailed {
		t.Fatalf("attempt=%q request=%q want failed", failedAttempt.terminal.Owner().State(), failedTurn.requestTerminal().Owner().State())
	}

	settledTurn := newTurnTerminal()
	settledAttempt := newAttemptSession(attemptSessionInput{})
	initial := settledAttempt.terminal.Terminalize(context.Background(), sdkterminal.CommandClose, snap, nil)
	if !initial.Won {
		t.Fatalf("pre-settle attempt: %+v", initial)
	}
	var fallbackEffects atomic.Int32
	result := testTurnTerminalize(context.Background(), settledTurn, sdkterminal.CommandClose, settledAttempt, snap,
		func(context.Context, coreterm.Outcome) error {
			fallbackEffects.Add(1)
			return nil
		})
	if !result.Won || result.Err != nil || fallbackEffects.Load() != 1 {
		t.Fatalf("settled attempt request fallback: result=%+v effects=%d", result, fallbackEffects.Load())
	}
}

func TestTurnTerminal_RequestWinnerFallsBackAfterConflictingSettledAttempt(t *testing.T) {
	t.Parallel()
	turn := newTurnTerminal()
	attempt := newAttemptSession(attemptSessionInput{})
	snap := func() coreterm.AccumulatorSnapshot {
		return coreterm.NewAccumulatorSnapshot(nil, false)
	}

	settled := attempt.terminal.Terminalize(context.Background(), sdkterminal.CommandSwallowedAttempt, snap, nil)
	if !settled.Won {
		t.Fatalf("pre-settle conflicting attempt: %+v", settled)
	}

	var effects atomic.Int32
	result := testTurnTerminalize(context.Background(), turn, sdkterminal.CommandClose, attempt, snap,
		func(context.Context, coreterm.Outcome) error {
			effects.Add(1)
			return nil
		})
	if !result.Won || result.Err != nil {
		t.Fatalf("request winner after conflicting attempt: %+v", result)
	}
	if effects.Load() != 1 {
		t.Fatalf("request fallback effects=%d want exactly once", effects.Load())
	}
	if turn.requestTerminal().Owner().State() != sdkterminal.StateReleased {
		t.Fatalf("request state=%q want released", turn.requestTerminal().Owner().State())
	}
}

func TestTurnTerminal_ConcurrentLoserWaitsAndSharesWinnerOutcome(t *testing.T) {
	t.Parallel()
	turn := newTurnTerminal()
	attempt := newAttemptSession(attemptSessionInput{})
	entered := make(chan struct{})
	loserReady := make(chan struct{})
	winnerEffectsComplete := make(chan struct{})
	release := make(chan struct{})
	var effects atomic.Int32
	results := make(chan coreterm.Result, 2)

	go func() {
		results <- testTurnTerminalize(context.Background(), turn, sdkterminal.CommandClose, attempt,
			func() coreterm.AccumulatorSnapshot {
				return coreterm.NewAccumulatorSnapshot([]byte("winner"), false)
			}, func(context.Context, coreterm.Outcome) error {
				effects.Add(1)
				close(entered)
				<-release
				close(winnerEffectsComplete)
				return nil
			})
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("winner did not enter effects")
	}
	loserDone := make(chan struct{})
	go func() {
		close(loserReady)
		results <- testTurnTerminalize(context.Background(), turn, sdkterminal.CommandEOF, attempt,
			func() coreterm.AccumulatorSnapshot {
				return coreterm.NewAccumulatorSnapshot([]byte("loser"), false)
			}, nil)
		close(loserDone)
	}()
	<-loserReady
	close(release)
	<-winnerEffectsComplete
	<-loserDone
	winner := <-results
	loser := <-results
	if !winner.Won && !loser.Won {
		t.Fatalf("no winner: winner=%+v loser=%+v", winner, loser)
	}
	if effects.Load() != 1 {
		t.Fatalf("effects=%d want exactly once", effects.Load())
	}
	var winning coreterm.Result
	if winner.Won {
		winning = winner
	} else {
		winning = loser
	}
	for _, result := range []coreterm.Result{winner, loser} {
		if result.Outcome.Command != winning.Outcome.Command || !result.Outcome.Snapshot.Equal(winning.Outcome.Snapshot) {
			t.Fatalf("shared outcome diverged: winner=%+v loser=%+v", winner, loser)
		}
	}
}

func TestTurnTerminal_CommittedGateReplacementRejectsWithoutClaim(t *testing.T) {
	t.Parallel()
	turn := newTurnTerminal()
	attempt := newAttemptSession(attemptSessionInput{})
	turn.markCommitted(attempt)
	r := testTurnTerminalize(context.Background(), turn, sdkterminal.CommandGateReplacement, attempt,
		func() coreterm.AccumulatorSnapshot {
			return coreterm.NewAccumulatorSnapshot([]byte("not marked by snap"), false)
		}, func(context.Context, coreterm.Outcome) error {
			t.Fatal("committed gate replacement must not run effects")
			return nil
		})
	if r.Won || !errors.Is(r.Err, sdkterminal.ErrOutputCommitted) {
		t.Fatalf("result=%+v want output committed rejection", r)
	}
	if turn.requestTerminal().Owner().State() != sdkterminal.StateOpen {
		t.Fatalf("rejected gate must leave request owner open, state=%q", turn.requestTerminal().Owner().State())
	}
}

func TestTurnTerminal_FinishedPublicationIsIdempotent(t *testing.T) {
	t.Parallel()
	turn := newTurnTerminal()
	if turn.finished() {
		t.Fatal("new turn must not be finished")
	}
	if !turn.markFinished() || !turn.finished() {
		t.Fatal("first finish must publish finished")
	}
	if turn.markFinished() {
		t.Fatal("repeated finish must not publish a second transition")
	}
}

// TestTurnTerminal_ThinkerBoundaryNeverFinishesOrLatchesLogicalTurn pins the
// receive-phase split for an interleaved thinker: its boundary completion ends
// only the hidden B-leg, so the shared request terminal stays open and the
// scalar accepted-completion evidence the wire audit reads stays unlatched
// until the executor phase owns the logical turn. The same boundary on a
// client-visible terminal must finish and latch.
func TestTurnTerminal_ThinkerBoundaryNeverFinishesOrLatchesLogicalTurn(t *testing.T) {
	t.Parallel()
	thinker := newTurnTerminal()
	thinker.setInterleavedThinker()
	if !thinker.finishResponseAtBoundary(nil, nil, true) {
		t.Fatal("a thinker boundary completion must report finished")
	}
	if thinker.finished() || thinker.acceptedLogicalCompletionObserved() {
		t.Fatal("a thinker boundary must neither finish the request terminal nor latch accepted completion")
	}

	plain := newTurnTerminal()
	if !plain.finishResponseAtBoundary(nil, nil, true) {
		t.Fatal("a client-visible boundary completion must report finished")
	}
	if !plain.finished() || !plain.acceptedLogicalCompletionObserved() {
		t.Fatal("a client-visible boundary completion must finish and latch accepted completion")
	}
}
