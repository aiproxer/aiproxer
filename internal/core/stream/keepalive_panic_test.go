package stream_test

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Unrelated HTTP requests after another request's isolated failure are covered at
// internal/stdhttp (e.g. TestStackHTTPHandler_recoveredPanicThenOK_metricsObserves5xx).

// panicRecvStream panics on every Recv with a distinctive string that must not
// appear in client-safe error text from the keepalive boundary.
type panicRecvStream struct{}

func (panicRecvStream) Recv(context.Context) (aipapi.Event, error) {
	panic("panic_recv_stream_secret_payload")
}

func (panicRecvStream) Close() error { return nil }

func TestKeepalive_innerRecvPanic_surfacesPanicError(t *testing.T) {
	t.Parallel()

	ka := mustNewKeepalive(t, panicRecvStream{}, stream.KeepaliveConfig{
		Interval: time.Hour,
	})
	defer func() { _ = ka.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := ka.Recv(ctx)
	if err == nil {
		t.Fatal("expected error from inner panic")
	}
	var pe *safety.PanicError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *safety.PanicError, got %T: %v", err, err)
	}
	if pe.Boundary() != safety.BoundaryWorker {
		t.Fatalf("boundary: got %q want %q", pe.Boundary(), safety.BoundaryWorker)
	}
	if pe.Operation() != "stream_keepalive_reader" {
		t.Fatalf("operation: got %q", pe.Operation())
	}
	msg := err.Error()
	if strings.Contains(msg, "panic_recv_stream_secret_payload") {
		t.Fatalf("Error() must not expose raw panic value: %q", msg)
	}
	if strings.Contains(msg, "goroutine") || strings.Contains(msg, "runtime.") {
		t.Fatalf("Error() must not look like a stack dump: %q", msg)
	}
}

func TestKeepalive_innerRecvPanic_subsequentRecvCanceled(t *testing.T) {
	t.Parallel()

	ka := mustNewKeepalive(t, panicRecvStream{}, stream.KeepaliveConfig{
		Interval: time.Hour,
	})
	defer func() { _ = ka.Close() }()

	ctx := context.Background()
	_, err := ka.Recv(ctx)
	if err == nil {
		t.Fatal("expected error from inner panic")
	}
	var pe *safety.PanicError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *safety.PanicError, got %v", err)
	}
	_ = pe

	_, err2 := ka.Recv(ctx)
	if !errors.Is(err2, context.Canceled) {
		t.Fatalf("second Recv: got %v want context.Canceled", err2)
	}
}

func TestKeepalive_innerRecvPanic_explicitCallerCancel_nextRecvHonorsCtx(t *testing.T) {
	t.Parallel()

	ka := mustNewKeepalive(t, panicRecvStream{}, stream.KeepaliveConfig{
		Interval: time.Hour,
	})
	defer func() { _ = ka.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	_, err := ka.Recv(ctx)
	if err == nil {
		t.Fatal("expected error from inner panic")
	}
	var pe *safety.PanicError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *safety.PanicError, got %v", err)
	}
	_ = pe

	cancel()
	_, err2 := ka.Recv(ctx)
	if !errors.Is(err2, context.Canceled) {
		t.Fatalf("after caller cancel post-panic: got %v want context.Canceled", err2)
	}
}

func TestKeepalive_innerRecvPanic_closeDoesNotLeakReader(t *testing.T) {
	t.Parallel()

	runtime.GC()
	baseline := runtime.NumGoroutine()

	ka := mustNewKeepalive(t, panicRecvStream{}, stream.KeepaliveConfig{
		Interval: time.Hour,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := ka.Recv(ctx)
	if err == nil {
		t.Fatal("expected error from inner panic")
	}
	if err := ka.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for range 300 {
		runtime.GC()
		if n := runtime.NumGoroutine(); n <= baseline+2 {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("goroutine leak suspected after keepalive reader panic + Close: baseline=%d now=%d", baseline, runtime.NumGoroutine())
}
