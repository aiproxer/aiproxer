package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

type countingStreamObserver struct {
	opens    *atomic.Int32
	finishes *atomic.Int32
}

func (o *countingStreamObserver) Observe(context.Context, aipapi.Event) error {
	return nil
}

func (o *countingStreamObserver) Finish(context.Context, response.StreamOutcome) error {
	if o.finishes != nil {
		o.finishes.Add(1)
	}
	return nil
}

type countingObserverFactory struct {
	opens    *atomic.Int32
	finishes *atomic.Int32
	onOpen   func(ctx context.Context)
}

func (f *countingObserverFactory) ID() string                        { return "counting-obs" }
func (f *countingObserverFactory) Order() int                        { return 0 }
func (f *countingObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (f *countingObserverFactory) Open(ctx context.Context, _ response.StreamMeta, _ response.Services) (response.StreamObserver, error) {
	if f.opens != nil {
		f.opens.Add(1)
	}
	if f.onOpen != nil {
		f.onOpen(ctx)
	}
	return &countingStreamObserver{opens: f.opens, finishes: f.finishes}, nil
}

// TestReviewerSchedule_CancelAfterRegisterBeforePrepare verifies that if A-leg is canceled
// after RegisterBLeg succeeds but before Prepare runs:
// 1. Construct/open tx/session/ready.
// 2. Register ready.lifecycleHandle() successfully with a live A-leg.
// 3. Prove Register returned success and ready state Active; pause before Prepare.
// 4. Cancel A-leg from test.
// 5. Resume/call Prepare and attempt slot publication.
// Assert:
// - Prepare fails (refuses disposed)
// - publishReady returns false
// - backend Cancel and Close are each invoked exactly once
// - observer Open count is 0 and Finish count is 0
// - authority, B-leg release, and billing append executed once as applicable
// - ready is Disposed
// - A-leg handle Close after Cancel is no-op
func TestReviewerSchedule_CancelAfterRegisterBeforePrepare(t *testing.T) {
	t.Parallel()

	// 1. Construct/open tx/session/ready
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aLeg := coord.StartALeg("aleg-sched-1")

	backendStream := newNonIdempotentBackendStream()

	var openCalls atomic.Int32
	var finishCalls atomic.Int32
	obsFactory := &countingObserverFactory{
		opens:    &openCalls,
		finishes: &finishCalls,
	}

	snap := extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			StreamObserverFactories: []response.StreamObserverFactory{obsFactory},
		}),
	})
	pipe := &responsePipeline{
		runtimeSnapshot: snap,
	}

	svc := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Reserved:       true,
			ReservationID:  "res-1",
			SelectedRuleID: "rule-1",
		},
	}
	state := attemptAuthorityState{
		admissionInput: usagelimit.AdmissionInput{
			Correlation: controlplane.Correlation{TraceID: "trace-sched-1", ALegID: "aleg-sched-1", BLegID: "bleg-sched-1", RequestID: "req-sched-1"},
		},
		admissionResult: usagelimit.AdmissionResult{
			Reserved:       true,
			ReservationID:  "res-1",
			SelectedRuleID: "rule-1",
		},
	}
	ex := TestExecutor()
	ex.UsageAuthority = svc
	lc := ex.newAttemptAuthorityLifecycle(state, routing.AttemptCandidate{Key: "backend:m", Primary: routing.Primary{Backend: "backend", Model: "m"}})
	lc.backendAttempted.Store(true)

	var billingAppends atomic.Int32
	sess := newAttemptSession(attemptSessionInput{
		inner:          backendStream,
		bleg:           b2bua.BLegRecord{ALegID: "aleg-sched-1", BLegID: "bleg-sched-1", Seq: 1},
		cand:           routing.AttemptCandidate{Key: "backend:m", Primary: routing.Primary{Backend: "backend", Model: "m"}},
		authority:      lc,
		aScope:         aLeg,
		finalStreamObs: &extensions.FinalStreamObservationSession{},
		appendBillingLegFn: func(ctx context.Context, bleg b2bua.BLegRecord, p routing.Primary, started, ended time.Time, outcome usageevidence.LegOutcome) {
			billingAppends.Add(1)
		},
	})

	ready := newReadyAttempt(sess, pendingSelectionEffects{})
	handle := ready.lifecycleHandle()

	// 2. Register ready.lifecycleHandle() successfully with a live A-leg
	regErr := aLeg.RegisterBLeg(context.Background(), leglifecycle.BLegHandle{
		ID:      sess.bleg.BLegID,
		Attempt: handle,
	})
	if regErr != nil {
		t.Fatalf("RegisterBLeg failed on live A-leg: %v", regErr)
	}

	// 3. Prove Register returned success and ready state Active; pause before Prepare
	ready.mu.Lock()
	stateBeforeCancel := ready.state
	ready.mu.Unlock()
	if stateBeforeCancel != readyStateActive {
		t.Fatalf("expected ready state Active before cancel, got %v", stateBeforeCancel)
	}

	// 4. Cancel A-leg from test
	cancelCause := leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit, Detail: "reviewer cancel"}
	if err := coord.CancelALeg(context.Background(), "aleg-sched-1", cancelCause); err != nil {
		t.Fatalf("CancelALeg failed: %v", err)
	}

	// 5. Resume/call Prepare and attempt slot publication
	facts := recvTurnFacts{aLegID: "aleg-sched-1", traceID: "trace-sched-1"}
	prepErr := ready.Prepare(context.Background(), facts, pipe, false)
	if prepErr == nil {
		t.Fatal("expected Prepare to fail on canceled/disposed readyAttempt, got nil")
	}

	slot := &attemptSlot{}
	publishedSess, published := slot.swapIfOpen(ready)
	if published {
		t.Fatalf("expected publish to return false on canceled/disposed readyAttempt, got true (sess=%v)", publishedSess)
	}

	// Assert backend Cancel and Close are each invoked exactly once
	calls := backendStream.getCalls()
	if backendStream.cancelCount.Load() != 1 || backendStream.closeCount.Load() != 1 {
		t.Fatalf("backendStream cancelCount=%d closeCount=%d, want 1/1, calls: %v",
			backendStream.cancelCount.Load(), backendStream.closeCount.Load(), calls)
	}

	// Assert observer Open count is 0 and Finish count is 0
	if openCalls.Load() != 0 {
		t.Fatalf("observer Open count = %d, want 0", openCalls.Load())
	}
	if finishCalls.Load() != 0 {
		t.Fatalf("observer Finish count = %d, want 0", finishCalls.Load())
	}

	// Assert authority/B-leg/billing once as applicable
	if totalAuth := svc.settleCalls.Load() + svc.releaseCalls.Load(); totalAuth != 1 {
		t.Fatalf("authority settlements/releases count = %d (settles=%d, releases=%d), want 1",
			totalAuth, svc.settleCalls.Load(), svc.releaseCalls.Load())
	}
	if billingAppends.Load() != 1 {
		t.Fatalf("billing appends count = %d, want 1", billingAppends.Load())
	}

	// Assert ready Disposed
	ready.mu.Lock()
	stateAfter := ready.state
	ready.mu.Unlock()
	if stateAfter != readyStateDisposed {
		t.Fatalf("expected ready state Disposed, got %v", stateAfter)
	}

	// Ensure A-leg handle Close after Cancel is no-op
	if err := handle.Close(); err != nil {
		t.Fatalf("handle.Close() after cancel returned error: %v", err)
	}
	if backendStream.cancelCount.Load() != 1 || backendStream.closeCount.Load() != 1 {
		t.Fatalf("backendStream cancelCount=%d closeCount=%d after handle.Close(), want 1/1",
			backendStream.cancelCount.Load(), backendStream.closeCount.Load())
	}
}

// TestReviewerSchedule_CancelDuringPrepareCooperation verifies that if A-leg cancellation arrives
// while Prepare is executing in-flight observer Open:
// - Cancellation records pending invalidation before waiting on cond
// - In-flight Open returns; Prepare observes pending invalidation and fails (never marks prepared)
// - Cancellation disposes readyAttempt and terminalizes the attempt exactly once
// - Observer Open count is 1, Finish count is 1 (clean teardown of opened observer)
// - Backend Cancel and Close are each invoked exactly once
// - Publication fails (Consume cannot publish disposed attempt)
func TestReviewerSchedule_CancelDuringPrepareCooperation(t *testing.T) {
	t.Parallel()
	backendStream := newNonIdempotentBackendStream()

	var openCalls atomic.Int32
	var finishCalls atomic.Int32
	obsOpenEntered := make(chan struct{})
	allowObsOpenFinish := make(chan struct{})

	obsFactory := &countingObserverFactory{
		opens:    &openCalls,
		finishes: &finishCalls,
		onOpen: func(ctx context.Context) {
			close(obsOpenEntered)
			<-allowObsOpenFinish
		},
	}

	snap := extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			StreamObserverFactories: []response.StreamObserverFactory{obsFactory},
		}),
	})

	pipe := &responsePipeline{
		runtimeSnapshot: snap,
	}

	sess := &attemptSession{
		terminal:       newStreamTerminal(sdkterminal.ScopeAttempt),
		bleg:           b2bua.BLegRecord{ALegID: "aleg-during-prep", BLegID: "bleg-during-prep", Seq: 1},
		cand:           routing.AttemptCandidate{Key: "cand-during-prep", Primary: routing.Primary{Backend: "backend", Model: "m"}},
		finalStreamObs: &extensions.FinalStreamObservationSession{},
	}
	sess.storeInner(backendStream)

	ready := newReadyAttempt(sess, pendingSelectionEffects{})
	handle := ready.lifecycleHandle()

	prepErrCh := make(chan error, 1)
	go func() {
		facts := recvTurnFacts{aLegID: "aleg-during-prep", traceID: "trace-during-prep"}
		prepErrCh <- ready.Prepare(context.Background(), facts, pipe, false)
	}()

	select {
	case <-obsOpenEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for observer Open to be entered")
	}

	// Now cancel A-leg while Prepare has opInFlight = true
	cancelStarted := make(chan struct{})
	cancelDone := make(chan struct{})
	go func() {
		close(cancelStarted)
		handle.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: "cancel during prepare"})
		close(cancelDone)
	}()

	select {
	case <-cancelStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Cancel goroutine to start")
	}

	// Confirm pending invalidation is recorded before releasing Open
	deadline := time.Now().Add(5 * time.Second)
	for !ready.hasPendingInvalidation() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for pending invalidation to be recorded")
		}
		time.Sleep(10 * time.Microsecond)
	}

	// Deterministic assertion: while Prepare's observer Open is blocked on allowObsOpenFinish,
	// Cancel must be blocked waiting on cond and cannot have returned.
	select {
	case <-cancelDone:
		t.Fatal("Cancel must not return before in-flight Prepare finishes")
	default:
	}

	// Allow Prepare to complete openFinalStreamObservation
	close(allowObsOpenFinish)

	select {
	case prepErr := <-prepErrCh:
		// Prepare must observe pending invalidation and fail (never transition to prepared)
		if prepErr == nil {
			t.Fatal("expected Prepare to fail on attempt canceled during Prepare, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Prepare to finish")
	}

	select {
	case <-cancelDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Cancel to finish")
	}

	// Consume must fail
	if _, err := ready.Consume(); err == nil {
		t.Fatal("Consume must fail on attempt canceled during Prepare")
	}

	if openCalls.Load() != 1 {
		t.Fatalf("observer Open count = %d, want 1", openCalls.Load())
	}
	if finishCalls.Load() != 1 {
		t.Fatalf("observer Finish count = %d, want 1 (opened observer must be finished by TerminalizeAttempt)", finishCalls.Load())
	}

	calls := backendStream.getCalls()
	if backendStream.cancelCount.Load() != 1 || backendStream.closeCount.Load() != 1 {
		t.Fatalf("backendStream cancelCount=%d closeCount=%d, want 1/1, calls: %v", backendStream.cancelCount.Load(), backendStream.closeCount.Load(), calls)
	}
}

// TestReviewerSchedule_CancelDuringPreparePendingInvalidation_RaceConsume verifies across 100 iterations:
// - Observer Open is blocked while in flight in Prepare
// - Cancel starts and records pending invalidation under mutex
// - Open is released and Prepare finishes while Consume immediately races
// - Assert: Consume always fails, slot publish is never true, observer Open=1/Finish=1, cleanup runs exactly once
func TestReviewerSchedule_CancelDuringPreparePendingInvalidation_RaceConsume(t *testing.T) {
	t.Parallel()

	for i := range 100 {
		backendStream := newNonIdempotentBackendStream()

		var openCalls atomic.Int32
		var finishCalls atomic.Int32
		obsOpenEntered := make(chan struct{})
		allowObsOpenFinish := make(chan struct{})

		obsFactory := &countingObserverFactory{
			opens:    &openCalls,
			finishes: &finishCalls,
			onOpen: func(ctx context.Context) {
				close(obsOpenEntered)
				<-allowObsOpenFinish
			},
		}

		snap := extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{
			FeaturePlanes: freezeBundle(testFeatureBundle{
				StreamObserverFactories: []response.StreamObserverFactory{obsFactory},
			}),
		})

		pipe := &responsePipeline{
			runtimeSnapshot: snap,
		}

		sess := &attemptSession{
			terminal:       newStreamTerminal(sdkterminal.ScopeAttempt),
			bleg:           b2bua.BLegRecord{ALegID: "aleg-race-100", BLegID: "bleg-race-100", Seq: 1},
			cand:           routing.AttemptCandidate{Key: "cand-race-100", Primary: routing.Primary{Backend: "backend", Model: "m"}},
			finalStreamObs: &extensions.FinalStreamObservationSession{},
		}
		sess.storeInner(backendStream)

		ready := newReadyAttempt(sess, pendingSelectionEffects{})
		handle := ready.lifecycleHandle()

		prepErrCh := make(chan error, 1)
		go func() {
			facts := recvTurnFacts{aLegID: "aleg-race-100", traceID: "trace-race-100"}
			prepErrCh <- ready.Prepare(context.Background(), facts, pipe, false)
		}()

		select {
		case <-obsOpenEntered:
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: timed out waiting for observer Open to be entered", i)
		}

		cancelDone := make(chan struct{})
		go func() {
			handle.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: "race cancel during prepare"})
			close(cancelDone)
		}()

		// Wait deterministically for pending invalidation to be recorded under mutex
		deadline := time.Now().Add(5 * time.Second)
		for !ready.hasPendingInvalidation() {
			if time.Now().After(deadline) {
				t.Fatalf("iteration %d: timed out waiting for pending invalidation", i)
			}
			time.Sleep(10 * time.Microsecond)
		}

		// Ensure cancel is still waiting for in-flight Open
		select {
		case <-cancelDone:
			t.Fatalf("iteration %d: Cancel returned prematurely while Open was still in flight", i)
		default:
		}

		// Unblock Open
		close(allowObsOpenFinish)

		// Concurrently race Consume / swapIfOpen
		var consumeErr error
		var consumedSess *attemptSession
		var published bool
		consumeDone := make(chan struct{})
		slot := &attemptSlot{}

		go func() {
			defer close(consumeDone)
			_, published = slot.swapIfOpen(ready)
			consumedSess, consumeErr = ready.Consume()
		}()

		select {
		case prepErr := <-prepErrCh:
			if prepErr == nil {
				t.Fatalf("iteration %d: expected Prepare to fail on canceled attempt, got nil", i)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: timed out waiting for Prepare", i)
		}

		select {
		case <-cancelDone:
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: timed out waiting for Cancel", i)
		}

		select {
		case <-consumeDone:
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: timed out waiting for Consume", i)
		}

		if published {
			t.Fatalf("iteration %d: swapIfOpen must not publish canceled attempt", i)
		}
		if consumedSess != nil || consumeErr == nil {
			t.Fatalf("iteration %d: Consume must fail on canceled attempt, got sess=%v err=%v", i, consumedSess, consumeErr)
		}
		if openCalls.Load() != 1 {
			t.Fatalf("iteration %d: observer Open count = %d, want 1", i, openCalls.Load())
		}
		if finishCalls.Load() != 1 {
			t.Fatalf("iteration %d: observer Finish count = %d, want 1", i, finishCalls.Load())
		}
		if backendStream.cancelCount.Load() != 1 || backendStream.closeCount.Load() != 1 {
			t.Fatalf("iteration %d: backendStream cancelCount=%d closeCount=%d, want 1/1",
				i, backendStream.cancelCount.Load(), backendStream.closeCount.Load())
		}
	}
}

// TestReviewerSchedule_CloseDuringPrepare verifies that lifecycle Close during Prepare:
// - Records pending invalidation while Open is in flight
// - Prevents Prepare from transitioning to Prepared
// - Prevents Consume and slot publication
// - Observer Open=1, Finish=1
// - Backend Close=1, Cancel=0/1
func TestReviewerSchedule_CloseDuringPrepare(t *testing.T) {
	t.Parallel()
	backendStream := newNonIdempotentBackendStream()

	var openCalls atomic.Int32
	var finishCalls atomic.Int32
	obsOpenEntered := make(chan struct{})
	allowObsOpenFinish := make(chan struct{})

	obsFactory := &countingObserverFactory{
		opens:    &openCalls,
		finishes: &finishCalls,
		onOpen: func(ctx context.Context) {
			close(obsOpenEntered)
			<-allowObsOpenFinish
		},
	}

	snap := extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			StreamObserverFactories: []response.StreamObserverFactory{obsFactory},
		}),
	})

	pipe := &responsePipeline{
		runtimeSnapshot: snap,
	}

	sess := &attemptSession{
		terminal:       newStreamTerminal(sdkterminal.ScopeAttempt),
		bleg:           b2bua.BLegRecord{ALegID: "aleg-close-prep", BLegID: "bleg-close-prep", Seq: 1},
		cand:           routing.AttemptCandidate{Key: "cand-close-prep", Primary: routing.Primary{Backend: "backend", Model: "m"}},
		finalStreamObs: &extensions.FinalStreamObservationSession{},
	}
	sess.storeInner(backendStream)

	ready := newReadyAttempt(sess, pendingSelectionEffects{})
	handle := ready.lifecycleHandle()

	prepErrCh := make(chan error, 1)
	go func() {
		facts := recvTurnFacts{aLegID: "aleg-close-prep", traceID: "trace-close-prep"}
		prepErrCh <- ready.Prepare(context.Background(), facts, pipe, false)
	}()

	select {
	case <-obsOpenEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for observer Open to be entered")
	}

	closeDone := make(chan struct{})
	go func() {
		_ = handle.Close()
		close(closeDone)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for !ready.hasPendingInvalidation() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for pending invalidation to be recorded")
		}
		time.Sleep(10 * time.Microsecond)
	}

	select {
	case <-closeDone:
		t.Fatal("Close returned prematurely while Open was still in flight")
	default:
	}

	close(allowObsOpenFinish)

	select {
	case prepErr := <-prepErrCh:
		if prepErr == nil {
			t.Fatal("expected Prepare to fail on closed attempt, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Prepare to finish")
	}

	select {
	case <-closeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Close to finish")
	}

	if _, err := ready.Consume(); err == nil {
		t.Fatal("Consume must fail on attempt closed during Prepare")
	}

	if openCalls.Load() != 1 {
		t.Fatalf("observer Open count = %d, want 1", openCalls.Load())
	}
	if finishCalls.Load() != 1 {
		t.Fatalf("observer Finish count = %d, want 1", finishCalls.Load())
	}
	if backendStream.closeCount.Load() != 1 {
		t.Fatalf("backendStream closeCount=%d, want 1", backendStream.closeCount.Load())
	}
}

// TestReviewerSchedule_CancelDuringInstallBridgeStream verifies that if Cancel arrives during
// InstallBridgeStream, the attempt is invalidated, Consume fails, and cleanup runs exactly once.
func TestReviewerSchedule_CancelDuringInstallBridgeStream(t *testing.T) {
	t.Parallel()
	backendStream := newNonIdempotentBackendStream()
	bridgeStream := newNonIdempotentBackendStream()

	sess := &attemptSession{
		terminal: newStreamTerminal(sdkterminal.ScopeAttempt),
		bleg:     b2bua.BLegRecord{ALegID: "aleg-bridge-cancel", BLegID: "bleg-bridge-cancel", Seq: 1},
		cand:     routing.AttemptCandidate{Key: "cand-bridge-cancel"},
	}
	sess.storeInner(backendStream)

	ready := newReadyAttempt(sess, pendingSelectionEffects{})
	handle := ready.lifecycleHandle()

	// Cancel before or during bridge install
	handle.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: "cancel before bridge"})

	if err := ready.InstallBridgeStream(bridgeStream); err == nil {
		t.Fatal("expected InstallBridgeStream to fail on canceled readyAttempt, got nil")
	}

	if _, err := ready.Consume(); err == nil {
		t.Fatal("Consume must fail on canceled readyAttempt")
	}

	if backendStream.cancelCount.Load() != 1 || backendStream.closeCount.Load() != 1 {
		t.Fatalf("backendStream cancelCount=%d closeCount=%d, want 1/1",
			backendStream.cancelCount.Load(), backendStream.closeCount.Load())
	}
}

// TestReviewerSchedule_CloseDuringInstallBridgeStream verifies that if Close arrives before/during
// InstallBridgeStream, the attempt is invalidated, Consume fails, and cleanup runs exactly once.
func TestReviewerSchedule_CloseDuringInstallBridgeStream(t *testing.T) {
	t.Parallel()
	backendStream := newNonIdempotentBackendStream()
	bridgeStream := newNonIdempotentBackendStream()

	sess := &attemptSession{
		terminal: newStreamTerminal(sdkterminal.ScopeAttempt),
		bleg:     b2bua.BLegRecord{ALegID: "aleg-bridge-close", BLegID: "bleg-bridge-close", Seq: 1},
		cand:     routing.AttemptCandidate{Key: "cand-bridge-close"},
	}
	sess.storeInner(backendStream)

	ready := newReadyAttempt(sess, pendingSelectionEffects{})
	handle := ready.lifecycleHandle()

	_ = handle.Close()

	if err := ready.InstallBridgeStream(bridgeStream); err == nil {
		t.Fatal("expected InstallBridgeStream to fail on closed readyAttempt, got nil")
	}

	if _, err := ready.Consume(); err == nil {
		t.Fatal("Consume must fail on closed readyAttempt")
	}

	if backendStream.closeCount.Load() != 1 {
		t.Fatalf("backendStream closeCount=%d, want 1", backendStream.closeCount.Load())
	}
}

// TestReviewerSchedule_CancelVsConsumeLinearizability verifies the linearizable race between
// Consume and Cancel: exactly one wins ready ownership, and post-Consume Cancel delegates to session.
func TestReviewerSchedule_CancelVsConsumeLinearizability(t *testing.T) {
	t.Parallel()

	for i := range 50 {
		backendStream := newNonIdempotentBackendStream()
		sess := &attemptSession{
			terminal: newStreamTerminal(sdkterminal.ScopeAttempt),
			bleg:     b2bua.BLegRecord{ALegID: "aleg-race", BLegID: "bleg-race", Seq: 1},
			cand:     routing.AttemptCandidate{Key: "k"},
		}
		sess.storeInner(backendStream)

		ready := newReadyAttempt(sess, pendingSelectionEffects{})
		if err := ready.Prepare(context.Background(), recvTurnFacts{}, &responsePipeline{}, false); err != nil {
			t.Fatalf("iteration %d: Prepare failed: %v", i, err)
		}
		handle := ready.lifecycleHandle()

		var wg sync.WaitGroup
		wg.Add(2)

		var consumedSess *attemptSession
		var consumeErr error

		go func() {
			defer wg.Done()
			consumedSess, consumeErr = ready.Consume()
		}()

		go func() {
			defer wg.Done()
			handle.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: "race cancel"})
		}()

		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: timed out waiting for racers", i)
		}

		// In either outcome, backend stream must be canceled exactly once and closed exactly once
		calls := backendStream.getCalls()
		if backendStream.cancelCount.Load() != 1 || backendStream.closeCount.Load() != 1 {
			t.Fatalf("iteration %d: backendStream cancelCount=%d closeCount=%d, want 1/1, calls: %v (consumed=%v, err=%v)",
				i, backendStream.cancelCount.Load(), backendStream.closeCount.Load(), calls, consumedSess != nil, consumeErr)
		}
	}
}
