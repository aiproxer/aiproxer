package openresponses_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type compactExecutor struct {
	calls  int
	call   *aipapi.Call
	stream aipapi.EventStream
	err    error
}

func (s *compactExecutor) Execute(_ context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	s.calls++
	s.call = call
	if s.err != nil {
		return nil, s.err
	}
	return s.stream, nil
}

type closeCountingCompactStream struct {
	events []aipapi.Event
	pos    int
	closes int
}

func (s *closeCountingCompactStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if err := ctx.Err(); err != nil {
		return aipapi.Event{}, err
	}
	if s.pos == len(s.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := s.events[s.pos]
	s.pos++
	return ev, nil
}

func (s *closeCountingCompactStream) Close() error {
	s.closes++
	return nil
}

type compactIDClock struct{}

func (compactIDClock) NewCompactResourceID() string { return "comp_proxy_1" }
func (compactIDClock) NewResponseID() string        { return "unused" }
func (compactIDClock) Now() time.Time               { return time.Unix(1_700_000_000, 0) }

func TestCompact_RoutesCanonicalOperationToNormalExecutor(t *testing.T) {
	stream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "compressed"},
		{Kind: aipapi.EventUsageDelta, InputTokens: 8, OutputTokens: 3, TotalTokens: 11},
		{Kind: aipapi.EventResponseFinished},
	})
	executor := &compactExecutor{stream: stream}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated:    true,
		Executor:                executor,
		CompactResourceIDSource: compactIDClock{},
		ResponseClock:           compactIDClock{},
	})

	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses/compact", bytes.NewBufferString(`{
		"model":"gpt-4o",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"compact this"}]}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if executor.calls != 1 {
		t.Fatalf("executor calls=%d, want 1", executor.calls)
	}
	if executor.call == nil {
		t.Fatal("executor received nil call")
	}
	if got := executor.call.Invocation.Operation; got != aipapi.OperationContextCompaction {
		t.Fatalf("operation=%q, want %q", got, aipapi.OperationContextCompaction)
	}
	if got := executor.call.Invocation.TransportMode; got != aipapi.TransportModeNonStreaming {
		t.Fatalf("transport=%q, want %q", got, aipapi.TransportModeNonStreaming)
	}
	if !executor.call.HasItemAuthority() || len(executor.call.Items) != 1 {
		t.Fatalf("executor call lost ordered item authority: %+v", executor.call.Items)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"object":"response.compaction"`)) ||
		!bytes.Contains(rec.Body.Bytes(), []byte(`"model":"gpt-4o"`)) ||
		!bytes.Contains(rec.Body.Bytes(), []byte(`"total_tokens":11`)) {
		t.Fatalf("compact resource lost required fields: %s", rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"previous_response_id"`)) || bytes.Contains(rec.Body.Bytes(), []byte(`"store"`)) {
		t.Fatalf("compact resource must not create continuation metadata: %s", rec.Body.String())
	}
}

func TestCompact_AccumulatesUsageDeltas(t *testing.T) {
	stream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 2, TotalTokens: 5, CacheReadTokens: 1, ReasoningTokens: 1},
		{Kind: aipapi.EventUsageDelta, InputTokens: 4, OutputTokens: 3, TotalTokens: 7, CacheReadTokens: 2, ReasoningTokens: 2},
		{Kind: aipapi.EventResponseFinished},
	})
	executor := &compactExecutor{stream: stream}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated:    true,
		Executor:                executor,
		CompactResourceIDSource: compactIDClock{},
		ResponseClock:           compactIDClock{},
	})
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses/compact", bytes.NewBufferString(`{"model":"gpt-4o","input":"compact this"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"input_tokens":7`)) ||
		!bytes.Contains(rec.Body.Bytes(), []byte(`"output_tokens":5`)) ||
		!bytes.Contains(rec.Body.Bytes(), []byte(`"total_tokens":12`)) ||
		!bytes.Contains(rec.Body.Bytes(), []byte(`"cached_tokens":3`)) ||
		!bytes.Contains(rec.Body.Bytes(), []byte(`"reasoning_tokens":3`)) {
		t.Fatalf("usage deltas were not accumulated: %s", rec.Body.String())
	}
}

func TestCompact_ClosesCanonicalStreamExactlyOnceAndSanitizesPreOutputFailure(t *testing.T) {
	stream := &closeCountingCompactStream{events: []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
	}}
	executor := &compactExecutor{stream: stream}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true, Executor: executor,
	})
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses/compact", bytes.NewBufferString(`{"model":"gpt-4o","input":"compact this"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("native_backend_secret")) {
		t.Fatalf("stream failure leaked provider detail: %s", rec.Body.String())
	}
	if stream.closes != 1 {
		t.Fatalf("stream closes=%d, want exactly 1", stream.closes)
	}
}

func TestCompact_UnsupportedCapabilityIsSanitizedBeforeResource(t *testing.T) {
	// The shared execerr contract renders a *aipapi.RejectError's own bounded
	// reason as the client-safe message (400 client reject) and never the
	// wrapping chain text, so provider detail stays out of the response.
	executor := &compactExecutor{err: fmt.Errorf("provider-internal: %w", &aipapi.RejectError{Reason: "compaction is not supported for this route"})}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true, Executor: executor,
	})
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses/compact", bytes.NewBufferString(`{"model":"gpt-4o","input":"compact this"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusBadRequest)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("provider-internal")) {
		t.Fatalf("wrapped executor detail leaked into response: %s", rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("compaction is not supported for this route")) {
		t.Fatalf("client-safe capability reason missing from response: %s", rec.Body.String())
	}
}

func TestCompact_ExecutorInvokedOnceForCanonicalOperation(t *testing.T) {
	executor := &compactExecutor{err: errors.New("native_backend_secret")}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true, Executor: executor,
	})
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses/compact", bytes.NewBufferString(`{"model":"gpt-4o","input":"compact this"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if executor.calls != 1 {
		t.Fatalf("executor calls=%d, want exactly 1", executor.calls)
	}
}
