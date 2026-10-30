package openresponses

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestSSE_FramingPrimitives(t *testing.T) {
	evt := StreamEvent{
		Type:           "response.created",
		SequenceNumber: 0,
		Response: &WireResponseResource{
			ID:     "resp_1",
			Object: "response",
			Status: "in_progress",
		},
	}

	formatted, err := FormatSSEEvent(evt)
	if err != nil {
		t.Fatalf("FormatSSEEvent failed: %v", err)
	}

	str := string(formatted)
	if !strings.HasPrefix(str, "event: response.created\n") {
		t.Fatalf("expected event header 'event: response.created\\n', got %q", str)
	}
	if !strings.Contains(str, "data: {") {
		t.Fatalf("expected data line with JSON, got %q", str)
	}
	if !strings.HasSuffix(str, "\n\n") {
		t.Fatalf("expected \\n\\n suffix, got %q", str)
	}

	// Verify valid JSON in data payload
	lines := strings.Split(str, "\n")
	var dataLine string
	for _, l := range lines {
		if after, ok := strings.CutPrefix(l, "data: "); ok {
			dataLine = after
			break
		}
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(dataLine), &parsed); err != nil {
		t.Fatalf("failed to parse data JSON: %v", err)
	}
	if parsed["type"] != "response.created" {
		t.Fatalf("expected data type 'response.created', got %v", parsed["type"])
	}
	if float64(0) != parsed["sequence_number"] {
		t.Fatalf("expected sequence_number 0, got %v", parsed["sequence_number"])
	}

	// DONE framing
	doneBytes := FormattedDONE()
	if string(doneBytes) != "data: [DONE]\n\n" {
		t.Fatalf("expected 'data: [DONE]\\n\\n', got %q", string(doneBytes))
	}
}

func TestSSE_FramedWriter(t *testing.T) {
	buf := new(bytes.Buffer)
	writer := NewSSEWriter(buf)

	err := writer.WriteEvent(StreamEvent{
		Type:           "response.created",
		SequenceNumber: 0,
	})
	if err != nil {
		t.Fatalf("WriteEvent failed: %v", err)
	}

	err = writer.WriteEvent(StreamEvent{
		Type:           "response.completed",
		SequenceNumber: 1,
	})
	if err != nil {
		t.Fatalf("WriteEvent completed failed: %v", err)
	}

	err = writer.WriteDONE()
	if err != nil {
		t.Fatalf("WriteDONE failed: %v", err)
	}

	// Writing after DONE / terminal must fail
	err = writer.WriteEvent(StreamEvent{
		Type:           "response.output_text.delta",
		SequenceNumber: 2,
	})
	if err == nil {
		t.Fatalf("expected error writing event after terminal/DONE")
	}

	out := buf.String()
	if !strings.Contains(out, "event: response.created") {
		t.Fatalf("missing response.created in output")
	}
	if !strings.Contains(out, "event: response.completed") {
		t.Fatalf("missing response.completed in output")
	}
	if !strings.HasSuffix(out, "data: [DONE]\n\n") {
		t.Fatalf("expected output to end with data: [DONE]\\n\\n, got %q", out)
	}
}

func TestSSE_InjectionSafety(t *testing.T) {
	badEvts := []StreamEvent{
		{Type: "response.created\nheader: injected"},
		{Type: "response.created\r\nheader: injected"},
		{Type: ""},
	}
	for _, evt := range badEvts {
		_, err := FormatSSEEvent(evt)
		if err == nil {
			t.Fatalf("expected error for unsafe/empty event type %q, got nil", evt.Type)
		}
	}
}

func TestSSE_WriteDONEBeforeTerminal(t *testing.T) {
	buf := new(bytes.Buffer)
	writer := NewSSEWriter(buf)

	// WriteDONE before terminal event must fail
	err := writer.WriteDONE()
	if err == nil {
		t.Fatalf("expected error writing DONE before terminal response event")
	}

	// Duplicate successful DONE must still fail
	buf2 := new(bytes.Buffer)
	w2 := NewSSEWriter(buf2)
	_ = w2.WriteEvent(StreamEvent{Type: "response.completed"})
	if err := w2.WriteDONE(); err != nil {
		t.Fatalf("unexpected error on first WriteDONE: %v", err)
	}
	if err := w2.WriteDONE(); err == nil {
		t.Fatalf("expected error on duplicate WriteDONE, got nil")
	}
}

func TestSSE_WriteDONEAfterFailedTerminal(t *testing.T) {
	buf := new(bytes.Buffer)
	w := NewSSEWriter(buf)

	if err := w.WriteEvent(StreamEvent{Type: "response.created"}); err != nil {
		t.Fatalf("unexpected pre-terminal error: %v", err)
	}
	if err := w.WriteEvent(StreamEvent{Type: "response.failed"}); err != nil {
		t.Fatalf("unexpected failed terminal error: %v", err)
	}
	// Output after the failed terminal is rejected before DONE is written.
	if err := w.WriteEvent(StreamEvent{Type: "response.output_text.delta"}); err == nil {
		t.Fatal("expected output_after_terminal error after response.failed")
	}
	if err := w.WriteDONE(); err != nil {
		t.Fatalf("failed terminal must still emit DONE: %v", err)
	}

	out := buf.String()
	if got := bytes.Count([]byte(out), []byte("data: [DONE]")); got != 1 {
		t.Fatalf("expected exactly one DONE sentinel after response.failed, got %d: %q", got, out)
	}
	if !strings.HasSuffix(out, "data: [DONE]\n\n") {
		t.Fatalf("expected output to end with data: [DONE]\\n\\n, got %q", out)
	}

	// Duplicate DONE after a failed terminal must error like any other duplicate.
	if err := w.WriteDONE(); !errors.Is(err, ErrDuplicateTerminal) {
		t.Fatalf("expected ErrDuplicateTerminal on duplicate DONE after failed, got %v", err)
	}
}

func TestSSEFrame_PayloadLinesCarryDataPrefix(t *testing.T) {
	payloads := []string{
		`{"type":"response.delta"}`,
		"{\n\"x\": 1\n}",
		"{\r\n\"x\": 1\r\n}",
		"{\r\"x\": 1\r}",
		"{\"trailing\": true}\n",
		"line1\nline2\nline3",
	}
	for _, payload := range payloads {
		framed := frameSSEEvent("response.output_text.delta", []byte(payload))
		str := string(framed)
		if !strings.HasPrefix(str, "event: response.output_text.delta\n") {
			t.Fatalf("missing event header for payload %q: %q", payload, str)
		}
		if !strings.HasSuffix(str, "\n\n") {
			t.Fatalf("missing blank-line terminator for payload %q: %q", payload, str)
		}
		lines := strings.Split(strings.TrimSuffix(str, "\n"), "\n")
		if last := lines[len(lines)-1]; last != "" {
			t.Fatalf("expected blank terminator line for payload %q, got %q", payload, last)
		}
		lines = lines[:len(lines)-1]
		var data []string
		for i, line := range lines[1:] {
			rest, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				t.Fatalf("line %d missing data prefix for payload %q: %q", i, payload, line)
			}
			data = append(data, rest)
		}
		want := strings.ReplaceAll(payload, "\r\n", "\n")
		want = strings.ReplaceAll(want, "\r", "\n")
		if got := strings.Join(data, "\n"); got != want {
			t.Fatalf("data lines do not rejoin to payload for %q:\n got %q\nwant %q", payload, got, want)
		}
	}
}

func TestSSE_MarshalError(t *testing.T) {
	evt := StreamEvent{Type: "response.delta", Opaque: json.RawMessage("{invalid}")}
	if _, err := FormatSSEEvent(evt); !errors.Is(err, ErrEncodeFailed) {
		t.Fatalf("expected ErrEncodeFailed for unmarshable payload, got %v", err)
	}
}

func TestSSE_StreamErrorToRawMessage(t *testing.T) {
	if res := StreamErrorToRawMessage(nil); string(res) != "null" {
		t.Fatalf("expected 'null' for nil StreamError, got %s", string(res))
	}

	se := &aipapi.StreamError{
		Code:    "test_code",
		Message: "test message",
	}
	res := StreamErrorToRawMessage(se)
	str := string(res)
	if !strings.Contains(str, `"code":"test_code"`) || !strings.Contains(str, `"message":"an internal system error occurred"`) {
		t.Fatalf("unexpected sanitized raw message JSON: %s", str)
	}
}
