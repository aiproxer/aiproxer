package gemini_test

import (
	"bytes"
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/decodeqos"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/reqbody"
)

var minimalGenerateContentRequest = []byte(`{
  "contents": [{"role":"user","parts":[{"text":"ping"}]}],
  "generationConfig": {"maxOutputTokens": 128, "temperature": 0.5, "topP": 0.9}
}`)

func TestHandler_canceledContextBeforeTryAdmitReturns503WithoutExecutor(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	exec := &recordingExecutor{}
	h := &gemini.Handler{Exec: exec, DefaultRouteSelector: "stub:gemini-2.0-flash", DecodeAdmission: decodeqos.New(1, math.MaxInt64)}
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.0-flash:generateContent", bytes.NewReader(minimalGenerateContentRequest))
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if exec.called {
		t.Fatal("executor was called with canceled request context")
	}
}

func TestHandler_decodeLimiterSaturationReturns429WithoutExecutor(t *testing.T) {
	t.Parallel()

	limiter := decodeqos.New(1, math.MaxInt64)
	release, ok, err := limiter.TryAcquire(t.Context(), 0)
	if err != nil || !ok {
		t.Fatalf("pre-acquire limiter: ok=%v err=%v", ok, err)
	}
	defer release()

	exec := &recordingExecutor{}
	h := &gemini.Handler{Exec: exec, DefaultRouteSelector: "stub:gemini-2.0-flash", DecodeAdmission: limiter}
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.0-flash:generateContent", bytes.NewReader(minimalGenerateContentRequest))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Retry-After"); got != decodeqos.RetryAfterSeconds {
		t.Fatalf("Retry-After: %q, want %q", got, decodeqos.RetryAfterSeconds)
	}
	if exec.called {
		t.Fatal("executor was called while decode limiter was saturated")
	}
}

func TestHandler_oversizedBodyReturns413WithoutAcquiringDecodeLimiter(t *testing.T) {
	t.Parallel()

	limiter := decodeqos.New(1, math.MaxInt64)
	release, ok, err := limiter.TryAcquire(t.Context(), 0)
	if err != nil || !ok {
		t.Fatalf("pre-acquire limiter: ok=%v err=%v", ok, err)
	}
	defer release()

	exec := &recordingExecutor{}
	h := &gemini.Handler{Exec: exec, DefaultRouteSelector: "stub:gemini-2.0-flash", DecodeAdmission: limiter}
	body := bytes.Repeat([]byte("a"), int(reqbody.DefaultMaxBytes)+1)
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.0-flash:generateContent", bytes.NewReader(body))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if exec.called {
		t.Fatal("executor was called for oversized body")
	}
}

func TestHandler_nilDecodeLimiterStillReachesExecutor(t *testing.T) {
	t.Parallel()

	exec := &recordingExecutor{}
	h := &gemini.Handler{Exec: exec, DefaultRouteSelector: "stub:gemini-2.0-flash"}
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.0-flash:generateContent", bytes.NewReader(minimalGenerateContentRequest))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if !exec.called {
		t.Fatal("executor was not called with nil decode limiter")
	}
}

func TestHandler_decodeLimiterReleasesAfterDecodeFailureAndSuccess(t *testing.T) {
	t.Parallel()

	limiter := decodeqos.New(1, math.MaxInt64)
	h := &gemini.Handler{Exec: &recordingExecutor{}, DefaultRouteSelector: "stub:gemini-2.0-flash", DecodeAdmission: limiter}

	badReq := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.0-flash:generateContent", strings.NewReader(`{}`))
	badRR := httptest.NewRecorder()
	h.ServeHTTP(badRR, badReq)
	if badRR.Code != http.StatusBadRequest {
		t.Fatalf("bad status: %d body: %s", badRR.Code, badRR.Body.String())
	}

	goodReq := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.0-flash:generateContent", bytes.NewReader(minimalGenerateContentRequest))
	goodRR := httptest.NewRecorder()
	h.ServeHTTP(goodRR, goodReq)
	if goodRR.Code != http.StatusOK {
		t.Fatalf("good status: %d body: %s", goodRR.Code, goodRR.Body.String())
	}

	release, ok, err := limiter.TryAcquire(t.Context(), 0)
	if err != nil || !ok {
		t.Fatalf("limiter remained held after success: ok=%v err=%v", ok, err)
	}
	release()
}
