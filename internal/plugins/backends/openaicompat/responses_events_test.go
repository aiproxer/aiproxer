package openaicompat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3/responses"
)

func TestResponsesStreamFinishOnEOF_noResponseZeroEventsClosed(t *testing.T) {
	t.Parallel()
	s := newUnitResponsesStream()
	if err := s.finishOnEOF(); err != nil {
		t.Fatal(err)
	}
	if events := stream.DrainPending(&s.pending); len(events) != 0 {
		t.Fatalf("events = %+v", events)
	}
	if !s.closed {
		t.Fatal("expected stream closed")
	}
}

func TestResponsesStreamFinishOnEOF_afterCompletedNoDuplicateFinish(t *testing.T) {
	t.Parallel()
	s := newUnitResponsesStream()
	s.terminalEmitted = true

	if err := s.finishOnEOF(); err != nil {
		t.Fatal(err)
	}
	if events := stream.DrainPending(&s.pending); len(events) != 0 {
		t.Fatalf("events = %+v", events)
	}
	if !s.closed {
		t.Fatal("expected stream closed")
	}
}

func TestResponsesStreamFinishOnEOF_afterResponseStarted(t *testing.T) {
	t.Parallel()
	s := newUnitResponsesStream()
	if err := s.mapper.ResponseCreated(); err != nil {
		t.Fatal(err)
	}

	if err := s.finishOnEOF(); err != nil {
		t.Fatal(err)
	}
	events := stream.DrainPending(&s.pending)
	if len(events) != 2 || events[0].Kind != aipapi.EventResponseStarted || events[1].Kind != aipapi.EventResponseFinished {
		t.Fatalf("events = %+v", events)
	}
}

func TestResponsesStreamFinishOnEOF_unresolvedReasoningDraft_errors(t *testing.T) {
	t.Parallel()
	s := newUnitResponsesStream()
	var item responses.ResponseOutputItemUnion
	if err := json.Unmarshal([]byte(`{"type":"reasoning","id":"rs_1","summary":[]}`), &item); err != nil {
		t.Fatal(err)
	}
	if err := s.mapper.ReasoningOutputItemAdded(0, item); err != nil {
		t.Fatal(err)
	}
	err := s.finishOnEOF()
	if err == nil {
		t.Fatal("expected unresolved reasoning assembly error on clean EOF")
	}
	if strings.Contains(err.Error(), "rs_1") {
		t.Fatalf("error must not leak id: %v", err)
	}
	if !s.closed {
		t.Fatal("expected stream closed after EOF assembly error")
	}
}

func TestResponsesStreamCancel_abortsUnresolvedReasoningDrafts(t *testing.T) {
	t.Parallel()
	s := newUnitResponsesStream()
	var item responses.ResponseOutputItemUnion
	if err := json.Unmarshal([]byte(`{"type":"reasoning","id":"rs_open","summary":[]}`), &item); err != nil {
		t.Fatal(err)
	}
	if err := s.mapper.ReasoningOutputItemAdded(0, item); err != nil {
		t.Fatal(err)
	}
	_ = s.Cancel(context.Background(), leglifecycle.CancelCause{})
	if err := s.mapper.FinalizeOnEOF(); err != nil {
		t.Fatalf("Cancel/Close must abort drafts so FinalizeOnEOF is nil: %v", err)
	}
}

func TestHandleUnion_streamError_emitsEventError(t *testing.T) {
	t.Parallel()
	raw := `{"type":"error","code":"invalid_request_error","message":"bad request","sequence_number":2}`
	var u responses.ResponseStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}

	s := newUnitResponsesStream()
	if err := s.handleUnion(u); err != nil {
		t.Fatal(err)
	}

	sawResponseStarted := false
	sawError := false
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == aipapi.EventResponseStarted {
			sawResponseStarted = true
		}
		if ev.Kind == aipapi.EventError {
			sawError = true
			if ev.ErrorCode != "invalid_request_error" || ev.ErrorMessage != "bad request" {
				t.Fatalf("error event: %+v", ev)
			}
		}
	}
	if !sawResponseStarted {
		t.Fatal("expected EventResponseStarted before stream error")
	}
	if !sawError {
		t.Fatal("expected EventError")
	}
}

func TestHandleUnion_toolCallStream_callIDOnlyOnDeltaDone(t *testing.T) {
	t.Parallel()
	s := newUnitResponsesStream()

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

func TestHandleUnion_toolCallStream_mapsToolEvents(t *testing.T) {
	t.Parallel()
	s := newUnitResponsesStream()

	rawEvents := []string{
		`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_fc_1","status":"in_progress","name":"get_weather"}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":1,"item_id":"fc_1","output_index":0,"delta":"{\"city\":"}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":2,"item_id":"fc_1","output_index":0,"delta":"\"NYC\"}"}`,
		`{"type":"response.function_call_arguments.done","sequence_number":3,"item_id":"fc_1","output_index":0,"name":"get_weather","arguments":"{\"city\":\"NYC\"}"}`,
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

	var kinds []aipapi.EventKind
	var args strings.Builder
	for _, ev := range stream.DrainPending(&s.pending) {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == aipapi.EventToolCallArgsDelta {
			args.WriteString(ev.Delta)
		}
	}
	if len(kinds) < 4 {
		t.Fatalf("unexpected events: %v", kinds)
	}
	if kinds[0] != aipapi.EventResponseStarted || kinds[1] != aipapi.EventMessageStarted || kinds[2] != aipapi.EventToolCallStarted {
		t.Fatalf("opening events: %v", kinds)
	}
	if kinds[len(kinds)-1] != aipapi.EventToolCallFinished {
		t.Fatalf("last event: %v", kinds[len(kinds)-1])
	}
	if got := args.String(); got != `{"city":"NYC"}` {
		t.Fatalf("combined args: %q", got)
	}
}

func TestHandleUnion_outputItemDone_emitsCompleteToolCall(t *testing.T) {
	t.Parallel()
	s := newUnitResponsesStream()

	raw := `{"type":"response.output_item.done","sequence_number":1,"output_index":0,"item":{"type":"function_call","id":"fc_done","call_id":"call_fc_done","status":"completed","name":"get_weather","arguments":"{\"city\":\"NYC\"}"}}`
	var u responses.ResponseStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	if err := s.handleUnion(u); err != nil {
		t.Fatal(err)
	}

	kinds := eventKinds(stream.DrainPending(&s.pending))
	want := []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventToolCallStarted,
		aipapi.EventToolCallArgsDelta,
		aipapi.EventToolCallFinished,
	}
	if len(kinds) != len(want) {
		t.Fatalf("events = %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("event[%d] = %v, want %v", i, kinds[i], want[i])
		}
	}
}

func TestHandleUnion_completed_emitsAssistantMedia(t *testing.T) {
	t.Parallel()
	raw := `{
  "type": "response.completed",
  "sequence_number": 1,
  "response": {
    "id": "resp_x",
    "object": "response",
    "status": "completed",
    "model": "gpt-4o-mini",
    "output": [
      {
        "type": "message",
        "id": "m1",
        "status": "completed",
        "role": "assistant",
        "content": [
          {"type": "output_text", "text": "see"},
          {"type": "input_image", "image_url": {"url": "https://img.example/x.png"}},
          {"type": "input_file", "file_id": "file_doc_1"}
        ]
      }
    ]
  }
}`
	var u responses.ResponseStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}

	s := newUnitResponsesStream()
	if err := s.handleUnion(u); err != nil {
		t.Fatal(err)
	}

	gotImage := ""
	gotFile := ""
	sawFinished := false
	for _, ev := range stream.DrainPending(&s.pending) {
		switch ev.Kind {
		case aipapi.EventAssistantImageRef:
			gotImage = ev.AssistantRef
		case aipapi.EventAssistantFileRef:
			gotFile = ev.AssistantRef
		case aipapi.EventResponseFinished:
			sawFinished = true
		}
	}
	if gotImage != "https://img.example/x.png" {
		t.Fatalf("image ref: %q", gotImage)
	}
	if gotFile != "file_doc_1" {
		t.Fatalf("file ref: %q", gotFile)
	}
	if !sawFinished {
		t.Fatal("expected EventResponseFinished")
	}
}

func TestResponseEvents_textAndUsage(t *testing.T) {
	t.Parallel()
	raw := `{
  "id": "resp_ns",
  "object": "response",
  "created_at": 1715620000,
  "status": "completed",
  "model": "gpt-4o-mini",
  "output": [
    {
      "type": "message",
      "id": "msg_out",
      "status": "completed",
      "role": "assistant",
      "content": [
        {"type": "output_text", "text": "hello"}
      ]
    }
  ],
  "usage": {
    "input_tokens": 1,
    "output_tokens": 2,
    "total_tokens": 3,
    "output_tokens_details": {"reasoning_tokens": 4}
  }
}`
	var resp responses.Response
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}

	events, err := ResponseEvents(resp)
	if err != nil {
		t.Fatal(err)
	}
	kinds := eventKinds(events)
	want := []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventTextDelta,
		aipapi.EventUsageDelta,
		aipapi.EventResponseFinished,
	}
	if len(kinds) != len(want) {
		t.Fatalf("events = %v", kinds)
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

func TestHandleUnion_reasoningOutputItemDone_exactPart(t *testing.T) {
	t.Parallel()
	raw := `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[],"status":"completed"}}`
	var u responses.ResponseStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	s := newUnitResponsesStream()
	if err := s.handleUnion(u); err != nil {
		t.Fatal(err)
	}
	var parts int
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == aipapi.EventReasoningDelta {
			t.Fatal("no EventReasoningDelta")
		}
		if ev.Kind == aipapi.EventReasoningPart {
			parts++
			if ev.Reasoning == nil || ev.Reasoning.Dialect != aipapi.ReasoningDialectOpenAIResponsesItemV1 {
				t.Fatalf("bad part: %+v", ev.Reasoning)
			}
			if !strings.Contains(string(ev.Reasoning.Opaque), `"rs_1"`) {
				t.Fatalf("opaque=%s", ev.Reasoning.Opaque)
			}
		}
	}
	if parts != 1 {
		t.Fatalf("parts=%d", parts)
	}
}

func TestHandleUnion_reasoningDoneUnknownField_noExactPart(t *testing.T) {
	t.Parallel()
	s := newUnitResponsesStream()
	for _, raw := range []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}`,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"SECRET_SUM"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"SECRET_SUM"}],"extra":1,"status":"completed"}}`,
	} {
		var u responses.ResponseStreamEventUnion
		if err := json.Unmarshal([]byte(raw), &u); err != nil {
			t.Fatal(err)
		}
		err := s.handleUnion(u)
		if strings.Contains(raw, `"extra"`) {
			if err == nil {
				t.Fatal("expected unknown field error")
			}
			if strings.Contains(err.Error(), "SECRET_SUM") || strings.Contains(err.Error(), "rs_1") {
				t.Fatalf("content-safe error required: %v", err)
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, ev := range stream.DrainPending(&s.pending) {
		if ev.Kind == aipapi.EventReasoningPart {
			t.Fatal("unknown done field must not emit EventReasoningPart")
		}
	}
}
