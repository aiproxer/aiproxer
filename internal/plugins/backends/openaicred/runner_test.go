package openaicred_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicred"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3"
)

func TestClassifyHTTPStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    int
		header    http.Header
		wantKind  openaicred.FailureKind
		wantRetry string
	}{
		{
			name:     "unauthorized",
			status:   http.StatusUnauthorized,
			wantKind: openaicred.FailureAuthInvalid,
		},
		{
			name:      "rate_limited_with_retry_after",
			status:    http.StatusTooManyRequests,
			header:    http.Header{"Retry-After": []string{"45"}},
			wantKind:  openaicred.FailureRateLimited,
			wantRetry: "45",
		},
		{
			name:     "rate_limited_without_retry_after",
			status:   http.StatusTooManyRequests,
			wantKind: openaicred.FailureRateLimited,
		},
		{
			name:     "request_timeout",
			status:   http.StatusRequestTimeout,
			wantKind: openaicred.FailureRetryable,
		},
		{
			name:     "internal_server_error",
			status:   http.StatusInternalServerError,
			wantKind: openaicred.FailureRetryable,
		},
		{
			name:     "bad_gateway",
			status:   http.StatusBadGateway,
			wantKind: openaicred.FailureRetryable,
		},
		{
			name:     "bad_request",
			status:   http.StatusBadRequest,
			wantKind: openaicred.FailureNone,
		},
		{
			name:     "forbidden_is_not_rotation_evidence",
			status:   http.StatusForbidden,
			wantKind: openaicred.FailureNone,
		},
		{
			name:     "payment_required_is_not_rotation_evidence",
			status:   http.StatusPaymentRequired,
			wantKind: openaicred.FailureNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			k, ra := openaicred.ClassifyHTTPStatus(tt.status, tt.header)
			if k != tt.wantKind || ra != tt.wantRetry {
				t.Fatalf("got kind=%v retryAfter=%q, want kind=%v retryAfter=%q", k, ra, tt.wantKind, tt.wantRetry)
			}
		})
	}
}

func TestClassifyHTTPResponse(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	rec.Header().Set("Retry-After", "30")
	rec.WriteHeader(http.StatusTooManyRequests)
	resp := rec.Result()

	k, ra := openaicred.ClassifyHTTPResponse(resp)
	if k != openaicred.FailureRateLimited || ra != "30" {
		t.Fatalf("got kind=%v retryAfter=%q, want kind=%v retryAfter=30", k, ra, openaicred.FailureRateLimited)
	}

	if k, _ := openaicred.ClassifyHTTPResponse(nil); k != openaicred.FailureNone {
		t.Fatalf("nil resp: got %v, want FailureNone", k)
	}
}

func TestExecuteWithCredentialPool_NilPool(t *testing.T) {
	t.Parallel()

	called := false
	stream, err := openaicred.ExecuteWithCredentialPool(
		context.Background(),
		"test-provider",
		nil,
		time.Minute,
		func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
			called = true
			if cred.Secret != "" {
				t.Fatalf("expected empty secret for nil pool, got %q", cred.Secret)
			}
			return aipapi.NewFixedEventStream(nil), nil
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called || stream == nil {
		t.Fatalf("expected called=true and non-nil stream")
	}
}

func TestExecuteWithCredentialPool_ContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	pool, err := credpool.New([]credpool.Credential{{ID: "k1", Secret: "s1"}})
	if err != nil {
		t.Fatal(err)
	}

	_, err = openaicred.ExecuteWithCredentialPool(
		ctx,
		"test-provider",
		pool,
		time.Minute,
		func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
			t.Fatal("attempt must not be called on canceled context")
			return nil, nil
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestExecuteWithCredentialPool_ShortThrottleDoesNotRotate(t *testing.T) {
	t.Parallel()

	pool, err := credpool.New([]credpool.Credential{
		{ID: "k1", Secret: "s1"},
		{ID: "k2", Secret: "s2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	attempts := 0
	_, err = openaicred.ExecuteWithCredentialPool(
		context.Background(),
		"test-provider",
		pool,
		time.Hour,
		func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
			attempts++
			rec := httptest.NewRecorder()
			rec.Header().Set("Retry-After", "30")
			return nil, &openai.Error{
				StatusCode: http.StatusTooManyRequests,
				Response:   rec.Result(),
			}
		},
	)
	if err == nil {
		t.Fatal("expected short-throttle error")
	}
	if attempts != 1 {
		t.Fatalf("short 429 must not consume a second credential: attempts=%d", attempts)
	}
	snap := pool.Snapshot(time.Now())
	if len(snap) != 2 || snap[0].ID != "k1" || snap[0].State != credpool.StateUsable {
		t.Fatalf("primary must stay usable after a 30s throttle: %+v", snap)
	}
}

func TestExecuteWithCredentialPool_RateLimitCooldownRotation(t *testing.T) {
	t.Parallel()

	pool, err := credpool.New([]credpool.Credential{
		{ID: "k1", Secret: "s1"},
		{ID: "k2", Secret: "s2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	attempts := 0
	stream, err := openaicred.ExecuteWithCredentialPool(
		context.Background(),
		"test-provider",
		pool,
		time.Hour,
		func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
			attempts++
			if attempts == 1 {
				if cred.Secret != "s1" {
					t.Fatalf("first attempt expected s1, got %q", cred.Secret)
				}
				rec := httptest.NewRecorder()
				rec.Header().Set("Retry-After", "7200")
				return nil, &openai.Error{
					StatusCode: http.StatusTooManyRequests,
					Response:   rec.Result(),
				}
			}
			if cred.Secret != "s2" {
				t.Fatalf("second attempt expected s2, got %q", cred.Secret)
			}
			return aipapi.NewFixedEventStream(nil), nil
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stream == nil || attempts != 2 {
		t.Fatalf("expected 2 attempts and non-nil stream, got attempts=%d", attempts)
	}
}

func TestExecuteWithCredentialPool_AuthInvalidRotation(t *testing.T) {
	t.Parallel()

	pool, err := credpool.New([]credpool.Credential{
		{ID: "k1", Secret: "s1"},
		{ID: "k2", Secret: "s2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	attempts := 0
	stream, err := openaicred.ExecuteWithCredentialPool(
		context.Background(),
		"test-provider",
		pool,
		time.Minute,
		func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
			attempts++
			if attempts == 1 {
				return nil, &openai.Error{
					StatusCode: http.StatusUnauthorized,
					Response:   &http.Response{StatusCode: http.StatusUnauthorized},
				}
			}
			return aipapi.NewFixedEventStream(nil), nil
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stream == nil || attempts != 2 {
		t.Fatalf("expected 2 attempts and non-nil stream, got attempts=%d", attempts)
	}
}

func TestExecuteWithCredentialPool_RetryableReturnsPreOutputError(t *testing.T) {
	t.Parallel()

	pool, err := credpool.New([]credpool.Credential{{ID: "k1", Secret: "s1"}})
	if err != nil {
		t.Fatal(err)
	}

	serverErr := &openai.Error{
		StatusCode: http.StatusInternalServerError,
		Response:   &http.Response{StatusCode: http.StatusInternalServerError},
	}
	_, err = openaicred.ExecuteWithCredentialPool(
		context.Background(),
		"test-provider",
		pool,
		time.Minute,
		func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
			return nil, serverErr
		},
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("expected aipapi.IsRecoverablePreOutput(err) to be true, got %v", err)
	}
}

func TestExecuteWithCredentialPool_ExhaustedReturnsPreOutputError(t *testing.T) {
	t.Parallel()

	pool, err := credpool.New([]credpool.Credential{{ID: "k1", Secret: "s1"}})
	if err != nil {
		t.Fatal(err)
	}
	pool.MarkAuthInvalid("k1")

	_, err = openaicred.ExecuteWithCredentialPool(
		context.Background(),
		"test-provider",
		pool,
		time.Minute,
		func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
			t.Fatal("attempt must not be called when pool exhausted")
			return nil, nil
		},
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("expected aipapi.IsRecoverablePreOutput(err) to be true on exhausted pool, got %v", err)
	}
}

func TestExecuteWithCredentialPool_MissingHintDoesNotRotate(t *testing.T) {
	t.Parallel()

	pool, err := credpool.New([]credpool.Credential{
		{ID: "k1", Secret: "s1"},
		{ID: "k2", Secret: "s2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	attempts := 0
	_, err = openaicred.ExecuteWithCredentialPool(
		context.Background(),
		"test-provider",
		pool,
		time.Hour,
		func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
			attempts++
			return nil, &openai.Error{
				StatusCode: http.StatusTooManyRequests,
				Response:   &http.Response{StatusCode: http.StatusTooManyRequests},
			}
		},
	)
	if err == nil {
		t.Fatal("expected throttle error")
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("missing-hint throttle must be pre-output recoverable, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("missing hint must not rotate: attempts=%d", attempts)
	}
}

func TestExecuteWithCredentialPool_ExactThresholdDoesNotRotate(t *testing.T) {
	t.Parallel()

	pool, err := credpool.New([]credpool.Credential{
		{ID: "k1", Secret: "s1"},
		{ID: "k2", Secret: "s2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	attempts := 0
	_, err = openaicred.ExecuteWithCredentialPool(
		context.Background(),
		"test-provider",
		pool,
		time.Hour,
		func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
			attempts++
			rec := httptest.NewRecorder()
			rec.Header().Set("Retry-After", "3600")
			return nil, &openai.Error{
				StatusCode: http.StatusTooManyRequests,
				Response:   rec.Result(),
			}
		},
	)
	if err == nil {
		t.Fatal("expected throttle error")
	}
	if attempts != 1 {
		t.Fatalf("at-threshold hint is not strictly greater: attempts=%d", attempts)
	}
}

func TestExecuteWithCredentialPool_ForbiddenReturnsOriginalError(t *testing.T) {
	t.Parallel()

	pool, err := credpool.New([]credpool.Credential{
		{ID: "k1", Secret: "s1"},
		{ID: "k2", Secret: "s2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	attempts := 0
	forbidden := &openai.Error{
		StatusCode: http.StatusForbidden,
		Response:   &http.Response{StatusCode: http.StatusForbidden},
	}
	_, err = openaicred.ExecuteWithCredentialPool(
		context.Background(),
		"test-provider",
		pool,
		time.Hour,
		func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
			attempts++
			return nil, forbidden
		},
	)
	if !errors.Is(err, forbidden) {
		t.Fatalf("403 must return the original error, got %v", err)
	}
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("403 must not be a pre-output rotation signal: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("403 must not rotate: attempts=%d", attempts)
	}
}

func TestExecuteWithCredentialPool_BoundedAttemptsOnRepeatedAuthInvalid(t *testing.T) {
	t.Parallel()

	pool, err := credpool.New([]credpool.Credential{
		{ID: "k1", Secret: "s1"},
		{ID: "k2", Secret: "s2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	attempts := 0
	_, err = openaicred.ExecuteWithCredentialPool(
		context.Background(),
		"test-provider",
		pool,
		time.Hour,
		func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
			attempts++
			return nil, &openai.Error{
				StatusCode: http.StatusUnauthorized,
				Response:   &http.Response{StatusCode: http.StatusUnauthorized},
			}
		},
	)
	if err == nil || !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("drained pool must be pre-output recoverable, got %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts bounded by credentials: attempts=%d", attempts)
	}
}

func TestExecuteWithCredentialPool_ConcurrentShortThrottlePinsPrimary(t *testing.T) {
	t.Parallel()

	pool, err := credpool.New([]credpool.Credential{
		{ID: "k1", Secret: "s1"},
		{ID: "k2", Secret: "s2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, runErr := openaicred.ExecuteWithCredentialPool(
				context.Background(),
				"test-provider",
				pool,
				time.Hour,
				func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
					mu.Lock()
					seen[cred.ID]++
					mu.Unlock()
					rec := httptest.NewRecorder()
					rec.Header().Set("Retry-After", "30")
					return nil, &openai.Error{
						StatusCode: http.StatusTooManyRequests,
						Response:   rec.Result(),
					}
				},
			)
			if runErr == nil || !aipapi.IsRecoverablePreOutput(runErr) {
				t.Errorf("short throttle must be pre-output recoverable, got %v", runErr)
			}
		})
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if seen["k2"] != 0 || seen["k1"] != 8 {
		t.Fatalf("concurrent short throttles must pin primary: %v", seen)
	}
}

func TestExecuteWithCredentialPool_ReportsSelectedCredentialIdentity(t *testing.T) {
	t.Parallel()
	pool, err := credpool.New([]credpool.Credential{{ID: "k1", Secret: "s1"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, collector := execbackend.WithAttemptIdentityCollector(context.Background())
	if _, err := openaicred.ExecuteWithCredentialPool(ctx, "test-provider", pool, time.Hour, func(context.Context, credpool.Credential) (aipapi.ManagedEventStream, error) {
		return aipapi.NewFixedEventStream(nil), nil
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := collector.Identity().Credential; got == "" {
		t.Fatal("selected credential identity was not reported")
	}
}

func TestExecuteWithCredentialPool_DoesNotReportFailedAttemptIdentity(t *testing.T) {
	t.Parallel()
	pool, err := credpool.New([]credpool.Credential{{ID: "k1", Secret: "s1"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, collector := execbackend.WithAttemptIdentityCollector(context.Background())
	if _, err := openaicred.ExecuteWithCredentialPool(ctx, "test-provider", pool, time.Hour, func(context.Context, credpool.Credential) (aipapi.ManagedEventStream, error) {
		return nil, errors.New("fatal")
	}); err == nil {
		t.Fatal("expected error")
	}
	if got := collector.Identity().Credential; got != "" {
		t.Fatalf("failed attempt reported identity %q", got)
	}
}
