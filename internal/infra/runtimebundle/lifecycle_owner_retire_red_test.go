package runtimebundle_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
)

// Task 7.1 cross-layer lifecycle targets live next to the real GenerationBundle
// construction helpers (export_test). Req 8.8 is intentional RED against current
// GenerationBundle/ResourceLedger close-result caches that defeat
// Generation/Manager retirement retry.
//
// Task 7.3 moved retirement scheduling under Manager ownership: Publish
// auto-schedules one background retirement per replaced generation, and
// Manager.RetireGeneration is the synchronous retry/wait counterpart. Tests
// below tolerate a benign ErrAlreadyClosed from an explicit RetireGeneration
// call that races an already-completed automatic retirement, and use
// channel-based close hooks (not sleeps) to observe completion.

func TestLifecycleOwner_Retire_RetryableCloseThroughGenerationBundle(t *testing.T) {
	t.Parallel()
	var closes atomic.Int32
	closeDone := make(chan struct{})
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("backend", runtimebundle.PhaseClose, func() error {
		n := closes.Add(1)
		if n == 1 {
			return errors.New("temp-close")
		}
		close(closeDone)
		return nil
	})
	ledger.AddClose("worker", runtimebundle.PhaseQuiesce, func() error { return nil })
	bundle := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)

	m := runtimehost.NewManager(2, nil)
	m.SetCleanupPolicy(runtimehost.CleanupPolicy{MaxAttempts: 3})
	g1 := m.PrepareRequestPlane("g1-retry", bundle)
	mustPublishBundle(t, m, g1)
	g2 := m.Prepare("g2-active")
	mustPublishBundle(t, m, g2)

	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Retire must eventually succeed after retryable close")
	}
	// closeDone fires inside the successful ledger callback, before
	// Generation.Close publishes GenClosed. RetireGeneration serializes
	// behind the in-flight automatic retirement via admission.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := m.RetireGeneration(ctx, g1); err != nil && !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("await retirement: %v (lifecycle=%v)", err, g1.Lifecycle())
	}
	if g1.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("lifecycle=%v want GenClosed after successful retry", g1.Lifecycle())
	}
	if closes.Load() != 2 {
		t.Fatalf("resource close executions=%d want 2 (fail then success); cached wrapper made retry a no-op", closes.Load())
	}
	if m.Active() != g2 || g2.Lifecycle() != runtimehost.GenActive {
		t.Fatal("active generation must remain healthy")
	}
}

// TestLifecycleOwner_Retire_StaysClosingAfterRetryableFailureBeforeSuccess
// drives Generation.BeginClose/Close directly, so it uses BeginShutdown +
// DetachActive (not a replacing Publish) to avoid racing Manager's automatic
// post-publish retirement scheduling (task 7.3) against this manual drive.
func TestLifecycleOwner_Retire_StaysClosingAfterRetryableFailureBeforeSuccess(t *testing.T) {
	t.Parallel()
	var closes atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("backend", runtimebundle.PhaseClose, func() error {
		n := closes.Add(1)
		if n == 1 {
			startOnce.Do(func() { close(started) })
			<-release
			return errors.New("temp-close")
		}
		return nil
	})
	bundle := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)

	m := runtimehost.NewManager(2, nil)
	g1 := m.PrepareRequestPlane("g1", bundle)
	mustPublishBundle(t, m, g1)
	m.BeginShutdown()
	m.DetachActive()

	<-g1.Drained()
	if err := g1.BeginClose(); err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- g1.Close() }()
	<-started
	if g1.Lifecycle() != runtimehost.GenClosing {
		t.Fatalf("lifecycle=%v want GenClosing during first failing cleanup", g1.Lifecycle())
	}
	close(release)
	if err := <-errCh; err == nil {
		t.Fatal("first Close must fail")
	}
	if g1.Lifecycle() != runtimehost.GenClosing {
		t.Fatalf("lifecycle=%v want GenClosing after retryable failure (req 8.8)", g1.Lifecycle())
	}

	if err := g1.Close(); err != nil {
		t.Fatalf("owned retry Close: %v (closes=%d)", err, closes.Load())
	}
	if g1.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("lifecycle=%v want GenClosed", g1.Lifecycle())
	}
	if closes.Load() != 2 {
		t.Fatalf("closes=%d want 2", closes.Load())
	}
}

func TestLifecycleOwner_Retire_PanicIsolatedThroughManagerRetirement(t *testing.T) {
	t.Parallel()
	var closes atomic.Int32
	closeDone := make(chan struct{})
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("boom", runtimebundle.PhaseClose, func() error {
		n := closes.Add(1)
		if n == 1 {
			panic("cleanup boom")
		}
		close(closeDone)
		return nil
	})
	ledger.AddClose("worker", runtimebundle.PhaseQuiesce, func() error { return nil })
	bundle := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)

	m := runtimehost.NewManager(2, nil)
	m.SetCleanupPolicy(runtimehost.CleanupPolicy{MaxAttempts: 3})
	g1 := m.PrepareRequestPlane("g1-panic", bundle)
	mustPublishBundle(t, m, g1)
	mustPublishBundle(t, m, m.Prepare("g2"))

	// Manager's retireGeneration safeClose is the isolation boundary for
	// Generation→owned cleanup panics (req 8.8 / 8.10). Ledger may also
	// convert panics to errors; either boundary must keep GenClosing retryable.
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Retire after panic isolation must succeed")
	}
	// closeDone fires inside the successful ledger callback, before
	// Generation.Close publishes GenClosed. RetireGeneration serializes
	// behind the in-flight automatic retirement via admission; it must not
	// re-run successful cleanup (panic-then-retry already completed once).
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := m.RetireGeneration(ctx, g1); err != nil && !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("await retirement: %v (lifecycle=%v)", err, g1.Lifecycle())
	}
	if g1.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("lifecycle=%v want GenClosed", g1.Lifecycle())
	}
	if closes.Load() != 2 {
		t.Fatalf("closes=%d want 2 (panic fail then retry success); permanent claim defeated retry", closes.Load())
	}
}

func TestLifecycleOwner_Discard_UnpublishedGenerationBundleRollbackOnce(t *testing.T) {
	t.Parallel()
	var closes atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("backend", runtimebundle.PhaseClose, func() error {
		closes.Add(1)
		return nil
	})
	ledger.AddClose("prepare", runtimebundle.PhasePrepare, func() error {
		closes.Add(1)
		return nil
	})
	bundle := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)

	m := runtimehost.NewManager(2, nil)
	g := m.PrepareRequestPlane("unpublished", bundle)
	if g.Lifecycle() != runtimehost.GenPrepared {
		t.Fatalf("lifecycle=%v want GenPrepared before Discard", g.Lifecycle())
	}
	if err := g.Discard(); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if closes.Load() != 2 {
		t.Fatalf("Discard cleanup executions=%d want 2 (close+prepare phases)", closes.Load())
	}
	if err := g.Discard(); !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("second Discard: %v", err)
	}
	if closes.Load() != 2 {
		t.Fatalf("repeated Discard double-cleaned closes=%d", closes.Load())
	}
}

func TestLifecycleOwner_Retire_ConcurrentRetirementNoDoubleClean(t *testing.T) {
	t.Parallel()
	var quiesces, closes atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("worker", runtimebundle.PhaseQuiesce, func() error {
		quiesces.Add(1)
		return nil
	})
	ledger.AddClose("backend", runtimebundle.PhaseClose, func() error {
		closes.Add(1)
		return nil
	})
	bundle := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)

	m := runtimehost.NewManager(4, nil)
	g1 := m.PrepareRequestPlane("g1", bundle)
	mustPublishBundle(t, m, g1)
	mustPublishBundle(t, m, m.Prepare("g2"))

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			<-start
			_, err := m.RetireGeneration(context.Background(), g1)
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(errs)

	// Manager's automatic post-publish retirement (task 7.3) races these 8
	// explicit calls too; it may itself win and finish the close before any
	// of them run, in which case all 8 observe a benign ErrAlreadyClosed.
	// The invariant under test is single-execution close/quiesce, not which
	// caller (explicit or automatic) performs it.
	for err := range errs {
		if err != nil && !errors.Is(err, runtimehost.ErrAlreadyClosed) {
			t.Fatalf("unexpected retire err: %v", err)
		}
	}
	if quiesces.Load() != 1 {
		t.Fatalf("quiesce executions=%d want 1", quiesces.Load())
	}
	if closes.Load() != 1 {
		t.Fatalf("close executions=%d want 1", closes.Load())
	}
	if g1.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("lifecycle=%v", g1.Lifecycle())
	}
}

func TestLifecycleOwner_Close_RepeatedShutdownCannotDoubleClean(t *testing.T) {
	t.Parallel()
	var closes atomic.Int32
	closeDone := make(chan struct{})
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("backend", runtimebundle.PhaseClose, func() error {
		closes.Add(1)
		close(closeDone)
		return nil
	})
	bundle := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)

	m := runtimehost.NewManager(2, nil)
	g1 := m.PrepareRequestPlane("g1", bundle)
	mustPublishBundle(t, m, g1)
	mustPublishBundle(t, m, m.Prepare("g2"))

	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
	if _, err := m.RetireGeneration(context.Background(), g1); !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("second Retire: %v", err)
	}
	if err := g1.Close(); !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("Close after closed: %v", err)
	}
	if closes.Load() != 1 {
		t.Fatalf("closes=%d want 1", closes.Load())
	}
}

// TestLifecycleOwner_ShutdownDetached_RacesRetirementNoDoubleCleanOrProcessClose
// races Manager.ShutdownDetached with Manager.RetireGeneration against a real
// GenerationBundle/ResourceLedger. Proves no generation-resource double cleanup
// and no process-owned premature close (channels/atomics + context bound; no sleep).
func TestLifecycleOwner_ShutdownDetached_RacesRetirementNoDoubleCleanOrProcessClose(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)
	var closes atomic.Int32
	ledger := runtimebundle.NewResourceLedger()
	ledger.AddClose("backend", runtimebundle.PhaseClose, func() error {
		closes.Add(1)
		return nil
	})
	ledger.AddClose("worker", runtimebundle.PhaseQuiesce, func() error {
		return nil
	})
	bundle := runtimebundle.NewGenerationBundleWithLedgerForTest(ledger)

	m := runtimehost.NewManager(4, nil)
	g1 := m.PrepareRequestPlane("g1-shutdown", bundle)
	mustPublishBundle(t, m, g1)
	g2 := m.Prepare("g2-keep")
	mustPublishBundle(t, m, g2)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 4)

	wg.Go(func() {
		<-start
		_, err := m.RetireGeneration(ctx, g1)
		errs <- err
	})
	wg.Go(func() {
		<-start
		errs <- m.ShutdownDetached(ctx)
	})
	wg.Go(func() {
		<-start
		_, err := m.RetireGeneration(ctx, g1)
		errs <- err
	})
	wg.Go(func() {
		<-start
		errs <- m.ShutdownDetached(ctx)
	})
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err == nil || errors.Is(err, runtimehost.ErrAlreadyClosed) {
			continue
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown/retire hit context bound unexpectedly: %v", err)
		}
		t.Fatalf("unexpected shutdown/retire err: %v", err)
	}
	if closes.Load() != 1 {
		t.Fatalf("generation resource closes=%d want 1 (no double cleanup)", closes.Load())
	}
	if ps.Closed() {
		t.Fatal("process-owned resources must not close during generation retirement/shutdown race")
	}
	if g1.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("g1 lifecycle=%v want GenClosed", g1.Lifecycle())
	}
}
