package backendplugin_test

import (
	"context"
	"errors"
	"io"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

type blockingCancelManaged struct {
	recvEntered      atomic.Bool
	cancelCalled     atomic.Bool
	cancelCtxExpired atomic.Bool
	cancelCause      aipapi.CancelCause
	unblockRecv      chan struct{}
}

func (m *blockingCancelManaged) Recv(ctx context.Context) (aipapi.Event, error) {
	m.recvEntered.Store(true)
	select {
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	case <-m.unblockRecv:
		return aipapi.Event{}, io.EOF
	}
}

func (m *blockingCancelManaged) Close() error { return nil }

func (m *blockingCancelManaged) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	m.cancelCalled.Store(true)
	m.cancelCause = cause
	select {
	case <-ctx.Done():
		m.cancelCtxExpired.Store(true)
	case <-time.After(10 * time.Second):
	}
	return aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
}

//nolint:paralleltest // mutates package-level fallbackCancelGrace and inspects NumGoroutine
func TestForwardExecute_FallbackCancel_BoundedGraceAndNonBlocking(t *testing.T) {
	restore := backendplugin.SetFallbackCancelGraceForTest(200 * time.Millisecond)
	t.Cleanup(restore)

	runtime.GC()
	baselineGoroutines := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	stream := newNegotiatedChannelExecuteStream(ctx)
	stream.inFrames <- validStartFrame(t)

	ms := &blockingCancelManaged{
		unblockRecv: make(chan struct{}),
	}

	start := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
			return ms, nil
		})
	}()

	waitUntil(t, time.Second, func() bool { return ms.recvEntered.Load() })

	// Cancel external stream context
	cancel()

	select {
	case err := <-done:
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("expected ForwardExecute to return error on stream cancel, got nil")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ForwardExecute returned error %v, want context.Canceled", err)
		}
		if elapsed > 1500*time.Millisecond {
			t.Fatalf("ForwardExecute took %v, exceeded grace + margin", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ForwardExecute hung or exceeded deadline after stream context cancel")
	}

	if !ms.cancelCalled.Load() {
		t.Fatal("upstream ManagedEventStream.Cancel was not called")
	}
	if !ms.cancelCtxExpired.Load() {
		t.Fatal("upstream ManagedEventStream.Cancel context did not expire (passed unbounded context)")
	}

	var leakSuccess bool
	for range 100 {
		runtime.GC()
		if n := runtime.NumGoroutine(); n <= baselineGoroutines+2 {
			leakSuccess = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !leakSuccess {
		t.Fatalf("goroutine leak suspected: baseline=%d now=%d", baselineGoroutines, runtime.NumGoroutine())
	}
}

//nolint:paralleltest // mutates package-level fallbackCancelGrace
func TestForwardExecute_Legacy_FallbackCancel_BoundedGrace(t *testing.T) {
	restore := backendplugin.SetFallbackCancelGraceForTest(200 * time.Millisecond)
	t.Cleanup(restore)

	ctx, cancel := context.WithCancel(context.Background())
	stream := newFakeExecuteStream(ctx, validStartFrame(t))

	ms := &blockingCancelManaged{
		unblockRecv: make(chan struct{}),
	}

	done := make(chan error, 1)
	go func() {
		done <- backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
			return ms, nil
		})
	}()

	waitUntil(t, time.Second, func() bool { return ms.recvEntered.Load() })

	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected ForwardExecute to return error on stream cancel, got nil")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ForwardExecute returned %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ForwardExecute legacy hung after stream cancel")
	}

	// Wait for legacy watcher to execute Cancel and its bounded context to expire
	waitUntil(t, time.Second, func() bool { return ms.cancelCtxExpired.Load() })
}

//nolint:paralleltest // mutates package-level fallbackCancelGrace
func TestForwardExecute_FallbackCancel_ControlReaderDisconnect(t *testing.T) {
	restore := backendplugin.SetFallbackCancelGraceForTest(200 * time.Millisecond)
	t.Cleanup(restore)

	ctx, cancel := context.WithCancel(context.Background())
	stream := newNegotiatedChannelExecuteStream(ctx)
	stream.inFrames <- validStartFrame(t)

	ms := &blockingCancelManaged{
		unblockRecv: make(chan struct{}),
	}

	start := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
			return ms, nil
		})
	}()

	waitUntil(t, time.Second, func() bool { return ms.recvEntered.Load() })

	// Cancel context and close stream to trigger control reader error
	cancel()
	_ = stream.Close()

	select {
	case err := <-done:
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("expected ForwardExecute to return error on disconnect, got nil")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ForwardExecute returned %v, want context.Canceled", err)
		}
		if elapsed > 1500*time.Millisecond {
			t.Fatalf("ForwardExecute took %v, exceeded grace + margin", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ForwardExecute hung after control reader disconnect")
	}

	if !ms.cancelCalled.Load() {
		t.Fatal("upstream ManagedEventStream.Cancel was not called on disconnect")
	}
	if !ms.cancelCtxExpired.Load() {
		t.Fatal("upstream ManagedEventStream.Cancel context did not expire")
	}
}
