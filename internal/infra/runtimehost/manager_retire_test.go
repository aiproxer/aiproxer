package runtimehost_test

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
)

// Task 7.3: Manager owns retirement scheduling (retireGeneration in
// retire.go). LifecycleWorker is gone; Publish auto-schedules one bounded
// background retirement per replaced generation, and Manager.RetireGeneration
// is the synchronous retry/wait counterpart used by callers/shutdown.

type ledgerOwned struct {
	quiesceFn func(context.Context) error
	closeFn   func() error

	quiesces atomic.Int32
	closes   atomic.Int32
}

func (l *ledgerOwned) Quiesce(ctx context.Context) error {
	l.quiesces.Add(1)
	if l.quiesceFn != nil {
		return l.quiesceFn(ctx)
	}
	return nil
}

func (l *ledgerOwned) Close() error {
	l.closes.Add(1)
	if l.closeFn != nil {
		return l.closeFn()
	}
	return nil
}

// retireWait bounds every wait for an asynchronous retirement step to finish.
// The happy path returns as soon as the step completes; the bound only matters
// when a test would already fail, so it is generous enough for a loaded CI
// runner (a 2 s bound flaked on main under shard load).
const retireWait = 30 * time.Second

// awaitClosed waits for generation close without sleeps: RetireGeneration
// serializes behind any in-flight auto-retirement via context-aware admission,
// then observes GenClosed / ErrAlreadyClosed.
func awaitClosed(t *testing.T, m *runtimehost.Manager, g *runtimehost.Generation) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), retireWait)
	defer cancel()
	_, err := m.RetireGeneration(ctx, g)
	if err != nil && !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("awaitClosed: %v (lifecycle=%v)", err, g.Lifecycle())
	}
	if g.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("awaitClosed: lifecycle=%v want GenClosed", g.Lifecycle())
	}
}

// 1. Publish replacement auto-retires old via quiesce/drain/close.
func TestManagerRetire_PublishReplacementAutoRetiresOld(t *testing.T) {
	t.Parallel()
	closeDone := make(chan struct{})
	owned := &ledgerOwned{closeFn: func() error { close(closeDone); return nil }}
	m := runtimehost.NewManager(2, nil)
	g1 := m.PrepareOwned("g1", owned)
	mustPublish(t, m, g1)
	mustPublish(t, m, m.Prepare("g2"))

	select {
	case <-closeDone:
	case <-time.After(retireWait):
		t.Fatal("publish replacement did not auto-retire old generation")
	}
	awaitClosed(t, m, g1)
	if owned.quiesces.Load() != 1 || owned.closes.Load() != 1 {
		t.Fatalf("quiesces=%d closes=%d want 1/1", owned.quiesces.Load(), owned.closes.Load())
	}
}

// 2 & 3. Publish returns before pinned old drains; releasing pin completes retirement.
func TestManagerRetire_PublishReturnsBeforePinnedOldDrains_ReleaseCompletes(t *testing.T) {
	t.Parallel()
	closeDone := make(chan struct{})
	owned := &ledgerOwned{closeFn: func() error { close(closeDone); return nil }}
	m := runtimehost.NewManager(2, nil)
	g1 := m.PrepareOwned("g1", owned)
	mustPublish(t, m, g1)
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}

	publishDone := make(chan struct{})
	go func() {
		mustPublish(t, m, m.Prepare("g2"))
		close(publishDone)
	}()
	select {
	case <-publishDone:
	case <-time.After(retireWait):
		t.Fatal("publish must not wait for pinned drain/cleanup")
	}
	// Lease is still held: close must not have happened yet regardless of
	// whether the background auto-retire has already started quiescing.
	select {
	case <-closeDone:
		t.Fatal("must not close while pinned")
	default:
	}

	lease.Release()
	select {
	case <-closeDone:
	case <-time.After(retireWait):
		t.Fatal("releasing pin must complete auto-retirement")
	}
	awaitClosed(t, m, g1)
	if owned.closes.Load() != 1 {
		t.Fatalf("closes=%d want 1", owned.closes.Load())
	}
}

// 4. Close fail then success obeys policy attempts.
func TestManagerRetire_CloseFailThenSucceedsObeysPolicyAttempts(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(2, nil)
	m.SetCleanupPolicy(runtimehost.CleanupPolicy{MaxAttempts: 3})
	closeErr := errors.New("cleanup-temp")
	var closes atomic.Int32
	closeDone := make(chan struct{})
	owned := &ledgerOwned{closeFn: func() error {
		if closes.Add(1) == 1 {
			return closeErr
		}
		close(closeDone)
		return nil
	}}
	g1 := m.PrepareOwned("g1", owned)
	mustPublish(t, m, g1)
	mustPublish(t, m, m.Prepare("g2"))

	select {
	case <-closeDone:
	case <-time.After(retireWait):
		t.Fatal("retry-to-success timeout")
	}
	awaitClosed(t, m, g1)
	if closes.Load() != 2 {
		t.Fatalf("closes=%d want 2 (fail then success)", closes.Load())
	}
}

// 5. Exhausted cleanup retains GenClosing; explicit Manager.RetireGeneration later closes.
// Driven synchronously via DetachActive (no auto-schedule) so the exhausted
// attempt completes before the GenClosing assertion without sleeps/polls.
func TestManagerRetire_ExhaustedCleanupRetainsClosing_ExplicitRetryCloses(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(2, nil)
	m.SetCleanupPolicy(runtimehost.CleanupPolicy{MaxAttempts: 2})
	closeErr := errors.New("cleanup-permanent")
	var closes atomic.Int32
	var succeed atomic.Bool
	owned := &ledgerOwned{closeFn: func() error {
		closes.Add(1)
		if succeed.Load() {
			return nil
		}
		return closeErr
	}}
	g1 := m.PrepareOwned("g1", owned)
	mustPublish(t, m, g1)
	m.BeginShutdown()
	m.DetachActive()

	status, err := m.RetireGeneration(context.Background(), g1)
	if !errors.Is(err, closeErr) {
		t.Fatalf("want exhausted cleanup err, got %v (status=%+v)", err, status)
	}
	if g1.Lifecycle() != runtimehost.GenClosing {
		t.Fatalf("lifecycle=%v want GenClosing after exhausted attempts", g1.Lifecycle())
	}
	if closes.Load() != 2 {
		t.Fatalf("closes=%d want 2 after exhausted attempts", closes.Load())
	}
	if status.Outcome != runtimehost.LifecycleOutcomeCleanupFailed || status.Attempts != 2 {
		t.Fatalf("status=%+v", status)
	}

	succeed.Store(true)
	status, err = m.RetireGeneration(context.Background(), g1)
	if err != nil {
		t.Fatalf("explicit retry: %v", err)
	}
	if status.Outcome != runtimehost.LifecycleOutcomeOK {
		t.Fatalf("status=%+v", status)
	}
	if g1.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("lifecycle=%v want closed", g1.Lifecycle())
	}
	if closes.Load() != 3 {
		t.Fatalf("closes=%d want 3 (2 exhausted + 1 successful retry)", closes.Load())
	}
}

// 6. Panic isolation then retry.
func TestManagerRetire_CleanupPanicIsolatedThenRetrySucceeds(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(2, nil)
	m.SetCleanupPolicy(runtimehost.CleanupPolicy{MaxAttempts: 1})
	var closes atomic.Int32
	owned := &ledgerOwned{closeFn: func() error {
		if closes.Add(1) == 1 {
			panic("cleanup boom")
		}
		return nil
	}}
	g1 := m.PrepareOwned("g1", owned)
	mustPublish(t, m, g1)
	m.BeginShutdown()
	m.DetachActive()

	status, err := m.RetireGeneration(context.Background(), g1)
	if err == nil || !strings.Contains(err.Error(), "cleanup boom") {
		t.Fatalf("want isolated panic error, got %v", err)
	}
	if g1.Lifecycle() != runtimehost.GenClosing {
		t.Fatalf("lifecycle=%v want GenClosing after panic isolation", g1.Lifecycle())
	}
	if status.Outcome != runtimehost.LifecycleOutcomeCleanupFailed {
		t.Fatalf("status=%+v", status)
	}

	status, err = m.RetireGeneration(context.Background(), g1)
	if err != nil {
		t.Fatalf("retry after panic isolation: %v", err)
	}
	if status.Outcome != runtimehost.LifecycleOutcomeOK {
		t.Fatalf("status=%+v", status)
	}
	if g1.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("lifecycle=%v want closed", g1.Lifecycle())
	}
	if closes.Load() != 2 {
		t.Fatalf("closes=%d want 2 (panic then retry success)", closes.Load())
	}
}

// 7. Two gens retire independently: a pinned old generation must not block an
// unrelated zero-ref generation's auto-retirement.
func TestManagerRetire_TwoGenerationsRetireIndependently(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(3, nil)
	close1Done := make(chan struct{})
	owned1 := &ledgerOwned{closeFn: func() error { close(close1Done); return nil }}
	g1 := m.PrepareOwned("g1", owned1)
	mustPublish(t, m, g1)
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire g1 lease")
	}

	close2Done := make(chan struct{})
	owned2 := &ledgerOwned{closeFn: func() error { close(close2Done); return nil }}
	g2 := m.PrepareOwned("g2", owned2)
	mustPublish(t, m, g2)
	g3 := m.Prepare("g3")
	mustPublish(t, m, g3)

	// g2 is zero-ref: its auto-retirement must fully close even though g1's
	// auto-retirement is still blocked on the held lease.
	select {
	case <-close2Done:
	case <-time.After(retireWait):
		t.Fatal("g2 retirement must not block behind g1's pinned drain")
	}
	awaitClosed(t, m, g2)
	if owned1.closes.Load() != 0 {
		t.Fatal("g1 must still be waiting on held lease")
	}

	lease.Release()
	select {
	case <-close1Done:
	case <-time.After(retireWait):
		t.Fatal("g1 auto-retirement must complete after pin release")
	}
	awaitClosed(t, m, g1)
	if owned1.quiesces.Load() != 1 || owned1.closes.Load() != 1 {
		t.Fatalf("g1 quiesces=%d closes=%d", owned1.quiesces.Load(), owned1.closes.Load())
	}
}

// 8 & 9. Shutdown races in-flight auto retirement, honors deadline, and never
// forces a pinned generation closed.
func TestManagerRetire_ShutdownHonorsDeadlineWhileAutoRetireBlockedOnPin(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(2, nil)
	quiesced := make(chan struct{})
	closeDone := make(chan struct{})
	owned := &ledgerOwned{
		quiesceFn: func(context.Context) error {
			close(quiesced)
			return nil
		},
		closeFn: func() error {
			close(closeDone)
			return nil
		},
	}
	g1 := m.PrepareOwned("g1", owned)
	mustPublish(t, m, g1)
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	mustPublish(t, m, m.Prepare("g2"))
	// Quiesce barrier: auto-retire reached Quiescing and invoked Quiesce while pin held.
	select {
	case <-quiesced:
	case <-time.After(retireWait):
		t.Fatal("auto-retire did not reach quiesce while pinned")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := m.ShutdownDetached(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if owned.closes.Load() != 0 {
		t.Fatal("pinned generation must never be force-closed")
	}

	lease.Release()
	select {
	case <-closeDone:
	case <-time.After(retireWait):
		t.Fatal("pin release must complete retirement")
	}
	awaitClosed(t, m, g1)
	if owned.closes.Load() != 1 {
		t.Fatalf("closes=%d want 1 after release", owned.closes.Load())
	}
}

// 10. Concurrent Manager retirement for the same generation is per-generation
// serialized and context-aware (a bounded caller does not wait forever behind
// an in-flight retirement blocked on a pin).
func TestManagerRetire_ConcurrentSameGenerationSerializedAndContextAware(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(2, nil)
	quiesced := make(chan struct{})
	closeDone := make(chan struct{})
	owned := &ledgerOwned{
		quiesceFn: func(context.Context) error {
			close(quiesced)
			return nil
		},
		closeFn: func() error {
			close(closeDone)
			return nil
		},
	}
	g1 := m.PrepareOwned("g1", owned)
	mustPublish(t, m, g1)
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	mustPublish(t, m, m.Prepare("g2"))
	select {
	case <-quiesced:
	case <-time.After(retireWait):
		t.Fatal("auto-retire did not reach quiesce while pinned")
	}

	// A second, context-bounded RetireGeneration call for the same generation
	// must be admitted-blocked behind the in-flight auto-retire and return
	// promptly via ctx, not hang on a plain mutex.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := m.RetireGeneration(ctx, g1)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v want DeadlineExceeded", err)
	}

	lease.Release()
	select {
	case <-closeDone:
	case <-time.After(retireWait):
		t.Fatal("pin release must complete retirement")
	}
	awaitClosed(t, m, g1)

	// Many concurrent unbounded RetireGeneration calls after close: exactly
	// one close, the rest ErrAlreadyClosed.
	const n = 16
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			_, err := m.RetireGeneration(context.Background(), g1)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, runtimehost.ErrAlreadyClosed) {
			t.Fatalf("unexpected concurrent retire err: %v", err)
		}
	}
	if owned.closes.Load() != 1 {
		t.Fatalf("closes=%d want 1", owned.closes.Load())
	}
}

// 11. Discard concurrent/repeated without closed/cache fields.
func TestManagerRetire_DiscardConcurrentRepeatedExactlyOnce(t *testing.T) {
	t.Parallel()
	var closes atomic.Int32
	m := runtimehost.NewManager(1, nil)
	g := m.PrepareOwned("race", &stubOwned{closeFn: func() error {
		closes.Add(1)
		return nil
	}})
	const n = 32
	errs := make(chan error, n)
	var start sync.WaitGroup
	var release sync.WaitGroup
	start.Add(n)
	release.Add(1)
	for range n {
		go func() {
			start.Done()
			release.Wait()
			errs <- g.Discard()
		}()
	}
	start.Wait()
	release.Done()

	var ok, already int
	for range n {
		switch err := <-errs; {
		case err == nil:
			ok++
		case errors.Is(err, runtimehost.ErrAlreadyClosed):
			already++
		default:
			t.Fatalf("unexpected discard err: %v", err)
		}
	}
	if ok != 1 || already != n-1 {
		t.Fatalf("ok=%d already=%d", ok, already)
	}
	if closes.Load() != 1 || g.CloseCount() != 1 {
		t.Fatalf("closes=%d count=%d", closes.Load(), g.CloseCount())
	}

	// Repeated calls after the race settle remain ErrAlreadyClosed.
	if err := g.Discard(); !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("post-race discard: %v", err)
	}
	if closes.Load() != 1 {
		t.Fatalf("closes=%d want 1", closes.Load())
	}
}

// 12. Observer receives lifecycle telemetry; Manager owns no mutable
// retirement-status cache/history (RetireGeneration returns a fresh
// per-attempt value each call; nothing is cached on Manager itself).
//
// This test drives retirement synchronously via BeginShutdown+DetachActive
// and a direct RetireGeneration call (rather than the automatic background
// scheduler) so the log write is guaranteed complete before the assertion
// reads it back — no additional synchronization needed.
func TestManagerRetire_ObserverReceivesLifecycleTelemetry_NoManagerStatusCache(t *testing.T) {
	t.Parallel()
	var log strings.Builder
	logger := slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelInfo}))
	observer := runtimehost.NewReloadObserver(runtimehost.ReloadObserverDeps{Logger: logger})

	m := runtimehost.NewManager(2, nil)
	m.SetLifecycleObserver(observer)
	owned := &ledgerOwned{}
	g1 := m.PrepareOwned("g1", owned)
	mustPublish(t, m, g1)
	m.BeginShutdown()
	m.DetachActive()

	if _, err := m.RetireGeneration(context.Background(), g1); err != nil {
		t.Fatalf("RetireGeneration: %v", err)
	}
	if g1.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("lifecycle=%v want GenClosed", g1.Lifecycle())
	}

	if !strings.Contains(log.String(), "reload lifecycle stage") {
		t.Fatalf("observer did not record lifecycle telemetry: %s", log.String())
	}

	// RetireGeneration on an already-closed generation still returns a fresh
	// status value (Manager holds no mutable last-status cache/history).
	st1, err := m.RetireGeneration(context.Background(), g1)
	if !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("err=%v", err)
	}
	st2, err := m.RetireGeneration(context.Background(), g1)
	if !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("err=%v", err)
	}
	if st1.GenerationID != g1.ID() || st2.GenerationID != g1.ID() {
		t.Fatalf("status generation id mismatch: %+v / %+v", st1, st2)
	}
}

// 8. The last lease released while the async retirement of a just-replaced
// generation is starting must still close it. Release moves a Retiring
// generation straight to Drained; a retirement that had already read the
// Retiring state used to fail its Retiring->Quiescing step on that and give up,
// so nothing ever closed the generation. The release is issued from its own
// goroutine after 0-7 scheduler yields to land it at different points of the
// retirement's start-up.
func TestManagerRetire_LastLeaseReleasedAsRetirementStartsStillCloses(t *testing.T) {
	t.Parallel()
	for i := range 2000 {
		m := runtimehost.NewManager(2, nil)
		closed := make(chan struct{})
		owned := &ledgerOwned{closeFn: func() error { close(closed); return nil }}
		g1 := m.PrepareOwned("g1", owned)
		mustPublish(t, m, g1)
		lease, ok := m.Acquire()
		if !ok {
			t.Fatal("acquire g1 lease")
		}
		yields := i % 8
		go func() {
			for range yields {
				runtime.Gosched()
			}
			lease.Release() // races with g1's background retirement
		}()
		mustPublish(t, m, m.Prepare("g2")) // starts g1's background retirement
		select {
		case <-closed:
		case <-time.After(retireWait):
			t.Fatalf("iteration %d: g1 never closed (lifecycle=%v refs=%d quiesces=%d)", i, g1.Lifecycle(), g1.Refs(), owned.quiesces.Load())
		}
	}
}
