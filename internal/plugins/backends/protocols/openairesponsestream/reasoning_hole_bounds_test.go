package openairesponsestream

import (
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3/responses"
)

func assertAssemblyErrorContentSafe(t *testing.T, err error) {
	t.Helper()
	assertReasoningErrorContentSafe(t, err)
	msg := err.Error()
	if strings.Contains(msg, "rs_low") || strings.Contains(msg, "rs_high") {
		t.Fatalf("assembly error must not leak ids: %v", err)
	}
}

func TestMapper_completedOmitsOpenLower_holeErrorNoHigherEmit(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	low := mustOutputItem(t, `{"type":"reasoning","id":"rs_low","summary":[]}`)
	high := mustOutputItem(t, `{"type":"reasoning","id":"rs_high","summary":[],"status":"completed"}`)
	if err := m.ReasoningOutputItemAdded(0, low); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningOutputItemDone(1, high); err != nil {
		t.Fatal(err)
	}
	if got := len(reasoningParts(drainEvents(q))); got != 0 {
		t.Fatalf("blocked higher must not emit before completed, got=%d", got)
	}
	resp := responses.Response{Output: []responses.ResponseOutputItemUnion{
		mustOutputItem(t, `{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"x"}]}`),
		high,
	}}
	if err := m.BeginCompleted(); err != nil {
		t.Fatal(err)
	}
	err := m.EmitCompletedOutputByIndex(resp)
	if err == nil {
		t.Fatal("expected unresolvable hole error when open lower omitted from completed")
	}
	assertAssemblyErrorContentSafe(t, err)
	if got := len(reasoningParts(drainEvents(q))); got != 0 {
		t.Fatalf("must not emit higher exact part on hole, got=%d", got)
	}
}

func TestMapper_completedLowerIncomplete_higherEmitsInOrder(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	lowAdded := mustOutputItem(t, `{"type":"reasoning","id":"rs_low","summary":[]}`)
	high := mustOutputItem(t, `{"type":"reasoning","id":"rs_high","summary":[{"type":"summary_text","text":"h"}],"status":"completed"}`)
	if err := m.ReasoningOutputItemAdded(0, lowAdded); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningOutputItemDone(1, high); err != nil {
		t.Fatal(err)
	}
	lowIncomplete := mustOutputItem(t, `{"type":"reasoning","id":"rs_low","summary":[],"status":"incomplete"}`)
	resp := responses.Response{Output: []responses.ResponseOutputItemUnion{lowIncomplete, high}}
	if err := m.BeginCompleted(); err != nil {
		t.Fatal(err)
	}
	if err := m.EmitCompletedOutputByIndex(resp); err != nil {
		t.Fatal(err)
	}
	parts := reasoningParts(drainEvents(q))
	if len(parts) != 1 {
		t.Fatalf("want only higher exact part, got=%d", len(parts))
	}
	if !strings.Contains(string(parts[0].Opaque), `"rs_high"`) {
		t.Fatalf("want higher opaque id token present structurally")
	}
	if strings.Contains(string(parts[0].Opaque), `"rs_low"`) {
		t.Fatal("incomplete lower must not emit exact part")
	}
}

func TestMapper_FinalizeOnEOF_unresolvedDraft_errors(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	low := mustOutputItem(t, `{"type":"reasoning","id":"rs_low","summary":[]}`)
	if err := m.ReasoningOutputItemAdded(0, low); err != nil {
		t.Fatal(err)
	}
	err := m.FinalizeOnEOF()
	if err == nil {
		t.Fatal("expected EOF unresolved draft error")
	}
	assertAssemblyErrorContentSafe(t, err)
	if got := len(reasoningParts(drainEvents(q))); got != 0 {
		t.Fatalf("EOF hole must not emit exact part, got=%d", got)
	}
}

func TestMapper_FinalizeOnEOF_inProgressDoneWithoutCompleted_errors(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	inProgress := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[],"status":"in_progress"}`)
	if err := m.ReasoningOutputItemDone(0, inProgress); err != nil {
		t.Fatal(err)
	}
	if got := len(reasoningParts(drainEvents(q))); got != 0 {
		t.Fatalf("in_progress done must not emit, got=%d", got)
	}
	err := m.FinalizeOnEOF()
	if err == nil {
		t.Fatal("expected EOF fail-closed after in_progress done without response.completed")
	}
	assertAssemblyErrorContentSafe(t, err)
}

func TestMapper_FinalizeOnEOF_incompleteDoneWithoutCompleted_errors(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	incomplete := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[],"status":"incomplete"}`)
	if err := m.ReasoningOutputItemDone(0, incomplete); err != nil {
		t.Fatal(err)
	}
	err := m.FinalizeOnEOF()
	if err == nil {
		t.Fatal("expected EOF fail-closed after incomplete done without response.completed")
	}
	assertAssemblyErrorContentSafe(t, err)
	if got := len(reasoningParts(drainEvents(q))); got != 0 {
		t.Fatalf("incomplete done must not emit, got=%d", got)
	}
}

func TestMapper_completedOmitsIncompleteStreamDone_holeError(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	incomplete := mustOutputItem(t, `{"type":"reasoning","id":"rs_low","summary":[],"status":"incomplete"}`)
	high := mustOutputItem(t, `{"type":"reasoning","id":"rs_high","summary":[],"status":"completed"}`)
	if err := m.ReasoningOutputItemDone(0, incomplete); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningOutputItemDone(1, high); err != nil {
		t.Fatal(err)
	}
	if got := len(reasoningParts(drainEvents(q))); got != 0 {
		t.Fatalf("awaiting incomplete lower must block higher emit, got=%d", got)
	}
	resp := responses.Response{Output: []responses.ResponseOutputItemUnion{
		mustOutputItem(t, `{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"x"}]}`),
		high,
	}}
	if err := m.BeginCompleted(); err != nil {
		t.Fatal(err)
	}
	err := m.EmitCompletedOutputByIndex(resp)
	if err == nil {
		t.Fatal("expected hole error when incomplete stream-done item omitted from completed")
	}
	assertAssemblyErrorContentSafe(t, err)
	if got := len(reasoningParts(drainEvents(q))); got != 0 {
		t.Fatalf("must not emit on unresolved incomplete hole, got=%d", got)
	}
}

func TestMapper_FinalizeOnEOF_noDrafts_ok(t *testing.T) {
	t.Parallel()
	m, _ := newTestMapper()
	if err := m.ResponseCreated(); err != nil {
		t.Fatal(err)
	}
	if err := m.OutputTextDelta("hi"); err != nil {
		t.Fatal(err)
	}
	if err := m.FinalizeOnEOF(); err != nil {
		t.Fatalf("clean stream without reasoning drafts must FinalizeOnEOF nil: %v", err)
	}
}

func TestMapper_FinalizeOnEOF_afterCompleted_ok(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	item := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[],"status":"completed"}`)
	if err := m.ReasoningOutputItemDone(0, item); err != nil {
		t.Fatal(err)
	}
	resp := responses.Response{Output: []responses.ResponseOutputItemUnion{item}}
	if err := m.BeginCompleted(); err != nil {
		t.Fatal(err)
	}
	if err := m.EmitCompletedOutputByIndex(resp); err != nil {
		t.Fatal(err)
	}
	_ = drainEvents(q)
	if err := m.FinalizeOnEOF(); err != nil {
		t.Fatalf("after completed reconcile FinalizeOnEOF must be nil: %v", err)
	}
}

func TestMapper_reasoningDraftCountBound(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	for i := range maxReasoningDrafts {
		id := "rs_" + strconv.Itoa(i)
		item := mustOutputItem(t, `{"type":"reasoning","id":"`+id+`","summary":[]}`)
		if err := m.ReasoningOutputItemAdded(int64(i), item); err != nil {
			t.Fatalf("draft %d: %v", i, err)
		}
	}
	extra := mustOutputItem(t, `{"type":"reasoning","id":"rs_overflow","summary":[]}`)
	err := m.ReasoningOutputItemAdded(int64(maxReasoningDrafts), extra)
	if err == nil {
		t.Fatal("expected draft-count overflow error")
	}
	assertAssemblyErrorContentSafe(t, err)
	if got := len(reasoningParts(drainEvents(q))); got != 0 {
		t.Fatalf("overflow must not emit, got=%d", got)
	}
}

func TestMapper_reasoningAssemblyByteBound(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	item := mustOutputItem(t, `{"type":"reasoning","id":"rs_1","summary":[]}`)
	if err := m.ReasoningOutputItemAdded(0, item); err != nil {
		t.Fatal(err)
	}
	chunk := strings.Repeat("x", 64<<10)
	var err error
	for {
		err = m.ReasoningSummaryTextDelta("rs_1", 0, 0, chunk)
		if err != nil {
			break
		}
		if m.reasoningAssemblyBytes > maxReasoningAssemblyBytes {
			t.Fatal("assembly bytes exceeded bound without error")
		}
	}
	if err == nil {
		t.Fatal("expected assembly byte overflow error")
	}
	assertAssemblyErrorContentSafe(t, err)
	if strings.Contains(err.Error(), chunk[:16]) {
		t.Fatalf("error must not leak assembly text: %v", err)
	}
	if got := len(reasoningParts(drainEvents(q))); got != 0 {
		t.Fatalf("byte overflow must not emit, got=%d", got)
	}
}

func TestMapper_sameIndexDifferentIDs_error(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	a := mustOutputItem(t, `{"type":"reasoning","id":"rs_a","summary":[]}`)
	b := mustOutputItem(t, `{"type":"reasoning","id":"rs_b","summary":[],"status":"completed"}`)
	if err := m.ReasoningOutputItemAdded(0, a); err != nil {
		t.Fatal(err)
	}
	err := m.ReasoningOutputItemDone(0, b)
	if err == nil {
		t.Fatal("expected same-index different-id error")
	}
	assertAssemblyErrorContentSafe(t, err)
	if got := len(reasoningParts(drainEvents(q))); got != 0 {
		t.Fatalf("collision must not emit, got=%d", got)
	}
}

func TestMapper_AbortReasoningAssembly_discardsWithoutError(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	low := mustOutputItem(t, `{"type":"reasoning","id":"rs_low","summary":[]}`)
	if err := m.ReasoningOutputItemAdded(0, low); err != nil {
		t.Fatal(err)
	}
	m.AbortReasoningAssembly()
	if err := m.FinalizeOnEOF(); err != nil {
		t.Fatalf("after abort, FinalizeOnEOF must be nil: %v", err)
	}
	if got := len(reasoningParts(drainEvents(q))); got != 0 {
		t.Fatalf("abort must not emit, got=%d", got)
	}
}

func TestMapper_StreamError_abortsDraftsContentSafe(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	secret := mustOutputItem(t, `{"type":"reasoning","id":"rs_secret_leak_id","summary":[{"type":"summary_text","text":"SECRET_SUMMARY_TEXT"}]}`)
	if err := m.ReasoningOutputItemAdded(0, secret); err != nil {
		t.Fatal(err)
	}
	if err := m.StreamError("upstream", "provider_failed", "provider_failed"); err != nil {
		t.Fatal(err)
	}
	if err := m.FinalizeOnEOF(); err != nil {
		t.Fatalf("after StreamError abort, FinalizeOnEOF must be nil: %v", err)
	}
	evs := drainEvents(q)
	if len(reasoningParts(evs)) != 0 {
		t.Fatal("StreamError must not emit exact parts")
	}
	for _, ev := range evs {
		if ev.Kind == aipapi.EventError {
			msg := ev.ErrorMessage + ev.ErrorCode
			for _, needle := range []string{"rs_secret_leak_id", "SECRET_SUMMARY_TEXT"} {
				if strings.Contains(msg, needle) {
					t.Fatalf("stream error leaked %q in %q", needle, msg)
				}
			}
		}
	}
}

func TestMapper_holeAfterText_stillErrors(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	if err := m.OutputTextDelta("hi"); err != nil {
		t.Fatal(err)
	}
	low := mustOutputItem(t, `{"type":"reasoning","id":"rs_low","summary":[]}`)
	high := mustOutputItem(t, `{"type":"reasoning","id":"rs_high","summary":[],"status":"completed"}`)
	if err := m.ReasoningOutputItemAdded(0, low); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningOutputItemDone(1, high); err != nil {
		t.Fatal(err)
	}
	resp := responses.Response{Output: []responses.ResponseOutputItemUnion{
		mustOutputItem(t, `{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"x"}]}`),
		high,
	}}
	if err := m.BeginCompleted(); err != nil {
		t.Fatal(err)
	}
	err := m.EmitCompletedOutputByIndex(resp)
	if err == nil {
		t.Fatal("expected hole error after content-class emission")
	}
	assertAssemblyErrorContentSafe(t, err)
	evs := drainEvents(q)
	if !hasKind(evs, aipapi.EventTextDelta) {
		t.Fatal("expected prior text")
	}
	if len(reasoningParts(evs)) != 0 {
		t.Fatal("must not emit exact part on post-content hole")
	}
}
