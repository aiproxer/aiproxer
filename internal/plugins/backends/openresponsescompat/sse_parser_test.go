package openresponsescompat

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

func sseEvent(eventType, data string) string {
	return "event: " + eventType + "\ndata: " + data + "\n\n"
}

func TestSSEParser_SequentialRecords(t *testing.T) {
	t.Parallel()
	input := sseEvent("response.created", `{"type":"response.created","sequence_number":0}`) +
		sseEvent("response.completed", `{"type":"response.completed"}`) +
		"data: [DONE]\n\n"
	br := bufio.NewReader(strings.NewReader(input))

	rec, err := nextSSERecord(br, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if rec.eventType != "response.created" || !strings.Contains(string(rec.data), `"response.created"`) {
		t.Fatalf("record = %+v", rec)
	}

	rec, err = nextSSERecord(br, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if rec.eventType != "response.completed" {
		t.Fatalf("event type = %q", rec.eventType)
	}

	rec, err = nextSSERecord(br, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if rec.eventType != "" || string(rec.data) != "[DONE]" {
		t.Fatalf("DONE record = %+v", rec)
	}

	if _, err := nextSSERecord(br, 4096); !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF after final record, got %v", err)
	}
}

func TestSSEParser_MultiLineDataJoinedWithNewline(t *testing.T) {
	t.Parallel()
	input := "event: response.created\n" +
		"data: {\"type\":\"response.created\",\n" +
		"data: \"sequence_number\":0}\n\n"
	br := bufio.NewReader(strings.NewReader(input))
	rec, err := nextSSERecord(br, 4096)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"response.created",
"sequence_number":0}`
	if string(rec.data) != want {
		t.Fatalf("data = %q, want %q", string(rec.data), want)
	}
}

func TestSSEParser_CommentsAndIgnoredFields(t *testing.T) {
	t.Parallel()
	input := ": keepalive comment\n" +
		"event: response.created\n" +
		"id: 7\n" +
		"retry: 3000\n" +
		"data: {\"type\":\"response.created\"}\n\n" +
		"\n" +
		"data: [DONE]\n\n"
	br := bufio.NewReader(strings.NewReader(input))
	rec, err := nextSSERecord(br, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if rec.eventType != "response.created" {
		t.Fatalf("event type = %q", rec.eventType)
	}
	rec, err = nextSSERecord(br, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if string(rec.data) != "[DONE]" {
		t.Fatalf("data = %q", string(rec.data))
	}
}

func TestSSEParser_CRLFLineEndings(t *testing.T) {
	t.Parallel()
	input := "event: response.created\r\ndata: {\"type\":\"response.created\"}\r\n\r\n"
	br := bufio.NewReader(strings.NewReader(input))
	rec, err := nextSSERecord(br, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if rec.eventType != "response.created" || !strings.Contains(string(rec.data), "response.created") {
		t.Fatalf("record = %+v", rec)
	}
}

func TestSSEParser_FieldValueStripsSingleLeadingSpace(t *testing.T) {
	t.Parallel()
	if got := sseFieldValue(" value"); got != "value" {
		t.Fatalf("value = %q", got)
	}
	if got := sseFieldValue("  double"); got != " double" {
		t.Fatalf("double space must keep second space, got %q", got)
	}
	if got := sseFieldValue(""); got != "" {
		t.Fatalf("empty value = %q", got)
	}
}

func TestSSEParser_TrailingRecordAtEOFIsFlushed(t *testing.T) {
	t.Parallel()
	input := "event: response.created\ndata: {\"type\":\"response.created\"}"
	br := bufio.NewReader(strings.NewReader(input))
	rec, err := nextSSERecord(br, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if rec.eventType != "response.created" || !strings.Contains(string(rec.data), "response.created") {
		t.Fatalf("record = %+v", rec)
	}
	if _, err := nextSSERecord(br, 4096); !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

func TestSSEParser_DataPayloadBoundEnforced(t *testing.T) {
	t.Parallel()
	input := sseEvent("response.created", `{"type":"response.created","sequence_number":0,"delta":"aaaaaaaa"}`)
	br := bufio.NewReader(strings.NewReader(input))
	if _, err := nextSSERecord(br, 8); err == nil {
		t.Fatal("expected payload bound rejection")
	}
}

func TestSSEParser_SingleLineExactMaxBytesAccepted(t *testing.T) {
	t.Parallel()
	// A single data line whose full content ("data: " + value) is exactly
	// maxBytes must be accepted: the trailing newline and the first-line data
	// separator must not push the payload over the bound (no off-by-one).
	const maxBuf = 32
	payload := strings.Repeat("x", maxBuf-len("data: "))
	br := bufio.NewReader(strings.NewReader("data: " + payload + "\n\n"))
	rec, err := nextSSERecord(br, maxBuf)
	if err != nil {
		t.Fatal(err)
	}
	if string(rec.data) != payload {
		t.Fatalf("data = %q, want %q", string(rec.data), payload)
	}
}

func TestSSEParser_SingleLineOverMaxBytesRejected(t *testing.T) {
	t.Parallel()
	// One byte over the exact single-line bound must be rejected.
	const maxBuf = 32
	br := bufio.NewReader(strings.NewReader("data: " + strings.Repeat("x", maxBuf-len("data: ")+1) + "\n\n"))
	if _, err := nextSSERecord(br, maxBuf); err == nil {
		t.Fatal("expected line bound rejection for maxBytes+1")
	}
}

func TestSSEParser_MultiLineExactMaxBytesIncludingNewlineAccepted(t *testing.T) {
	t.Parallel()
	// Two data lines joined with a single '\n': 15 + 1 + 16 = 32 == maxBytes.
	first := strings.Repeat("a", 15)
	second := strings.Repeat("b", 16)
	input := "data: " + first + "\ndata: " + second + "\n\n"
	br := bufio.NewReader(strings.NewReader(input))
	rec, err := nextSSERecord(br, 32)
	if err != nil {
		t.Fatal(err)
	}
	if string(rec.data) != first+"\n"+second {
		t.Fatalf("data = %q", string(rec.data))
	}
}

func TestSSEParser_MultiLineOverMaxBytesRejected(t *testing.T) {
	t.Parallel()
	// 16 + 1 + 16 = 33 > 32; the newline separator counts once joined.
	line := strings.Repeat("a", 16)
	input := "data: " + line + "\ndata: " + line + "\n\n"
	br := bufio.NewReader(strings.NewReader(input))
	if _, err := nextSSERecord(br, 32); err == nil {
		t.Fatal("expected payload bound rejection for multi-line overflow")
	}
}

func TestSSEParser_LineBoundEnforced(t *testing.T) {
	t.Parallel()
	input := "event: " + strings.Repeat("x", 32) + "\ndata: {}\n\n"
	br := bufio.NewReader(strings.NewReader(input))
	if _, err := nextSSERecord(br, 16); err == nil {
		t.Fatal("expected line bound rejection")
	}
}

func TestSSEParser_EmptyStreamIsEOF(t *testing.T) {
	t.Parallel()
	br := bufio.NewReader(strings.NewReader(""))
	if _, err := nextSSERecord(br, 4096); !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

func TestSSEParser_ConsecutiveBlankLinesSkipped(t *testing.T) {
	t.Parallel()
	input := "\n\n" + sseEvent("response.created", `{"type":"response.created"}`)
	br := bufio.NewReader(strings.NewReader(input))
	rec, err := nextSSERecord(br, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if rec.eventType != "response.created" {
		t.Fatalf("event type = %q", rec.eventType)
	}
}

func TestSSEParser_ReadErrorPropagated(t *testing.T) {
	t.Parallel()
	br := bufio.NewReader(errReader{err: io.ErrUnexpectedEOF})
	if _, err := nextSSERecord(br, 4096); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected propagated read error, got %v", err)
	}
}

type errReader struct {
	err error
}

func (r errReader) Read([]byte) (int, error) { return 0, r.err }
