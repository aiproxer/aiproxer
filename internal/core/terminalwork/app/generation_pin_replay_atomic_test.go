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
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func settleInput(req, attempt string) app.SettleFailureInput {
	return app.SettleFailureInput{
		RequestID:  req,
		AttemptID:  attempt,
		ProviderID: "quota",
		Handles:    []string{"h"},
		Versions:   terminalwork.BoundVersions{GenerationID: "11", ProviderID: "quota"},
	}
}

func forceComplete(t *testing.T, store *workstore.MemoryStore, pins *app.GenerationPinTracker, workID, owner string) {
	t.Helper()
	claimed, err := store.ClaimDue(context.Background(), terminalwork.ClaimDueCommand{
		OwnerID: owner,
		TTL:     time.Minute,
		Limit:   8,
		Now:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rec := range claimed {
		if rec.WorkID != workID {
			continue
		}
		found = true
		if err := store.Complete(context.Background(), terminalwork.CompleteCommand{
			WorkID:          workID,
			ExpectedOwnerID: owner,
			Now:             time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
		if pins != nil {
			pins.MarkTerminal(workID)
		}
	}
	if !found {
		t.Fatalf("work %s not claimed", workID)
	}
}

func forceQuarantine(t *testing.T, store *workstore.MemoryStore, pins *app.GenerationPinTracker, workID string) {
	t.Helper()
	if err := store.Quarantine(context.Background(), terminalwork.QuarantineCommand{
		WorkID: workID,
		Now:    time.Now().UTC(),
		Err:    terminalwork.BoundedError{Code: "bad", Message: "quarantined", Permanent: true},
	}); err != nil {
		t.Fatal(err)
	}
	if pins != nil {
		pins.MarkTerminal(workID)
	}
}

func listOneWorkID(t *testing.T, store *workstore.MemoryStore) string {
	t.Helper()
	page, err := store.List(context.Background(), workstore.Query{
		ProviderID: "quota",
		Limit:      10,
	})
	if err != nil || len(page.Records) == 0 {
		t.Fatalf("list: %v len=%d", err, len(page.Records))
	}
	return page.Records[0].WorkID
}

func TestGenerationPin_Replay_CompletedReleasesCandidate(t *testing.T) {
	t.Parallel()
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "replay-completed"})
	if err != nil {
		t.Fatal(err)
	}
	pins := app.NewGenerationPinTracker()
	pin1 := &countingPin{}
	ctx1 := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "1", pin: pin1})
	svc := app.NewIntentService(store, app.IntentServiceConfig{Pins: pins})
	in := settleInput("req-done", "a")
	if err := svc.AcceptSettleFailure(ctx1, in); err != nil {
		t.Fatal(err)
	}
	workID := listOneWorkID(t, store)
	forceComplete(t, store, pins, workID, "owner-done")
	if pins.Len() != 0 || pin1.releases.Load() != 1 {
		t.Fatalf("after complete pins=%d releases=%d", pins.Len(), pin1.releases.Load())
	}

	pin2 := &countingPin{}
	ctx2 := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "1", pin: pin2})
	if err := svc.AcceptSettleFailure(ctx2, in); err != nil {
		t.Fatalf("terminal replay must be idempotent: %v", err)
	}
	if pins.Len() != 0 {
		t.Fatalf("tracker entry leaked: %d", pins.Len())
	}
	if pin2.releases.Load() != 1 {
		t.Fatalf("candidate releases=%d want 1", pin2.releases.Load())
	}
}

func TestGenerationPin_Replay_QuarantinedReleasesCandidate(t *testing.T) {
	t.Parallel()
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "replay-quar"})
	if err != nil {
		t.Fatal(err)
	}
	pins := app.NewGenerationPinTracker()
	pin1 := &countingPin{}
	ctx1 := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "2", pin: pin1})
	svc := app.NewIntentService(store, app.IntentServiceConfig{Pins: pins})
	in := settleInput("req-quar", "a")
	if err := svc.AcceptSettleFailure(ctx1, in); err != nil {
		t.Fatal(err)
	}
	workID := listOneWorkID(t, store)
	forceQuarantine(t, store, pins, workID)
	if pins.Len() != 0 {
		t.Fatalf("pins=%d", pins.Len())
	}

	pin2 := &countingPin{}
	ctx2 := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "2", pin: pin2})
	if err := svc.AcceptSettleFailure(ctx2, in); err != nil {
		t.Fatalf("quarantined replay: %v", err)
	}
	if pins.Len() != 0 || pin2.releases.Load() != 1 {
		t.Fatalf("pins=%d candidate releases=%d", pins.Len(), pin2.releases.Load())
	}
}

// terminalBarrierStore forces the adopt-then-reconcile race: first replay lookup
// returns a stale pending view, then waits until the test completes+releases the
// original pin before promote/reconcile observes the truthful terminal row.
type terminalBarrierStore struct {
	inner *workstore.MemoryStore

	replay  atomic.Bool
	lookups atomic.Int32

	afterStaleLookup chan struct{}
	afterTerminal    chan struct{}
}

func (s *terminalBarrierStore) AppendIntent(ctx context.Context, rec terminalwork.WorkRecord) error {
	return s.inner.AppendIntent(ctx, rec)
}

func (s *terminalBarrierStore) PromotePending(ctx context.Context, cmd terminalwork.PromotePendingCommand) error {
	return s.inner.PromotePending(ctx, cmd)
}

func (s *terminalBarrierStore) LookupIntent(ctx context.Context, workID string) (terminalwork.WorkRecord, bool, error) {
	if !s.replay.Load() {
		return s.inner.LookupIntent(ctx, workID)
	}
	n := s.lookups.Add(1)
	if n == 1 {
		rec, found, err := s.inner.LookupIntent(ctx, workID)
		if err != nil || !found {
			return rec, found, err
		}
		rec.State = sdk.WorkStatePending
		select {
		case s.afterStaleLookup <- struct{}{}:
		default:
		}
		<-s.afterTerminal
		return rec, true, nil
	}
	return s.inner.LookupIntent(ctx, workID)
}

func TestGenerationPin_Replay_BarrierTerminalBeforeAdopt(t *testing.T) {
	t.Parallel()
	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "replay-barrier"})
	if err != nil {
		t.Fatal(err)
	}
	store := &terminalBarrierStore{
		inner:            backing,
		afterStaleLookup: make(chan struct{}, 1),
		afterTerminal:    make(chan struct{}),
	}
	pins := app.NewGenerationPinTracker()
	pin1 := &countingPin{}
	svc := app.NewIntentService(store, app.IntentServiceConfig{Pins: pins})
	in := settleInput("req-barrier", "a")
	ctx1 := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "3", pin: pin1})
	if err := svc.AcceptSettleFailure(ctx1, in); err != nil {
		t.Fatal(err)
	}
	workID := listOneWorkID(t, backing)

	store.replay.Store(true)
	pin2 := &countingPin{}
	ctx2 := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "3", pin: pin2})

	var acceptErr error
	var wg sync.WaitGroup
	wg.Go(func() {
		acceptErr = svc.AcceptSettleFailure(ctx2, in)
	})

	<-store.afterStaleLookup
	forceComplete(t, backing, pins, workID, "owner-barrier")
	close(store.afterTerminal)
	wg.Wait()

	if acceptErr != nil {
		t.Fatalf("replay after barrier: %v", acceptErr)
	}
	if pins.Len() != 0 {
		t.Fatalf("pin leaked after barrier race: %d", pins.Len())
	}
	if pin2.releases.Load() != 1 {
		t.Fatalf("candidate releases=%d want 1", pin2.releases.Load())
	}
}

func TestGenerationPin_Replay_PromoteFailNonterminalRetains(t *testing.T) {
	t.Parallel()
	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "replay-promote-nt"})
	if err != nil {
		t.Fatal(err)
	}
	pins := app.NewGenerationPinTracker()
	pin := &countingPin{}
	ctx := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "4", pin: pin})
	store := &promoteFailStore{MemoryStore: backing}
	svc := app.NewIntentService(store, app.IntentServiceConfig{Pins: pins})
	err = svc.AcceptSettleFailure(ctx, settleInput("req-nt", "a"))
	if err == nil {
		t.Fatal("expected promote failure")
	}
	if pins.Len() != 1 || pin.releases.Load() != 0 {
		t.Fatalf("nonterminal promote fail must retain; pins=%d releases=%d", pins.Len(), pin.releases.Load())
	}
}

func TestGenerationPin_Replay_AppendErrorTerminalSameIntent(t *testing.T) {
	t.Parallel()
	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "replay-append-term"})
	if err != nil {
		t.Fatal(err)
	}
	pins := app.NewGenerationPinTracker()
	in := settleInput("req-aeterm", "a")
	svcSeed := app.NewIntentService(backing, app.IntentServiceConfig{Pins: pins})
	pin1 := &countingPin{}
	ctx1 := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "5", pin: pin1})
	if err := svcSeed.AcceptSettleFailure(ctx1, in); err != nil {
		t.Fatal(err)
	}
	workID := listOneWorkID(t, backing)
	forceComplete(t, backing, pins, workID, "owner-aeterm")

	store := &commitThenErrorStore{inner: backing}
	svc := app.NewIntentService(store, app.IntentServiceConfig{Pins: pins})
	pin2 := &countingPin{}
	ctx2 := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "5", pin: pin2})
	if err := svc.AcceptSettleFailure(ctx2, in); err != nil {
		t.Fatalf("append-error terminal reconcile: %v", err)
	}
	if pins.Len() != 0 || pin2.releases.Load() != 1 {
		t.Fatalf("pins=%d releases=%d", pins.Len(), pin2.releases.Load())
	}
}

func TestGenerationPin_AtomicAdoption_MarkTerminalDuringBind(t *testing.T) {
	t.Parallel()
	// With PublishBound, Bind runs under the tracker mutex. External barriers inside
	// Bind deadlock MarkTerminal; terminal-before-bound-publication is forced via
	// a lookup barrier before PublishBound instead.
	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "atomic-rel"})
	if err != nil {
		t.Fatal(err)
	}
	afterStaleLookup := make(chan struct{})
	afterTerminal := make(chan struct{})
	store := &reviewerInterleaveStore{
		inner:            backing,
		afterStaleLookup: afterStaleLookup,
		afterTerminal:    afterTerminal,
	}
	pins := app.NewGenerationPinTracker()
	execGen := &snapshotgen.ExecutableGeneration{ID: 11, Version: "v1"}

	pin1 := &countingPin{}
	svcSeed := app.NewIntentService(store, app.IntentServiceConfig{
		Pins:              pins,
		ExecutablePending: executableBinder{gen: execGen},
	})
	in := settleInput("req-atomic", "a")
	ctx1 := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "6", pin: pin1})
	if err := svcSeed.AcceptSettleFailure(ctx1, in); err != nil {
		t.Fatal(err)
	}
	workID := listOneWorkID(t, backing)
	if pins.Len() != 1 || execGen.PendingWorkCount() != 1 {
		t.Fatalf("seed pins=%d pending=%d", pins.Len(), execGen.PendingWorkCount())
	}

	var bindCalls atomic.Int32
	svc := app.NewIntentService(store, app.IntentServiceConfig{
		Pins: pins,
		ExecutablePending: countingExecutableBinder{
			inner: executableBinder{gen: execGen},
			calls: &bindCalls,
		},
	})
	store.replay.Store(true)
	pin2 := &countingPin{}
	ctx2 := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "6", pin: pin2})

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.AcceptSettleFailure(ctx2, in)
	}()
	select {
	case <-afterStaleLookup:
	case err := <-errCh:
		t.Fatalf("accept finished before stale lookup: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for stale lookup")
	}
	forceComplete(t, backing, pins, workID, "owner-atomic")
	close(afterTerminal)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if bindCalls.Load() != 0 {
		t.Fatalf("PublishBound must not Bind after terminal; calls=%d", bindCalls.Load())
	}
	if pins.Len() != 0 || pins.EntryCount() != 0 {
		t.Fatalf("tracker leaked; pins=%d entries=%d", pins.Len(), pins.EntryCount())
	}
	if execGen.PendingWorkCount() != 0 || pin2.releases.Load() != 1 {
		t.Fatalf("after drain pending=%d releases=%d", execGen.PendingWorkCount(), pin2.releases.Load())
	}
}

func TestGenerationPin_AtomicAdoption_LegacyExecutableOnly(t *testing.T) {
	t.Parallel()
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "legacy-exec"})
	if err != nil {
		t.Fatal(err)
	}
	pins := app.NewGenerationPinTracker()
	execGen := &snapshotgen.ExecutableGeneration{ID: 11, Version: "v1"}
	svc := app.NewIntentService(store, app.IntentServiceConfig{
		Pins:              pins,
		ExecutablePending: executableBinder{gen: execGen},
	})
	// No retainer => nil runtime pin; executable-only tracker entry.
	if err := svc.AcceptSettleFailure(context.Background(), settleInput("req-legacy", "a")); err != nil {
		t.Fatal(err)
	}
	if pins.Len() != 1 {
		t.Fatalf("expected executable-only entry; pins=%d", pins.Len())
	}
	if execGen.PendingWorkCount() != 1 {
		t.Fatalf("pending=%d", execGen.PendingWorkCount())
	}
	workID := listOneWorkID(t, store)
	pins.MarkTerminal(workID)
	if pins.Len() != 0 || execGen.PendingWorkCount() != 0 {
		t.Fatalf("terminal clear failed; pins=%d pending=%d", pins.Len(), execGen.PendingWorkCount())
	}
}

func TestGenerationPin_AtomicAdoption_DuplicateLoserNoClearWinner(t *testing.T) {
	t.Parallel()
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "dup-loser"})
	if err != nil {
		t.Fatal(err)
	}
	pins := app.NewGenerationPinTracker()
	execGen := &snapshotgen.ExecutableGeneration{ID: 11, Version: "v1"}
	svc := app.NewIntentService(store, app.IntentServiceConfig{
		Pins:              pins,
		ExecutablePending: executableBinder{gen: execGen},
	})
	pinWin := &countingPin{}
	ctxWin := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "7", pin: pinWin})
	in := settleInput("req-dup", "a")
	if err := svc.AcceptSettleFailure(ctxWin, in); err != nil {
		t.Fatal(err)
	}
	if pins.Len() != 1 || execGen.PendingWorkCount() != 1 {
		t.Fatalf("winner pins=%d pending=%d", pins.Len(), execGen.PendingWorkCount())
	}

	pinLose := &countingPin{}
	ctxLose := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "7", pin: pinLose})
	if err := svc.AcceptSettleFailure(ctxLose, in); err != nil {
		t.Fatal(err)
	}
	if pinLose.releases.Load() != 1 {
		t.Fatalf("loser pin releases=%d", pinLose.releases.Load())
	}
	if pinWin.releases.Load() != 0 {
		t.Fatalf("winner pin must remain; releases=%d", pinWin.releases.Load())
	}
	if pins.Len() != 1 || execGen.PendingWorkCount() != 1 {
		t.Fatalf("winner hold must remain; pins=%d pending=%d", pins.Len(), execGen.PendingWorkCount())
	}
}

func TestGenerationPin_AtomicAdoption_BinderFalseNoClear(t *testing.T) {
	t.Parallel()
	pins := app.NewGenerationPinTracker()
	execGen := &snapshotgen.ExecutableGeneration{ID: 11, Version: "v1"}
	workID := "tw_winner"
	if !execGen.AddPendingWork(workID, "quota") {
		t.Fatal("seed pending")
	}
	binder := executableBinder{gen: execGen}
	release, ok := binder.Bind(workID, terminalwork.BoundVersions{GenerationID: "11", ProviderID: "quota"})
	if ok || release != nil {
		t.Fatalf("duplicate AddPendingWork must not return release; ok=%v release=%v", ok, release != nil)
	}
	// Winner clear still works.
	pins.Hold(workID, &countingPin{}, func() { execGen.ClearPendingWork(workID) })
	pins.MarkTerminal(workID)
	if execGen.PendingWorkCount() != 0 {
		t.Fatalf("pending=%d", execGen.PendingWorkCount())
	}
	if pins.Len() != 0 {
		t.Fatalf("pins=%d", pins.Len())
	}
}

func TestGenerationPinTracker_ReleasePanicIsolatesCleanups(t *testing.T) {
	t.Parallel()
	pins := app.NewGenerationPinTracker()
	var pinReleases atomic.Int32
	var execClears atomic.Int32
	pin := panicPin{releases: &pinReleases}
	if !pins.Hold("tw_panic", pin, func() {
		execClears.Add(1)
		panic("clear boom")
	}) {
		t.Fatal("hold failed")
	}
	pins.Release("tw_panic")
	if pinReleases.Load() != 1 || execClears.Load() != 1 {
		t.Fatalf("pin releases=%d exec clears=%d", pinReleases.Load(), execClears.Load())
	}
	if pins.Len() != 0 {
		t.Fatalf("pins=%d", pins.Len())
	}

	// Reverse panic order: pin panics, exec still runs.
	pins2 := app.NewGenerationPinTracker()
	var pinReleases2 atomic.Int32
	var execClears2 atomic.Int32
	if !pins2.Hold("tw_panic2", panicPin{releases: &pinReleases2, boom: true}, func() {
		execClears2.Add(1)
	}) {
		t.Fatal("hold failed")
	}
	pins2.MarkTerminal("tw_panic2")
	if pinReleases2.Load() != 1 || execClears2.Load() != 1 {
		t.Fatalf("pin releases=%d exec clears=%d", pinReleases2.Load(), execClears2.Load())
	}
}

func TestGenerationPin_AtomicAdoption_ConcurrentAcceptRace(t *testing.T) {
	t.Parallel()
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "atomic-race100"})
	if err != nil {
		t.Fatal(err)
	}
	pins := app.NewGenerationPinTracker()
	execGen := &snapshotgen.ExecutableGeneration{ID: 11, Version: "v1"}
	svc := app.NewIntentService(store, app.IntentServiceConfig{
		Pins:              pins,
		ExecutablePending: executableBinder{gen: execGen},
	})
	in := settleInput("req-race100", "a")
	const n = 32
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			pin := &countingPin{}
			ctx := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "8", pin: pin})
			_ = svc.AcceptSettleFailure(ctx, in)
		}()
	}
	wg.Wait()
	if pins.Len() != 1 || execGen.PendingWorkCount() != 1 {
		t.Fatalf("pins=%d pending=%d", pins.Len(), execGen.PendingWorkCount())
	}
	workID := listOneWorkID(t, store)
	pins.MarkTerminal(workID)
	if pins.Len() != 0 || execGen.PendingWorkCount() != 0 {
		t.Fatalf("after release pins=%d pending=%d", pins.Len(), execGen.PendingWorkCount())
	}
}

type panicPin struct {
	releases *atomic.Int32
	boom     bool
}

func (p panicPin) Kind() genpin.Kind { return genpin.KindProvider }
func (p panicPin) Release() {
	p.releases.Add(1)
	if p.boom {
		panic("pin boom")
	}
}
