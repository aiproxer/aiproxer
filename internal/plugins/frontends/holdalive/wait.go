package holdalive

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

type Config struct {
	Enabled  bool
	Interval time.Duration
}

type result[T any] struct {
	value T
	err   error
}

// holdaliveProducerDiscardPanicReason is the fixed reason for a contained
// abandoned-result disposal panic. The signal carries this plus a bounded
// kind, never the raw recovered value, which may carry request material.
const holdaliveProducerDiscardPanicReason = "holdalive_producer_discard_panic"

// producerDiscardPanicKind maps a recovered disposer panic to a bounded safe
// token. Only error identity is inspected; the raw value is never rendered.
func producerDiscardPanicKind(recovered any) string {
	if err, ok := recovered.(error); ok && err != nil {
		if errors.Is(err, context.Canceled) {
			return "context_canceled"
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "context_deadline"
		}
		return "disposal_error_panic"
	}
	return "disposal_panic"
}

// reportProducerDiscardPanic makes a contained disposer panic visible as
// sanitized process diagnostics instead of crashing the process. Wait carries
// no logger, so the process default logger is the reporting channel;
// disposers must not use the response writer.
func reportProducerDiscardPanic(recovered any) {
	slog.Default().LogAttrs(context.Background(), slog.LevelError,
		"holdalive producer discard panicked",
		slog.String("reason", holdaliveProducerDiscardPanicReason),
		slog.String("cleanup_error_kind", producerDiscardPanicKind(recovered)),
	)
}

// discardAbandoned disposes an untransferred result with panic containment. A
// panicking disposer runs in this independently spawned producer goroutine,
// where an unrecovered panic would terminate the entire process beyond the
// reach of per-handler recovery. The panic is reported through the sanitized
// path and never propagates; producer-done publication is unaffected because
// the producer's own defer runs on both return and unwinding.
func discardAbandoned[T any](value T, discard func(T)) {
	defer func() {
		if r := recover(); r != nil {
			reportProducerDiscardPanic(r)
		}
	}()
	discard(value)
}

// cancelDrainGrace bounds how long Wait waits for fn to finish after ctx is canceled.
// It is a var (not a const) only so in-package tests can shorten it; production code
// must not mutate it.
var cancelDrainGrace = 5 * time.Second

// Wait runs fn and, while it is pending, optionally emits HTTP 102 Processing informational
// responses. 1xx responses do not commit the final status, so normal JSON/SSE error contracts remain intact.
//
// Ownership is transfer-or-dispose: every value fn produces is either handed to the
// caller (including a value returned alongside an error) or passed to discard exactly
// once. discard may be nil when T carries no owned resource.
//
// Enabled mode keeps the existing producer, an unbuffered result rendezvous, an
// abandonment channel, and a producer-done channel. A successful receive transfers
// ownership. Cancellation closes abandonment, does not receive the result, and waits
// only for producer completion or cancelDrainGrace. The producer selects between
// transfer and abandonment: a selected transfer cannot also be discarded, and a
// result that becomes ready while cancellation is selected is disposed by the
// producer. Simultaneous readiness keeps plain select semantics (no cancellation
// priority).
//
// An idempotent abandonment signal is deferred before any writer/timer callback, so
// every wait exit — including a WriteHeader/Flush panic — disposes the eventual
// untransferred result without the (possibly gone) caller and without touching the
// abandoned writer. A transferred result is unaffected by that later signal. Panics
// from the writer propagate unchanged; disposers must not use the response writer.
//
// Goroutine allowlist justification (audited exception to the "no per-request handler
// goroutines" rule): emitting 102 Processing keepalives requires the Execute call and
// the status write to proceed concurrently, and the caller's ctx must propagate into
// fn. After the grace Wait returns ctx.Err() and the producer disposes its late
// result inline, so no cleanup waiter goroutine or buffered resource-bearing orphan
// remains. A fn that ignores ctx still bounds caller waiting; only a permanently
// blocked fn/Close outlives the grace.
func Wait[T any](ctx context.Context, w http.ResponseWriter, cfg Config, fn func(context.Context) (T, error), discard func(T)) (T, error) {
	if !cfg.Enabled || cfg.Interval <= 0 || w == nil {
		return fn(ctx)
	}
	resultCh := make(chan result[T])
	abandoned := make(chan struct{})
	producerDone := make(chan struct{})
	var abandonOnce sync.Once
	signalAbandon := func() {
		abandonOnce.Do(func() { close(abandoned) })
	}
	// Installed before any writer/timer callback: covers every wait exit,
	// including a WriteHeader/Flush panic during unwinding.
	defer signalAbandon()
	go func() {
		defer close(producerDone)
		v, err := fn(ctx)
		select {
		case resultCh <- result[T]{value: v, err: err}:
			// Transferred: the caller owns v (and err) now.
		case <-abandoned:
			if discard != nil {
				discardAbandoned(v, discard)
			}
		}
	}()

	timer := time.NewTimer(cfg.Interval)
	defer timer.Stop()
	for {
		select {
		case r := <-resultCh:
			return r.value, r.err
		case <-ctx.Done():
			// Do not receive the result: the producer owns disposal from here.
			signalAbandon()
			select {
			case <-producerDone:
			case <-time.After(cancelDrainGrace):
			}
			var zero T
			return zero, ctx.Err()
		case <-timer.C:
			w.WriteHeader(http.StatusProcessing)
			// net/http sends informational headers immediately. Flush here
			// would implicitly commit a final 200 before Execute completes.
			timer.Reset(cfg.Interval)
		}
	}
}
