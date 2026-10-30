package acp

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestExecutableCache_ResetClearsCachedEntries(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	cache := NewExecutableCache(func(string) (string, error) {
		return fmt.Sprintf("resolved-%d", calls.Add(1)), nil
	})
	if _, err := cache.LookPath("cached"); err != nil {
		t.Fatalf("initial lookup: %v", err)
	}
	if _, err := cache.LookPath("cached"); err != nil {
		t.Fatalf("cached lookup: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("before Reset resolver calls = %d, want 1", got)
	}

	cache.Reset()

	if _, err := cache.LookPath("cached"); err != nil {
		t.Fatalf("lookup after Reset: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("after Reset resolver calls = %d, want 2", got)
	}
}

func TestExecutableCache_ResetConcurrentWithLookups(t *testing.T) {
	t.Parallel()
	runDeterministicExecutableCacheSchedule(t)
}

func TestExecutableCache_ResetGeneration(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var calls atomic.Int32
	started := make(chan int32, 2)
	releaseOld := make(chan struct{})
	cache := NewExecutableCache(func(string) (string, error) {
		call := calls.Add(1)
		started <- call
		if call == 1 {
			<-releaseOld
			return "old-generation", nil
		}
		return "fresh-generation", nil
	})

	oldResult := make(chan string, 1)
	go func() {
		path, _ := cache.LookPath("tool")
		oldResult <- path
	}()
	waitForResolverCallContext(ctx, t, started, 1)

	cache.Reset()
	freshResult := make(chan string, 1)
	go func() {
		path, _ := cache.LookPath("tool")
		freshResult <- path
	}()
	waitForResolverCallContext(ctx, t, started, 2)

	select {
	case got := <-freshResult:
		if got != "fresh-generation" {
			t.Fatalf("post-reset lookup = %q, want fresh generation", got)
		}
	case <-ctx.Done():
		t.Fatalf("post-reset lookup did not complete while old lookup was in flight: %v", ctx.Err())
	}

	close(releaseOld)
	select {
	case got := <-oldResult:
		if got != "old-generation" {
			t.Fatalf("in-flight lookup = %q, want old generation", got)
		}
	case <-ctx.Done():
		t.Fatalf("old lookup did not complete after resolver release: %v", ctx.Err())
	}

	path, err := cache.LookPath("tool")
	if err != nil || path != "fresh-generation" {
		t.Fatalf("cached post-reset lookup = %q, %v; want fresh generation", path, err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("resolver calls = %d, want 2", got)
	}
}

func TestExecutableCache_DeterministicConcurrency(t *testing.T) {
	t.Parallel()
	runDeterministicExecutableCacheSchedule(t)
}

func TestExecutableCache_InstanceOwnership(t *testing.T) {
	t.Parallel()

	first := NewExecutableCache(func(string) (string, error) { return "first", nil })
	second := NewExecutableCache(func(string) (string, error) { return "second", nil })

	if got, _ := first.LookPath("tool"); got != "first" {
		t.Fatalf("first cache lookup = %q, want first", got)
	}
	if got, _ := second.LookPath("tool"); got != "second" {
		t.Fatalf("second cache lookup = %q, want second", got)
	}

	first.Reset()
	if got, _ := first.LookPath("tool"); got != "first" {
		t.Fatalf("first cache lookup after Reset = %q, want first", got)
	}
	if got, _ := second.LookPath("tool"); got != "second" {
		t.Fatalf("second cache lookup after first Reset = %q, want second", got)
	}
}

func TestExecutableCache_RealLookupSmoke(t *testing.T) {
	t.Parallel()

	cache := NewExecutableCache(nil)
	goPath, err := cache.LookPath("go")
	if err != nil {
		t.Fatalf("required executable %q was not found on PATH: %v; install Go or add its bin directory to PATH", "go", err)
	}
	if goPath == "" {
		t.Fatal("real resolver returned an empty path for go")
	}

	const missing = "aip-acp-reliability-known-missing-7f4d"
	if _, err := cache.LookPath(missing); err == nil {
		t.Fatalf("known-missing executable %q unexpectedly resolved", missing)
	}
}

func runDeterministicExecutableCacheSchedule(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var calls atomic.Int32
	started := make(chan int32, 2)
	releaseOld := make(chan struct{})
	cache := NewExecutableCache(func(string) (string, error) {
		call := calls.Add(1)
		started <- call
		if call == 1 {
			<-releaseOld
			return "old", nil
		}
		return "fresh", nil
	})

	var wg sync.WaitGroup
	oldResult := make(chan string, 1)
	go func() {
		path, _ := cache.LookPath("scheduled")
		oldResult <- path
	}()
	waitForResolverCallContext(ctx, t, started, 1)

	cache.Reset()

	const freshLookups = 8
	results := make(chan string, freshLookups)
	wg.Add(freshLookups)
	for range freshLookups {
		go func() {
			defer wg.Done()
			path, _ := cache.LookPath("scheduled")
			results <- path
		}()
	}
	waitForResolverCallContext(ctx, t, started, 2)

	for range freshLookups {
		select {
		case got := <-results:
			if got != "fresh" {
				t.Errorf("post-reset lookup = %q, want fresh", got)
			}
		case <-ctx.Done():
			t.Fatalf("post-reset lookup blocked: %v", ctx.Err())
		}
	}
	close(releaseOld)
	wg.Wait()
	select {
	case got := <-oldResult:
		if got != "old" {
			t.Fatalf("old in-flight lookup = %q, want old", got)
		}
	case <-ctx.Done():
		t.Fatalf("old lookup blocked: %v", ctx.Err())
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("resolver calls = %d, want 2", got)
	}
}

func waitForResolverCallContext(ctx context.Context, t *testing.T, started <-chan int32, want int32) {
	t.Helper()
	select {
	case got := <-started:
		if got != want {
			t.Fatalf("resolver call = %d, want %d", got, want)
		}
	case <-ctx.Done():
		t.Fatalf("waiting for resolver call %d: %v", want, ctx.Err())
	}
}
