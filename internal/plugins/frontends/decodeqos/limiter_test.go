package decodeqos

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
)

func TestNew_unlimitedReturnsNil(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		conc  int
		bytes int64
	}{
		{"zero concurrent", 0, 1024},
		{"negative concurrent", -1, 1024},
		{"zero bytes", 1, 0},
		{"negative bytes", 1, -1},
		{"both zero", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := New(tc.conc, tc.bytes); got != nil {
				t.Fatalf("New(%d, %d) = %#v, want nil", tc.conc, tc.bytes, got)
			}
		})
	}
}

func TestLimiter_TryAcquire_nilLimiterIsNoop(t *testing.T) {
	t.Parallel()

	var l *Limiter
	release, ok, err := l.TryAcquire(context.Background(), 8)
	if err != nil || !ok {
		t.Fatalf("TryAcquire nil limiter = release:%v ok:%v err:%v, want ok", release != nil, ok, err)
	}
	release()
}

func TestLimiter_TryAcquire_capacityOneFailsFastUntilRelease(t *testing.T) {
	t.Parallel()

	l := New(1, math.MaxInt64)
	release, ok, err := l.TryAcquire(context.Background(), 0)
	if err != nil || !ok {
		t.Fatalf("first TryAcquire = ok:%v err:%v, want ok", ok, err)
	}

	_, ok, err = l.TryAcquire(context.Background(), 0)
	if err != nil || ok {
		t.Fatalf("second TryAcquire while saturated = ok:%v err:%v, want saturated without error", ok, err)
	}

	release()
	release, ok, err = l.TryAcquire(context.Background(), 0)
	if err != nil || !ok {
		t.Fatalf("TryAcquire after release = ok:%v err:%v, want ok", ok, err)
	}
	release()
}

func TestLimiter_TryAcquire_byteBudgetBlocksSecondHeavyRequest(t *testing.T) {
	t.Parallel()

	l := New(100, 12)
	releaseA, ok, err := l.TryAcquire(context.Background(), 8)
	if err != nil || !ok {
		t.Fatalf("first TryAcquire = ok:%v err:%v", ok, err)
	}
	defer releaseA()

	_, ok, err = l.TryAcquire(context.Background(), 8)
	if err != nil || ok {
		t.Fatalf("second weight-8 TryAcquire = ok:%v err:%v, want saturated", ok, err)
	}

	releaseA()
	releaseSmall, ok, err := l.TryAcquire(context.Background(), 4)
	if err != nil || !ok {
		t.Fatalf("small TryAcquire after release = ok:%v err:%v", ok, err)
	}
	releaseSmall()
}

func TestLimiter_TryAcquire_overweightReturnsErrOverweight(t *testing.T) {
	t.Parallel()

	l := New(10, 12)
	release, ok, err := l.TryAcquire(context.Background(), 13)
	if release != nil || ok || !errors.Is(err, ErrOverweight) {
		t.Fatalf("TryAcquire overweight = release:%v ok:%v err:%v, want ErrOverweight", release != nil, ok, err)
	}
}

func TestLimiter_TryAcquire_negativeWeightInvalid(t *testing.T) {
	t.Parallel()

	l := New(1, 8)
	release, ok, err := l.TryAcquire(context.Background(), -5)
	if release != nil || ok || !errors.Is(err, ErrInvalidWeight) {
		t.Fatalf("TryAcquire negative weight = release:%v ok:%v err:%v, want ErrInvalidWeight", release != nil, ok, err)
	}
}

func TestLimiter_TryAcquire_overflowSafeNearMaxInt64(t *testing.T) {
	t.Parallel()

	l := New(2, math.MaxInt64)
	release, ok, err := l.TryAcquire(context.Background(), math.MaxInt64)
	if err != nil || !ok {
		t.Fatalf("first TryAcquire MaxInt64 = ok:%v err:%v", ok, err)
	}
	defer release()

	_, ok, err = l.TryAcquire(context.Background(), 1)
	if err != nil || ok {
		t.Fatalf("second TryAcquire while byte budget full = ok:%v err:%v, want saturated", ok, err)
	}
}

func TestLimiter_TryAcquire_zeroWeightStillTakesConcurrencySlot(t *testing.T) {
	t.Parallel()

	l := New(1, 8)
	release, ok, err := l.TryAcquire(context.Background(), 0)
	if err != nil || !ok {
		t.Fatalf("first zero-weight TryAcquire = ok:%v err:%v", ok, err)
	}
	defer release()

	_, ok, err = l.TryAcquire(context.Background(), 0)
	if err != nil || ok {
		t.Fatalf("second zero-weight TryAcquire = ok:%v err:%v, want saturated", ok, err)
	}
}

func TestLimiter_TryAcquire_releaseOnlyOnce(t *testing.T) {
	t.Parallel()

	l := New(1, 8)
	release, ok, err := l.TryAcquire(context.Background(), 4)
	if err != nil || !ok {
		t.Fatalf("TryAcquire = ok:%v err:%v", ok, err)
	}
	release()
	release()

	release, ok, err = l.TryAcquire(context.Background(), 4)
	if err != nil || !ok {
		t.Fatalf("TryAcquire after double release = ok:%v err:%v", ok, err)
	}
	release()
}

func TestLimiter_TryAcquire_canceledContextReturnsPromptly(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	l := New(1, 8)
	release, ok, err := l.TryAcquire(ctx, 1)
	if release != nil || ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("TryAcquire canceled context = release:%v ok:%v err:%v, want context canceled", release != nil, ok, err)
	}
}

func TestLimiter_Acquire_preCanceledContextRejectsWhenCapacityAvailable(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	l := New(1, 8)
	release, err := l.Acquire(ctx, 1)
	if release != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire canceled context = release:%v err:%v, want context canceled", release != nil, err)
	}
}

func TestLimiter_Acquire_waitsUntilRelease(t *testing.T) {
	t.Parallel()

	l := New(1, 8)
	firstRelease, ok, err := l.TryAcquire(context.Background(), 4)
	if err != nil || !ok {
		t.Fatalf("TryAcquire = ok:%v err:%v", ok, err)
	}

	done := make(chan struct{})
	go func() {
		release, err := l.Acquire(context.Background(), 4)
		if err != nil {
			t.Errorf("Acquire: %v", err)
			close(done)
			return
		}
		release()
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("Acquire returned before first release")
	case <-time.After(20 * time.Millisecond):
	}

	firstRelease()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Acquire did not return after release")
	}
}

func TestLimiter_Acquire_respectsContextCancelWhileWaiting(t *testing.T) {
	t.Parallel()

	l := New(1, 8)
	release, ok, err := l.TryAcquire(context.Background(), 4)
	if err != nil || !ok {
		t.Fatalf("TryAcquire = ok:%v err:%v", ok, err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := l.Acquire(ctx, 4)
		done <- err
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Acquire err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Acquire did not return after context cancel")
	}
}

func TestGuard_releasesOnReturnAndPanic(t *testing.T) {
	t.Parallel()

	t.Run("return", func(t *testing.T) {
		t.Parallel()
		var released int
		err := Guard(func() { released++ }, func() error { return errors.New("decode failed") })
		if err == nil || released != 1 {
			t.Fatalf("err=%v released=%d, want error and one release", err, released)
		}
	})

	t.Run("panic", func(t *testing.T) {
		t.Parallel()
		var released int
		defer func() {
			_ = recover()
			if released != 1 {
				t.Fatalf("released=%d, want 1 after panic", released)
			}
		}()
		_ = Guard(func() { released++ }, func() error { panic("decode panic") })
	})
}

func TestHTTPStatus(t *testing.T) {
	t.Parallel()

	cases := []struct {
		ok         bool
		err        error
		wantStatus int
		wantRetry  bool
	}{
		{true, nil, 0, false},
		{false, nil, http.StatusTooManyRequests, true},
		{false, ErrOverweight, http.StatusTooManyRequests, true},
		{false, ErrInvalidWeight, http.StatusInternalServerError, false},
		{false, context.Canceled, http.StatusServiceUnavailable, false},
	}
	for _, tc := range cases {
		status, retry := HTTPStatus(tc.ok, tc.err)
		if status != tc.wantStatus || retry != tc.wantRetry {
			t.Fatalf("HTTPStatus(ok=%v err=%v) = (%d,%v), want (%d,%v)", tc.ok, tc.err, status, retry, tc.wantStatus, tc.wantRetry)
		}
	}
}

func TestDecide(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		ok          bool
		err         error
		wantStatus  int
		wantRetry   bool
		wantMessage string
	}{
		{name: "ok", ok: true, wantStatus: 0},
		{name: "saturated", ok: false, wantStatus: http.StatusTooManyRequests, wantRetry: true, wantMessage: AdmissionRejectedWireMessage},
		{name: "overweight", ok: false, err: ErrOverweight, wantStatus: http.StatusTooManyRequests, wantRetry: true, wantMessage: AdmissionRejectedWireMessage},
		{name: "invalid_weight", ok: false, err: ErrInvalidWeight, wantStatus: http.StatusInternalServerError, wantMessage: execerr.InternalWireMessage},
		{name: "canceled", ok: false, err: context.Canceled, wantStatus: http.StatusServiceUnavailable, wantMessage: execerr.InternalWireMessage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := Decide(tc.ok, tc.err)
			if d.Status != tc.wantStatus || d.RetryAfter != tc.wantRetry || d.Message != tc.wantMessage {
				t.Fatalf("Decide = %+v, want status=%d retry=%v msg=%q", d, tc.wantStatus, tc.wantRetry, tc.wantMessage)
			}
		})
	}
}

func BenchmarkLimiter_TryAcquire_manySmallAdmits(b *testing.B) {
	l := New(32, 64<<20)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			release, ok, err := l.TryAcquire(context.Background(), 64)
			if err != nil || !ok {
				b.Fatalf("TryAcquire = ok:%v err:%v", ok, err)
			}
			release()
		}
	})
}

func BenchmarkLimiter_TryAcquire_fewLargeAdmits(b *testing.B) {
	l := New(8, 64<<20)
	b.ReportAllocs()
	for b.Loop() {
		release, ok, err := l.TryAcquire(context.Background(), 8<<20)
		if err != nil || !ok {
			b.Fatalf("TryAcquire = ok:%v err:%v", ok, err)
		}
		release()
	}
}

func TestLimiter_concurrentTryAcquireWithinBudget(t *testing.T) {
	t.Parallel()

	l := New(8, 64)
	var (
		wg       sync.WaitGroup
		admitted atomic.Int64
		failures atomic.Int64
	)
	for range 32 {
		wg.Go(func() {
			release, ok, err := l.TryAcquire(context.Background(), 4)
			if err != nil {
				failures.Add(1)
				return
			}
			if !ok {
				return
			}
			admitted.Add(1)
			release()
		})
	}
	wg.Wait()
	if failures.Load() != 0 {
		t.Fatalf("unexpected TryAcquire errors: %d", failures.Load())
	}
	if admitted.Load() == 0 {
		t.Fatal("expected at least one successful admission")
	}
}
