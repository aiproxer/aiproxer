package frontendpipe_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// callIDLogHandler captures the log records the shared pipeline emits.
type callIDLogHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *callIDLogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *callIDLogHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *callIDLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *callIDLogHandler) WithGroup(string) slog.Handler      { return h }

// errorRecord returns the first captured error-level record with the message.
func (h *callIDLogHandler) errorRecord(message string) (slog.Record, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Level == slog.LevelError && r.Message == message {
			return r, true
		}
	}
	return slog.Record{}, false
}

// TestServeHTTP_StreamEncodeFailureIsLoggedWithCallID proves a streaming
// encoder failure reaches the operator's log with the call identity. A
// frontend mounted without a logger dropped that record entirely, so the
// failure left no trace (#1317).
func TestServeHTTP_StreamEncodeFailureIsLoggedWithCallID(t *testing.T) {
	t.Parallel()
	handler := &callIDLogHandler{}
	spec := testSpec(&hookExec{}, &hookWire{})
	spec.Log = slog.New(handler)
	spec.Decode = func(frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
		call := validCall()
		call.ID = "call-1317"
		return &frontendpipe.Decoded{Call: call, Stream: true}, nil
	}
	spec.WriteStream = func(context.Context, http.ResponseWriter, *aipapi.Call, aipapi.EventStream, struct{}) error {
		return errors.New("stream encoder failed")
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/x", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	record, ok := handler.errorRecord("stream encode failed")
	if !ok {
		t.Fatal("the streaming encoder failure was not logged")
	}
	var callID string
	record.Attrs(func(a slog.Attr) bool {
		if a.Key == "call_id" {
			callID = a.Value.String()
		}
		return true
	})
	if callID != "call-1317" {
		t.Fatalf("call_id = %q, want %q", callID, "call-1317")
	}
}
