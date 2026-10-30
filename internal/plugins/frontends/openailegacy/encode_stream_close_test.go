package openailegacy_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestWriteStreamSSE_OwnsPrePumpStreamCleanup pins the canonical stream
// ownership contract: frontendpipe hands WriteStream an EventStream and never
// closes it, so every return before PumpSSE must close the stream. Once PumpSSE
// is called it owns the close, so all paths close exactly once.
func TestWriteStreamSSE_OwnsPrePumpStreamCleanup(t *testing.T) {
	t.Parallel()
	writeErr := errors.New("client disconnected")
	for _, tc := range []struct {
		name    string
		writer  func(*httptest.ResponseRecorder) http.ResponseWriter
		events  []aipapi.Event
		wantErr string
	}{
		{
			name: "missing flusher closes before pump",
			writer: func(rec *httptest.ResponseRecorder) http.ResponseWriter {
				return struct{ http.ResponseWriter }{rec}
			},
			wantErr: "not a Flusher",
		},
		{
			name: "initial chunk write failure closes before pump",
			writer: func(rec *httptest.ResponseRecorder) http.ResponseWriter {
				return &failFirstWriteWriter{ResponseRecorder: rec, err: writeErr}
			},
			wantErr: writeErr.Error(),
		},
		{
			name:   "pump path closes exactly once",
			writer: func(rec *httptest.ResponseRecorder) http.ResponseWriter { return rec },
			events: []aipapi.Event{{Kind: aipapi.EventResponseFinished}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			es := &closeCountingStream{EventStream: aipapi.NewFixedEventStream(tc.events)}
			rec := httptest.NewRecorder()
			err := openailegacy.WriteStreamSSE(context.Background(), tc.writer(rec), &aipapi.Call{}, es, openailegacy.EncodeOptions{CompletionID: "cc_close"})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("error = %v, want nil", err)
			}
			if got := es.closes.Load(); got != 1 {
				t.Fatalf("stream closes = %d, want 1", got)
			}
		})
	}
}

// TestWireWriteStreamSSE_RetainsCallerStreamCleanup pins the wire fast path:
// the replay caller (frontendpipe candidate) holds the raw stream and closes it
// on every return, so the wire encoder must not add a pre-pump close.
func TestWireWriteStreamSSE_RetainsCallerStreamCleanup(t *testing.T) {
	t.Parallel()
	es := &closeCountingStream{EventStream: aipapi.NewFixedEventStream(nil)}
	rec := httptest.NewRecorder()
	nonFlusher := struct{ http.ResponseWriter }{rec}
	err := openailegacy.WireWriteStreamSSE(context.Background(), nonFlusher, frontendpipe.ResponseContext{}, es, false)
	if err == nil || !strings.Contains(err.Error(), "not a Flusher") {
		t.Fatalf("error = %v, want containing %q", err, "not a Flusher")
	}
	if got := es.closes.Load(); got != 0 {
		t.Fatalf("wire stream closes = %d, want 0 (replay caller retains raw-stream cleanup)", got)
	}
}

type failFirstWriteWriter struct {
	*httptest.ResponseRecorder
	err    error
	failed bool
}

func (w *failFirstWriteWriter) Write(p []byte) (int, error) {
	if !w.failed {
		w.failed = true
		return 0, w.err
	}
	return w.ResponseRecorder.Write(p)
}

type closeCountingStream struct {
	aipapi.EventStream
	closes atomic.Int32
}

func (s *closeCountingStream) Close() error {
	s.closes.Add(1)
	return nil
}
