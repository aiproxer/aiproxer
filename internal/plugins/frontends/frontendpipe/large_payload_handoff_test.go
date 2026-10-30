package frontendpipe_test

// Task 4 (large-payload-lifecycle-correctness): accepted replay must transfer
// through frontend response consumption. A usable execution result transfers
// source ownership to the returned lifetime: the frontend registers
// returned-stream cleanup BEFORE any fallible response callback (OnWireCommit,
// session headers, wrapping, writers) and never closes the root after a
// successful handoff. Pre-transfer error/panic/nil-success rollback keeps the
// lexical source guard. Nil-success (nil error with nil stream) is an internal
// execution failure: the untransferred root is closed and the existing safe
// error shape is emitted, never canonical fallback after the execute
// commitment (Requirements 1.1, 1.2, 1.3).

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// handoffTrackingStream observes disposal of a transferred execution stream.
// On first Close it also closes the attached source, emulating the production
// runtime transfer contract (the returned lifetime owns source cleanup).
type handoffTrackingStream struct {
	mu       sync.Mutex
	closes   int
	attached largebody.Source
}

func (s *handoffTrackingStream) attach(src largebody.Source) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attached = src
}

func (s *handoffTrackingStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (s *handoffTrackingStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closes++
	if s.attached != nil {
		_ = s.attached.Close()
	}
	return nil
}

func (s *handoffTrackingStream) closeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closes
}

func handoffSourceClosed(t *testing.T, src largebody.Source) bool {
	t.Helper()
	if src == nil {
		t.Fatal("executor did not receive a source")
	}
	closed, ok := src.(interface{ IsClosed() bool })
	if !ok {
		t.Fatalf("source %T has no IsClosed observer", src)
	}
	return closed.IsClosed()
}

// handoffHarness builds the accepted-assessment ServeHTTP fixture shared by
// the transfer-seam tests: every case assesses-accepts, executes via execFn,
// and counts Spec.Decode calls (which must stay zero after the execute
// commitment). Each test keeps its own execution result and assertions.
type handoffHarness struct {
	spec        frontendpipe.Spec[struct{}]
	exec        *testAssessorExecutor
	capturedSrc *largebody.Source
	decodeCalls *int
}

func newHandoffHarness(execFn func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error), cfgMut func(*frontendpipe.LargePayloadConfig)) *handoffHarness {
	h := &handoffHarness{exec: &testAssessorExecutor{}, capturedSrc: new(largebody.Source), decodeCalls: new(int)}
	h.exec.assessFunc = func(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
		return makeAcceptedAssessment(proof)
	}
	h.exec.executeLargeFunc = func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
		*h.capturedSrc = src
		return execFn(ctx, accepted, src)
	}
	cfg := frontendpipe.LargePayloadConfig{
		Enabled:        true,
		ThresholdBytes: 1 << 20,
	}
	if cfgMut != nil {
		cfgMut(&cfg)
	}
	h.spec = newAssessmentTestSpec(
		h.exec,
		minimalValidProofProfile(),
		cfg,
		&trackingAdmissionLimiter{},
		nil,
		nil,
		func(dctx frontendpipe.DecodeContext) {
			*h.decodeCalls++
		},
	)
	return h
}

func (h *handoffHarness) serve(payload []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&h.spec, rec, req)
	return rec
}

func serveHandoffRecovered(spec *frontendpipe.Spec[struct{}], rec *httptest.ResponseRecorder, req *http.Request) (panicked any) {
	defer func() { panicked = recover() }()
	frontendpipe.ServeHTTP(spec, rec, req)
	return nil
}

// TestLargePayloadLifecycle_WireNilSuccess_ClosesRootWithoutCanonicalFallback
// proves Requirement 1.2 at the transfer seam: a nil stream with nil error is
// an internal execution failure. The untransferred root must be released and
// the existing safe error shape emitted; Spec.Decode must never run after the
// execute commitment.
func TestLargePayloadLifecycle_WireNilSuccess_ClosesRootWithoutCanonicalFallback(t *testing.T) {
	t.Parallel()

	h := newHandoffHarness(func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
		return largebody.ExecutionResult{}, nil
	}, nil)
	payload := buildJSONPayload(1200 * 1024)
	rec := h.serve(payload)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("nil-success must fail explicitly, got HTTP %d with body %q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `{"status":"ok"}`) {
		t.Fatalf("nil-success must not emit the success placeholder, got %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "internal error") {
		t.Fatalf("nil-success must emit the existing safe error shape, got %q", rec.Body.String())
	}
	if *h.decodeCalls != 0 {
		t.Fatalf("Spec.Decode calls=%d want 0 (no canonical fallback after commitment)", *h.decodeCalls)
	}
	if h.exec.ExecuteLargeCallCount() != 1 {
		t.Fatalf("ExecuteLargeBody calls=%d want 1", h.exec.ExecuteLargeCallCount())
	}
	if !handoffSourceClosed(t, *h.capturedSrc) {
		t.Fatal("nil-success must close the untransferred root source")
	}
}

// TestLargePayloadLifecycle_WireOnWireCommitPanic_ClosesTransferredStream proves
// Requirement 1.3 at the transfer seam: returned-stream cleanup is registered
// BEFORE OnWireCommit, so a panicking fallible callback cannot strand the
// transferred lifetime (which owns source cleanup). The panic propagates
// unchanged.
func TestLargePayloadLifecycle_WireOnWireCommitPanic_ClosesTransferredStream(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("handoff test OnWireCommit panic")
	tracker := &handoffTrackingStream{}
	h := newHandoffHarness(func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
		tracker.attach(src)
		return largebody.ExecutionResult{Stream: tracker}, nil
	}, nil)
	h.spec.OnWireCommit = func(r *http.Request, res frontendpipe.WireCommitResult) {
		panic(sentinel)
	}

	payload := buildJSONPayload(1200 * 1024)
	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	panicked := serveHandoffRecovered(&h.spec, rec, req)
	if panicked != sentinel {
		t.Fatalf("panic=%v want the OnWireCommit sentinel to propagate unchanged", panicked)
	}
	if got := tracker.closeCount(); got != 1 {
		t.Fatalf("transferred stream closes=%d want exactly 1 despite the callback panic", got)
	}
	if !handoffSourceClosed(t, *h.capturedSrc) {
		t.Fatal("transferred-stream cleanup must reclaim the owned source on panic unwinding")
	}
	if *h.decodeCalls != 0 {
		t.Fatalf("Spec.Decode calls=%d want 0 (no canonical fallback after commitment)", *h.decodeCalls)
	}
}

// TestLargePayloadLifecycle_WireErrorBearingResult_PreservesCleanupWithoutFallback
// guards the pre-transfer error rollback end to end: an execution error with a
// partial stream disposes the stream at the execute seam, releases the root,
// emits the safe error shape, and never falls back to canonical decode.
func TestLargePayloadLifecycle_WireErrorBearingResult_PreservesCleanupWithoutFallback(t *testing.T) {
	t.Parallel()

	boom := errors.New("handoff test wire execute failure")
	tracker := &handoffTrackingStream{}
	h := newHandoffHarness(func(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
		tracker.attach(src)
		return largebody.ExecutionResult{Stream: tracker}, boom
	}, nil)
	payload := buildJSONPayload(1200 * 1024)
	rec := h.serve(payload)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("error-bearing result must fail explicitly, got HTTP %d with body %q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `{"status":"ok"}`) {
		t.Fatalf("error-bearing result must not emit the success placeholder, got %q", rec.Body.String())
	}
	if *h.decodeCalls != 0 {
		t.Fatalf("Spec.Decode calls=%d want 0 (no canonical fallback after commitment)", *h.decodeCalls)
	}
	if got := tracker.closeCount(); got != 1 {
		t.Fatalf("error-bearing stream closes=%d want exactly 1", got)
	}
	if !handoffSourceClosed(t, *h.capturedSrc) {
		t.Fatal("error-bearing result must release the untransferred root source")
	}
}
