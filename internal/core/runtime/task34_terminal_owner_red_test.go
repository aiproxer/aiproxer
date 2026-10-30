package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// Task 3.4 RED: terminal entry points must be owner operations over an
// explicitly captured evidence snapshot and attempt. The old retryRecvStream
// forwarding families do not provide this operation yet, so this test is
// intentionally uncached and fails to compile until the owner seam exists.
func TestTask34TerminalOwner_CommandsAndEffects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  sdkterminal.Command
	}{
		{name: "normal finish", cmd: sdkterminal.CommandNormalFinish},
		{name: "cancel", cmd: sdkterminal.CommandCancel},
		{name: "timeout", cmd: sdkterminal.CommandTimeout},
		{name: "eof", cmd: sdkterminal.CommandEOF},
		{name: "partial error", cmd: sdkterminal.CommandPartialError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			turn := newTurnTerminal()
			attempt := newAttemptSession(attemptSessionInput{})
			snapshot := coreterm.NewAccumulatorSnapshot([]byte(test.name), false)
			var attemptEffects, requestEffects atomic.Int32
			// Attempt terminal via TerminalizeAttempt
			attemptEv := attemptEvidence{Command: test.cmd, Snapshot: &snapshot}
			attemptIntent := IntentSurfacedFailure
			switch test.cmd {
			case sdkterminal.CommandNormalFinish:
				attemptIntent = IntentSuccess
			case sdkterminal.CommandCancel:
				attemptIntent = IntentCancellation
			case sdkterminal.CommandTimeout:
				attemptIntent = IntentTimeout
			}
			winnerA := attempt.TerminalizeAttempt(context.Background(), attemptIntent, attemptEv)
			if !winnerA.Result.Won || winnerA.Result.Err != nil || winnerA.Result.Outcome.Command != test.cmd {
				t.Fatalf("attempt winner=%+v want winning %s", winnerA.Result, test.cmd)
			}
			attemptEffects.Add(1)
			observerA := attempt.TerminalizeAttempt(context.Background(), attemptIntent, attemptEv)
			if observerA.Result.Won || observerA.Result.Outcome.Command != test.cmd {
				t.Fatalf("attempt same-command observer=%+v want published %s", observerA.Result, test.cmd)
			}
			conflict := sdkterminal.CommandEOF
			if conflict == test.cmd {
				conflict = sdkterminal.CommandCancel
			}
			conflictEv := attemptEvidence{Command: conflict, Snapshot: &snapshot}
			loserA := attempt.TerminalizeAttempt(context.Background(), IntentSurfacedFailure, conflictEv)
			if loserA.Result.Won || !errors.Is(loserA.Result.Err, sdkterminal.ErrConflict) || loserA.Result.Outcome.Command != test.cmd {
				t.Fatalf("attempt loser=%+v want conflict observing %s", loserA.Result, test.cmd)
			}
			// Request terminal via terminalizeRequest
			requestAfter := func(context.Context, coreterm.Outcome) error {
				requestEffects.Add(1)
				return nil
			}
			winnerR := turn.terminalizeRequest(context.Background(), test.cmd, snapshot, requestAfter)
			if !winnerR.Won || winnerR.Err != nil || winnerR.Outcome.Command != test.cmd {
				t.Fatalf("request winner=%+v want winning %s", winnerR, test.cmd)
			}
			observerR := turn.terminalizeRequest(context.Background(), test.cmd, snapshot, requestAfter)
			if observerR.Won || observerR.Outcome.Command != test.cmd {
				t.Fatalf("request same-command observer=%+v want published %s", observerR, test.cmd)
			}
			loserR := turn.terminalizeRequest(context.Background(), conflict, snapshot, requestAfter)
			if loserR.Won || !errors.Is(loserR.Err, sdkterminal.ErrConflict) || loserR.Outcome.Command != test.cmd {
				t.Fatalf("request loser=%+v want conflict observing %s", loserR, test.cmd)
			}
			if got := attemptEffects.Load(); got != 1 {
				t.Fatalf("attempt effects=%d want once", got)
			}
			if got := requestEffects.Load(); got != 1 {
				t.Fatalf("request effects=%d want once", got)
			}
		})
	}

	t.Run("swallowed attempt does not claim request", func(t *testing.T) {
		turn := newTurnTerminal()
		attempt := newAttemptSession(attemptSessionInput{})
		snapshot := coreterm.NewAccumulatorSnapshot(nil, false)
		ev := attemptEvidence{Command: sdkterminal.CommandSwallowedAttempt, Snapshot: &snapshot}
		result := attempt.TerminalizeAttempt(context.Background(), IntentSwallowedFailure, ev)
		if !result.Result.Won || result.Result.Err != nil {
			t.Fatalf("swallowed result=%+v", result.Result)
		}
		if turn.requestTerminal().Owner().State() != sdkterminal.StateOpen {
			t.Fatalf("request state=%q want open", turn.requestTerminal().Owner().State())
		}
	})

	t.Run("committed gate replacement rejects without effects", func(t *testing.T) {
		callID, err := usageevidence.NewBillingCallID()
		if err != nil {
			t.Fatal(err)
		}
		var closures atomic.Int32
		executor := &Executor{BillingRuntime: BillingRuntime{
			TerminalUsageSink: testTerminalSink{appendCall: func(context.Context, usageevidence.CallUsageRecord) error {
				closures.Add(1)
				return nil
			}},
			BillingIdentity: testBillingIdentity(),
		}}
		stream := &retryRecvStream{
			terminal: newTurnTerminal(),
			facts: testRecvTurnFacts(recvTurnFacts{
				aLegID:        "a-task34-gate",
				billingCallID: callID,
				baseline:      aipapi.Call{Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-task34-gate"}},
			}),
			attempt: testAttemptSlot(
				b2bua.BLegRecord{ALegID: "a-task34-gate", BLegID: "b-task34-gate", Seq: 1},
				routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}},
				authorityLifecycle{},
			),
		}
		stream = stampStreamIdentity(stream, executor)
		turn := stream.terminal
		attempt := stream.attempt.snapshot()
		turn.markCommitted(attempt)
		var attemptEffects atomic.Int32
		requestEffects := func(ctx context.Context, _ coreterm.Outcome) error {
			if err := stream.terminal.handoffBillingTurn(ctx, stream.facts.terminalFacts(), sdkterminal.CommandGateReplacement); err != nil {
				t.Fatalf("handoffBillingTurn: %v", err)
			}
			return nil
		}
		snap := coreterm.NewAccumulatorSnapshot(nil, false)
		result := turn.terminalizeRequest(context.Background(), sdkterminal.CommandGateReplacement, snap, requestEffects)
		// Gate replacement is request-only; attempt terminal must remain unclaimed.
		if result.Won || !errors.Is(result.Err, sdkterminal.ErrOutputCommitted) || closures.Load() != 1 {
			t.Fatalf("gate replacement result=%+v closures=%d", result, closures.Load())
		}
		if attemptEffects.Load() != 0 {
			t.Fatalf("gate replacement attempt effects must remain 0, got %d", attemptEffects.Load())
		}
		// A competing rejected gate still invokes the request closure seam, but
		// the economic owner deduplicates the already sealed call.
		result = turn.terminalizeRequest(context.Background(), sdkterminal.CommandGateReplacement, snap, requestEffects)
		if result.Won || !errors.Is(result.Err, sdkterminal.ErrOutputCommitted) || closures.Load() != 1 {
			t.Fatalf("repeated gate replacement result=%+v closures=%d", result, closures.Load())
		}
	})

	t.Run("captured attempt remains authoritative after replacement", func(t *testing.T) {
		old := newAttemptSession(attemptSessionInput{})
		next := newAttemptSession(attemptSessionInput{})
		snap := coreterm.NewAccumulatorSnapshot(nil, false)
		ev := attemptEvidence{Command: sdkterminal.CommandSwallowedAttempt, Snapshot: &snap}
		result := old.TerminalizeAttempt(context.Background(), IntentSwallowedFailure, ev)
		if !result.Result.Won || old.terminal.Owner().State() != sdkterminal.StateReleased {
			t.Fatalf("old result=%+v state=%q", result.Result, old.terminal.Owner().State())
		}
		if next.terminal.Owner().State() != sdkterminal.StateOpen {
			t.Fatalf("replacement attempt state=%q want open", next.terminal.Owner().State())
		}
	})
}
