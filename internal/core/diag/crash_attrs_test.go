package diag_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/safety"
)

func TestIsolatedCrashAttrs_includesBoundaryAndNoStack(t *testing.T) {
	t.Parallel()
	ctx := diag.WithCallDiag(context.Background(), "tr-aa", "a-leg-bb")
	pe := triggerPanicForCapture(t, safety.BoundaryHTTP, "GetRoute")
	stack := pe.Stack()
	if len(stack) == 0 {
		t.Fatal("need stack for test")
	}
	attrs := diag.IsolatedCrashAttrs(ctx, pe, diag.CrashAttrOpts{
		AttrOpts: diag.AttrOpts{
			CallID: "c-1",
			BLegID: "b-9",
		},
		AttemptSeq: 2,
	})
	// No stack in safe fields.
	if string(stack) == "" {
		t.Fatal("empty stack")
	}
	for _, a := range attrs {
		if a.Key == "panic_stack" {
			t.Fatalf("safe attrs must not include panic_stack, got %v", attrs)
		}
		if a.Value.Kind() == slog.KindString {
			if s := a.Value.String(); s == string(stack) {
				t.Fatalf("attr %s leaked full stack", a.Key)
			}
		}
	}
	buf := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelError}))
	attrs2 := diag.AppendIsolatedCrashStack(attrs, pe)
	log.LogAttrs(context.Background(), slog.LevelError, "isolated_panic", attrs2...)
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["panic_stack"] == nil {
		t.Fatal("expected panic_stack in extended attrs")
	}
	if m["trace_id"] != "tr-aa" || m["a_leg_id"] != "a-leg-bb" {
		t.Fatalf("correlation: %#v", m)
	}
	if m["b_leg_id"] != "b-9" || m["call_id"] != "c-1" {
		t.Fatalf("opts: %#v", m)
	}
	if m["attempt_seq"] != float64(2) { // JSON number
		t.Fatalf("attempt_seq: %#v", m["attempt_seq"])
	}
	if m["panic_boundary"] != "http_request" {
		t.Fatalf("panic_boundary: %#v", m["panic_boundary"])
	}
	if m["operation"] != "GetRoute" {
		t.Fatalf("operation: %#v", m["operation"])
	}
	// stack must not appear in safe IsolatedCrashAttrs when logging only attrs (first path).
	var bufSafe bytes.Buffer
	log2 := slog.New(slog.NewJSONHandler(&bufSafe, &slog.HandlerOptions{Level: slog.LevelError}))
	log2.LogAttrs(context.Background(), slog.LevelError, "safe", attrs...)
	var mSafe map[string]any
	if err := json.Unmarshal(bufSafe.Bytes(), &mSafe); err != nil {
		t.Fatal(err)
	}
	if mSafe["panic_stack"] != nil {
		t.Fatal("safe log must not include panic_stack")
	}
}

func triggerPanicForCapture(t *testing.T, b safety.Boundary, op string) *safety.PanicError {
	t.Helper()
	var pe *safety.PanicError
	func() {
		defer func() {
			if r := recover(); r != nil {
				pe = safety.Capture(b, op, r)
			}
		}()
		panic("boom")
	}()
	if pe == nil {
		t.Fatal("no panic")
	}
	return pe
}

func TestIsolatedCrashAttrs_nilPanic_stillEmitsLineage(t *testing.T) {
	t.Parallel()
	ctx := diag.WithTraceID(context.Background(), "t1")
	attrs := diag.IsolatedCrashAttrs(ctx, nil, diag.CrashAttrOpts{})
	if len(attrs) != 1 {
		t.Fatalf("got %d attrs", len(attrs))
	}
}
