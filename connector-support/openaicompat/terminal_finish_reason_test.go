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

func terminalSSE(chunks ...string) string {
	lines := make([]string, 0, len(chunks)+1)
	for _, c := range chunks {
		lines = append(lines, "data: "+c)
	}
	lines = append(lines, "data: [DONE]", "")
	return strings.Join(lines, "\n")
}

func chatChunk(content, finish string) string {
	fr := "null"
	if finish != "" {
		fr = `"` + finish + `"`
	}
	delta := `{}`
	if content != "" {
		delta = `{"content":"` + content + `"}`
	}
	return `{"id":"cc","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + fr + `}]}`
}

func collectTerminal(t *testing.T, sse, jsonBody string, stream bool) aipapi.Event {
	t.Helper()
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{
		ChatStreamSSE:     sse,
		ChatNonStreamJSON: jsonBody,
	}))
	t.Cleanup(srv.Close)
	c := &openaicompat.Client{
		BaseURL:    srv.URL + "/v1",
		APIKey:     "sk-test",
		HTTPClient: srv.Client(),
	}
	call := sampleCall()
	if !stream {
		call.Invocation.DeliveryMode = aipapi.DeliveryModeNonStreaming
		call.Invocation.TransportMode = aipapi.TransportModeNonStreaming
	}
	es, err := c.Open(context.Background(), call, "emu-model", openaicompat.FlavorChat)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = es.Close() })
	var terminal aipapi.Event
	sawTerminal := false
	for {
		ev, err := es.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == aipapi.EventResponseFinished {
			terminal = ev
			sawTerminal = true
		}
	}
	if !sawTerminal {
		t.Fatal("no EventResponseFinished emitted")
	}
	return terminal
}

func chatJSON(content, finish string) string {
	return `{"id":"cc","object":"chat.completion","created":0,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"` + content + `"},"finish_reason":"` + finish + `"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`
}

func chatToolJSON() string {
	return `{"id":"cc","object":"chat.completion","created":0,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_ab","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`
}

func collectResponsesTerminal(t *testing.T, sse, jsonBody string, stream bool) aipapi.Event {
	t.Helper()
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{
		ResponsesStreamSSE:     sse,
		ResponsesNonStreamJSON: jsonBody,
	}))
	t.Cleanup(srv.Close)
	c := &openaicompat.Client{
		BaseURL:    srv.URL + "/v1",
		APIKey:     "sk-test",
		HTTPClient: srv.Client(),
		Transport:  openaicompat.TransportChatAndResponses,
	}
	call := sampleCall()
	call.Invocation.Operation = aipapi.OperationOpenAIResponses
	if !stream {
		call.Invocation.DeliveryMode = aipapi.DeliveryModeNonStreaming
		call.Invocation.TransportMode = aipapi.TransportModeNonStreaming
	}
	es, err := c.Open(context.Background(), call, "emu-model", openaicompat.FlavorResponses)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = es.Close() })
	var terminal aipapi.Event
	sawTerminal := false
	for {
		ev, err := es.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == aipapi.EventResponseFinished {
			terminal = ev
			sawTerminal = true
		}
	}
	if !sawTerminal {
		t.Fatal("no EventResponseFinished emitted")
	}
	return terminal
}

func collectResponsesEvents(t *testing.T, sse, jsonBody string, stream bool) []aipapi.Event {
	t.Helper()
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{
		ResponsesStreamSSE:     sse,
		ResponsesNonStreamJSON: jsonBody,
	}))
	t.Cleanup(srv.Close)
	c := &openaicompat.Client{
		BaseURL:    srv.URL + "/v1",
		APIKey:     "sk-test",
		HTTPClient: srv.Client(),
		Transport:  openaicompat.TransportChatAndResponses,
	}
	call := sampleCall()
	call.Invocation.Operation = aipapi.OperationOpenAIResponses
	if !stream {
		call.Invocation.DeliveryMode = aipapi.DeliveryModeNonStreaming
		call.Invocation.TransportMode = aipapi.TransportModeNonStreaming
	}
	es, err := c.Open(context.Background(), call, "emu-model", openaicompat.FlavorResponses)
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

func assertToolFinished(t *testing.T, events []aipapi.Event, id string) {
	t.Helper()
	for _, ev := range events {
		if ev.Kind == aipapi.EventToolCallFinished && ev.ToolCallID == id {
			return
		}
	}
	t.Fatalf("no ToolCallFinished for %q; events=%+v", id, events)
}

// TestResponsesToolLifecycle pins function_call mapping on both transports:
// added/delta frames build the lifecycle, completed closes it clean.
func TestResponsesToolLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("stream", func(t *testing.T) {
		t.Parallel()
		events := collectResponsesEvents(t, responsesToolSSE(), "", true)
		assertToolFinished(t, events, "call_ab")
	})

	t.Run("nonstream", func(t *testing.T) {
		t.Parallel()
		events := collectResponsesEvents(t, "", responsesToolJSON(), false)
		assertToolFinished(t, events, "call_ab")
	})
}

// TestUsagePresenceExplicit pins that connector usage deltas carry explicit
// presence on every path: the staged usage cells assert presence, and the
// host adapter used to drop it at the boundary.
func TestUsagePresenceExplicit(t *testing.T) {
	t.Parallel()

	assertPresence := func(t *testing.T, path string, events []aipapi.Event) {
		t.Helper()
		for _, ev := range events {
			if ev.Kind == aipapi.EventUsageDelta {
				if !ev.UsagePresence.Any() {
					t.Fatalf("%s: usage without explicit presence: %+v", path, ev)
				}
				return
			}
		}
		t.Fatalf("%s: no usage delta; events=%+v", path, events)
	}

	t.Run("chat_stream", func(t *testing.T) {
		t.Parallel()
		sse := "data: " + `{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}` + "\n\n" +
			"data: " + `{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}` + "\n\n" +
			"data: " + `{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
			"data: [DONE]\n\n"
		assertPresence(t, "chat stream", collectChatSSE(t, sse))
	})

	t.Run("chat_nonstream", func(t *testing.T) {
		t.Parallel()
		body := `{"id":"c","object":"chat.completion","created":0,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
		srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{ChatNonStreamJSON: body}))
		t.Cleanup(srv.Close)
		c := &openaicompat.Client{BaseURL: srv.URL + "/v1", APIKey: "sk-test", HTTPClient: srv.Client()}
		call := sampleCall()
		call.Invocation.DeliveryMode = aipapi.DeliveryModeNonStreaming
		call.Invocation.TransportMode = aipapi.TransportModeNonStreaming
		es, err := c.Open(context.Background(), call, "emu-model", openaicompat.FlavorChat)
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
		assertPresence(t, "chat non-stream", events)
	})

	t.Run("responses_stream", func(t *testing.T) {
		t.Parallel()
		assertPresence(t, "responses stream", collectResponsesEvents(t, responsesCompletedSSE(), "", true))
	})

	t.Run("responses_nonstream", func(t *testing.T) {
		t.Parallel()
		assertPresence(t, "responses non-stream", collectResponsesEvents(t, "", responsesJSON("completed", ""), false))
	})
}

func responsesIncompleteSSE(reason string) string {
	return "data: " + `{"type":"response.output_text.delta","delta":"hello"}` + "\n\n" +
		"data: " + `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"` + reason + `"},"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n" +
		"data: [DONE]\n\n"
}

func responsesCompletedSSE() string {
	return "data: " + `{"type":"response.output_text.delta","delta":"hello"}` + "\n\n" +
		"data: " + `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n" +
		"data: [DONE]\n\n"
}

func responsesToolSSE() string {
	return "data: " + `{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_ab","name":"get_weather","arguments":""}}` + "\n\n" +
		"data: " + `{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_1","delta":"{}"}` + "\n\n" +
		"data: " + `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_ab","name":"get_weather","arguments":"{}","status":"completed"}}` + "\n\n" +
		"data: " + `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n" +
		"data: [DONE]\n\n"
}

func responsesToolJSON() string {
	return `{"id":"resp","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_ab","name":"get_weather","arguments":"{}","status":"completed"}],"usage":{"input_tokens":1,"output_tokens":1}}`
}

func responsesJSON(status, reason string) string {
	details := "null"
	if reason != "" {
		details = `{"reason":"` + reason + `"}`
	}
	return `{"id":"resp","status":"` + status + `","incomplete_details":` + details + `,"output":[{"content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`
}

// TestResponsesTerminalFinishReason pins the responses-flavor mapping:
// response.incomplete carries incomplete status with max_output_tokens
// folded to length and content_filter verbatim; completed stays clean.
func TestResponsesTerminalFinishReason(t *testing.T) {
	t.Parallel()

	t.Run("stream", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name       string
			sse        string
			wantStatus string
			wantFinish string
		}{
			{name: "limit_truncation", sse: responsesIncompleteSSE("max_output_tokens"), wantStatus: "incomplete", wantFinish: "length"},
			{name: "content_filter_refusal", sse: responsesIncompleteSSE("content_filter"), wantStatus: "incomplete", wantFinish: "content_filter"},
			{name: "completed_clean", sse: responsesCompletedSSE()},
			{
				// A bare completed frame still opens and closes the frame.
				name: "completed_bare",
				sse: "data: " + `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n" +
					"data: [DONE]\n\n",
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				terminal := collectResponsesTerminal(t, tc.sse, "", true)
				if terminal.ResponseStatus != tc.wantStatus {
					t.Fatalf("ResponseStatus = %q, want %q", terminal.ResponseStatus, tc.wantStatus)
				}
				if terminal.FinishReason != tc.wantFinish {
					t.Fatalf("FinishReason = %q, want %q", terminal.FinishReason, tc.wantFinish)
				}
			})
		}
	})

	t.Run("nonstream", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name       string
			status     string
			reason     string
			wantStatus string
			wantFinish string
		}{
			{name: "limit_truncation", status: "incomplete", reason: "max_output_tokens", wantStatus: "incomplete", wantFinish: "length"},
			{name: "content_filter_refusal", status: "incomplete", reason: "content_filter", wantStatus: "incomplete", wantFinish: "content_filter"},
			{name: "completed_clean", status: "completed", reason: ""},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				terminal := collectResponsesTerminal(t, "", responsesJSON(tc.status, tc.reason), false)
				if terminal.ResponseStatus != tc.wantStatus {
					t.Fatalf("ResponseStatus = %q, want %q", terminal.ResponseStatus, tc.wantStatus)
				}
				if terminal.FinishReason != tc.wantFinish {
					t.Fatalf("FinishReason = %q, want %q", terminal.FinishReason, tc.wantFinish)
				}
			})
		}
	})
}

// TestChatTerminalFinishReason pins the connector-shared terminal mapping:
// output-token-limit carries incomplete/length, refusal is carried verbatim
// with no incomplete classification, and every other stop stays clean.
func TestChatTerminalFinishReason(t *testing.T) {
	t.Parallel()

	t.Run("stream", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name       string
			chunks     []string
			wantStatus string
			wantFinish string
		}{
			{
				name:       "length_truncation",
				wantStatus: "incomplete",
				wantFinish: "length",
				chunks: []string{
					chatChunk("cut off he", ""),
					chatChunk("", "length"),
				},
			},
			{
				name:       "content_filter_refusal",
				wantFinish: "content_filter",
				chunks: []string{
					chatChunk("I cannot help with that", ""),
					chatChunk("", "content_filter"),
				},
			},
			{
				name:   "clean_stop",
				chunks: []string{chatChunk("hello", ""), chatChunk("", "stop")},
			},
			{
				name: "tool_calls_stop",
				chunks: []string{
					`{"id":"cc","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_ab","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},"finish_reason":null}]}`,
					chatChunk("", "tool_calls"),
				},
			},
			{
				name:   "empty_finish_reason",
				chunks: []string{chatChunk("hello", ""), chatChunk("", "")},
			},
			{
				// A terminal-only chunk still opens and closes the frame.
				name:   "empty_turn_stream",
				chunks: []string{chatChunk("", "stop")},
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				terminal := collectTerminal(t, terminalSSE(tc.chunks...), "", true)
				if terminal.ResponseStatus != tc.wantStatus {
					t.Fatalf("ResponseStatus = %q, want %q", terminal.ResponseStatus, tc.wantStatus)
				}
				if terminal.FinishReason != tc.wantFinish {
					t.Fatalf("FinishReason = %q, want %q", terminal.FinishReason, tc.wantFinish)
				}
			})
		}
	})

	t.Run("nonstream", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name       string
			content    string
			finish     string
			toolJSON   bool
			wantStatus string
			wantFinish string
		}{
			{name: "length_truncation", content: "cut", finish: "length", wantStatus: "incomplete", wantFinish: "length"},
			{name: "content_filter_refusal", content: "I cannot help with that", finish: "content_filter", wantFinish: "content_filter"},
			{name: "clean_stop", content: "hello", finish: "stop"},
			{name: "tool_calls_stop", toolJSON: true},
			{name: "empty_finish_reason", content: "hello", finish: ""},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				body := chatJSON(tc.content, tc.finish)
				if tc.toolJSON {
					body = chatToolJSON()
				}
				terminal := collectTerminal(t, "", body, false)
				if terminal.ResponseStatus != tc.wantStatus {
					t.Fatalf("ResponseStatus = %q, want %q", terminal.ResponseStatus, tc.wantStatus)
				}
				if terminal.FinishReason != tc.wantFinish {
					t.Fatalf("FinishReason = %q, want %q", terminal.FinishReason, tc.wantFinish)
				}
			})
		}
	})
}
