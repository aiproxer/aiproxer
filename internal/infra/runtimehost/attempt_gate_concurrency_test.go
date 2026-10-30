package runtimehost

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/configreload"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

// Task 6.2 deterministic concurrency suite against the production AttemptGate.
// Barriers and channels only — no wall-clock synchronization.
// Context timeouts appear only as post-barrier deadlock guards.
// A past time.Time used solely to build an already-expired deadline is allowed.

func TestAttemptGate_BusyAPIConflictNoPending(t *testing.T) {
	t.Parallel()
	g := newAttemptGate()
	admitted := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if admitted.Kind != admissionAdmitted || admitted.Lease == nil {
		t.Fatalf("setup admit=%+v", admitted)
	}
	defer admitted.Lease.Complete()

	res := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if res.Kind != admissionBusyAPI {
		t.Fatalf("API while busy kind=%v want busy API", res.Kind)
	}
	if res.Category != sdkreload.ResultBusy {
		t.Fatalf("API while busy category=%q want busy", res.Category)
	}
	if res.ReasonCategory != configreload.StageBusy {
		t.Fatalf("API while busy reason=%q want %q", res.ReasonCategory, configreload.StageBusy)
	}
	if res.CoalescedSignals != 0 {
		t.Fatalf("API busy coalesced on outcome=%d want 0", res.CoalescedSignals)
	}
	if res.Lease != nil {
		t.Fatal("API busy must not return a live lease")
	}
	st := g.Snapshot()
	if !st.Busy {
		t.Fatal("must remain busy")
	}
	if st.PendingSignal {
		t.Fatal("API busy must not queue a pending follow-up (req 6.9)")
	}
	if st.CoalescedSignals != 0 {
		t.Fatalf("API busy must not coalesce; got %d", st.CoalescedSignals)
	}
}

func TestAttemptGate_CoalesceBoundedPending(t *testing.T) {
	t.Parallel()
	g := newAttemptGate()
	admitted := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if admitted.Kind != admissionAdmitted || admitted.Lease == nil {
		t.Fatalf("setup admit=%+v", admitted)
	}
	defer admitted.Lease.Complete()

	first := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
	if first.Kind != admissionPendingHUP {
		t.Fatalf("first HUP kind=%v", first.Kind)
	}
	if first.Category != sdkreload.ResultBusy || first.ReasonCategory != configreload.StageCoalesce {
		t.Fatalf("first HUP=%+v", first)
	}
	if first.CoalescedSignals != 0 {
		t.Fatalf("first HUP outcome coalesced=%d want 0", first.CoalescedSignals)
	}
	if first.Lease != nil {
		t.Fatal("first HUP must not return a live lease")
	}
	st := g.Snapshot()
	if !st.PendingSignal {
		t.Fatal("first HUP must create at most one pending follow-up (req 6.8)")
	}
	if st.CoalescedSignals != 0 {
		t.Fatalf("first HUP coalesced=%d want 0", st.CoalescedSignals)
	}

	second := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
	if second.Kind != admissionCoalescedHUP {
		t.Fatalf("second HUP kind=%v", second.Kind)
	}
	if second.Category != sdkreload.ResultBusy || second.ReasonCategory != configreload.StageCoalesce {
		t.Fatalf("second HUP=%+v", second)
	}
	if second.CoalescedSignals != 1 {
		t.Fatalf("second HUP coalesced signals=%d want 1", second.CoalescedSignals)
	}
	if second.Lease != nil {
		t.Fatal("coalesced HUP must not return a live lease")
	}
	st = g.Snapshot()
	if !st.PendingSignal {
		t.Fatal("pending must remain singular")
	}
	if st.CoalescedSignals != 1 {
		t.Fatalf("coalesced count=%d want 1", st.CoalescedSignals)
	}

	third := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
	if third.Kind != admissionCoalescedHUP || third.CoalescedSignals != 2 {
		t.Fatalf("third HUP=%+v", third)
	}
	st = g.Snapshot()
	if !st.PendingSignal || st.CoalescedSignals != 2 {
		t.Fatalf("bounded coalesce status=%+v", st)
	}
}

func TestAttemptGate_AtomicArmNoBusyWithoutLease(t *testing.T) {
	t.Parallel()
	g := newAttemptGate()
	outcome := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if outcome.Kind != admissionAdmitted {
		t.Fatalf("admit kind=%v", outcome.Kind)
	}
	if outcome.Lease == nil {
		t.Fatal("admitted outcome must carry a live lease")
	}
	st := g.Snapshot()
	if !st.Busy {
		t.Fatal("busy must be observable only with an armed lease")
	}
	// Under the gate lock, busy and active lease are the same transition.
	g.mu.Lock()
	busy := g.active != nil
	armed := g.active != nil && g.idleNotify != nil
	select {
	case <-g.idleNotify:
		g.mu.Unlock()
		t.Fatal("idle notify must be open (armed) while busy")
	default:
	}
	g.mu.Unlock()
	if !busy || !armed {
		t.Fatal("busy must never be observable without armed completion notification")
	}
	fin := outcome.Lease.Complete()
	if fin.Kind != finishReleasedIdle {
		t.Fatalf("finish=%v", fin.Kind)
	}
}

func TestAttemptGate_ImmutableAdmissionMetadataNoLease(t *testing.T) {
	t.Parallel()
	g := newAttemptGate()
	admitted := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if admitted.Lease == nil {
		t.Fatal("setup")
	}
	defer admitted.Lease.Complete()

	busy := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if busy.Lease != nil {
		t.Fatal("non-admitted busy outcome must not carry a live lease")
	}
	busy.CoalescedSignals = 99 // mutate local copy only
	again := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
	if again.Lease != nil || again.CoalescedSignals != 0 {
		t.Fatalf("immutable outcome / pending first HUP=%+v", again)
	}
	st := g.Snapshot()
	if st.CoalescedSignals != 0 {
		t.Fatalf("mutating prior outcome must not affect gate; coalesced=%d", st.CoalescedSignals)
	}
}

func TestAttemptGate_FinishClaimsPendingFollowUp(t *testing.T) {
	t.Parallel()
	g := newAttemptGate()
	admitted := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if admitted.Lease == nil {
		t.Fatal("setup")
	}

	_ = g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
	_ = g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
	_ = g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
	if got := g.Snapshot(); !got.PendingSignal || got.CoalescedSignals != 2 {
		t.Fatalf("pre-finish snap=%+v", got)
	}

	// Idle waiters must remain blocked across the follow-up claim (no idle gap).
	bctx, reached := newWaitIdleSelectBarrierCtx(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- g.WaitForIdle(bctx)
	}()
	awaitWaitIdleBarriers(t, reached)

	fin := admitted.Lease.Complete()
	if fin.Kind != finishFollowUpClaimed {
		t.Fatalf("finish kind=%v want follow-up", fin.Kind)
	}
	if fin.FollowUpLease == nil {
		t.Fatal("follow-up lease required")
	}
	if fin.CoalescedSignals != 2 {
		t.Fatalf("claimed coalesced=%d want 2", fin.CoalescedSignals)
	}
	st := g.Snapshot()
	if !st.Busy {
		t.Fatal("gate must remain non-idle after follow-up claim")
	}
	if st.PendingSignal || st.CoalescedSignals != 0 {
		t.Fatalf("pending/count must reset on claim; snap=%+v", st)
	}

	// Waiter must still be blocked — no admission/idle gap.
	select {
	case err := <-errCh:
		t.Fatalf("idle waiter woke across follow-up claim: %v", err)
	default:
	}

	// SIGHUP during follow-up may reserve exactly one next follow-up.
	during := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
	if during.Kind != admissionPendingHUP || during.Lease != nil {
		t.Fatalf("HUP during follow-up=%+v", during)
	}
	if got := g.Snapshot(); !got.PendingSignal || got.CoalescedSignals != 0 {
		t.Fatalf("during follow-up snap=%+v", got)
	}

	final := fin.FollowUpLease.Complete()
	if final.Kind != finishFollowUpClaimed || final.FollowUpLease == nil {
		t.Fatalf("second follow-up claim=%+v", final)
	}
	released := final.FollowUpLease.Complete()
	if released.Kind != finishReleasedIdle {
		t.Fatalf("final release=%v", released.Kind)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("idle after final release: %v", err)
	}
}

func TestAttemptGate_ShutdownRejectsAndCancels(t *testing.T) {
	t.Parallel()
	g := newAttemptGate()
	admitted := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if admitted.Lease == nil {
		t.Fatal("setup")
	}
	_ = g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
	_ = g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
	if got := g.Snapshot(); !got.PendingSignal || got.CoalescedSignals != 1 {
		t.Fatalf("pre-shutdown snap=%+v", got)
	}

	g.BeginShutdown()

	select {
	case <-admitted.Lease.Context().Done():
	default:
		t.Fatal("BeginShutdown must cancel active attempt lease")
	}
	st := g.Snapshot()
	if st.PendingSignal || st.CoalescedSignals != 0 {
		t.Fatalf("BeginShutdown must clear pending/count; snap=%+v", st)
	}

	late := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if late.Kind != admissionRejectedShutdown || late.Lease != nil {
		t.Fatalf("post-shutdown API=%+v", late)
	}
	if late.Category != sdkreload.ResultCanceled || late.ReasonCategory != configreload.StageShutdown {
		t.Fatalf("post-shutdown API metadata=%+v", late)
	}
	lateHUP := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
	if lateHUP.Kind != admissionRejectedShutdown || lateHUP.Lease != nil {
		t.Fatalf("post-shutdown HUP=%+v", lateHUP)
	}

	fin := admitted.Lease.Complete()
	if fin.Kind != finishReleasedIdle {
		t.Fatalf("shutdown finish must release idle (pending cleared); got %v", fin.Kind)
	}
}

func TestAttemptGate_ExactFinishSequentialDuplicate(t *testing.T) {
	t.Parallel()
	g := newAttemptGate()
	admitted := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if admitted.Lease == nil {
		t.Fatal("setup")
	}
	lease := admitted.Lease

	first := lease.Complete()
	if first.Kind != finishReleasedIdle {
		t.Fatalf("first complete=%v", first.Kind)
	}
	second := lease.Complete()
	if second.Kind != finishAlreadyCompleted {
		t.Fatalf("second complete=%v want already-completed", second.Kind)
	}
	third := lease.Complete()
	if third.Kind != finishAlreadyCompleted || third.FollowUpLease != nil {
		t.Fatalf("third complete=%+v", third)
	}

	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			out := lease.Complete()
			if out.Kind != finishAlreadyCompleted {
				t.Errorf("dup complete kind=%v", out.Kind)
			}
		})
	}
	wg.Wait()
}

func TestAttemptGate_ConcurrentDuplicateComplete(t *testing.T) {
	t.Parallel()
	const goroutines = 64
	for range 50 {
		g := newAttemptGate()
		admitted := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
		if admitted.Lease == nil {
			t.Fatal("setup")
		}
		_ = g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})

		var releases, followUps, already atomic.Int64
		var wg sync.WaitGroup
		wg.Add(goroutines)
		start := make(chan struct{})
		for range goroutines {
			go func() {
				defer wg.Done()
				<-start
				out := admitted.Lease.Complete()
				switch out.Kind {
				case finishReleasedIdle:
					releases.Add(1)
				case finishFollowUpClaimed:
					followUps.Add(1)
					if out.FollowUpLease != nil {
						_ = out.FollowUpLease.Complete()
					}
				case finishAlreadyCompleted:
					already.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()

		if releases.Load()+followUps.Load() != 1 {
			t.Fatalf("want exactly one release or follow-up; releases=%d followUps=%d already=%d",
				releases.Load(), followUps.Load(), already.Load())
		}
		if already.Load() != int64(goroutines-1) {
			t.Fatalf("already=%d want %d", already.Load(), goroutines-1)
		}
		if err := g.WaitForIdle(context.Background()); err != nil {
			t.Fatalf("idle: %v", err)
		}
	}
}

func TestAttemptGate_IdleWaitersWake(t *testing.T) {
	t.Parallel()
	g := newAttemptGate()
	admitted := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if admitted.Lease == nil {
		t.Fatal("setup")
	}

	const waiters = 8
	reached := make([]<-chan struct{}, waiters)
	errs := make(chan error, waiters)
	var wg sync.WaitGroup
	wg.Add(waiters)
	for i := range waiters {
		bctx, ch := newWaitIdleSelectBarrierCtx(context.Background())
		reached[i] = ch
		go func(ctx context.Context) {
			defer wg.Done()
			errs <- g.WaitForIdle(ctx)
		}(bctx)
	}
	awaitWaitIdleBarriers(t, reached...)

	if fin := admitted.Lease.Complete(); fin.Kind != finishReleasedIdle {
		t.Fatalf("finish=%v", fin.Kind)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("waiter err=%v", err)
		}
	}
}

func TestAttemptGate_IdleAlreadyIdle(t *testing.T) {
	t.Parallel()
	g := newAttemptGate()
	if err := g.WaitForIdle(context.Background()); err != nil {
		t.Fatalf("already idle: %v", err)
	}
	if err := g.WaitForIdle(
		nil, //nolint:staticcheck // SA1012: intentional nil context contract
	); err != nil {
		t.Fatalf("nil ctx idle: %v", err)
	}
}

func TestAttemptGate_IdleCanceledContext(t *testing.T) {
	t.Parallel()
	g := newAttemptGate()
	admitted := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if admitted.Lease == nil {
		t.Fatal("setup")
	}
	defer admitted.Lease.Complete()

	ctx, ctxCancel := context.WithCancel(context.Background())
	ctxCancel()
	err := g.WaitForIdle(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled WaitForIdle err=%v want context.Canceled", err)
	}
	select {
	case <-admitted.Lease.Context().Done():
		t.Fatal("wait cancel must not cancel active lease")
	default:
	}
}

func TestAttemptGate_IdleDeadlineExceeded(t *testing.T) {
	t.Parallel()
	g := newAttemptGate()
	admitted := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if admitted.Lease == nil {
		t.Fatal("setup")
	}
	defer admitted.Lease.Complete()

	ctx, ctxCancel := context.WithDeadline(context.Background(), time.Time{})
	defer ctxCancel()
	err := g.WaitForIdle(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired-deadline WaitForIdle err=%v want context.DeadlineExceeded", err)
	}
}

func TestAttemptGate_IdleAfterActiveFinish(t *testing.T) {
	t.Parallel()
	g := newAttemptGate()
	admitted := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if admitted.Lease == nil {
		t.Fatal("setup")
	}

	bctx, reached := newWaitIdleSelectBarrierCtx(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- g.WaitForIdle(bctx)
	}()
	awaitWaitIdleBarriers(t, reached)

	if fin := admitted.Lease.Complete(); fin.Kind != finishReleasedIdle {
		t.Fatalf("finish=%v", fin.Kind)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("active→finish WaitForIdle: %v", err)
	}
}

func TestAttemptGate_ShutdownCancelsIdleWaiters(t *testing.T) {
	t.Parallel()
	g := newAttemptGate()
	admitted := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if admitted.Lease == nil {
		t.Fatal("setup")
	}

	bctx, reached := newWaitIdleSelectBarrierCtx(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- g.WaitForIdle(bctx)
	}()
	awaitWaitIdleBarriers(t, reached)

	g.BeginShutdown()
	select {
	case <-admitted.Lease.Context().Done():
	default:
		t.Fatal("shutdown must cancel lease context")
	}
	// Shutdown alone must not wake idle waiters — final Complete does.
	select {
	case err := <-errCh:
		t.Fatalf("idle woke on shutdown alone: %v", err)
	default:
	}
	if fin := admitted.Lease.Complete(); fin.Kind != finishReleasedIdle {
		t.Fatalf("finish=%v", fin.Kind)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("idle after shutdown+finish: %v", err)
	}
}

func TestAttemptGate_ShutdownFinalizerRunsFromInitiallyIdleState(t *testing.T) {
	t.Parallel()
	g := newAttemptGate()
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	wantErr := errors.New("source close failed")
	g.SetShutdownFinalizer(func() error {
		calls.Add(1)
		close(entered)
		<-release
		return wantErr
	})
	beginDone := make(chan struct{})
	beginStarted := make(chan struct{})
	go func() {
		close(beginStarted)
		g.BeginShutdown()
		close(beginDone)
	}()
	<-beginStarted
	guard, cancelGuard := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelGuard()
	select {
	case <-entered:
	case <-guard.Done():
		t.Fatal("initially-idle finalizer did not start")
	}

	bctx, reached := newWaitIdleSelectBarrierCtx(context.Background())
	waitDone := make(chan error, 1)
	go func() { waitDone <- g.WaitForIdle(bctx) }()
	awaitWaitIdleBarriers(t, reached)
	select {
	case err := <-waitDone:
		t.Fatalf("WaitForIdle returned before source finalization: %v", err)
	default:
	}
	close(release)
	<-beginDone
	if err := <-waitDone; !errors.Is(err, wantErr) {
		t.Fatalf("WaitForIdle finalization error=%v want %v", err, wantErr)
	}
	g.BeginShutdown()
	if err := g.WaitForIdle(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("cached finalization error=%v want %v", err, wantErr)
	}
	if calls.Load() != 1 {
		t.Fatalf("finalizer calls=%d want once", calls.Load())
	}
}

func TestAttemptGate_ShutdownFinalizerWaitsForAdmittedAttempt(t *testing.T) {
	t.Parallel()
	g := newAttemptGate()
	entered, release := make(chan struct{}), make(chan struct{})
	g.SetShutdownFinalizer(func() error {
		close(entered)
		<-release
		return nil
	})
	admitted := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if admitted.Lease == nil {
		t.Fatal("setup admission")
	}
	bctx, reached := newWaitIdleSelectBarrierCtx(context.Background())
	waitDone := make(chan error, 1)
	go func() { waitDone <- g.WaitForIdle(bctx) }()
	awaitWaitIdleBarriers(t, reached)
	g.BeginShutdown()
	select {
	case <-entered:
		t.Fatal("source finalizer ran while an admitted attempt was active")
	default:
	}
	completeDone := make(chan struct{})
	completeStarted := make(chan struct{})
	go func() {
		close(completeStarted)
		admitted.Lease.Complete()
		close(completeDone)
	}()
	<-completeStarted
	guard, cancelGuard := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelGuard()
	select {
	case <-entered:
	case <-guard.Done():
		t.Fatal("finalizer did not start after attempt completion")
	}
	select {
	case err := <-waitDone:
		t.Fatalf("WaitForIdle returned before finalization completed: %v", err)
	default:
	}
	close(release)
	<-completeDone
	if err := <-waitDone; err != nil {
		t.Fatalf("WaitForIdle: %v", err)
	}
}

func TestAttemptGate_ShutdownFinalizerPanicWakesConcurrentWaitersOnce(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		admit bool
	}{
		{name: "initially idle"},
		{name: "after admitted attempt", admit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := newAttemptGate()
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			releaseFinalizer := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseFinalizer()
			var finalizerCalls atomic.Int32
			g.SetShutdownFinalizer(func() error {
				finalizerCalls.Add(1)
				close(entered)
				<-release
				panic("private callback failure")
			})

			var lease *attemptLease
			if tc.admit {
				admission := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
				if admission.Lease == nil {
					t.Fatal("setup admission")
				}
				lease = admission.Lease
			}

			const waiters = 6
			barriers := make([]<-chan struct{}, waiters)
			waitResults := make(chan error, waiters)
			startWaiters := func() {
				for i := range waiters {
					ctx, reached := newWaitIdleSelectBarrierCtx(context.Background())
					barriers[i] = reached
					go func(ctx context.Context) { waitResults <- g.WaitForIdle(ctx) }(ctx)
				}
				awaitWaitIdleBarriers(t, barriers...)
			}

			var shutdownDone, completeDone chan struct{}
			if tc.admit {
				startWaiters()
				g.BeginShutdown()
				completeDone = make(chan struct{})
				go func() {
					lease.Complete()
					close(completeDone)
				}()
			} else {
				shutdownDone = make(chan struct{})
				go func() {
					g.BeginShutdown()
					close(shutdownDone)
				}()
				guard, cancelGuard := context.WithTimeout(context.Background(), 5*time.Second)
				select {
				case <-entered:
				case <-guard.Done():
					cancelGuard()
					t.Fatal("shutdown finalizer did not start")
				}
				cancelGuard()
				startWaiters()
			}
			if tc.admit {
				guard, cancelGuard := context.WithTimeout(context.Background(), 5*time.Second)
				select {
				case <-entered:
				case <-guard.Done():
					cancelGuard()
					t.Fatal("shutdown finalizer did not start")
				}
				cancelGuard()
			}

			deadlineCtx := newGateDeadlineBarrierContext()
			deadlineResult := make(chan error, 1)
			go func() { deadlineResult <- g.WaitForIdle(deadlineCtx) }()
			awaitWaitIdleBarriers(t, deadlineCtx.reached)
			deadlineCtx.expire()
			if err := <-deadlineResult; !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("WaitForIdle deadline while finalizing=%v want deadline exceeded", err)
			}
			for range waiters {
				select {
				case err := <-waitResults:
					t.Fatalf("WaitForIdle returned before the panicking finalizer completed: %v", err)
				default:
				}
			}

			releaseFinalizer()
			if shutdownDone != nil {
				awaitAttemptGateCompletion(t, shutdownDone, "BeginShutdown")
			}
			if completeDone != nil {
				awaitAttemptGateCompletion(t, completeDone, "attempt completion")
			}
			var cached error
			for range waiters {
				err := awaitAttemptGateWaitResult(t, waitResults)
				if err == nil || err.Error() != "runtimehost: source finalization failed" {
					t.Fatalf("waiter error=%v want bounded finalization failure", err)
				}
				if cached == nil {
					cached = err
				} else if err != cached {
					t.Fatalf("waiters received different cached finalization errors: %p and %p", err, cached)
				}
			}

			g.BeginShutdown()
			if lease != nil {
				lease.Complete()
			}
			if err := g.WaitForIdle(context.Background()); err != cached {
				t.Fatalf("repeated shutdown returned %v; want cached error %v", err, cached)
			}
			if finalizerCalls.Load() != 1 {
				t.Fatalf("finalizer calls=%d want once", finalizerCalls.Load())
			}
		})
	}
}

func TestAttemptGate_RepeatedConcurrentShutdown(t *testing.T) {
	t.Parallel()
	g := newAttemptGate()
	admitted := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if admitted.Lease == nil {
		t.Fatal("setup")
	}

	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			g.BeginShutdown()
		})
	}
	wg.Wait()
	g.BeginShutdown()
	g.BeginShutdown()

	select {
	case <-admitted.Lease.Context().Done():
	default:
		t.Fatal("shutdown must cancel")
	}
	if fin := admitted.Lease.Complete(); fin.Kind != finishReleasedIdle {
		t.Fatalf("finish=%v", fin.Kind)
	}
	late := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if late.Kind != admissionRejectedShutdown {
		t.Fatalf("late=%v", late.Kind)
	}
}

func TestAttemptGate_CallerCancelDetachment(t *testing.T) {
	t.Parallel()
	g := newAttemptGate()
	type ctxKey struct{}
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "keep"))
	admitted := g.TryStart(parent, sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if admitted.Lease == nil {
		t.Fatal("setup")
	}
	leaseCtx := admitted.Lease.Context()
	if leaseCtx.Value(ctxKey{}) != "keep" {
		t.Fatal("caller context values must remain available on host-owned attempt context")
	}
	cancel()
	select {
	case <-leaseCtx.Done():
		t.Fatal("caller cancel must not cancel host-owned attempt context")
	default:
	}

	deadlineParent, deadlineCancel := context.WithDeadline(context.Background(), time.Time{})
	defer deadlineCancel()
	// Already-expired parent must still admit a live host context.
	secondGate := newAttemptGate()
	second := secondGate.TryStart(deadlineParent, sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if second.Kind != admissionAdmitted || second.Lease == nil {
		t.Fatalf("admit under expired parent=%+v", second)
	}
	select {
	case <-second.Lease.Context().Done():
		t.Fatal("caller deadline must not own admitted attempt context")
	default:
	}
	_ = second.Lease.Complete()
	_ = admitted.Lease.Complete()
}

func TestAttemptGate_InterleavingsOneCompleteVsWaitShutdown(t *testing.T) {
	t.Parallel()
	const iters = 100
	for i := range iters {
		g := newAttemptGate()
		admitted := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
		if admitted.Lease == nil {
			t.Fatal("setup")
		}

		var wg sync.WaitGroup
		var waitErrs atomic.Int64

		for range 4 {
			wg.Go(func() {
				if err := g.WaitForIdle(context.Background()); err != nil {
					waitErrs.Add(1)
				}
			})
		}

		var hammer sync.WaitGroup
		hammer.Go(func() {
			_ = g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
			_ = g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
			_ = g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
		})
		hammer.Wait()

		wg.Go(func() {
			g.BeginShutdown()
		})

		wg.Go(func() {
			out := admitted.Lease.Complete()
			if out.Kind == finishFollowUpClaimed && out.FollowUpLease != nil {
				_ = out.FollowUpLease.Complete()
			}
		})

		wg.Wait()
		if waitErrs.Load() != 0 {
			t.Fatalf("iter %d: WaitForIdle errors=%d", i, waitErrs.Load())
		}
		if err := g.WaitForIdle(context.Background()); err != nil {
			t.Fatalf("iter %d idle: %v", i, err)
		}
		late := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
		if late.Kind != admissionRejectedShutdown {
			t.Fatalf("iter %d late start=%v", i, late.Kind)
		}
	}
}

func TestAttemptGate_CoordinatorAPIStatusParity(t *testing.T) {
	t.Parallel()
	// Coordinator busy/coalesce results must retain categories and status projection.
	c := &Coordinator{gate: newAttemptGate(), timeout: time.Minute, state: newReloadState(reloadStateInitial{})}
	admitted := c.gate.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if admitted.Lease == nil {
		t.Fatal("setup")
	}
	defer admitted.Lease.Complete()

	res := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if res.Category != sdkreload.ResultBusy || res.ReasonCategory != configreload.StageBusy {
		t.Fatalf("coord API busy=%+v", res)
	}
	st := c.Status()
	if !st.Busy || st.PendingSignal || st.CoalescedSignals != 0 {
		t.Fatalf("coord status API busy=%+v", st)
	}

	hup := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
	if hup.Category != sdkreload.ResultBusy || hup.ReasonCategory != configreload.StageCoalesce || hup.CoalescedSignals != 0 {
		t.Fatalf("coord first HUP=%+v", hup)
	}
	st = c.Status()
	if !st.Busy || !st.PendingSignal || st.CoalescedSignals != 0 {
		t.Fatalf("coord status pending=%+v", st)
	}
	hup2 := c.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
	if hup2.CoalescedSignals != 1 {
		t.Fatalf("coord second HUP coalesced=%d", hup2.CoalescedSignals)
	}
	st = c.Status()
	if !st.PendingSignal || st.CoalescedSignals != 1 {
		t.Fatalf("coord status coalesced=%+v", st)
	}
}

func TestAttemptGate_AbandonReleasesWithoutFollowUp(t *testing.T) {
	t.Parallel()
	g := newAttemptGate()
	admitted := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if admitted.Lease == nil {
		t.Fatal("setup")
	}
	_ = g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
	_ = g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
	if got := g.Snapshot(); !got.PendingSignal || got.CoalescedSignals != 1 {
		t.Fatalf("pre-abandon snap=%+v", got)
	}

	bctx, reached := newWaitIdleSelectBarrierCtx(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- g.WaitForIdle(bctx)
	}()
	awaitWaitIdleBarriers(t, reached)

	admitted.Lease.Abandon()

	select {
	case <-admitted.Lease.Context().Done():
	default:
		t.Fatal("Abandon must cancel host-owned lease context")
	}
	st := g.Snapshot()
	if st.Busy || st.PendingSignal || st.CoalescedSignals != 0 {
		t.Fatalf("Abandon must clear active/pending/coalesced without follow-up; snap=%+v", st)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("idle after Abandon: %v", err)
	}

	// Complete after Abandon is a panic-free no-op and must not claim follow-up.
	fin := admitted.Lease.Complete()
	if fin.Kind != finishAlreadyCompleted || fin.FollowUpLease != nil {
		t.Fatalf("Complete after Abandon=%+v", fin)
	}
	// Deferred Abandon after a normal final Complete is an idempotent no-op.
	second := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if second.Lease == nil {
		t.Fatal("re-admit")
	}
	if fin := second.Lease.Complete(); fin.Kind != finishReleasedIdle {
		t.Fatalf("final complete=%v", fin.Kind)
	}
	second.Lease.Abandon()
	second.Lease.Abandon()
	if err := g.WaitForIdle(context.Background()); err != nil {
		t.Fatalf("idle after noop Abandon: %v", err)
	}
}

func TestAttemptGate_CompleteVsAbandonRace(t *testing.T) {
	t.Parallel()
	const goroutines = 64
	for range 50 {
		g := newAttemptGate()
		admitted := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
		if admitted.Lease == nil {
			t.Fatal("setup")
		}
		_ = g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})

		var releases, followUps, already atomic.Int64
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(goroutines)
		for i := range goroutines {
			go func(i int) {
				defer wg.Done()
				<-start
				if i%2 == 0 {
					out := admitted.Lease.Complete()
					switch out.Kind {
					case finishReleasedIdle:
						releases.Add(1)
					case finishFollowUpClaimed:
						followUps.Add(1)
						if out.FollowUpLease != nil {
							out.FollowUpLease.Abandon()
						}
					case finishAlreadyCompleted:
						already.Add(1)
					}
					return
				}
				admitted.Lease.Abandon()
			}(i)
		}
		close(start)
		wg.Wait()

		winners := releases.Load() + followUps.Load()
		if winners > 1 {
			t.Fatalf("want at most one Complete advance/release; releases=%d followUps=%d already=%d",
				releases.Load(), followUps.Load(), already.Load())
		}
		if err := g.WaitForIdle(context.Background()); err != nil {
			t.Fatalf("idle after Complete/Abandon race: %v", err)
		}
		if g.Snapshot().Busy {
			t.Fatal("gate must not leak active lease after Complete/Abandon race")
		}
		// Idle notify must remain closed (already-idle WaitForIdle) — no second close panic.
		if err := g.WaitForIdle(context.Background()); err != nil {
			t.Fatalf("second idle wait: %v", err)
		}
	}
}

func TestAttemptGate_DuplicateAbandonRace(t *testing.T) {
	t.Parallel()
	const goroutines = 64
	for range 50 {
		g := newAttemptGate()
		admitted := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
		if admitted.Lease == nil {
			t.Fatal("setup")
		}
		_ = g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})
		_ = g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})

		bctx, reached := newWaitIdleSelectBarrierCtx(context.Background())
		errCh := make(chan error, 1)
		go func() {
			errCh <- g.WaitForIdle(bctx)
		}()
		awaitWaitIdleBarriers(t, reached)

		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(goroutines)
		for range goroutines {
			go func() {
				defer wg.Done()
				<-start
				admitted.Lease.Abandon()
			}()
		}
		close(start)
		wg.Wait()

		if err := <-errCh; err != nil {
			t.Fatalf("idle waiter after duplicate Abandon: %v", err)
		}
		st := g.Snapshot()
		if st.Busy || st.PendingSignal || st.CoalescedSignals != 0 {
			t.Fatalf("duplicate Abandon must release once without follow-up; snap=%+v", st)
		}
		admitted.Lease.Abandon()
		fin := admitted.Lease.Complete()
		if fin.Kind != finishAlreadyCompleted || fin.FollowUpLease != nil {
			t.Fatalf("post-abandon complete=%+v", fin)
		}
	}
}

func TestAttemptGate_PanicCleanupAbandon(t *testing.T) {
	t.Parallel()
	g := newAttemptGate()
	admitted := g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if admitted.Lease == nil {
		t.Fatal("setup")
	}
	lease := admitted.Lease
	_ = g.TryStart(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerSIGHUP})

	bctx, reached := newWaitIdleSelectBarrierCtx(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- g.WaitForIdle(bctx)
	}()
	awaitWaitIdleBarriers(t, reached)

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected simulated panic")
			}
		}()
		defer lease.Abandon()
		panic("simulated post-admission panic outside runAttempt")
	}()

	if err := <-errCh; err != nil {
		t.Fatalf("WaitForIdle must unblock after panic Abandon: %v", err)
	}
	st := g.Snapshot()
	if st.Busy || st.PendingSignal || st.CoalescedSignals != 0 {
		t.Fatalf("panic Abandon must not leak gate state; snap=%+v", st)
	}
	select {
	case <-lease.Context().Done():
	default:
		t.Fatal("panic Abandon must cancel lease context")
	}
}

// waitIdleSelectBarrierCtx is a deterministic observation barrier for WaitForIdle.
// Done() signals once when WaitForIdle has entered the armed select (after reading
// the active completion channel) and returns a never-closed channel so only the
// completion signal (or a wrapping parent cancel, unused here) can unblock.
type waitIdleSelectBarrierCtx struct {
	parent  context.Context
	reached chan struct{}
	once    sync.Once
	never   chan struct{}
}

type gateDeadlineBarrierContext struct {
	reached chan struct{}
	done    chan struct{}
	once    sync.Once
}

func newGateDeadlineBarrierContext() *gateDeadlineBarrierContext {
	return &gateDeadlineBarrierContext{reached: make(chan struct{}), done: make(chan struct{})}
}

func (c *gateDeadlineBarrierContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *gateDeadlineBarrierContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.reached) })
	return c.done
}

func (c *gateDeadlineBarrierContext) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}
func (*gateDeadlineBarrierContext) Value(any) any { return nil }
func (c *gateDeadlineBarrierContext) expire()     { close(c.done) }

func awaitAttemptGateCompletion(t *testing.T, done <-chan struct{}, label string) {
	t.Helper()
	guard, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case <-done:
	case <-guard.Done():
		t.Fatalf("%s did not complete after finalizer release", label)
	}
}

func awaitAttemptGateWaitResult(t *testing.T, results <-chan error) error {
	t.Helper()
	guard, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case err := <-results:
		return err
	case <-guard.Done():
		t.Fatal("WaitForIdle caller was not notified after finalization")
		return nil
	}
}

func newWaitIdleSelectBarrierCtx(parent context.Context) (*waitIdleSelectBarrierCtx, <-chan struct{}) {
	if parent == nil {
		parent = context.Background()
	}
	reached := make(chan struct{})
	return &waitIdleSelectBarrierCtx{
		parent:  parent,
		reached: reached,
		never:   make(chan struct{}),
	}, reached
}

func (c *waitIdleSelectBarrierCtx) Deadline() (time.Time, bool) { return c.parent.Deadline() }

func (c *waitIdleSelectBarrierCtx) Done() <-chan struct{} {
	c.once.Do(func() { close(c.reached) })
	return c.never
}

func (c *waitIdleSelectBarrierCtx) Err() error { return nil }

func (c *waitIdleSelectBarrierCtx) Value(key any) any { return c.parent.Value(key) }

// awaitWaitIdleBarriers waits until every waiter has reached WaitForIdle's armed
// select. Context timeout is only a post-setup deadlock guard — not synchronization.
func awaitWaitIdleBarriers(t *testing.T, reached ...<-chan struct{}) {
	t.Helper()
	guard, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i, ch := range reached {
		select {
		case <-ch:
		case <-guard.Done():
			t.Fatalf("deadlock: waiter %d did not reach WaitForIdle armed select barrier", i)
		}
	}
}
