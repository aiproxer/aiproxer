package openresponsescompat

import (
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func rec(eventType, data string) sseRecord {
	return sseRecord{eventType: eventType, data: []byte(data)}
}

func mustMap(t *testing.T, records ...sseRecord) ([]aipapi.Event, *streamMapper) {
	t.Helper()
	m := newStreamMapper("my-or", defaultResponseTestLimits())
	var events []aipapi.Event
	for i, r := range records {
		evs, err := m.mapRecord(r)
		if err != nil {
			t.Fatalf("record %d (%q) rejected: %v", i, r.eventType, err)
		}
		events = append(events, evs...)
	}
	return events, m
}

func created(data string) sseRecord {
	return rec("response.created", data)
}

func completed() sseRecord {
	return rec("response.completed", `{"type":"response.completed","sequence_number":8,"response":{"id":"resp_abc","object":"response","status":"completed","model":"model-x","output":[],"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}}`)
}

func textRecords() []sseRecord {
	return []sseRecord{
		created(`{"type":"response.created","sequence_number":0,"response":{"id":"resp_abc","object":"response","status":"in_progress","model":"model-x","output":[]}}`),
		rec("response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}}`),
		rec("response.content_part.added", `{"type":"response.content_part.added","sequence_number":2,"item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`),
		rec("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"Hello"}`),
		rec("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_1","output_index":0,"content_index":0,"delta":" world"}`),
		rec("response.output_text.done", `{"type":"response.output_text.done","sequence_number":5,"item_id":"msg_1","output_index":0,"content_index":0,"text":"Hello world"}`),
		rec("response.content_part.done", `{"type":"response.content_part.done","sequence_number":6,"item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":"Hello world"}}`),
		rec("response.output_item.done", `{"type":"response.output_item.done","sequence_number":7,"output_index":0,"item":{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello world"}]}}`),
		completed(),
	}
}

func TestStreamMapper_TextLifecyclePreserved(t *testing.T) {
	t.Parallel()
	events, m := mustMap(t, textRecords()...)
	kinds := kindsOf(events)
	want := []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventTextDelta,
		aipapi.EventTextDelta,
		aipapi.EventUsageDelta,
		aipapi.EventResponseFinished,
	}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds = %v, want %v", kinds, want)
		}
	}
	if events[2].Delta != "Hello" || events[3].Delta != " world" {
		t.Fatalf("deltas = %q, %q", events[2].Delta, events[3].Delta)
	}
	usage := events[len(events)-2]
	if usage.InputTokens != 5 || usage.OutputTokens != 3 || usage.TotalTokens != 8 {
		t.Fatalf("usage = %+v", usage)
	}
	if m.Native().ResponseID != "resp_abc" {
		t.Fatalf("native response id = %q", m.Native().ResponseID)
	}
	if len(m.Native().ItemIDs) != 1 || m.Native().ItemIDs[0] != "msg_1" {
		t.Fatalf("native item ids = %v", m.Native().ItemIDs)
	}
	for _, ev := range events {
		if strings.Contains(string(ev.Kind), "resp_abc") || strings.Contains(ev.Delta, "resp_abc") {
			t.Fatalf("native id leaked into canonical event: %+v", ev)
		}
	}
}

func TestStreamMapper_FeedsProductionStateMachine(t *testing.T) {
	t.Parallel()
	events, _ := mustMap(t, textRecords()...)
	for i, ev := range events {
		if err := aipapi.ValidateEventEnvelope(&ev); err != nil {
			t.Fatalf("event %d invalid: %v", i, err)
		}
	}
	if err := aipapi.ValidateEventSequence(events); err != nil {
		t.Fatalf("canonical sequence invalid: %v", err)
	}
}

func TestStreamMapper_ToolCallLifecycle(t *testing.T) {
	t.Parallel()
	records := []sseRecord{
		created(`{"type":"response.created","sequence_number":0}`),
		rec("response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"call_1","name":"get_weather","arguments":""}}`),
		rec("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","sequence_number":2,"item_id":"fc_1","output_index":0,"call_id":"call_1","delta":"{\"loc"}`),
		rec("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","sequence_number":3,"item_id":"fc_1","output_index":0,"call_id":"call_1","delta":"ation\":\"Paris\"}"}`),
		rec("response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","sequence_number":4,"item_id":"fc_1","output_index":0,"call_id":"call_1","arguments":"{\"location\":\"Paris\"}"}`),
		rec("response.output_item.done", `{"type":"response.output_item.done","sequence_number":5,"output_index":0,"item":{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"get_weather","arguments":"{\"location\":\"Paris\"}"}}`),
		completed(),
	}
	events, _ := mustMap(t, records...)
	kinds := kindsOf(events)
	want := []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventToolCallStarted,
		aipapi.EventToolCallArgsDelta,
		aipapi.EventToolCallArgsDelta,
		aipapi.EventToolCallFinished,
		aipapi.EventUsageDelta,
		aipapi.EventResponseFinished,
	}
	assertKinds(t, kinds, want)
	if events[2].ToolCallID != "call_1" || events[2].ToolName != "get_weather" {
		t.Fatalf("tool start = %+v", events[2])
	}
	if events[3].ToolCallID != "call_1" || events[3].Delta != `{"loc` {
		t.Fatalf("args delta = %+v", events[3])
	}
	if events[5].ToolCallID != "call_1" {
		t.Fatalf("tool finish = %+v", events[5])
	}
}

func TestStreamMapper_ToolArgsCallIDMismatchRejected(t *testing.T) {
	t.Parallel()
	m := newStreamMapper("my-or", defaultResponseTestLimits())
	if _, err := m.mapRecord(created(`{"type":"response.created","sequence_number":0}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.mapRecord(rec("response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"get_weather"}}`)); err != nil {
		t.Fatal(err)
	}
	_, err := m.mapRecord(rec("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","sequence_number":2,"call_id":"call_other","delta":"x"}`))
	if err == nil || !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("error = %v, want ErrMalformedResponse", err)
	}
}

func TestStreamMapper_InlineReasoningTextPreserved(t *testing.T) {
	t.Parallel()
	records := []sseRecord{
		created(`{"type":"response.created","sequence_number":0}`),
		rec("response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"rs_1","type":"reasoning","status":"in_progress","reasoning":"inline reasoning"}}`),
		rec("response.output_item.done", `{"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":{"id":"rs_1","type":"reasoning","status":"completed"}}`),
		completed(),
	}
	events, _ := mustMap(t, records...)
	assertKinds(t, kindsOf(events), []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventReasoningDelta,
		aipapi.EventUsageDelta,
		aipapi.EventResponseFinished,
	})
	if events[2].Delta != "inline reasoning" {
		t.Fatalf("reasoning delta = %q", events[2].Delta)
	}
}

func TestStreamMapper_ReasoningLifecycle(t *testing.T) {
	t.Parallel()
	records := []sseRecord{
		created(`{"type":"response.created","sequence_number":0}`),
		rec("response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"rs_1","type":"reasoning","status":"in_progress","reasoning":""}}`),
		rec("response.reasoning_text.delta", `{"type":"response.reasoning_text.delta","sequence_number":2,"item_id":"rs_1","output_index":0,"content_index":0,"delta":"let me think"}`),
		rec("response.reasoning_text.done", `{"type":"response.reasoning_text.done","sequence_number":3,"item_id":"rs_1","output_index":0,"content_index":0,"text":"let me think"}`),
		rec("response.output_item.done", `{"type":"response.output_item.done","sequence_number":4,"output_index":0,"item":{"id":"rs_1","type":"reasoning","status":"completed"}}`),
		completed(),
	}
	events, _ := mustMap(t, records...)
	assertKinds(t, kindsOf(events), []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventReasoningDelta,
		aipapi.EventUsageDelta,
		aipapi.EventResponseFinished,
	})
	if events[2].Delta != "let me think" {
		t.Fatalf("reasoning delta = %q", events[2].Delta)
	}
}

func TestStreamMapper_ErrorEventAndFailed(t *testing.T) {
	t.Parallel()
	records := []sseRecord{
		created(`{"type":"response.created","sequence_number":0}`),
		rec("error", `{"type":"error","code":"provider_broken","message":"upstream boom\nwith details"}`),
		rec("response.failed", `{"type":"response.failed","sequence_number":2,"response":{"id":"resp_err","object":"response","status":"failed","model":"model-x","output":[]}}`),
		rec("", "[DONE]"),
	}
	m := newStreamMapper("my-or", defaultResponseTestLimits())
	var events []aipapi.Event
	for _, r := range records {
		evs, err := m.mapRecord(r)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, evs...)
	}
	last := events[len(events)-1]
	if last.Kind != aipapi.EventError {
		t.Fatalf("last event = %+v, want error event", last)
	}
	if last.ErrorCode != "provider_broken" {
		t.Fatalf("error code = %q", last.ErrorCode)
	}
	if strings.Contains(last.ErrorMessage, "\n") {
		t.Fatalf("error message not sanitized: %q", last.ErrorMessage)
	}
}

func TestStreamMapper_FailedWithoutErrorEventUsesResourceError(t *testing.T) {
	t.Parallel()
	m := newStreamMapper("my-or", defaultResponseTestLimits())
	records := []sseRecord{
		created(`{"type":"response.created","sequence_number":0}`),
		rec("response.failed", `{"type":"response.failed","sequence_number":1,"response":{"id":"r","status":"failed","model":"m","output":[],"error":{"type":"server_error","message":"boom","code":"model_failed"}}}`),
	}
	var events []aipapi.Event
	for _, r := range records {
		evs, err := m.mapRecord(r)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, evs...)
	}
	last := events[len(events)-1]
	if last.Kind != aipapi.EventError || last.ErrorCode != "model_failed" {
		t.Fatalf("last event = %+v", last)
	}
}

func TestStreamMapper_IncompleteMapsToLengthFinish(t *testing.T) {
	t.Parallel()
	m := newStreamMapper("my-or", defaultResponseTestLimits())
	if _, err := m.mapRecord(created(`{"type":"response.created","sequence_number":0}`)); err != nil {
		t.Fatal(err)
	}
	events, err := m.mapRecord(rec("response.incomplete", `{"type":"response.incomplete","sequence_number":1,"response":{"id":"r","status":"incomplete","model":"m","output":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Kind != aipapi.EventResponseFinished || last.FinishReason != "length" {
		t.Fatalf("last event = %+v", last)
	}
	if last.ResponseStatus != "incomplete" {
		t.Fatalf("response status = %q, want explicit incomplete", last.ResponseStatus)
	}
}

func TestStreamMapper_IncompleteDetailsReasonMapped(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		reason string
		want   string
	}{
		{name: "max_output_tokens_maps_length", reason: `{"reason":"max_output_tokens"}`, want: "length"},
		{name: "content_filter_preserved", reason: `{"reason":"content_filter"}`, want: "content_filter"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newStreamMapper("my-or", defaultResponseTestLimits())
			if _, err := m.mapRecord(created(`{"type":"response.created","sequence_number":0}`)); err != nil {
				t.Fatal(err)
			}
			body := `{"type":"response.incomplete","sequence_number":1,"response":{"id":"r","status":"incomplete","model":"m","output":[],"incomplete_details":` + tc.reason + `}}`
			events, err := m.mapRecord(rec("response.incomplete", body))
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

func TestStreamMapper_CompletedCarriesExplicitStatus(t *testing.T) {
	t.Parallel()
	m := newStreamMapper("my-or", defaultResponseTestLimits())
	if _, err := m.mapRecord(created(`{"type":"response.created","sequence_number":0}`)); err != nil {
		t.Fatal(err)
	}
	events, err := m.mapRecord(rec("response.completed", `{"type":"response.completed","sequence_number":1,"response":{"id":"r","status":"completed","model":"m","output":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Kind != aipapi.EventResponseFinished || last.ResponseStatus != "completed" {
		t.Fatalf("last event = %+v, want response_finished with explicit completed", last)
	}
}

func TestStreamMapper_UnknownPrefixedOutputPreserved(t *testing.T) {
	t.Parallel()
	m := newStreamMapper("my-or", defaultResponseTestLimits())
	if _, err := m.mapRecord(created(`{"type":"response.created","sequence_number":0}`)); err != nil {
		t.Fatal(err)
	}
	// A valid vendor-prefixed extension event must not disturb the stream.
	evs, err := m.mapRecord(rec("acme:telemetry", `{"type":"acme:telemetry","sequence_number":1,"latency_ms":4}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 0 {
		t.Fatalf("extension event produced canonical events: %+v", evs)
	}
	// A vendor-prefixed output item is accepted and preserved as private evidence.
	evs, err = m.mapRecord(rec("response.output_item.added", `{"type":"response.output_item.added","sequence_number":2,"item":{"id":"wc_1","type":"acme:widget","namespace":"acme","status":"completed","data":{"k":1}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 0 {
		t.Fatalf("extension item produced canonical events: %+v", evs)
	}
	// The rest of the text lifecycle still works after prefixed output.
	evs, err = m.mapRecord(rec("response.output_item.added", `{"type":"response.output_item.added","sequence_number":3,"item":{"id":"msg_1","type":"message","role":"assistant"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != aipapi.EventMessageStarted {
		t.Fatalf("events = %+v", evs)
	}
	native := m.Native()
	found := false
	for _, tt := range native.ExtensionTypes {
		if tt == "acme:telemetry" || tt == "acme:widget" {
			found = true
		}
	}
	if !found {
		t.Fatalf("prefixed output not preserved in evidence: %v", native.ExtensionTypes)
	}
}

func TestStreamMapper_MalformedRejected(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		records []sseRecord
	}{
		{
			name:    "event_before_start",
			records: []sseRecord{rec("response.output_item.added", `{"type":"response.output_item.added","item":{"id":"m","type":"message","role":"assistant"}}`)},
		},
		{
			name: "duplicate_start",
			records: []sseRecord{
				created(`{"type":"response.created","sequence_number":0}`),
				created(`{"type":"response.created","sequence_number":1}`),
			},
		},
		{
			name:    "terminal_without_start",
			records: []sseRecord{rec("response.completed", `{"type":"response.completed"}`)},
		},
		{
			name: "text_delta_without_content_part",
			records: []sseRecord{
				created(`{"type":"response.created","sequence_number":0}`),
				rec("response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"item":{"id":"m","type":"message","role":"assistant"}}`),
				rec("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":2,"delta":"x"}`),
			},
		},
		{
			name: "args_delta_without_item",
			records: []sseRecord{
				created(`{"type":"response.created","sequence_number":0}`),
				rec("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","sequence_number":1,"call_id":"c","delta":"x"}`),
			},
		},
		{
			name: "unknown_unprefixed_event",
			records: []sseRecord{
				created(`{"type":"response.created","sequence_number":0}`),
				rec("response.bogus", `{"type":"response.bogus","sequence_number":1}`),
			},
		},
		{
			name: "duplicate_terminal",
			records: []sseRecord{
				created(`{"type":"response.created","sequence_number":0}`),
				completed(),
				completed(),
			},
		},
		{
			name: "event_after_terminal",
			records: []sseRecord{
				created(`{"type":"response.created","sequence_number":0}`),
				completed(),
				rec("response.output_item.added", `{"type":"response.output_item.added","sequence_number":2,"item":{"id":"m","type":"message","role":"assistant"}}`),
			},
		},
		{
			name: "done_before_terminal",
			records: []sseRecord{
				created(`{"type":"response.created","sequence_number":0}`),
				rec("", "[DONE]"),
			},
		},
		{
			name: "duplicate_done",
			records: []sseRecord{
				created(`{"type":"response.created","sequence_number":0}`),
				completed(),
				rec("", "[DONE]"),
				rec("", "[DONE]"),
			},
		},
		{
			name: "sequence_decreased",
			records: []sseRecord{
				created(`{"type":"response.created","sequence_number":5}`),
				rec("response.completed", `{"type":"response.completed","sequence_number":4}`),
			},
		},
		{
			name: "sequence_duplicate",
			records: []sseRecord{
				created(`{"type":"response.created","sequence_number":1}`),
				rec("response.completed", `{"type":"response.completed","sequence_number":1}`),
			},
		},
		{
			name: "unprefixed_output_item",
			records: []sseRecord{
				created(`{"type":"response.created","sequence_number":0}`),
				rec("response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"item":{"type":"bogus_type","id":"x"}}`),
			},
		},
		{
			name: "refusal_content_part",
			records: []sseRecord{
				created(`{"type":"response.created","sequence_number":0}`),
				rec("response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"item":{"id":"m","type":"message","role":"assistant"}}`),
				rec("response.content_part.added", `{"type":"response.content_part.added","sequence_number":2,"part":{"type":"refusal","refusal":"no"}}`),
			},
		},
		{
			name: "created_with_terminal_status",
			records: []sseRecord{
				rec("response.created", `{"type":"response.created","sequence_number":0,"response":{"id":"r","status":"completed","model":"m","output":[]}}`),
			},
		},
		{
			name: "event_body_type_mismatch",
			records: []sseRecord{
				rec("response.created", `{"type":"response.completed","sequence_number":0}`),
			},
		},
		{
			name: "missing_event_field",
			records: []sseRecord{
				rec("", `{"type":"response.created","sequence_number":0}`),
			},
		},
		{
			name: "content_part_added_before_start",
			records: []sseRecord{
				rec("response.content_part.added", `{"type":"response.content_part.added","sequence_number":0}`),
			},
		},
		{
			name: "text_done_without_content_part",
			records: []sseRecord{
				created(`{"type":"response.created","sequence_number":0}`),
				rec("response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"item":{"id":"m","type":"message","role":"assistant"}}`),
				rec("response.output_text.done", `{"type":"response.output_text.done","sequence_number":2,"delta":"x"}`),
			},
		},
		{
			name: "content_part_done_without_content_part",
			records: []sseRecord{
				created(`{"type":"response.created","sequence_number":0}`),
				rec("response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"item":{"id":"m","type":"message","role":"assistant"}}`),
				rec("response.content_part.done", `{"type":"response.content_part.done","sequence_number":2}`),
			},
		},
		{
			name: "reasoning_delta_without_item",
			records: []sseRecord{
				created(`{"type":"response.created","sequence_number":0}`),
				rec("response.reasoning_text.delta", `{"type":"response.reasoning_text.delta","sequence_number":1,"delta":"x"}`),
			},
		},
		{
			name: "reasoning_done_without_item",
			records: []sseRecord{
				created(`{"type":"response.created","sequence_number":0}`),
				rec("response.reasoning_text.done", `{"type":"response.reasoning_text.done","sequence_number":1}`),
			},
		},
		{
			name: "output_item_done_without_open_item",
			records: []sseRecord{
				created(`{"type":"response.created","sequence_number":0}`),
				rec("response.output_item.done", `{"type":"response.output_item.done","sequence_number":1}`),
			},
		},
		{
			name: "output_item_added_while_open",
			records: []sseRecord{
				created(`{"type":"response.created","sequence_number":0}`),
				rec("response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"item":{"id":"m1","type":"message","role":"assistant"}}`),
				rec("response.output_item.added", `{"type":"response.output_item.added","sequence_number":2,"item":{"id":"m2","type":"message","role":"assistant"}}`),
			},
		},
		{
			name: "content_part_added_while_open",
			records: []sseRecord{
				created(`{"type":"response.created","sequence_number":0}`),
				rec("response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"item":{"id":"m","type":"message","role":"assistant"}}`),
				rec("response.content_part.added", `{"type":"response.content_part.added","sequence_number":2,"part":{"type":"output_text","text":""}}`),
				rec("response.content_part.added", `{"type":"response.content_part.added","sequence_number":3,"part":{"type":"output_text","text":""}}`),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newStreamMapper("my-or", defaultResponseTestLimits())
			for i, r := range tc.records {
				_, err := m.mapRecord(r)
				if err == nil {
					continue
				}
				if !errors.Is(err, ErrMalformedResponse) {
					t.Fatalf("record %d error = %v, want ErrMalformedResponse", i, err)
				}
				return
			}
			t.Fatal("expected malformed response rejection")
		})
	}
}

func TestStreamMapper_SequenceAbsentAllowed(t *testing.T) {
	t.Parallel()
	m := newStreamMapper("my-or", defaultResponseTestLimits())
	if _, err := m.mapRecord(created(`{"type":"response.created"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.mapRecord(completed()); err != nil {
		t.Fatal(err)
	}
}

func TestStreamMapper_LimitsEnforced(t *testing.T) {
	t.Parallel()
	limits := defaultResponseTestLimits()

	limits.MaxItems = 1
	m := newStreamMapper("my-or", limits)
	records := []sseRecord{
		created(`{"type":"response.created","sequence_number":0}`),
		rec("response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"item":{"id":"m1","type":"message","role":"assistant"}}`),
		rec("response.output_item.done", `{"type":"response.output_item.done","sequence_number":2,"item":{"id":"m1","type":"message"}}`),
		rec("response.output_item.added", `{"type":"response.output_item.added","sequence_number":3,"item":{"id":"m2","type":"message","role":"assistant"}}`),
	}
	var wantErr error
	for _, r := range records {
		_, err := m.mapRecord(r)
		if err != nil {
			wantErr = err
			break
		}
	}
	if wantErr == nil || !errors.Is(wantErr, ErrLimitExceeded) {
		t.Fatalf("items limit error = %v", wantErr)
	}

	limits = defaultResponseTestLimits()
	limits.MaxTextBytes = 4
	m = newStreamMapper("my-or", limits)
	records = []sseRecord{
		created(`{"type":"response.created","sequence_number":0}`),
		rec("response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"item":{"id":"m","type":"message","role":"assistant"}}`),
		rec("response.content_part.added", `{"type":"response.content_part.added","sequence_number":2,"part":{"type":"output_text","text":""}}`),
		rec("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":3,"delta":"hello"}`),
	}
	wantErr = nil
	for _, r := range records {
		_, err := m.mapRecord(r)
		if err != nil {
			wantErr = err
			break
		}
	}
	if wantErr == nil || !errors.Is(wantErr, ErrLimitExceeded) {
		t.Fatalf("text limit error = %v", wantErr)
	}

	limits = defaultResponseTestLimits()
	limits.MaxReasoningBytes = 4
	m = newStreamMapper("my-or", limits)
	records = []sseRecord{
		created(`{"type":"response.created","sequence_number":0}`),
		rec("response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"item":{"id":"r","type":"reasoning","status":"in_progress"}}`),
		rec("response.reasoning_text.delta", `{"type":"response.reasoning_text.delta","sequence_number":2,"delta":"think"}`),
	}
	wantErr = nil
	for _, r := range records {
		_, err := m.mapRecord(r)
		if err != nil {
			wantErr = err
			break
		}
	}
	if wantErr == nil || !errors.Is(wantErr, ErrLimitExceeded) {
		t.Fatalf("reasoning limit error = %v", wantErr)
	}
}

func TestStreamMapper_ItemReferenceIsPrivateEvidence(t *testing.T) {
	t.Parallel()
	m := newStreamMapper("my-or", defaultResponseTestLimits())
	records := []sseRecord{
		created(`{"type":"response.created","sequence_number":0}`),
		rec("response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"item":{"id":"resp_prev","type":"item_reference","status":"completed"}}`),
		completed(),
	}
	var events []aipapi.Event
	for _, r := range records {
		evs, err := m.mapRecord(r)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, evs...)
	}
	for _, ev := range events {
		if strings.Contains(string(ev.Kind), "resp_prev") {
			t.Fatalf("native reference leaked: %+v", events)
		}
	}
	if len(m.Native().ItemIDs) != 1 || m.Native().ItemIDs[0] != "resp_prev" {
		t.Fatalf("native item ids = %v", m.Native().ItemIDs)
	}
}

func kindsOf(events []aipapi.Event) []aipapi.EventKind {
	out := make([]aipapi.EventKind, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Kind)
	}
	return out
}

func assertKinds(t *testing.T, got, want []aipapi.EventKind) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("kinds = %v, want %v", got, want)
		}
	}
}
