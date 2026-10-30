package stream_test

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Recv returning early due to caller context cancellation must unblock the inner
// Recv without requiring Close(), otherwise the background reader goroutine leaks
// for every abandoned wait.
func TestKeepalive_recvDeadlineWithoutClose_doesNotLeakReaderGoroutines(t *testing.T) {
	t.Parallel()

	runtime.GC()
	baseline := runtime.NumGoroutine()

	const iters = 40
	for range iters {
		block := make(chan struct{})
		inner := &blockingRecvStream{unblock: block}

		ka := mustNewKeepalive(t, inner, stream.KeepaliveConfig{
			Interval: time.Hour,
		})

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		_, err := ka.Recv(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Recv: got %v want DeadlineExceeded", err)
		}

		// Intentionally do not Close ka: caller context expiry must release the reader.
		_ = ka
	}

	for range 300 {
		runtime.GC()
		if n := runtime.NumGoroutine(); n <= baseline+2 {
			return
		}
		runtime.Gosched()
	}

	t.Fatalf("goroutine leak suspected after %d abandoned Recv waits: baseline=%d now=%d", iters, baseline, runtime.NumGoroutine())
}

// gateRecvStream blocks in Recv until the caller closes entered, then waits on ctx
// or unblock. Tests can observe when the keepalive reader has entered inner.Recv.
type gateRecvStream struct {
	entered atomic.Bool
	unblock chan struct{}
}

func (g *gateRecvStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	g.entered.Store(true)
	select {
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	case <-g.unblock:
		return aipapi.Event{}, context.Canceled
	}
}

func (g *gateRecvStream) Close() error { return nil }

// Close must abort an in-flight inner Recv and wait for the reader goroutine so
// goleak sees zero survivors when tests tear down without draining the stream.
func TestKeepalive_closeReleasesReaderBlockedOnInnerRecv(t *testing.T) {
	t.Parallel()

	gate := &gateRecvStream{unblock: make(chan struct{})}
	ka := mustNewKeepalive(t, gate, stream.KeepaliveConfig{
		Interval: time.Hour,
	})

	recvDone := make(chan struct{})
	go func() {
		defer close(recvDone)
		ctx := context.Background()
		_, _ = ka.Recv(ctx)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for !gate.entered.Load() {
		if time.Now().After(deadline) {
			t.Fatal("reader never entered inner Recv")
		}
		runtime.Gosched()
	}

	if err := ka.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	close(gate.unblock)

	select {
	case <-recvDone:
	case <-time.After(time.Second):
		t.Fatal("outer Recv did not return after Close aborted inner Recv")
	}
}
