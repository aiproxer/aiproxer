package decodeqos_test

// Task 1.3 characterization: freeze decode-admission edge semantics not already
// covered by limiter_test.go (requirements 1, 6; design 1, 2, 8, 16).
//
// Reuses limiter_test.go for weight/saturation/overweight/cancel/Retry-After
// basics. This file adds only the gaps: nil TryAdmit unlimited, nil Guard
// release, DeadlineExceeded mapping, canceled-TryAdmit-to-Decide chain, and
// panic-release integration against a real Limiter (permit must not leak).

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/decodeqos"
)

func TestTryAdmit_nilAcquirerIsUnlimited(t *testing.T) {
	t.Parallel()
	release, ok, err := decodeqos.TryAdmit(context.Background(), nil, 1<<62)
	if err != nil || !ok || release == nil {
		t.Fatalf("TryAdmit(nil) = ok:%v err:%v release:%v, want unlimited ok", ok, err, release != nil)
	}
	release()
}

func TestGuard_nilReleaseRunsFn(t *testing.T) {
	t.Parallel()
	want := errors.New("decode failed")
	if err := decodeqos.Guard(nil, func() error { return want }); !errors.Is(err, want) {
		t.Fatalf("Guard(nil) err=%v want %v", err, want)
	}
	if err := decodeqos.Guard(nil, func() error { return nil }); err != nil {
		t.Fatalf("Guard(nil) ok err=%v", err)
	}
}

func TestHTTPStatus_deadlineExceededIs503WithoutRetryAfter(t *testing.T) {
	t.Parallel()
	status, retry := decodeqos.HTTPStatus(false, context.DeadlineExceeded)
	if status != http.StatusServiceUnavailable || retry {
		t.Fatalf("HTTPStatus(deadline) = (%d,%v), want (503,false)", status, retry)
	}
	d := decodeqos.Decide(false, context.DeadlineExceeded)
	if d.Status != http.StatusServiceUnavailable || d.RetryAfter {
		t.Fatalf("Decide(deadline) = %+v, want 503 without Retry-After", d)
	}
}

func TestTryAdmitThenDecide_canceledMapsTo503WithoutRetryAfter(t *testing.T) {
	t.Parallel()
	limiter := decodeqos.New(1, 1<<20)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	release, ok, err := decodeqos.TryAdmit(ctx, limiter, 1)
	if release != nil || ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("TryAdmit(canceled) = release:%v ok:%v err:%v, want context.Canceled", release != nil, ok, err)
	}
	d := decodeqos.Decide(ok, err)
	if d.Status != http.StatusServiceUnavailable || d.RetryAfter {
		t.Fatalf("Decide(canceled) = %+v, want 503 without Retry-After", d)
	}
}

func TestGuard_panicDoesNotLeakLimiterPermit(t *testing.T) {
	t.Parallel()
	limiter := decodeqos.New(1, 1024)
	release, ok, err := limiter.TryAcquire(context.Background(), 16)
	if err != nil || !ok {
		t.Fatalf("TryAcquire = ok:%v err:%v", ok, err)
	}
	func() {
		defer func() { _ = recover() }()
		_ = decodeqos.Guard(release, func() error { panic("decode panic") })
	}()
	// Permit must have been released exactly once by Guard despite the panic:
	// a fresh acquire at full capacity must succeed.
	second, ok, err := limiter.TryAcquire(context.Background(), 16)
	if err != nil || !ok {
		t.Fatalf("TryAcquire after Guard panic = ok:%v err:%v, want ok (permit leaked)", ok, err)
	}
	second()
}
