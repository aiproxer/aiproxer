package openairesponsestream

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3/responses"
)

func mustOutputItem(t *testing.T, raw string) responses.ResponseOutputItemUnion {
	t.Helper()
	var item responses.ResponseOutputItemUnion
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		t.Fatal(err)
	}
	return item
}

// mustOutputItemRawJSON constructs via UnmarshalJSON so trailing garbage / exact bytes survive.
func mustOutputItemRawJSON(t *testing.T, raw string) responses.ResponseOutputItemUnion {
	t.Helper()
	var item responses.ResponseOutputItemUnion
	if err := item.UnmarshalJSON([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if item.RawJSON() != raw {
		t.Fatalf("RawJSON mismatch\nwant %q\ngot  %q", raw, item.RawJSON())
	}
	return item
}

func assertReasoningErrorContentSafe(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	forbidden := []string{
		"rs_1", "rs_2", "SECRET_SUM", "SECRET_BODY", "SECRET_ENC",
		`"summary"`, `"content"`, `"encrypted_content"`,
		"summary_text", "reasoning_text", "garbage",
	}
	for _, f := range forbidden {
		if strings.Contains(msg, f) {
			t.Fatalf("error must not leak provider values, contains %q: %v", f, err)
		}
	}
}

func drainEvents(q *stream.PendingEventQueue) []aipapi.Event {
	return stream.DrainPending(q)
}

func reasoningParts(evs []aipapi.Event) []*aipapi.ReasoningPart {
	var out []*aipapi.ReasoningPart
	for _, ev := range evs {
		if ev.Kind == aipapi.EventReasoningPart && ev.Reasoning != nil {
			cp := *ev.Reasoning
			out = append(out, &cp)
		}
	}
	return out
}

func TestMapper_reasoningOutputItemDone_emitsExactPart(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	item := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"s"}],"content":[{"type":"reasoning_text","text":"c"}],"encrypted_content":"enc","status":"completed"}`)
	if err := m.ReasoningOutputItemDone(0, item); err != nil {
		t.Fatal(err)
	}
	parts := reasoningParts(drainEvents(q))
	if len(parts) != 1 {
		t.Fatalf("parts=%d", len(parts))
	}
	if parts[0].Dialect != aipapi.ReasoningDialectOpenAIResponsesItemV1 {
		t.Fatalf("dialect=%q", parts[0].Dialect)
	}
	assertJSONEqual(t, `{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"s"}],"content":[{"type":"reasoning_text","text":"c"}],"encrypted_content":"enc","status":"completed"}`, string(parts[0].Opaque))
}

func TestMapper_reasoningAssembly_deltasNoReasoningDelta(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	added := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[]}`)
	if err := m.ReasoningOutputItemAdded(0, added); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningSummaryPartAdded("rs_1", 0, 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningSummaryTextDelta("rs_1", 0, 0, "sum"); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningSummaryTextDone("rs_1", 0, 0, "sum"); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningSummaryPartDone("rs_1", 0, 0, "sum"); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningTextDelta("rs_1", 0, 0, "body"); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningTextDone("rs_1", 0, 0, "body"); err != nil {
		t.Fatal(err)
	}
	done := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"sum"}],"status":"completed"}`)
	if err := m.ReasoningOutputItemDone(0, done); err != nil {
		t.Fatal(err)
	}
	evs := drainEvents(q)
	for _, ev := range evs {
		if ev.Kind == aipapi.EventReasoningDelta {
			t.Fatalf("must not emit EventReasoningDelta, got %+v", ev)
		}
	}
	parts := reasoningParts(evs)
	if len(parts) != 1 {
		t.Fatalf("want one exact part, events=%v", drainKinds(q))
	}
	assertJSONEqual(t, `{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"sum"}],"content":[{"type":"reasoning_text","text":"body"}],"status":"completed"}`, string(parts[0].Opaque))
}

func TestMapper_reasoningAssembly_doneOmitsContent_mergesDeltas(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	added := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[]}`)
	if err := m.ReasoningOutputItemAdded(0, added); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningSummaryTextDelta("rs_1", 0, 0, "th"); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningSummaryTextDelta("rs_1", 0, 0, "ink"); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningSummaryTextDone("rs_1", 0, 0, "think"); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningTextDelta("rs_1", 0, 0, "c"); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningTextDone("rs_1", 0, 0, "c"); err != nil {
		t.Fatal(err)
	}
	// done carries summary but omits content; assembly must supply content presence.
	done := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"think"}],"status":"completed"}`)
	if err := m.ReasoningOutputItemDone(0, done); err != nil {
		t.Fatal(err)
	}
	parts := reasoningParts(drainEvents(q))
	if len(parts) != 1 {
		t.Fatalf("parts=%d", len(parts))
	}
	assertJSONEqual(t, `{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"think"}],"content":[{"type":"reasoning_text","text":"c"}],"status":"completed"}`, string(parts[0].Opaque))
}

func TestMapper_reasoningAssembly_doneFullTextDoesNotDuplicateDeltas(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	added := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[]}`)
	if err := m.ReasoningOutputItemAdded(0, added); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningSummaryTextDelta("rs_1", 0, 0, "think"); err != nil {
		t.Fatal(err)
	}
	// Done sets authoritative full text; must not append again onto deltas.
	if err := m.ReasoningSummaryTextDone("rs_1", 0, 0, "think"); err != nil {
		t.Fatal(err)
	}
	done := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"think"}],"status":"completed"}`)
	if err := m.ReasoningOutputItemDone(0, done); err != nil {
		t.Fatal(err)
	}
	parts := reasoningParts(drainEvents(q))
	if len(parts) != 1 {
		t.Fatalf("parts=%d", len(parts))
	}
	assertJSONEqual(t, `{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"think"}],"status":"completed"}`, string(parts[0].Opaque))
}

func TestMapper_reasoningInProgressDone_noEmitUntilCompleted(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	inProgress := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[],"status":"in_progress"}`)
	if err := m.ReasoningOutputItemDone(0, inProgress); err != nil {
		t.Fatal(err)
	}
	if len(reasoningParts(drainEvents(q))) != 0 {
		t.Fatal("in_progress must not emit terminal exact part")
	}
	completed := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[],"status":"completed"}`)
	if err := m.EmitCompletedReasoningItems(responses.Response{Output: []responses.ResponseOutputItemUnion{completed}}); err != nil {
		t.Fatal(err)
	}
	if got := len(reasoningParts(drainEvents(q))); got != 1 {
		t.Fatalf("completed fallback want 1, got %d", got)
	}
}

func TestMapper_reasoningSemanticDedupe_keyOrderWhitespace(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	a := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[],"status":"completed"}`)
	b := mustOutputItem(t, `{ "status":"completed", "id":"rs_1", "summary":[] , "type":"reasoning" }`)
	if err := m.ReasoningOutputItemDone(0, a); err != nil {
		t.Fatal(err)
	}
	if err := m.EmitCompletedReasoningItems(responses.Response{Output: []responses.ResponseOutputItemUnion{b}}); err != nil {
		t.Fatal(err)
	}
	if got := len(reasoningParts(drainEvents(q))); got != 1 {
		t.Fatalf("semantic dedupe want 1, got %d", got)
	}
}

func TestMapper_reasoningStatusUpgrade_absentToCompleted_noFalseConflict(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	a := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"s"}]}`)
	b := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"s"}],"status":"completed"}`)
	if err := m.ReasoningOutputItemDone(0, a); err != nil {
		t.Fatal(err)
	}
	if err := m.EmitCompletedReasoningItems(responses.Response{Output: []responses.ResponseOutputItemUnion{b}}); err != nil {
		t.Fatal(err)
	}
	if got := len(reasoningParts(drainEvents(q))); got != 1 {
		t.Fatalf("status-only upgrade must dedupe, got %d", got)
	}
}

func TestMapper_reasoningSameIDDifferentOutputIndex_classified(t *testing.T) {
	t.Parallel()
	m, _ := newTestMapper()
	a := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[],"status":"completed"}`)
	b := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[],"status":"completed"}`)
	if err := m.ReasoningOutputItemDone(0, a); err != nil {
		t.Fatal(err)
	}
	err := m.ReasoningOutputItemDone(1, b)
	if err == nil {
		t.Fatal("same id at two output indices must be classified")
	}
	if strings.Contains(err.Error(), "rs_1") {
		t.Fatalf("content-safe error required: %v", err)
	}
}

func TestMapper_reasoningOutOfOrderDone_emitsLowerIndexFirst(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	a := mustOutputItem(t, `{"type":"reasoning","id":"rs_0","summary":[],"status":"completed"}`)
	b := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[],"status":"completed"}`)
	if err := m.ReasoningOutputItemAdded(0, a); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningOutputItemAdded(1, b); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningOutputItemDone(1, b); err != nil {
		t.Fatal(err)
	}
	if got := len(reasoningParts(drainEvents(q))); got != 0 {
		t.Fatalf("higher index must wait for lower open index, got %d parts", got)
	}
	if err := m.ReasoningOutputItemDone(0, a); err != nil {
		t.Fatal(err)
	}
	parts := reasoningParts(drainEvents(q))
	if len(parts) != 2 {
		t.Fatalf("parts=%d", len(parts))
	}
	if !strings.Contains(string(parts[0].Opaque), `"rs_0"`) || !strings.Contains(string(parts[1].Opaque), `"rs_1"`) {
		t.Fatalf("order wrong: %s then %s", parts[0].Opaque, parts[1].Opaque)
	}
}

func TestMapper_completedFallback_interleavesByOutputIndex(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	resp := responses.Response{
		Output: []responses.ResponseOutputItemUnion{
			mustOutputItem(t, `{"type":"reasoning","id":"rs_a","summary":[],"status":"completed"}`),
			mustOutputItem(t, `{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"hi"}]}`),
			mustOutputItem(t, `{"type":"reasoning","id":"rs_b","summary":[{"type":"summary_text","text":"b"}],"status":"completed"}`),
		},
	}
	if err := m.BeginCompleted(); err != nil {
		t.Fatal(err)
	}
	if err := m.EmitCompletedOutputByIndex(resp); err != nil {
		t.Fatal(err)
	}
	kinds := drainKinds(q)
	var seq []string
	for _, k := range kinds {
		switch k {
		case aipapi.EventReasoningPart:
			seq = append(seq, "reasoning")
		case aipapi.EventTextDelta:
			seq = append(seq, "text")
		}
	}
	want := []string{"reasoning", "text", "reasoning"}
	if len(seq) != len(want) {
		t.Fatalf("seq=%v kinds=%v", seq, kinds)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("seq=%v want %v", seq, want)
		}
	}
}

func TestMapper_reasoningRawJSONPresence_viaUnion(t *testing.T) {
	t.Parallel()
	raw := `{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":null,"status":"completed"}}`
	var u responses.ResponseStreamEventUnion
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	done := u.AsResponseOutputItemDone()
	item := done.Item
	if item.RawJSON() == "" {
		t.Fatal("ResponseOutputItemUnion.RawJSON empty after union parse")
	}
	if !strings.Contains(item.RawJSON(), `"encrypted_content":null`) {
		t.Fatalf("union item RawJSON must preserve null encrypted_content, got %s", item.RawJSON())
	}
	as := item.AsReasoning()
	if as.RawJSON() == "" {
		t.Fatal("AsReasoning.RawJSON empty after union parse")
	}
	m, q := newTestMapper()
	if err := m.ReasoningOutputItemDone(done.OutputIndex, item); err != nil {
		t.Fatal(err)
	}
	parts := reasoningParts(drainEvents(q))
	if len(parts) != 1 {
		t.Fatalf("parts=%d", len(parts))
	}
	assertJSONEqual(t, `{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":null,"status":"completed"}`, string(parts[0].Opaque))
}

func TestMapper_reasoningCompletedFallback_only(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	resp := responses.Response{
		Output: []responses.ResponseOutputItemUnion{
			mustOutputItem(t, `{"type":"reasoning","id":"rs_a","summary":[],"status":"completed"}`),
			mustOutputItem(t, `{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"hi"}]}`),
			mustOutputItem(t, `{"type":"reasoning","id":"rs_b","summary":[{"type":"summary_text","text":"b"}],"status":"completed"}`),
		},
	}
	if err := m.EmitCompletedReasoningItems(resp); err != nil {
		t.Fatal(err)
	}
	parts := reasoningParts(drainEvents(q))
	if len(parts) != 2 {
		t.Fatalf("parts=%d", len(parts))
	}
	if parts[0].Opaque == nil || !strings.Contains(string(parts[0].Opaque), `"rs_a"`) {
		t.Fatalf("order[0]=%s", parts[0].Opaque)
	}
	if !strings.Contains(string(parts[1].Opaque), `"rs_b"`) {
		t.Fatalf("order[1]=%s", parts[1].Opaque)
	}
}

func TestMapper_reasoningIncrementalAndCompleted_dedupe(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	item := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[],"status":"completed"}`)
	if err := m.ReasoningOutputItemDone(0, item); err != nil {
		t.Fatal(err)
	}
	resp := responses.Response{Output: []responses.ResponseOutputItemUnion{item}}
	if err := m.EmitCompletedReasoningItems(resp); err != nil {
		t.Fatal(err)
	}
	if got := len(reasoningParts(drainEvents(q))); got != 1 {
		t.Fatalf("dedupe want 1 part, got %d", got)
	}
}

func TestMapper_reasoningMultipleItems_orderByOutputIndex(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	a := mustOutputItem(t, `{"type":"reasoning","id":"rs_0","summary":[],"status":"completed"}`)
	b := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[],"status":"completed"}`)
	if err := m.ReasoningOutputItemDone(0, a); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningOutputItemDone(1, b); err != nil {
		t.Fatal(err)
	}
	parts := reasoningParts(drainEvents(q))
	if len(parts) != 2 {
		t.Fatalf("parts=%d", len(parts))
	}
	if !strings.Contains(string(parts[0].Opaque), `"rs_0"`) || !strings.Contains(string(parts[1].Opaque), `"rs_1"`) {
		t.Fatalf("order wrong: %s then %s", parts[0].Opaque, parts[1].Opaque)
	}
}

func TestMapper_reasoningConflictingDuplicateID(t *testing.T) {
	t.Parallel()
	m, _ := newTestMapper()
	a := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"a"}],"status":"completed"}`)
	b := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"b"}],"status":"completed"}`)
	if err := m.ReasoningOutputItemDone(0, a); err != nil {
		t.Fatal(err)
	}
	err := m.ReasoningOutputItemDone(1, b)
	if err == nil {
		t.Fatal("expected conflicting duplicate id error")
	}
	if strings.Contains(err.Error(), `"text":"a"`) || strings.Contains(err.Error(), "summary_text") {
		t.Fatalf("content-safe error required: %v", err)
	}
}

func TestMapper_reasoningMalformed_beforeContent(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	bad := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":null}`)
	err := m.ReasoningOutputItemDone(0, bad)
	if err == nil {
		t.Fatal("expected malformed error")
	}
	if len(reasoningParts(drainEvents(q))) != 0 {
		t.Fatal("must not emit exact part on malformed")
	}
}

func TestMapper_reasoningMalformed_afterText_returnsError(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	if err := m.OutputTextDelta("hi"); err != nil {
		t.Fatal(err)
	}
	bad := mustOutputItem(t, `{"type":"reasoning","summary":[]}`)
	err := m.ReasoningOutputItemDone(1, bad)
	if err == nil {
		t.Fatal("expected terminal mapping error after content")
	}
	evs := drainEvents(q)
	if !aipapi.OutputCommitted(evs[len(evs)-1]) && !hasKind(evs, aipapi.EventTextDelta) {
		t.Fatal("expected prior text content")
	}
	if len(reasoningParts(evs)) != 0 {
		t.Fatal("must not emit partial exact part")
	}
}

func TestMapper_reasoningIncomplete_noExactPart(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	added := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[]}`)
	if err := m.ReasoningOutputItemAdded(0, added); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningSummaryTextDelta("rs_1", 0, 0, "x"); err != nil {
		t.Fatal(err)
	}
	if len(reasoningParts(drainEvents(q))) != 0 {
		t.Fatal("incomplete assembly must not emit")
	}
}

func TestMapper_reasoningDoneBeforeLaterText_orderAssumption(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	item := mustOutputItem(t, `{"type":"reasoning","id":"rs_0","summary":[],"status":"completed"}`)
	if err := m.ReasoningOutputItemDone(0, item); err != nil {
		t.Fatal(err)
	}
	if err := m.OutputTextDelta("hi"); err != nil {
		t.Fatal(err)
	}
	kinds := drainKinds(q)
	var sawPart, sawText bool
	for _, k := range kinds {
		if k == aipapi.EventReasoningPart {
			if sawText {
				t.Fatalf("reasoning part must precede later text under SDK order assumption: %v", kinds)
			}
			sawPart = true
		}
		if k == aipapi.EventTextDelta {
			sawText = true
		}
	}
	if !sawPart || !sawText {
		t.Fatalf("kinds=%v", kinds)
	}
}

func hasKind(evs []aipapi.Event, kind aipapi.EventKind) bool {
	for _, ev := range evs {
		if ev.Kind == kind {
			return true
		}
	}
	return false
}

func TestMapper_reasoningDoneUnknownField_noExactPart(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	added := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[]}`)
	if err := m.ReasoningOutputItemAdded(0, added); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningSummaryTextDelta("rs_1", 0, 0, "SECRET_SUM"); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningSummaryTextDone("rs_1", 0, 0, "SECRET_SUM"); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningTextDelta("rs_1", 0, 0, "SECRET_BODY"); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningTextDone("rs_1", 0, 0, "SECRET_BODY"); err != nil {
		t.Fatal(err)
	}
	done := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"SECRET_SUM"}],"content":[{"type":"reasoning_text","text":"SECRET_BODY"}],"extra":1,"status":"completed"}`)
	err := m.ReasoningOutputItemDone(0, done)
	assertReasoningErrorContentSafe(t, err)
	if len(reasoningParts(drainEvents(q))) != 0 {
		t.Fatal("unknown done field must not emit EventReasoningPart")
	}
}

func TestMapper_reasoningDoneDuplicateKey_noExactPart(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	added := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[]}`)
	if err := m.ReasoningOutputItemAdded(0, added); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningSummaryTextDelta("rs_1", 0, 0, "SECRET_SUM"); err != nil {
		t.Fatal(err)
	}
	done := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"SECRET_SUM"}],"id":"rs_2","status":"completed"}`)
	err := m.ReasoningOutputItemDone(0, done)
	assertReasoningErrorContentSafe(t, err)
	if len(reasoningParts(drainEvents(q))) != 0 {
		t.Fatal("duplicate done key must not emit EventReasoningPart")
	}
}

func TestMapper_reasoningDoneTrailingGarbage_noExactPart(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	added := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[]}`)
	if err := m.ReasoningOutputItemAdded(0, added); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningSummaryTextDelta("rs_1", 0, 0, "SECRET_SUM"); err != nil {
		t.Fatal(err)
	}
	done := mustOutputItemRawJSON(t, `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"SECRET_SUM"}],"status":"completed"}garbage`)
	err := m.ReasoningOutputItemDone(0, done)
	assertReasoningErrorContentSafe(t, err)
	if len(reasoningParts(drainEvents(q))) != 0 {
		t.Fatal("trailing garbage on done must not emit EventReasoningPart")
	}
}

func TestMapper_reasoningMalformedSeed_incompleteDone_noSanitizedEmit(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	seed := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"SECRET_SUM"}],"extra":true}`)
	if err := m.ReasoningOutputItemAdded(0, seed); err != nil {
		// early structural reject at added is acceptable
		assertReasoningErrorContentSafe(t, err)
		if len(reasoningParts(drainEvents(q))) != 0 {
			t.Fatal("must not emit on malformed seed")
		}
		return
	}
	done := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","status":"completed"}`)
	err := m.ReasoningOutputItemDone(0, done)
	assertReasoningErrorContentSafe(t, err)
	if len(reasoningParts(drainEvents(q))) != 0 {
		t.Fatal("malformed seed must not be laundered into exact artifact")
	}
}

func TestMapper_reasoningDoneOmitsSummary_fillsFromAssembly(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	added := mustOutputItem(t, `{"type":"reasoning","id":"rs_1"}`)
	if err := m.ReasoningOutputItemAdded(0, added); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningSummaryTextDelta("rs_1", 0, 0, "think"); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningSummaryTextDone("rs_1", 0, 0, "think"); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningTextDelta("rs_1", 0, 0, "c"); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningTextDone("rs_1", 0, 0, "c"); err != nil {
		t.Fatal(err)
	}
	done := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","status":"completed"}`)
	if err := m.ReasoningOutputItemDone(0, done); err != nil {
		t.Fatal(err)
	}
	parts := reasoningParts(drainEvents(q))
	if len(parts) != 1 {
		t.Fatalf("parts=%d", len(parts))
	}
	assertJSONEqual(t, `{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"think"}],"content":[{"type":"reasoning_text","text":"c"}],"status":"completed"}`, string(parts[0].Opaque))
}

func TestMapper_reasoningDoneMalformedSyntax_cannotFillFromAssembly(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	added := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[]}`)
	if err := m.ReasoningOutputItemAdded(0, added); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningSummaryTextDelta("rs_1", 0, 0, "SECRET_SUM"); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningSummaryTextDone("rs_1", 0, 0, "SECRET_SUM"); err != nil {
		t.Fatal(err)
	}
	done := mustOutputItemRawJSON(t, `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"SECRET_SUM"}],"status":"completed"`)
	err := m.ReasoningOutputItemDone(0, done)
	assertReasoningErrorContentSafe(t, err)
	if len(reasoningParts(drainEvents(q))) != 0 {
		t.Fatal("malformed done syntax must not be filled from assembly")
	}
}

func assertJSONEqual(t *testing.T, want, got string) {
	t.Helper()
	var w, g any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want json: %v", err)
	}
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("got json: %v", err)
	}
	wb, _ := json.Marshal(w)
	gb, _ := json.Marshal(g)
	if string(wb) != string(gb) {
		t.Fatalf("json mismatch\nwant %s\ngot  %s", wb, gb)
	}
}
