package openresponses

import (
	"errors"
	"net/http"
	"testing"
)

// seamTestResponseWriter is a controllable ResponseWriter that distinguishes
// Header/WriteHeader/Write behavior so the SSE response-writer seam can be
// verified independently of the full streaming handler.
type seamTestResponseWriter struct {
	header     http.Header
	status     int
	writes     int
	writeErr   error
	partialLen int
	wroteBody  []byte
	flushes    int
}

func newSeamTestResponseWriter() *seamTestResponseWriter {
	return &seamTestResponseWriter{header: make(http.Header)}
}

func (w *seamTestResponseWriter) Header() http.Header { return w.header }

func (w *seamTestResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *seamTestResponseWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.partialLen > 0 {
		// Accept some bytes (committing the response) then fail, the way a
		// net/http connection failure can surface after body bytes were sent.
		n := min(w.partialLen, len(p))
		if w.status == 0 {
			w.status = http.StatusOK
		}
		w.wroteBody = append(w.wroteBody, p[:n]...)
		return n, errors.New("seamTestResponseWriter: partial write failed")
	}
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.wroteBody = append(w.wroteBody, p...)
	return len(p), nil
}

func (w *seamTestResponseWriter) Flush() { w.flushes++ }

func TestSSEResponseWriterSuccessCommitsHeadersOnce(t *testing.T) {
	inner := newSeamTestResponseWriter()
	seam := &sseResponseWriter{w: inner}

	if _, err := seam.Write([]byte("event: response.created\n\ndata: x\n\n")); err != nil {
		t.Fatalf("unexpected first write error: %v", err)
	}
	if !seam.committed {
		t.Fatal("seam must be committed after a successful first write")
	}
	if inner.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", inner.status)
	}
	if ct := inner.header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	for _, k := range []string{"Cache-Control", "Connection"} {
		if inner.header.Get(k) == "" {
			t.Fatalf("missing SSE header %q", k)
		}
	}

	if _, err := seam.Write([]byte("event: response.output_text.delta\n\ndata: y\n\n")); err != nil {
		t.Fatalf("unexpected second write error: %v", err)
	}
	if inner.writes != 2 {
		t.Fatalf("writes = %d, want 2", inner.writes)
	}
	if inner.header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("second write must not re-commit SSE headers, content-type=%q", inner.header.Get("Content-Type"))
	}
}

func TestSSEResponseWriterWriteFailureBeforeCommitLeavesSeamUncommitted(t *testing.T) {
	inner := newSeamTestResponseWriter()
	inner.writeErr = errors.New("connection reset")
	seam := &sseResponseWriter{w: inner}

	if _, err := seam.Write([]byte("event: response.created\n\ndata: x\n\n")); err == nil {
		t.Fatal("expected write failure")
	}
	if seam.committed {
		t.Fatal("a write failure with zero bytes accepted must leave the seam uncommitted so a pre-commit JSON error is still possible")
	}
}

func TestSSEResponseWriterPartialWriteFailureIsCommitted(t *testing.T) {
	inner := newSeamTestResponseWriter()
	inner.partialLen = 7
	seam := &sseResponseWriter{w: inner}

	n, err := seam.Write([]byte("event: response.created\n\ndata: x\n\n"))
	if err == nil {
		t.Fatal("expected partial write failure")
	}
	if n != 7 {
		t.Fatalf("n = %d, want 7", n)
	}
	if !seam.committed {
		t.Fatal("a write that accepted bytes before failing must be treated as committed")
	}
	if inner.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", inner.status)
	}
}
