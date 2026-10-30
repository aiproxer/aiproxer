package openairesponses

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
)

func TestHandleUnion_textDeltaThenCompleted_noDuplicateText(t *testing.T) {
	t.Parallel()
	s := &sdkStream{}

	_ = s.handleUnion(responses.ResponseStreamEventUnion{
		Type:  "response.output_text.delta",
		Delta: "hel",
	})
	_ = s.handleUnion(responses.ResponseStreamEventUnion{
		Type:  "response.output_text.delta",
		Delta: "lo",
	})

	resp := responses.Response{
		ID:     "r1",
		Status: "completed",
		Output: []responses.ResponseOutputItemUnion{
			{
				Type: "message",
				Content: []responses.ResponseOutputMessageContentUnion{
					{Type: "output_text", Text: "hello"},
				},
			},
		},
	}
	_ = s.handleUnion(responses.ResponseStreamEventUnion{
		Type:     "response.completed",
		Response: resp,
	})

	var texts []string
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == aipapi.EventTextDelta {
			texts = append(texts, ev.Delta)
		}
	}
	if got := len(texts); got != 2 {
		t.Fatalf("expected 2 text deltas (from incremental only), got %d: %v", got, texts)
	}
	combined := texts[0] + texts[1]
	if combined != "hello" {
		t.Fatalf("combined text: %q", combined)
	}
}

func TestHandleUnion_completedOnly_emitsFullText(t *testing.T) {
	t.Parallel()
	s := &sdkStream{}

	resp := responses.Response{
		ID:     "r2",
		Status: "completed",
		Output: []responses.ResponseOutputItemUnion{
			{
				Type: "message",
				Content: []responses.ResponseOutputMessageContentUnion{
					{Type: "output_text", Text: "done"},
				},
			},
		},
	}
	_ = s.handleUnion(responses.ResponseStreamEventUnion{
		Type:     "response.completed",
		Response: resp,
	})

	var texts []string
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == aipapi.EventTextDelta {
			texts = append(texts, ev.Delta)
		}
	}
	if got := len(texts); got != 1 {
		t.Fatalf("expected 1 text delta (from completed), got %d: %v", got, texts)
	}
	if texts[0] != "done" {
		t.Fatalf("text: %q", texts[0])
	}
}

func TestHandleUnion_streamError_emitsEventError(t *testing.T) {
	t.Parallel()
	raw := `{"type":"error","code":"invalid_request_error","message":"bad request","param":"","sequence_number":2}`
	var u responses.ResponseStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	s := &sdkStream{}
	_ = s.handleUnion(u)
	var errs []aipapi.Event
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == aipapi.EventError {
			errs = append(errs, ev)
		}
	}
	if len(errs) != 1 {
		t.Fatalf("errors: %+v", errs)
	}
	if errs[0].ErrorCode != "invalid_request_error" || errs[0].ErrorMessage != "bad request" {
		t.Fatalf("event: %+v", errs[0])
	}
}

func TestHandleUnion_streamError_emptyMessage_defaults(t *testing.T) {
	t.Parallel()
	raw := `{"type":"error","code":"server_error","message":"","param":"","sequence_number":1}`
	var u responses.ResponseStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	s := &sdkStream{}
	_ = s.handleUnion(u)
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == aipapi.EventError && ev.ErrorMessage != "stream error" {
			t.Fatalf("expected default message, got %q", ev.ErrorMessage)
		}
	}
}

// Status / queue events must not emit canonical text or tool deltas.
func TestHandleUnion_nonMappedEventTypes_emitNoTextOrToolDeltas(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
	}{
		{name: "response_in_progress", raw: `{"type":"response.in_progress","sequence_number":0}`},
		{name: "response_queued", raw: `{"type":"response.queued","sequence_number":0}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var u responses.ResponseStreamEventUnion
			if err := json.Unmarshal([]byte(tc.raw), &u); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.raw, err)
			}
			s := &sdkStream{}
			_ = s.handleUnion(u)
			for _, ev := range stream.DrainPending(&s.pending) {
				switch ev.Kind {
				case aipapi.EventTextDelta, aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta, aipapi.EventToolCallFinished:
					t.Fatalf("unexpected %s for raw %s", ev.Kind, tc.raw)
				}
			}
		})
	}
}

func TestHandleUnion_toolCallStream_callIDOnlyOnDeltaDone(t *testing.T) {
	t.Parallel()
	s := &sdkStream{}

	rawEvents := []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_only","name":"get_weather"}}`,
		`{"type":"response.function_call_arguments.delta","call_id":"call_only","output_index":0,"delta":"{\"x\":1}"}`,
		`{"type":"response.function_call_arguments.done","call_id":"call_only","output_index":0,"name":"get_weather","arguments":"{\"x\":1}"}`,
	}
	for _, raw := range rawEvents {
		var u responses.ResponseStreamEventUnion
		if err := json.Unmarshal([]byte(raw), &u); err != nil {
			t.Fatal(err)
		}
		if err := s.handleUnion(u); err != nil {
			t.Fatal(err)
		}
	}

	var toolID string
	var argDeltas int
	var finished bool
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == aipapi.EventToolCallStarted {
			toolID = ev.ToolCallID
		}
		if ev.Kind == aipapi.EventToolCallArgsDelta {
			argDeltas++
		}
		if ev.Kind == aipapi.EventToolCallFinished {
			finished = true
		}
	}
	if toolID != "call_only" {
		t.Fatalf("tool call id: %q", toolID)
	}
	if argDeltas == 0 {
		t.Fatal("expected tool call arg deltas")
	}
	if !finished {
		t.Fatal("expected tool call finished")
	}
}

// The typed event no longer exposes the function name, so the mapping reads it
// from the raw payload. Without that, a done event that arrives without a
// preceding output_item.added is dropped by the mapper's
// never-surface-unnamed-tool-calls rule.
func TestHandleUnion_toolCallStream_doneWithoutAddedKeepsName(t *testing.T) {
	t.Parallel()
	s := &sdkStream{}

	raw := `{"type":"response.function_call_arguments.done","sequence_number":0,"item_id":"fc_raw","output_index":0,"name":"get_weather","arguments":"{\"city\":\"NYC\"}"}`
	var u responses.ResponseStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	if err := s.handleUnion(u); err != nil {
		t.Fatal(err)
	}

	var started, finished bool
	var name string
	for _, ev := range stream.DrainPending(&s.pending) {
		switch ev.Kind {
		case aipapi.EventToolCallStarted:
			started = true
			name = ev.ToolName
		case aipapi.EventToolCallFinished:
			finished = true
		}
	}
	if !started {
		t.Fatal("expected tool call started from the done event")
	}
	if name != "get_weather" {
		t.Fatalf("tool name: %q", name)
	}
	if !finished {
		t.Fatal("expected tool call finished")
	}
}

func TestHandleUnion_toolCallStream_mapsToCanonicalToolEvents(t *testing.T) {
	t.Parallel()
	s := &sdkStream{}

	rawAdded := `{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_fc_1","status":"in_progress","name":"get_weather"}}`
	var u1 responses.ResponseStreamEventUnion
	if err := json.Unmarshal([]byte(rawAdded), &u1); err != nil {
		t.Fatal(err)
	}
	_ = s.handleUnion(u1)

	rawDelta := `{"type":"response.function_call_arguments.delta","sequence_number":1,"item_id":"fc_1","output_index":0,"delta":"{\"city\":"}`
	var u2 responses.ResponseStreamEventUnion
	if err := json.Unmarshal([]byte(rawDelta), &u2); err != nil {
		t.Fatal(err)
	}
	_ = s.handleUnion(u2)

	rawDelta2 := `{"type":"response.function_call_arguments.delta","sequence_number":2,"item_id":"fc_1","output_index":0,"delta":"\"NYC\"}"}`
	var u3 responses.ResponseStreamEventUnion
	if err := json.Unmarshal([]byte(rawDelta2), &u3); err != nil {
		t.Fatal(err)
	}
	_ = s.handleUnion(u3)

	rawDone := `{"type":"response.function_call_arguments.done","sequence_number":3,"item_id":"fc_1","output_index":0,"name":"get_weather","arguments":"{\"city\":\"NYC\"}"}`
	var u4 responses.ResponseStreamEventUnion
	if err := json.Unmarshal([]byte(rawDone), &u4); err != nil {
		t.Fatal(err)
	}
	_ = s.handleUnion(u4)

	var kinds []aipapi.EventKind
	var args strings.Builder
	for _, ev := range stream.DrainPending(&s.pending) {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == aipapi.EventToolCallArgsDelta {
			args.WriteString(ev.Delta)
		}
	}
	if kinds[0] != aipapi.EventResponseStarted || kinds[1] != aipapi.EventMessageStarted || kinds[2] != aipapi.EventToolCallStarted {
		t.Fatalf("opening events: %v", kinds)
	}
	if got := args.String(); got != `{"city":"NYC"}` {
		t.Fatalf("combined args: %q", got)
	}
	if kinds[len(kinds)-1] != aipapi.EventToolCallFinished {
		t.Fatalf("last event: %v", kinds)
	}
}

func TestUsageFromResponse_usageDetails(t *testing.T) {
	t.Parallel()
	raw := `{"id":"resp_usage","object":"response","created_at":1715620000,"status":"completed","model":"gpt-4o-mini","output":[],"usage":{"input_tokens":11,"input_tokens_details":{"cached_tokens":3,"x_aip_cache_write_tokens":2},"output_tokens":8,"output_tokens_details":{"reasoning_tokens":5},"total_tokens":19}}`
	var resp responses.Response
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}

	ev := usageFromResponse(resp)
	if ev == nil {
		t.Fatal("usage event is nil")
		return
	}
	if ev.InputTokens != 11 || ev.OutputTokens != 8 {
		t.Fatalf("usage tokens: in=%d out=%d", ev.InputTokens, ev.OutputTokens)
	}
	assertUsageIntField(t, *ev, "CacheReadTokens", 3)
	assertUsageIntField(t, *ev, "CacheWriteTokens", 2)
	assertUsageIntField(t, *ev, "ReasoningTokens", 5)
	assertUsageIntField(t, *ev, "TotalTokens", 19)
	assertUsageRawJSONContains(t, *ev, "total_tokens")
}

func TestUsageFromResponse_explicitAllZeroUsage(t *testing.T) {
	t.Parallel()
	raw := `{"id":"resp_zero","object":"response","created_at":1715620000,"status":"completed","model":"gpt-4o-mini","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`
	var resp responses.Response
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}

	ev := usageFromResponse(resp)
	if ev == nil {
		t.Fatal("explicit all-zero usage must emit an event")
	}
	if ev.InputTokens != 0 || ev.OutputTokens != 0 || ev.TotalTokens != 0 {
		t.Fatalf("tokens = in=%d out=%d total=%d, want zeros", ev.InputTokens, ev.OutputTokens, ev.TotalTokens)
	}
	if !ev.UsagePresence.InputTokens || !ev.UsagePresence.OutputTokens || !ev.UsagePresence.TotalTokens {
		t.Fatalf("presence = %+v, want input/output/total present", ev.UsagePresence)
	}
}

func TestUsageFromResponse_omittedUsage(t *testing.T) {
	t.Parallel()
	raw := `{"id":"resp_nousage","object":"response","created_at":1715620000,"status":"completed","model":"gpt-4o-mini","output":[]}`
	var resp responses.Response
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}
	if ev := usageFromResponse(resp); ev != nil {
		t.Fatalf("omitted usage must not emit, got %#v", ev)
	}
}

type errDecoderResponses struct{ err error }

func (d *errDecoderResponses) Event() ssestream.Event {
	return ssestream.Event{Data: []byte(`{"type":"response.in_progress","sequence_number":0}`)}
}

func (d *errDecoderResponses) Next() bool { return false }

func (d *errDecoderResponses) Close() error { return nil }

func (d *errDecoderResponses) Err() error { return d.err }

func TestSDKStream_Recv_wrapsSDKErr(t *testing.T) {
	t.Parallel()
	root := errors.New("root")
	sdk := ssestream.NewStream[responses.ResponseStreamEventUnion](&errDecoderResponses{err: root}, nil)
	es := NewSDKStream(sdk, 0)
	s, ok := es.(*sdkStream)
	if !ok {
		t.Fatalf("newSDKStream returned %T", es)
	}
	_, err := s.Recv(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "openai-responses: recv stream") {
		t.Fatalf("got %q", err.Error())
	}
	if !errors.Is(err, root) {
		t.Fatalf("underlying: %v", err)
	}
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
