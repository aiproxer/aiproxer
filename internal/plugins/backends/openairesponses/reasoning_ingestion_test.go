package openairesponses

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/protocols/openairesponsestream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3/responses"
)

func mustUnion(t *testing.T, raw string) responses.ResponseStreamEventUnion {
	t.Helper()
	var u responses.ResponseStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	return u
}

func drainSDK(s *sdkStream) []aipapi.Event {
	return stream.DrainPending(&s.pending)
}

func TestHandleUnion_reasoningOutputItemDone_exactPart(t *testing.T) {
	t.Parallel()
	s := &sdkStream{}
	u := mustUnion(t, `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"s"}],"encrypted_content":null,"status":"completed"}}`)
	if err := s.handleUnion(u); err != nil {
		t.Fatal(err)
	}
	var parts int
	for _, ev := range drainSDK(s) {
		if ev.Kind == aipapi.EventReasoningDelta {
			t.Fatal("no EventReasoningDelta")
		}
		if ev.Kind == aipapi.EventReasoningPart {
			parts++
			if ev.Reasoning == nil || ev.Reasoning.Dialect != aipapi.ReasoningDialectOpenAIResponsesItemV1 {
				t.Fatalf("bad part: %+v", ev.Reasoning)
			}
			if !strings.Contains(string(ev.Reasoning.Opaque), `"encrypted_content":null`) {
				t.Fatalf("opaque=%s", ev.Reasoning.Opaque)
			}
		}
	}
	if parts != 1 {
		t.Fatalf("parts=%d", parts)
	}
}

func TestHandleUnion_reasoningAssemblyAndCompletedFallback(t *testing.T) {
	t.Parallel()
	s := &sdkStream{}
	for _, raw := range []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}`,
		`{"type":"response.reasoning_summary_part.added","item_id":"rs_1","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"think"}`,
		`{"type":"response.reasoning_summary_text.done","item_id":"rs_1","output_index":0,"summary_index":0,"text":"think"}`,
		`{"type":"response.reasoning_summary_part.done","item_id":"rs_1","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":"think"}}`,
		`{"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":0,"delta":"c"}`,
		`{"type":"response.reasoning_text.done","item_id":"rs_1","output_index":0,"content_index":0,"text":"c"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"think"}],"content":[{"type":"reasoning_text","text":"c"}],"status":"completed"}}`,
	} {
		if err := s.handleUnion(mustUnion(t, raw)); err != nil {
			t.Fatalf("%s: %v", raw[:48], err)
		}
	}
	evs := drainSDK(s)
	for _, ev := range evs {
		if ev.Kind == aipapi.EventReasoningDelta {
			t.Fatal("terminal-only: no ReasoningDelta")
		}
	}
	var n int
	for _, ev := range evs {
		if ev.Kind == aipapi.EventReasoningPart {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want 1 exact part after done, got %d", n)
	}

	s2 := &sdkStream{}
	completed := mustUnion(t, `{"type":"response.completed","response":{"id":"r1","status":"completed","output":[{"type":"reasoning","id":"rs_x","summary":[],"status":"completed"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}}`)
	if err := s2.handleUnion(completed); err != nil {
		t.Fatal(err)
	}
	var texts, parts int
	for _, ev := range drainSDK(s2) {
		switch ev.Kind {
		case aipapi.EventTextDelta:
			texts++
		case aipapi.EventReasoningPart:
			parts++
		case aipapi.EventReasoningDelta:
			t.Fatal("no ReasoningDelta on completed-only")
		}
	}
	if parts != 1 || texts != 1 {
		t.Fatalf("completed-only parts=%d texts=%d", parts, texts)
	}
}

func TestHandleUnion_reasoningDedupeWithCompleted(t *testing.T) {
	t.Parallel()
	s := &sdkStream{}
	if err := s.handleUnion(mustUnion(t, `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[],"status":"completed"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.handleUnion(mustUnion(t, `{"type":"response.completed","response":{"id":"r1","status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[],"status":"completed"}]}}`)); err != nil {
		t.Fatal(err)
	}
	var n int
	for _, ev := range drainSDK(s) {
		if ev.Kind == aipapi.EventReasoningPart {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("dedupe want 1, got %d", n)
	}
}

func TestHandleUnion_reasoningConflictingDuplicate(t *testing.T) {
	t.Parallel()
	s := &sdkStream{}
	if err := s.handleUnion(mustUnion(t, `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"a"}],"status":"completed"}}`)); err != nil {
		t.Fatal(err)
	}
	err := s.handleUnion(mustUnion(t, `{"type":"response.completed","response":{"id":"r1","status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"b"}],"status":"completed"}]}}`))
	if err == nil {
		t.Fatal("expected conflict error")
	}
}

func TestHandleUnion_reasoningIncompleteCancel_noPart(t *testing.T) {
	t.Parallel()
	s := &sdkStream{}
	_ = s.handleUnion(mustUnion(t, `{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}`))
	_ = s.handleUnion(mustUnion(t, `{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"x"}`))
	_ = s.handleUnion(mustUnion(t, `{"type":"error","code":"canceled","message":"canceled"}`))
	for _, ev := range drainSDK(s) {
		if ev.Kind == aipapi.EventReasoningPart {
			t.Fatal("incomplete must not emit exact part")
		}
	}
}

func TestHandleUnion_textThenMalformedReasoning_terminalError(t *testing.T) {
	t.Parallel()
	s := &sdkStream{}
	if err := s.handleUnion(mustUnion(t, `{"type":"response.output_text.delta","delta":"hi"}`)); err != nil {
		t.Fatal(err)
	}
	err := s.handleUnion(mustUnion(t, `{"type":"response.output_item.done","output_index":1,"item":{"type":"reasoning","id":"","summary":[]}}`))
	if err == nil {
		t.Fatal("expected terminal error after content")
	}
	evs := drainSDK(s)
	committed := false
	for _, ev := range evs {
		if aipapi.OutputCommitted(ev) {
			committed = true
		}
		if ev.Kind == aipapi.EventReasoningPart {
			t.Fatal("no exact part on malformed")
		}
	}
	if !committed {
		t.Fatal("prior text must OutputCommitted")
	}
}

func TestHandleUnion_functionCallStillWorks(t *testing.T) {
	t.Parallel()
	s := &sdkStream{}
	if err := s.handleUnion(mustUnion(t, `{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"n","arguments":""}}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.handleUnion(mustUnion(t, `{"type":"response.function_call_arguments.delta","item_id":"fc_1","call_id":"call_1","delta":"{}"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.handleUnion(mustUnion(t, `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"n","arguments":"{}"}}`)); err != nil {
		t.Fatal(err)
	}
	var started, finished bool
	for _, ev := range drainSDK(s) {
		switch ev.Kind {
		case aipapi.EventToolCallStarted:
			started = true
		case aipapi.EventToolCallFinished:
			finished = true
		}
	}
	if !started || !finished {
		t.Fatalf("tool regression started=%v finished=%v", started, finished)
	}
}

func TestHandleUnion_completedInterleavesReasoningAndText(t *testing.T) {
	t.Parallel()
	s := &sdkStream{}
	u := mustUnion(t, `{"type":"response.completed","response":{"id":"r1","status":"completed","output":[{"type":"reasoning","id":"rs_a","summary":[],"status":"completed"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]},{"type":"reasoning","id":"rs_b","summary":[],"status":"completed"}]}}`)
	if err := s.handleUnion(u); err != nil {
		t.Fatal(err)
	}
	var seq []string
	for _, ev := range drainSDK(s) {
		switch ev.Kind {
		case aipapi.EventReasoningPart:
			seq = append(seq, "reasoning")
		case aipapi.EventTextDelta:
			seq = append(seq, "text")
		case aipapi.EventReasoningDelta:
			t.Fatal("no EventReasoningDelta")
		}
	}
	want := []string{"reasoning", "text", "reasoning"}
	if len(seq) != len(want) {
		t.Fatalf("seq=%v", seq)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("seq=%v want %v", seq, want)
		}
	}
}

func assertReasoningHandleErrorSafe(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, f := range []string{"rs_1", "SECRET_SUM", "SECRET_BODY", "SECRET_ENC", "summary_text", "reasoning_text"} {
		if strings.Contains(msg, f) {
			t.Fatalf("error must not leak provider values, contains %q: %v", f, err)
		}
	}
}

func TestHandleUnion_reasoningDoneUnknownField_noExactPart(t *testing.T) {
	t.Parallel()
	s := &sdkStream{}
	for _, raw := range []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}`,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"SECRET_SUM"}`,
		`{"type":"response.reasoning_summary_text.done","item_id":"rs_1","output_index":0,"summary_index":0,"text":"SECRET_SUM"}`,
		`{"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":0,"delta":"SECRET_BODY"}`,
		`{"type":"response.reasoning_text.done","item_id":"rs_1","output_index":0,"content_index":0,"text":"SECRET_BODY"}`,
	} {
		if err := s.handleUnion(mustUnion(t, raw)); err != nil {
			t.Fatalf("%s: %v", raw[:40], err)
		}
	}
	err := s.handleUnion(mustUnion(t, `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"SECRET_SUM"}],"content":[{"type":"reasoning_text","text":"SECRET_BODY"}],"extra":1,"status":"completed"}}`))
	assertReasoningHandleErrorSafe(t, err)
	for _, ev := range drainSDK(s) {
		if ev.Kind == aipapi.EventReasoningPart {
			t.Fatal("unknown done field must not emit EventReasoningPart")
		}
	}
}

func TestHandleUnion_reasoningDoneDuplicateKey_noExactPart(t *testing.T) {
	t.Parallel()
	s := &sdkStream{}
	if err := s.handleUnion(mustUnion(t, `{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.handleUnion(mustUnion(t, `{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"SECRET_SUM"}`)); err != nil {
		t.Fatal(err)
	}
	err := s.handleUnion(mustUnion(t, `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"SECRET_SUM"}],"id":"rs_2","status":"completed"}}`))
	assertReasoningHandleErrorSafe(t, err)
	for _, ev := range drainSDK(s) {
		if ev.Kind == aipapi.EventReasoningPart {
			t.Fatal("duplicate done key must not emit EventReasoningPart")
		}
	}
}

func TestHandleUnion_reasoningMalformedSeed_incompleteDone_noSanitizedEmit(t *testing.T) {
	t.Parallel()
	s := &sdkStream{}
	err := s.handleUnion(mustUnion(t, `{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"SECRET_SUM"}],"extra":true}}`))
	if err != nil {
		assertReasoningHandleErrorSafe(t, err)
		for _, ev := range drainSDK(s) {
			if ev.Kind == aipapi.EventReasoningPart {
				t.Fatal("must not emit on malformed seed")
			}
		}
		return
	}
	err = s.handleUnion(mustUnion(t, `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","status":"completed"}}`))
	assertReasoningHandleErrorSafe(t, err)
	for _, ev := range drainSDK(s) {
		if ev.Kind == aipapi.EventReasoningPart {
			t.Fatal("malformed seed must not be laundered into exact artifact")
		}
	}
}

func TestHandleUnion_reasoningDoneOmitsSummary_fillsFromAssembly(t *testing.T) {
	t.Parallel()
	s := &sdkStream{}
	for _, raw := range []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"think"}`,
		`{"type":"response.reasoning_summary_text.done","item_id":"rs_1","output_index":0,"summary_index":0,"text":"think"}`,
		`{"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":0,"delta":"c"}`,
		`{"type":"response.reasoning_text.done","item_id":"rs_1","output_index":0,"content_index":0,"text":"c"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","status":"completed"}}`,
	} {
		if err := s.handleUnion(mustUnion(t, raw)); err != nil {
			t.Fatalf("%s: %v", raw[:48], err)
		}
	}
	var n int
	var opaque string
	for _, ev := range drainSDK(s) {
		if ev.Kind == aipapi.EventReasoningPart {
			n++
			if ev.Reasoning != nil {
				opaque = string(ev.Reasoning.Opaque)
			}
		}
	}
	if n != 1 {
		t.Fatalf("want 1 exact part, got %d", n)
	}
	if !strings.Contains(opaque, `"summary"`) || !strings.Contains(opaque, `"content"`) || !strings.Contains(opaque, `"think"`) {
		t.Fatalf("assembly fill missing, opaque=%s", opaque)
	}
}

func TestRecv_textThenMalformedReasoning_preservesPendingText(t *testing.T) {
	t.Parallel()
	s := &sdkStream{pending: stream.NewPendingEventQueue(0)}
	s.mapper = openairesponsestream.New(&s.pending)

	feeds := []responses.ResponseStreamEventUnion{
		mustUnion(t, `{"type":"response.output_text.delta","delta":"hi"}`),
		mustUnion(t, `{"type":"response.output_item.done","output_index":1,"item":{"type":"reasoning","id":"","summary":[]}}`),
	}
	idx := 0
	var mu sync.Mutex
	pump := stream.EventPump[responses.ResponseStreamEventUnion]{
		Lock:    &mu,
		Pending: &s.pending,
		Read: func() (responses.ResponseStreamEventUnion, bool, error) {
			if idx >= len(feeds) {
				return responses.ResponseStreamEventUnion{}, false, nil
			}
			cur := feeds[idx]
			idx++
			return cur, true, nil
		},
		Handle: s.handleUnion,
	}

	var sawText bool
	var sawErr error
	for range 8 {
		ev, err := pump.Recv(context.Background())
		if err != nil {
			sawErr = err
			break
		}
		if ev.Kind == aipapi.EventTextDelta && ev.Delta == "hi" {
			sawText = true
		}
		if ev.Kind == aipapi.EventReasoningPart {
			t.Fatal("must not emit exact part")
		}
	}
	if !sawText {
		t.Fatal("EventPump must surface pending text before handler error")
	}
	if sawErr == nil {
		t.Fatal("expected malformed reasoning error after text")
	}
}
