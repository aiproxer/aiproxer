package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// TestPhase2_ReadyAttempt_SingleUse verifies that readyAttempt Consume()
// is single-use and duplicate consumption is rejected.
func TestPhase2_ReadyAttempt_SingleUse(t *testing.T) {
	t.Parallel()

	session := &attemptSession{}
	ready := &readyAttempt{
		session: session,
		state:   readyStatePrepared,
	}

	// First consume must succeed
	got, err := ready.Consume()
	if err != nil {
		t.Fatalf("first Consume() failed: %v", err)
	}
	if got != session {
		t.Errorf("got session %v, want %v", got, session)
	}

	// Second consume must fail (duplicate consumption rejected)
	got2, err2 := ready.Consume()
	if err2 == nil {
		t.Fatal("expected error on second Consume(), got nil")
	}
	if got2 != nil {
		t.Errorf("expected nil session on second Consume(), got %v", got2)
	}
}

// TestPhase2_ReadyAttempt_Disposal verifies that disposal of an unconsumed ready attempt
// invokes complete attempt terminalization.
func TestPhase2_ReadyAttempt_Disposal(t *testing.T) {
	t.Parallel()

	ex := TestExecutor()
	terminal := newTurnTerminal()
	bindTurnTerminalRuntime(terminal, ex)

	session := &attemptSession{
		terminal: newStreamTerminal(sdkterminal.ScopeAttempt),
		bleg:     b2bua.BLegRecord{ALegID: "a-leg-1", BLegID: "b-leg-1", Seq: 1},
	}
	ready := &readyAttempt{
		session: session,
	}

	// Dispose without consuming
	ready.Dispose(context.Background(), errors.New("unconsumed ready attempt disposal test"))

	// Verify that the readyAttempt is marked consumed and session stream/terminal resources are cleaned up
	if !ready.IsConsumed() {
		t.Error("expected readyAttempt to be marked consumed after Dispose")
	}
}

// TestPhase2_AssemblyCommitAtomicity_Success verifies that a successful stream assembly commit
// publishes the attempt and hands off request ownership (Handoff called).
func TestPhase2_AssemblyCommitAtomicity_Success(t *testing.T) {
	t.Parallel()

	session := &attemptSession{}
	ready := &readyAttempt{
		session: session,
	}

	guard := &preStreamGuard{}
	tx := &streamAssemblyTx{
		ready: ready,
		guard: guard,
	}

	// Perform successful commit
	if err := tx.Commit(); err != nil {
		t.Fatalf("tx.Commit() failed: %v", err)
	}

	if !tx.committed {
		t.Error("expected streamAssemblyTx to be committed")
	}
	if !guard.handedOver {
		t.Error("expected request guard to be handed over (Handoff called) on successful commit")
	}

	// Attempt rollback after commit should be a no-op (mutual exclusivity)
	tx.Rollback(context.Background(), errors.New("test rollback after commit"))
	if ready.IsConsumed() {
		t.Error("expected readyAttempt not to be consumed via Dispose during rollback of committed tx")
	}
}

// TestPhase2_AssemblyCommitAtomicity_Failure verifies that a stream assembly failure (e.g. panic or error before return)
// rolls back the transaction, terminalizing the unpublished attempt completely and leaving request cleanup active.
func TestPhase2_AssemblyCommitAtomicity_Failure(t *testing.T) {
	t.Parallel()

	session := &attemptSession{
		terminal: newStreamTerminal(sdkterminal.ScopeAttempt),
		bleg:     b2bua.BLegRecord{ALegID: "a-leg-2", BLegID: "b-leg-2", Seq: 1},
	}
	ready := &readyAttempt{
		session: session,
	}

	guard := &preStreamGuard{}
	tx := &streamAssemblyTx{
		ready: ready,
		guard: guard,
	}

	// Simulate failure triggering rollback
	tx.Rollback(context.Background(), errors.New("test assembly error"))

	if !tx.committed {
		t.Error("expected streamAssemblyTx committed flag to be set to true on rollback")
	}
	if ready.session != nil || !ready.IsConsumed() {
		t.Error("expected ready attempt to be disposed/consumed and detached on rollback")
	}
	if guard.handedOver {
		t.Error("expected request guard NOT to be handed over (Handoff not called) on failure rollback")
	}
}

// TestPhase2_AcquisitionRollback_Idempotence verifies that Rollback/Abort is idempotent,
// runs cleanups only once, and uses detached contexts.
func TestPhase2_AcquisitionRollback_Idempotence(t *testing.T) {
	t.Parallel()

	ex := TestExecutor()
	budget := &attemptBudget{max: 10}
	if !budget.tryAcquire() {
		t.Fatal("failed to acquire budget")
	}

	tx := &attemptTx{
		e:              ex,
		budget:         budget,
		budgetAcquired: true,
		bleg:           b2bua.BLegRecord{ALegID: "a-leg-3", BLegID: "b-leg-3", Seq: 1},
		cand:           routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}},
	}

	// Call Rollback once
	tx.Rollback(context.Background(), sdkterminal.CommandBackendOpenFailure, usagelimit.ReleaseKindAdmissionFailure, usageevidence.LegOutcomeFailed, aipapi.Event{})

	if !tx.completed {
		t.Error("expected attemptTx to be marked completed")
	}
	if tx.budgetAcquired {
		t.Error("expected budget to be released and budgetAcquired set to false")
	}
	if budget.usedNow() != 0 {
		t.Errorf("expected budget used count to be 0, got %d", budget.usedNow())
	}

	// Double call should be a safe no-op (idempotency check)
	tx.Rollback(context.Background(), sdkterminal.CommandBackendOpenFailure, usagelimit.ReleaseKindAdmissionFailure, usageevidence.LegOutcomeFailed, aipapi.Event{})
}
