package openresponses_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func allowedToolsCreateBody(stream bool, mode string) string {
	s := "false"
	if stream {
		s = "true"
	}
	modeJSON := ""
	if mode != "" {
		modeJSON = `"mode": "` + mode + `",`
	}
	return `{
	"model": "gpt-4o",
	"input": "call a tool",
	"tools": [
		{"type": "function", "name": "allowed_fn", "parameters": {"type": "object"}},
		{"type": "function", "name": "forbidden_fn", "parameters": {"type": "object"}}
	],
	"tool_choice": {"type": "allowed_tools", ` + modeJSON + ` "tools": [{"type": "function", "name": "allowed_fn"}]},
	"stream": ` + s + `
}`
}

func allowedToolsMixedToolEvents() []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_bad", ToolName: "forbidden_fn"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_bad", Delta: `{"query":"nope"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_bad"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_good", ToolName: "allowed_fn"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_good", Delta: `{"query":"yes"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_good"},
		{Kind: aipapi.EventResponseFinished},
	}
}

func TestHandlerStreaming_allowedToolsSuppressesForbiddenToolCall(t *testing.T) {
	stream := &streamingEventStream{events: allowedToolsMixedToolEvents()}
	executor := &streamingExecutor{stream: stream}
	handler := newStreamingHandler(executor)
	w := newStreamingResponseWriter()

	req := httptestNewRequest(context.Background(), allowedToolsCreateBody(true, ""))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	body, status, _, _ := w.snapshot()
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, body)
	}
	if strings.Contains(body, "forbidden_fn") {
		t.Fatalf("forbidden tool call escaped on SSE output:\n%s", body)
	}
	if !strings.Contains(body, "allowed_fn") {
		t.Fatalf("allowed tool call missing from SSE output:\n%s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("missing DONE terminator:\n%s", body)
	}
}

func TestHandlerNonStreaming_allowedToolsSuppressesForbiddenToolCall(t *testing.T) {
	stream := &scriptedEventStream{events: allowedToolsMixedToolEvents()}
	executor := &nonStreamingExecutor{stream: stream}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Executor:             executor,
		ResponseIDSource:     deterministicResponseMetadata{id: "resp_allowed", now: time.Unix(1_700_000_005, 0)},
		ResponseClock:        deterministicResponseMetadata{id: "resp_allowed", now: time.Unix(1_700_000_005, 0)},
	})

	rec := executeNonStreaming(t, handler, allowedToolsCreateBody(false, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "forbidden_fn") {
		t.Fatalf("forbidden tool call escaped on non-streaming resource:\n%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "allowed_fn") {
		t.Fatalf("allowed tool call missing from non-streaming resource:\n%s", rec.Body.String())
	}

	var resource struct {
		Output []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"output"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resource); err != nil {
		t.Fatalf("invalid resource JSON: %v", err)
	}
	functionCalls := 0
	for _, it := range resource.Output {
		if it.Type == "function_call" {
			functionCalls++
			if it.Name != "allowed_fn" {
				t.Fatalf("unexpected function_call name %q", it.Name)
			}
		}
	}
	if functionCalls != 1 {
		t.Fatalf("expected exactly one function_call in output, got %d", functionCalls)
	}
}

func TestHandlerStreaming_allowedToolsModeNoneSuppressesAllToolCalls(t *testing.T) {
	// mode none is a hard "never call" constraint: even a subset-member tool
	// call emitted by the backend must never surface on the wire.
	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_forbidden", ToolName: "forbidden_fn"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_forbidden", Delta: `{"query":"nope"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_forbidden"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_allowed", ToolName: "allowed_fn"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_allowed", Delta: `{"query":"leak"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_allowed"},
		{Kind: aipapi.EventResponseFinished},
	}
	stream := &streamingEventStream{events: events}
	executor := &streamingExecutor{stream: stream}
	handler := newStreamingHandler(executor)
	w := newStreamingResponseWriter()

	req := httptestNewRequest(context.Background(), allowedToolsCreateBody(true, "none"))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	body, status, _, _ := w.snapshot()
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, body)
	}
	if strings.Contains(body, "allowed_fn") || strings.Contains(body, "forbidden_fn") {
		t.Fatalf("mode none leaked a tool call on SSE output:\n%s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("missing DONE terminator:\n%s", body)
	}
}
