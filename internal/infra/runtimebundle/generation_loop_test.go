package runtimebundle

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStartOwnedLoop_RunsWorkAfterOwnership(t *testing.T) {
	t.Parallel()
	ledger := NewResourceLedger()
	ran := make(chan struct{})
	startOwnedLoop(context.Background(), ledger, "loop", PhaseQuiesce, func(ctx context.Context) {
		close(ran)
	})
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not run after ownership was established")
	}
	if err := ledger.Quiesce(context.Background()); err != nil {
		t.Fatalf("quiesce: %v", err)
	}
}

func TestStartOwnedLoop_NilLedgerNoop(t *testing.T) {
	t.Parallel()
	var worked atomic.Bool
	// The nil-ledger guard is the first line of startOwnedLoop and returns
	// synchronously, so the loop body is never scheduled; asserting immediately
	// after the call is deterministic and needs no sleep.
	startOwnedLoop(context.Background(), nil, "loop", PhaseQuiesce, func(ctx context.Context) {
		worked.Store(true)
	})
	if worked.Load() {
		t.Fatal("nil ledger must not start an unowned loop")
	}
}

func TestStartOwnedLoop_SealedLedgerCancelsBeforeWork(t *testing.T) {
	t.Parallel()
	ledger := NewResourceLedger()
	if err := ledger.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	var worked atomic.Bool
	// The sealed ledger triggers synchronous immediate cleanup: the gated loop
	// must observe cancellation and never enter application work.
	startOwnedLoop(context.Background(), ledger, "loop", PhaseQuiesce, func(ctx context.Context) {
		worked.Store(true)
	})
	if worked.Load() {
		t.Fatal("loop ran application work despite a sealed ledger")
	}
}

func TestStartOwnedLoop_PreCancelledParentSkipsWork(t *testing.T) {
	t.Parallel()
	ledger := NewResourceLedger()
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	var worked atomic.Bool
	startOwnedLoop(parent, ledger, "loop", PhaseQuiesce, func(ctx context.Context) {
		worked.Store(true)
	})
	if err := ledger.Quiesce(context.Background()); err != nil {
		t.Fatalf("quiesce: %v", err)
	}
	if worked.Load() {
		t.Fatal("loop ran application work despite a cancelled parent")
	}
}

func TestStartOwnedLoop_QuiesceCancelsAndJoins(t *testing.T) {
	t.Parallel()
	ledger := NewResourceLedger()
	entered := make(chan struct{})
	exited := make(chan struct{})
	startOwnedLoop(context.Background(), ledger, "loop", PhaseQuiesce, func(ctx context.Context) {
		close(entered)
		<-ctx.Done()
		close(exited)
	})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not enter application work")
	}
	if err := ledger.Quiesce(context.Background()); err != nil {
		t.Fatalf("quiesce: %v", err)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not join after quiesce")
	}
}

func TestStartOwnedLoop_RollbackCancelsAndJoins(t *testing.T) {
	t.Parallel()
	ledger := NewResourceLedger()
	entered := make(chan struct{})
	exited := make(chan struct{})
	startOwnedLoop(context.Background(), ledger, "loop", PhaseQuiesce, func(ctx context.Context) {
		close(entered)
		<-ctx.Done()
		close(exited)
	})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not enter application work")
	}
	if err := ledger.Rollback(context.Background()); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not join after rollback")
	}
}

func TestStartOwnedLoop_QuiesceCompletesBeforeClosePhase(t *testing.T) {
	t.Parallel()
	ledger := NewResourceLedger()
	var mu sync.Mutex
	var order []string
	track := func(name string) func() error {
		return func() error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}
	loopEntered := make(chan struct{})
	startOwnedLoop(context.Background(), ledger, "refresh", PhaseQuiesce, func(ctx context.Context) {
		close(loopEntered)
		<-ctx.Done()
		_ = track("refresh-exit")()
	})
	select {
	case <-loopEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not enter application work")
	}
	ledger.AddClose("catalog", PhaseClose, track("catalog-close"))

	if err := ledger.Quiesce(context.Background()); err != nil {
		t.Fatalf("quiesce: %v", err)
	}
	if err := ledger.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "refresh-exit" || got[1] != "catalog-close" {
		t.Fatalf("order=%v, want [refresh-exit catalog-close]", got)
	}
}

func TestStartOwnedLoop_ConcurrentRetirementNoLeak(t *testing.T) {
	t.Parallel()
	const loops = 32
	ledger := NewResourceLedger()
	entered := make(chan struct{}, loops)
	exited := make(chan struct{}, loops)
	for range loops {
		startOwnedLoop(context.Background(), ledger, "loop", PhaseQuiesce, func(ctx context.Context) {
			entered <- struct{}{}
			<-ctx.Done()
			exited <- struct{}{}
		})
	}
	for i := range loops {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatalf("loop %d did not enter", i)
		}
	}
	if err := ledger.Quiesce(context.Background()); err != nil {
		t.Fatalf("quiesce: %v", err)
	}
	for i := range loops {
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			t.Fatalf("loop %d did not join within timeout", i)
		}
	}
}
