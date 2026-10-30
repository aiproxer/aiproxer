package app_test

import (
	"context"
	"errors"
	"runtime"
	"strconv"
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
	"go.uber.org/goleak"
)

// controllableAmbiguousStore drives deterministic reconciler scenarios.
type controllableAmbiguousStore struct {
	mu sync.Mutex

	appendFn  func(context.Context, terminalwork.WorkRecord) (terminalwork.AppendIntentOutcome, error)
	lookupFn  func(context.Context, string) (terminalwork.WorkRecord, bool, error)
	promoteFn func(context.Context, terminalwork.PromotePendingCommand) error

	inner *workstore.MemoryStore

	appendCalls  atomic.Int32
	lookupCalls  atomic.Int32
	promoteCalls atomic.Int32
}

func (s *controllableAmbiguousStore) AppendIntentOutcome(ctx context.Context, rec terminalwork.WorkRecord) (terminalwork.AppendIntentOutcome, error) {
	s.appendCalls.Add(1)
	s.mu.Lock()
	fn := s.appendFn
	inner := s.inner
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, rec)
	}
	if inner != nil {
		return inner.AppendIntentOutcome(ctx, rec)
	}
	return terminalwork.AppendIntentOutcome{}, errors.New("append unavailable")
}

func (s *controllableAmbiguousStore) LookupIntent(ctx context.Context, workID string) (terminalwork.WorkRecord, bool, error) {
	s.lookupCalls.Add(1)
	s.mu.Lock()
	fn := s.lookupFn
	inner := s.inner
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, workID)
	}
	if inner != nil {
		return inner.LookupIntent(ctx, workID)
	}
	return terminalwork.WorkRecord{}, false, errors.New("lookup unavailable")
}

func (s *controllableAmbiguousStore) PromotePending(ctx context.Context, cmd terminalwork.PromotePendingCommand) error {
	s.promoteCalls.Add(1)
	s.mu.Lock()
	fn := s.promoteFn
	inner := s.inner
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, cmd)
	}
	if inner != nil {
		return inner.PromotePending(ctx, cmd)
	}
	return errors.New("promote unavailable")
}

func (s *controllableAmbiguousStore) setAppend(fn func(context.Context, terminalwork.WorkRecord) (terminalwork.AppendIntentOutcome, error)) {
	s.mu.Lock()
	s.appendFn = fn
	s.mu.Unlock()
}

func (s *controllableAmbiguousStore) setLookup(fn func(context.Context, string) (terminalwork.WorkRecord, bool, error)) {
	s.mu.Lock()
	s.lookupFn = fn
	s.mu.Unlock()
}

func (s *controllableAmbiguousStore) setPromote(fn func(context.Context, terminalwork.PromotePendingCommand) error) {
	s.mu.Lock()
	s.promoteFn = fn
	s.mu.Unlock()
}

func newTestReconciler(t *testing.T, store app.AmbiguousAppendStore, pins *app.GenerationPinTracker, cfg app.AmbiguousAppendReconcilerConfig) *app.AmbiguousAppendReconciler {
	t.Helper()
	cfg.Pins = pins
	if cfg.RetryMin == 0 && cfg.RetryMax == 0 && cfg.After == nil {
		cfg.RetryMin = time.Millisecond
		cfg.RetryMax = 5 * time.Millisecond
		cfg.After = func(ctx context.Context, d time.Duration) error {
			if d <= 0 {
				d = time.Millisecond
			}
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		}
	}
	if cfg.OperationLimit <= 0 {
		cfg.OperationLimit = time.Second
	}
	r, err := app.NewAmbiguousAppendReconciler(store, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = r.Shutdown(ctx)
	})
	return r
}

func waitPending(t *testing.T, r *app.AmbiguousAppendReconciler, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.Pending() == want {
			return
		}
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("pending=%d want %d", r.Pending(), want)
}

func waitTrackerLen(t *testing.T, pins *app.GenerationPinTracker, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pins.Len() == want {
			return
		}
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("tracker len=%d want %d", pins.Len(), want)
}

func sampleRecord(workID string) terminalwork.WorkRecord {
	return terminalwork.WorkRecord{
		WorkID:         workID,
		SourceKey:      terminalwork.SourceKey{IdentityVersion: 1, Key: "sk_" + workID},
		PayloadVersion: 1,
		Kind:           sdk.WorkKindSettleRequestProvider,
		State:          sdk.WorkStateIntent,
		ProviderID:     "quota",
		Versions:       terminalwork.BoundVersions{GenerationID: "1", ProviderID: "quota"},
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
}

func TestAmbiguousAppend_CommitAmbiguousLookupMissThenReplay(t *testing.T) {
	t.Parallel()
	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "amb-replay"})
	if err != nil {
		t.Fatal(err)
	}
	rec := sampleRecord("tw_amb_replay")
	if _, err := backing.AppendIntentOutcome(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	store := &controllableAmbiguousStore{inner: backing}
	var appendN atomic.Int32
	store.setAppend(func(ctx context.Context, r terminalwork.WorkRecord) (terminalwork.AppendIntentOutcome, error) {
		n := appendN.Add(1)
		if n == 1 {
			return terminalwork.AppendIntentOutcome{}, errors.New("transport ambiguous")
		}
		return backing.AppendIntentOutcome(ctx, r)
	})
	var lookupN atomic.Int32
	store.setLookup(func(ctx context.Context, workID string) (terminalwork.WorkRecord, bool, error) {
		n := lookupN.Add(1)
		if n == 1 {
			return terminalwork.WorkRecord{}, false, nil // immediate miss races commit
		}
		return backing.LookupIntent(ctx, workID)
	})

	pins := app.NewGenerationPinTracker()
	r := newTestReconciler(t, store, pins, app.AmbiguousAppendReconcilerConfig{Capacity: 8})
	pin := &countingPin{}
	if err := r.Take(context.Background(), app.AmbiguousAppend{
		WorkID: rec.WorkID, Record: rec, Pin: pin, Cause: errors.New("ambiguous"),
	}); err != nil {
		t.Fatal(err)
	}
	waitPending(t, r, 0)
	waitTrackerLen(t, pins, 1)
	if pin.releases.Load() != 0 {
		t.Fatalf("pin released early: %d", pin.releases.Load())
	}
}

func TestAmbiguousAppend_NeverCommitsThenRecoversInsert(t *testing.T) {
	t.Parallel()
	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "amb-recover"})
	if err != nil {
		t.Fatal(err)
	}
	store := &controllableAmbiguousStore{inner: backing}
	var fail atomic.Bool
	fail.Store(true)
	store.setAppend(func(ctx context.Context, r terminalwork.WorkRecord) (terminalwork.AppendIntentOutcome, error) {
		if fail.Load() {
			return terminalwork.AppendIntentOutcome{}, errors.New("db down")
		}
		return backing.AppendIntentOutcome(ctx, r)
	})
	store.setLookup(func(ctx context.Context, workID string) (terminalwork.WorkRecord, bool, error) {
		if fail.Load() {
			return terminalwork.WorkRecord{}, false, errors.New("lookup down")
		}
		return backing.LookupIntent(ctx, workID)
	})

	pins := app.NewGenerationPinTracker()
	r := newTestReconciler(t, store, pins, app.AmbiguousAppendReconcilerConfig{Capacity: 8})
	pin := &countingPin{}
	rec := sampleRecord("tw_amb_recover")
	if err := r.Take(context.Background(), app.AmbiguousAppend{WorkID: rec.WorkID, Record: rec, Pin: pin}); err != nil {
		t.Fatal(err)
	}
	waitPending(t, r, 1)
	if pins.Len() != 0 || pin.releases.Load() != 0 {
		t.Fatalf("must retain candidate while failing; pins=%d releases=%d", pins.Len(), pin.releases.Load())
	}
	fail.Store(false)
	waitPending(t, r, 0)
	waitTrackerLen(t, pins, 1)
	if pin.releases.Load() != 0 {
		t.Fatalf("pin released=%d", pin.releases.Load())
	}
	_, found, err := backing.LookupIntent(context.Background(), rec.WorkID)
	if err != nil || !found {
		t.Fatalf("expected durable row after recovery; found=%v err=%v", found, err)
	}
}

func TestAmbiguousAppend_IntentServiceNotFoundStillHandsOff(t *testing.T) {
	t.Parallel()
	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "handoff-miss"})
	if err != nil {
		t.Fatal(err)
	}
	store := &controllableAmbiguousStore{inner: backing}
	var fail atomic.Bool
	fail.Store(true)
	store.setAppend(func(ctx context.Context, rec terminalwork.WorkRecord) (terminalwork.AppendIntentOutcome, error) {
		if fail.Load() {
			return terminalwork.AppendIntentOutcome{}, errors.New("ambiguous transport")
		}
		return backing.AppendIntentOutcome(ctx, rec)
	})
	store.setLookup(func(ctx context.Context, workID string) (terminalwork.WorkRecord, bool, error) {
		if fail.Load() {
			return terminalwork.WorkRecord{}, false, nil
		}
		return backing.LookupIntent(ctx, workID)
	})
	pins := app.NewGenerationPinTracker()
	r := newTestReconciler(t, store, pins, app.AmbiguousAppendReconcilerConfig{Capacity: 4})
	svc := app.NewIntentService(intentStoreAdapter{store}, app.IntentServiceConfig{
		Pins:             pins,
		AmbiguousHandoff: r,
	})
	pin := &countingPin{}
	ctx := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "i", id: "1", pin: pin})
	err = svc.AcceptSettleFailure(ctx, settleInput("req-miss-hand", "a"))
	if err == nil || !errors.Is(err, app.ErrDurablePending) {
		t.Fatalf("got %v want ErrDurablePending", err)
	}
	waitPending(t, r, 1)
	if pin.releases.Load() != 0 {
		t.Fatalf("pin must survive handoff; releases=%d", pin.releases.Load())
	}
	fail.Store(false)
	waitPending(t, r, 0)
	waitTrackerLen(t, pins, 1)
}

type intentStoreAdapter struct{ *controllableAmbiguousStore }

func (a intentStoreAdapter) AppendIntent(ctx context.Context, rec terminalwork.WorkRecord) error {
	_, err := a.AppendIntentOutcome(ctx, rec)
	return err
}

func TestAmbiguousAppend_RequestCancelAfterHandoffContinues(t *testing.T) {
	t.Parallel()
	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "cancel-after"})
	if err != nil {
		t.Fatal(err)
	}
	store := &controllableAmbiguousStore{inner: backing}
	gate := make(chan struct{})
	store.setAppend(func(ctx context.Context, r terminalwork.WorkRecord) (terminalwork.AppendIntentOutcome, error) {
		<-gate
		return backing.AppendIntentOutcome(ctx, r)
	})
	pins := app.NewGenerationPinTracker()
	r := newTestReconciler(t, store, pins, app.AmbiguousAppendReconcilerConfig{Capacity: 4})
	pin := &countingPin{}
	reqCtx, cancel := context.WithCancel(context.Background())
	rec := sampleRecord("tw_cancel_after")
	if err := r.Take(reqCtx, app.AmbiguousAppend{WorkID: rec.WorkID, Record: rec, Pin: pin}); err != nil {
		t.Fatal(err)
	}
	cancel() // must not stop worker
	close(gate)
	waitPending(t, r, 0)
	waitTrackerLen(t, pins, 1)
	if pin.releases.Load() != 0 {
		t.Fatalf("releases=%d", pin.releases.Load())
	}
}

// TestAmbiguousAppend_ReviewerQueueFullCancelKeepsCommittedPin replaces the
// unsafe CancelBeforeCapacityHandoffReleasesOnce behavior: after append is
// ambiguous, request cancellation while the bounded queue is full must not
// release the candidate pin or return a cancellation error.
func TestAmbiguousAppend_ReviewerQueueFullCancelKeepsCommittedPin(t *testing.T) {
	t.Parallel()
	// Register before newTestReconciler so Shutdown cleanup runs first (LIFO).
	leakOpts := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, leakOpts) })

	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "queue-full-cancel"})
	if err != nil {
		t.Fatal(err)
	}
	store := &controllableAmbiguousStore{inner: backing}

	// WorkID A fills capacity and stays ambiguous until released.
	blockA := make(chan struct{})
	store.setAppend(func(ctx context.Context, r terminalwork.WorkRecord) (terminalwork.AppendIntentOutcome, error) {
		if r.WorkID == "tw_queue_a" {
			select {
			case <-blockA:
				return backing.AppendIntentOutcome(ctx, r)
			case <-ctx.Done():
				return terminalwork.AppendIntentOutcome{}, ctx.Err()
			}
		}
		return backing.AppendIntentOutcome(ctx, r)
	})
	store.setLookup(func(ctx context.Context, workID string) (terminalwork.WorkRecord, bool, error) {
		if workID == "tw_queue_a" {
			select {
			case <-blockA:
			case <-ctx.Done():
				return terminalwork.WorkRecord{}, false, ctx.Err()
			}
		}
		return backing.LookupIntent(ctx, workID)
	})

	pins := app.NewGenerationPinTracker()
	r := newTestReconciler(t, store, pins, app.AmbiguousAppendReconcilerConfig{Capacity: 1})

	pinA := &countingPin{}
	recA := sampleRecord("tw_queue_a")
	if err := r.Take(context.Background(), app.AmbiguousAppend{WorkID: recA.WorkID, Record: recA, Pin: pinA}); err != nil {
		t.Fatal(err)
	}
	waitPending(t, r, 1)

	// B may already be committed when capacity wait begins.
	recB := sampleRecord("tw_queue_b")
	if _, err := backing.AppendIntentOutcome(context.Background(), recB); err != nil {
		t.Fatal(err)
	}
	pinB := &countingPin{}
	ctxB, cancelB := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- r.Take(ctxB, app.AmbiguousAppend{WorkID: recB.WorkID, Record: recB, Pin: pinB})
	}()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if r.Pending() == 1 && pinB.releases.Load() == 0 {
			// Give the waiter time to enter space.Wait under the mutex.
			runtime.Gosched()
			time.Sleep(5 * time.Millisecond)
			break
		}
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}

	cancelB()

	select {
	case err := <-errCh:
		t.Fatalf("committed ambiguous row lost runtime ownership on queue cancellation: %v state=intent pin_releases=%d tracker=%d reconciler_pending=%d",
			err, pinB.releases.Load(), pins.Len(), r.Pending())
	case <-time.After(80 * time.Millisecond):
	}
	if pinB.releases.Load() != 0 {
		t.Fatalf("B pin releases=%d want 0 while capacity-blocked", pinB.releases.Load())
	}
	if r.Pending() != 1 {
		t.Fatalf("pending=%d want only bounded queue item A", r.Pending())
	}

	close(blockA)

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Take after capacity: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Take did not admit B after capacity opened")
	}

	waitPending(t, r, 0)
	waitTrackerLen(t, pins, 2)
	if pinB.releases.Load() != 0 {
		t.Fatalf("B pin released early: %d", pinB.releases.Load())
	}
	rowB, found, err := backing.LookupIntent(context.Background(), recB.WorkID)
	if err != nil || !found || rowB.State != sdk.WorkStatePending {
		t.Fatalf("B must promote under retained ownership; row=%+v found=%v err=%v", rowB, found, err)
	}
	if !pins.OwnershipSafe(recB.WorkID) {
		t.Fatal("B generation pin must remain through terminal ownership")
	}

	pins.MarkTerminal(recA.WorkID)
	pins.MarkTerminal(recB.WorkID)
	if pinB.releases.Load() != 1 {
		t.Fatalf("B exact-one release=%d want 1", pinB.releases.Load())
	}
	if pinA.releases.Load() != 1 {
		t.Fatalf("A exact-one release=%d want 1", pinA.releases.Load())
	}
	if r.Pending() != 0 || pins.Len() != 0 || pins.EntryCount() != 0 {
		t.Fatalf("empty after terminal; pending=%d len=%d entries=%d", r.Pending(), pins.Len(), pins.EntryCount())
	}
}

func TestAmbiguousAppend_CapacityBlocksWithoutExtraWorkers(t *testing.T) {
	t.Parallel()
	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "cap-block"})
	if err != nil {
		t.Fatal(err)
	}
	store := &controllableAmbiguousStore{inner: backing}
	block := make(chan struct{})
	store.setAppend(func(ctx context.Context, r terminalwork.WorkRecord) (terminalwork.AppendIntentOutcome, error) {
		select {
		case <-block:
			return backing.AppendIntentOutcome(ctx, r)
		case <-ctx.Done():
			return terminalwork.AppendIntentOutcome{}, ctx.Err()
		}
	})
	pins := app.NewGenerationPinTracker()
	r := newTestReconciler(t, store, pins, app.AmbiguousAppendReconcilerConfig{Capacity: 1})
	t.Cleanup(func() {
		close(block)
		waitPending(t, r, 0)
	})
	rec1 := sampleRecord("tw_block_1")
	if err := r.Take(context.Background(), app.AmbiguousAppend{WorkID: rec1.WorkID, Record: rec1, Pin: &countingPin{}}); err != nil {
		t.Fatal(err)
	}
	waitPending(t, r, 1)
	// Capacity waiters block in Take on the caller goroutine; bounded queue stays
	// at one item (no overflow worker pool). Absolute NumGoroutine checks are
	// flaky under parallel -race/-count siblings; goleak covers process leaks.
	rec2 := sampleRecord("tw_block_2")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	pin2 := &countingPin{}
	errCh := make(chan error, 1)
	go func() {
		errCh <- r.Take(ctx, app.AmbiguousAppend{WorkID: rec2.WorkID, Record: rec2, Pin: pin2})
	}()
	select {
	case err := <-errCh:
		cancel()
		t.Fatalf("capacity wait must ignore deadline; got %v", err)
	case <-time.After(50 * time.Millisecond):
		cancel()
	}
	if pin2.releases.Load() != 0 {
		t.Fatalf("pin2 releases=%d want 0", pin2.releases.Load())
	}
	if r.Pending() != 1 {
		t.Fatalf("pending=%d", r.Pending())
	}
}

func TestAmbiguousAppend_DuplicateSameWorkIDOneItem(t *testing.T) {
	t.Parallel()
	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "dup-same"})
	if err != nil {
		t.Fatal(err)
	}
	store := &controllableAmbiguousStore{inner: backing}
	gate := make(chan struct{})
	store.setAppend(func(ctx context.Context, r terminalwork.WorkRecord) (terminalwork.AppendIntentOutcome, error) {
		<-gate
		return backing.AppendIntentOutcome(ctx, r)
	})
	pins := app.NewGenerationPinTracker()
	r := newTestReconciler(t, store, pins, app.AmbiguousAppendReconcilerConfig{Capacity: 4})
	rec := sampleRecord("tw_dup_same")
	pin1 := &countingPin{}
	pin2 := &countingPin{}
	if err := r.Take(context.Background(), app.AmbiguousAppend{WorkID: rec.WorkID, Record: rec, Pin: pin1}); err != nil {
		t.Fatal(err)
	}
	if err := r.Take(context.Background(), app.AmbiguousAppend{WorkID: rec.WorkID, Record: rec, Pin: pin2}); err != nil {
		t.Fatal(err)
	}
	if r.Pending() != 1 {
		t.Fatalf("pending=%d want 1", r.Pending())
	}
	if pin2.releases.Load() != 1 {
		t.Fatalf("loser releases=%d", pin2.releases.Load())
	}
	close(gate)
	waitPending(t, r, 0)
	if pin1.releases.Load() != 0 {
		t.Fatalf("winner released=%d", pin1.releases.Load())
	}
}

func TestAmbiguousAppend_ConflictingDuplicate(t *testing.T) {
	t.Parallel()
	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "dup-conflict"})
	if err != nil {
		t.Fatal(err)
	}
	store := &controllableAmbiguousStore{inner: backing}
	gate := make(chan struct{})
	store.setAppend(func(ctx context.Context, r terminalwork.WorkRecord) (terminalwork.AppendIntentOutcome, error) {
		<-gate
		return backing.AppendIntentOutcome(ctx, r)
	})
	pins := app.NewGenerationPinTracker()
	r := newTestReconciler(t, store, pins, app.AmbiguousAppendReconcilerConfig{Capacity: 4})
	rec := sampleRecord("tw_dup_conflict")
	conflict := rec
	conflict.Versions.RatingID = "other"
	pin1 := &countingPin{}
	pin2 := &countingPin{}
	if err := r.Take(context.Background(), app.AmbiguousAppend{WorkID: rec.WorkID, Record: rec, Pin: pin1}); err != nil {
		t.Fatal(err)
	}
	err = r.Take(context.Background(), app.AmbiguousAppend{WorkID: conflict.WorkID, Record: conflict, Pin: pin2})
	if !errors.Is(err, app.ErrIntentReplayConflict) {
		t.Fatalf("got %v want conflict", err)
	}
	if pin2.releases.Load() != 1 {
		t.Fatalf("loser releases=%d", pin2.releases.Load())
	}
	close(gate)
	waitPending(t, r, 0)
}

func TestAmbiguousAppend_TerminalBetweenLookupAndBindRejectsPublish(t *testing.T) {
	t.Parallel()
	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "term-race"})
	if err != nil {
		t.Fatal(err)
	}
	rec := sampleRecord("tw_term_race")
	if _, err := backing.AppendIntentOutcome(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	store := &controllableAmbiguousStore{inner: backing}
	afterLookup := make(chan struct{})
	afterTerminal := make(chan struct{})
	store.setAppend(func(context.Context, terminalwork.WorkRecord) (terminalwork.AppendIntentOutcome, error) {
		return terminalwork.AppendIntentOutcome{Replay: true}, nil
	})
	var lookups atomic.Int32
	store.setLookup(func(ctx context.Context, workID string) (terminalwork.WorkRecord, bool, error) {
		n := lookups.Add(1)
		if n == 1 {
			row, found, err := backing.LookupIntent(ctx, workID)
			if err != nil || !found {
				return row, found, err
			}
			close(afterLookup)
			<-afterTerminal
			return row, true, nil
		}
		return terminalwork.WorkRecord{}, false, errors.New("transient after terminal")
	})

	pins := app.NewGenerationPinTracker()
	execGen := &snapshotgen.ExecutableGeneration{ID: 7, Version: "v"}
	var bindCalls atomic.Int32
	r := newTestReconciler(t, store, pins, app.AmbiguousAppendReconcilerConfig{
		Capacity: 4,
		ExecutablePending: countingExecutableBinder{
			inner: executableBinder{gen: execGen},
			calls: &bindCalls,
		},
	})
	pin := &countingPin{}
	if err := r.Take(context.Background(), app.AmbiguousAppend{WorkID: rec.WorkID, Record: rec, Pin: pin}); err != nil {
		t.Fatal(err)
	}
	<-afterLookup
	pins.MarkTerminal(rec.WorkID)
	close(afterTerminal)
	waitPending(t, r, 0)
	if bindCalls.Load() != 0 {
		t.Fatalf("PublishBound must not Bind after terminal; calls=%d", bindCalls.Load())
	}
	if pins.Len() != 0 || pins.EntryCount() != 0 {
		t.Fatalf("zero holds required; len=%d entries=%d", pins.Len(), pins.EntryCount())
	}
	if pin.releases.Load() != 1 {
		t.Fatalf("candidate releases=%d want 1", pin.releases.Load())
	}
	if execGen.PendingWorkCount() != 0 {
		t.Fatalf("exec pending=%d", execGen.PendingWorkCount())
	}
}

func TestAmbiguousAppend_PromoteErrorThenProcessorVisibleDrains(t *testing.T) {
	t.Parallel()
	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "promote-class"})
	if err != nil {
		t.Fatal(err)
	}
	store := &controllableAmbiguousStore{inner: backing}
	var promoteN atomic.Int32
	store.setPromote(func(ctx context.Context, cmd terminalwork.PromotePendingCommand) error {
		n := promoteN.Add(1)
		if n == 1 {
			// Commit promote, then return error.
			if err := backing.PromotePending(ctx, cmd); err != nil {
				return err
			}
			return errors.New("promote ack lost")
		}
		return backing.PromotePending(ctx, cmd)
	})
	pins := app.NewGenerationPinTracker()
	r := newTestReconciler(t, store, pins, app.AmbiguousAppendReconcilerConfig{Capacity: 4})
	pin := &countingPin{}
	rec := sampleRecord("tw_promote_class")
	if err := r.Take(context.Background(), app.AmbiguousAppend{WorkID: rec.WorkID, Record: rec, Pin: pin}); err != nil {
		t.Fatal(err)
	}
	waitPending(t, r, 0)
	waitTrackerLen(t, pins, 1)
	if pin.releases.Load() != 0 {
		t.Fatalf("tracker must retain ownership; releases=%d", pin.releases.Load())
	}
	row, found, err := backing.LookupIntent(context.Background(), rec.WorkID)
	if err != nil || !found || row.State != sdk.WorkStatePending {
		t.Fatalf("row=%+v found=%v err=%v", row, found, err)
	}
}

func TestAmbiguousAppend_TerminalRowReleases(t *testing.T) {
	t.Parallel()
	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "term-row"})
	if err != nil {
		t.Fatal(err)
	}
	rec := sampleRecord("tw_term_row")
	if _, err := backing.AppendIntentOutcome(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if err := backing.PromotePending(context.Background(), terminalwork.PromotePendingCommand{WorkID: rec.WorkID, Now: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	// Mark completed via direct state poke through quarantine/complete is heavy;
	// use lookup override returning terminal.
	store := &controllableAmbiguousStore{inner: backing}
	store.setAppend(func(context.Context, terminalwork.WorkRecord) (terminalwork.AppendIntentOutcome, error) {
		return terminalwork.AppendIntentOutcome{Replay: true}, nil
	})
	store.setLookup(func(ctx context.Context, workID string) (terminalwork.WorkRecord, bool, error) {
		row, found, err := backing.LookupIntent(ctx, workID)
		if err != nil || !found {
			return row, found, err
		}
		row.State = sdk.WorkStateCompleted
		return row, true, nil
	})
	pins := app.NewGenerationPinTracker()
	r := newTestReconciler(t, store, pins, app.AmbiguousAppendReconcilerConfig{Capacity: 4})
	pin := &countingPin{}
	if err := r.Take(context.Background(), app.AmbiguousAppend{WorkID: rec.WorkID, Record: rec, Pin: pin}); err != nil {
		t.Fatal(err)
	}
	waitPending(t, r, 0)
	if pins.Len() != 0 || pin.releases.Load() != 1 {
		t.Fatalf("pins=%d releases=%d", pins.Len(), pin.releases.Load())
	}
}

func TestAmbiguousAppend_ShutdownDBDownTimesOutThenRecovers(t *testing.T) {
	t.Parallel()
	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "shutdown-db"})
	if err != nil {
		t.Fatal(err)
	}
	store := &controllableAmbiguousStore{inner: backing}
	var down atomic.Bool
	down.Store(true)
	store.setAppend(func(ctx context.Context, r terminalwork.WorkRecord) (terminalwork.AppendIntentOutcome, error) {
		if down.Load() {
			return terminalwork.AppendIntentOutcome{}, errors.New("db down")
		}
		return backing.AppendIntentOutcome(ctx, r)
	})
	store.setLookup(func(ctx context.Context, workID string) (terminalwork.WorkRecord, bool, error) {
		if down.Load() {
			return terminalwork.WorkRecord{}, false, errors.New("lookup down")
		}
		return backing.LookupIntent(ctx, workID)
	})
	pins := app.NewGenerationPinTracker()
	r, err := app.NewAmbiguousAppendReconciler(store, app.AmbiguousAppendReconcilerConfig{
		Capacity: 4, Pins: pins,
		RetryMin: time.Millisecond, RetryMax: 5 * time.Millisecond,
		After: func(ctx context.Context, d time.Duration) error {
			if d <= 0 {
				d = time.Millisecond
			}
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
		OperationLimit: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	pin := &countingPin{}
	rec := sampleRecord("tw_shutdown_db")
	if err := r.Take(context.Background(), app.AmbiguousAppend{WorkID: rec.WorkID, Record: rec, Pin: pin}); err != nil {
		t.Fatal(err)
	}
	waitPending(t, r, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	err = r.Shutdown(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v want deadline", err)
	}
	if r.Pending() != 1 || pin.releases.Load() != 0 {
		t.Fatalf("must keep queue/pin; pending=%d releases=%d", r.Pending(), pin.releases.Load())
	}

	down.Store(false)
	waitPending(t, r, 0)
	waitTrackerLen(t, pins, 1)

	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := r.Shutdown(ctx2); err != nil {
		t.Fatalf("drain shutdown: %v", err)
	}
}

func TestAmbiguousAppend_OneWorkerRaceTakeTerminalShutdown(t *testing.T) {
	t.Parallel()
	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "race-one"})
	if err != nil {
		t.Fatal(err)
	}
	store := &controllableAmbiguousStore{inner: backing}
	pins := app.NewGenerationPinTracker()
	r := newTestReconciler(t, store, pins, app.AmbiguousAppendReconcilerConfig{Capacity: 64})

	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := sampleRecord("tw_race_" + strconv.Itoa(i))
			pin := &countingPin{}
			_ = r.Take(context.Background(), app.AmbiguousAppend{WorkID: rec.WorkID, Record: rec, Pin: pin})
			if i%4 == 0 {
				pins.MarkTerminal(rec.WorkID)
			}
		}(i)
	}
	wg.Wait()
	waitPending(t, r, 0)
}

func TestAmbiguousAppend_NoGoroutineLeakStartupShutdown(t *testing.T) {
	t.Parallel()
	leakOpts := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, leakOpts) })
	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "leak"})
	if err != nil {
		t.Fatal(err)
	}
	pins := app.NewGenerationPinTracker()
	for i := range 5 {
		r, err := app.NewAmbiguousAppendReconciler(backing, app.AmbiguousAppendReconcilerConfig{
			Capacity: 4, Pins: pins,
			RetryMin: time.Millisecond, RetryMax: 5 * time.Millisecond,
			After: func(ctx context.Context, d time.Duration) error {
				if d <= 0 {
					d = time.Millisecond
				}
				timer := time.NewTimer(d)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-timer.C:
					return nil
				}
			},
			OperationLimit: time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Start(); err != nil {
			t.Fatal(err)
		}
		rec := sampleRecord("tw_leak_" + strconv.Itoa(i))
		pin := &countingPin{}
		if err := r.Take(context.Background(), app.AmbiguousAppend{WorkID: rec.WorkID, Record: rec, Pin: pin}); err != nil {
			t.Fatal(err)
		}
		waitPending(t, r, 0)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if err := r.Shutdown(ctx); err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
	}
}
