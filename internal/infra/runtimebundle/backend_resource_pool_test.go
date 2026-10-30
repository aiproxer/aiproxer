package runtimebundle

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"go.uber.org/goleak"
)

// These tests intentionally exercise only the private connector-specific pool
// seam.  They are RED until backendResourcePool, backendResourceEntry, and
// backendResourceLease are implemented.

func TestBackendResourcePoolAcquireFirstAndLiveReuse(t *testing.T) {
	id := backendResourcePoolTestIdentity(t, "pool-first-live")
	pool := newBackendResourcePool()
	probe := &backendResourcePoolProbe{}

	first, err := pool.Acquire(context.Background(), id, probe.build)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if first.Cleanup == nil {
		t.Fatal("first Acquire returned nil lease cleanup")
	}

	second, err := pool.Acquire(context.Background(), id, probe.build)
	if err != nil {
		t.Fatalf("live Acquire: %v", err)
	}
	if second.Cleanup == nil {
		t.Fatal("live Acquire returned nil lease cleanup")
	}
	if got := probe.builds.Load(); got != 1 {
		t.Fatalf("physical builds=%d, want 1 after live reuse", got)
	}

	entry, claims, owned := backendResourcePoolSnapshot(t, pool, id)
	if entry == nil {
		t.Fatal("live entry missing from current index")
	}
	if claims != 2 {
		t.Fatalf("live claims=%d, want 2", claims)
	}
	if owned != 1 {
		t.Fatalf("owned entries=%d, want 1", owned)
	}

	if err := first.Cleanup(); err != nil {
		t.Fatalf("first lease release: %v", err)
	}
	if got := probe.cleanups.Load(); got != 0 {
		t.Fatalf("physical cleanups=%d after non-final release, want 0", got)
	}
	if err := second.Cleanup(); err != nil {
		t.Fatalf("final lease release: %v", err)
	}
	if got := probe.cleanups.Load(); got != 1 {
		t.Fatalf("physical cleanups=%d after final release, want 1", got)
	}
	if _, claims, owned = backendResourcePoolSnapshot(t, pool, id); claims != 0 || owned != 0 {
		t.Fatalf("after final release claims=%d owned=%d, want 0/0", claims, owned)
	}
	if err := first.Cleanup(); err != nil {
		t.Fatalf("idempotent first lease release: %v", err)
	}
	if err := second.Cleanup(); err != nil {
		t.Fatalf("idempotent second lease release: %v", err)
	}
	if got := probe.cleanups.Load(); got != 1 {
		t.Fatalf("physical cleanups=%d after idempotent releases, want 1", got)
	}
	if err := pool.Close(); err != nil {
		t.Fatalf("Pool.Close after drained release: %v", err)
	}
}

func TestBackendResourcePoolAcquireConcurrentReservationsSurviveFirstReleaseBeforeWaiterWake(t *testing.T) {
	id := backendResourcePoolTestIdentity(t, "pool-concurrent-reservation")
	pool := newBackendResourcePool()
	probe := &backendResourcePoolProbe{}
	buildStarted := make(chan struct{})
	allowBuild := make(chan struct{})
	var startedOnce sync.Once

	build := func(ctx context.Context, incarnation uint64) (execbackend.Backend, func() error, error) {
		probe.builds.Add(1)
		probe.lastIncarnation.Store(incarnation)
		startedOnce.Do(func() { close(buildStarted) })
		select {
		case <-allowBuild:
		case <-ctx.Done():
			return execbackend.Backend{}, nil, ctx.Err()
		}
		return execbackend.Backend{}, probe.cleanup(incarnation), nil
	}

	firstDone := make(chan struct{})
	var firstResult pluginreg.BackendBuildResult
	var firstErr error
	go func() {
		result, err := pool.Acquire(context.Background(), id, build)
		firstResult = result
		firstErr = err
		close(firstDone)
	}()
	<-buildStarted

	secondDone := make(chan struct{})
	var secondResult pluginreg.BackendBuildResult
	var secondErr error
	go func() {
		result, err := pool.Acquire(context.Background(), id, build)
		secondResult = result
		secondErr = err
		close(secondDone)
	}()

	// The second claimant must be visible before the builder is allowed to
	// publish.  This is a scheduling barrier, not a timing threshold.
	backendResourcePoolWaitForClaims(t, pool, id, 2)
	close(allowBuild)
	<-firstDone
	if firstErr != nil {
		t.Fatalf("first Acquire: %v", firstErr)
	}
	if firstResult.Cleanup == nil {
		t.Fatal("first Acquire returned nil result")
	}

	// Release the first claim before accepting the waiter result.  The
	// reserved second claim must keep the physical entry alive even if the
	// waiter has not yet completed its handoff.
	if err := firstResult.Cleanup(); err != nil {
		t.Fatalf("first lease release before waiter handoff: %v", err)
	}
	if got := probe.cleanups.Load(); got != 0 {
		t.Fatalf("physical cleanups=%d before waiter handoff, want 0", got)
	}
	<-secondDone
	if secondErr != nil {
		t.Fatalf("waiter Acquire: %v", secondErr)
	}
	if secondResult.Cleanup == nil {
		t.Fatal("waiter Acquire returned nil result")
	}
	if err := secondResult.Cleanup(); err != nil {
		t.Fatalf("waiter lease release: %v", err)
	}
	if got := probe.builds.Load(); got != 1 {
		t.Fatalf("physical builds=%d, want 1", got)
	}
	if got := probe.cleanups.Load(); got != 1 {
		t.Fatalf("physical cleanups=%d after waiter release, want 1", got)
	}
}

func TestBackendResourcePoolAcquireWaiterCancellationAbandonsOnlyItsClaim(t *testing.T) {
	id := backendResourcePoolTestIdentity(t, "pool-waiter-cancel")
	pool := newBackendResourcePool()
	probe := &backendResourcePoolProbe{}
	buildStarted := make(chan struct{})
	allowBuild := make(chan struct{})
	var startedOnce sync.Once

	build := func(ctx context.Context, incarnation uint64) (execbackend.Backend, func() error, error) {
		probe.builds.Add(1)
		probe.lastIncarnation.Store(incarnation)
		startedOnce.Do(func() { close(buildStarted) })
		select {
		case <-allowBuild:
		case <-ctx.Done():
			return execbackend.Backend{}, nil, ctx.Err()
		}
		return execbackend.Backend{}, probe.cleanup(incarnation), nil
	}

	firstDone := make(chan struct{})
	var firstResult pluginreg.BackendBuildResult
	var firstErr error
	go func() {
		result, err := pool.Acquire(context.Background(), id, build)
		firstResult, firstErr = result, err
		close(firstDone)
	}()
	<-buildStarted

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterDone := make(chan struct{})
	var waiterErr error
	go func() {
		_, waiterErr = pool.Acquire(waiterCtx, id, build)
		close(waiterDone)
	}()
	backendResourcePoolWaitForClaims(t, pool, id, 2)
	cancelWaiter()
	<-waiterDone
	if !errors.Is(waiterErr, context.Canceled) {
		t.Fatalf("waiter error=%v, want context.Canceled", waiterErr)
	}
	if got := probe.builds.Load(); got != 1 {
		t.Fatalf("waiter cancellation started builds=%d, want 1", got)
	}

	// The pool-owned builder must survive cancellation of one claimant.
	close(allowBuild)
	<-firstDone
	if firstErr != nil {
		t.Fatalf("first Acquire after waiter cancellation: %v", firstErr)
	}
	if firstResult.Cleanup == nil {
		t.Fatal("first Acquire returned nil result")
	}
	if err := firstResult.Cleanup(); err != nil {
		t.Fatalf("first lease release: %v", err)
	}
	if got := probe.cleanups.Load(); got != 1 {
		t.Fatalf("physical cleanups=%d, want 1", got)
	}
}

func TestBackendResourcePoolAcquireBuildFailureIsNotNegativeCachedAndRetries(t *testing.T) {
	id := backendResourcePoolTestIdentity(t, "pool-build-retry")
	pool := newBackendResourcePool()
	probe := &backendResourcePoolProbe{}
	buildStarted := make(chan struct{})
	allowFailure := make(chan struct{})
	firstBuildErr := errors.New("deterministic physical build failure")
	var calls atomic.Int32

	build := func(ctx context.Context, incarnation uint64) (execbackend.Backend, func() error, error) {
		call := calls.Add(1)
		probe.lastIncarnation.Store(incarnation)
		if call == 1 {
			close(buildStarted)
			select {
			case <-allowFailure:
			case <-ctx.Done():
				return execbackend.Backend{}, nil, ctx.Err()
			}
			return execbackend.Backend{}, nil, firstBuildErr
		}
		probe.builds.Add(1)
		return execbackend.Backend{}, probe.cleanup(incarnation), nil
	}

	firstDone := make(chan struct{})
	var firstErr error
	go func() {
		_, firstErr = pool.Acquire(context.Background(), id, build)
		close(firstDone)
	}()
	<-buildStarted

	secondDone := make(chan struct{})
	var secondErr error
	go func() {
		_, secondErr = pool.Acquire(context.Background(), id, build)
		close(secondDone)
	}()
	backendResourcePoolWaitForClaims(t, pool, id, 2)
	close(allowFailure)
	<-firstDone
	<-secondDone
	if !errors.Is(firstErr, firstBuildErr) || !errors.Is(secondErr, firstBuildErr) {
		t.Fatalf("waiters saw errors first=%v second=%v, want %v", firstErr, secondErr, firstBuildErr)
	}
	if got := probe.cleanups.Load(); got != 0 {
		t.Fatalf("physical cleanups=%d after failed build, want 0", got)
	}

	retry, err := pool.Acquire(context.Background(), id, build)
	if err != nil {
		t.Fatalf("later Acquire retry: %v", err)
	}
	if retry.Cleanup == nil {
		t.Fatal("retry returned nil lease cleanup")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("builder calls=%d, want 2 after retry", got)
	}
	if err := retry.Cleanup(); err != nil {
		t.Fatalf("retry lease release: %v", err)
	}
	if got := probe.cleanups.Load(); got != 1 {
		t.Fatalf("physical cleanups=%d after retry release, want 1", got)
	}
}

func TestBackendResourcePoolBuildFailureCleansPartialResultExactlyOnce(t *testing.T) {
	id := backendResourcePoolTestIdentity(t, "pool-build-failure-cleanup")
	pool := newBackendResourcePool()
	buildErr := errors.New("deterministic partial build failure")
	cleanupErr := errors.New("deterministic partial cleanup failure")
	var cleanupCalls atomic.Int32

	build := func(_ context.Context, _ uint64) (execbackend.Backend, func() error, error) {
		return execbackend.Backend{}, func() error {
			// Cleanup must not run while the pool mutex is held.  Re-entering it
			// here would deadlock the builder's failure handoff otherwise.
			pool.mu.Lock()
			//nolint:staticcheck // SA2001: deliberate lock acquisition to assert pool.mu is not held
			pool.mu.Unlock()
			cleanupCalls.Add(1)
			return cleanupErr
		}, buildErr
	}

	_, err := pool.Acquire(context.Background(), id, build)
	if err == nil {
		t.Fatal("Acquire unexpectedly succeeded after partial build failure")
	}
	if !errors.Is(err, buildErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("Acquire error=%v, want build and cleanup failures", err)
	}
	if got := cleanupCalls.Load(); got != 1 {
		t.Fatalf("partial-result cleanups=%d after failed Acquire, want exactly 1", got)
	}

	if err := pool.Close(); err != nil {
		t.Fatalf("Pool.Close after failed Acquire: %v", err)
	}
	if got := cleanupCalls.Load(); got != 1 {
		t.Fatalf("partial-result cleanups=%d after Pool.Close, want exactly 1", got)
	}
	if err := pool.Close(); err != nil {
		t.Fatalf("idempotent Pool.Close after failed Acquire: %v", err)
	}
	if got := cleanupCalls.Load(); got != 1 {
		t.Fatalf("partial-result cleanups=%d after idempotent Pool.Close, want exactly 1", got)
	}
}

func TestBackendResourcePoolRejectsGenerationLocalLifecycleCallbacks(t *testing.T) {
	tests := []struct {
		name    string
		install func(*execbackend.Backend)
	}{
		{name: "close", install: func(be *execbackend.Backend) { be.Close = func() error { return nil } }},
		{name: "start", install: func(be *execbackend.Backend) { be.Start = func(context.Context) error { return nil } }},
		{name: "stop", install: func(be *execbackend.Backend) { be.Stop = func(context.Context) error { return nil } }},
		{name: "cleanup idle transports", install: func(be *execbackend.Backend) {
			be.CleanupIdleTransports = func(context.Context) error { return nil }
		}},
		{name: "preflight capability", install: func(be *execbackend.Backend) {
			be.PreflightCapability = func(context.Context) (execbackend.CapabilityPreflight, error) {
				return execbackend.CapabilityPreflight{Ready: true}, nil
			}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := backendResourcePoolTestIdentity(t, "pool-incompatible-"+tt.name)
			pool := newBackendResourcePool()
			var builds, cleanups atomic.Int32

			build := func(_ context.Context, _ uint64) (execbackend.Backend, func() error, error) {
				if builds.Add(1) == 1 {
					backend := execbackend.Backend{}
					tt.install(&backend)
					return backend, func() error {
						cleanups.Add(1)
						return nil
					}, nil
				}
				return execbackend.Backend{}, func() error {
					cleanups.Add(1)
					return nil
				}, nil
			}

			if _, err := pool.Acquire(context.Background(), id, build); !errors.Is(err, errBackendResourceLifecycle) {
				t.Fatalf("Acquire error=%v, want incompatible lifecycle error", err)
			}
			if got := cleanups.Load(); got != 1 {
				t.Fatalf("incompatible backend cleanups=%d, want exactly 1", got)
			}
			if entry, _, owned := backendResourcePoolSnapshot(t, pool, id); entry != nil || owned != 0 {
				t.Fatalf("incompatible backend published entry=%v owned=%d, want no reusable resource", entry != nil, owned)
			}

			result, err := pool.Acquire(context.Background(), id, build)
			if err != nil {
				t.Fatalf("valid retry Acquire: %v", err)
			}
			if result.Backend.Close != nil || result.Backend.Start != nil || result.Backend.Stop != nil ||
				result.Backend.CleanupIdleTransports != nil || result.Backend.PreflightCapability != nil {
				t.Fatal("valid retry published a lifecycle callback")
			}
			if err := result.Cleanup(); err != nil {
				t.Fatalf("valid retry cleanup: %v", err)
			}
			if got := builds.Load(); got != 2 {
				t.Fatalf("physical builds=%d, want incompatible build plus valid retry", got)
			}
			if got := cleanups.Load(); got != 2 {
				t.Fatalf("physical cleanups=%d, want exactly 2", got)
			}
			if err := pool.Close(); err != nil {
				t.Fatalf("Pool.Close: %v", err)
			}
			if got := cleanups.Load(); got != 2 {
				t.Fatalf("physical cleanups=%d after Pool.Close, want exactly 2", got)
			}
		})
	}
}

func TestBackendResourcePoolInvalidateExactIncarnationDetachesAndAllowsFreshReplacement(t *testing.T) {
	id := backendResourcePoolTestIdentity(t, "pool-invalidate-replace")
	pool := newBackendResourcePool()
	probe := &backendResourcePoolProbe{}

	old, err := pool.Acquire(context.Background(), id, probe.build)
	if err != nil {
		t.Fatalf("old Acquire: %v", err)
	}
	oldEntry, _, _ := backendResourcePoolSnapshot(t, pool, id)
	if oldEntry == nil {
		t.Fatal("old current entry missing")
	}
	oldIncarnation := oldEntry.incarnation

	pool.Invalidate(id, oldIncarnation)
	current, claims, owned := backendResourcePoolEntryOwnershipSnapshot(t, pool, id, oldEntry)
	if current || claims != 1 || !owned {
		t.Fatalf("after invalidation current=%v detached claims=%d owned=%v, want false/1/true", current, claims, owned)
	}
	if got := probe.cleanups.Load(); got != 0 {
		t.Fatalf("physical cleanups=%d while invalidated lease remains, want 0", got)
	}

	fresh, err := pool.Acquire(context.Background(), id, probe.build)
	if err != nil {
		t.Fatalf("fresh Acquire after invalidation: %v", err)
	}
	freshEntry, _, _ := backendResourcePoolSnapshot(t, pool, id)
	if freshEntry == nil {
		t.Fatal("fresh current entry missing")
	}
	if freshEntry.incarnation == oldIncarnation {
		t.Fatalf("fresh incarnation=%d reused invalidated incarnation", freshEntry.incarnation)
	}
	if got := probe.builds.Load(); got != 2 {
		t.Fatalf("physical builds=%d after replacement, want 2", got)
	}

	// A stale callback for the old incarnation must not detach the replacement.
	pool.Invalidate(id, oldIncarnation)
	liveHit, err := pool.Acquire(context.Background(), id, probe.build)
	if err != nil {
		t.Fatalf("live Acquire after stale invalidation: %v", err)
	}
	if got := probe.builds.Load(); got != 2 {
		t.Fatalf("stale invalidation caused physical builds=%d, want 2", got)
	}

	if err := old.Cleanup(); err != nil {
		t.Fatalf("old lease release: %v", err)
	}
	if got := probe.cleanups.Load(); got != 1 {
		t.Fatalf("physical cleanups=%d after old release, want 1", got)
	}
	if err := fresh.Cleanup(); err != nil {
		t.Fatalf("fresh lease release: %v", err)
	}
	if err := liveHit.Cleanup(); err != nil {
		t.Fatalf("live-hit lease release: %v", err)
	}
	if got := probe.cleanups.Load(); got != 2 {
		t.Fatalf("physical cleanups=%d after both incarnations release, want 2", got)
	}
}

func TestBackendResourcePoolFinalReleaseDetachesBeforePhysicalCleanupAndNewAcquire(t *testing.T) {
	id := backendResourcePoolTestIdentity(t, "pool-release-reacquire")
	pool := newBackendResourcePool()
	probe := &backendResourcePoolProbe{}
	oldCleanupEntered := make(chan struct{})
	allowOldCleanup := make(chan struct{})
	var cleanupOnce sync.Once

	build := func(ctx context.Context, incarnation uint64) (execbackend.Backend, func() error, error) {
		probe.builds.Add(1)
		probe.lastIncarnation.Store(incarnation)
		return execbackend.Backend{}, func() error {
			probe.cleanups.Add(1)
			if incarnation == 1 {
				cleanupOnce.Do(func() { close(oldCleanupEntered) })
				select {
				case <-allowOldCleanup:
				case <-ctx.Done():
				}
			}
			return nil
		}, nil
	}

	old, err := pool.Acquire(context.Background(), id, build)
	if err != nil {
		t.Fatalf("old Acquire: %v", err)
	}
	oldDone := make(chan struct{})
	go func() {
		if err := old.Cleanup(); err != nil {
			t.Errorf("old lease release: %v", err)
		}
		close(oldDone)
	}()
	<-oldCleanupEntered

	// Physical cleanup is intentionally blocked.  Acquire must still be able
	// to build a fresh incarnation because final release detached the entry
	// before invoking cleanup and did not hold the pool mutex during cleanup.
	fresh, err := pool.Acquire(context.Background(), id, build)
	if err != nil {
		t.Fatalf("fresh Acquire during old cleanup: %v", err)
	}
	if got := probe.builds.Load(); got != 2 {
		t.Fatalf("physical builds=%d during release/Acquire race, want 2", got)
	}
	if err := fresh.Cleanup(); err != nil {
		t.Fatalf("fresh lease release: %v", err)
	}
	close(allowOldCleanup)
	<-oldDone
	if got := probe.cleanups.Load(); got != 2 {
		t.Fatalf("physical cleanups=%d after release/Acquire race, want 2", got)
	}
}

func TestBackendResourcePoolFinalReleaseRacingCloseCleansExactlyOnce(t *testing.T) {
	id := backendResourcePoolTestIdentity(t, "pool-release-close")
	pool := newBackendResourcePool()
	probe := &backendResourcePoolProbe{}
	cleanupEntered := make(chan struct{})
	allowCleanup := make(chan struct{})
	var enteredOnce sync.Once

	build := func(_ context.Context, incarnation uint64) (execbackend.Backend, func() error, error) {
		probe.builds.Add(1)
		return execbackend.Backend{}, func() error {
			probe.cleanups.Add(1)
			enteredOnce.Do(func() { close(cleanupEntered) })
			<-allowCleanup
			return nil
		}, nil
	}
	lease, err := pool.Acquire(context.Background(), id, build)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	closeDone := make(chan struct{})
	var closeErr error
	go func() {
		closeErr = pool.Close()
		close(closeDone)
	}()
	<-cleanupEntered

	releaseDone := make(chan struct{})
	go func() {
		if err := lease.Cleanup(); err != nil {
			t.Errorf("lease release racing Close: %v", err)
		}
		close(releaseDone)
	}()
	close(allowCleanup)
	<-closeDone
	<-releaseDone
	if closeErr != nil {
		t.Fatalf("Pool.Close: %v", closeErr)
	}
	if got := probe.cleanups.Load(); got != 1 {
		t.Fatalf("physical cleanups=%d during release/Close race, want 1", got)
	}
	if err := lease.Cleanup(); err != nil {
		t.Fatalf("late lease release after Close cleanup: %v", err)
	}
	if err := pool.Close(); err != nil {
		t.Fatalf("idempotent Pool.Close: %v", err)
	}
	if got := probe.cleanups.Load(); got != 1 {
		t.Fatalf("physical cleanups=%d after idempotent Close/release, want 1", got)
	}
}

func TestBackendResourcePoolInvalidateOutstandingLeasePoolCloseEnumeratesDetachedEntry(t *testing.T) {
	id := backendResourcePoolTestIdentity(t, "pool-detached-close")
	pool := newBackendResourcePool()
	probe := &backendResourcePoolProbe{}
	lease, err := pool.Acquire(context.Background(), id, probe.build)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	entry, _, _ := backendResourcePoolSnapshot(t, pool, id)
	if entry == nil {
		t.Fatal("current entry missing")
	}

	pool.Invalidate(id, entry.incarnation)
	current, claims, owned := backendResourcePoolEntryOwnershipSnapshot(t, pool, id, entry)
	if current || claims != 1 || !owned {
		t.Fatalf("after invalidation current=%v detached claims=%d owned=%v, want false/1/true", current, claims, owned)
	}
	if err := pool.Close(); err != nil {
		t.Fatalf("Pool.Close fail-safe detached cleanup: %v", err)
	}
	if got := probe.cleanups.Load(); got != 1 {
		t.Fatalf("physical cleanups=%d after detached Pool.Close, want 1", got)
	}
	if err := lease.Cleanup(); err != nil {
		t.Fatalf("late outstanding lease release: %v", err)
	}
	if got := probe.cleanups.Load(); got != 1 {
		t.Fatalf("physical cleanups=%d after late lease release, want 1", got)
	}
	if _, _, owned := backendResourcePoolSnapshot(t, pool, id); owned != 0 {
		t.Fatalf("owned entries=%d after detached cleanup, want 0", owned)
	}
}

func TestBackendResourcePoolCloseLinearizationRejectsNewClaimsAndPendingHandoff(t *testing.T) {
	id := backendResourcePoolTestIdentity(t, "pool-close-linearization")
	pool := newBackendResourcePool()
	probe := &backendResourcePoolProbe{}
	buildStarted := make(chan struct{})
	builderCanceled := make(chan struct{})
	var startedOnce sync.Once
	var canceledOnce sync.Once

	build := func(ctx context.Context, incarnation uint64) (execbackend.Backend, func() error, error) {
		probe.builds.Add(1)
		probe.lastIncarnation.Store(incarnation)
		startedOnce.Do(func() { close(buildStarted) })
		<-ctx.Done()
		canceledOnce.Do(func() { close(builderCanceled) })
		return execbackend.Backend{}, nil, ctx.Err()
	}

	firstDone := make(chan struct{})
	var firstErr error
	go func() {
		_, firstErr = pool.Acquire(context.Background(), id, build)
		close(firstDone)
	}()
	backendResourcePoolWaitSignal(t, buildStarted, "initial builder start")

	secondDone := make(chan struct{})
	var secondErr error
	go func() {
		_, secondErr = pool.Acquire(context.Background(), id, build)
		close(secondDone)
	}()
	backendResourcePoolWaitForClaims(t, pool, id, 2)

	closeDone := make(chan struct{})
	var closeErr error
	go func() {
		closeErr = pool.Close()
		close(closeDone)
	}()
	backendResourcePoolWaitForClosing(t, pool, id)

	// The Close linearization point rejects this claim even though the
	// original build entry has not yet completed its waiter handoff.
	postCloseDone := make(chan struct{})
	var postClose pluginreg.BackendBuildResult
	var postCloseErr error
	go func() {
		postClose, postCloseErr = pool.Acquire(context.Background(), id, build)
		close(postCloseDone)
	}()
	backendResourcePoolWaitSignal(t, postCloseDone, "post-close Acquire rejection")
	if postCloseErr == nil {
		t.Fatal("Acquire after Close linearization unexpectedly succeeded")
	}
	if postClose.Cleanup != nil {
		t.Fatal("post-close Acquire returned a lease cleanup")
	}
	if got := probe.builds.Load(); got != 1 {
		t.Fatalf("post-close Acquire started builds=%d, want 1", got)
	}

	backendResourcePoolWaitSignal(t, builderCanceled, "pool-owned builder cancellation")
	backendResourcePoolWaitSignal(t, firstDone, "initiating Acquire completion")
	backendResourcePoolWaitSignal(t, secondDone, "pending waiter completion")
	backendResourcePoolWaitSignal(t, closeDone, "Pool.Close completion")
	if closeErr != nil {
		t.Fatalf("Pool.Close: %v", closeErr)
	}
	if !errors.Is(firstErr, context.Canceled) || !errors.Is(secondErr, context.Canceled) {
		t.Fatalf("pending Acquires saw first=%v second=%v, want context.Canceled", firstErr, secondErr)
	}
	if got := probe.cleanups.Load(); got != 0 {
		t.Fatalf("physical cleanups=%d after canceled build, want 0", got)
	}
}

func TestBackendResourcePoolCloseCancelsAndJoinsPoolOwnedBuilder(t *testing.T) {
	id := backendResourcePoolTestIdentity(t, "pool-close-builder-join")
	pool := newBackendResourcePool()
	probe := &backendResourcePoolProbe{}
	buildStarted := make(chan struct{})
	builderCanceled := make(chan struct{})
	allowBuilderReturn := make(chan struct{})
	var startedOnce sync.Once
	var canceledOnce sync.Once

	build := func(ctx context.Context, incarnation uint64) (execbackend.Backend, func() error, error) {
		probe.builds.Add(1)
		probe.lastIncarnation.Store(incarnation)
		startedOnce.Do(func() { close(buildStarted) })
		<-ctx.Done()
		canceledOnce.Do(func() { close(builderCanceled) })
		<-allowBuilderReturn
		return execbackend.Backend{}, nil, ctx.Err()
	}

	acquireDone := make(chan struct{})
	var acquireErr error
	go func() {
		_, acquireErr = pool.Acquire(context.Background(), id, build)
		close(acquireDone)
	}()
	backendResourcePoolWaitSignal(t, buildStarted, "builder start")

	closeDone := make(chan struct{})
	var closeErr error
	go func() {
		closeErr = pool.Close()
		close(closeDone)
	}()
	backendResourcePoolWaitForClosing(t, pool, id)
	backendResourcePoolWaitSignal(t, builderCanceled, "builder cancellation")

	// Close must join the builder; it cannot return while the builder is held
	// after observing cancellation.
	select {
	case <-closeDone:
		t.Fatal("Pool.Close returned before joining the canceled builder")
	default:
	}
	close(allowBuilderReturn)
	backendResourcePoolWaitSignal(t, acquireDone, "Acquire completion after builder cancellation")
	backendResourcePoolWaitSignal(t, closeDone, "Pool.Close completion after builder join")
	if closeErr != nil {
		t.Fatalf("Pool.Close: %v", closeErr)
	}
	if !errors.Is(acquireErr, context.Canceled) {
		t.Fatalf("Acquire error=%v, want context.Canceled", acquireErr)
	}
	if got := probe.cleanups.Load(); got != 0 {
		t.Fatalf("physical cleanups=%d after canceled builder, want 0", got)
	}
}

func TestBackendResourcePoolCloseLateSuccessfulBuildCleansExactlyOnceAndNeverPublishes(t *testing.T) {
	id := backendResourcePoolTestIdentity(t, "pool-close-late-success")
	pool := newBackendResourcePool()
	probe := &backendResourcePoolProbe{}
	buildStarted := make(chan struct{})
	allowSuccess := make(chan struct{})
	cleanupEntered := make(chan struct{})
	allowCleanup := make(chan struct{})
	acquireDone := make(chan struct{})
	closeDone := make(chan struct{})
	closeStarted := false
	var entry *backendResourceEntry
	var cleanupBeforeHandoff atomic.Bool
	var startedOnce sync.Once
	var cleanupOnce sync.Once
	var successReleaseOnce, cleanupReleaseOnce sync.Once
	releaseSuccess := func() { successReleaseOnce.Do(func() { close(allowSuccess) }) }
	releaseCleanup := func() { cleanupReleaseOnce.Do(func() { close(allowCleanup) }) }

	build := func(_ context.Context, incarnation uint64) (execbackend.Backend, func() error, error) {
		probe.builds.Add(1)
		probe.lastIncarnation.Store(incarnation)
		startedOnce.Do(func() { close(buildStarted) })
		<-allowSuccess
		return execbackend.Backend{}, func() error {
			// The pool joins its internal Acquire handoff, not the caller's
			// later observation of that return. All claims must be released.
			pool.mu.Lock()
			if entry == nil || entry.claims != 0 {
				cleanupBeforeHandoff.Store(true)
			}
			pool.mu.Unlock()
			probe.cleanups.Add(1)
			cleanupOnce.Do(func() { close(cleanupEntered) })
			<-allowCleanup
			return nil
		}, nil
	}

	var acquired pluginreg.BackendBuildResult
	var acquireErr error
	go func() {
		acquired, acquireErr = pool.Acquire(context.Background(), id, build)
		close(acquireDone)
	}()
	// Release blocked callbacks and join the pool even if an assertion fails.
	// Otherwise this test's failure contaminates later generation leak checks.
	t.Cleanup(func() {
		releaseSuccess()
		releaseCleanup()
		if err := pool.Close(); err != nil {
			t.Errorf("fixture Pool.Close: %v", err)
		}
		backendResourcePoolWaitSignal(t, acquireDone, "fixture Acquire cleanup")
		if closeStarted {
			backendResourcePoolWaitSignal(t, closeDone, "fixture Close cleanup")
		}
	})
	backendResourcePoolWaitSignal(t, buildStarted, "late-success builder start")
	entry, _, _ = backendResourcePoolSnapshot(t, pool, id)
	if entry == nil {
		t.Fatal("late-success building entry missing")
	}

	var closeErr error
	go func() {
		closeErr = pool.Close()
		close(closeDone)
	}()
	closeStarted = true
	backendResourcePoolWaitForClosing(t, pool, id)

	// The physical builder is deliberately allowed to succeed after Close has
	// linearized.  The result must be cleaned, never published or handed off.
	releaseSuccess()
	backendResourcePoolWaitSignal(t, acquireDone, "late-success Acquire completion")
	if acquireErr == nil {
		t.Fatal("late-success Acquire unexpectedly succeeded after Close")
	}
	if acquired.Cleanup != nil {
		t.Fatal("late-success Acquire received a post-close lease cleanup")
	}
	backendResourcePoolWaitSignal(t, cleanupEntered, "late physical cleanup")
	if cleanupBeforeHandoff.Load() {
		t.Fatal("residual physical cleanup began before the pending Acquire handoff terminated")
	}
	select {
	case <-closeDone:
		t.Fatal("Pool.Close returned before late-result cleanup completed")
	default:
	}
	postCloseDone := make(chan struct{})
	var postClose pluginreg.BackendBuildResult
	var postCloseErr error
	go func() {
		postClose, postCloseErr = pool.Acquire(context.Background(), id, build)
		close(postCloseDone)
	}()
	backendResourcePoolWaitSignal(t, postCloseDone, "post-close Acquire rejection after late result")
	if postCloseErr == nil {
		t.Fatal("Acquire after late-result Close unexpectedly succeeded")
	}
	if postClose.Cleanup != nil {
		t.Fatal("post-close Acquire returned a lease cleanup")
	}
	releaseCleanup()
	backendResourcePoolWaitSignal(t, closeDone, "late-result Pool.Close completion")
	if closeErr != nil {
		t.Fatalf("Pool.Close: %v", closeErr)
	}
	if got := probe.builds.Load(); got != 1 {
		t.Fatalf("physical builds=%d, want 1", got)
	}
	if got := probe.cleanups.Load(); got != 1 {
		t.Fatalf("physical cleanups=%d, want exactly 1", got)
	}
	if _, _, owned := backendResourcePoolSnapshot(t, pool, id); owned != 0 {
		t.Fatalf("owned entries=%d after late-result cleanup, want 0", owned)
	}
	if err := pool.Close(); err != nil {
		t.Fatalf("idempotent Pool.Close: %v", err)
	}
	if got := probe.cleanups.Load(); got != 1 {
		t.Fatalf("physical cleanups=%d after idempotent Close, want exactly 1", got)
	}
}

func TestBackendResourcePoolCloseCanceledWaiterAndPendingBuilderNoLeak(t *testing.T) {
	// This test owns only the in-memory pool and its builder/waiter/handoff
	// goroutines. Other package tests (for example the provider-profile
	// diagnostics test) perform real HTTPS discovery through a shared
	// process-global transport and leave an idle net/http HTTP/2 keep-alive
	// readLoop behind; that connection is created before this test starts and
	// is not owned here. IgnoreCurrent snapshots the goroutines already running
	// at this point so the assertion still fails on any goroutine this test
	// actually leaks while not misattributing unrelated pooled HTTP connections.
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	id := backendResourcePoolTestIdentity(t, "pool-close-canceled-waiter")
	pool := newBackendResourcePool()
	probe := &backendResourcePoolProbe{}
	buildStarted := make(chan struct{})
	builderCanceled := make(chan struct{})
	allowBuilderReturn := make(chan struct{})
	var startedOnce sync.Once
	var canceledOnce sync.Once

	build := func(ctx context.Context, incarnation uint64) (execbackend.Backend, func() error, error) {
		probe.builds.Add(1)
		probe.lastIncarnation.Store(incarnation)
		startedOnce.Do(func() { close(buildStarted) })
		<-ctx.Done()
		canceledOnce.Do(func() { close(builderCanceled) })
		<-allowBuilderReturn
		return execbackend.Backend{}, nil, ctx.Err()
	}

	firstDone := make(chan struct{})
	var firstErr error
	go func() {
		_, firstErr = pool.Acquire(context.Background(), id, build)
		close(firstDone)
	}()
	backendResourcePoolWaitSignal(t, buildStarted, "canceled-waiter builder start")

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterDone := make(chan struct{})
	var waiterErr error
	go func() {
		_, waiterErr = pool.Acquire(waiterCtx, id, build)
		close(waiterDone)
	}()
	backendResourcePoolWaitForClaims(t, pool, id, 2)
	cancelWaiter()
	backendResourcePoolWaitSignal(t, waiterDone, "waiter cancellation")
	if !errors.Is(waiterErr, context.Canceled) {
		t.Fatalf("waiter error=%v, want context.Canceled", waiterErr)
	}

	closeDone := make(chan struct{})
	var closeErr error
	go func() {
		closeErr = pool.Close()
		close(closeDone)
	}()
	backendResourcePoolWaitForClosing(t, pool, id)
	backendResourcePoolWaitSignal(t, builderCanceled, "pending builder cancellation")
	select {
	case <-closeDone:
		t.Fatal("Pool.Close returned before pending builder joined")
	default:
	}
	close(allowBuilderReturn)
	backendResourcePoolWaitSignal(t, firstDone, "initiating Acquire after waiter cancellation")
	backendResourcePoolWaitSignal(t, closeDone, "Pool.Close after canceled waiter")
	if closeErr != nil {
		t.Fatalf("Pool.Close: %v", closeErr)
	}
	if !errors.Is(firstErr, context.Canceled) {
		t.Fatalf("initiating Acquire error=%v, want context.Canceled", firstErr)
	}
	if got := probe.cleanups.Load(); got != 0 {
		t.Fatalf("physical cleanups=%d after canceled waiter/builder, want 0", got)
	}
}

type backendResourcePoolProbe struct {
	builds          atomic.Int32
	cleanups        atomic.Int32
	lastIncarnation atomic.Uint64
}

func (p *backendResourcePoolProbe) build(_ context.Context, incarnation uint64) (execbackend.Backend, func() error, error) {
	p.builds.Add(1)
	p.lastIncarnation.Store(incarnation)
	return execbackend.Backend{}, p.cleanup(incarnation), nil
}

func (p *backendResourcePoolProbe) cleanup(_ uint64) func() error {
	return func() error {
		p.cleanups.Add(1)
		return nil
	}
}

func backendResourcePoolTestIdentity(t *testing.T, instanceID string) backendResourceIdentity {
	t.Helper()
	in := testBackendResourcePhysicalInput()
	in.InstanceID = instanceID
	id, shareable, err := physicalIdentity(in)
	if err != nil {
		t.Fatalf("physical identity %q: %v", instanceID, err)
	}
	if !shareable {
		t.Fatalf("physical identity %q is not shareable", instanceID)
	}
	return id
}

func backendResourcePoolSnapshot(t *testing.T, pool *backendResourcePool, id backendResourceIdentity) (*backendResourceEntry, int, int) {
	t.Helper()
	pool.mu.Lock()
	defer pool.mu.Unlock()
	entry := pool.current[id]
	claims := 0
	if entry != nil {
		claims = entry.claims
	}
	return entry, claims, len(pool.owned)
}

func backendResourcePoolEntryOwnershipSnapshot(t *testing.T, pool *backendResourcePool, id backendResourceIdentity, entry *backendResourceEntry) (bool, int, bool) {
	t.Helper()
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return pool.current[id] == entry, entry.claims, func() bool {
		_, ok := pool.owned[entry]
		return ok
	}()
}

func backendResourcePoolWaitForClaims(t *testing.T, pool *backendResourcePool, id backendResourceIdentity, want int) {
	t.Helper()
	// This is only a generous deadlock fail-safe.  Scheduling remains driven
	// by the explicit claim barrier below; the timer is not a performance
	// assertion and does not make the test pass based on elapsed time.
	deadlockGuard := time.NewTimer(30 * time.Second)
	defer deadlockGuard.Stop()
	for {
		pool.mu.Lock()
		entry := pool.current[id]
		claims := 0
		if entry != nil {
			claims = entry.claims
		}
		owned := len(pool.owned)
		pool.mu.Unlock()
		if claims >= want {
			return
		}
		select {
		case <-deadlockGuard.C:
			t.Fatalf("claim reservation barrier not reached: current=%v claims=%d owned=%d want=%d", entry != nil, claims, owned, want)
		default:
			// Yield until the explicit claim barrier is reached.  No sleep is
			// used; the timer above only converts a broken protocol into a
			// bounded failure with its observed state.
			runtime.Gosched()
		}
	}
}

func backendResourcePoolWaitForClosing(t *testing.T, pool *backendResourcePool, id backendResourceIdentity) {
	t.Helper()
	deadlockGuard := time.NewTimer(30 * time.Second)
	defer deadlockGuard.Stop()
	for {
		pool.mu.Lock()
		closing := pool.closing
		entry := pool.current[id]
		claims := 0
		if entry != nil {
			claims = entry.claims
		}
		owned := len(pool.owned)
		pool.mu.Unlock()
		if closing {
			return
		}
		select {
		case <-deadlockGuard.C:
			t.Fatalf("Pool.Close linearization not observed: closing=%v current=%v claims=%d owned=%d", closing, entry != nil, claims, owned)
			return
		default:
			runtime.Gosched()
		}
	}
}

func backendResourcePoolWaitSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	deadlockGuard := time.NewTimer(30 * time.Second)
	defer deadlockGuard.Stop()
	select {
	case <-signal:
	case <-deadlockGuard.C:
		t.Fatalf("timed out waiting for %s", label)
	}
}
