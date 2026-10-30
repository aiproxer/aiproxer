package stream

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ErrNilEventStream is returned by NewKeepalive when the inner stream is nil.
// It is identical to aipapi.ErrNilEventStream so callers can use errors.Is against either package.
var ErrNilEventStream = aipapi.ErrNilEventStream

// ErrNilKeepalive is returned by Recv when the receiver is a nil *Keepalive.
var ErrNilKeepalive = errors.New("stream: nil Keepalive")

// KeepaliveConfig controls keepalive event injection during idle stream waits.
type KeepaliveConfig struct {
	// Interval is the idle window before a synthetic keepalive may be returned from Keepalive.Recv.
	// Zero schedules the first keepalive as soon as the inner stream blocks (high churn); use a
	// positive duration in production paths.
	Interval time.Duration
	// NewKeepalive builds the synthetic event. Nil selects DefaultKeepaliveEvent.
	NewKeepalive func() aipapi.Event
	// Log when set receives the rare reader-panic log when the panic cannot be sent on the result channel.
	Log *slog.Logger
}

// KeepaliveEventCode is the WarningCode used for canonical keepalive events.
const KeepaliveEventCode = "keepalive"

// DefaultKeepaliveEvent returns a standard keepalive warning event.
func DefaultKeepaliveEvent() aipapi.Event {
	return aipapi.Event{
		Kind:           aipapi.EventWarning,
		WarningCode:    KeepaliveEventCode,
		WarningMessage: "keepalive",
	}
}

// Keepalive wraps an inner EventStream and emits keepalive events at the configured
// interval while Recv is blocked waiting for the next real event. This prevents
// client-side idle timeouts during recovery waits (Req 5.5).
//
// A single background goroutine sequentially reads from the inner stream,
// preventing concurrent access. Recv returns either a buffered real event or
// a keepalive when the inner stream hasn't produced a result within the interval.
// Close cancels any in-flight inner Recv (see abortRead) and closes k.done so the
// reader loop exits without a second goroutine.
//
// EventStream concurrency: one goroutine calls Recv at a time. Close is safe
// concurrently with Recv blocked on the keepalive select or inner I/O; Close aborts
// the in-flight inner Recv then closes the inner stream.
type Keepalive struct {
	inner aipapi.EventStream
	cfg   KeepaliveConfig

	mu     sync.Mutex
	closed bool

	once         sync.Once
	shutdownOnce sync.Once
	shutdownErr  error
	result       chan item
	done         chan struct{}

	// idleTimer is the single wait timer reused by every Recv of this stream.
	// Recv runs once per streamed event, so a timer allocated per call would
	// put a timer allocation and a runtime timer registration on the per-event
	// path. Reuse relies on the documented single-consumer Recv contract above.
	idleTimer *time.Timer

	// abortRead cancels the in-flight inner.Recv when the caller's Recv context
	// ends (deadline/cancel) so we do not leak the reader goroutine when Close is
	// never called. Timer-based keepalive returns clear the pending AfterFunc via
	// defer stop() without cancelling the inner read.
	abortMu      sync.Mutex
	abortRead    context.CancelFunc
	abortPending bool
	// abortWatch stops the watcher that aborts the in-flight inner read when the
	// caller context observed by Recv ends, and abortWatchOn is the cancellation
	// channel that watcher is registered for. A stream's Recv calls observe the
	// same cancellation channel for every event, so the watcher is kept while that
	// channel is unchanged instead of being registered (and allocated) per event.
	abortWatch   func() bool
	abortWatchOn <-chan struct{}

	readerWG sync.WaitGroup
}

var _ aipapi.EventStream = (*Keepalive)(nil)

type item struct {
	ev  aipapi.Event
	err error
}

// preferBufferedItemOrKeepalive returns a buffered inner item when one is already
// available on ch; otherwise it emits a keepalive. Used after the outer select
// chose the keepalive timer while a real event may have become ready concurrently.
func preferBufferedItemOrKeepalive(ch <-chan item, kaFn func() aipapi.Event) (aipapi.Event, error) {
	select {
	case it, ok := <-ch:
		if !ok {
			return aipapi.Event{}, context.Canceled
		}
		return it.ev, it.err
	default:
		return kaFn(), nil
	}
}

// NewKeepalive wraps s with keepalive injection using cfg. It returns ErrNilEventStream if s is nil.
func NewKeepalive(s aipapi.EventStream, cfg KeepaliveConfig) (*Keepalive, error) {
	if s == nil {
		return nil, ErrNilEventStream
	}
	kaFn := cfg.NewKeepalive
	if kaFn == nil {
		kaFn = DefaultKeepaliveEvent
	}
	return &Keepalive{
		inner: s,
		cfg: KeepaliveConfig{
			Interval:     cfg.Interval,
			NewKeepalive: kaFn,
			Log:          cfg.Log,
		},
		// Buffer 2: one slot may hold a queued inner event while Recv waits on the keepalive
		// timer; the reader goroutine must still be able to deliver a panic without dropping it.
		result: make(chan item, 2),
		done:   make(chan struct{}),
	}, nil
}

func (k *Keepalive) startReader() {
	k.once.Do(func() {
		k.readerWG.Go(func() {
			defer func() {
				if r := recover(); r != nil {
					pe := safety.Capture(safety.BoundaryWorker, "stream_keepalive_reader", r)
					ctx := context.Background()
					select {
					case k.result <- item{err: pe}:
					case <-k.done:
					default:
						attrs := safety.PanicSlogFieldAttrs(pe)
						attrs = safety.AppendPanicStackAttr(attrs, pe)
						log := k.cfg.Log
						if log == nil {
							log = slog.Default()
						}
						log.LogAttrs(ctx, slog.LevelError, "stream keepalive: reader panic could not be sent on result channel", attrs...)
					}
				}
				close(k.result)
			}()
			// Detached cancel tree: the reader goroutine must not inherit the outer Recv
			// deadline; the consumer's ctx only aborts the inner read via AfterFunc/Close.
			//
			// One cancel context serves every read until an abort cancels it. An aborted
			// read returns an error, which ends this loop, so a cancelled context is never
			// handed to a later read and the reuse is invisible to the inner stream; it
			// keeps one allocation per streamed event off the client-facing path.
			var (
				innerCtx    context.Context
				cancelInner context.CancelFunc
			)
			for {
				select {
				case <-k.done:
					return
				default:
				}
				if innerCtx == nil || innerCtx.Err() != nil {
					innerCtx, cancelInner = context.WithCancel(context.Background())
				}
				k.setAbortRead(cancelInner)
				select {
				case <-k.done:
					cancelInner()
					return
				default:
				}
				ev, err := k.inner.Recv(innerCtx)
				k.clearAbortRead()
				select {
				case k.result <- item{ev: ev, err: err}:
				case <-k.done:
					cancelInner()
					return
				}
				if err != nil {
					return
				}
			}
		})
	})
}

// watchAbort keeps exactly one watcher registered on the cancellation channel of
// the context Recv is observing. The watcher aborts the in-flight inner read when
// that context ends, so a stream never outlives its caller's cancellation. A
// context that cannot be cancelled is never watched: such a watcher could not fire.
func (k *Keepalive) watchAbort(ctx context.Context) {
	done := ctx.Done()
	if done == nil {
		return
	}
	k.abortMu.Lock()
	if k.abortWatch != nil && k.abortWatchOn == done {
		k.abortMu.Unlock()
		return
	}
	previous := k.abortWatch
	k.abortWatchOn = done
	k.abortWatch = context.AfterFunc(ctx, k.abortCurrentRead)
	k.abortMu.Unlock()
	if previous != nil {
		previous()
	}
}

// stopAbortWatch drops the abort watcher, releasing the callback it registered on
// the caller's context.
func (k *Keepalive) stopAbortWatch() {
	k.abortMu.Lock()
	stop := k.abortWatch
	k.abortWatch, k.abortWatchOn = nil, nil
	k.abortMu.Unlock()
	if stop != nil {
		stop()
	}
}

func (k *Keepalive) setAbortRead(cancel context.CancelFunc) {
	k.abortMu.Lock()
	k.abortRead = cancel
	pending := k.abortPending
	k.abortMu.Unlock()
	if pending {
		cancel()
	}
}

func (k *Keepalive) clearAbortRead() {
	k.abortMu.Lock()
	k.abortRead = nil
	k.abortPending = false
	k.abortMu.Unlock()
}

func (k *Keepalive) abortCurrentRead() {
	k.abortMu.Lock()
	fn := k.abortRead
	if fn != nil {
		k.abortRead = nil
	} else {
		k.abortPending = true
	}
	k.abortMu.Unlock()
	if fn != nil {
		fn()
	}
}

// Recv returns the next event from the inner stream, or a keepalive event if the
// inner stream is idle beyond the configured interval.
func (k *Keepalive) Recv(ctx context.Context) (aipapi.Event, error) {
	if k == nil {
		return aipapi.Event{}, ErrNilKeepalive
	}
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return aipapi.Event{}, err
	}
	k.mu.Lock()
	if k.closed {
		k.mu.Unlock()
		return aipapi.Event{}, context.Canceled
	}
	k.mu.Unlock()

	k.startReader()
	k.watchAbort(ctx)

	kaFn := k.cfg.NewKeepalive

	timer := k.armIdleTimer()
	defer timer.Stop()

	select {
	case <-ctx.Done():
		k.abortCurrentRead()
		return aipapi.Event{}, ctx.Err()
	case it, ok := <-k.result:
		// AfterFunc aborts the detached inner read when ctx ends. That abort posts
		// context.Canceled on the result channel and can race with ctx.Done() here.
		// Prefer the caller's context error so consumers observe DeadlineExceeded
		// (or their cancel) instead of the inner abort cancel.
		if err := ctx.Err(); err != nil && (!ok || errors.Is(it.err, context.Canceled)) {
			return aipapi.Event{}, err
		}
		if !ok {
			return aipapi.Event{}, context.Canceled
		}
		return it.ev, it.err
	case <-timer.C:
		// If ctx was canceled in the same window as the timer, prefer cancellation
		// over emitting a keepalive.
		select {
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		default:
		}
		// If the inner reader posted a result in the same scheduling window as the
		// timer, Go's select chooses arbitrarily among ready cases. Prefer a buffered
		// real event over a synthetic keepalive for stable stream semantics and tests.
		return preferBufferedItemOrKeepalive(k.result, kaFn)
	}
}

// armIdleTimer returns this stream's idle wait timer armed for one interval.
// The timer is created on first use and reused by later Recv calls. Go 1.23+
// Reset semantics guarantee that a receive after Reset cannot observe a value
// from the previous interval, so no drain between arms is required.
func (k *Keepalive) armIdleTimer() *time.Timer {
	if k.idleTimer == nil {
		k.idleTimer = time.NewTimer(k.cfg.Interval)
		return k.idleTimer
	}
	k.idleTimer.Reset(k.cfg.Interval)
	return k.idleTimer
}

// Close stops the reader goroutine and closes the inner stream.
// Close is idempotent and safe for concurrent callers.
func (k *Keepalive) Close() error {
	if k == nil {
		return nil
	}
	k.shutdownOnce.Do(func() {
		k.mu.Lock()
		k.closed = true
		k.mu.Unlock()
		// Close k.done first so the reader loop cannot start another inner Recv after
		// shutdown; abortCurrentRead then cancels any in-flight read (including one
		// whose cancel func was not yet registered — see abortPending). Inner Close
		// runs before Wait so backends that ignore ctx while blocked still release the
		// reader goroutine (see recv_context_contract_test).
		close(k.done)
		k.abortCurrentRead()
		k.stopAbortWatch()
		k.shutdownErr = k.inner.Close()
		k.readerWG.Wait()
	})
	return k.shutdownErr
}
