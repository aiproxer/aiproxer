package archtest

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSourceScanCache_ConcurrentFirstLoads(t *testing.T) {
	t.Parallel()
	root := setupTestScanRepo(t)
	const concurrency = 30
	var wg sync.WaitGroup
	errs := make(chan error, concurrency*2)

	for i := range concurrency {
		wg.Add(2)
		go func(idx int) {
			defer wg.Done()
			var files []string
			if err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
				files = append(files, rel)
				return nil
			}); err != nil || len(files) != 3 {
				errs <- fmt.Errorf("prod walk goroutine %d failed (len=%d): %w", idx, len(files), err)
			}
		}(i)

		go func(idx int) {
			defer wg.Done()
			var files []string
			if err := WalkGoFiles(root, func(rel, abs string, src []byte) error {
				files = append(files, rel)
				return nil
			}); err != nil || len(files) != 6 {
				errs <- fmt.Errorf("all walk goroutine %d failed (len=%d): %w", idx, len(files), err)
			}
		}(i)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestSourceScanCache_ConcurrentCoalescing(t *testing.T) {
	t.Parallel()
	uniqueKey := sourceScanCacheKey{
		canonicalRoot: filepath.Join(t.TempDir(), "coalescing-key"),
		includeTests:  false,
	}

	var loadCount atomic.Int32
	loaderStarted := make(chan struct{})
	loaderRelease := make(chan struct{})
	var startOnce sync.Once

	customLoader := func() ([]sourceFileEntry, error) {
		loadCount.Add(1)
		startOnce.Do(func() { close(loaderStarted) })
		<-loaderRelease
		return []sourceFileEntry{{rel: "pkg/foo.go", abs: "/pkg/foo.go", src: []byte("package foo")}}, nil
	}

	const numWaiters = 20
	var wg sync.WaitGroup
	var waitersRegistered sync.WaitGroup
	waitersRegistered.Add(numWaiters - 1)
	errs := make(chan error, numWaiters)

	wg.Go(func() {
		entries, err := loadCachedSourceFiles(uniqueKey, customLoader)
		if err != nil || len(entries) != 1 {
			errs <- fmt.Errorf("initial caller err=%v len=%d", err, len(entries))
		}
	})

	<-loaderStarted
	for i := 1; i < numWaiters; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			entries, err := loadCachedSourceFilesWithObserver(uniqueKey, customLoader, func() {
				waitersRegistered.Done()
			})
			if err != nil || len(entries) != 1 {
				errs <- fmt.Errorf("waiter %d err=%v len=%d", idx, err, len(entries))
			}
		}(i)
	}

	waitersRegistered.Wait()
	close(loaderRelease)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
	if count := loadCount.Load(); count != 1 {
		t.Fatalf("expected exactly 1 loader invocation, got %d", count)
	}
}

func TestSourceScanCache_SharedErrorAndRetry(t *testing.T) {
	t.Parallel()
	sentinelErr := errors.New("sentinel disk load failure")
	uniqueKey := sourceScanCacheKey{
		canonicalRoot: filepath.Join(t.TempDir(), "shared-error-key"),
		includeTests:  false,
	}

	var loadCount atomic.Int32
	loaderStarted := make(chan struct{})
	loaderRelease := make(chan struct{})
	var startOnce sync.Once

	failingLoader := func() ([]sourceFileEntry, error) {
		loadCount.Add(1)
		startOnce.Do(func() { close(loaderStarted) })
		<-loaderRelease
		return nil, sentinelErr
	}

	const numWaiters = 10
	var wg sync.WaitGroup
	var waitersRegistered sync.WaitGroup
	waitersRegistered.Add(numWaiters - 1)
	errs := make(chan error, numWaiters)

	wg.Go(func() {
		if _, err := loadCachedSourceFiles(uniqueKey, failingLoader); !errors.Is(err, sentinelErr) {
			errs <- fmt.Errorf("initial caller got %v, want sentinelErr", err)
		}
	})

	<-loaderStarted
	for i := 1; i < numWaiters; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if _, err := loadCachedSourceFilesWithObserver(uniqueKey, failingLoader, func() {
				waitersRegistered.Done()
			}); !errors.Is(err, sentinelErr) {
				errs <- fmt.Errorf("waiter %d got %v, want sentinelErr", idx, err)
			}
		}(i)
	}

	waitersRegistered.Wait()
	close(loaderRelease)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
	if count := loadCount.Load(); count != 1 {
		t.Fatalf("expected 1 loader call during failure run, got %d", count)
	}

	successLoader := func() ([]sourceFileEntry, error) {
		loadCount.Add(1)
		return []sourceFileEntry{{rel: "pkg/bar.go", abs: "/pkg/bar.go", src: []byte("package bar")}}, nil
	}
	entries, err := loadCachedSourceFiles(uniqueKey, successLoader)
	if err != nil || len(entries) != 1 || loadCount.Load() != 2 {
		t.Fatalf("retry load failed: entries=%d err=%v count=%d", len(entries), err, loadCount.Load())
	}
}

func TestSourceScanCache_PanicRecoveryAndSubsequentCall(t *testing.T) {
	t.Parallel()
	uniqueKey := sourceScanCacheKey{
		canonicalRoot: filepath.Join(t.TempDir(), "panic-key"),
		includeTests:  false,
	}

	var loadCount atomic.Int32
	panicMsg := "intentional test panic in loader"
	panickingLoader := func() ([]sourceFileEntry, error) {
		loadCount.Add(1)
		panic(panicMsg)
	}

	recoveredVal := make(chan any, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				recoveredVal <- r
			}
		}()
		_, _ = loadCachedSourceFiles(uniqueKey, panickingLoader)
	}()

	var r any
	select {
	case r = <-recoveredVal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for loader panic to be caught")
	}

	if r != panicMsg {
		t.Fatalf("unexpected panic value: %v, want %q", r, panicMsg)
	}
	if count := loadCount.Load(); count != 1 {
		t.Fatalf("expected 1 loader call, got %d", count)
	}

	successLoader := func() ([]sourceFileEntry, error) {
		loadCount.Add(1)
		return []sourceFileEntry{{rel: "pkg/rec.go", abs: "/pkg/rec.go", src: []byte("package rec")}}, nil
	}

	entries, err := loadCachedSourceFiles(uniqueKey, successLoader)
	if err != nil || len(entries) != 1 || loadCount.Load() != 2 {
		t.Fatalf("subsequent load failed: len=%d err=%v count=%d", len(entries), err, loadCount.Load())
	}
}

func TestSourceScanCache_PanicConcurrentWaitersUnblock(t *testing.T) {
	t.Parallel()
	uniqueKey := sourceScanCacheKey{
		canonicalRoot: filepath.Join(t.TempDir(), "panic-waiters-key"),
		includeTests:  false,
	}

	var loadCount atomic.Int32
	loaderStarted := make(chan struct{})
	loaderRelease := make(chan struct{})
	var startOnce sync.Once

	panicMsg := "winner panicked during scan"
	panickingLoader := func() ([]sourceFileEntry, error) {
		loadCount.Add(1)
		startOnce.Do(func() { close(loaderStarted) })
		<-loaderRelease
		panic(panicMsg)
	}

	const numWaiters = 5
	var wg sync.WaitGroup
	var waitersRegistered sync.WaitGroup
	waitersRegistered.Add(numWaiters)
	waiterErrs := make(chan error, numWaiters)

	winnerPanicCh := make(chan any, 1)

	wg.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				winnerPanicCh <- r
			}
		}()
		_, _ = loadCachedSourceFiles(uniqueKey, panickingLoader)
	})

	<-loaderStarted
	for i := range numWaiters {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := loadCachedSourceFilesWithObserver(uniqueKey, panickingLoader, func() {
				waitersRegistered.Done()
			})
			if err == nil {
				waiterErrs <- fmt.Errorf("waiter %d expected error on winner panic, got nil", idx)
			} else if !strings.Contains(err.Error(), panicMsg) {
				waiterErrs <- fmt.Errorf("waiter %d error %q does not contain panic msg %q", idx, err.Error(), panicMsg)
			}
		}(i)
	}

	waitersRegistered.Wait()
	close(loaderRelease)
	wg.Wait()
	close(waiterErrs)

	var r any
	select {
	case r = <-winnerPanicCh:
	default:
		t.Fatal("winner panic was not caught")
	}
	if r != panicMsg {
		t.Fatalf("winner panic value = %v, want %q", r, panicMsg)
	}

	for err := range waiterErrs {
		t.Error(err)
	}

	if count := loadCount.Load(); count != 1 {
		t.Fatalf("expected exactly 1 loader invocation, got %d", count)
	}

	// Verify successor can now load successfully
	successLoader := func() ([]sourceFileEntry, error) {
		loadCount.Add(1)
		return []sourceFileEntry{{rel: "pkg/postpanic.go", abs: "/pkg/postpanic.go", src: []byte("package postpanic")}}, nil
	}
	entries, err := loadCachedSourceFiles(uniqueKey, successLoader)
	if err != nil || len(entries) != 1 || loadCount.Load() != 2 {
		t.Fatalf("successor load failed: len=%d err=%v count=%d", len(entries), err, loadCount.Load())
	}
}

func TestSourceScanCache_LinearizeFailureCompletion(t *testing.T) {
	t.Parallel()
	uniqueKey := sourceScanCacheKey{
		canonicalRoot: filepath.Join(t.TempDir(), "linearize-key"),
		includeTests:  false,
	}

	var loadCount atomic.Int32
	loaderStarted := make(chan struct{})
	loaderRelease := make(chan struct{})
	var startOnce sync.Once

	failingLoader := func() ([]sourceFileEntry, error) {
		loadCount.Add(1)
		startOnce.Do(func() { close(loaderStarted) })
		<-loaderRelease
		return nil, errors.New("predecessor failed")
	}

	var waiterRegistered sync.WaitGroup
	waiterRegistered.Add(1)

	var wg sync.WaitGroup

	// Predecessor (winner)
	wg.Go(func() {
		_, _ = loadCachedSourceFiles(uniqueKey, failingLoader)
	})

	// Establish the predecessor before starting a waiter; otherwise the waiter
	// can win the cache race and never execute its registration callback.
	<-loaderStarted

	// Waiter
	waiterUnblocked := make(chan struct{})
	wg.Go(func() {
		_, _ = loadCachedSourceFilesWithObserver(uniqueKey, failingLoader, func() {
			waiterRegistered.Done()
		})
		close(waiterUnblocked)
	})

	waiterRegistered.Wait()

	// Waiter is blocked on predecessor's entry.done.
	// Now release predecessor.
	close(loaderRelease)

	// Wait for predecessor and waiter to finish
	wg.Wait()

	select {
	case <-waiterUnblocked:
	default:
		t.Fatal("waiter was not unblocked")
	}

	// Now successor must run cleanly
	successLoader := func() ([]sourceFileEntry, error) {
		loadCount.Add(1)
		return []sourceFileEntry{{rel: "pkg/succ.go", abs: "/pkg/succ.go", src: []byte("package succ")}}, nil
	}
	entries, err := loadCachedSourceFiles(uniqueKey, successLoader)
	if err != nil || len(entries) != 1 || loadCount.Load() != 2 {
		t.Fatalf("successor load failed: len=%d err=%v count=%d", len(entries), err, loadCount.Load())
	}
}
