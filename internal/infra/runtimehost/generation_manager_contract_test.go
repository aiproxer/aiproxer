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

// Task 1.4→3.1: deterministic generation-manager linearizability/retention under production APIs.
// Barriers/channels/fake clocks only — no timing sleeps.
func TestPublish_Acquire_LinearizablePointerRecheck(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, runtimehost.NewManualClock(time.Unix(1_700_000_000, 0)))
	g1 := m.Prepare("g1")
	mustPublish(t, m, g1)
	retained := make(chan struct{})
	resume := make(chan struct{})
	var once sync.Once
	m.SetAfterRetainHook(func(g *runtimehost.Generation) {
		if g.ID() != 1 {
			return
		}
		once.Do(func() { close(retained); <-resume })
	})
	result := make(chan int64, 1)
	go func() {
		lease, ok := m.Acquire()
		if !ok {
			result <- -1
			return
		}
		id := lease.Generation().ID()
		lease.Release()
		result <- id
	}()
	<-retained
	mustPublish(t, m, m.Prepare("g2"))
	// Auto-retirement may already have advanced past GenRetiring into
	// Quiescing/Quiesced while the acquire lease still pins g1. Closed/drain
	// states are impossible with refs>0.
	switch st := g1.Lifecycle(); st {
	case runtimehost.GenRetiring, runtimehost.GenQuiescing, runtimehost.GenQuiesced:
	default:
		t.Fatalf("g1 lifecycle=%v want retiring/quiescing/quiesced while lease held", st)
	}
	close(resume)
	if id := <-result; id != 2 {
		t.Fatalf("pointer recheck must bind g2, got %d", id)
	}
	m.SetAfterRetainHook(nil)
	_ = m.ClockNow()
	const workers = 32
	start := make(chan struct{})
	var sawOld, sawNew, failed atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			<-start
			lease, ok := m.Acquire()
			if !ok {
				failed.Add(1)
				return
			}
			id := lease.Generation().ID()
			lease.Release()
			switch id {
			case 2:
				sawOld.Add(1)
			case 3:
				sawNew.Add(1)
			default:
				failed.Add(1)
			}
		}()
	}
	pubErr := make(chan error, 1)
	go func() { <-start; pubErr <- m.Publish(m.Prepare("g3")) }()
	close(start)
	wg.Wait()
	if err := <-pubErr; err != nil {
		t.Fatal(err)
	}
	if failed.Load() != 0 || sawOld.Load()+sawNew.Load() != workers {
		t.Fatalf("race failed=%d old=%d new=%d", failed.Load(), sawOld.Load(), sawNew.Load())
	}
}

// TestAcquire_RetiringBitRefcountAndNoNewRetain uses BeginShutdown+
// DetachActive (not a replacing Publish) so Manager's automatic post-publish
// retirement scheduling (task 7.3) cannot advance g1 past GenDrained before
// this test observes that exact state.
func TestAcquire_RetiringBitRefcountAndNoNewRetain(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	g1 := m.Prepare("g1")
	mustPublish(t, m, g1)
	hold := make(chan struct{})
	started := make(chan struct{})
	var held atomic.Pointer[runtimehost.Lease]
	go func() {
		lease, ok := m.Acquire()
		if ok {
			held.Store(lease)
		}
		close(started)
		<-hold
		if lease != nil {
			lease.Release()
		}
	}()
	<-started
	if held.Load() == nil {
		t.Fatal("hold acquire failed")
	}
	l2, ok := m.Acquire()
	if !ok {
		t.Fatal("second acquire")
	}
	if g1.Refs() != 2 {
		t.Fatalf("refs=%d want 2", g1.Refs())
	}
	m.BeginShutdown()
	m.DetachActive()
	if g1.Lifecycle() != runtimehost.GenRetiring {
		t.Fatalf("lifecycle=%v", g1.Lifecycle())
	}
	if held.Load().Generation().ID() != 1 {
		t.Fatal("pre-publish lease must stay on g1")
	}
	late := make(chan struct{})
	var lateOnG1 atomic.Int64
	var wg sync.WaitGroup
	wg.Add(16)
	for range 16 {
		go func() {
			defer wg.Done()
			<-late
			lease, ok := m.Acquire()
			if !ok {
				return
			}
			if lease.Generation().ID() == 1 {
				lateOnG1.Add(1)
			}
			lease.Release()
		}()
	}
	close(late)
	wg.Wait()
	if lateOnG1.Load() != 0 {
		t.Fatalf("new acquires entered retiring g1: %d", lateOnG1.Load())
	}
	l2.Release()
	select {
	case <-g1.Drained():
		t.Fatal("must not drain while hold remains")
	default:
	}
	close(hold)
	<-g1.Drained()
	if g1.Lifecycle() != runtimehost.GenDrained || g1.Refs() != 0 {
		t.Fatalf("drain state=%v refs=%d", g1.Lifecycle(), g1.Refs())
	}
}

func TestRelease_ExactlyOnceDoubleCloseAndAsyncPinTransfer(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	g1 := m.Prepare("g1")
	mustPublish(t, m, g1)
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	lease.Release()
	lease.Release()
	if g1.Refs() != 0 {
		t.Fatalf("double-close underflow refs=%d", g1.Refs())
	}
	lease, ok = m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	pin, ok := lease.TransferPin(runtimehost.PinAsync)
	if !ok {
		t.Fatal("transfer")
	}
	lease.Release() // transferred; must not drop
	if g1.Refs() != 1 || pin.Kind() != runtimehost.PinAsync {
		t.Fatalf("pin refs=%d kind=%v", g1.Refs(), pin.Kind())
	}
	// BeginShutdown+DetachActive (not a replacing Publish) avoids racing
	// Manager's automatic post-publish retirement scheduling (task 7.3)
	// against this test's own manual BeginClose/Close drive below.
	m.BeginShutdown()
	m.DetachActive()
	select {
	case <-g1.Drained():
		t.Fatal("async pin must block drain")
	default:
	}
	pin.Release()
	pin.Release()
	<-g1.Drained()
	if err := g1.BeginClose(); err != nil {
		t.Fatal(err)
	}
	if err := g1.Close(); err != nil {
		t.Fatal(err)
	}
	if err := g1.Close(); !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("double close: %v", err)
	}
	if g1.CloseCount() != 1 || g1.Lifecycle() != runtimehost.GenClosed {
		t.Fatalf("close count=%d lifecycle=%v", g1.CloseCount(), g1.Lifecycle())
	}
}

func TestBlockedPins_AndRetentionBudgetRejectsWithoutKillingOldWork(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(1, runtimehost.NewManualClock(time.Unix(1_700_000_200, 0)))
	g1 := m.Prepare("g1")
	mustPublish(t, m, g1)
	kinds := []runtimehost.PinKind{runtimehost.PinSSE, runtimehost.PinAsync, runtimehost.PinProvider}
	pins := make([]*runtimehost.Pin, 0, len(kinds))
	for _, k := range kinds {
		lease, ok := m.Acquire()
		if !ok {
			t.Fatal("acquire")
		}
		pin, ok := lease.TransferPin(k)
		if !ok {
			t.Fatalf("transfer %v", k)
		}
		pins = append(pins, pin)
	}
	mustPublish(t, m, m.Prepare("g2"))
	if g1.Refs() != uint32(len(pins)) {
		t.Fatalf("refs=%d", g1.Refs())
	}
	active := m.Active()
	if err := m.Publish(m.Prepare("g3")); !errors.Is(err, runtimehost.ErrRetentionBlocked) {
		t.Fatalf("want retention blocked, got %v", err)
	}
	if m.Active() != active {
		t.Fatal("retention block must not swap active")
	}
	select {
	case <-g1.Drained():
		t.Fatal("must not kill old pinned work")
	default:
	}
	for i, p := range pins {
		p.Release()
		if i < len(pins)-1 {
			select {
			case <-g1.Drained():
				t.Fatal("drained early")
			default:
			}
		}
	}
	<-g1.Drained()
	// Manager's automatic post-publish retirement (task 7.3) may already be
	// racing to close g1 in the background; RetireGeneration serializes with
	// it via the generation's own admission and tolerates a benign
	// ErrAlreadyClosed if the background attempt already finished.
	if _, err := m.RetireGeneration(context.Background(), g1); err != nil && !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatal(err)
	}
	m.SweepClosed()
	mustPublish(t, m, m.Prepare("g3"))
	if m.Active().Label() != "g3" {
		t.Fatalf("active=%q", m.Active().Label())
	}
}

func TestPublish_RetentionBlocked_RollsBackPrepareOwnedCandidate(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(1, nil)
	g1 := m.Prepare("g1")
	mustPublish(t, m, g1)
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	pin, ok := lease.TransferPin(runtimehost.PinAsync)
	if !ok {
		t.Fatal("transfer")
	}
	mustPublish(t, m, m.Prepare("g2"))
	active := m.Active()
	retainedBefore := m.RetainedCount()

	var candidateCloses atomic.Int32
	cand := m.PrepareOwned("blocked", &stubOwned{closeFn: func() error {
		candidateCloses.Add(1)
		return nil
	}})
	err := m.Publish(cand)
	if !errors.Is(err, runtimehost.ErrRetentionBlocked) {
		t.Fatalf("want retention blocked, got %v", err)
	}
	if candidateCloses.Load() != 1 {
		t.Fatalf("candidate owned closes=%d want 1", candidateCloses.Load())
	}
	if cand.Lifecycle() != runtimehost.GenFailed || cand.CloseCount() != 1 {
		t.Fatalf("candidate lifecycle=%v count=%d", cand.Lifecycle(), cand.CloseCount())
	}
	if m.Active() != active {
		t.Fatal("active pointer must be unchanged")
	}
	if m.RetainedCount() != retainedBefore {
		t.Fatalf("retained=%d want %d", m.RetainedCount(), retainedBefore)
	}
	if g1.Refs() != 1 {
		t.Fatalf("old pin refs=%d", g1.Refs())
	}
	select {
	case <-g1.Drained():
		t.Fatal("old pin must survive retention rejection")
	default:
	}
	if err := m.Publish(cand); !errors.Is(err, runtimehost.ErrNotPrepared) {
		t.Fatalf("rolled-back candidate must not publish: %v", err)
	}
	if err := cand.Discard(); !errors.Is(err, runtimehost.ErrAlreadyClosed) {
		t.Fatalf("second discard: %v", err)
	}
	if candidateCloses.Load() != 1 {
		t.Fatalf("double-close closes=%d", candidateCloses.Load())
	}
	pin.Release()
}

func TestPublish_RetentionBlocked_SurfacesRollbackCloseError(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(1, nil)
	mustPublish(t, m, m.Prepare("g1"))
	mustPublish(t, m, m.Prepare("g2"))
	closeErr := errors.New("owned close failed")
	cand := m.PrepareOwned("boom", &stubOwned{closeFn: func() error { return closeErr }})
	err := m.Publish(cand)
	if !errors.Is(err, runtimehost.ErrRetentionBlocked) {
		t.Fatalf("must preserve ErrRetentionBlocked: %v", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("must surface rollback close error: %v", err)
	}
	if cand.Lifecycle() != runtimehost.GenFailed || cand.CloseCount() != 1 {
		t.Fatalf("lifecycle=%v count=%d", cand.Lifecycle(), cand.CloseCount())
	}
}

func TestPublish_AcquireRace_BarrierRounds(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(8, nil)
	mustPublish(t, m, m.Prepare("seed"))
	for round := range 40 {
		readyAcq, readyPub, gate := make(chan struct{}), make(chan struct{}), make(chan struct{})
		result := make(chan int64, 1)
		go func() {
			close(readyAcq)
			<-gate
			lease, ok := m.Acquire()
			if !ok {
				result <- -1
				return
			}
			id := lease.Generation().ID()
			lease.Release()
			result <- id
		}()
		cand := m.Prepare("race")
		go func() {
			close(readyPub)
			<-gate
			_ = m.Publish(cand)
		}()
		<-readyAcq
		<-readyPub
		close(gate)
		id := <-result
		if id < 1 || m.Active() == nil || m.Active().ID() < id {
			t.Fatalf("round %d id=%d active=%v", round, id, m.Active())
		}
	}
}

func TestProductionGenerationManager_IntegrationRED(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(2, runtimehost.NewManualClock(time.Unix(1_700_000_400, 0)))
	var ownedCloses atomic.Int32
	g1 := m.PrepareOwned("prod-1", &stubOwned{closeFn: func() error {
		ownedCloses.Add(1)
		return nil
	}})
	mustPublish(t, m, g1)
	lease, ok := m.Acquire()
	if !ok || lease.Generation().ID() != 1 {
		t.Fatal("acquire active")
	}
	pin, ok := lease.TransferPin(runtimehost.PinProvider)
	if !ok {
		t.Fatal("transfer")
	}
	mustPublish(t, m, m.Prepare("prod-2"))
	lease2, ok := m.Acquire()
	if !ok {
		t.Fatal("new work on active")
	}
	lease2.Release()
	// Drain blocked by pin; retention still holds g1.
	select {
	case <-g1.Drained():
		t.Fatal("pin must block")
	default:
	}
	pin.Release()
	<-g1.Drained()
	_ = g1.BeginClose()
	_ = g1.Close()
	if ownedCloses.Load() != 1 {
		t.Fatalf("owned closes=%d", ownedCloses.Load())
	}
	m.SweepClosed()
	mustPublish(t, m, m.Prepare("prod-3"))
}

func mustPublish(t *testing.T, m *runtimehost.Manager, g *runtimehost.Generation) {
	t.Helper()
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}
}
