package openresponses_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type deterministicResponseMetadata struct {
	id  string
	now time.Time
}

func (m deterministicResponseMetadata) NewResponseID() string { return m.id }
func (m deterministicResponseMetadata) Now() time.Time        { return m.now }

type scriptedEventStream struct {
	events   []aipapi.Event
	err      error
	pos      int
	closes   int
	canceled bool
}

func (s *scriptedEventStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if err := ctx.Err(); err != nil {
		s.canceled = true
		return aipapi.Event{}, err
	}
	if s.pos < len(s.events) {
		ev := s.events[s.pos]
		s.pos++
		return ev, nil
	}
	if s.err != nil {
		return aipapi.Event{}, s.err
	}
	return aipapi.Event{}, io.EOF
}

func (s *scriptedEventStream) Close() error {
	s.closes++
	return nil
}

type nonStreamingExecutor struct {
	stream aipapi.EventStream
	calls  int
}

func (e *nonStreamingExecutor) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	e.calls++
	return e.stream, nil
}

func TestNonStreamingResponseResourceSuccess(t *testing.T) {
	created := time.Unix(1_700_000_000, 0)
	stream := &scriptedEventStream{events: []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "hello"},
		{Kind: aipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 2, TotalTokens: 5},
		{Kind: aipapi.EventResponseFinished},
	}}
	executor := &nonStreamingExecutor{stream: stream}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Executor:             executor,
		ResponseIDSource:     deterministicResponseMetadata{id: "resp_proxy_1", now: created},
		ResponseClock:        deterministicResponseMetadata{id: "resp_proxy_1", now: created},
	})

	rec := executeNonStreaming(t, handler, `{"model":"gpt-4o","input":"hello","stream":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected application/json, got %q", got)
	}
	var resource map[string]any
	decodeJSON(t, rec, &resource)
	for _, field := range []string{"id", "object", "created_at", "completed_at", "status", "model", "output", "usage", "error", "incomplete_details"} {
		if _, ok := resource[field]; !ok {
			t.Errorf("required response field %q is missing", field)
		}
	}
	if resource["id"] != "resp_proxy_1" || resource["status"] != "completed" || resource["model"] != "gpt-4o" {
		t.Fatalf("unexpected response envelope: %#v", resource)
	}
	if got := resource["created_at"]; got != float64(created.Unix()) {
		t.Fatalf("unexpected created_at: %#v", got)
	}
	usage, ok := resource["usage"].(map[string]any)
	if !ok || usage["total_tokens"] != float64(5) {
		t.Fatalf("unexpected usage: %#v", resource["usage"])
	}
	if executor.calls != 1 || stream.closes != 1 {
		t.Fatalf("expected one synchronous execution and one close, calls=%d closes=%d", executor.calls, stream.closes)
	}
}

func TestNonStreamingResponseResourceToolAndReasoningOrder(t *testing.T) {
	stream := &scriptedEventStream{events: []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "think"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_proxy", ToolName: "search"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_proxy", Delta: `{"q":"x"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_proxy"},
		{Kind: aipapi.EventResponseFinished},
	}}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Executor:             &nonStreamingExecutor{stream: stream},
		ResponseIDSource:     deterministicResponseMetadata{id: "resp_tools", now: time.Unix(1_700_000_001, 0)},
		ResponseClock:        deterministicResponseMetadata{id: "resp_tools", now: time.Unix(1_700_000_001, 0)},
	})

	rec := executeNonStreaming(t, handler, `{"model":"gpt-4o","input":"find","stream":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resource struct {
		Output []struct {
			Type string `json:"type"`
		} `json:"output"`
	}
	decodeJSON(t, rec, &resource)
	if len(resource.Output) != 2 || resource.Output[0].Type != "reasoning" || resource.Output[1].Type != "function_call" {
		t.Fatalf("unexpected output order: %#v", resource.Output)
	}
	if strings.Contains(rec.Body.String(), "native_backend") {
		t.Fatal("native backend identifier leaked into response")
	}
}

func TestNonStreamingPreservesNativeCallIDAndDoesNotEchoArbitraryMetadata(t *testing.T) {
	stream := &scriptedEventStream{events: []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_native_42", ToolName: "search"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_native_42", Delta: `{"q":"x"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_native_42"},
		{Kind: aipapi.EventResponseFinished},
	}}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Executor:             &nonStreamingExecutor{stream: stream},
		ResponseIDSource:     deterministicResponseMetadata{id: "resp_native", now: time.Unix(1_700_000_004, 0)},
		ResponseClock:        deterministicResponseMetadata{id: "resp_native", now: time.Unix(1_700_000_004, 0)},
	})

	rec := executeNonStreaming(t, handler, `{"model":"gpt-4o","input":"find","metadata":{"tenant":"private"},"stream":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "call_native_42") {
		t.Fatalf("native call ID was not preserved: %s", body)
	}
	if strings.Contains(body, "call_resp_native_0") || strings.Contains(body, "private") {
		t.Fatalf("proxy-generated/arbitrary metadata leaked into response: %s", body)
	}
}

func TestNonStreamingResponseResourceFailedAndIncomplete(t *testing.T) {
	tests := []struct {
		name   string
		event  aipapi.Event
		status string
	}{
		{name: "failed", event: aipapi.Event{Kind: aipapi.EventError, ErrorCode: "provider_failure", ErrorMessage: "provider secret"}, status: "failed"},
		{name: "incomplete", event: aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: "length"}, status: "incomplete"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stream := &scriptedEventStream{events: []aipapi.Event{{Kind: aipapi.EventResponseStarted}, tc.event}}
			handler := openresponses.NewHandler(openresponses.HandlerConfig{
				AllowUnauthenticated: true,
				Executor:             &nonStreamingExecutor{stream: stream},
				ResponseIDSource:     deterministicResponseMetadata{id: "resp_" + tc.name, now: time.Unix(1_700_000_002, 0)},
				ResponseClock:        deterministicResponseMetadata{id: "resp_" + tc.name, now: time.Unix(1_700_000_002, 0)},
			})
			rec := executeNonStreaming(t, handler, `{"model":"gpt-4o","input":"hello","stream":false}`)
			if tc.name == "failed" {
				if rec.Code != http.StatusBadGateway {
					t.Fatalf("expected 502 for terminal backend error, got %d: %s", rec.Code, rec.Body.String())
				}
				if !strings.Contains(rec.Body.String(), `"code":"provider_failure"`) {
					t.Fatalf("canonical provider error code was lost: %s", rec.Body.String())
				}
				if strings.Contains(rec.Body.String(), "provider secret") {
					t.Fatalf("provider error message leaked: %s", rec.Body.String())
				}
				return
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			var resource map[string]any
			decodeJSON(t, rec, &resource)
			if resource["status"] != tc.status {
				t.Fatalf("expected status %q, got %#v", tc.status, resource["status"])
			}
		})
	}
}

func TestNonStreamingMalformedStreamAndCancellationAreSanitized(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func() (*scriptedEventStream, context.Context)
	}{
		{name: "no terminal", make: func() (*scriptedEventStream, context.Context) {
			return &scriptedEventStream{events: []aipapi.Event{{Kind: aipapi.EventResponseStarted}}}, context.Background()
		}},
		{name: "stream error", make: func() (*scriptedEventStream, context.Context) {
			return &scriptedEventStream{events: []aipapi.Event{{Kind: aipapi.EventResponseStarted}}, err: errors.New("native_backend_secret")}, context.Background()
		}},
		{name: "canceled", make: func() (*scriptedEventStream, context.Context) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return &scriptedEventStream{}, ctx
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream, ctx := tc.make()
			handler := openresponses.NewHandler(openresponses.HandlerConfig{
				AllowUnauthenticated: true,
				Executor:             &nonStreamingExecutor{stream: stream},
				ResponseIDSource:     deterministicResponseMetadata{id: "resp_bad", now: time.Unix(1_700_000_003, 0)},
				ResponseClock:        deterministicResponseMetadata{id: "resp_bad", now: time.Unix(1_700_000_003, 0)},
			})
			req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewBufferString(`{"model":"gpt-4o","input":"hello","stream":false}`)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code == http.StatusOK && rec.Body.Len() > 0 {
				t.Fatalf("malformed/canceled stream unexpectedly succeeded: %s", rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "native_backend_secret") {
				t.Fatal("stream error leaked into response")
			}
			if tc.name == "canceled" {
				if stream.closes != 0 {
					t.Fatalf("canceled request executed an unneeded backend stream; closes=%d", stream.closes)
				}
				return
			}
			if stream.closes != 1 {
				t.Fatalf("expected exactly one close, got %d", stream.closes)
			}
		})
	}
}

func executeNonStreaming(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), target); err != nil {
		t.Fatalf("invalid JSON response: %v; body=%s", err, rec.Body.String())
	}
}
