package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/genpin"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

const (
	defaultAmbiguousCapacity = 1024
	defaultAmbiguousOpLimit  = 5 * time.Second
	defaultAmbiguousRetryMin = 50 * time.Millisecond
	defaultAmbiguousRetryMax = 5 * time.Second
)

// AmbiguousAppendStore is the durable seam used by the process-owned reconciler.
type AmbiguousAppendStore interface {
	AppendIntentOutcome(context.Context, terminalwork.WorkRecord) (terminalwork.AppendIntentOutcome, error)
	LookupIntent(context.Context, string) (terminalwork.WorkRecord, bool, error)
	PromotePending(context.Context, terminalwork.PromotePendingCommand) error
}

// AmbiguousAppendReconcilerConfig configures the process-owned ambiguous-append worker.
type AmbiguousAppendReconcilerConfig struct {
	Capacity       int
	OperationLimit time.Duration
	RetryMin       time.Duration
	RetryMax       time.Duration
	Clock          func() time.Time
	// After waits for d unless ctx is canceled. Tests inject a barrier seam.
	After             func(ctx context.Context, d time.Duration) error
	Pins              *GenerationPinTracker
	ExecutablePending ExecutablePendingBinder
}

// AmbiguousAppendReconciler owns candidate runtime pins after an ambiguous append
// until durable state is definitive. Exactly one tracked worker processes a
// WorkID-keyed queue; no per-item goroutines. Memory stays bounded by Capacity;
// capacity waiters block in Take without an overflow queue or extra goroutines.
//
// Ownership contract for Take:
//   - Once Take receives an already-ambiguous append, queue-capacity
//     backpressure is independent of request cancellation. A canceled or
//     deadline-exceeded caller context never releases the candidate pin, never
//     removes the WorkID, and never returns a cancellation error from the
//     capacity wait. The blocked Take call remains the owner until capacity
//     opens (ownership transfers to the process worker) or the same WorkID is
//     already owned by the reconciler (safe duplicate/conflict resolution).
//   - Before a successful Take return, the caller owns the candidate pin.
//     Validation errors and ErrNotRunning release that pin exactly once.
//     Production starts the reconciler before HTTP admission and shuts it down
//     only after handlers/generations drain, so Take must not observe
//     not-running while capacity-blocked; that release path is only for the
//     impossible misconfiguration of calling Take when the reconciler is not
//     accepting.
//   - After a successful Take return, the reconciler owns the candidate pin
//     until adoption, conflict, terminal classification, or definitive insert.
//     Worker store operations use a private process context, not the request.
type AmbiguousAppendReconciler struct {
	store AmbiguousAppendStore

	capacity int
	opLimit  time.Duration
	retryMin time.Duration
	retryMax time.Duration
	clock    func() time.Time
	after    func(ctx context.Context, d time.Duration) error
	pins     *GenerationPinTracker

	mu   sync.Mutex
	exec ExecutablePendingBinder

	items     map[string]*ambiguousItem
	accepting bool
	started   bool
	stopping  bool // graceful drain requested; empty => stop worker
	wake      chan struct{}
	done      chan struct{}
	space     *sync.Cond // capacity waiters
}

type ambiguousItem struct {
	workID    string
	record    terminalwork.WorkRecord
	pin       genpin.Pin
	published bool
	attempt   int
	nextAt    time.Time
}

// NewAmbiguousAppendReconciler constructs a reconciler. Start must be called
// before Take. Store and Pins are required.
func NewAmbiguousAppendReconciler(store AmbiguousAppendStore, cfg AmbiguousAppendReconcilerConfig) (*AmbiguousAppendReconciler, error) {
	if store == nil {
		return nil, fmt.Errorf("terminalwork: nil ambiguous append store")
	}
	if cfg.Pins == nil {
		return nil, fmt.Errorf("terminalwork: nil generation pin tracker")
	}
	capacity := cfg.Capacity
	if capacity <= 0 {
		capacity = defaultAmbiguousCapacity
	}
	opLimit := cfg.OperationLimit
	if opLimit <= 0 {
		opLimit = defaultAmbiguousOpLimit
	}
	retryMin := max(cfg.RetryMin, 0)
	if cfg.RetryMin == 0 && cfg.RetryMax == 0 && cfg.Clock == nil && cfg.After == nil {
		retryMin = defaultAmbiguousRetryMin
	}
	retryMax := cfg.RetryMax
	if retryMax <= 0 {
		retryMax = defaultAmbiguousRetryMax
	}
	if retryMax < retryMin {
		retryMax = retryMin
	}
	clock := cfg.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	after := cfg.After
	if after == nil {
		after = func(ctx context.Context, d time.Duration) error {
			if d <= 0 {
				return ctx.Err()
			}
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	r := &AmbiguousAppendReconciler{
		store:     store,
		capacity:  capacity,
		opLimit:   opLimit,
		retryMin:  retryMin,
		retryMax:  retryMax,
		clock:     clock,
		after:     after,
		pins:      cfg.Pins,
		exec:      cfg.ExecutablePending,
		items:     make(map[string]*ambiguousItem),
		accepting: true,
		wake:      make(chan struct{}, 1),
	}
	r.space = sync.NewCond(&r.mu)
	return r, nil
}

// SetExecutablePending updates the optional executable pending binder.
func (r *AmbiguousAppendReconciler) SetExecutablePending(b ExecutablePendingBinder) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.exec = b
	r.mu.Unlock()
}

// Start owns exactly one worker goroutine until a successful empty Shutdown.
func (r *AmbiguousAppendReconciler) Start() error {
	if r == nil {
		return ErrNotRunning
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return ErrAlreadyStarted
	}
	r.started = true
	r.stopping = false
	r.accepting = true
	r.done = make(chan struct{})
	go r.worker(r.done)
	return nil
}

// Take transfers candidate ownership into the reconciler queue.
//
// Same-WorkID duplicate detection runs before capacity wait and releases the
// loser only when an existing reconciler item already owns that WorkID.
// Conflict likewise releases the loser. Queue-full waits ignore request
// cancellation: ctx is retained for AmbiguousAppendHandoff compatibility and
// is not consulted during capacity backpressure.
//
// On ErrNotRunning / validation errors the still-caller-owned pin is released
// exactly once. After success the reconciler owns the pin until a definitive
// outcome.
func (r *AmbiguousAppendReconciler) Take(ctx context.Context, amb AmbiguousAppend) error {
	if r == nil {
		if amb.Pin != nil {
			amb.Pin.Release()
		}
		return ErrNotRunning
	}
	// Request ctx must not drive capacity wait or pin release. Kept for the
	// AmbiguousAppendHandoff signature used by IntentService.handoffAmbiguous.
	_ = ctx

	workID := strings.TrimSpace(amb.WorkID)
	if workID == "" {
		workID = strings.TrimSpace(amb.Record.WorkID)
	}
	if workID == "" {
		if amb.Pin != nil {
			amb.Pin.Release()
		}
		return fmt.Errorf("%w: empty work id", sdk.ErrInvalid)
	}
	amb.WorkID = workID
	amb.Record.WorkID = workID

	r.mu.Lock()
	defer r.mu.Unlock()

	for {
		if !r.started || r.done == nil || !r.accepting {
			// Not started / not accepting: production ordering makes this
			// unreachable for capacity-blocked ambiguous handoffs. Release
			// here is the documented misconfiguration contract so the caller
			// does not leak a pin when Take never transfers ownership.
			if amb.Pin != nil {
				amb.Pin.Release()
			}
			return ErrNotRunning
		}
		if existing, ok := r.items[workID]; ok {
			same := terminalwork.SameIntentReplay(existing.record, amb.Record)
			if amb.Pin != nil {
				amb.Pin.Release()
			}
			if same {
				return nil
			}
			return ErrIntentReplayConflict
		}
		if len(r.items) < r.capacity {
			r.items[workID] = &ambiguousItem{
				workID: workID,
				record: amb.Record,
				pin:    amb.Pin,
				nextAt: r.clock().UTC(),
			}
			r.signalWakeLocked()
			return nil
		}
		// Capacity full: keep the candidate pin on this blocked call until
		// removeLocked/Shutdown broadcasts space. No overflow queue.
		r.space.Wait()
	}
}

// Shutdown stops new handoffs and waits for the queue to drain.
// When the queue is empty, the worker stops and Shutdown succeeds.
// When items remain past ctx, returns ctx error without releasing pins or
// discarding items; the worker stays live and handoffs resume so a later
// recovery drain can complete.
func (r *AmbiguousAppendReconciler) Shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if !r.started || r.done == nil {
		r.mu.Unlock()
		return nil
	}
	r.accepting = false
	r.stopping = true
	done := r.done
	pending := len(r.items)
	r.signalWakeLocked()
	r.space.Broadcast()
	r.mu.Unlock()

	if pending == 0 {
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			// Worker should exit immediately when empty; still respect ctx.
			select {
			case <-done:
				return nil
			default:
				return ctx.Err()
			}
		}
	}

	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			r.mu.Lock()
			still := len(r.items)
			if still == 0 {
				r.mu.Unlock()
				<-done
				return nil
			}
			// Leave worker live with queue intact for recovery.
			r.stopping = false
			r.accepting = true
			r.mu.Unlock()
			return ctx.Err()
		case <-ticker.C:
			r.mu.Lock()
			n := len(r.items)
			r.mu.Unlock()
			if n == 0 {
				select {
				case <-done:
					return nil
				case <-ctx.Done():
					select {
					case <-done:
						return nil
					default:
						return ctx.Err()
					}
				}
			}
		}
	}
}

// Pending reports unique WorkIDs awaiting reconciliation (tests/readiness).
func (r *AmbiguousAppendReconciler) Pending() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.items)
}

func (r *AmbiguousAppendReconciler) signalWakeLocked() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *AmbiguousAppendReconciler) worker(done chan struct{}) {
	defer func() {
		r.mu.Lock()
		r.started = false
		r.stopping = false
		r.done = nil
		r.mu.Unlock()
		close(done)
	}()
	for {
		item, wait, stop := r.nextWork()
		if stop {
			return
		}
		if item == nil {
			if wait > 0 {
				waitCtx, cancel := context.WithCancel(context.Background())
				waitDone := make(chan struct{})
				go func() {
					_ = r.after(waitCtx, wait)
					close(waitDone)
				}()
				select {
				case <-waitDone:
				case <-r.wake:
					cancel()
					<-waitDone
				}
				cancel()
				continue
			}
			<-r.wake
			continue
		}
		remove := r.reconcileOnce(item)
		r.mu.Lock()
		cur, ok := r.items[item.workID]
		if !ok || cur != item {
			r.mu.Unlock()
			continue
		}
		if remove {
			r.removeLocked(item.workID, item, false)
		} else {
			item.attempt++
			item.nextAt = r.clock().UTC().Add(r.backoff(item.attempt))
			r.signalWakeLocked()
		}
		stoppingEmpty := r.stopping && len(r.items) == 0
		r.mu.Unlock()
		if stoppingEmpty {
			return
		}
	}
}

func (r *AmbiguousAppendReconciler) nextWork() (item *ambiguousItem, wait time.Duration, stop bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopping && len(r.items) == 0 {
		return nil, 0, true
	}
	if len(r.items) == 0 {
		return nil, 0, false
	}
	now := r.clock().UTC()
	var earliest *ambiguousItem
	var earliestAt time.Time
	for _, it := range r.items {
		if !it.nextAt.After(now) {
			return it, 0, false
		}
		if earliest == nil || it.nextAt.Before(earliestAt) {
			earliest = it
			earliestAt = it.nextAt
		}
	}
	if earliest == nil {
		return nil, 0, false
	}
	return nil, earliestAt.Sub(now), false
}

func (r *AmbiguousAppendReconciler) backoff(attempt int) time.Duration {
	sched := terminalwork.RetrySchedule{
		Initial:    r.retryMin,
		Multiplier: 2,
		Max:        r.retryMax,
	}
	return sched.Delay(attempt)
}

func (r *AmbiguousAppendReconciler) removeLocked(workID string, item *ambiguousItem, releasePin bool) {
	cur, ok := r.items[workID]
	if !ok || (item != nil && cur != item) {
		return
	}
	if releasePin && cur.pin != nil {
		pin := cur.pin
		cur.pin = nil
		func() {
			defer func() { _ = recover() }()
			pin.Release()
		}()
	}
	delete(r.items, workID)
	r.space.Broadcast()
	r.signalWakeLocked()
}

func (r *AmbiguousAppendReconciler) reconcileOnce(item *ambiguousItem) (remove bool) {
	if item == nil {
		return true
	}
	token := r.pins.BeginAdoption(item.workID)
	defer token.End()

	opCtx, cancel := context.WithTimeout(context.Background(), r.opLimit)
	defer cancel()

	if item.published {
		return r.classifyAfterPromote(opCtx, item)
	}

	outcome, err := r.store.AppendIntentOutcome(opCtx, item.record)
	if err == nil && outcome.Inserted {
		return r.adoptAndPromote(opCtx, item, token)
	}
	if err == nil && outcome.Replay {
		return r.classifyLookup(opCtx, item, token, true)
	}
	// Zero outcome / error: opportunistic lookup; not-found stays ambiguous.
	return r.classifyLookup(opCtx, item, token, false)
}

func (r *AmbiguousAppendReconciler) classifyLookup(ctx context.Context, item *ambiguousItem, token *AdoptionToken, requireFound bool) bool {
	existing, found, lerr := r.store.LookupIntent(ctx, item.workID)
	if lerr != nil || !found {
		if requireFound {
			// Replay claimed a row but lookup missed: retain and retry.
			return false
		}
		return false
	}
	if !terminalwork.SameIntentReplay(existing, item.record) {
		r.releaseCandidate(item)
		return true
	}
	if existing.State.IsTerminal() {
		r.pins.MarkTerminal(item.workID)
		r.releaseCandidate(item)
		return true
	}
	return r.adoptAndPromote(ctx, item, token)
}

func (r *AmbiguousAppendReconciler) adoptAndPromote(ctx context.Context, item *ambiguousItem, token *AdoptionToken) bool {
	r.mu.Lock()
	exec := r.exec
	pin := item.pin
	r.mu.Unlock()

	published := token.PublishBound(pin, func() (func(), bool) {
		if exec == nil {
			return nil, false
		}
		return exec.Bind(item.workID, item.record.Versions)
	})
	r.mu.Lock()
	item.pin = nil
	r.mu.Unlock()
	if published {
		r.mu.Lock()
		item.published = true
		r.mu.Unlock()
		return r.classifyAfterPromote(ctx, item)
	}
	// PublishBound unwound the candidate pin (if any). Authoritative ownership
	// or a terminal tombstone means reconciliation is done.
	if r.pins.OwnershipSafe(item.workID) {
		return true
	}
	// Eligible but nothing to hold (no runtime pin / no executable gen): still
	// promote so processor-visible Intent rows progress (legacy path).
	if pin == nil {
		r.mu.Lock()
		item.published = true
		r.mu.Unlock()
		return r.classifyAfterPromote(ctx, item)
	}
	return false
}

func (r *AmbiguousAppendReconciler) classifyAfterPromote(ctx context.Context, item *ambiguousItem) bool {
	now := r.clock().UTC()
	err := r.store.PromotePending(ctx, terminalwork.PromotePendingCommand{
		WorkID: item.workID,
		Now:    now,
	})
	if err == nil {
		// Row is processor-visible; tracker owns combined resources.
		return true
	}
	existing, found, lerr := r.store.LookupIntent(ctx, item.workID)
	if lerr != nil || !found {
		return false
	}
	if existing.State.IsTerminal() {
		r.pins.MarkTerminal(item.workID)
		return true
	}
	if isProcessorVisible(existing.State) {
		return true
	}
	// Still Intent (or unknown nonterminal): retain tracker ownership and retry.
	return false
}

func (r *AmbiguousAppendReconciler) releaseCandidate(item *ambiguousItem) {
	if item == nil || item.pin == nil {
		return
	}
	pin := item.pin
	item.pin = nil
	func() {
		defer func() { _ = recover() }()
		pin.Release()
	}()
}

func isProcessorVisible(state sdk.WorkState) bool {
	return state == sdk.WorkStatePending ||
		state == sdk.WorkStateClaimed ||
		state == sdk.WorkStateRetry
}
