package frontendpipe_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/decodeqos"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
)

// Task 11.9: Decode-admission SATURATION RACE tests proving no second 429/503 decision
// on assessment decline fallback, and proving accept releases once before commit.
// Requirements 1, 6; design target-flow commit rule.

// TestCandidateAssessment_SaturationRace_ConcurrentDecline_SingleAdmissionPerRequest
// proves that under a capacity-constrained limiter, concurrent candidate requests that
// decline during AssessLargeBody each see strictly ONE TryAdmit decision (no fallback-induced
// second admission), admitted requests fall back to canonical Spec.Decode under the same permit
// and succeed (HTTP 200), saturated requests receive HTTP 429, and no request sees a second
// 429/503 decision.
func TestCandidateAssessment_SaturationRace_ConcurrentDecline_SingleAdmissionPerRequest(t *testing.T) {
	t.Parallel()

	const (
		maxConcurrent = 3
		numGoroutines = 20
		iterations    = 3
		payloadSize   = 1200 * 1024 // 1.2 MiB (> 1 MiB threshold)
	)

	payload := buildJSONPayload(payloadSize)

	for iter := range iterations {
		realLimiter := decodeqos.New(maxConcurrent, int64(maxConcurrent*payloadSize))
		limiter := newPerRequestTrackingLimiter(realLimiter)

		exec := &testAssessorExecutor{}
		var assessCalls atomic.Int64
		exec.assessFunc = func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
			assessCalls.Add(1)
			// Hold the permit until the limiter has actually rejected a competing
			// request. This forces contention while the limit is held instead of
			// assuming overlapping goroutine scheduling. The shared wait is bounded
			// and reports through t (no t.Fatal from this goroutine), so a limiter
			// regression cannot hang the suite.
			limiter.awaitFirstReject(t, saturationRejectWait)
			return largebody.NewDeclinedAssessment(largebody.DeclineReasonRouteIncompatible)
		}

		var decodeCalls atomic.Int64
		spec := newAssessmentTestSpec(
			exec,
			minimalValidProofProfile(),
			frontendpipe.LargePayloadConfig{
				Enabled:        true,
				ThresholdBytes: 1 << 20,
			},
			limiter,
			nil,
			nil,
			func(dctx frontendpipe.DecodeContext) {
				decodeCalls.Add(1)
			},
		)

		startBarrier := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		type reqResult struct {
			reqID      string
			statusCode int
			retryAfter string
		}
		results := make([]reqResult, numGoroutines)

		for i := range numGoroutines {
			go func(idx int) {
				defer wg.Done()
				reqID := fmt.Sprintf("iter-%d-cand-%03d", iter, idx)

				req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				ctx := context.WithValue(req.Context(), reqIDKey, reqID)
				req = req.WithContext(ctx)

				<-startBarrier

				rec := httptest.NewRecorder()
				frontendpipe.ServeHTTP(&spec, rec, req)

				results[idx] = reqResult{
					reqID:      reqID,
					statusCode: rec.Code,
					retryAfter: rec.Header().Get("Retry-After"),
				}
			}(i)
		}

		close(startBarrier)
		wg.Wait()

		var count200, count429 int
		for _, res := range results {
			rec := limiter.RecordFor(res.reqID)

			// Invariant 1: Strictly ONE TryAcquire call per request
			if rec.calls != 1 {
				t.Fatalf("[iter %d] request %s: expected strictly 1 TryAcquire call, got %d", iter, res.reqID, rec.calls)
			}

			switch res.statusCode {
			case http.StatusOK:
				count200++
				if !rec.admitted {
					t.Fatalf("[iter %d] request %s: 200 OK but limiter recorded not admitted", iter, res.reqID)
				}
				// Invariant 2: Admitted request releases permit exactly once at post-decode boundary
				if rec.releaseCount != 1 {
					t.Fatalf("[iter %d] request %s: expected exactly 1 release, got %d", iter, res.reqID, rec.releaseCount)
				}
			case http.StatusTooManyRequests:
				count429++
				if rec.admitted {
					t.Fatalf("[iter %d] request %s: 429 but limiter recorded admitted", iter, res.reqID)
				}
				if rec.releaseCount != 0 {
					t.Fatalf("[iter %d] request %s: 429 must not release, got %d", iter, res.reqID, rec.releaseCount)
				}
				if res.retryAfter != decodeqos.RetryAfterSeconds {
					t.Fatalf("[iter %d] request %s: expected Retry-After %q, got %q", iter, res.reqID, decodeqos.RetryAfterSeconds, res.retryAfter)
				}
			default:
				t.Fatalf("[iter %d] request %s: unexpected status code %d", iter, res.reqID, res.statusCode)
			}
		}

		if count200 == 0 {
			t.Fatalf("[iter %d] expected at least 1 admitted request, got 0", iter)
		}
		if count429 == 0 {
			t.Fatalf("[iter %d] expected at least 1 saturated request under concurrency, got 0", iter)
		}
		if count200+count429 != numGoroutines {
			t.Fatalf("[iter %d] total results: got %d (200: %d, 429: %d), want %d", iter, count200+count429, count200, count429, numGoroutines)
		}
		if maxCalls := limiter.MaxCallsPerRequest(); maxCalls != 1 {
			t.Fatalf("[iter %d] max TryAcquire calls per request: got %d, want 1", iter, maxCalls)
		}
		if total := limiter.TotalCalls(); total != int64(numGoroutines) {
			t.Fatalf("[iter %d] total TryAcquire calls: got %d, want %d", iter, total, numGoroutines)
		}
		if int64(count200) != decodeCalls.Load() {
			t.Fatalf("[iter %d] Spec.Decode calls (%d) must equal admitted requests (%d)", iter, decodeCalls.Load(), count200)
		}
		if exec.ExecuteLargeCallCount() != 0 {
			t.Fatalf("[iter %d] ExecuteLargeBody must NOT be called on decline, got %d", iter, exec.ExecuteLargeCallCount())
		}
	}
}

// TestCandidateAssessment_SaturationRace_ConcurrentAccept_SingleAdmissionAndSingleRelease
// proves that under concurrent requests that accept in AssessLargeBody:
// - Each admitted request releases the permit once before ExecuteLargeBody.
// - ExecuteLargeBody is called exactly once per admitted request.
// - Spec.Decode is NEVER called.
// - Every request sees strictly ONE TryAdmit decision.
func TestCandidateAssessment_SaturationRace_ConcurrentAccept_SingleAdmissionAndSingleRelease(t *testing.T) {
	t.Parallel()

	const (
		maxConcurrent = 3
		numGoroutines = 20
		iterations    = 3
		payloadSize   = 1200 * 1024
	)

	payload := buildJSONPayload(payloadSize)

	for iter := range iterations {
		realLimiter := decodeqos.New(maxConcurrent, int64(maxConcurrent*payloadSize))
		limiter := newPerRequestTrackingLimiter(realLimiter)

		exec := &testAssessorExecutor{}
		exec.assessFunc = func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
			// Synthetic delay while permit is held to ensure competing goroutines hit the capacity limiter
			time.Sleep(50 * time.Millisecond)
			return makeAcceptedAssessment(proof)
		}

		var executeLargeCalls atomic.Int64
		exec.executeLargeFunc = func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
			executeLargeCalls.Add(1)
			return largebody.ExecutionResult{Stream: &trackingCloserStream{}}, nil
		}

		var decodeCalls int64
		spec := newAssessmentTestSpec(
			exec,
			minimalValidProofProfile(),
			frontendpipe.LargePayloadConfig{
				Enabled:        true,
				ThresholdBytes: 1 << 20,
			},
			limiter,
			nil,
			nil,
			func(dctx frontendpipe.DecodeContext) {
				atomic.AddInt64(&decodeCalls, 1)
			},
		)

		startBarrier := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		type reqResult struct {
			reqID      string
			statusCode int
		}
		results := make([]reqResult, numGoroutines)

		for i := range numGoroutines {
			go func(idx int) {
				defer wg.Done()
				reqID := fmt.Sprintf("iter-%d-accept-%03d", iter, idx)

				req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				ctx := context.WithValue(req.Context(), reqIDKey, reqID)
				req = req.WithContext(ctx)

				<-startBarrier

				rec := httptest.NewRecorder()
				frontendpipe.ServeHTTP(&spec, rec, req)

				results[idx] = reqResult{
					reqID:      reqID,
					statusCode: rec.Code,
				}
			}(i)
		}

		close(startBarrier)
		wg.Wait()

		var count200, count429 int
		for _, res := range results {
			rec := limiter.RecordFor(res.reqID)

			if rec.calls != 1 {
				t.Fatalf("[iter %d] request %s: expected strictly 1 TryAcquire call, got %d", iter, res.reqID, rec.calls)
			}

			switch res.statusCode {
			case http.StatusOK:
				count200++
				if !rec.admitted {
					t.Fatalf("[iter %d] request %s: 200 OK but limiter recorded not admitted", iter, res.reqID)
				}
				if rec.releaseCount != 1 {
					t.Fatalf("[iter %d] request %s: expected exactly 1 release, got %d", iter, res.reqID, rec.releaseCount)
				}
			case http.StatusTooManyRequests:
				count429++
				if rec.admitted {
					t.Fatalf("[iter %d] request %s: 429 but limiter recorded admitted", iter, res.reqID)
				}
				if rec.releaseCount != 0 {
					t.Fatalf("[iter %d] request %s: 429 must not release, got %d", iter, res.reqID, rec.releaseCount)
				}
			default:
				t.Fatalf("[iter %d] request %s: unexpected status code %d", iter, res.reqID, res.statusCode)
			}
		}

		if count200 == 0 {
			t.Fatalf("[iter %d] expected at least 1 admitted request, got 0", iter)
		}
		if count429 == 0 {
			t.Fatalf("[iter %d] expected at least 1 saturated request, got 0", iter)
		}
		if maxCalls := limiter.MaxCallsPerRequest(); maxCalls != 1 {
			t.Fatalf("[iter %d] max TryAcquire calls per request: got %d, want 1", iter, maxCalls)
		}
		if total := limiter.TotalCalls(); total != int64(numGoroutines) {
			t.Fatalf("[iter %d] total TryAcquire calls: got %d, want %d", iter, total, numGoroutines)
		}
		// Spec.Decode must NEVER be called when assessment accepts
		if atomic.LoadInt64(&decodeCalls) != 0 {
			t.Fatalf("[iter %d] Spec.Decode must NEVER be called on accept, got %d", iter, decodeCalls)
		}
		// ExecuteLargeBody must be called once per admitted request
		if executeLargeCalls.Load() != int64(count200) {
			t.Fatalf("[iter %d] ExecuteLargeBody calls (%d) must equal admitted requests (%d)", iter, executeLargeCalls.Load(), count200)
		}
	}
}
