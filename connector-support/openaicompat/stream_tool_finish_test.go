package openaicompat_test

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// toolCallFinishSSE mirrors the essential adapter fixture in
// internal/plugins/backends/openaicompat.TestHandleChatChunk_toolCallsStreamingFromJSON:
// tool-call deltas then finish_reason=="tool_calls".
func toolCallFinishSSE() string {
	return strings.Join([]string{
		`data: {"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_ab","type":"function","function":{"name":"get_weather"}}]},"finish_reason":null}]}`,
		`data: {"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\""}}]},"finish_reason":null}]}`,
		`data: {"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"NYC\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		"",
	}, "\n")
}

// multiToolCallFinishSSE starts two tool calls (index 0 and 1) then finishes both.
func multiToolCallFinishSSE() string {
	return strings.Join([]string{
		`data: {"id":"cc_multi","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_ma","type":"function","function":{"name":"alpha"}}]},"finish_reason":null}]}`,
		`data: {"id":"cc_multi","choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_mb","type":"function","function":{"name":"beta"}}]},"finish_reason":null}]}`,
		`data: {"id":"cc_multi","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"a\":1}"}}]},"finish_reason":null}]}`,
		`data: {"id":"cc_multi","choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"b\":2}"}}]},"finish_reason":null}]}`,
		`data: {"id":"cc_multi","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		"",
	}, "\n")
}

func collectChatSSE(t *testing.T, sse string) []aipapi.Event {
	t.Helper()
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{
		ChatStreamSSE: sse,
	}))
	t.Cleanup(srv.Close)
	c := &openaicompat.Client{
		BaseURL:    srv.URL + "/v1",
		APIKey:     "sk-test",
		HTTPClient: srv.Client(),
	}
	es, err := c.Open(context.Background(), sampleCall(), "emu-model", openaicompat.FlavorChat)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = es.Close() })
	var events []aipapi.Event
	for {
		ev, err := es.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev)
	}
	return events
}

func toolLifecycle(events []aipapi.Event) []aipapi.Event {
	out := make([]aipapi.Event, 0, len(events))
	for _, ev := range events {
		switch ev.Kind {
		case aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta, aipapi.EventToolCallFinished:
			out = append(out, ev)
		}
	}
	return out
}

func TestDecodeChatSSE_toolCallFinishSemantics(t *testing.T) {
	t.Parallel()
	events := collectChatSSE(t, toolCallFinishSSE())
	lifecycle := toolLifecycle(events)

	// Essential adapter sequence for the same SSE: Started, Args, Args, Finished.
	want := []aipapi.Event{
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_ab", ToolName: "get_weather"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_ab", Delta: `{"city"`},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_ab", Delta: `:"NYC"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_ab"},
	}
	if len(lifecycle) != len(want) {
		t.Fatalf("tool lifecycle len=%d want %d\ngot=%+v", len(lifecycle), len(want), lifecycle)
	}
	for i := range want {
		got, w := lifecycle[i], want[i]
		if got.Kind != w.Kind || got.ToolCallID != w.ToolCallID || got.ToolName != w.ToolName || got.Delta != w.Delta {
			t.Fatalf("lifecycle[%d]=%+v want %+v", i, got, w)
		}
	}
}

// idOnlyToolCallSSE emits ToolCallStarted on an id-only tool_call chunk (no name).
func idOnlyToolCallSSE() string {
	return strings.Join([]string{
		`data: {"id":"cc_idonly","choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_idonly","type":"function","function":{}}]},"finish_reason":null}]}`,
		`data: {"id":"cc_idonly","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":1}"}}]},"finish_reason":null}]}`,
		`data: {"id":"cc_idonly","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		"",
	}, "\n")
}

// idThenNameToolCallSSE starts with id-only then delivers the name in a later chunk.
func idThenNameToolCallSSE() string {
	return strings.Join([]string{
		`data: {"id":"cc_split","choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_split","type":"function","function":{}}]},"finish_reason":null}]}`,
		`data: {"id":"cc_split","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"get_weather","arguments":"{\"city\""}}]},"finish_reason":null}]}`,
		`data: {"id":"cc_split","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"NYC\"}"}}]},"finish_reason":null}]}`,
		`data: {"id":"cc_split","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		"",
	}, "\n")
}

func TestDecodeChatSSE_toolCallStartedOnIDOnlyChunk(t *testing.T) {
	t.Parallel()
	events := collectChatSSE(t, idOnlyToolCallSSE())
	lifecycle := toolLifecycle(events)

	want := []aipapi.Event{
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_idonly", ToolName: ""},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_idonly", Delta: `{"q":1}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_idonly"},
	}
	if len(lifecycle) != len(want) {
		t.Fatalf("tool lifecycle len=%d want %d\ngot=%+v", len(lifecycle), len(want), lifecycle)
	}
	for i := range want {
		got, w := lifecycle[i], want[i]
		if got.Kind != w.Kind || got.ToolCallID != w.ToolCallID || got.ToolName != w.ToolName || got.Delta != w.Delta {
			t.Fatalf("lifecycle[%d]=%+v want %+v", i, got, w)
		}
	}
}

func TestDecodeChatSSE_toolCallStartedOnceOnIDThenNameSplit(t *testing.T) {
	t.Parallel()
	events := collectChatSSE(t, idThenNameToolCallSSE())
	lifecycle := toolLifecycle(events)

	started := 0
	for _, ev := range lifecycle {
		if ev.Kind == aipapi.EventToolCallStarted {
			started++
			if ev.ToolCallID != "call_split" || ev.ToolName != "" {
				t.Fatalf("Started=%+v want id=call_split name=\"\"", ev)
			}
		}
	}
	if started != 1 {
		t.Fatalf("ToolCallStarted count=%d want exactly one (essential does not re-emit on name-only chunk)", started)
	}
}

func TestDecodeChatSSE_multiToolCallFinishByIndex(t *testing.T) {
	t.Parallel()
	events := collectChatSSE(t, multiToolCallFinishSSE())
	lifecycle := toolLifecycle(events)

	want := []aipapi.Event{
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_ma", ToolName: "alpha"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_mb", ToolName: "beta"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_ma", Delta: `{"a":1}`},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_mb", Delta: `{"b":2}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_ma"},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_mb"},
	}
	if len(lifecycle) != len(want) {
		t.Fatalf("tool lifecycle len=%d want %d\ngot=%+v", len(lifecycle), len(want), lifecycle)
	}
	for i := range want {
		got, w := lifecycle[i], want[i]
		if got.Kind != w.Kind || got.ToolCallID != w.ToolCallID || got.ToolName != w.ToolName || got.Delta != w.Delta {
			t.Fatalf("lifecycle[%d]=%+v want %+v", i, got, w)
		}
	}
}
