package stream_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type delayedStream struct {
	events []aipapi.Event
	delay  time.Duration
	index  int
}

func (d *delayedStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	if d.index >= len(d.events) {
		t := time.NewTimer(d.delay)
		defer func() {
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
		}()
		select {
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		case <-t.C:
			// If cancellation became ready in the same scheduling window as the timer,
			// Go's select may pick the timer branch; prefer ctx for deterministic tests.
			select {
			case <-ctx.Done():
				return aipapi.Event{}, ctx.Err()
			default:
			}
			return aipapi.Event{}, io.EOF
		}
	}
	tm := time.NewTimer(d.delay)
	defer func() {
		if !tm.Stop() {
			select {
			case <-tm.C:
			default:
			}
		}
	}()
	select {
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	case <-tm.C:
		select {
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		default:
		}
		ev := d.events[d.index]
		d.index++
		return ev, nil
	}
}

func (d *delayedStream) Close() error { return nil }

func mustNewKeepalive(t *testing.T, inner aipapi.EventStream, cfg stream.KeepaliveConfig) *stream.Keepalive {
	t.Helper()
	ka, err := stream.NewKeepalive(inner, cfg)
	if err != nil {
		t.Fatalf("NewKeepalive: %v", err)
	}
	return ka
}

func TestKeepalive_emitsDuringIdle(t *testing.T) {
	t.Parallel()

	inner := &delayedStream{
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventTextDelta, Delta: "hi"},
			{Kind: aipapi.EventResponseFinished},
		},
		delay: 60 * time.Millisecond,
	}

	ka := mustNewKeepalive(t, inner, stream.KeepaliveConfig{
		Interval: 20 * time.Millisecond,
	})
	defer func() { _ = ka.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var keepaliveCount int
	var realCount int

	for {
		ev, err := ka.Recv(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev.WarningCode == stream.KeepaliveEventCode {
			keepaliveCount++
		} else {
			realCount++
		}
	}

	if realCount != 3 {
		t.Fatalf("expected 3 real events, got %d", realCount)
	}
	if keepaliveCount == 0 {
		t.Fatal("expected at least one keepalive event during idle waits")
	}
}

func TestKeepalive_customKeepaliveEvent(t *testing.T) {
	t.Parallel()

	customEvent := aipapi.Event{
		Kind:           aipapi.EventWarning,
		WarningCode:    "custom_ka",
		WarningMessage: "custom keepalive",
	}

	inner := &delayedStream{
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseFinished},
		},
		// Long delay vs keepalive interval so the first outer Recv is not flaky under
		// scheduler stalls: if both timer.C and k.result become ready together, select
		// chooses arbitrarily between them.
		delay: 300 * time.Millisecond,
	}

	ka := mustNewKeepalive(t, inner, stream.KeepaliveConfig{
		Interval: 15 * time.Millisecond,
		NewKeepalive: func() aipapi.Event {
			return customEvent
		},
	})
	defer func() { _ = ka.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ev, err := ka.Recv(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.WarningCode != "custom_ka" {
		t.Fatalf("expected custom keepalive, got code %q", ev.WarningCode)
	}
}

func TestKeepalive_propagatesEOF(t *testing.T) {
	t.Parallel()
	for _, idle := range []bool{false, true} {
		name := "immediate EOF"
		if idle {
			name = "EOF after keepalive"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			unblock := make(chan struct{})
			if !idle {
				close(unblock)
			}
			ka := mustNewKeepalive(t, &blockingRecvStream{unblock: unblock}, stream.KeepaliveConfig{
				Interval: time.Millisecond,
			})
			defer func() { _ = ka.Close() }()
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			if idle {
				ev, err := ka.Recv(ctx)
				if err != nil || ev.Kind != aipapi.EventWarning || ev.WarningCode != stream.KeepaliveEventCode {
					t.Fatalf("blocked source must emit a keepalive before EOF: event=%+v err=%v", ev, err)
				}
				close(unblock)
			}
			for {
				ev, err := ka.Recv(ctx)
				if err != nil {
					if !errors.Is(err, io.EOF) {
						t.Fatalf("expected EOF, got %v", err)
					}
					break
				}
				// The reader can be scheduled after the keepalive timer even
				// when EOF is ready. Only synthetic keepalives may precede EOF.
				if ev.Kind != aipapi.EventWarning || ev.WarningCode != stream.KeepaliveEventCode {
					t.Fatalf("empty source emitted a real event before EOF: %+v", ev)
				}
			}
		})
	}
}

func TestKeepalive_respectsCancellation(t *testing.T) {
	t.Parallel()

	blockCh := make(chan struct{})
	blockingStream := &blockingRecvStream{unblock: blockCh}

	ka := mustNewKeepalive(t, blockingStream, stream.KeepaliveConfig{
		Interval: 200 * time.Millisecond,
	})
	defer func() { _ = ka.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := ka.Recv(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
	close(blockCh)
}

type blockingRecvStream struct {
	unblock chan struct{}
}

func (b *blockingRecvStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	select {
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	case <-b.unblock:
		// If unblock and cancellation become ready together, prefer ctx (deterministic).
		select {
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		default:
		}
		return aipapi.Event{}, io.EOF
	}
}

func (b *blockingRecvStream) Close() error { return nil }

func TestKeepalive_closeStopsEmission(t *testing.T) {
	t.Parallel()

	inner := &delayedStream{
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseFinished},
		},
		delay: 10 * time.Second,
	}

	ka := mustNewKeepalive(t, inner, stream.KeepaliveConfig{
		Interval: 20 * time.Millisecond,
	})

	if err := ka.Close(); err != nil {
		t.Fatalf("close error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := ka.Recv(ctx)
	if err == nil {
		t.Fatal("expected error after close")
	}
}

func TestKeepalive_passesThroughRealEvents(t *testing.T) {
	t.Parallel()

	inner := &delayedStream{
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventTextDelta, Delta: "hello"},
			{Kind: aipapi.EventResponseFinished},
		},
		delay: 1 * time.Millisecond,
	}

	ka := mustNewKeepalive(t, inner, stream.KeepaliveConfig{
		Interval: 100 * time.Millisecond,
	})
	defer func() { _ = ka.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var deltas []string
	for {
		ev, err := ka.Recv(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev.Kind == aipapi.EventTextDelta {
			deltas = append(deltas, ev.Delta)
		}
	}

	if len(deltas) != 1 || deltas[0] != "hello" {
		t.Fatalf("expected [hello], got %v", deltas)
	}
}

// delayedStream must prefer ctx cancellation over a zero-delay timer when both are
// simultaneously ready; otherwise Recv can return a non-deterministic mix of
// context.Canceled vs the next event / io.EOF across runs (Go select fairness).
func TestDelayedStream_prefersCanceledContextOverZeroDelayEvents(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := &delayedStream{
		events: []aipapi.Event{{Kind: aipapi.EventResponseStarted}},
		delay:  0,
	}
	for range 200 {
		_, err := d.Recv(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	}
}

func TestKeepalive_recvRejectsPreCanceledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	inner := aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}})
	ka := mustNewKeepalive(t, inner, stream.KeepaliveConfig{Interval: time.Hour})
	defer func() { _ = ka.Close() }()
	_, err := ka.Recv(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestDelayedStream_prefersCanceledContextOverZeroDelayEOF(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := &delayedStream{events: nil, delay: 0}
	for range 200 {
		_, err := d.Recv(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v (expected not io.EOF when ctx canceled)", err)
		}
	}
}
