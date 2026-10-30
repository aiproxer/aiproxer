package streamdebug

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestMain enables the debug gate for this package: diag.DebugTurnsEnabled reads
// AIP_CODEX_DEBUG_TURNS once per process, so it must be set before any test runs.
func TestMain(m *testing.M) {
	_ = os.Setenv("AIP_CODEX_DEBUG_TURNS", "1")
	os.Exit(m.Run())
}

type ctxCorrRecord struct {
	msg     string
	traceID string
	aLegID  string
	attrs   map[string]string
}

type ctxCorrHandler struct {
	mu      sync.Mutex
	records []ctxCorrRecord
}

func (h *ctxCorrHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *ctxCorrHandler) Handle(ctx context.Context, r slog.Record) error {
	rec := ctxCorrRecord{
		msg:     r.Message,
		traceID: diag.TraceID(ctx),
		aLegID:  diag.ALegID(ctx),
		attrs:   map[string]string{},
	}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, rec)
	h.mu.Unlock()
	return nil
}

func (h *ctxCorrHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *ctxCorrHandler) WithGroup(string) slog.Handler      { return h }

func (h *ctxCorrHandler) terminal() (ctxCorrRecord, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.msg == "aip.debug.stream_terminal" {
			return r, true
		}
	}
	return ctxCorrRecord{}, false
}

func TestClose_LogsTerminalWithCapturedCallDiag(t *testing.T) {
	t.Parallel()
	h := &ctxCorrHandler{}
	log := slog.New(h)
	call := &aipapi.Call{ID: "call-1"}
	call.Session.ALegID = "aleg-call"
	ctx := diag.EnsureCallDiag(context.Background(), "trace-wrap", "aleg-wrap")

	wrapped := Wrap(ctx, log, "test", call, &testStream{}, time.Now())
	if wrapped == nil {
		t.Fatal("Wrap returned nil stream with debug gate enabled")
	}
	if err := wrapped.Close(); err != nil {
		t.Fatal(err)
	}

	rec, ok := h.terminal()
	if !ok {
		t.Fatal("Close did not emit aip.debug.stream_terminal")
	}
	if rec.traceID != "trace-wrap" || rec.aLegID != "aleg-wrap" {
		t.Fatalf("terminal log ctx correlation = (%q, %q), want Wrap-time (trace-wrap, aleg-wrap)",
			rec.traceID, rec.aLegID)
	}
	// Attr keys must stay exactly as before.
	if rec.attrs["trace_id"] != diag.StableCallID(call) {
		t.Fatalf("trace_id attr = %q, want %q", rec.attrs["trace_id"], diag.StableCallID(call))
	}
	if rec.attrs["call_id"] != "call-1" {
		t.Fatalf("call_id attr = %q", rec.attrs["call_id"])
	}
	if rec.attrs["status"] != "closed" {
		t.Fatalf("status attr = %q", rec.attrs["status"])
	}
}

func TestClose_FallsBackToCallDerivedDiag(t *testing.T) {
	t.Parallel()
	h := &ctxCorrHandler{}
	log := slog.New(h)
	call := &aipapi.Call{ID: "call-2"}
	call.Session.ALegID = "  aleg-call  "

	wrapped := Wrap(context.Background(), log, "test", call, &testStream{}, time.Now())
	if err := wrapped.Close(); err != nil {
		t.Fatal(err)
	}

	rec, ok := h.terminal()
	if !ok {
		t.Fatal("Close did not emit aip.debug.stream_terminal")
	}
	if rec.traceID != diag.StableCallID(call) {
		t.Fatalf("fallback traceID = %q, want %q", rec.traceID, diag.StableCallID(call))
	}
	if rec.aLegID != strings.TrimSpace(call.Session.ALegID) {
		t.Fatalf("fallback aLegID = %q, want trimmed call A-leg", rec.aLegID)
	}
}
