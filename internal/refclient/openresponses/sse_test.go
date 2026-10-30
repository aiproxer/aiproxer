package openresponses

import (
	"os"
	"strings"
	"testing"
)

// TestSSE_StreamFixture_Parses consumes the pinned SSE stream fixture and asserts
// lifecycle ordering, delta accumulation, terminal ownership, and [DONE].
func TestSSE_StreamFixture_Parses(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("testdata/scenarios/stream_text.sse")
	if err != nil {
		t.Fatal(err)
	}
	stream, done, err := ParseSSE(b, DefaultParseOptions())
	if err != nil {
		t.Fatalf("ParseSSE: %v", err)
	}
	if !done {
		t.Fatal("expected [DONE] terminal")
	}
	if len(stream) != 9 {
		t.Fatalf("expected 9 events, got %d", len(stream))
	}

	types := make([]string, 0, len(stream))
	for _, e := range stream {
		types = append(types, e.Type)
	}
	want := []string{
		"response.created",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("event %d: got %q want %q (full: %v)", i, types[i], want[i], types)
		}
	}

	if stream[0].Response == nil || stream[0].Response.ID != "resp_stream_1" {
		t.Fatalf("created response: %+v", stream[0].Response)
	}
	// Delta accumulation across two deltas.
	if stream[3].Delta != "Hello" || stream[4].Delta != " world" {
		t.Fatalf("deltas: %q %q", stream[3].Delta, stream[4].Delta)
	}
	if stream[5].Text != "Hello world" {
		t.Fatalf("done text: %q", stream[5].Text)
	}
	if stream[7].Item == nil || stream[7].Item.Status != "completed" {
		t.Fatalf("item done: %+v", stream[7].Item)
	}
	// Sequence numbers are monotonically increasing.
	for i := 1; i < len(stream); i++ {
		if stream[i].SequenceNumber <= stream[i-1].SequenceNumber {
			t.Fatalf("sequence not increasing at %d", i)
		}
	}
	// Exactly one terminal event at the end.
	for i, e := range stream {
		if e.IsTerminal() && i != len(stream)-1 {
			t.Fatalf("terminal event %q not last", e.Type)
		}
	}
	if !stream[len(stream)-1].IsTerminal() {
		t.Fatal("last event must be terminal")
	}
	if stream[len(stream)-1].Response == nil || stream[len(stream)-1].Response.Status != "completed" {
		t.Fatalf("completed response: %+v", stream[len(stream)-1].Response)
	}
}

// TestSSE_MissingDONE rejects a stream that never emits [DONE].
func TestSSE_MissingDONE(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("testdata/scenarios/stream_text.sse")
	if err != nil {
		t.Fatal(err)
	}
	withoutDone := strings.TrimSuffix(strings.TrimSpace(string(b)), "data: [DONE]")
	if _, done, err := ParseSSE([]byte(withoutDone), DefaultParseOptions()); err == nil || done {
		t.Fatalf("expected error for missing [DONE], got done=%v err=%v", done, err)
	}
}

// TestSSE_MalformedData ensures malformed event payloads are rejected without panic.
func TestSSE_MalformedData(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		data string
	}{
		{"event_mismatch", "event: response.created\ndata: {\"type\":\"response.completed\"}\n\n"},
		{"bad_json", "event: response.created\ndata: {not json}\n\n"},
		{"empty_data", "event: response.created\ndata: \n\n"},
		{"done_early", "data: [DONE]\n\n"},
		{"no_terminal", "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0}\n\n"},
		{"missing_event_field", "data: {\"type\":\"response.created\"}\n\n"},
		{"terminal_duplicate", "data: {\"type\":\"response.completed\"}\ndata: {\"type\":\"response.completed\"}\ndata: [DONE]\n\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := ParseSSE([]byte(tc.data), DefaultParseOptions()); err == nil {
				t.Fatalf("expected error for %q", tc.name)
			}
		})
	}
}

// TestSSE_EventFieldMismatch asserts the SSE event: header must match the data type.
func TestSSE_EventFieldMismatch(t *testing.T) {
	t.Parallel()
	// Missing event header entirely is tolerated by lenient readers but we
	// require the event header to match the payload type when present.
	bad := "event: response.completed\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":2}\n\n"
	if _, _, err := ParseSSE([]byte(bad), DefaultParseOptions()); err == nil {
		t.Fatal("expected event/data mismatch error")
	}
}

// TestSSE_EventParseFromWS asserts a single event JSON (WebSocket frame payload)
// parses identically to the SSE data payload parser.
func TestSSE_EventParseFromWS(t *testing.T) {
	t.Parallel()
	evt, err := ParseEvent([]byte(`{"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_stream_1","output_index":0,"content_index":0,"delta":"Hello"}`), DefaultParseOptions())
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if evt.Type != "response.output_text.delta" || evt.Delta != "Hello" || evt.ItemID != "msg_stream_1" {
		t.Fatalf("event: %+v", evt)
	}
}

func TestSSE_ReaderBackpressureAndBoundedLines(t *testing.T) {
	t.Parallel()
	// A single oversized line must be rejected by the bounded line reader.
	long := "event: response.created\ndata: {\"type\":\"response.created\",\"x\":\"" + strings.Repeat("a", 4096) + "\"}\n\n"
	opts := DefaultParseOptions()
	opts.MaxEventBytes = 512
	if _, _, err := ParseSSE([]byte(long), opts); err == nil {
		t.Fatal("expected bounded-read error for oversized event")
	}
}

// sseScenarioCases registers the SSE scenario cases.
func sseScenarioCases() []scenarioCase {
	return []scenarioCase{
		{
			id:          "scenario-sse-text",
			kind:        ScenarioSSEText,
			fixture:     "stream_text.sse",
			description: "Pinned SSE stream parses in order with lifecycle, deltas, single terminal, and [DONE].",
			parse: func(t *testing.T, data []byte) {
				t.Helper()
				stream, done, err := ParseSSE(data, DefaultParseOptions())
				if err != nil {
					t.Fatalf("ParseSSE: %v", err)
				}
				if !done {
					t.Fatal("expected [DONE]")
				}
				if len(stream) != 9 {
					t.Fatalf("event count: %d", len(stream))
				}
			},
		},
	}
}
