package openailegacy_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/decodeqos"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/reqbody"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestHandler_canceledContextBeforeTryAdmitReturns503WithoutExecutor(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	exec := &recordingExecutor{}
	h := &openailegacy.Handler{Exec: exec, DefaultRouteSelector: "stub:gpt-4o-mini", DecodeAdmission: decodeqos.New(1, math.MaxInt64)}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(readGolden(t, "create_text_nonstream.json")))
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
	h := &openailegacy.Handler{Exec: exec, DefaultRouteSelector: "stub:gpt-4o-mini", DecodeAdmission: limiter}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(readGolden(t, "create_text_nonstream.json")))
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

func TestHandler_decodeByteBudgetSaturationReturns429WithoutExecutor(t *testing.T) {
	t.Parallel()

	limiter := decodeqos.New(100, 12)
	release, ok, err := limiter.TryAcquire(t.Context(), 8)
	if err != nil || !ok {
		t.Fatalf("pre-acquire limiter: ok=%v err=%v", ok, err)
	}
	defer release()

	exec := &recordingExecutor{}
	h := &openailegacy.Handler{Exec: exec, DefaultRouteSelector: "stub:gpt-4o-mini", DecodeAdmission: limiter}
	body := []byte(`{"ab":1}`) // 8 bytes, passes JSON preflight
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Retry-After"); got != decodeqos.RetryAfterSeconds {
		t.Fatalf("Retry-After: %q, want %q", got, decodeqos.RetryAfterSeconds)
	}
	if exec.called {
		t.Fatal("executor was called while decode byte budget was saturated")
	}
}

func TestHandler_gzipDecodeWeightUsesDecompressedLength(t *testing.T) {
	t.Parallel()

	plain := []byte(`{"ab":1}`) // 8 bytes after decompress
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(plain); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	limiter := decodeqos.New(100, 12)
	release, ok, err := limiter.TryAcquire(t.Context(), 8)
	if err != nil || !ok {
		t.Fatalf("pre-acquire limiter: ok=%v err=%v", ok, err)
	}
	defer release()

	exec := &recordingExecutor{}
	h := &openailegacy.Handler{Exec: exec, DefaultRouteSelector: "stub:gpt-4o-mini", DecodeAdmission: limiter}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Encoding", "gzip")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if exec.called {
		t.Fatal("executor was called while gzip decode weight exceeded byte budget")
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
	h := &openailegacy.Handler{Exec: exec, DefaultRouteSelector: "stub:gpt-4o-mini", DecodeAdmission: limiter}
	body := bytes.Repeat([]byte("a"), int(reqbody.DefaultMaxBytes)+1)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: %d body: %s", rr.Code, rr.Body.String())
	}
	if exec.called {
		t.Fatal("executor was called for oversized body")
	}
}

func TestHandler_largeConfiguredBodyAcceptedUnderRaisedLimits(t *testing.T) {
	t.Parallel()

	// Max string is 8MiB; envelope pushes total body over the default 8MiB request cap.
	content := strings.Repeat("x", int(reqbody.DefaultMaxBytes))
	payload, err := json.Marshal(map[string]any{
		"model": "gpt-4o-mini",
		"messages": []map[string]string{
			{"role": "user", "content": content},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(payload)) <= reqbody.DefaultMaxBytes {
		t.Fatalf("payload len %d not above default body cap", len(payload))
	}
	raised := int64(len(payload)) + 1024
	limiter := decodeqos.New(4, raised)

	exec := &recordingExecutor{}
	h := &openailegacy.Handler{
		Exec:                 exec,
		DefaultRouteSelector: "stub:gpt-4o-mini",
		MaxRequestBodyBytes:  raised,
		DecodeAdmission:      limiter,
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body %s; want 200 with raised body/budget (len=%d)", rr.Code, rr.Body.String(), len(payload))
	}
	if !exec.called {
		t.Fatal("executor was not called for raised large body")
	}
}

func TestHandler_decodeAdmissionReleasedBeforeBlockingExecute(t *testing.T) {
	t.Parallel()

	limiter := decodeqos.New(1, math.MaxInt64)
	entered := make(chan struct{})
	unblock := make(chan struct{})
	var closeUnblock sync.Once
	defer closeUnblock.Do(func() { close(unblock) })
	exec := &blockingExecutor{entered: entered, unblock: unblock}
	h := &openailegacy.Handler{
		Exec:                 exec,
		DefaultRouteSelector: "stub:gpt-4o-mini",
		DecodeAdmission:      limiter,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(readGolden(t, "create_text_nonstream.json")))
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("status %d body %s", rr.Code, rr.Body.String())
		}
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("execute did not start")
	}

	release, ok, err := limiter.TryAcquire(t.Context(), 0)
	if err != nil || !ok {
		t.Fatalf("admission still held during Execute: ok=%v err=%v", ok, err)
	}
	release()
	closeUnblock.Do(func() { close(unblock) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not finish")
	}
}

func TestHandler_nilDecodeLimiterStillReachesExecutor(t *testing.T) {
	t.Parallel()

	exec := &recordingExecutor{}
	h := &openailegacy.Handler{Exec: exec, DefaultRouteSelector: "stub:gpt-4o-mini"}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(readGolden(t, "create_text_nonstream.json")))
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
	h := &openailegacy.Handler{Exec: &recordingExecutor{}, DefaultRouteSelector: "stub:gpt-4o-mini", DecodeAdmission: limiter}

	badReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	badRR := httptest.NewRecorder()
	h.ServeHTTP(badRR, badReq)
	if badRR.Code != http.StatusBadRequest {
		t.Fatalf("bad status: %d body: %s", badRR.Code, badRR.Body.String())
	}

	goodReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(readGolden(t, "create_text_nonstream.json")))
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

type blockingExecutor struct {
	entered chan struct{}
	unblock chan struct{}
}

func (e *blockingExecutor) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	close(e.entered)
	<-e.unblock
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "ok"},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

func (e *blockingExecutor) CancelALeg(context.Context, aipapi.ALegCancelRequest) error { return nil }

func (e *blockingExecutor) WallClock() func() time.Time { return nil }
