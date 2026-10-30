package cohere_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/cohere/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type capturedCohereRequest struct {
	raw  []byte
	body map[string]any
}

func openCohereTestClient(t *testing.T, handler http.Handler) *service.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &service.Client{
		Config:        service.Config{APIOrigin: srv.URL},
		TokenProvider: service.StaticTokenProvider("test-token"),
		HTTPClient:    srv.Client(),
	}
}

func serveCohereCaptureJSON(captured *capturedCohereRequest, responseBody string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		captured.raw = raw
		_ = json.Unmarshal(raw, &captured.body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(responseBody))
	})
}

func cohereTestCall(msgs []aipapi.Message, tools []aipapi.ToolDef, choice aipapi.ToolChoice, streaming bool) aipapi.Call {
	call := aipapi.Call{
		Messages:   msgs,
		Tools:      tools,
		ToolChoice: choice,
	}
	if streaming {
		call.Invocation.DeliveryMode = aipapi.DeliveryModeStreaming
		call.Invocation.TransportMode = aipapi.TransportModeStreaming
	} else {
		call.Invocation.DeliveryMode = aipapi.DeliveryModeNonStreaming
		call.Invocation.TransportMode = aipapi.TransportModeNonStreaming
	}
	call.Invocation.Operation = aipapi.OperationOpenAIChatCompletions
	return call
}

func cohereUserCall(text string) aipapi.Call {
	return cohereTestCall(
		[]aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(text)}}},
		nil,
		aipapi.ToolChoice{},
		false,
	)
}

func drainCohereEvents(ctx context.Context, t *testing.T, s aipapi.ManagedEventStream) []aipapi.Event {
	t.Helper()
	defer func() { _ = s.Close() }()
	var out []aipapi.Event
	for {
		ev, err := s.Recv(ctx)
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("recv failed: %v", err)
		}
		out = append(out, ev)
	}
}

func eventKinds(evs []aipapi.Event) []aipapi.EventKind {
	out := make([]aipapi.EventKind, 0, len(evs))
	for _, ev := range evs {
		out = append(out, ev.Kind)
	}
	return out
}

const cohereSimpleTextResponse = `{"id":"c-1","finish_reason":"COMPLETE","message":{"role":"assistant","content":"ok"}}`

func cohereWireTools(t *testing.T, body map[string]any) []any {
	t.Helper()
	raw, ok := body["tools"]
	if !ok {
		t.Fatalf("expected tools in request body, got keys: %v", mapKeys(body))
	}
	arr, ok := raw.([]any)
	if !ok {
		t.Fatalf("expected tools array, got %T", raw)
	}
	return arr
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestCohereTools_RequestToolDefinitions(t *testing.T) {
	t.Parallel()

	weatherParams := `{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`

	t.Run("single tool maps to Cohere function definition", func(t *testing.T) {
		t.Parallel()
		var captured capturedCohereRequest
		cl := openCohereTestClient(t, serveCohereCaptureJSON(&captured, cohereSimpleTextResponse))

		call := cohereUserCall("weather?")
		call.Tools = []aipapi.ToolDef{{
			Name:        "get_weather",
			Description: "Get the weather",
			Parameters:  json.RawMessage(weatherParams),
		}}
		stream, err := cl.Open(context.Background(), call, "command-r")
		if err != nil {
			t.Fatalf("open failed: %v", err)
		}
		_ = stream.Close()

		arr := cohereWireTools(t, captured.body)
		if len(arr) != 1 {
			t.Fatalf("expected 1 tool, got %d", len(arr))
		}
		tool, ok := arr[0].(map[string]any)
		if !ok {
			t.Fatalf("expected tool object, got %T", arr[0])
		}
		if tool["type"] != "function" {
			t.Fatalf("expected type function, got %v", tool["type"])
		}
		fn, ok := tool["function"].(map[string]any)
		if !ok {
			t.Fatalf("expected function object, got %T", tool["function"])
		}
		if fn["name"] != "get_weather" {
			t.Fatalf("unexpected tool name: %v", fn["name"])
		}
		if fn["description"] != "Get the weather" {
			t.Fatalf("unexpected tool description: %v", fn["description"])
		}
		params, ok := fn["parameters"].(map[string]any)
		if !ok {
			t.Fatalf("expected parameters object, got %T", fn["parameters"])
		}
		if params["type"] != "object" {
			t.Fatalf("unexpected parameters type: %v", params["type"])
		}
		props, ok := params["properties"].(map[string]any)
		if !ok || props["city"] == nil {
			t.Fatalf("unexpected parameters properties: %v", params["properties"])
		}
	})

	t.Run("parallel tools preserve order", func(t *testing.T) {
		t.Parallel()
		var captured capturedCohereRequest
		cl := openCohereTestClient(t, serveCohereCaptureJSON(&captured, cohereSimpleTextResponse))

		call := cohereUserCall("both?")
		call.Tools = []aipapi.ToolDef{
			{Name: "get_weather", Parameters: json.RawMessage(weatherParams)},
			{Name: "get_time", Parameters: json.RawMessage(`{"type":"object","properties":{"tz":{"type":"string"}}}`)},
		}
		stream, err := cl.Open(context.Background(), call, "command-r")
		if err != nil {
			t.Fatalf("open failed: %v", err)
		}
		_ = stream.Close()

		arr := cohereWireTools(t, captured.body)
		if len(arr) != 2 {
			t.Fatalf("expected 2 tools, got %d", len(arr))
		}
		for i, want := range []string{"get_weather", "get_time"} {
			tool, ok := arr[i].(map[string]any)
			if !ok {
				t.Fatalf("tool %d is not map[string]any", i)
			}
			fn, ok := tool["function"].(map[string]any)
			if !ok {
				t.Fatalf("tool %d function is not map[string]any", i)
			}
			if fn["name"] != want {
				t.Fatalf("tool %d: expected %q, got %v", i, want, fn["name"])
			}
		}
	})

	t.Run("empty parameters default to object schema", func(t *testing.T) {
		t.Parallel()
		var captured capturedCohereRequest
		cl := openCohereTestClient(t, serveCohereCaptureJSON(&captured, cohereSimpleTextResponse))

		call := cohereUserCall("hi")
		call.Tools = []aipapi.ToolDef{{Name: "no_args"}}
		stream, err := cl.Open(context.Background(), call, "command-r")
		if err != nil {
			t.Fatalf("open failed: %v", err)
		}
		_ = stream.Close()

		arr := cohereWireTools(t, captured.body)
		tool, ok := arr[0].(map[string]any)
		if !ok {
			t.Fatalf("tool 0 is not map[string]any")
		}
		fn, ok := tool["function"].(map[string]any)
		if !ok {
			t.Fatalf("tool 0 function is not map[string]any")
		}
		params, ok := fn["parameters"].(map[string]any)
		if !ok || params["type"] != "object" {
			t.Fatalf("expected default object schema, got %v", fn["parameters"])
		}
	})

	t.Run("invalid parameters fail closed naming the tool", func(t *testing.T) {
		t.Parallel()
		var captured capturedCohereRequest
		cl := openCohereTestClient(t, serveCohereCaptureJSON(&captured, cohereSimpleTextResponse))

		call := cohereUserCall("hi")
		call.Tools = []aipapi.ToolDef{{Name: "broken", Parameters: json.RawMessage(`{"type":`)}}
		_, err := cl.Open(context.Background(), call, "command-r")
		if err == nil {
			t.Fatalf("expected invalid parameters to fail closed, got nil")
		}
		if !strings.Contains(err.Error(), "broken") {
			t.Fatalf("expected error to name the tool, got %q", err.Error())
		}
	})
}

func TestCohereTools_ToolChoiceMapping(t *testing.T) {
	t.Parallel()

	tools := []aipapi.ToolDef{{
		Name:       "get_weather",
		Parameters: json.RawMessage(`{"type":"object"}`),
	}}

	none := "NONE"
	required := "REQUIRED"

	cases := []struct {
		name    string
		choice  aipapi.ToolChoice
		tools   []aipapi.ToolDef
		want    *string
		wantErr string
	}{
		{name: "auto omits tool_choice", choice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}, tools: tools, want: nil},
		{name: "empty mode omits tool_choice", choice: aipapi.ToolChoice{}, tools: tools, want: nil},
		{name: "none maps to NONE", choice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone}, tools: nil, want: &none},
		{name: "any maps to REQUIRED", choice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny}, tools: tools, want: &required},
		{name: "required without name maps to REQUIRED", choice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired}, tools: tools, want: &required},
		{name: "required with name fails closed", choice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: "get_weather"}, tools: tools, wantErr: "get_weather"},
		{name: "any without tools fails closed", choice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny}, tools: nil, wantErr: "at least one tool"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var captured capturedCohereRequest
			cl := openCohereTestClient(t, serveCohereCaptureJSON(&captured, cohereSimpleTextResponse))

			call := cohereUserCall("hi")
			call.Tools = tc.tools
			call.ToolChoice = tc.choice
			stream, err := cl.Open(context.Background(), call, "command-r")
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %q", tc.wantErr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("open failed: %v", err)
			}
			_ = stream.Close()
			raw, present := captured.body["tool_choice"]
			if tc.want == nil {
				if present {
					t.Fatalf("expected tool_choice to be omitted, got %v", raw)
				}
				return
			}
			if !present {
				t.Fatalf("expected tool_choice %q, got omitted", *tc.want)
			}
			if raw != *tc.want {
				t.Fatalf("expected tool_choice %q, got %v", *tc.want, raw)
			}
		})
	}
}

const cohereParallelToolResponse = `{
	"id": "msg-tools-1",
	"finish_reason": "TOOL_CALL",
	"message": {
		"role": "assistant",
		"content": [{"type":"text","text":"Let me check."}],
		"tool_calls": [
			{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"NYC\"}"}},
			{"id":"call_2","type":"function","function":{"name":"get_time","arguments":"{\"tz\":\"EST\"}"}}
		]
	}
}`

func TestCohereTools_NonStreamingToolCallsIDCorrelation(t *testing.T) {
	t.Parallel()
	var captured capturedCohereRequest
	cl := openCohereTestClient(t, serveCohereCaptureJSON(&captured, cohereParallelToolResponse))

	call := cohereUserCall("weather and time?")
	call.Tools = []aipapi.ToolDef{
		{Name: "get_weather", Parameters: json.RawMessage(`{"type":"object"}`)},
		{Name: "get_time", Parameters: json.RawMessage(`{"type":"object"}`)},
	}
	stream, err := cl.Open(context.Background(), call, "command-r")
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}
	evs := drainCohereEvents(context.Background(), t, stream)

	wantKinds := []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventTextDelta,
		aipapi.EventToolCallStarted,
		aipapi.EventToolCallArgsDelta,
		aipapi.EventToolCallFinished,
		aipapi.EventToolCallStarted,
		aipapi.EventToolCallArgsDelta,
		aipapi.EventToolCallFinished,
		aipapi.EventResponseFinished,
	}
	gotKinds := eventKinds(evs)
	if fmt.Sprintf("%v", gotKinds) != fmt.Sprintf("%v", wantKinds) {
		t.Fatalf("unexpected event kinds:\n got: %v\nwant: %v", gotKinds, wantKinds)
	}

	if evs[2].Delta != "Let me check." {
		t.Fatalf("unexpected text delta: %q", evs[2].Delta)
	}
	// First tool call correlation.
	if evs[3].ToolCallID != "call_1" || evs[3].ToolName != "get_weather" {
		t.Fatalf("unexpected first tool start: %+v", evs[3])
	}
	if evs[4].ToolCallID != "call_1" || evs[4].Delta != `{"city":"NYC"}` {
		t.Fatalf("unexpected first tool args: %+v", evs[4])
	}
	if evs[5].ToolCallID != "call_1" {
		t.Fatalf("unexpected first tool finish: %+v", evs[5])
	}
	// Second (parallel) tool call correlation.
	if evs[6].ToolCallID != "call_2" || evs[6].ToolName != "get_time" {
		t.Fatalf("unexpected second tool start: %+v", evs[6])
	}
	if evs[7].ToolCallID != "call_2" || evs[7].Delta != `{"tz":"EST"}` {
		t.Fatalf("unexpected second tool args: %+v", evs[7])
	}
	if evs[8].ToolCallID != "call_2" {
		t.Fatalf("unexpected second tool finish: %+v", evs[8])
	}
	if evs[9].FinishReason != "TOOL_CALL" {
		t.Fatalf("expected TOOL_CALL finish reason, got %q", evs[9].FinishReason)
	}
}

func TestCohereTools_ToolResultRoundtrip(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		assistantPart aipapi.Part
		toolPart      aipapi.Part
		wantArgs      string
		wantResult    string
	}{
		{
			name:          "raw args and text result",
			assistantPart: aipapi.Part{Kind: aipapi.PartJSON, ToolCallID: "call_hist_1", ToolName: "get_weather", Content: json.RawMessage(`{"city":"Paris"}`)},
			toolPart:      aipapi.Part{Kind: aipapi.PartToolResult, ToolCallID: "call_hist_1", ToolName: "get_weather", Text: "sunny"},
			wantArgs:      `{"city":"Paris"}`,
			wantResult:    "sunny",
		},
		{
			name: "envelope args and JSON string result",
			assistantPart: aipapi.Part{Kind: aipapi.PartJSON, ToolCallID: "call_hist_1", ToolName: "get_weather", Content: json.RawMessage(
				`{"id":"call_hist_1","type":"function","function":{"name":"get_weather","arguments":{"city":"Paris"}}}`)},
			toolPart:   aipapi.Part{Kind: aipapi.PartToolResult, ToolCallID: "call_hist_1", ToolName: "get_weather", Content: json.RawMessage(`"sunny"`)},
			wantArgs:   `{"city":"Paris"}`,
			wantResult: "sunny",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var captured capturedCohereRequest
			cl := openCohereTestClient(t, serveCohereCaptureJSON(&captured, cohereSimpleTextResponse))

			msgs := []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("weather?")}},
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{tc.assistantPart}},
				{Role: aipapi.RoleTool, Parts: []aipapi.Part{tc.toolPart}},
			}
			call := cohereTestCall(msgs, []aipapi.ToolDef{{Name: "get_weather"}}, aipapi.ToolChoice{}, false)
			stream, err := cl.Open(context.Background(), call, "command-r")
			if err != nil {
				t.Fatalf("open failed: %v", err)
			}
			_ = stream.Close()

			messages, ok := captured.body["messages"].([]any)
			if !ok || len(messages) != 3 {
				t.Fatalf("expected 3 wire messages, got %v", captured.body["messages"])
			}
			assistant, ok := messages[1].(map[string]any)
			if !ok || assistant["role"] != "assistant" {
				t.Fatalf("unexpected assistant message: %v", messages[1])
			}
			wireCalls, ok := assistant["tool_calls"].([]any)
			if !ok || len(wireCalls) != 1 {
				t.Fatalf("expected 1 wire tool_call, got %v", assistant["tool_calls"])
			}
			wireCall, ok := wireCalls[0].(map[string]any)
			if !ok {
				t.Fatalf("expected tool_call object, got %T", wireCalls[0])
			}
			if wireCall["id"] != "call_hist_1" {
				t.Fatalf("unexpected tool_call id: %v", wireCall["id"])
			}
			fn, ok := wireCall["function"].(map[string]any)
			if !ok || fn["name"] != "get_weather" {
				t.Fatalf("unexpected tool_call function: %v", wireCall["function"])
			}
			args, ok := fn["arguments"].(string)
			if !ok {
				t.Fatalf("expected arguments string, got %T", fn["arguments"])
			}
			var gotArgs, wantArgs any
			if err := json.Unmarshal([]byte(args), &gotArgs); err != nil {
				t.Fatalf("arguments must be JSON: %q", args)
			}
			if err := json.Unmarshal([]byte(tc.wantArgs), &wantArgs); err != nil {
				t.Fatalf("bad test fixture: %v", err)
			}
			if fmt.Sprintf("%v", gotArgs) != fmt.Sprintf("%v", wantArgs) {
				t.Fatalf("unexpected arguments: %q", args)
			}

			toolMsg, ok := messages[2].(map[string]any)
			if !ok || toolMsg["role"] != "tool" {
				t.Fatalf("unexpected tool message: %v", messages[2])
			}
			if toolMsg["tool_call_id"] != "call_hist_1" {
				t.Fatalf("unexpected tool_call_id: %v", toolMsg["tool_call_id"])
			}
			if toolMsg["content"] != tc.wantResult {
				t.Fatalf("unexpected tool content: %v", toolMsg["content"])
			}
		})
	}
}

func cohereSSEHandler(chunks []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, c := range chunks {
			_, _ = io.WriteString(w, c+"\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	})
}

func TestCohereTools_StreamingToolCallDeltas(t *testing.T) {
	t.Parallel()
	cl := openCohereTestClient(t, cohereSSEHandler([]string{
		`data: {"type":"message-start","id":"msg-s1","delta":{"message":{"role":"assistant"}}}`,
		`data: {"type":"tool-plan-delta","index":0,"delta":{"message":{"tool_plan":"I will check "}}}`,
		`data: {"type":"tool-call-start","index":0,"delta":{"message":{"tool_calls":{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}}}}`,
		`data: {"type":"tool-call-delta","index":0,"delta":{"message":{"tool_calls":{"function":{"arguments":"{\"city\":"}}}}}`,
		`data: {"type":"tool-call-delta","index":0,"delta":{"message":{"tool_calls":{"function":{"arguments":"\"NYC\"}"}}}}}`,
		`data: {"type":"tool-call-start","index":1,"delta":{"message":{"tool_calls":{"id":"call_2","type":"function","function":{"name":"get_time"}}}}}`,
		`data: {"type":"tool-call-delta","index":1,"delta":{"message":{"tool_calls":{"function":{"arguments":"{\"tz\":\"EST\"}"}}}}}`,
		`data: {"type":"tool-call-end","index":0}`,
		`data: {"type":"tool-call-end","index":1}`,
		`data: {"type":"message-end","id":"msg-s1","delta":{"finish_reason":"TOOL_CALL"}}`,
	}))

	call := cohereUserCall("weather and time?")
	call.Tools = []aipapi.ToolDef{{Name: "get_weather"}, {Name: "get_time"}}
	call.Invocation.DeliveryMode = aipapi.DeliveryModeStreaming
	call.Invocation.TransportMode = aipapi.TransportModeStreaming

	stream, err := cl.Open(context.Background(), call, "command-r")
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}
	evs := drainCohereEvents(context.Background(), t, stream)

	wantKinds := []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventReasoningDelta,
		aipapi.EventToolCallStarted,
		aipapi.EventToolCallArgsDelta,
		aipapi.EventToolCallArgsDelta,
		aipapi.EventToolCallStarted,
		aipapi.EventToolCallArgsDelta,
		aipapi.EventToolCallFinished,
		aipapi.EventToolCallFinished,
		aipapi.EventResponseFinished,
	}
	gotKinds := eventKinds(evs)
	if fmt.Sprintf("%v", gotKinds) != fmt.Sprintf("%v", wantKinds) {
		t.Fatalf("unexpected event kinds:\n got: %v\nwant: %v", gotKinds, wantKinds)
	}

	if evs[2].Delta != "I will check " {
		t.Fatalf("unexpected tool plan delta: %+v", evs[2])
	}
	if evs[3].ToolCallID != "call_1" || evs[3].ToolName != "get_weather" {
		t.Fatalf("unexpected first tool start: %+v", evs[3])
	}
	if evs[4].ToolCallID != "call_1" || evs[4].Delta != `{"city":` {
		t.Fatalf("unexpected first args chunk: %+v", evs[4])
	}
	if evs[5].ToolCallID != "call_1" || evs[5].Delta != `"NYC"}` {
		t.Fatalf("unexpected second args chunk: %+v", evs[5])
	}
	if evs[6].ToolCallID != "call_2" || evs[6].ToolName != "get_time" {
		t.Fatalf("unexpected second tool start: %+v", evs[6])
	}
	if evs[7].ToolCallID != "call_2" || evs[7].Delta != `{"tz":"EST"}` {
		t.Fatalf("unexpected second tool args: %+v", evs[7])
	}
	if evs[8].ToolCallID != "call_1" {
		t.Fatalf("unexpected first tool finish: %+v", evs[8])
	}
	if evs[9].ToolCallID != "call_2" {
		t.Fatalf("unexpected second tool finish: %+v", evs[9])
	}
	if evs[10].FinishReason != "TOOL_CALL" {
		t.Fatalf("expected TOOL_CALL finish reason, got %q", evs[10].FinishReason)
	}
}

func TestCohereTools_StreamingDeltaBufferedBeforeStart(t *testing.T) {
	t.Parallel()
	cl := openCohereTestClient(t, cohereSSEHandler([]string{
		`data: {"type":"message-start","id":"msg-s2","delta":{"message":{"role":"assistant"}}}`,
		`data: {"type":"tool-call-delta","index":0,"delta":{"message":{"tool_calls":{"function":{"arguments":"{\"a\":1}"}}}}}`,
		`data: {"type":"tool-call-start","index":0,"delta":{"message":{"tool_calls":{"id":"call_9","type":"function","function":{"name":"late_tool"}}}}}`,
		`data: {"type":"tool-call-end","index":0}`,
		`data: {"type":"message-end","id":"msg-s2","delta":{"finish_reason":"TOOL_CALL"}}`,
	}))

	call := cohereUserCall("hi")
	call.Tools = []aipapi.ToolDef{{Name: "late_tool"}}
	call.Invocation.DeliveryMode = aipapi.DeliveryModeStreaming
	call.Invocation.TransportMode = aipapi.TransportModeStreaming

	stream, err := cl.Open(context.Background(), call, "command-r")
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}
	evs := drainCohereEvents(context.Background(), t, stream)

	wantKinds := []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventToolCallStarted,
		aipapi.EventToolCallArgsDelta,
		aipapi.EventToolCallFinished,
		aipapi.EventResponseFinished,
	}
	gotKinds := eventKinds(evs)
	if fmt.Sprintf("%v", gotKinds) != fmt.Sprintf("%v", wantKinds) {
		t.Fatalf("unexpected event kinds:\n got: %v\nwant: %v", gotKinds, wantKinds)
	}
	if evs[2].ToolCallID != "call_9" || evs[2].ToolName != "late_tool" {
		t.Fatalf("unexpected tool start: %+v", evs[2])
	}
	if evs[3].ToolCallID != "call_9" || evs[3].Delta != `{"a":1}` {
		t.Fatalf("expected buffered args after start, got %+v", evs[3])
	}
}

func TestCohereTools_UnsupportedSubSemanticsFailClosed(t *testing.T) {
	t.Parallel()

	t.Run("assistant tool call without id and name", func(t *testing.T) {
		t.Parallel()
		var captured capturedCohereRequest
		cl := openCohereTestClient(t, serveCohereCaptureJSON(&captured, cohereSimpleTextResponse))

		msgs := []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"city":"Paris"}`)}}},
		}
		call := cohereTestCall(msgs, []aipapi.ToolDef{{Name: "get_weather"}}, aipapi.ToolChoice{}, false)
		_, err := cl.Open(context.Background(), call, "command-r")
		if err == nil {
			t.Fatalf("expected anonymous tool call to fail closed, got nil")
		}
		if !strings.Contains(err.Error(), "requires id and name") {
			t.Fatalf("expected id/name error, got %q", err.Error())
		}
	})

	t.Run("empty tool result content", func(t *testing.T) {
		t.Parallel()
		var captured capturedCohereRequest
		cl := openCohereTestClient(t, serveCohereCaptureJSON(&captured, cohereSimpleTextResponse))

		msgs := []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "call_1"}}},
		}
		call := cohereTestCall(msgs, []aipapi.ToolDef{{Name: "get_weather"}}, aipapi.ToolChoice{}, false)
		_, err := cl.Open(context.Background(), call, "command-r")
		if err == nil {
			t.Fatalf("expected empty tool result to fail closed, got nil")
		}
		if !strings.Contains(err.Error(), "empty content") {
			t.Fatalf("expected empty content error, got %q", err.Error())
		}
	})

	t.Run("vision parts still fail closed", func(t *testing.T) {
		t.Parallel()
		var captured capturedCohereRequest
		cl := openCohereTestClient(t, serveCohereCaptureJSON(&captured, cohereSimpleTextResponse))

		call := cohereTestCall([]aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{{Kind: aipapi.PartImageRef, ImageRef: "http://image.png"}},
		}}, []aipapi.ToolDef{{Name: "get_weather"}}, aipapi.ToolChoice{}, false)
		_, err := cl.Open(context.Background(), call, "command-r")
		if err == nil {
			t.Fatalf("expected vision to fail closed, got nil")
		}
		if !strings.Contains(err.Error(), "unsupported part kind") {
			t.Fatalf("expected unsupported part error, got %q", err.Error())
		}
	})
}

func TestCohereTools_StreamMissingMessageStartEmitted(t *testing.T) {
	t.Parallel()
	// Plain text streams must stay Collect-compatible (response_started →
	// message_started → content → finished) now that tool events share the path.
	cl := openCohereTestClient(t, cohereSSEHandler([]string{
		`data: {"type":"message-start","id":"msg-t1","delta":{"message":{"role":"assistant"}}}`,
		`data: {"type":"content-delta","index":0,"delta":{"message":{"content":{"text":"hello"}}}}`,
		`data: {"type":"message-end","id":"msg-t1","delta":{"finish_reason":"COMPLETE"}}`,
	}))

	call := cohereUserCall("hi")
	call.Invocation.DeliveryMode = aipapi.DeliveryModeStreaming
	call.Invocation.TransportMode = aipapi.TransportModeStreaming

	stream, err := cl.Open(context.Background(), call, "command-r")
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}
	evs := drainCohereEvents(context.Background(), t, stream)
	gotKinds := eventKinds(evs)
	wantKinds := []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventTextDelta,
		aipapi.EventResponseFinished,
	}
	if fmt.Sprintf("%v", gotKinds) != fmt.Sprintf("%v", wantKinds) {
		t.Fatalf("unexpected event kinds:\n got: %v\nwant: %v", gotKinds, wantKinds)
	}
	collected, err := aipapi.Collect(context.Background(), aipapi.NewFixedEventStream(evs))
	if err != nil {
		t.Fatalf("collect failed: %v", err)
	}
	if got := collected.Text.String(); got != "hello" {
		t.Fatalf("unexpected collected text: %q", got)
	}
}

func cohereWireAssistantMessage(t *testing.T, body map[string]any, index int) map[string]any {
	t.Helper()
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) <= index {
		t.Fatalf("expected at least %d wire messages, got %v", index+1, body["messages"])
	}
	assistant, ok := messages[index].(map[string]any)
	if !ok || assistant["role"] != "assistant" {
		t.Fatalf("unexpected assistant message at index %d: %v", index, messages[index])
	}
	return assistant
}

func TestCohereTools_AssistantReasoningReplay(t *testing.T) {
	t.Parallel()

	t.Run("reasoning text maps to tool_plan", func(t *testing.T) {
		t.Parallel()
		var captured capturedCohereRequest
		cl := openCohereTestClient(t, serveCohereCaptureJSON(&captured, cohereSimpleTextResponse))

		msgs := []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{
					Dialect: aipapi.ReasoningDialectOpenAIChatTextV1,
					Text:    "I will check",
				}},
			}},
		}
		call := cohereTestCall(msgs, nil, aipapi.ToolChoice{}, false)
		stream, err := cl.Open(context.Background(), call, "command-r")
		if err != nil {
			t.Fatalf("open failed: %v", err)
		}
		_ = stream.Close()

		assistant := cohereWireAssistantMessage(t, captured.body, 1)
		if assistant["tool_plan"] != "I will check" {
			t.Fatalf("expected tool_plan %q, got %v", "I will check", assistant["tool_plan"])
		}
	})

	t.Run("signature reasoning fails closed", func(t *testing.T) {
		t.Parallel()
		var captured capturedCohereRequest
		cl := openCohereTestClient(t, serveCohereCaptureJSON(&captured, cohereSimpleTextResponse))

		msgs := []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{
					Dialect:   aipapi.ReasoningDialectOpenAIChatTextV1,
					Text:      "think",
					Signature: "sig",
				}},
			}},
		}
		call := cohereTestCall(msgs, nil, aipapi.ToolChoice{}, false)
		_, err := cl.Open(context.Background(), call, "command-r")
		if err == nil {
			t.Fatalf("expected signature reasoning to fail closed, got nil")
		}
		if !strings.Contains(err.Error(), "tool_plan") {
			t.Fatalf("expected tool_plan round-trip error, got %q", err.Error())
		}
	})

	t.Run("opaque reasoning fails closed", func(t *testing.T) {
		t.Parallel()
		var captured capturedCohereRequest
		cl := openCohereTestClient(t, serveCohereCaptureJSON(&captured, cohereSimpleTextResponse))

		msgs := []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{
					Dialect: aipapi.ReasoningDialectOpenAIChatTextV1,
					Text:    "think",
					Opaque:  json.RawMessage(`{"x":1}`),
				}},
			}},
		}
		call := cohereTestCall(msgs, nil, aipapi.ToolChoice{}, false)
		_, err := cl.Open(context.Background(), call, "command-r")
		if err == nil {
			t.Fatalf("expected opaque reasoning to fail closed, got nil")
		}
		if !strings.Contains(err.Error(), "tool_plan") {
			t.Fatalf("expected tool_plan round-trip error, got %q", err.Error())
		}
	})

	t.Run("mixed text reasoning and tool calls coherent", func(t *testing.T) {
		t.Parallel()
		var captured capturedCohereRequest
		cl := openCohereTestClient(t, serveCohereCaptureJSON(&captured, cohereSimpleTextResponse))

		msgs := []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("weather?")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				aipapi.TextPart("Let me check."),
				{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{
					Dialect: aipapi.ReasoningDialectOpenAIChatTextV1,
					Text:    "I will check",
				}},
				{Kind: aipapi.PartJSON, ToolCallID: "call_1", ToolName: "get_weather", Content: json.RawMessage(`{"city":"Paris"}`)},
			}},
		}
		call := cohereTestCall(msgs, []aipapi.ToolDef{{Name: "get_weather"}}, aipapi.ToolChoice{}, false)
		stream, err := cl.Open(context.Background(), call, "command-r")
		if err != nil {
			t.Fatalf("open failed: %v", err)
		}
		_ = stream.Close()

		assistant := cohereWireAssistantMessage(t, captured.body, 1)
		if assistant["content"] != "Let me check." {
			t.Fatalf("unexpected content: %v", assistant["content"])
		}
		if assistant["tool_plan"] != "I will check" {
			t.Fatalf("unexpected tool_plan: %v", assistant["tool_plan"])
		}
		wireCalls, ok := assistant["tool_calls"].([]any)
		if !ok || len(wireCalls) != 1 {
			t.Fatalf("expected 1 wire tool_call, got %v", assistant["tool_calls"])
		}
		wireCall, ok := wireCalls[0].(map[string]any)
		if !ok || wireCall["id"] != "call_1" {
			t.Fatalf("unexpected wire tool_call: %v", wireCalls[0])
		}
		fn, ok := wireCall["function"].(map[string]any)
		if !ok || fn["name"] != "get_weather" {
			t.Fatalf("unexpected wire function: %v", wireCall["function"])
		}
	})
}

func TestCohereTools_EnvelopeDiscriminatorCollidingRawArgs(t *testing.T) {
	t.Parallel()
	var captured capturedCohereRequest
	cl := openCohereTestClient(t, serveCohereCaptureJSON(&captured, cohereSimpleTextResponse))

	msgs := []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
			Kind: aipapi.PartJSON, ToolCallID: "call_collide", ToolName: "get_weather",
			Content: json.RawMessage(`{"function":{"name":"main"}}`),
		}}},
		{Role: aipapi.RoleTool, Parts: []aipapi.Part{
			{Kind: aipapi.PartToolResult, ToolCallID: "call_collide", ToolName: "get_weather", Text: "ok"},
		}},
	}
	call := cohereTestCall(msgs, []aipapi.ToolDef{{Name: "get_weather"}}, aipapi.ToolChoice{}, false)
	stream, err := cl.Open(context.Background(), call, "command-r")
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}
	_ = stream.Close()

	assistant := cohereWireAssistantMessage(t, captured.body, 1)
	wireCalls, ok := assistant["tool_calls"].([]any)
	if !ok || len(wireCalls) != 1 {
		t.Fatalf("expected 1 wire tool_call, got %v", assistant["tool_calls"])
	}
	wireCall, ok := wireCalls[0].(map[string]any)
	if !ok {
		t.Fatalf("expected tool_call object, got %T", wireCalls[0])
	}
	fn, ok := wireCall["function"].(map[string]any)
	if !ok {
		t.Fatalf("expected function object, got %T", wireCall["function"])
	}
	args, ok := fn["arguments"].(string)
	if !ok {
		t.Fatalf("expected arguments string, got %T", fn["arguments"])
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(args), &decoded); err != nil {
		t.Fatalf("arguments must be JSON: %q", args)
	}
	nested, ok := decoded["function"].(map[string]any)
	if !ok || nested["name"] != "main" {
		t.Fatalf("expected raw arguments preserved via raw path, got %q", args)
	}
}

func TestCohereStream_CancellationWithQueuedToolEvents(t *testing.T) {
	t.Parallel()

	ssePayload := strings.Join([]string{
		`data: {"type":"message-start","delta":{"message":{"content":{"text":""}}}}`,
		`data: {"type":"tool-call-start","index":0,"delta":{"message":{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"search","arguments":"{\"q\":\"aip\"}"}}]}}}`,
		`data: [DONE]`,
		"",
	}, "\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(ssePayload))
	}))
	t.Cleanup(srv.Close)

	cl := &service.Client{
		Config:        service.Config{APIOrigin: srv.URL},
		TokenProvider: service.StaticTokenProvider("test-token"),
		HTTPClient:    srv.Client(),
	}

	call := cohereTestCall([]aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}}, []aipapi.ToolDef{{Name: "search"}}, aipapi.ToolChoice{}, true)
	stream, err := cl.Open(context.Background(), call, "command-r")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Initial event is response_started
	ev, err := stream.Recv(ctx)
	if err != nil {
		t.Fatalf("first Recv failed: %v", err)
	}
	if ev.Kind != aipapi.EventResponseStarted {
		t.Fatalf("expected EventResponseStarted, got %v", ev.Kind)
	}

	// 2. Second event reads SSE and enqueues message_started + tool_call_started + args delta.
	// It returns message_started, leaving tool_call_started + args delta in pending.
	ev, err = stream.Recv(ctx)
	if err != nil {
		t.Fatalf("second Recv failed: %v", err)
	}
	if ev.Kind != aipapi.EventMessageStarted {
		t.Fatalf("expected EventMessageStarted, got %v", ev.Kind)
	}

	// 3. Caller cancels before consuming queued tool events
	cancel()

	// 4. Recv with cancelled context MUST return context.Canceled error and NOT yield queued events.
	_, err = stream.Recv(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled from Recv after cancellation, got: %v", err)
	}

	// 5. Nil context check
	var nilCtx context.Context
	_, err = stream.Recv(nilCtx)
	if !errors.Is(err, aipapi.ErrNilContext) {
		t.Fatalf("expected ErrNilContext, got: %v", err)
	}
}

func TestCohereStream_Recv_PreCancelledContextReturnsEarly(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)

	cl := &service.Client{
		Config:        service.Config{APIOrigin: srv.URL},
		TokenProvider: service.StaticTokenProvider("test-token"),
		HTTPClient:    srv.Client(),
	}

	call := cohereTestCall([]aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}}, nil, aipapi.ToolChoice{}, true)
	stream, err := cl.Open(context.Background(), call, "command-r")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = stream.Recv(canceledCtx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled before first event, got: %v", err)
	}
}
