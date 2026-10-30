package anthropic_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestWriteStreamSSE_OwnsPrePumpStreamCleanup pins the canonical stream
// ownership contract: frontendpipe hands WriteStream an EventStream and never
// closes it, so every return before PumpSSE must close the stream. Once PumpSSE
// is called it owns the close, so all paths close exactly once.
func TestWriteStreamSSE_OwnsPrePumpStreamCleanup(t *testing.T) {
	t.Parallel()
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
			name:   "pump path closes exactly once",
			writer: func(rec *httptest.ResponseRecorder) http.ResponseWriter { return rec },
			events: []aipapi.Event{{Kind: aipapi.EventResponseFinished}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			es := &closeCountingStream{EventStream: aipapi.NewFixedEventStream(tc.events)}
			rec := httptest.NewRecorder()
			err := anthropic.WriteStreamSSE(context.Background(), tc.writer(rec), &aipapi.Call{}, es, anthropic.EncodeOptions{MessageID: "msg_close"})
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

type closeCountingStream struct {
	aipapi.EventStream
	closes atomic.Int32
}

func (s *closeCountingStream) Close() error {
	s.closes.Add(1)
	return nil
}
