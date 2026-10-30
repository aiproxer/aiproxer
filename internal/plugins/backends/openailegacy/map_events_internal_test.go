package openailegacy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

//nolint:paralleltest // chunks share chatStream; inner t.Run is for failure attribution only
func TestHandleChunk_toolCallsStreamingFromJSON(t *testing.T) {
	t.Parallel()
	chunks := []string{
		`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_ab","type":"function","function":{"name":"get_weather"}}]},"finish_reason":null}]}`,
		`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\""}}]},"finish_reason":null}]}`,
		`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"NYC\"}"}}]},"finish_reason":null}]}`,
		`{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	}
	s := &chatStream{}
	for i, raw := range chunks {
		t.Run(fmt.Sprintf("chunk_%d", i), func(t *testing.T) {
			var ch openai.ChatCompletionChunk
			if err := json.Unmarshal([]byte(raw), &ch); err != nil {
				t.Fatal(err)
			}
			_ = s.handleChunk(ch)
		})
	}
	var args strings.Builder
	var sawFinish bool
	for _, ev := range stream.DrainPending(&s.pending) {
		switch ev.Kind {
		case aipapi.EventToolCallArgsDelta:
			args.WriteString(ev.Delta)
		case aipapi.EventToolCallFinished:
			if ev.ToolCallID == "call_ab" {
				sawFinish = true
			}
		}
	}
	if got := args.String(); got != `{"city":"NYC"}` {
		t.Fatalf("concat args: %q", got)
	}
	if !sawFinish {
		t.Fatal("expected ToolCallFinished for call_ab")
	}
}

func TestHandleChunk_multipleToolCallsFinishInStartOrder(t *testing.T) {
	t.Parallel()
	raw := `{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_ab","type":"function","function":{"name":"get_weather"}},{"index":1,"id":"call_cd","type":"function","function":{"name":"lookup_stock"}}]},"finish_reason":"tool_calls"}]}`
	var ch openai.ChatCompletionChunk
	if err := json.Unmarshal([]byte(raw), &ch); err != nil {
		t.Fatal(err)
	}

	s := &chatStream{}
	_ = s.handleChunk(ch)

	var startIDs []string
	var finishIDs []string
	for _, ev := range stream.DrainPending(&s.pending) {
		switch ev.Kind {
		case aipapi.EventToolCallStarted:
			startIDs = append(startIDs, ev.ToolCallID)
		case aipapi.EventToolCallFinished:
			finishIDs = append(finishIDs, ev.ToolCallID)
		}
	}

	if len(startIDs) != 2 || startIDs[0] != "call_ab" || startIDs[1] != "call_cd" {
		t.Fatalf("start order: %v", startIDs)
	}
	if len(finishIDs) != 2 || finishIDs[0] != "call_ab" || finishIDs[1] != "call_cd" {
		t.Fatalf("finish order: %v", finishIDs)
	}
}

// When tool index 1 appears before index 0 in the stream, finish events must follow that
// first-seen order (not numeric index sort). This also avoids non-determinism from ranging a map.
func TestHandleChunk_multipleToolCallsFinishFollowsWireOrderWhenIndicesOutOfNumericOrder(t *testing.T) {
	t.Parallel()
	raw := `{"id":"cc_tool","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":1,"id":"call_second","type":"function","function":{"name":"b"}},{"index":0,"id":"call_first","type":"function","function":{"name":"a"}}]},"finish_reason":"tool_calls"}]}`
	var ch openai.ChatCompletionChunk
	if err := json.Unmarshal([]byte(raw), &ch); err != nil {
		t.Fatal(err)
	}

	s := &chatStream{}
	_ = s.handleChunk(ch)

	var startIDs []string
	var finishIDs []string
	for _, ev := range stream.DrainPending(&s.pending) {
		switch ev.Kind {
		case aipapi.EventToolCallStarted:
			startIDs = append(startIDs, ev.ToolCallID)
		case aipapi.EventToolCallFinished:
			finishIDs = append(finishIDs, ev.ToolCallID)
		}
	}

	if len(startIDs) != 2 || startIDs[0] != "call_second" || startIDs[1] != "call_first" {
		t.Fatalf("start order: %v", startIDs)
	}
	if len(finishIDs) != 2 || finishIDs[0] != "call_second" || finishIDs[1] != "call_first" {
		t.Fatalf("finish order: %v", finishIDs)
	}
}

func TestHandleChunk_usageDetails(t *testing.T) {
	t.Parallel()
	raw := `{"id":"cc_usage","object":"chat.completion.chunk","created":1715620000,"model":"gpt-4o-mini","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":8,"total_tokens":19,"prompt_tokens_details":{"cached_tokens":3,"x_aip_cache_write_tokens":2},"completion_tokens_details":{"reasoning_tokens":5}}}`
	var ch openai.ChatCompletionChunk
	if err := json.Unmarshal([]byte(raw), &ch); err != nil {
		t.Fatal(err)
	}

	s := &chatStream{}
	if err := s.handleChunk(ch); err != nil {
		t.Fatal(err)
	}

	ev := findUsageEvent(t, stream.DrainPending(&s.pending))
	if ev.InputTokens != 11 || ev.OutputTokens != 8 {
		t.Fatalf("usage tokens: in=%d out=%d", ev.InputTokens, ev.OutputTokens)
	}
	assertUsageIntField(t, ev, "CacheReadTokens", 3)
	assertUsageIntField(t, ev, "CacheWriteTokens", 2)
	assertUsageIntField(t, ev, "ReasoningTokens", 5)
	assertUsageIntField(t, ev, "TotalTokens", 19)
	assertUsageRawJSONContains(t, ev, "total_tokens")
}

type errDecoderLegacy struct{ err error }

func (d *errDecoderLegacy) Event() ssestream.Event {
	return ssestream.Event{Data: []byte(`{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[]}`)}
}

func (d *errDecoderLegacy) Next() bool { return false }

func (d *errDecoderLegacy) Close() error { return nil }

func (d *errDecoderLegacy) Err() error { return d.err }

func TestChatStream_Recv_wrapsSDKErr(t *testing.T) {
	t.Parallel()
	root := errors.New("root")
	sdk := ssestream.NewStream[openai.ChatCompletionChunk](&errDecoderLegacy{err: root}, nil)
	es := NewChatStream(sdk, 0)
	s, ok := es.(*chatStream)
	if !ok {
		t.Fatalf("newChatStream returned %T", es)
	}
	_, err := s.Recv(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "openai-legacy: recv stream") {
		t.Fatalf("got %q", err.Error())
	}
	if !errors.Is(err, root) {
		t.Fatalf("underlying: %v", err)
	}
}

func findUsageEvent(t *testing.T, events []aipapi.Event) aipapi.Event {
	t.Helper()
	for _, ev := range events {
		if ev.Kind == aipapi.EventUsageDelta {
			return ev
		}
	}
	t.Fatalf("usage event not found in %+v", events)
	return aipapi.Event{}
}

func assertUsageIntField(t *testing.T, ev aipapi.Event, name string, want int64) {
	t.Helper()
	field := reflect.ValueOf(ev).FieldByName(name)
	if !field.IsValid() {
		return
	}
	if got := field.Int(); got != want {
		t.Fatalf("%s: got %d, want %d", name, got, want)
	}
}

func assertUsageRawJSONContains(t *testing.T, ev aipapi.Event, needle string) {
	t.Helper()
	field := reflect.ValueOf(ev).FieldByName("RawUsageJSON")
	if !field.IsValid() {
		return
	}
	switch field.Kind() {
	case reflect.String:
		if !strings.Contains(field.String(), needle) {
			t.Fatalf("RawUsageJSON: %q does not contain %q", field.String(), needle)
		}
	case reflect.Slice:
		if !strings.Contains(string(field.Bytes()), needle) {
			t.Fatalf("RawUsageJSON: %q does not contain %q", string(field.Bytes()), needle)
		}
	}
}
