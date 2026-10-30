package holdalive_test

// Task 1 (large-payload-lifecycle-correctness): transfer-or-dispose ownership for
// holdalive.Wait. These tests pin Requirement 2 behavior: every produced result is
// either handed to the caller or disposed exactly once, including cancellation
// rendezvous, simultaneous readiness, nil/partial results, disposer failure, and
// informational-writer panic followed by a late result.

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/holdalive"
)

// ownableResult is a minimal owned value: Close models disposal.
type ownableResult struct {
	closed   atomic.Int32
	closeErr error
}

func (o *ownableResult) Close() error {
	o.closed.Add(1)
	return o.closeErr
}

func (o *ownableResult) closeCount() int32 {
	return o.closed.Load()
}

type quietOwnershipWriter struct {
	statuses []int
	flushed  int
}

func (w *quietOwnershipWriter) Header() http.Header         { return http.Header{} }
func (w *quietOwnershipWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *quietOwnershipWriter) WriteHeader(statusCode int) {
	w.statuses = append(w.statuses, statusCode)
}
func (w *quietOwnershipWriter) Flush() { w.flushed++ }

// countingDiscard closes a non-nil value and records every call without
// touching the response writer.
type countingDiscard struct {
	calls atomic.Int32
}

func (d *countingDiscard) fn() func(*ownableResult) {
	return func(v *ownableResult) {
		d.calls.Add(1)
		if v != nil {
			_ = v.Close()
		}
	}
}

func (d *countingDiscard) count() int32 {
	return d.calls.Load()
}

func TestLargePayloadLifecycle_HoldaliveTransferOwnership(t *testing.T) {
	t.Parallel()

	boom := errors.New("partial produce failure")
	tests := []struct {
		name   string
		result *ownableResult
		err    error
	}{
		{name: "success leaves ownership with caller", result: &ownableResult{}},
		{name: "value plus error transfers", result: &ownableResult{}, err: boom},
		{name: "nil result transfers"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			discard := &countingDiscard{}
			got, err := holdalive.Wait(context.Background(), &quietOwnershipWriter{}, holdalive.Config{
				Enabled:  true,
				Interval: time.Hour,
			}, func(context.Context) (*ownableResult, error) {
				return tt.result, tt.err
			}, discard.fn())
			if !errors.Is(err, tt.err) {
				t.Fatalf("err=%v want %v", err, tt.err)
			}
			if got != tt.result {
				t.Fatalf("transferred value mismatch: got %p want %p", got, tt.result)
			}
			if tt.result != nil && tt.result.closeCount() != 0 {
				t.Fatalf("transferred result was disposed: closes=%d", tt.result.closeCount())
			}
			if discard.count() != 0 {
				t.Fatalf("discard called on transferred result: calls=%d", discard.count())
			}
		})
	}
}

func TestLargePayloadLifecycle_HoldaliveCancelBeforeCompletionDisposesOnce(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	res := &ownableResult{}
	discard := &countingDiscard{}
	started := make(chan struct{})
	returned := make(chan error, 1)
	go func() {
		_, err := holdalive.Wait(ctx, &quietOwnershipWriter{}, holdalive.Config{
			Enabled:  true,
			Interval: time.Hour,
		}, func(ctx context.Context) (*ownableResult, error) {
			close(started)
			<-ctx.Done()
			<-release
			return res, nil
		}, discard.fn())
		returned <- err
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("producer did not start")
	}
	cancel()
	// The producer is still blocked on release, so cancellation must win the
	// rendezvous deterministically: no result is available to transfer.
	close(release)
	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Wait did not return after cancellation and producer completion")
	}
	if got := res.closeCount(); got != 1 {
		t.Fatalf("abandoned result closes=%d want exactly 1", got)
	}
	if got := discard.count(); got != 1 {
		t.Fatalf("discard calls=%d want exactly 1", got)
	}
}

func TestLargePayloadLifecycle_HoldaliveSimultaneousReadinessExactlyOnce(t *testing.T) {
	t.Parallel()
	// Cancellation and result readiness coincide: either the caller receives the
	// result (transfer) or the producer disposes it (abandonment) — exactly one
	// of the two must happen, never both and never neither.
	for i := range 50 {
		ctx, cancel := context.WithCancel(context.Background())
		release := make(chan struct{})
		res := &ownableResult{}
		discard := &countingDiscard{}
		type outcome struct {
			v   *ownableResult
			err error
		}
		got := make(chan outcome, 1)
		go func() {
			v, err := holdalive.Wait(ctx, &quietOwnershipWriter{}, holdalive.Config{
				Enabled:  true,
				Interval: time.Hour,
			}, func(context.Context) (*ownableResult, error) {
				<-release
				return res, nil
			}, discard.fn())
			got <- outcome{v: v, err: err}
		}()
		close(release)
		cancel()
		var o outcome
		select {
		case o = <-got:
		case <-time.After(10 * time.Second):
			cancel()
			t.Fatalf("iter %d: Wait did not return", i)
		}
		transferred := o.v == res && o.err == nil
		disposed := discard.count() == 1 && res.closeCount() == 1 && o.v == nil && errors.Is(o.err, context.Canceled)
		if transferred == disposed {
			t.Fatalf("iter %d: transferred=%v disposed=%v discardCalls=%d closes=%d err=%v",
				i, transferred, disposed, discard.count(), res.closeCount(), o.err)
		}
		cancel()
	}
}

func TestLargePayloadLifecycle_HoldaliveDisposerFailurePreservesCallerError(t *testing.T) {
	t.Parallel()
	cleanupErr := errors.New("owned close failed")
	cleanupSeen := make(chan error, 1)
	res := &ownableResult{closeErr: cleanupErr}
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	started := make(chan struct{})
	returned := make(chan error, 1)
	go func() {
		_, err := holdalive.Wait(ctx, &quietOwnershipWriter{}, holdalive.Config{
			Enabled:  true,
			Interval: time.Hour,
		}, func(ctx context.Context) (*ownableResult, error) {
			close(started)
			<-ctx.Done()
			<-release
			return res, nil
		}, func(v *ownableResult) {
			if v == nil {
				return
			}
			// Cleanup failure is reported separately; it must not replace the
			// caller's cancellation error or panic.
			if cerr := v.Close(); cerr != nil {
				cleanupSeen <- cerr
			}
		})
		returned <- err
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("producer did not start")
	}
	cancel()
	close(release)
	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Wait did not return after cancellation and producer completion")
	}
	select {
	case cerr := <-cleanupSeen:
		if !errors.Is(cerr, cleanupErr) {
			t.Fatalf("cleanup err=%v want %v", cerr, cleanupErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup failure was not reported separately")
	}
	if got := res.closeCount(); got != 1 {
		t.Fatalf("abandoned result closes=%d want exactly 1", got)
	}
}

// panicOwnershipWriter panics on the first informational write.
type panicOwnershipWriter struct {
	header  http.Header
	calls   atomic.Int32
	panicEr error
}

func (w *panicOwnershipWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *panicOwnershipWriter) Write(b []byte) (int, error) { return len(b), nil }

func (w *panicOwnershipWriter) WriteHeader(int) {
	w.calls.Add(1)
	panic(w.panicEr)
}

func (w *panicOwnershipWriter) Flush() { w.calls.Add(1) }

func TestLargePayloadLifecycle_HoldaliveWriterPanicLateResultDisposes(t *testing.T) {
	t.Parallel()

	partialErr := errors.New("late partial failure")
	for _, lateErr := range []error{nil, partialErr} {
		name := "late success"
		if lateErr != nil {
			name = "late partial"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sentinel := errors.New("informational writer panic")
			w := &panicOwnershipWriter{panicEr: sentinel}
			release := make(chan struct{})
			res := &ownableResult{}
			disposed := make(chan struct{}, 8)
			discard := func(v *ownableResult) {
				if v != nil {
					_ = v.Close()
				}
				select {
				case disposed <- struct{}{}:
				default:
				}
			}
			func() {
				defer func() {
					r := recover()
					if r != sentinel {
						t.Fatalf("panic=%v want sentinel %v", r, sentinel)
					}
				}()
				_, _ = holdalive.Wait(context.Background(), w, holdalive.Config{
					Enabled:  true,
					Interval: time.Millisecond,
				}, func(context.Context) (*ownableResult, error) {
					<-release
					return res, lateErr
				}, discard)
				t.Fatal("Wait returned instead of propagating the writer panic")
			}()
			// The caller is gone (panic propagated); the late result must still be
			// disposed without the canceled caller and without touching the writer.
			close(release)
			select {
			case <-disposed:
			case <-time.After(10 * time.Second):
				t.Fatal("late result was not disposed after writer panic")
			}
			if got := res.closeCount(); got != 1 {
				t.Fatalf("late result closes=%d want exactly 1", got)
			}
			if got := w.calls.Load(); got != 1 {
				t.Fatalf("abandoned writer accessed after panic: calls=%d want 1", got)
			}
		})
	}
}
