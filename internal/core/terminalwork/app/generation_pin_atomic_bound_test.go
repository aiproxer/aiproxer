package app_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	"github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/genpin"
)

func TestAtomicBound_DuplicatePublication_OneBinderHold(t *testing.T) {
	t.Parallel()
	pins := app.NewGenerationPinTracker()
	execGen := &snapshotgen.ExecutableGeneration{ID: 99, Version: "v1"}
	var bindCalls atomic.Int32
	var createdHolds atomic.Int32

	const n = 2
	var ready sync.WaitGroup
	start := make(chan struct{})
	var wg sync.WaitGroup
	pinsOut := make([]*countingPin, n)
	ready.Add(n)
	wg.Add(n)
	for i := range n {
		pinsOut[i] = &countingPin{}
		go func() {
			defer wg.Done()
			tok := pins.BeginAdoption("tw_atomic_dup")
			ready.Done()
			<-start
			ok := tok.PublishBound(pinsOut[i], func() (func(), bool) {
				bindCalls.Add(1)
				if !execGen.AddPendingWork("tw_atomic_dup", "quota") {
					return nil, false
				}
				createdHolds.Add(1)
				return func() { execGen.ClearPendingWork("tw_atomic_dup") }, true
			})
			_ = ok
			tok.End()
		}()
	}
	ready.Wait()
	close(start)
	wg.Wait()

	if bindCalls.Load() != 1 {
		t.Fatalf("binder invocations=%d want 1 (loser must not Bind)", bindCalls.Load())
	}
	if createdHolds.Load() != 1 || execGen.PendingWorkCount() != 1 {
		t.Fatalf("holds created=%d pending=%d want 1", createdHolds.Load(), execGen.PendingWorkCount())
	}
	if pins.Len() != 1 {
		t.Fatalf("tracker ownership=%d want 1", pins.Len())
	}
	var pinReleases int32
	for i := range n {
		pinReleases += pinsOut[i].releases.Load()
	}
	if pinReleases != 1 {
		t.Fatalf("exactly one loser pin cleanup; releases=%d", pinReleases)
	}

	pins.MarkTerminal("tw_atomic_dup")
	if pins.Len() != 0 || execGen.PendingWorkCount() != 0 {
		t.Fatalf("after terminal pins=%d pending=%d", pins.Len(), execGen.PendingWorkCount())
	}
	pinReleases = 0
	for i := range n {
		pinReleases += pinsOut[i].releases.Load()
	}
	if pinReleases != 2 {
		t.Fatalf("winner pin must clear on terminal; total releases=%d", pinReleases)
	}
}

func TestAtomicBound_TerminalWinsBeforeBoundPublication(t *testing.T) {
	t.Parallel()
	pins := app.NewGenerationPinTracker()
	pin := &countingPin{}
	var bindCalls atomic.Int32
	var cleared atomic.Int32
	tok := pins.BeginAdoption("tw_term_before_bound")
	pins.MarkTerminal("tw_term_before_bound")
	if tok.PublishBound(pin, func() (func(), bool) {
		bindCalls.Add(1)
		cleared.Add(1) // should never run
		return func() { cleared.Add(1) }, true
	}) {
		t.Fatal("PublishBound must reject after MarkTerminal")
	}
	tok.End()
	if bindCalls.Load() != 0 {
		t.Fatalf("binder must not run; calls=%d", bindCalls.Load())
	}
	if pin.releases.Load() != 1 {
		t.Fatalf("candidate pin releases=%d want 1", pin.releases.Load())
	}
	if cleared.Load() != 0 {
		t.Fatalf("no executable hold; clears=%d", cleared.Load())
	}
	if pins.Len() != 0 || pins.EntryCount() != 0 {
		t.Fatalf("pins=%d entries=%d", pins.Len(), pins.EntryCount())
	}
}

func TestAtomicBound_BoundPublicationWinsBeforeTerminal(t *testing.T) {
	t.Parallel()
	pins := app.NewGenerationPinTracker()
	pin := &countingPin{}
	var cleared atomic.Int32
	tok := pins.BeginAdoption("tw_bound_before_term")
	if !tok.PublishBound(pin, func() (func(), bool) {
		return func() { cleared.Add(1) }, true
	}) {
		t.Fatal("PublishBound should win")
	}
	if pins.Len() != 1 {
		t.Fatalf("combined ownership missing; pins=%d", pins.Len())
	}
	pins.MarkTerminal("tw_bound_before_term")
	tok.End()
	if pin.releases.Load() != 1 || cleared.Load() != 1 {
		t.Fatalf("terminal must clear both exactly once; pin=%d clear=%d", pin.releases.Load(), cleared.Load())
	}
	if pins.Len() != 0 || pins.EntryCount() != 0 {
		t.Fatalf("pins=%d entries=%d", pins.Len(), pins.EntryCount())
	}
}

func TestAtomicBound_MarkTerminalCannotInterleaveBindAndPublish(t *testing.T) {
	t.Parallel()
	pins := app.NewGenerationPinTracker()
	pin := &countingPin{}
	bound := make(chan struct{})
	proceed := make(chan struct{})
	tok := pins.BeginAdoption("tw_bind_serial")

	pubDone := make(chan bool, 1)
	go func() {
		ok := tok.PublishBound(pin, func() (func(), bool) {
			close(bound)
			<-proceed
			return func() {}, true
		})
		tok.End()
		pubDone <- ok
	}()

	select {
	case <-bound:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for binder under lock")
	}

	termDone := make(chan struct{})
	go func() {
		pins.MarkTerminal("tw_bind_serial")
		close(termDone)
	}()
	select {
	case <-termDone:
		t.Fatal("MarkTerminal must not complete while binder holds tracker mutex")
	case <-time.After(50 * time.Millisecond):
	}
	close(proceed)
	if !<-pubDone {
		t.Fatal("PublishBound should have published before MarkTerminal observed ownership")
	}
	select {
	case <-termDone:
	case <-time.After(5 * time.Second):
		t.Fatal("MarkTerminal did not finish after Bind")
	}
	if pin.releases.Load() != 1 {
		t.Fatalf("terminal must clear published pin; releases=%d", pin.releases.Load())
	}
	if pins.Len() != 0 {
		t.Fatalf("pins=%d want 0", pins.Len())
	}
}

func TestAtomicBound_LegacyNilPinExecutableOnly(t *testing.T) {
	t.Parallel()
	pins := app.NewGenerationPinTracker()
	execGen := &snapshotgen.ExecutableGeneration{ID: 3, Version: "v"}
	tok := pins.BeginAdoption("tw_exec_only")
	if !tok.PublishBound(nil, func() (func(), bool) {
		if !execGen.AddPendingWork("tw_exec_only", "quota") {
			return nil, false
		}
		return func() { execGen.ClearPendingWork("tw_exec_only") }, true
	}) {
		t.Fatal("executable-only ownership must publish")
	}
	tok.End()
	if pins.Len() != 1 || execGen.PendingWorkCount() != 1 {
		t.Fatalf("pins=%d pending=%d", pins.Len(), execGen.PendingWorkCount())
	}
	pins.MarkTerminal("tw_exec_only")
	if pins.Len() != 0 || execGen.PendingWorkCount() != 0 {
		t.Fatalf("clear pins=%d pending=%d", pins.Len(), execGen.PendingWorkCount())
	}
}

func TestAtomicBound_BinderPanicUnwindsWithoutPartialOwnership(t *testing.T) {
	t.Parallel()
	pins := app.NewGenerationPinTracker()
	pin := &countingPin{}
	tok := pins.BeginAdoption("tw_bind_panic")
	ok := tok.PublishBound(pin, func() (func(), bool) {
		panic("bind boom")
	})
	if ok {
		t.Fatal("panic must not publish")
	}
	tok.End()
	if pins.Len() != 0 || pin.releases.Load() != 1 {
		t.Fatalf("partial ownership forbidden; pins=%d releases=%d", pins.Len(), pin.releases.Load())
	}
}

func TestAtomicBound_NoTrackerReentryDeadlock(t *testing.T) {
	t.Parallel()
	pins := app.NewGenerationPinTracker()
	const rounds = 64
	var wg sync.WaitGroup
	wg.Add(rounds * 2)
	for i := range rounds {
		workID := "tw_reentry_" + itoa(i)
		go func(id string) {
			defer wg.Done()
			tok := pins.BeginAdoption(id)
			_ = tok.PublishBound(&countingPin{}, func() (func(), bool) {
				return func() {}, true
			})
			tok.End()
		}(workID)
		go func(id string) {
			defer wg.Done()
			pins.MarkTerminal(id)
		}(workID)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock: PublishBound/MarkTerminal did not finish")
	}
}

func TestAtomicBound_AmbiguousAppendDuplicate_NoSplitOwnership(t *testing.T) {
	t.Parallel()
	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "atomic-amb-dup"})
	if err != nil {
		t.Fatal(err)
	}
	pins := app.NewGenerationPinTracker()
	execGen := &snapshotgen.ExecutableGeneration{ID: 7, Version: "v"}
	var bindCalls atomic.Int32
	binder := countingExecutableBinder{
		inner: executableBinder{gen: execGen},
		calls: &bindCalls,
	}

	in := settleInput("req-atomic-amb", "a")
	svcSeed := app.NewIntentService(backing, app.IntentServiceConfig{
		Pins:              pins,
		ExecutablePending: binder,
	})
	pinSeed := &countingPin{}
	ctxSeed := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "amb", pin: pinSeed})
	if err := svcSeed.AcceptSettleFailure(ctxSeed, in); err != nil {
		t.Fatal(err)
	}
	workID := listOneWorkID(t, backing)
	rec, found, err := backing.LookupIntent(context.Background(), workID)
	if err != nil || !found {
		t.Fatalf("lookup seed: found=%v err=%v", found, err)
	}
	// Drop seed ownership so IntentService replay and reconciler adopt race cleanly.
	pins.AbortOwnership(workID)
	execGen.ClearPendingWork(workID)
	bindCalls.Store(0)

	store := &controllableAmbiguousStore{inner: backing}
	store.setAppend(func(context.Context, terminalwork.WorkRecord) (terminalwork.AppendIntentOutcome, error) {
		return terminalwork.AppendIntentOutcome{Replay: true}, nil
	})
	r := newTestReconciler(t, store, pins, app.AmbiguousAppendReconcilerConfig{
		Capacity:          8,
		ExecutablePending: binder,
	})
	svc := app.NewIntentService(backing, app.IntentServiceConfig{
		Pins:              pins,
		ExecutablePending: binder,
	})

	const n = 16
	var wg sync.WaitGroup
	pinsOut := make([]*countingPin, n+1)
	wg.Add(n + 1)
	pinsOut[0] = &countingPin{}
	go func() {
		defer wg.Done()
		_ = r.Take(context.Background(), app.AmbiguousAppend{
			WorkID: workID,
			Record: rec,
			Pin:    pinsOut[0],
		})
	}()
	for i := 1; i <= n; i++ {
		pinsOut[i] = &countingPin{}
		go func() {
			defer wg.Done()
			ctx := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "amb", pin: pinsOut[i]})
			_ = svc.AcceptSettleFailure(ctx, in)
		}()
	}
	wg.Wait()
	waitPending(t, r, 0)
	waitTrackerLen(t, pins, 1)
	if execGen.PendingWorkCount() != 1 {
		t.Fatalf("executable pending=%d want 1 (split ownership)", execGen.PendingWorkCount())
	}
	if bindCalls.Load() != 1 {
		t.Fatalf("binder calls=%d want 1", bindCalls.Load())
	}

	pins.MarkTerminal(workID)
	if pins.Len() != 0 || execGen.PendingWorkCount() != 0 {
		t.Fatalf("after terminal pins=%d pending=%d", pins.Len(), execGen.PendingWorkCount())
	}
}

func TestAtomicBound_IntentServiceUsesBoundPublication(t *testing.T) {
	t.Parallel()
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "atomic-intent"})
	if err != nil {
		t.Fatal(err)
	}
	pins := app.NewGenerationPinTracker()
	execGen := &snapshotgen.ExecutableGeneration{ID: 42, Version: "v1"}
	var bindCalls atomic.Int32
	svc := app.NewIntentService(store, app.IntentServiceConfig{
		Pins: pins,
		ExecutablePending: countingExecutableBinder{
			inner: executableBinder{gen: execGen},
			calls: &bindCalls,
		},
	})

	const n = 24
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			pin := &countingPin{}
			ctx := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "5", pin: pin})
			_ = svc.AcceptSettleFailure(ctx, app.SettleFailureInput{
				RequestID:  "req-atomic",
				AttemptID:  "a",
				ProviderID: "quota",
				Handles:    []string{"h"},
				Versions:   terminalwork.BoundVersions{GenerationID: "42", ProviderID: "quota"},
			})
		}()
	}
	wg.Wait()
	if pins.Len() != 1 || execGen.PendingWorkCount() != 1 {
		t.Fatalf("pins=%d pending=%d", pins.Len(), execGen.PendingWorkCount())
	}
	if bindCalls.Load() != 1 {
		t.Fatalf("only winner may Bind; calls=%d", bindCalls.Load())
	}
}
