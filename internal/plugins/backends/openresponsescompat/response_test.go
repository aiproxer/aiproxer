package openresponsescompat

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	proto "github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func fixedClockTime() time.Time { return time.Unix(1719900000, 0).UTC() }

const completeResourceJSON = `{
  "id": "resp_native_abc",
  "object": "response",
  "created_at": 1719900000,
  "status": "completed",
  "model": "model-x",
  "output": [
    {"type": "message", "id": "msg_out_1", "status": "completed", "role": "assistant", "content": [{"type": "output_text", "text": "The weather in Paris is sunny."}]},
    {"type": "function_call", "id": "fc_out_1", "status": "completed", "call_id": "call_out_1", "name": "get_weather", "arguments": "{\"location\":\"Paris\"}"},
    {"type": "reasoning", "id": "rs_out_1", "status": "completed", "reasoning": "Let me check the weather service."}
  ],
  "usage": {
    "input_tokens": 5,
    "input_tokens_details": {"cached_tokens": 2},
    "output_tokens": 7,
    "output_tokens_details": {"reasoning_tokens": 3},
    "total_tokens": 12
  }
}`

func defaultResponseTestLimits() ResponseLimits {
	return defaultResponseLimits()
}

func TestParseResource_NonStreamingCompleteLifecycleEvents(t *testing.T) {
	t.Parallel()
	events, native, err := parseResource("my-or", []byte(completeResourceJSON), defaultResponseTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	if native.ResponseID != "resp_native_abc" {
		t.Fatalf("native response id = %q", native.ResponseID)
	}
	if len(native.ItemIDs) != 3 || native.ItemIDs[0] != "msg_out_1" || native.ItemIDs[1] != "fc_out_1" || native.ItemIDs[2] != "rs_out_1" {
		t.Fatalf("native item ids = %v", native.ItemIDs)
	}
	if len(native.ToolCallIDs) != 1 || native.ToolCallIDs[0] != "call_out_1" {
		t.Fatalf("native tool call ids = %v", native.ToolCallIDs)
	}

	events = filterLifecycle(events)
	if len(events) == 0 {
		t.Fatal("no lifecycle events")
	}
	if events[0].Kind != aipapi.EventResponseStarted {
		t.Fatalf("first event = %q, want response_started", events[0].Kind)
	}
	if events[len(events)-1].Kind != aipapi.EventResponseFinished {
		t.Fatalf("last event = %q, want response_finished", events[len(events)-1].Kind)
	}
	var gotText, gotArgs bool
	var gotReasoning, gotUsage bool
	for _, ev := range events {
		switch ev.Kind {
		case aipapi.EventTextDelta:
			if ev.Delta == "The weather in Paris is sunny." {
				gotText = true
			}
		case aipapi.EventToolCallArgsDelta:
			if ev.Delta == `{"location":"Paris"}` {
				gotArgs = true
			}
		case aipapi.EventReasoningDelta:
			if ev.Delta == "Let me check the weather service." {
				gotReasoning = true
			}
		case aipapi.EventUsageDelta:
			if ev.InputTokens == 5 && ev.OutputTokens == 7 && ev.TotalTokens == 12 && ev.CacheReadTokens == 2 && ev.ReasoningTokens == 3 {
				gotUsage = true
			}
		}
	}
	for name, ok := range map[string]bool{
		"text": gotText, "args": gotArgs, "reasoning": gotReasoning, "usage": gotUsage,
	} {
		if !ok {
			t.Fatalf("missing %s lifecycle event: %+v", name, events)
		}
	}
}

func TestParseResource_NonStreamingFeedsProductionStateMachine(t *testing.T) {
	t.Parallel()
	events, _, err := parseResource("my-or", []byte(completeResourceJSON), defaultResponseTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	sm := proto.NewStateMachine(proto.EnvelopeMetadata{
		ResponseID: "resp_proxy_1",
		CreatedAt:  fixedClockTime(),
		Model:      "model-x",
	}, aipapi.GenerationOptions{})
	for i, ev := range events {
		if err := aipapi.ValidateEventEnvelope(&ev); err != nil {
			t.Fatalf("event %d invalid: %v", i, err)
		}
		if _, err := sm.ProcessCanonicalEvent(ev); err != nil {
			t.Fatalf("state machine rejected event %d (%s): %v", i, ev.Kind, err)
		}
	}
	if sm.State() != proto.StateTerminal {
		t.Fatalf("state machine not terminal: %s", sm.State())
	}
	if sm.Status() != "completed" {
		t.Fatalf("state machine status = %q", sm.Status())
	}
	traj := sm.Trajectory()
	if len(traj) != 3 {
		t.Fatalf("trajectory items = %d, want 3", len(traj))
	}
}

func filterLifecycle(events []aipapi.Event) []aipapi.Event {
	var out []aipapi.Event
	for _, ev := range events {
		switch ev.Kind {
		case aipapi.EventResponseStarted, aipapi.EventMessageStarted, aipapi.EventTextDelta,
			aipapi.EventReasoningDelta, aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta,
			aipapi.EventToolCallFinished, aipapi.EventUsageDelta, aipapi.EventError,
			aipapi.EventResponseFinished:
			out = append(out, ev)
		}
	}
	return out
}

func TestParseResource_NonStreamingIncompleteStatusMapsToFinishReason(t *testing.T) {
	t.Parallel()
	body := strings.ReplaceAll(completeResourceJSON, `"status": "completed"`, `"status": "incomplete"`)
	events, _, err := parseResource("my-or", []byte(body), defaultResponseTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Kind != aipapi.EventResponseFinished || last.FinishReason != "length" {
		t.Fatalf("last event = %+v, want response_finished length", last)
	}
	if last.ResponseStatus != "incomplete" {
		t.Fatalf("response status = %q, want explicit incomplete", last.ResponseStatus)
	}
}

func TestParseResource_NonStreamingCompletedStatusCarriesExplicitStatus(t *testing.T) {
	t.Parallel()
	events, _, err := parseResource("my-or", []byte(completeResourceJSON), defaultResponseTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Kind != aipapi.EventResponseFinished {
		t.Fatalf("last event = %+v, want response_finished", last)
	}
	if last.ResponseStatus != "completed" {
		t.Fatalf("response status = %q, want explicit completed", last.ResponseStatus)
	}
}

func TestParseResource_NonStreamingIncompleteDetailsReasonMapped(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		reason string
		want   string
	}{
		{name: "absent_reason_defaults_length", reason: `null`, want: "length"},
		{name: "max_output_tokens_maps_length", reason: `{"reason":"max_output_tokens"}`, want: "length"},
		{name: "content_filter_preserved", reason: `{"reason":"content_filter"}`, want: "content_filter"},
		{name: "custom_reason_preserved", reason: `{"reason":"provider_cutoff"}`, want: "provider_cutoff"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := strings.ReplaceAll(completeResourceJSON, `"status": "completed"`, `"status": "incomplete"`)
			body = strings.Replace(body, `"usage": {`, `"incomplete_details": `+tc.reason+`,
  "usage": {`, 1)
			events, _, err := parseResource("my-or", []byte(body), defaultResponseTestLimits())
			if err != nil {
				t.Fatal(err)
			}
			last := events[len(events)-1]
			if last.Kind != aipapi.EventResponseFinished {
				t.Fatalf("last event = %+v, want response_finished", last)
			}
			if last.FinishReason != tc.want {
				t.Fatalf("finish reason = %q, want %q", last.FinishReason, tc.want)
			}
			if last.ResponseStatus != "incomplete" {
				t.Fatalf("response status = %q, want explicit incomplete", last.ResponseStatus)
			}
		})
	}
}

func TestParseResource_NonStreamingFailedStatusEmitsErrorEvent(t *testing.T) {
	t.Parallel()
	body := `{
		"id": "resp_native_fail",
		"object": "response",
		"created_at": 1719900000,
		"status": "failed",
		"model": "model-x",
		"output": [],
		"error": {"type": "server_error", "message": "upstream boom\nwith details", "code": "provider_broken"}
	}`
	events, _, err := parseResource("my-or", []byte(body), defaultResponseTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Kind != aipapi.EventError {
		t.Fatalf("last event = %+v, want error event", last)
	}
	if last.ErrorCode != "provider_broken" {
		t.Fatalf("error code = %q", last.ErrorCode)
	}
	if last.ErrorMessage != "upstream reported an error" {
		t.Fatalf("error message = %q, want strict generic message", last.ErrorMessage)
	}
}

func TestParseResource_NonStreamingErrorMessageSecretScrubbed(t *testing.T) {
	t.Parallel()
	body := `{
		"id": "resp_native_fail",
		"object": "response",
		"created_at": 1719900000,
		"status": "failed",
		"model": "model-x",
		"output": [],
		"error": {"type": "server_error", "message": "auth failed with secret sk-abc123", "code": "auth"}
	}`
	events, _, err := parseResource("my-or", []byte(body), defaultResponseTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Kind != aipapi.EventError {
		t.Fatalf("last event = %+v, want error event", last)
	}
	if strings.Contains(last.ErrorMessage, "sk-abc123") || strings.Contains(last.ErrorMessage, "secret") {
		t.Fatalf("error message echoed secret: %q", last.ErrorMessage)
	}
}

func TestParseResource_NonStreamingItemReferenceIsPrivateEvidence(t *testing.T) {
	t.Parallel()
	body := `{
		"id": "resp_native_ref",
		"object": "response",
		"created_at": 1719900000,
		"status": "completed",
		"model": "model-x",
		"output": [
			{"type": "item_reference", "id": "resp_native_prev"}
		]
	}`
	events, native, err := parseResource("my-or", []byte(body), defaultResponseTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(native.ItemIDs) != 1 || native.ItemIDs[0] != "resp_native_prev" {
		t.Fatalf("native item ids = %v", native.ItemIDs)
	}
	for _, ev := range events {
		if strings.Contains(string(ev.Kind), "resp_native") {
			t.Fatalf("native reference leaked into canonical stream: %+v", events)
		}
	}
	last := events[len(events)-1]
	if last.Kind != aipapi.EventResponseFinished {
		t.Fatalf("last event = %+v", last)
	}
}

func TestParseResource_NonStreamingMalformedRejected(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"not_json":          `{`,
		"trailing_data":     `{} {"x":1}`,
		"unexpected_status": `{"id":"r","object":"response","status":"in_progress","model":"m","output":[]}`,
		"extension_output":  `{"id":"r","object":"response","status":"completed","model":"m","output":[{"type":"acme:widget","namespace":"acme","data":{"k":1}}]}`,
		"message_no_text":   `{"id":"r","object":"response","status":"completed","model":"m","output":[{"type":"message","role":"assistant","content":[{"type":"output_image","image_url":"x"}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, _, err := parseResource("my-or", []byte(body), defaultResponseTestLimits())
			if err == nil {
				t.Fatal("expected malformed response rejection")
			}
			if !errors.Is(err, ErrMalformedResponse) {
				t.Fatalf("error = %v, want ErrMalformedResponse", err)
			}
		})
	}
}

func TestParseResource_NonStreamingObjectDiscriminatorValidated(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"missing_object":   `{"id":"r","status":"completed","model":"m","output":[]}`,
		"wrong_object":     `{"id":"r","object":"response.compaction","status":"completed","model":"m","output":[]}`,
		"unrelated_object": `{"id":"r","object":"chat.completion","status":"completed","model":"m","output":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			events, _, err := parseResource("my-or", []byte(body), defaultResponseTestLimits())
			if !errors.Is(err, ErrMalformedResponse) {
				t.Fatalf("error = %v, want ErrMalformedResponse", err)
			}
			if len(events) != 0 {
				t.Fatalf("object mismatch emitted %d events, want zero", len(events))
			}
		})
	}
}

func TestParseResource_NonStreamingResponseLimitsEnforced(t *testing.T) {
	t.Parallel()
	limits := defaultResponseTestLimits()

	limits.MaxItems = 1
	_, _, err := parseResource("my-or", []byte(completeResourceJSON), limits)
	if err == nil || !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("items limit error = %v", err)
	}

	limits = defaultResponseTestLimits()
	limits.MaxTextBytes = 1
	_, _, err = parseResource("my-or", []byte(completeResourceJSON), limits)
	if err == nil || !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("text limit error = %v", err)
	}

	limits = defaultResponseTestLimits()
	limits.MaxReasoningBytes = 1
	_, _, err = parseResource("my-or", []byte(completeResourceJSON), limits)
	if err == nil || !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("reasoning limit error = %v", err)
	}
}

func TestNonStreamingReadHTTPBodyLimited_ClosesAndBounds(t *testing.T) {
	t.Parallel()
	body := io.NopCloser(strings.NewReader("hello"))
	got, err := readHTTPBodyLimited(body, 10)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("body = %q", got)
	}
	oversized := io.NopCloser(strings.NewReader("hello world"))
	if _, err := readHTTPBodyLimited(oversized, 4); err == nil {
		t.Fatal("expected oversized body rejection")
	}
}
