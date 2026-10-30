package app_test

import (
	"context"
	"errors"
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

// reviewerInterleaveStore encodes terminal winning before bound publication:
// forced Replay outcome, lookup returns stale pending, then MarkTerminal runs
// before PublishBound. Binder must not run; candidate pin releases; no late hold.
type reviewerInterleaveStore struct {
	inner *workstore.MemoryStore

	replay       atomic.Bool
	lookups      atomic.Int32
	promoteCalls atomic.Int32

	afterStaleLookup chan struct{}
	afterTerminal    chan struct{}
	lookupFailAfter  atomic.Bool
}

func (s *reviewerInterleaveStore) AppendIntent(ctx context.Context, rec terminalwork.WorkRecord) error {
	_, err := s.AppendIntentOutcome(ctx, rec)
	return err
}

func (s *reviewerInterleaveStore) AppendIntentOutcome(ctx context.Context, rec terminalwork.WorkRecord) (terminalwork.AppendIntentOutcome, error) {
	if s.replay.Load() {
		// Definitive replay without depending on store idempotency timing.
		return terminalwork.AppendIntentOutcome{Replay: true}, nil
	}
	return s.inner.AppendIntentOutcome(ctx, rec)
}

func (s *reviewerInterleaveStore) PromotePending(ctx context.Context, cmd terminalwork.PromotePendingCommand) error {
	if s.lookupFailAfter.Load() {
		s.promoteCalls.Add(1)
		return errors.New("promote conflict after terminal")
	}
	return s.inner.PromotePending(ctx, cmd)
}

func (s *reviewerInterleaveStore) LookupIntent(ctx context.Context, workID string) (terminalwork.WorkRecord, bool, error) {
	// Fail only after a replay-time promote attempt (not the seed promote).
	if s.lookupFailAfter.Load() && s.promoteCalls.Load() > 0 {
		return terminalwork.WorkRecord{}, false, errors.New("transient lookup failure")
	}
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
		s.afterStaleLookup <- struct{}{}
		<-s.afterTerminal
		return rec, true, nil
	}
	return s.inner.LookupIntent(ctx, workID)
}

func TestAdoption_ReviewerInterleave_PublishRejectsAfterMarkTerminal(t *testing.T) {
	t.Parallel()
	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "reviewer-interleave"})
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
	in := settleInput("req-reviewer", "a")
	ctx1 := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "r1", pin: pin1})
	if err := svcSeed.AcceptSettleFailure(ctx1, in); err != nil {
		t.Fatal(err)
	}
	workID := listOneWorkID(t, backing)
	if pins.Len() != 1 || execGen.PendingWorkCount() != 1 {
		t.Fatalf("seed pins=%d pending=%d", pins.Len(), execGen.PendingWorkCount())
	}

	var bindCalls atomic.Int32
	binder := countingExecutableBinder{
		inner: executableBinder{gen: execGen},
		calls: &bindCalls,
	}
	svc := app.NewIntentService(store, app.IntentServiceConfig{
		Pins:              pins,
		ExecutablePending: binder,
	})

	store.replay.Store(true)
	store.lookupFailAfter.Store(true)
	pin2 := &countingPin{}
	ctx2 := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "r1", pin: pin2})

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
	forceComplete(t, backing, pins, workID, "owner-reviewer")
	close(afterTerminal)
	acceptErr := <-errCh

	_ = acceptErr // promote may fail due to forced conflict; ownership must not leak
	if bindCalls.Load() != 0 {
		t.Fatalf("PublishBound must not Bind after terminal; calls=%d", bindCalls.Load())
	}
	if pins.Len() != 0 || pins.EntryCount() != 0 {
		t.Fatalf("late hold survived; pins=%d entries=%d err=%v", pins.Len(), pins.EntryCount(), acceptErr)
	}
	if pin2.releases.Load() != 1 {
		t.Fatalf("candidate releases=%d want 1", pin2.releases.Load())
	}
	if execGen.PendingWorkCount() != 0 {
		t.Fatalf("executable pending=%d want 0", execGen.PendingWorkCount())
	}
}

type countingExecutableBinder struct {
	inner app.ExecutablePendingBinder
	calls *atomic.Int32
}

func (b countingExecutableBinder) Bind(workID string, versions terminalwork.BoundVersions) (func(), bool) {
	b.calls.Add(1)
	return b.inner.Bind(workID, versions)
}

func TestAdoption_TerminalWinsBeforePublish(t *testing.T) {
	t.Parallel()
	pins := app.NewGenerationPinTracker()
	pin := &countingPin{}
	var cleared atomic.Int32
	tok := pins.BeginAdoption("tw_term_first")
	pins.MarkTerminal("tw_term_first")
	if tok.Publish(pin, func() { cleared.Add(1) }) {
		t.Fatal("Publish must reject after MarkTerminal")
	}
	tok.End()
	if pin.releases.Load() != 1 || cleared.Load() != 1 {
		t.Fatalf("unwind releases=%d clear=%d", pin.releases.Load(), cleared.Load())
	}
	if pins.Len() != 0 || pins.EntryCount() != 0 {
		t.Fatalf("pins=%d entries=%d", pins.Len(), pins.EntryCount())
	}
}

func TestAdoption_PublishWinsBeforeTerminal(t *testing.T) {
	t.Parallel()
	pins := app.NewGenerationPinTracker()
	pin := &countingPin{}
	var cleared atomic.Int32
	tok := pins.BeginAdoption("tw_pub_first")
	if !tok.Publish(pin, func() { cleared.Add(1) }) {
		t.Fatal("Publish should win")
	}
	if pins.Len() != 1 {
		t.Fatalf("pins=%d", pins.Len())
	}
	pins.MarkTerminal("tw_pub_first")
	tok.End()
	if pin.releases.Load() != 1 || cleared.Load() != 1 {
		t.Fatalf("terminal release pin=%d clear=%d", pin.releases.Load(), cleared.Load())
	}
	if pins.Len() != 0 || pins.EntryCount() != 0 {
		t.Fatalf("pins=%d entries=%d", pins.Len(), pins.EntryCount())
	}
}

func TestAdoption_TwoConcurrentReplayReservationsPlusTerminal(t *testing.T) {
	t.Parallel()
	pins := app.NewGenerationPinTracker()
	var wg sync.WaitGroup
	var ready sync.WaitGroup
	start := make(chan struct{})
	const n = 2
	pinsOut := make([]*countingPin, n)
	clears := make([]atomic.Int32, n)
	wg.Add(n)
	ready.Add(n)
	for i := range n {
		pinsOut[i] = &countingPin{}
		go func() {
			defer wg.Done()
			tok := pins.BeginAdoption("tw_two")
			ready.Done()
			<-start
			_ = tok.Publish(pinsOut[i], func() { clears[i].Add(1) })
			tok.End()
		}()
	}
	ready.Wait()
	pins.MarkTerminal("tw_two")
	close(start)
	wg.Wait()
	if pins.Len() != 0 || pins.EntryCount() != 0 {
		t.Fatalf("pins=%d entries=%d", pins.Len(), pins.EntryCount())
	}
	var pinReleases, clearCount int32
	for i := range n {
		pinReleases += pinsOut[i].releases.Load()
		clearCount += clears[i].Load()
	}
	if pinReleases != 2 || clearCount != 2 {
		t.Fatalf("both candidates must unwind; pin=%d clear=%d", pinReleases, clearCount)
	}
}

func TestAdoption_InsertedOutcomeAdopts(t *testing.T) {
	t.Parallel()
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "inserted-adopt"})
	if err != nil {
		t.Fatal(err)
	}
	pins := app.NewGenerationPinTracker()
	execGen := &snapshotgen.ExecutableGeneration{ID: 11, Version: "v1"}
	pin := &countingPin{}
	ctx := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "ins", pin: pin})
	svc := app.NewIntentService(store, app.IntentServiceConfig{
		Pins:              pins,
		ExecutablePending: executableBinder{gen: execGen},
	})
	if err := svc.AcceptSettleFailure(ctx, settleInput("req-ins", "a")); err != nil {
		t.Fatal(err)
	}
	if pins.Len() != 1 || execGen.PendingWorkCount() != 1 || pin.releases.Load() != 0 {
		t.Fatalf("inserted must adopt; pins=%d pending=%d releases=%d", pins.Len(), execGen.PendingWorkCount(), pin.releases.Load())
	}
}

func TestAdoption_ReplayTerminalDoesNotAdopt(t *testing.T) {
	t.Parallel()
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "replay-term-noadopt"})
	if err != nil {
		t.Fatal(err)
	}
	pins := app.NewGenerationPinTracker()
	svc := app.NewIntentService(store, app.IntentServiceConfig{Pins: pins})
	in := settleInput("req-rt", "a")
	pin1 := &countingPin{}
	ctx1 := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "rt", pin: pin1})
	if err := svc.AcceptSettleFailure(ctx1, in); err != nil {
		t.Fatal(err)
	}
	workID := listOneWorkID(t, store)
	forceComplete(t, store, pins, workID, "owner-rt")

	pin2 := &countingPin{}
	ctx2 := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "rt", pin: pin2})
	if err := svc.AcceptSettleFailure(ctx2, in); err != nil {
		t.Fatal(err)
	}
	if pins.Len() != 0 || pin2.releases.Load() != 1 {
		t.Fatalf("pins=%d candidate releases=%d", pins.Len(), pin2.releases.Load())
	}
}

func TestAdoption_ReplayLookupFailureNoUnownedHold(t *testing.T) {
	t.Parallel()
	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "replay-lookup-fail"})
	if err != nil {
		t.Fatal(err)
	}
	pins := app.NewGenerationPinTracker()
	in := settleInput("req-rlf", "a")
	svcSeed := app.NewIntentService(backing, app.IntentServiceConfig{Pins: pins})
	pin1 := &countingPin{}
	ctx1 := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "rlf", pin: pin1})
	if err := svcSeed.AcceptSettleFailure(ctx1, in); err != nil {
		t.Fatal(err)
	}
	// Winner ownership proves safe: candidate releases; no late hold / no handoff park in tracker.
	store := &replayThenLookupFailStore{inner: backing}
	svc := app.NewIntentService(store, app.IntentServiceConfig{Pins: pins})
	pin2 := &countingPin{}
	ctx2 := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "rlf", pin: pin2})
	err = svc.AcceptSettleFailure(ctx2, in)
	if err == nil || !errors.Is(err, app.ErrAppendReconcileAmbiguous) {
		t.Fatalf("got %v want ambiguous", err)
	}
	if pins.Len() != 1 || pin1.releases.Load() != 0 {
		t.Fatalf("winner must remain; pins=%d winRel=%d", pins.Len(), pin1.releases.Load())
	}
	if pin2.releases.Load() != 1 {
		t.Fatalf("candidate releases=%d want 1", pin2.releases.Load())
	}
}

func TestAdoption_ReplayLookupFailureHandoffWhenUnsafe(t *testing.T) {
	t.Parallel()
	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "replay-lookup-handoff"})
	if err != nil {
		t.Fatal(err)
	}
	// Seed durable row without tracker ownership so OwnershipSafe is false.
	in := settleInput("req-rlfh", "a")
	svcSeed := app.NewIntentService(backing, app.IntentServiceConfig{})
	if err := svcSeed.AcceptSettleFailure(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	pins := app.NewGenerationPinTracker()
	store := &replayThenLookupFailStore{inner: backing}
	handoff := &capturingHandoff{}
	svc := app.NewIntentService(store, app.IntentServiceConfig{
		Pins:             pins,
		AmbiguousHandoff: handoff,
	})
	pin2 := &countingPin{}
	ctx2 := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "rlfh", pin: pin2})
	if err := svc.AcceptSettleFailure(ctx2, in); err == nil || !errors.Is(err, app.ErrDurablePending) {
		t.Fatalf("got %v want ErrDurablePending", err)
	}
	if pins.Len() != 0 || pins.EntryCount() != 0 {
		t.Fatalf("must not publish unowned hold; pins=%d entries=%d", pins.Len(), pins.EntryCount())
	}
	if handoff.took.Load() != 1 || pin2.releases.Load() != 0 {
		t.Fatalf("handoff took=%d releases=%d", handoff.took.Load(), pin2.releases.Load())
	}
}

type replayThenLookupFailStore struct {
	inner   *workstore.MemoryStore
	lookups atomic.Int32
}

func (s *replayThenLookupFailStore) AppendIntent(ctx context.Context, rec terminalwork.WorkRecord) error {
	_, err := s.AppendIntentOutcome(ctx, rec)
	return err
}

func (s *replayThenLookupFailStore) AppendIntentOutcome(ctx context.Context, rec terminalwork.WorkRecord) (terminalwork.AppendIntentOutcome, error) {
	outcome, err := s.inner.AppendIntentOutcome(ctx, rec)
	if err != nil {
		return outcome, err
	}
	if outcome.Inserted {
		return outcome, err
	}
	return terminalwork.AppendIntentOutcome{Replay: true}, nil
}

func (s *replayThenLookupFailStore) PromotePending(ctx context.Context, cmd terminalwork.PromotePendingCommand) error {
	return s.inner.PromotePending(ctx, cmd)
}

func (s *replayThenLookupFailStore) LookupIntent(ctx context.Context, workID string) (terminalwork.WorkRecord, bool, error) {
	if s.lookups.Add(1) == 1 {
		return terminalwork.WorkRecord{}, false, errors.New("lookup boom")
	}
	return s.inner.LookupIntent(ctx, workID)
}

func TestAdoption_TombstoneCleanupBounded(t *testing.T) {
	t.Parallel()
	pins := app.NewGenerationPinTracker()
	const n = 256
	for i := range n {
		workID := "tw_bound_" + itoa(i)
		tok := pins.BeginAdoption(workID)
		pin := &countingPin{}
		if !tok.Publish(pin, nil) {
			t.Fatal("publish")
		}
		pins.MarkTerminal(workID)
		tok.End()
	}
	if pins.Len() != 0 || pins.EntryCount() != 0 {
		t.Fatalf("unbounded tombstones; pins=%d entries=%d", pins.Len(), pins.EntryCount())
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

func TestAdoption_ReleaseDoesNotMarkTerminal(t *testing.T) {
	t.Parallel()
	pins := app.NewGenerationPinTracker()
	tok := pins.BeginAdoption("tw_abort")
	pin := &countingPin{}
	if !tok.Publish(pin, nil) {
		t.Fatal("publish")
	}
	pins.AbortOwnership("tw_abort")
	if pin.releases.Load() != 1 {
		t.Fatalf("releases=%d", pin.releases.Load())
	}
	// Reservation still live; Publish of replacement must succeed (not terminal).
	pin2 := &countingPin{}
	if !tok.Publish(pin2, nil) {
		t.Fatal("Publish after AbortOwnership must work (no terminal tombstone)")
	}
	tok.End()
	pins.MarkTerminal("tw_abort")
	if pin2.releases.Load() != 1 {
		t.Fatalf("pin2 releases=%d", pin2.releases.Load())
	}
}
