package diag_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/diag"
)

func TestAttrs_stableKeys(t *testing.T) {
	t.Parallel()
	ctx := diag.WithTraceID(context.Background(), "tr-9")
	ctx = diag.WithALeg(ctx, "a-leg-2")
	attrs := diag.Attrs(ctx, diag.AttrOpts{CallID: "call-x", BLegID: "b-leg-3"})
	buf := &bytes.Buffer{}
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	log := slog.New(h)
	log.LogAttrs(context.Background(), slog.LevelInfo, "probe", attrs...)
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["trace_id"] != "tr-9" || m["a_leg_id"] != "a-leg-2" || m["b_leg_id"] != "b-leg-3" || m["call_id"] != "call-x" {
		t.Fatalf("attrs map: %#v", m)
	}
}

func TestLogDecision_includesDecisionField(t *testing.T) {
	t.Parallel()
	ctx := diag.WithTraceID(context.Background(), "t-1")
	ctx = diag.WithALeg(ctx, "a-1")
	buf := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	diag.LogDecision(ctx, log, "capability_reject", diag.AttrOpts{}, slog.String("decision", "exclude_candidate"))
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["msg"] != "capability_reject" {
		t.Fatalf("msg: %#v", m["msg"])
	}
	if m["decision"] != "exclude_candidate" {
		t.Fatalf("decision: %#v", m)
	}
	if m["trace_id"] != "t-1" {
		t.Fatalf("trace_id: %#v", m)
	}
}

func TestLogError_includesTraceAndError(t *testing.T) {
	t.Parallel()
	ctx := diag.WithTraceID(context.Background(), "t-err")
	buf := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelError}))
	errSample := errors.New("boom")
	diag.LogError(ctx, log, "execute failed", diag.AttrOpts{CallID: "c1"}, errSample)
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["trace_id"] != "t-err" {
		t.Fatalf("trace_id: %#v", m)
	}
	if m["call_id"] != "c1" {
		t.Fatalf("call_id: %#v", m)
	}
	if m["msg"] != "execute failed" {
		t.Fatalf("msg: %#v", m["msg"])
	}
}

func TestTruncErrDetail(t *testing.T) {
	t.Parallel()
	if got := diag.TruncErrDetail(errors.New("hi"), 10); got != "hi" {
		t.Fatalf("short: %q", got)
	}
	long := errors.New("abcdefghijklmnopqrstuvwxyz")
	if got := diag.TruncErrDetail(long, 5); got != "abcde" {
		t.Fatalf("trunc: %q", got)
	}
	if diag.TruncErrDetail(nil, 5) != "" {
		t.Fatal("nil")
	}
}
