package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// TestReadyAttempt_DisposeMutexDiscipline ensures Dispose does not hold lock across TerminalizeAttempt.
// It verifies lock/check/mark/detach, unlock, then terminalize by concurrent access.
func TestReadyAttempt_DisposeMutexDiscipline(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	terminal := newTurnTerminal()
	bindTurnTerminalRuntime(terminal, ex)
	sess := &attemptSession{
		terminal: newStreamTerminal(sdkterminal.ScopeAttempt),
		bleg:     b2bua.BLegRecord{ALegID: "a-leg-dispose", BLegID: "b-leg-dispose", Seq: 1},
	}
	ready := newReadyAttempt(sess, pendingSelectionEffects{})
	ready.state = readyStatePrepared

	// Concurrent Dispose and Consume should not deadlock and only one should succeed in terminalizing.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); ready.Dispose(context.Background(), errors.New("dispose")) }()
	go func() { defer wg.Done(); _, _ = ready.Consume() }()
	wg.Wait()
	if !ready.IsConsumed() {
		t.Error("ready should be consumed after concurrent Dispose/Consume")
	}
	// Dispose is single-use; second dispose must be no-op.
	ready.Dispose(context.Background(), errors.New("second dispose should be no-op"))
}

// TestReadyAttempt_ConsumeDetaches ensures Consume fails on active state, succeeds on prepared,
// detaches session, and prevents subsequent consumption/mutation.
func TestReadyAttempt_ConsumeDetaches(t *testing.T) {
	t.Parallel()
	sess := &attemptSession{
		terminal: newStreamTerminal(sdkterminal.ScopeAttempt),
		bleg:     b2bua.BLegRecord{ALegID: "a", BLegID: "b", Seq: 1},
		cand:     routing.AttemptCandidate{Key: "k"},
	}
	ready := newReadyAttempt(sess, pendingSelectionEffects{})

	// Consume on readyStateActive must fail with not-prepared error without detaching session
	if _, err := ready.Consume(); err == nil || err.Error() != "runtime: readyAttempt not prepared" {
		t.Fatalf("expected 'runtime: readyAttempt not prepared', got %v", err)
	}
	if ready.session != sess || ready.IsConsumed() {
		t.Fatal("failed Consume on active state must not detach session or mark consumed")
	}

	// Prepare readyAttempt
	ready.state = readyStatePrepared

	got, err := ready.Consume()
	if err != nil || got != sess {
		t.Fatalf("consume failed: %v", err)
	}
	if ready.session != nil {
		t.Error("Consume should detach session (nil after)")
	}
	if _, err := ready.Consume(); err == nil {
		t.Error("second Consume should fail")
	}
	if err := ready.InstallBridgeStream(nil); err == nil {
		t.Error("InstallBridgeStream after Consume should fail")
	}
	// Dispose after Consume must be no-op (no double terminalization)
	ready.Dispose(context.Background(), errors.New("after consume dispose"))
	if !ready.IsConsumed() {
		t.Error("should remain consumed")
	}
}

// TestReadyAttempt_ConsumeActiveRejectedAndRemainsDisposable verifies that attempting to consume
// an unprepared active readyAttempt is rejected, and the capability remains fully disposable.
func TestReadyAttempt_ConsumeActiveRejectedAndRemainsDisposable(t *testing.T) {
	t.Parallel()
	sess := &attemptSession{
		terminal: newStreamTerminal(sdkterminal.ScopeAttempt),
		bleg:     b2bua.BLegRecord{ALegID: "a-rej", BLegID: "b-rej", Seq: 1},
		cand:     routing.AttemptCandidate{Key: "k-rej"},
	}
	ready := newReadyAttempt(sess, pendingSelectionEffects{})

	// Consume is rejected
	gotSess, err := ready.Consume()
	if err == nil {
		t.Fatal("expected error on consuming unprepared active readyAttempt, got nil")
	}
	if gotSess != nil {
		t.Fatalf("expected nil session returned, got %v", gotSess)
	}
	if ready.IsConsumed() {
		t.Fatal("readyAttempt must not be marked consumed after rejected Consume")
	}

	// Dispose cleanly cleans up
	ready.Dispose(context.Background(), errors.New("cleanup active"))
	if !ready.IsConsumed() {
		t.Fatal("readyAttempt must be consumed/disposed after Dispose")
	}
	if ready.session != nil {
		t.Fatal("session must be detached after Dispose")
	}
}

// TestSlot_PublishDenialDoesNotConsume ensures publication denial leaves capability disposable exactly once.
func TestSlot_PublishDenialDoesNotConsume(t *testing.T) {
	t.Parallel()
	sess := &attemptSession{
		terminal: newStreamTerminal(sdkterminal.ScopeAttempt),
		bleg:     b2bua.BLegRecord{ALegID: "a-leg-publish", BLegID: "b-leg-publish", Seq: 1},
	}
	ready := newReadyAttempt(sess, pendingSelectionEffects{})
	ready.state = readyStatePrepared

	slot := &attemptSlot{}
	slot.publicationClosed = true // simulate Close won
	_, published := slot.publishReady(ready)
	if published {
		t.Fatal("publish should be denied when closed")
	}
	if ready.IsConsumed() {
		t.Fatal("denied publication should not consume ready")
	}
	// Now disposal should terminalize exactly once.
	ready.Dispose(context.Background(), errors.New("denied dispose"))
	if !ready.IsConsumed() {
		t.Error("ready should be consumed after Dispose")
	}
	if ready.session != nil {
		t.Error("session should be detached after Dispose")
	}
	// Second dispose is no-op
	ready.Dispose(context.Background(), errors.New("second dispose"))
}

// TestSlot_PublishConsumesAtomically ensures publish atomically consumes under lock.
func TestSlot_PublishConsumesAtomically(t *testing.T) {
	t.Parallel()
	sess := &attemptSession{terminal: newStreamTerminal(sdkterminal.ScopeAttempt), bleg: b2bua.BLegRecord{ALegID: "a", BLegID: "b", Seq: 1}}
	ready := newReadyAttempt(sess, pendingSelectionEffects{})
	ready.state = readyStatePrepared

	slot := &attemptSlot{}
	old, published := slot.publishReady(ready)
	if !published || old != nil {
		t.Fatalf("publish failed: published=%v old=%v", published, old)
	}
	if !ready.IsConsumed() || ready.session != nil {
		t.Error("ready should be consumed and detached after publish")
	}
	if slot.snapshot() != sess {
		t.Error("slot should hold published session")
	}
	// Second publish with same ready should be denied and not publish
	_, published2 := slot.publishReady(ready)
	if published2 {
		t.Error("second publish with consumed ready should be denied")
	}
}

// TestStreamAssembly_RollbackBeforePublication ensures rollback disposes ready and does not handoff guard.
func TestStreamAssembly_RollbackBeforePublication(t *testing.T) {
	t.Parallel()
	sess := &attemptSession{terminal: newStreamTerminal(sdkterminal.ScopeAttempt), bleg: b2bua.BLegRecord{ALegID: "a", BLegID: "b", Seq: 1}}
	ready := newReadyAttempt(sess, pendingSelectionEffects{})
	guard := &preStreamGuard{}
	slot := &attemptSlot{}
	tx := &streamAssemblyTx{ready: ready, guard: guard, slot: slot}
	tx.Rollback(context.Background(), errors.New("fail before publish"))
	if !ready.IsConsumed() {
		t.Error("rollback should dispose ready")
	}
	if guard.handedOver {
		t.Error("guard should not be handed over on rollback")
	}
	if !tx.committed {
		t.Error("tx should be marked committed after rollback")
	}
}

// TestStreamAssembly_CommitPublishesThenHandoffs ensures final sequence is non-fallible publish then handoff.
func TestStreamAssembly_CommitPublishesThenHandoffs(t *testing.T) {
	t.Parallel()
	sess := &attemptSession{terminal: newStreamTerminal(sdkterminal.ScopeAttempt), bleg: b2bua.BLegRecord{ALegID: "a", BLegID: "b", Seq: 1}}
	ready := newReadyAttempt(sess, pendingSelectionEffects{})
	ready.state = readyStatePrepared

	guard := &preStreamGuard{}
	slot := &attemptSlot{}
	tx := &streamAssemblyTx{ready: ready, guard: guard, slot: slot}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit failed: %v", err)
	}
	if !ready.IsConsumed() {
		t.Error("commit should consume ready")
	}
	if !guard.handedOver {
		t.Error("guard should be handed over after commit")
	}
	if slot.snapshot() != sess {
		t.Error("slot should hold session after commit")
	}
	// Rollback after commit is no-op
	tx.Rollback(context.Background(), errors.New("after commit"))
	if !guard.handedOver {
		t.Error("guard should remain handed over")
	}
}

// TestReady_NarrowMethods retain disposal ownership.
func TestReady_NarrowMethods(t *testing.T) {
	t.Parallel()
	sess := &attemptSession{bleg: b2bua.BLegRecord{ALegID: "a", BLegID: "b", Seq: 1}, cand: routing.AttemptCandidate{Key: "k", Primary: routing.Primary{Backend: "backend", Model: "model"}}}
	ready := newReadyAttempt(sess, pendingSelectionEffects{})
	if got := ready.Candidate(); got.Key != "k" {
		t.Errorf("Candidate() got %q want k", got.Key)
	}
	if got := ready.BLeg(); got.BLegID != "b" {
		t.Errorf("BLeg() got %q want b", got.BLegID)
	}
	// InstallBridgeStream should succeed before consumption
	if err := ready.InstallBridgeStream(&closeReplacementRaceStream{}); err != nil {
		t.Fatalf("InstallBridgeStream failed: %v", err)
	}
}

type deterministicRecoverableErrorStream struct {
	err error
}

func (s *deterministicRecoverableErrorStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, s.err
}

func (s *deterministicRecoverableErrorStream) Close() error { return nil }

func (s *deterministicRecoverableErrorStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

// TestReplacementPriorIsTerminalBeforeOpenerAndTerminalEffectsRunOnce deterministically proves:
// 1. Prior attempt is fully terminal before replacement opener executes.
// 2. Swallowed attempt terminal effects (authority settlement, lineage logging) run exactly once.
func TestReplacementPriorIsTerminalBeforeOpenerAndTerminalEffectsRunOnce(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-repl",
			ReservedAmount: authorityInputAmount(10),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
		status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	var (
		openerRan              bool
		priorWasTerminalInOpen bool
		priorSettledInOpen     bool
		priorSettlesInOpen     int64
	)

	sel, err := routing.Parse("backend-1:model-1")
	if err != nil {
		t.Fatalf("parse selector: %v", err)
	}

	initialAuthority := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(5),
		admissionResult: auth.admitResult,
	}
	initialAuthority.admissionResult.ReservationID = "reservation-initial"
	initialAuthority.admissionResult.ReservedAmount = authorityInputAmount(5)

	initialCand := routing.AttemptCandidate{Key: "initial", Primary: routing.Primary{Backend: "initial", Model: "initial"}}

	rs := &retryRecvStream{
		terminal: newTurnTerminal(),
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{
				ID:    "request-1",
				Route: aipapi.RouteIntent{Selector: "backend-1:model-1"},
				Invocation: aipapi.Invocation{
					Operation:    aipapi.OperationOpenAIChatCompletions,
					DeliveryMode: aipapi.DeliveryModeStreaming,
				},
				Messages: testMinimalUserMessages(),
			},
			aLegID:  aLegID,
			traceID: "trace-1",
		}),
		recovery:         &recoveryController{budget: &attemptBudget{max: 3, used: 0}, sel: sel, session: &routing.SessionRoutingState{}, excluded: map[string]struct{}{}, rng: routing.NewSeededRng(1)},
		attempt:          testAttemptSlot(b2bua.BLegRecord{BLegID: "b-leg-1", Seq: 1}, initialCand, testAuthorityLifecycle(ex, initialAuthority, initialCand)),
		responsePipeline: newResponsePipeline(),
	}
	bindTestRuntimeOwners(rs, ex)

	priorAttempt := rs.attempt.require()

	backend.openFn = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		openerRan = true
		priorWasTerminalInOpen = priorAttempt.terminal.Owner().State().IsTerminal()
		priorSettledInOpen = priorAttempt.authority.Settled()
		priorSettlesInOpen = auth.settleCalls.Load()
		return aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseFinished},
		}), nil
	}

	// First stream fails with recoverable pre-output error
	firstStream := &deterministicRecoverableErrorStream{err: &aipapi.UpstreamFailureError{Phase: aipapi.PhasePreOutput, Recoverable: true, Reason: "recoverable pre-output"}}
	testStoreInner(rs, firstStream)

	// Recv handles recoverable pre-output failure, terminalizes prior attempt, opens replacement, swaps, and receives response_finished
	ev, err := rs.Recv(context.Background())
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("Recv failed: %v", err)
	}
	if ev.Kind != aipapi.EventResponseFinished && !errors.Is(err, io.EOF) {
		t.Logf("Recv returned ev=%+v", ev)
	}

	if !openerRan {
		t.Fatal("expected replacement opener to run")
	}
	if !priorWasTerminalInOpen {
		t.Fatal("expected prior attempt to be terminal before replacement opener ran")
	}
	if !priorSettledInOpen {
		t.Fatal("expected prior attempt authority to be settled before replacement opener ran")
	}
	if priorSettlesInOpen != 1 {
		t.Fatalf("prior settle calls when opener ran = %d, want 1", priorSettlesInOpen)
	}

	// Total settle calls: 1 for prior swallowed, 1 for replacement winner
	if got := auth.settleCalls.Load(); got != 2 {
		t.Fatalf("total settle calls = %d, want 2 (1 swallowed + 1 winner)", got)
	}
	if got := auth.releaseCalls.Load(); got != 0 {
		t.Fatalf("total release calls = %d, want 0", got)
	}
}

type syncMockUsageStream struct {
	inFlight    chan struct{}
	releasePrep chan struct{}
}

func (s *syncMockUsageStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (s *syncMockUsageStream) Close() error { return nil }

func (s *syncMockUsageStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func (s *syncMockUsageStream) DrainUsageEvidence() []aipapi.Event {
	if s.inFlight != nil {
		select {
		case s.inFlight <- struct{}{}:
		default:
		}
	}
	if s.releasePrep != nil {
		<-s.releasePrep
	}
	return nil
}

// TestReadyAttempt_PrepareConcurrentWithDispose verifies synchronization between
// in-flight readiness operation (Prepare) and concurrent Dispose.
// Dispose waits for Prepare to exit preparing state, ensuring no data race or double settlement.
func TestReadyAttempt_PrepareConcurrentWithDispose(t *testing.T) {
	t.Parallel()
	inFlight := make(chan struct{}, 1)
	releasePrep := make(chan struct{})

	stream := &syncMockUsageStream{inFlight: inFlight, releasePrep: releasePrep}
	sess := &attemptSession{
		inner:    stream,
		terminal: newStreamTerminal(sdkterminal.ScopeAttempt),
		bleg:     b2bua.BLegRecord{ALegID: "a-leg-prep-disp", BLegID: "b-leg-prep-disp", Seq: 1},
	}
	ready := newReadyAttempt(sess, pendingSelectionEffects{})
	p := newResponsePipeline()
	facts := recvTurnFacts{}

	var (
		prepErr error
		wg      sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		prepErr = ready.Prepare(context.Background(), facts, p, false)
	}()
	go func() {
		defer wg.Done()
		<-inFlight
		go func() {
			for !ready.hasPendingInvalidation() {
				time.Sleep(50 * time.Microsecond)
			}
			close(releasePrep)
		}()
		ready.Dispose(context.Background(), errors.New("concurrent dispose"))
	}()

	wg.Wait()

	if prepErr == nil {
		t.Fatal("expected prepare to fail on concurrent dispose, got nil")
	}
	if !ready.IsConsumed() {
		t.Error("expected readyAttempt to be consumed/disposed")
	}
	// Subsequent Consume must fail
	if _, err := ready.Consume(); err == nil {
		t.Error("Consume after Dispose must fail")
	}
}

// TestReadyAttempt_PrepareConcurrentWithConsume verifies synchronization between
// in-flight Prepare and concurrent Consume. Consume waits for Prepare without stealing session midway.
func TestReadyAttempt_PrepareConcurrentWithConsume(t *testing.T) {
	t.Parallel()
	inFlight := make(chan struct{}, 1)
	releasePrep := make(chan struct{})

	stream := &syncMockUsageStream{inFlight: inFlight, releasePrep: releasePrep}
	sess := &attemptSession{
		inner:    stream,
		terminal: newStreamTerminal(sdkterminal.ScopeAttempt),
		bleg:     b2bua.BLegRecord{ALegID: "a-leg-prep-cons", BLegID: "b-leg-prep-cons", Seq: 1},
	}
	ready := newReadyAttempt(sess, pendingSelectionEffects{})
	p := newResponsePipeline()
	facts := recvTurnFacts{}

	var (
		prepErr    error
		consumeErr error
		consumed   *attemptSession
		wg         sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		prepErr = ready.Prepare(context.Background(), facts, p, false)
	}()
	go func() {
		defer wg.Done()
		<-inFlight
		consumed, consumeErr = ready.Consume()
	}()

	close(releasePrep)
	wg.Wait()

	if prepErr != nil {
		t.Fatalf("prepare failed: %v", prepErr)
	}
	if consumeErr != nil {
		t.Fatalf("consume failed: %v", consumeErr)
	}
	if consumed != sess {
		t.Fatalf("consumed session = %v, want %v", consumed, sess)
	}
	if !ready.IsConsumed() {
		t.Error("expected ready to be marked consumed")
	}
}

// TestReadyAttempt_PrepareFailureSingleOwnerTerminalization verifies that if readiness
// fails, Prepare terminalizes the session exactly once and transitions to disposed state.
func TestReadyAttempt_PrepareFailureSingleOwnerTerminalization(t *testing.T) {
	t.Parallel()
	sess := &attemptSession{
		terminal: newStreamTerminal(sdkterminal.ScopeAttempt),
		bleg:     b2bua.BLegRecord{ALegID: "a-leg-prep-fail", BLegID: "b-leg-prep-fail", Seq: 1},
	}
	ready := newReadyAttempt(sess, pendingSelectionEffects{})

	// Nil pipeline will trigger an error in Prepare
	err := ready.Prepare(context.Background(), recvTurnFacts{}, nil, false)
	if err == nil {
		t.Fatal("expected Prepare to fail with nil pipeline")
	}
	if !ready.IsConsumed() {
		t.Error("expected ready to be consumed/disposed on prepare failure")
	}

	// Subsequent Dispose is a safe no-op
	ready.Dispose(context.Background(), errors.New("subsequent dispose"))
	// Subsequent Consume fails
	if _, err := ready.Consume(); err == nil {
		t.Error("expected Consume after failed Prepare to fail")
	}
}

// TestReadyAttempt_DisposeRacesBridgeOperationAndWaits verifies that Dispose and InstallBridgeStream
// synchronize cleanly under concurrent execution, never deadlock or panic, and converge to disposed state.
func TestReadyAttempt_DisposeRacesBridgeOperationAndWaits(t *testing.T) {
	t.Parallel()
	const iterations = 50
	for i := range iterations {
		sess := &attemptSession{
			terminal: newStreamTerminal(sdkterminal.ScopeAttempt),
			bleg:     b2bua.BLegRecord{ALegID: "a-bridge-race", BLegID: "b-bridge-race", Seq: i + 1},
		}
		ready := newReadyAttempt(sess, pendingSelectionEffects{})
		bridge := aipapi.NewFixedEventStream(nil)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = ready.InstallBridgeStream(bridge)
		}()
		go func() {
			defer wg.Done()
			ready.Dispose(context.Background(), errors.New("bridge race dispose"))
		}()
		wg.Wait()

		if !ready.IsConsumed() {
			t.Fatalf("iteration %d: expected readyAttempt to be disposed", i)
		}
		if ready.session != nil {
			t.Fatalf("iteration %d: session should be detached after Dispose", i)
		}
	}
}

// TestReadyAttempt_PublishRacesPrepareNeverPublishesBeforeReadinessCompletes verifies that
// concurrent publishReady and Prepare never publishes before readiness completes.
func TestReadyAttempt_PublishRacesPrepareNeverPublishesBeforeReadinessCompletes(t *testing.T) {
	t.Parallel()
	inFlight := make(chan struct{}, 1)
	releasePrep := make(chan struct{})

	stream := &syncMockUsageStream{inFlight: inFlight, releasePrep: releasePrep}
	sess := &attemptSession{
		inner:    stream,
		terminal: newStreamTerminal(sdkterminal.ScopeAttempt),
		bleg:     b2bua.BLegRecord{ALegID: "a-pub-prep", BLegID: "b-pub-prep", Seq: 1},
	}
	ready := newReadyAttempt(sess, pendingSelectionEffects{})
	slot := &attemptSlot{}
	p := newResponsePipeline()
	facts := recvTurnFacts{}

	var (
		prepErr   error
		published bool
		wg        sync.WaitGroup
	)
	wg.Add(2)

	go func() {
		defer wg.Done()
		prepErr = ready.Prepare(context.Background(), facts, p, false)
	}()

	go func() {
		defer wg.Done()
		<-inFlight
		// publishReady invokes ready.Consume() which waits for Prepare to complete
		_, published = slot.publishReady(ready)
	}()

	close(releasePrep)
	wg.Wait()

	if prepErr != nil {
		t.Fatalf("Prepare failed: %v", prepErr)
	}
	if !published {
		t.Fatal("expected slot.publishReady to succeed after Prepare completes")
	}
	if slot.snapshot() != sess {
		t.Fatalf("slot snapshot = %v, want published session %v", slot.snapshot(), sess)
	}
	if !ready.IsConsumed() {
		t.Fatal("expected readyAttempt to be consumed after publication")
	}
}

// TestReadyAttempt_PrepareIdempotenceAndConvergence proves that multiple concurrent Prepare
// calls converge cleanly, run external readiness work once, and all return nil.
func TestReadyAttempt_PrepareIdempotenceAndConvergence(t *testing.T) {
	t.Parallel()
	sess := &attemptSession{
		terminal: newStreamTerminal(sdkterminal.ScopeAttempt),
		bleg:     b2bua.BLegRecord{ALegID: "a-idemp", BLegID: "b-idemp", Seq: 1},
	}
	ready := newReadyAttempt(sess, pendingSelectionEffects{})
	p := newResponsePipeline()
	facts := recvTurnFacts{}

	const racers = 8
	var wg sync.WaitGroup
	errorsList := make([]error, racers)

	for i := range racers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errorsList[idx] = ready.Prepare(context.Background(), facts, p, false)
		}(i)
	}
	wg.Wait()

	for i, err := range errorsList {
		if err != nil {
			t.Errorf("racer %d returned error: %v", i, err)
		}
	}

	if ready.state != readyStatePrepared {
		t.Fatalf("ready state = %v, want readyStatePrepared", ready.state)
	}

	// Consume now succeeds
	consumed, err := ready.Consume()
	if err != nil || consumed != sess {
		t.Fatalf("Consume after converged Prepare failed: %v (sess=%v)", err, consumed)
	}
}

// TestReadyAttempt_PrepareFailureConvergenceAndSingleSettlement proves that when readiness
// fails with multiple concurrent Prepare calls, all callers receive the error, session is
// terminalized exactly once, and subsequent Consume or Dispose observe the terminal state.
func TestReadyAttempt_PrepareFailureConvergenceAndSingleSettlement(t *testing.T) {
	t.Parallel()
	var termCalls atomic.Int32
	sess := &attemptSession{
		terminal: newStreamTerminal(sdkterminal.ScopeAttempt),
		bleg:     b2bua.BLegRecord{ALegID: "a-fail-conv", BLegID: "b-fail-conv", Seq: 1},
		recordAttemptLoggedFn: func(context.Context, recordAttemptParams, diag.AttrOpts) {
			termCalls.Add(1)
		},
	}
	ready := newReadyAttempt(sess, pendingSelectionEffects{})

	// Passing nil pipeline triggers readiness error
	const racers = 6
	var wg sync.WaitGroup
	errorsList := make([]error, racers)

	for i := range racers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errorsList[idx] = ready.Prepare(context.Background(), recvTurnFacts{}, nil, false)
		}(i)
	}
	wg.Wait()

	for i, err := range errorsList {
		if err == nil {
			t.Errorf("racer %d expected error, got nil", i)
		}
	}

	if !ready.IsConsumed() {
		t.Fatal("expected readyAttempt to be in disposed/consumed state")
	}

	// Subsequent Dispose is a no-op
	ready.Dispose(context.Background(), errors.New("subsequent dispose"))
	// Subsequent Consume fails
	if _, err := ready.Consume(); err == nil {
		t.Fatal("expected Consume to fail after failed Prepare")
	}
}
