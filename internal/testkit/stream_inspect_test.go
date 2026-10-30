package testkit

import (
	"net/http/httptest"
	"testing"
)

func TestParseSSEBody_eventAndData(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	_, _ = rec.WriteString("event: hello\ndata: {\"x\":1}\n\n")
	frames := ParseRecorderSSE(rec)
	if len(frames) != 1 || frames[0].Event != "hello" || frames[0].Data != `{"x":1}` {
		t.Fatalf("got %+v", frames)
	}
}

func TestParseSSEBody_dataOnly(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	_, _ = rec.WriteString("data: [DONE]\n\n")
	frames := ParseRecorderSSE(rec)
	if len(frames) != 1 || frames[0].Event != "" || frames[0].Data != "[DONE]" {
		t.Fatalf("got %+v", frames)
	}
}
