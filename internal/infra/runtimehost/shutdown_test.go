package runtimehost_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
)

type countingCloser struct{ closes atomic.Int32 }

func (c *countingCloser) Close() error {
	c.closes.Add(1)
	return nil
}

type reentrantCloser struct {
	m      *runtimehost.Manager
	closes atomic.Int32
}

func (c *reentrantCloser) Close() error {
	c.closes.Add(1)
	_ = c.m.ShuttingDown()
	_ = c.m.RetainedCount()
	_, _ = c.m.Acquire()
	return nil
}

func TestManager_ShutdownDetached_PreventsAcquire(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	g := m.PrepareOwned("shutdown", &countingCloser{})
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}
	m.BeginShutdown()
	if _, ok := m.Acquire(); ok {
		t.Fatal("Acquire must fail after BeginShutdown")
	}
	detached := m.DetachActive()
	if detached != g {
		t.Fatal("DetachActive must return prior active")
	}
	if m.Active() != nil {
		t.Fatal("active must be nil after detach")
	}
	st := g.Lifecycle()
	if st != runtimehost.GenRetiring && st != runtimehost.GenDrained {
		t.Fatalf("lifecycle=%v", st)
	}
}

func TestManager_AcquireCrossingBeginShutdownDoesNotEscape(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	g := m.PrepareOwned("shutdown-race", &countingCloser{})
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}

	retained := make(chan struct{})
	resume := make(chan struct{})
	m.SetAfterRetainHook(func(*runtimehost.Generation) {
		close(retained)
		<-resume
	})
	result := make(chan bool, 1)
	go func() {
		lease, ok := m.Acquire()
		if lease != nil {
			lease.Release()
		}
		result <- ok
	}()
	<-retained
	m.BeginShutdown()
	close(resume)
	if ok := <-result; ok {
		t.Fatal("Acquire crossing BeginShutdown must not return a lease")
	}
	if refs := g.Refs(); refs != 0 {
		t.Fatalf("refs=%d want 0 after rejected acquire", refs)
	}
	if detached := m.DetachActive(); detached != g {
		t.Fatalf("detached=%p want %p", detached, g)
	}
}

func TestManager_ShutdownDetached_PinnedTimeoutNoForceClose(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	closer := &countingCloser{}
	g := m.PrepareOwned("pinned", closer)
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	pin, ok := lease.TransferPin(runtimehost.PinAsync)
	if !ok {
		t.Fatal("pin")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := m.ShutdownDetached(ctx)
	if err == nil {
		t.Fatal("expected timeout error while pinned")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if closer.closes.Load() != 0 {
		t.Fatalf("force-closed pinned generation closes=%d", closer.closes.Load())
	}
	if !m.HasOpenGenerations() {
		t.Fatal("pinned generation must remain open")
	}
	pin.Release()
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if _, err := m.RetireGeneration(ctx2, g); err != nil && !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("retire after pin release: %v", err)
	}
	if closer.closes.Load() != 1 {
		t.Fatalf("closes=%d want 1", closer.closes.Load())
	}
	if m.HasOpenGenerations() {
		m.SweepClosed()
	}
	if m.HasOpenGenerations() {
		t.Fatal("expected no open generations after close+sweep")
	}
}

func TestManager_Publish_RejectsAfterBeginShutdown(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	mustPublish(t, m, m.Prepare("seed"))
	m.BeginShutdown()
	closer := &countingCloser{}
	cand := m.PrepareOwned("late", closer)
	err := m.Publish(cand)
	if !errors.Is(err, runtimehost.ErrHostShuttingDown) {
		t.Fatalf("err=%v", err)
	}
	if closer.closes.Load() != 1 {
		t.Fatalf("candidate closes=%d want 1", closer.closes.Load())
	}
	if cand.CloseCount() != 1 {
		t.Fatalf("closeCount=%d", cand.CloseCount())
	}
	if err := cand.Discard(); !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("second discard: %v", err)
	}
	if closer.closes.Load() != 1 {
		t.Fatalf("double-close closes=%d", closer.closes.Load())
	}
}

func TestManager_Publish_BeginShutdownRace_NoActiveLeak(t *testing.T) {
	t.Parallel()
	for round := range 40 {
		m := runtimehost.NewManager(8, nil)
		closer := &reentrantCloser{m: m}
		cand := m.PrepareOwned("race", closer)
		readyPub, readyShut, gate := make(chan struct{}), make(chan struct{}), make(chan struct{})
		var pubErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			close(readyPub)
			<-gate
			pubErr = m.Publish(cand)
		}()
		go func() {
			defer wg.Done()
			close(readyShut)
			<-gate
			m.BeginShutdown()
		}()
		<-readyPub
		<-readyShut
		close(gate)
		wg.Wait()

		if pubErr == nil {
			if m.Active() != cand {
				t.Fatalf("round %d: successful publish must leave candidate active", round)
			}
		} else if !errors.Is(pubErr, runtimehost.ErrHostShuttingDown) {
			t.Fatalf("round %d: unexpected publish err %v", round, pubErr)
		}
		_ = m.ShutdownDetached(context.Background())
		if m.Active() != nil {
			t.Fatalf("round %d: active must be nil after shutdown", round)
		}
		if _, ok := m.Acquire(); ok {
			t.Fatalf("round %d: acquire must fail after shutdown", round)
		}
		if closer.closes.Load() != 1 {
			t.Fatalf("round %d: candidate closes=%d want 1", round, closer.closes.Load())
		}
	}
}

func TestManager_Publish_RetentionBlocked_CleanupAfterUnlock(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(1, nil)
	mustPublish(t, m, m.Prepare("g1"))
	mustPublish(t, m, m.Prepare("g2"))
	closer := &reentrantCloser{m: m}
	cand := m.PrepareOwned("blocked", closer)
	err := m.Publish(cand)
	if !errors.Is(err, runtimehost.ErrRetentionBlocked) {
		t.Fatalf("err=%v", err)
	}
	if closer.closes.Load() != 1 {
		t.Fatalf("closes=%d", closer.closes.Load())
	}
}

func TestManager_ShutdownDetached_PinnedDoesNotBlockOtherGeneration(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	pinnedCloser := &countingCloser{}
	freeCloser := &countingCloser{}
	old := m.PrepareOwned("old-pinned", pinnedCloser)
	mustPublish(t, m, old)
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire old")
	}
	pin, ok := lease.TransferPin(runtimehost.PinSSE)
	if !ok {
		t.Fatal("pin old")
	}
	newG := m.PrepareOwned("new-free", freeCloser)
	mustPublish(t, m, newG)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	err := m.ShutdownDetached(ctx)
	if err == nil {
		t.Fatal("expected pin timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if freeCloser.closes.Load() != 1 {
		t.Fatalf("unrelated drained generation closes=%d want 1", freeCloser.closes.Load())
	}
	if pinnedCloser.closes.Load() != 0 {
		t.Fatalf("pinned closes=%d want 0", pinnedCloser.closes.Load())
	}
	pin.Release()
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if _, err := m.RetireGeneration(ctx2, old); err != nil && !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("retire pinned: %v", err)
	}
	if pinnedCloser.closes.Load() != 1 {
		t.Fatalf("pinned closes after release=%d want 1", pinnedCloser.closes.Load())
	}
}

func TestGenerationPin_TransferPin_RejectsInvalidKind(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(2, nil)
	g := m.Prepare("pin-kind")
	mustPublish(t, m, g)
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	if runtimehost.PinHTTP == 0 {
		t.Fatal("PinHTTP must not be the zero value")
	}
	if _, ok := lease.TransferPin(0); ok { // PinUnknown zero
		t.Fatal("zero kind must fail")
	}
	if _, ok := lease.TransferPin(runtimehost.PinKind(99)); ok {
		t.Fatal("unknown kind must fail")
	}
	if g.Refs() != 1 {
		t.Fatalf("refs after invalid=%d want 1 (lease retained)", g.Refs())
	}
	pin, ok := lease.TransferPin(runtimehost.PinProvider)
	if !ok {
		t.Fatal("valid transfer must succeed after invalid attempts")
	}
	if pin.Kind() != runtimehost.PinProvider {
		t.Fatalf("kind=%v", pin.Kind())
	}
	if g.Refs() != 1 {
		t.Fatalf("refs after transfer=%d", g.Refs())
	}
	pin.Release()
	if g.Refs() != 0 {
		t.Fatalf("refs after release=%d", g.Refs())
	}
}
