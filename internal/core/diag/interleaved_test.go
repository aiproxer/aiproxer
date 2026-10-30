package diag_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/diag"
)

func TestInterleavedTransitionAttrs_boundedKeys(t *testing.T) {
	t.Parallel()
	attrs := diag.InterleavedTransitionAttrs(diag.InterleavedTransition{
		Phase:             "executor",
		Role:              "thinker",
		MemoPresent:       true,
		MemoVisible:       false,
		MemoInjected:      true,
		MemoExpired:       false,
		ThinkerSuppressed: true,
		SkipReason:        "visible",
		ExtractionSource:  "block",
		StreamInterrupted: true,
	})
	allowed := map[string]struct{}{
		"interleaved_phase": {}, "interleaved_role": {}, "memo_present": {}, "memo_visible": {},
		"memo_injected": {}, "memo_expired": {}, "thinker_suppressed": {}, "memo_skip_reason": {},
		"memo_extraction_source": {}, "memo_stream_interrupted": {},
	}
	for _, a := range attrs {
		if _, ok := allowed[a.Key]; !ok {
			t.Fatalf("unexpected attr key %q", a.Key)
		}
	}
}

func TestLogInterleavedTransition_excludesMemoBody(t *testing.T) {
	t.Parallel()
	secret := "TOP_SECRET_MEMO_BODY_DO_NOT_LOG"
	ctx := diag.WithTraceID(context.Background(), "tr-diag")
	ctx = diag.WithALeg(ctx, "a-diag")
	buf := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	diag.LogInterleavedTransition(
		ctx, log, "interleaved_memo_captured", diag.AttrOpts{CallID: "call-1"},
		diag.InterleavedTransition{
			Phase:             "thinker",
			Role:              "thinker",
			MemoPresent:       true,
			ExtractionSource:  "block",
			StreamInterrupted: false,
		},
	)
	out := buf.String()
	if strings.Contains(out, secret) {
		t.Fatalf("log must not contain memo body, got %q", out)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["msg"] != "interleaved_memo_captured" {
		t.Fatalf("msg: %#v", m["msg"])
	}
	if m["interleaved_phase"] != "thinker" || m["interleaved_role"] != "thinker" {
		t.Fatalf("phase/role: %#v", m)
	}
	if m["memo_present"] != true {
		t.Fatalf("memo_present: %#v", m["memo_present"])
	}
}
