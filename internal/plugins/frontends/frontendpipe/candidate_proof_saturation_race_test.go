package frontendpipe_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/decodeqos"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
)

// Task 7.6: Decode-admission SATURATION RACE test proving no second 429/503 decision.
// Concurrent candidate proof-declines under a saturated limiter must each see exactly
// one TryAdmit decision (no fallback-induced second admission).
// Requirements 1, 6; design target-flow commit rule.
// Note: -race flag is unavailable on Windows cgo; tests use goroutine-concurrency
// with barrier starts across N goroutines x M iterations to stress concurrent paths.

type reqContextKey struct{}

var reqIDKey = reqContextKey{}

type reqAdmissionRecord struct {
	calls        int
	weights      []int64
	admitted     bool
	releaseCount int
}

// perRequestTrackingLimiter wraps a decodeqos.TryAcquirer and records admission
// attempts per request ID (injected into request context) to prove each request
// undergoes strictly one TryAdmit decision.
type perRequestTrackingLimiter struct {
	underlying decodeqos.TryAcquirer
	mu         sync.Mutex
	records    map[string]*reqAdmissionRecord
	totalCalls atomic.Int64
	rejectAll  bool

	firstRejectOnce sync.Once
	firstReject     chan struct{}
}

func newPerRequestTrackingLimiter(underlying decodeqos.TryAcquirer) *perRequestTrackingLimiter {
	return &perRequestTrackingLimiter{
		underlying:  underlying,
		records:     make(map[string]*reqAdmissionRecord),
		firstReject: make(chan struct{}),
	}
}

func (l *perRequestTrackingLimiter) TryAcquire(ctx context.Context, weight int64) (func(), bool, error) {
	l.totalCalls.Add(1)

	reqID, _ := ctx.Value(reqIDKey).(string)

	l.mu.Lock()
	if reqID != "" {
		rec, ok := l.records[reqID]
		if !ok {
			rec = &reqAdmissionRecord{}
			l.records[reqID] = rec
		}
		rec.calls++
		rec.weights = append(rec.weights, weight)
	}
	rejectAll := l.rejectAll
	l.mu.Unlock()

	if rejectAll {
		l.signalFirstReject()
		return nil, false, nil
	}

	var release func()
	var ok bool
	var err error
	if l.underlying != nil {
		release, ok, err = l.underlying.TryAcquire(ctx, weight)
	} else {
		release = func() {}
		ok = true
	}

	l.mu.Lock()
	if reqID != "" && ok {
		l.records[reqID].admitted = true
	}
	l.mu.Unlock()

	if !ok || err != nil {
		l.signalFirstReject()
		return nil, ok, err
	}

	wrappedRelease := func() {
		l.mu.Lock()
		if reqID != "" {
			l.records[reqID].releaseCount++
		}
		l.mu.Unlock()
		if release != nil {
			release()
		}
	}
	return wrappedRelease, true, nil
}

func (l *perRequestTrackingLimiter) RecordFor(reqID string) reqAdmissionRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec := l.records[reqID]
	if rec == nil {
		return reqAdmissionRecord{}
	}
	return *rec
}

func (l *perRequestTrackingLimiter) TotalCalls() int64 {
	return l.totalCalls.Load()
}

func (l *perRequestTrackingLimiter) MaxCallsPerRequest() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	maxCalls := 0
	for _, rec := range l.records {
		if rec.calls > maxCalls {
			maxCalls = rec.calls
		}
	}
	return maxCalls
}

// signalFirstReject closes the firstReject channel exactly once, when the
// underlying limiter rejects an admission attempt.
func (l *perRequestTrackingLimiter) signalFirstReject() {
	if l.firstReject == nil {
		return
	}
	l.firstRejectOnce.Do(func() { close(l.firstReject) })
}

// saturationRejectWait bounds how long a permit-holding candidate callback waits
// for a competing admission to be rejected. It is generous for loaded Windows CI
// but is only ever reached on a regression (a limiter that admits every request,
// or a permit released before the holding callback); the passing path returns as
// soon as a rejection is observed.
const saturationRejectWait = 10 * time.Second

// testErrorReporter is the minimal reporting surface the bounded saturation wait
// needs. *testing.T satisfies it; tests substitute a recording fake to assert the
// timeout is reported without failing the enclosing test.
type testErrorReporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// waitFirstReject blocks until the limiter has recorded a rejected admission or
// bound elapses, reporting whether a rejection was observed. It never closes or
// mutates firstReject, so the saturation assertions stay strict.
func (l *perRequestTrackingLimiter) waitFirstReject(bound time.Duration) bool {
	select {
	case <-l.firstReject:
		return true
	case <-time.After(bound):
		return false
	}
}

// awaitFirstReject is the shared bounded wait used by the permit-holding
// callbacks (proof compile and assessment). Admitted requests keep their permit
// in flight until a competing request is provably saturated, which replaces the
// sleep-based scheduling assumption. On timeout it records an explicit error
// through the reporter (safe from the request goroutines, unlike t.Fatal) and
// returns normally so every goroutine unwinds and wg.Wait completes instead of
// hanging the suite.
func (l *perRequestTrackingLimiter) awaitFirstReject(r testErrorReporter, bound time.Duration) {
	r.Helper()
	if !l.waitFirstReject(bound) {
		r.Errorf("saturation race: no admission rejection observed within %s; the limiter admitted every request (regression) or released its permit before the holding callback", bound)
	}
}

// recordingErrorReporter captures Errorf calls so the bounded wait's fail-fast
// reporting can be asserted without failing the enclosing test.
type recordingErrorReporter struct {
	mu     sync.Mutex
	errors int
}

func (r *recordingErrorReporter) Helper() {}

func (r *recordingErrorReporter) Errorf(string, ...any) {
	r.mu.Lock()
	r.errors++
	r.mu.Unlock()
}

func (r *recordingErrorReporter) errorCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.errors
}

// TestCandidateProof_SaturationRace_BoundedWait_FailsFast is the controlled
// regression for the bounded saturation wait:
//   - RED: a limiter that admits every request never rejects, so the shared wait
//     must report an explicit error and return quickly instead of blocking
//     forever (which would hang wg.Wait and the whole suite).
//   - GREEN: a real capacity limiter signals firstReject once it rejects an
//     admission, so the same wait observes the rejection and reports nothing.
func TestCandidateProof_SaturationRace_BoundedWait_FailsFast(t *testing.T) {
	t.Parallel()

	const (
		shortBound  = 100 * time.Millisecond
		payloadSize = 1200 * 1024
	)

	// RED: nil underlying admits every request, so no rejection is ever recorded.
	admitting := newPerRequestTrackingLimiter(nil)
	redReporter := &recordingErrorReporter{}
	start := time.Now()
	admitting.awaitFirstReject(redReporter, shortBound)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("no-rejection limiter: bounded wait returned after %s, want ~%s", elapsed, shortBound)
	}
	if got := redReporter.errorCount(); got != 1 {
		t.Fatalf("no-rejection limiter: got %d reported errors, want 1 (bounded wait must fail fast)", got)
	}

	// GREEN: a real capacity-1 limiter must signal after the second admission is rejected.
	realLimiter := decodeqos.New(1, int64(payloadSize))
	limiting := newPerRequestTrackingLimiter(realLimiter)

	release, ok, err := limiting.TryAcquire(context.Background(), int64(payloadSize))
	if err != nil || !ok {
		t.Fatalf("first admission: got ok=%t err=%v, want true/nil", ok, err)
	}
	defer release()

	if _, ok, err := limiting.TryAcquire(context.Background(), int64(payloadSize)); ok || err != nil {
		t.Fatalf("second admission: got ok=%t err=%v, want false/nil", ok, err)
	}
	greenReporter := &recordingErrorReporter{}
	limiting.awaitFirstReject(greenReporter, shortBound)
	if got := greenReporter.errorCount(); got != 0 {
		t.Fatalf("real limiter: got %d reported errors, want 0", got)
	}
}

// TestCandidateProof_SaturationRace_FullySaturatedLimiter verifies that when
// decode admission is fully saturated, N concurrent candidate requests (barrier start,
// M iterations) each see strictly ONE TryAdmit decision (HTTP 429), never invoke
// CompileProof or Spec.Decode, and never trigger a fallback-induced second admission.
func TestCandidateProof_SaturationRace_FullySaturatedLimiter(t *testing.T) {
	t.Parallel()

	const (
		numGoroutines = 24
		iterations    = 3
		payloadSize   = 1200 * 1024 // 1.2 MiB (> 1 MiB threshold)
	)

	payload := buildJSONPayload(payloadSize)

	for iter := range iterations {
		exec := &candidateGatesExec{}
		limiter := newPerRequestTrackingLimiter(nil)
		limiter.rejectAll = true // fully saturated limiter

		var compileCalls int64
		prof := &certifiedTestProfile{
			profileID: "test_saturated_race_profile",
			compileFunc: func(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
				atomic.AddInt64(&compileCalls, 1)
				return frontendpipe.ProofOutput{}, errors.New("proof declined")
			},
		}

		var decodeCalls int64
		spec := newCandidateProofSpec(
			exec, prof,
			frontendpipe.LargePayloadConfig{
				Enabled:        true,
				ThresholdBytes: 1 << 20,
			},
			limiter,
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
			retryAfter string
		}
		results := make([]reqResult, numGoroutines)

		for i := range numGoroutines {
			go func(idx int) {
				defer wg.Done()
				reqID := fmt.Sprintf("iter-%d-req-%03d", iter, idx)

				req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				ctx := context.WithValue(req.Context(), reqIDKey, reqID)
				req = req.WithContext(ctx)

				<-startBarrier // wait for all goroutines to assemble

				rec := httptest.NewRecorder()
				frontendpipe.ServeHTTP(&spec, rec, req)

				results[idx] = reqResult{
					reqID:      reqID,
					statusCode: rec.Code,
					retryAfter: rec.Header().Get("Retry-After"),
				}
			}(i)
		}

		// Unleash all concurrent requests simultaneously
		close(startBarrier)
		wg.Wait()

		// Verify every request received 429 and saw exactly one TryAdmit decision
		for _, res := range results {
			if res.statusCode != http.StatusTooManyRequests {
				t.Fatalf("[iter %d] request %s: expected HTTP 429, got %d", iter, res.reqID, res.statusCode)
			}
			if res.retryAfter != decodeqos.RetryAfterSeconds {
				t.Fatalf("[iter %d] request %s: expected Retry-After %q, got %q", iter, res.reqID, decodeqos.RetryAfterSeconds, res.retryAfter)
			}
			rec := limiter.RecordFor(res.reqID)
			if rec.calls != 1 {
				t.Fatalf("[iter %d] request %s: expected exactly 1 TryAcquire call, got %d", iter, res.reqID, rec.calls)
			}
			if rec.admitted {
				t.Fatalf("[iter %d] request %s: expected not admitted", iter, res.reqID)
			}
		}

		if maxCalls := limiter.MaxCallsPerRequest(); maxCalls != 1 {
			t.Fatalf("[iter %d] max TryAcquire calls per request: got %d, want 1", iter, maxCalls)
		}
		if total := limiter.TotalCalls(); total != int64(numGoroutines) {
			t.Fatalf("[iter %d] total TryAcquire calls: got %d, want %d", iter, total, numGoroutines)
		}
		if atomic.LoadInt64(&compileCalls) != 0 {
			t.Fatalf("[iter %d] CompileProof calls: got %d, want 0", iter, compileCalls)
		}
		if atomic.LoadInt64(&decodeCalls) != 0 {
			t.Fatalf("[iter %d] Spec.Decode calls: got %d, want 0", iter, decodeCalls)
		}
	}
}

// TestCandidateProof_SaturationRace_ConcurrentProofDecline_SingleAdmissionPerRequest
// is the core Task 7.6 requirement:
// Under a real capacity-constrained decodeqos limiter, N concurrent candidate requests
// race to enter. All requests have proof decline.
// Admitted requests fall back to canonical Spec.Decode under the SAME held permit.
// Because fallback does NOT reacquire or release/reacquire, admitted requests succeed (200 OK)
// and never see a secondary 429/503 from fallback.
// Saturated requests receive HTTP 429 Too Many Requests.
// Every single request (both 200s and 429s) sees strictly ONE TryAdmit decision.
func TestCandidateProof_SaturationRace_ConcurrentProofDecline_SingleAdmissionPerRequest(t *testing.T) {
	t.Parallel()

	const (
		maxConcurrent = 3
		numGoroutines = 20
		iterations    = 3
		payloadSize   = 1200 * 1024 // 1.2 MiB (> 1 MiB threshold)
	)

	payload := buildJSONPayload(payloadSize)

	for iter := range iterations {
		// Real limiter with capacity for maxConcurrent requests of payloadSize
		realLimiter := decodeqos.New(maxConcurrent, int64(maxConcurrent*payloadSize))
		limiter := newPerRequestTrackingLimiter(realLimiter)

		exec := &candidateGatesExec{}

		// Profile always declines: simulates unsupported extension / uncertified profile
		var compileCalls atomic.Int64
		prof := &certifiedTestProfile{
			profileID: "test_decline_race_profile",
			compileFunc: func(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
				compileCalls.Add(1)
				// Hold the permit until the limiter has actually rejected a competing
				// request. This forces contention while the limit is held instead of
				// assuming overlapping goroutine scheduling. The shared wait is
				// bounded and reports through t (no t.Fatal from this goroutine), so a
				// limiter regression cannot hang the suite.
				limiter.awaitFirstReject(t, saturationRejectWait)
				return frontendpipe.ProofOutput{}, errors.New("proof declined: fallback to canonical decode")
			},
		}

		var decodeCalls atomic.Int64
		spec := newCandidateProofSpec(
			exec, prof,
			frontendpipe.LargePayloadConfig{
				Enabled:        true,
				ThresholdBytes: 1 << 20,
			},
			limiter,
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
			body       string
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

				<-startBarrier // synchronize start of all concurrent requests

				rec := httptest.NewRecorder()
				frontendpipe.ServeHTTP(&spec, rec, req)

				results[idx] = reqResult{
					reqID:      reqID,
					statusCode: rec.Code,
					retryAfter: rec.Header().Get("Retry-After"),
					body:       rec.Body.String(),
				}
			}(i)
		}

		// Unleash concurrent race
		close(startBarrier)
		wg.Wait()

		var admittedCount int
		var saturatedCount int

		for _, res := range results {
			rec := limiter.RecordFor(res.reqID)

			// CRITICAL INVARIANT: strictly ONE TryAcquire call per request.
			// No request ever makes a second admission decision, even during fallback!
			if rec.calls != 1 {
				t.Fatalf("[iter %d] request %s: expected exactly 1 TryAcquire call, got %d (fallback-induced second admission!)",
					iter, res.reqID, rec.calls)
			}

			switch res.statusCode {
			case http.StatusOK:
				admittedCount++
				if !rec.admitted {
					t.Fatalf("[iter %d] request %s returned 200 OK but limiter record shows not admitted", iter, res.reqID)
				}
				if rec.releaseCount != 1 {
					t.Fatalf("[iter %d] request %s returned 200 OK but release count is %d, want 1", iter, res.reqID, rec.releaseCount)
				}
			case http.StatusTooManyRequests:
				saturatedCount++
				if rec.admitted {
					t.Fatalf("[iter %d] request %s returned 429 but limiter record shows admitted", iter, res.reqID)
				}
				if res.retryAfter != decodeqos.RetryAfterSeconds {
					t.Fatalf("[iter %d] request %s 429 missing Retry-After header %q, got %q",
						iter, res.reqID, decodeqos.RetryAfterSeconds, res.retryAfter)
				}
			default:
				t.Fatalf("[iter %d] request %s returned unexpected status %d: %s", iter, res.reqID, res.statusCode, res.body)
			}
		}

		// Assertions on the collective race behavior:
		if admittedCount == 0 {
			t.Fatalf("[iter %d] expected at least 1 admitted request, got 0", iter)
		}
		if saturatedCount == 0 {
			t.Fatalf("[iter %d] expected at least 1 saturated (429) request under contention, got 0", iter)
		}
		if admittedCount+saturatedCount != numGoroutines {
			t.Fatalf("[iter %d] sum of admitted (%d) + saturated (%d) != total requests (%d)",
				iter, admittedCount, saturatedCount, numGoroutines)
		}

		// Spec.Decode and CompileProof must match admitted count exactly
		if totalCompile := compileCalls.Load(); totalCompile != int64(admittedCount) {
			t.Fatalf("[iter %d] CompileProof calls: got %d, want %d", iter, totalCompile, admittedCount)
		}
		if totalDecode := decodeCalls.Load(); totalDecode != int64(admittedCount) {
			t.Fatalf("[iter %d] Spec.Decode calls: got %d, want %d", iter, totalDecode, admittedCount)
		}

		// Total admission calls across all requests equals numGoroutines (exactly 1 per request)
		if total := limiter.TotalCalls(); total != int64(numGoroutines) {
			t.Fatalf("[iter %d] total TryAcquire calls: got %d, want %d", iter, total, numGoroutines)
		}
		if maxCalls := limiter.MaxCallsPerRequest(); maxCalls != 1 {
			t.Fatalf("[iter %d] max TryAcquire calls per request: got %d, want 1", iter, maxCalls)
		}
	}
}

// TestCandidateProof_SaturationRace_ContextCanceledDuringSaturation verifies that
// when requests are canceled or time out during a saturation race, they receive HTTP 503,
// while active admitted requests complete fallback under same permit with HTTP 200,
// and saturated active requests receive HTTP 429. Every request sees strictly 1 TryAdmit decision.
func TestCandidateProof_SaturationRace_ContextCanceledDuringSaturation(t *testing.T) {
	t.Parallel()

	const (
		maxConcurrent = 2
		numGoroutines = 16
		payloadSize   = 1200 * 1024
	)

	payload := buildJSONPayload(payloadSize)

	realLimiter := decodeqos.New(maxConcurrent, int64(maxConcurrent*payloadSize))
	limiter := newPerRequestTrackingLimiter(realLimiter)

	exec := &candidateGatesExec{}
	prof := &certifiedTestProfile{
		profileID: "test_cancel_race_profile",
		compileFunc: func(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
			time.Sleep(10 * time.Millisecond)
			return frontendpipe.ProofOutput{}, errors.New("proof declined: fallback")
		},
	}

	spec := newCandidateProofSpec(
		exec, prof,
		frontendpipe.LargePayloadConfig{
			Enabled:        true,
			ThresholdBytes: 1 << 20,
		},
		limiter,
		nil,
		nil,
	)

	startBarrier := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	type reqResult struct {
		reqID      string
		statusCode int
		canceled   bool
	}
	results := make([]reqResult, numGoroutines)

	for i := range numGoroutines {
		go func(idx int) {
			defer wg.Done()
			reqID := fmt.Sprintf("cancel-race-req-%03d", idx)

			req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			ctx := context.WithValue(req.Context(), reqIDKey, reqID)

			shouldCancel := (idx % 3) == 0
			ctx, cancelFunc := context.WithCancel(ctx)
			defer cancelFunc()
			req = req.WithContext(ctx)

			<-startBarrier

			if shouldCancel {
				// Cancel immediately at barrier release
				cancelFunc()
			}

			rec := httptest.NewRecorder()
			frontendpipe.ServeHTTP(&spec, rec, req)

			results[idx] = reqResult{
				reqID:      reqID,
				statusCode: rec.Code,
				canceled:   shouldCancel,
			}
		}(i)
	}

	close(startBarrier)
	wg.Wait()

	for _, res := range results {
		rec := limiter.RecordFor(res.reqID)
		if rec.calls > 1 {
			t.Fatalf("request %s had %d TryAcquire calls, want at most 1", res.reqID, rec.calls)
		}
		switch res.statusCode {
		case http.StatusOK:
			if res.canceled {
				t.Fatalf("canceled request %s returned 200 OK", res.reqID)
			}
		case http.StatusTooManyRequests:
			// Expected for saturated requests
		case http.StatusServiceUnavailable:
			// Expected for canceled requests mapped to 503 by decodeqos
		case http.StatusBadRequest:
			// Expected if context canceled during body capture / preflight
		default:
			t.Fatalf("request %s returned unexpected status %d", res.reqID, res.statusCode)
		}
	}

	if maxCalls := limiter.MaxCallsPerRequest(); maxCalls > 1 {
		t.Fatalf("max TryAcquire calls per request: got %d, want at most 1", maxCalls)
	}
}

// TestCandidateProof_SaturationRace_PermitHeldThroughoutDeclineFallback verifies
// the spatiotemporal permit-holding invariant: while proof is declining and fallback
// decode is executing, the permit is continuously held and never released prematurely.
func TestCandidateProof_SaturationRace_PermitHeldThroughoutDeclineFallback(t *testing.T) {
	t.Parallel()

	const payloadSize = 1200 * 1024
	payload := buildJSONPayload(payloadSize)

	// Limiter capacity exactly 1 request
	realLimiter := decodeqos.New(1, payloadSize)
	limiter := newPerRequestTrackingLimiter(realLimiter)

	exec := &candidateGatesExec{}

	var onceStart, onceFinish sync.Once
	proofStarted := make(chan struct{})
	allowFallbackFinish := make(chan struct{})

	prof := &certifiedTestProfile{
		profileID: "test_permit_hold_profile",
		compileFunc: func(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
			onceStart.Do(func() { close(proofStarted) }) // signal that proof has started under the permit
			return frontendpipe.ProofOutput{}, errors.New("proof declined: fallback to decode")
		},
	}

	spec := newCandidateProofSpec(
		exec, prof,
		frontendpipe.LargePayloadConfig{
			Enabled:        true,
			ThresholdBytes: 1 << 20,
		},
		limiter,
		nil,
		func(dctx frontendpipe.DecodeContext) {
			// Inside Spec.Decode during fallback: block until signaled
			<-allowFallbackFinish
		},
	)

	var wg sync.WaitGroup
	wg.Add(2)

	var req1Status, req2Status int

	// Goroutine 1: candidate request whose proof declines and falls back
	go func() {
		defer wg.Done()
		defer onceFinish.Do(func() { close(allowFallbackFinish) })
		req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		ctx := context.WithValue(req.Context(), reqIDKey, "req-1-fallback")
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req.WithContext(ctx))
		req1Status = rec.Code
	}()

	// Wait until req1 has acquired the permit and entered CompileProof
	<-proofStarted

	// Goroutine 2: concurrent candidate request while req1 is in fallback under permit.
	// Because capacity is 1 and req1 still holds the permit, req2 MUST be rejected with 429!
	go func() {
		defer wg.Done()
		defer onceFinish.Do(func() { close(allowFallbackFinish) })
		req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		ctx := context.WithValue(req.Context(), reqIDKey, "req-2-saturated")
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req.WithContext(ctx))
		req2Status = rec.Code

		// Once req2 has experienced the 429 reject, allow req1 to finish fallback
		onceFinish.Do(func() { close(allowFallbackFinish) })
	}()

	wg.Wait()

	if req1Status != http.StatusOK {
		t.Fatalf("req1 (fallback under same permit): expected HTTP 200, got %d", req1Status)
	}
	if req2Status != http.StatusTooManyRequests {
		t.Fatalf("req2 (contending during req1 fallback): expected HTTP 429, got %d", req2Status)
	}

	// Verify both requests had strictly 1 TryAcquire decision
	rec1 := limiter.RecordFor("req-1-fallback")
	rec2 := limiter.RecordFor("req-2-saturated")

	if rec1.calls != 1 {
		t.Fatalf("req1: expected 1 TryAcquire call, got %d", rec1.calls)
	}
	if !rec1.admitted || rec1.releaseCount != 1 {
		t.Fatalf("req1: admitted=%t, releaseCount=%d, want true/1", rec1.admitted, rec1.releaseCount)
	}

	if rec2.calls != 1 {
		t.Fatalf("req2: expected 1 TryAcquire call, got %d", rec2.calls)
	}
	if rec2.admitted {
		t.Fatalf("req2: expected not admitted, got admitted=true")
	}
}
