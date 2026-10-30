package openaicompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/respjson"
)

func eventKinds(events []aipapi.Event) []aipapi.EventKind {
	out := make([]aipapi.EventKind, len(events))
	for i, ev := range events {
		out[i] = ev.Kind
	}
	return out
}

func TestChatCompletionEvents_textAndUsage(t *testing.T) {
	t.Parallel()
	raw := `{
  "id": "cc_ns",
  "object": "chat.completion",
  "created": 1715620000,
  "model": "gpt-4o-mini",
  "choices": [{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
  "usage": {
    "prompt_tokens": 1,
    "completion_tokens": 2,
    "total_tokens": 3,
    "completion_tokens_details": {"reasoning_tokens": 4}
  }
}`
	var comp openai.ChatCompletion
	if err := json.Unmarshal([]byte(raw), &comp); err != nil {
		t.Fatal(err)
	}

	events := ChatCompletionEvents(comp)
	kinds := eventKinds(events)
	want := []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventTextDelta,
		aipapi.EventUsageDelta,
		aipapi.EventResponseFinished,
	}
	if len(kinds) != len(want) {
		t.Fatalf("got kinds %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("event[%d] = %v, want %v", i, kinds[i], want[i])
		}
	}
	if events[2].Delta != "hello" {
		t.Fatalf("text delta = %q", events[2].Delta)
	}
	if events[3].ReasoningTokens != 4 || events[3].TotalTokens != 3 {
		t.Fatalf("usage event: %+v", events[3])
	}
}

func TestChatCompletionEvents_explicitAllZeroUsage(t *testing.T) {
	t.Parallel()
	raw := `{
  "id": "cc_zero",
  "object": "chat.completion",
  "created": 1715620000,
  "model": "gpt-4o-mini",
  "choices": [{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
  "usage": {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
}`
	var comp openai.ChatCompletion
	if err := json.Unmarshal([]byte(raw), &comp); err != nil {
		t.Fatal(err)
	}

	events := ChatCompletionEvents(comp)
	var usage *aipapi.Event
	for i := range events {
		if events[i].Kind == aipapi.EventUsageDelta {
			usage = &events[i]
			break
		}
	}
	if usage == nil {
		t.Fatal("explicit all-zero usage must emit EventUsageDelta")
	}
	if !usage.UsagePresence.InputTokens || !usage.UsagePresence.OutputTokens || !usage.UsagePresence.TotalTokens {
		t.Fatalf("presence = %+v, want input/output/total present", usage.UsagePresence)
	}
}

func TestChatCompletionEvents_openRouterProviderCost(t *testing.T) {
	t.Parallel()
	raw := `{
  "id": "cc_or",
  "object": "chat.completion",
  "created": 1715620000,
  "model": "openai/gpt-4o-mini",
  "choices": [{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
  "usage": {
    "prompt_tokens": 3,
    "completion_tokens": 7,
    "total_tokens": 10,
    "prompt_tokens_details": {"cached_tokens": 1},
    "completion_tokens_details": {"reasoning_tokens": 5},
    "cost": 0.00014
  }
}`
	var comp openai.ChatCompletion
	if err := json.Unmarshal([]byte(raw), &comp); err != nil {
		t.Fatal(err)
	}

	var usage aipapi.Event
	for _, ev := range ChatCompletionEvents(comp) {
		if ev.Kind == aipapi.EventUsageDelta {
			usage = ev
		}
	}
	if usage.Kind != aipapi.EventUsageDelta {
		t.Fatal("missing usage event")
	}
	if usage.CacheReadTokens != 1 || usage.ReasoningTokens != 5 {
		t.Fatalf("usage details: %+v", usage)
	}
	if usage.CostNanoUnits != 140_000 {
		t.Fatalf("CostNanoUnits = %d, want 140000", usage.CostNanoUnits)
	}
}

func TestChatCompletionEvents_toolCalls(t *testing.T) {
	t.Parallel()
	raw := `{
  "id": "cc_tool_ns",
  "object": "chat.completion",
  "created": 1715620000,
  "model": "gpt-4o-mini",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "tool_calls": [{
        "id": "call_ab",
        "type": "function",
        "function": {"name": "get_weather", "arguments": "{\"city\":\"NYC\"}"}
      }]
    },
    "finish_reason": "tool_calls"
  }]
}`
	var comp openai.ChatCompletion
	if err := json.Unmarshal([]byte(raw), &comp); err != nil {
		t.Fatal(err)
	}

	events := ChatCompletionEvents(comp)
	var started, args, finished bool
	for _, ev := range events {
		switch ev.Kind {
		case aipapi.EventToolCallStarted:
			if ev.ToolCallID == "call_ab" && ev.ToolName == "get_weather" {
				started = true
			}
		case aipapi.EventToolCallArgsDelta:
			if ev.ToolCallID == "call_ab" && ev.Delta == `{"city":"NYC"}` {
				args = true
			}
		case aipapi.EventToolCallFinished:
			if ev.ToolCallID == "call_ab" {
				finished = true
			}
		}
	}
	if !started || !args || !finished {
		t.Fatalf("tool events: started=%v args=%v finished=%v kinds=%v", started, args, finished, eventKinds(events))
	}
}

func TestChatCompletionEvents_noChoices(t *testing.T) {
	t.Parallel()
	raw := `{"id":"cc_empty","object":"chat.completion","created":1715620000,"model":"gpt-4o","choices":[]}`
	var comp openai.ChatCompletion
	if err := json.Unmarshal([]byte(raw), &comp); err != nil {
		t.Fatal(err)
	}
	events := ChatCompletionEvents(comp)
	kinds := eventKinds(events)
	want := []aipapi.EventKind{aipapi.EventResponseStarted, aipapi.EventResponseFinished}
	if len(kinds) != len(want) {
		t.Fatalf("got %v, want %v", kinds, want)
	}
}

func TestChatCompletionEvents_reasoningField(t *testing.T) {
	t.Parallel()
	raw := `{
  "id": "cc_reasoning",
  "object": "chat.completion",
  "created": 1715620000,
  "model": "deepseek-r1",
  "choices": [{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}]
}`
	var comp openai.ChatCompletion
	if err := json.Unmarshal([]byte(raw), &comp); err != nil {
		t.Fatal(err)
	}
	comp.Choices[0].Message.JSON.ExtraFields = map[string]respjson.Field{
		"reasoning": respjson.NewField(`"think"`),
	}
	events := ChatCompletionEvents(comp)
	sawReasoning := false
	for _, ev := range events {
		if ev.Kind == aipapi.EventReasoningDelta && ev.Delta == "think" {
			sawReasoning = true
		}
	}
	if !sawReasoning {
		t.Fatalf("expected EventReasoningDelta with 'think', got %v", eventKinds(events))
	}
}

func TestReasoningTextFromMessage_empty(t *testing.T) {
	t.Parallel()
	var msg openai.ChatCompletionMessage
	if got := ReasoningTextFromMessage(msg); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestReasoningTextFromMessage_reasoningKey(t *testing.T) {
	t.Parallel()
	var msg openai.ChatCompletionMessage
	msg.JSON.ExtraFields = map[string]respjson.Field{
		"reasoning": respjson.NewField(`"think step"`),
	}
	if got := ReasoningTextFromMessage(msg); got != "think step" {
		t.Fatalf("got %q", got)
	}
}

func TestReasoningTextFromMessage_reasoningContentKey(t *testing.T) {
	t.Parallel()
	var msg openai.ChatCompletionMessage
	msg.JSON.ExtraFields = map[string]respjson.Field{
		"reasoning_content": respjson.NewField(`"deeper think"`),
	}
	if got := ReasoningTextFromMessage(msg); got != "deeper think" {
		t.Fatalf("got %q", got)
	}
}

func TestReasoningTextFromMessage_reasoningSummaryKey(t *testing.T) {
	t.Parallel()
	var msg openai.ChatCompletionMessage
	msg.JSON.ExtraFields = map[string]respjson.Field{
		"reasoning_summary": respjson.NewField(`"summary think"`),
	}
	if got := ReasoningTextFromMessage(msg); got != "summary think" {
		t.Fatalf("got %q", got)
	}
}

func TestReasoningTextFromMessage_reasoningSummary_empty(t *testing.T) {
	t.Parallel()
	var msg openai.ChatCompletionMessage
	msg.JSON.ExtraFields = map[string]respjson.Field{
		"reasoning_summary": respjson.NewField(`"   "`),
	}
	if got := ReasoningTextFromMessage(msg); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestReasoningTextFromMessage_reasoningContentDecoded(t *testing.T) {
	t.Parallel()
	raw := `{"role":"assistant","content":"answer","reasoning_content":"decoded think"}`
	var msg openai.ChatCompletionMessage
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	if got := ReasoningTextFromMessage(msg); got != "decoded think" {
		t.Fatalf("decoded reasoning_content must be extracted; got %q", got)
	}
}

func TestReasoningTextFromChunkDelta_empty(t *testing.T) {
	t.Parallel()
	var delta openai.ChatCompletionChunkChoiceDelta
	if got := ReasoningTextFromChunkDelta(delta); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestReasoningTextFromChunkDelta_reasoningKey(t *testing.T) {
	t.Parallel()
	var delta openai.ChatCompletionChunkChoiceDelta
	delta.JSON.ExtraFields = map[string]respjson.Field{
		"reasoning": respjson.NewField(`"chunk think"`),
	}
	if got := ReasoningTextFromChunkDelta(delta); got != "chunk think" {
		t.Fatalf("got %q", got)
	}
}

func TestReasoningTextFromChunkDelta_reasoningSummaryKey(t *testing.T) {
	t.Parallel()
	var delta openai.ChatCompletionChunkChoiceDelta
	delta.JSON.ExtraFields = map[string]respjson.Field{
		"reasoning_summary": respjson.NewField(`"chunk summary"`),
	}
	if got := ReasoningTextFromChunkDelta(delta); got != "chunk summary" {
		t.Fatalf("got %q", got)
	}
}

func TestReasoningTextFromChunkDelta_reasoningSummary_empty(t *testing.T) {
	t.Parallel()
	var delta openai.ChatCompletionChunkChoiceDelta
	delta.JSON.ExtraFields = map[string]respjson.Field{
		"reasoning_summary": respjson.NewField(`"   "`),
	}
	if got := ReasoningTextFromChunkDelta(delta); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestReasoningTextFromChunkDelta_nullReasoningFallsThroughToSummary(t *testing.T) {
	t.Parallel()
	var delta openai.ChatCompletionChunkChoiceDelta
	delta.JSON.ExtraFields = map[string]respjson.Field{
		"reasoning":         respjson.NewField(`null`),
		"reasoning_summary": respjson.NewField(`"fallback summary"`),
	}
	if got := ReasoningTextFromChunkDelta(delta); got != "fallback summary" {
		t.Fatalf("null reasoning must fall through to summary; got %q", got)
	}
}

func TestReasoningTextFromMessage_nullReasoningFallsThroughToSummary(t *testing.T) {
	t.Parallel()
	var msg openai.ChatCompletionMessage
	msg.JSON.ExtraFields = map[string]respjson.Field{
		"reasoning":         respjson.NewField(`null`),
		"reasoning_summary": respjson.NewField(`"fallback summary"`),
	}
	if got := ReasoningTextFromMessage(msg); got != "fallback summary" {
		t.Fatalf("null reasoning must fall through to summary; got %q", got)
	}
}

func TestReasoningTextFromChunkDelta_reasoningContentPreferredOverSummary(t *testing.T) {
	t.Parallel()
	var delta openai.ChatCompletionChunkChoiceDelta
	delta.JSON.ExtraFields = map[string]respjson.Field{
		"reasoning":         respjson.NewField(`null`),
		"reasoning_content": respjson.NewField(`"raw think"`),
		"reasoning_summary": respjson.NewField(`"summary"`),
	}
	if got := ReasoningTextFromChunkDelta(delta); got != "raw think" {
		t.Fatalf("reasoning_content must win over summary; got %q", got)
	}
}

func TestHandleChatChunk_toolCallsStreamingFromJSON(t *testing.T) {
	t.Parallel()
	chunks := []string{
		`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_ab","type":"function","function":{"name":"get_weather"}}]},"finish_reason":null}]}`,
		`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\""}}]},"finish_reason":null}]}`,
		`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"NYC\"}"}}]},"finish_reason":null}]}`,
		`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	}

	s := &chatStream{pending: stream.NewPendingEventQueue(0)}
	for _, raw := range chunks {
		var ch openai.ChatCompletionChunk
		if err := json.Unmarshal([]byte(raw), &ch); err != nil {
			t.Fatal(err)
		}
		if err := s.handleChunk(ch); err != nil {
			t.Fatal(err)
		}
	}

	var args strings.Builder
	sawStarted := false
	sawFinished := false
	for _, ev := range stream.DrainPending(&s.pending) {
		switch ev.Kind {
		case aipapi.EventToolCallStarted:
			if ev.ToolCallID == "call_ab" && ev.ToolName == "get_weather" {
				sawStarted = true
			}
		case aipapi.EventToolCallArgsDelta:
			args.WriteString(ev.Delta)
		case aipapi.EventToolCallFinished:
			if ev.ToolCallID == "call_ab" {
				sawFinished = true
			}
		}
	}
	if !sawStarted {
		t.Fatal("expected ToolCallStarted for call_ab")
	}
	if !sawFinished {
		t.Fatal("expected ToolCallFinished for call_ab")
	}
	if got := args.String(); got != `{"city":"NYC"}` {
		t.Fatalf("concat args: %q", got)
	}
}

func TestHandleChatChunk_toolCallArgsBufferedUntilID(t *testing.T) {
	t.Parallel()
	chunks := []string{
		`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"function":{"arguments":"{\"city\""}}]},"finish_reason":null}]}`,
		`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_ab","type":"function","function":{"name":"get_weather","arguments":":\"NYC\"}"}}]},"finish_reason":null}]}`,
		`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	}

	s := &chatStream{pending: stream.NewPendingEventQueue(0)}
	for _, raw := range chunks {
		var ch openai.ChatCompletionChunk
		if err := json.Unmarshal([]byte(raw), &ch); err != nil {
			t.Fatal(err)
		}
		if err := s.handleChunk(ch); err != nil {
			t.Fatal(err)
		}
	}

	events := stream.DrainPending(&s.pending)
	toolEvents := []aipapi.Event{}
	for _, ev := range events {
		if ev.Kind == aipapi.EventToolCallStarted || ev.Kind == aipapi.EventToolCallArgsDelta || ev.Kind == aipapi.EventToolCallFinished {
			toolEvents = append(toolEvents, ev)
		}
	}
	if len(toolEvents) != 4 {
		t.Fatalf("tool events = %+v", toolEvents)
	}
	if toolEvents[0].Kind != aipapi.EventToolCallStarted || toolEvents[0].ToolCallID != "call_ab" {
		t.Fatalf("start event = %+v", toolEvents[0])
	}
	if toolEvents[1].Kind != aipapi.EventToolCallArgsDelta || toolEvents[1].ToolCallID != "call_ab" || toolEvents[1].Delta != `{"city"` {
		t.Fatalf("buffered args event = %+v", toolEvents[1])
	}
	if toolEvents[2].Kind != aipapi.EventToolCallArgsDelta || toolEvents[2].ToolCallID != "call_ab" || toolEvents[2].Delta != `:"NYC"}` {
		t.Fatalf("current args event = %+v", toolEvents[2])
	}
	if toolEvents[3].Kind != aipapi.EventToolCallFinished || toolEvents[3].ToolCallID != "call_ab" {
		t.Fatalf("finish event = %+v", toolEvents[3])
	}
}

func TestHandleChatChunk_toolCallStartedOnIDOnlyChunk(t *testing.T) {
	t.Parallel()
	chunks := []string{
		`{"id":"cc_idonly","choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_idonly","type":"function","function":{}}]},"finish_reason":null}]}`,
		`{"id":"cc_idonly","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":1}"}}]},"finish_reason":null}]}`,
		`{"id":"cc_idonly","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	}

	s := &chatStream{pending: stream.NewPendingEventQueue(0)}
	for _, raw := range chunks {
		var ch openai.ChatCompletionChunk
		if err := json.Unmarshal([]byte(raw), &ch); err != nil {
			t.Fatal(err)
		}
		if err := s.handleChunk(ch); err != nil {
			t.Fatal(err)
		}
	}

	toolEvents := []aipapi.Event{}
	for _, ev := range stream.DrainPending(&s.pending) {
		switch ev.Kind {
		case aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta, aipapi.EventToolCallFinished:
			toolEvents = append(toolEvents, ev)
		}
	}
	want := []aipapi.Event{
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_idonly", ToolName: ""},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_idonly", Delta: `{"q":1}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_idonly"},
	}
	if len(toolEvents) != len(want) {
		t.Fatalf("tool events len=%d want %d\ngot=%+v", len(toolEvents), len(want), toolEvents)
	}
	for i := range want {
		got, w := toolEvents[i], want[i]
		if got.Kind != w.Kind || got.ToolCallID != w.ToolCallID || got.ToolName != w.ToolName || got.Delta != w.Delta {
			t.Fatalf("toolEvents[%d]=%+v want %+v", i, got, w)
		}
	}
}

func TestHandleChatChunk_toolCallStartedOnceOnIDThenNameSplit(t *testing.T) {
	t.Parallel()
	chunks := []string{
		`{"id":"cc_split","choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_split","type":"function","function":{}}]},"finish_reason":null}]}`,
		`{"id":"cc_split","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"get_weather","arguments":"{\"city\""}}]},"finish_reason":null}]}`,
		`{"id":"cc_split","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"NYC\"}"}}]},"finish_reason":null}]}`,
		`{"id":"cc_split","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	}

	s := &chatStream{pending: stream.NewPendingEventQueue(0)}
	for _, raw := range chunks {
		var ch openai.ChatCompletionChunk
		if err := json.Unmarshal([]byte(raw), &ch); err != nil {
			t.Fatal(err)
		}
		if err := s.handleChunk(ch); err != nil {
			t.Fatal(err)
		}
	}

	started := 0
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind != aipapi.EventToolCallStarted {
			continue
		}
		started++
		if ev.ToolCallID != "call_split" || ev.ToolName != "" {
			t.Fatalf("Started=%+v want id=call_split name=\"\"", ev)
		}
	}
	if started != 1 {
		t.Fatalf("ToolCallStarted count=%d want exactly one", started)
	}
}

func TestHandleChatChunk_multiToolCallFinishByIndex(t *testing.T) {
	t.Parallel()
	// Mirrors connector-support/openaicompat.TestDecodeChatSSE_multiToolCallFinishByIndex.
	chunks := []string{
		`{"id":"cc_multi","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_ma","type":"function","function":{"name":"alpha"}}]},"finish_reason":null}]}`,
		`{"id":"cc_multi","choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_mb","type":"function","function":{"name":"beta"}}]},"finish_reason":null}]}`,
		`{"id":"cc_multi","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"a\":1}"}}]},"finish_reason":null}]}`,
		`{"id":"cc_multi","choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"b\":2}"}}]},"finish_reason":null}]}`,
		`{"id":"cc_multi","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	}

	s := &chatStream{pending: stream.NewPendingEventQueue(0)}
	for _, raw := range chunks {
		var ch openai.ChatCompletionChunk
		if err := json.Unmarshal([]byte(raw), &ch); err != nil {
			t.Fatal(err)
		}
		if err := s.handleChunk(ch); err != nil {
			t.Fatal(err)
		}
	}

	toolEvents := []aipapi.Event{}
	for _, ev := range stream.DrainPending(&s.pending) {
		switch ev.Kind {
		case aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta, aipapi.EventToolCallFinished:
			toolEvents = append(toolEvents, ev)
		}
	}
	want := []aipapi.Event{
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_ma", ToolName: "alpha"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_mb", ToolName: "beta"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_ma", Delta: `{"a":1}`},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_mb", Delta: `{"b":2}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_ma"},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_mb"},
	}
	if len(toolEvents) != len(want) {
		t.Fatalf("tool events len=%d want %d\ngot=%+v", len(toolEvents), len(want), toolEvents)
	}
	for i := range want {
		got, w := toolEvents[i], want[i]
		if got.Kind != w.Kind || got.ToolCallID != w.ToolCallID || got.ToolName != w.ToolName || got.Delta != w.Delta {
			t.Fatalf("toolEvents[%d]=%+v want %+v", i, got, w)
		}
	}
}

func TestHandleChatChunk_usageChunk(t *testing.T) {
	t.Parallel()
	raw := `{"id":"cc_u","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":7,"total_tokens":10}}`
	var ch openai.ChatCompletionChunk
	if err := json.Unmarshal([]byte(raw), &ch); err != nil {
		t.Fatal(err)
	}
	s := &chatStream{pending: stream.NewPendingEventQueue(0)}
	if err := s.handleChunk(ch); err != nil {
		t.Fatal(err)
	}
	events := stream.DrainPending(&s.pending)
	hasUsage := false
	for _, ev := range events {
		if ev.Kind == aipapi.EventUsageDelta {
			hasUsage = true
			if ev.InputTokens != 3 || ev.OutputTokens != 7 || ev.TotalTokens != 10 {
				t.Fatalf("usage event: %+v", ev)
			}
		}
	}
	if !hasUsage {
		t.Fatal("expected EventUsageDelta")
	}
}

func TestHandleChatChunk_reasoningContent(t *testing.T) {
	t.Parallel()
	raw := `{"id":"cc_reasoning","object":"chat.completion.chunk","created":1715620000,"model":"deepseek-r1","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"thinking"},"finish_reason":null}]}`
	var ch openai.ChatCompletionChunk
	if err := json.Unmarshal([]byte(raw), &ch); err != nil {
		t.Fatal(err)
	}

	s := &chatStream{pending: stream.NewPendingEventQueue(0)}
	if err := s.handleChunk(ch); err != nil {
		t.Fatal(err)
	}

	events := stream.DrainPending(&s.pending)
	for _, ev := range events {
		if ev.Kind == aipapi.EventReasoningDelta && ev.Delta == "thinking" {
			return
		}
	}
	t.Fatalf("expected reasoning delta, got %v", eventKinds(events))
}
