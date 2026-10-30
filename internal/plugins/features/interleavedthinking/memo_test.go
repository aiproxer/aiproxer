package interleavedthinking

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const (
	memoOpenTag  = "<proxy_thinker_memo>"
	memoCloseTag = "</proxy_thinker_memo>"
)

func newRecorder(maxBytes int) *Recorder {
	return &Recorder{
		MaxMemoBytes:          maxBytes,
		SourceSelector:        "openai-responses:gpt-4o[thinker]",
		Backend:               "openai-responses",
		Model:                 "gpt-4o",
		RequestID:             "req-1",
		RegularTurnsRemaining: 2,
	}
}

func textDelta(s string) aipapi.Event {
	return aipapi.Event{Kind: aipapi.EventTextDelta, Delta: s}
}

func reasoningDelta(s string) aipapi.Event {
	return aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: s}
}

func observeAll(t *testing.T, r *Recorder, events []aipapi.Event) {
	t.Helper()
	for _, ev := range events {
		r.Observe(ev)
	}
}

func TestRecorder_WholeOutputCapturedAsMemo(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	observeAll(t, r, []aipapi.Event{
		textDelta("## Session Steering Memo\n- **Goal**: ship it\n"),
		textDelta("- **Current state**: almost done\n"),
	})
	state := r.Finish(false)
	want := "## Session Steering Memo\n- **Goal**: ship it\n- **Current state**: almost done"
	if state.Memo != want {
		t.Fatalf("expected full output memo %q, got %q", want, state.Memo)
	}
	if state.ExtractionSource != ExtractionSourceFull {
		t.Fatalf("expected extraction source %q, got %q", ExtractionSourceFull, state.ExtractionSource)
	}
	if state.StreamInterrupted {
		t.Fatal("expected StreamInterrupted=false")
	}
	if !r.HadContent() {
		t.Fatal("HadContent must report observed content")
	}
}

func TestRecorder_WholeOutputAcrossTextAndReasoningDeltas(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	observeAll(t, r, []aipapi.Event{
		textDelta("thinking first, "),
		reasoningDelta("then the plan"),
	})
	state := r.Finish(false)
	if state.Memo != "thinking first, then the plan" {
		t.Fatalf("expected combined memo %q, got %q", "thinking first, then the plan", state.Memo)
	}
	if state.ExtractionSource != ExtractionSourceFull {
		t.Fatalf("expected extraction source full, got %q", state.ExtractionSource)
	}
}

func TestRecorder_ResidualWrapperTagsStrippedDefensively(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	observeAll(t, r, []aipapi.Event{
		textDelta("intro " + memoOpenTag + "the plan" + memoCloseTag + " outro"),
	})
	state := r.Finish(false)
	if state.Memo != "intro the plan outro" {
		t.Fatalf("expected tag-stripped full memo %q, got %q", "intro the plan outro", state.Memo)
	}
	if state.ExtractionSource != ExtractionSourceFull {
		t.Fatalf("expected extraction source full, got %q", state.ExtractionSource)
	}
}

func TestRecorder_ResidualWrapperTagsSplitAcrossDeltas(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	observeAll(t, r, []aipapi.Event{
		textDelta("intro " + memoOpenTag + "the "),
		textDelta("plan"),
		textDelta(memoCloseTag + " outro"),
	})
	state := r.Finish(false)
	if state.Memo != "intro the plan outro" {
		t.Fatalf("expected tag-stripped memo %q, got %q", "intro the plan outro", state.Memo)
	}
	if state.ExtractionSource != ExtractionSourceFull {
		t.Fatalf("expected extraction source full, got %q", state.ExtractionSource)
	}
}

func TestRecorder_MarkdownMemoWithoutTagsIsUnchanged(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	observeAll(t, r, []aipapi.Event{
		textDelta("## Session Steering Memo\n"),
		reasoningDelta("- **Recommended next step**: continue\n"),
	})
	state := r.Finish(false)
	want := "## Session Steering Memo\n- **Recommended next step**: continue"
	if state.Memo != want {
		t.Fatalf("expected memo %q, got %q", want, state.Memo)
	}
	if state.ExtractionSource != ExtractionSourceFull {
		t.Fatalf("expected extraction source full, got %q", state.ExtractionSource)
	}
}

func TestRecorder_ByteLimit_TruncatesWholeOutput(t *testing.T) {
	t.Parallel()
	r := newRecorder(8)
	observeAll(t, r, []aipapi.Event{
		textDelta("abcdefghij"),
	})
	state := r.Finish(false)
	if len(state.Memo) > 8 {
		t.Fatalf("bounded memo must not exceed limit: got %d bytes %q", len(state.Memo), state.Memo)
	}
	if state.Memo != "abcdefghij"[:8] {
		t.Fatalf("expected truncated memo %q, got %q", "abcdefghij"[:8], state.Memo)
	}
	if state.ExtractionSource != ExtractionSourceFull {
		t.Fatalf("expected extraction source full, got %q", state.ExtractionSource)
	}
}

func TestRecorder_ByteLimit_ZeroDisables(t *testing.T) {
	t.Parallel()
	r := newRecorder(0)
	observeAll(t, r, []aipapi.Event{
		textDelta("an unbounded plan body that is longer than any small limit"),
	})
	state := r.Finish(false)
	if state.Memo != "an unbounded plan body that is longer than any small limit" {
		t.Fatalf("zero limit must not truncate, got %q", state.Memo)
	}
}

func TestRecorder_TruncatedBoundaryKeepsDropping(t *testing.T) {
	t.Parallel()
	r := newRecorder(8)
	observeAll(t, r, []aipapi.Event{
		textDelta("abcdefgh"),
		textDelta("ij"),
	})
	state := r.Finish(false)
	if state.Memo != "abcdefgh" {
		t.Fatalf("post-truncation deltas must not extend the memo, got %q", state.Memo)
	}
}

func TestRecorder_InterruptedStreamSetsMetadata(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	observeAll(t, r, []aipapi.Event{
		textDelta("partial output before interruption"),
	})
	state := r.Finish(true)
	if !state.StreamInterrupted {
		t.Fatal("Finish(true) must set StreamInterrupted")
	}
	if state.Memo != "partial output before interruption" {
		t.Fatalf("interrupted memo must keep captured output, got %q", state.Memo)
	}
	if state.ExtractionSource != ExtractionSourceFull {
		t.Fatalf("expected extraction source full, got %q", state.ExtractionSource)
	}
}

func TestRecorder_EmptyStreamYieldsEmptyMemo(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	state := r.Finish(false)
	if state.Memo != "" {
		t.Fatalf("empty stream must yield empty memo, got %q", state.Memo)
	}
	if r.HadContent() {
		t.Fatal("empty stream must not report content")
	}
}

func TestRecorder_WhitespaceOnlyYieldsEmptyMemo(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	observeAll(t, r, []aipapi.Event{
		textDelta("  \n\t "),
	})
	state := r.Finish(false)
	if state.Memo != "" {
		t.Fatalf("whitespace-only output must yield empty memo, got %q", state.Memo)
	}
	if !r.HadContent() {
		t.Fatal("whitespace-only output must still report content observed")
	}
}

func TestRecorder_PreservesMetadata(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	observeAll(t, r, []aipapi.Event{
		textDelta("plan"),
	})
	state := r.Finish(false)
	if state.SourceSelector != "openai-responses:gpt-4o[thinker]" {
		t.Fatalf("expected SourceSelector preserved, got %q", state.SourceSelector)
	}
	if state.Backend != "openai-responses" || state.Model != "gpt-4o" || state.RequestID != "req-1" {
		t.Fatalf("expected identity metadata preserved, got %+v", state)
	}
	if state.RegularTurnsRemaining != 2 {
		t.Fatalf("expected RegularTurnsRemaining preserved, got %d", state.RegularTurnsRemaining)
	}
	if state.CreatedAt.IsZero() {
		t.Fatal("expected CreatedAt to be set at Finish")
	}
	if state.InjectedCount != 0 || state.VisibleToClient {
		t.Fatalf("capture must not set injection fields, got injected=%d visible=%v", state.InjectedCount, state.VisibleToClient)
	}
}

func TestRecorder_IgnoresNonContentEvents(t *testing.T) {
	t.Parallel()
	r := newRecorder(4096)
	observeAll(t, r, []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		textDelta("plan"),
		{Kind: aipapi.EventUsageDelta, InputTokens: 10},
		{Kind: aipapi.EventResponseFinished},
	})
	state := r.Finish(false)
	if state.Memo != "plan" {
		t.Fatalf("expected memo %q, got %q", "plan", state.Memo)
	}
}

func TestStripResidualMemoTags_RemovesCompleteTags(t *testing.T) {
	t.Parallel()
	got := StripResidualMemoTags("intro " + memoOpenTag + "plan" + memoCloseTag + " outro")
	if got != "intro plan outro" {
		t.Fatalf("got %q", got)
	}
}

func TestStripResidualMemoTags_RemovesLoneCompleteTags(t *testing.T) {
	t.Parallel()
	got := StripResidualMemoTags(memoOpenTag + "plan")
	if got != "plan" {
		t.Fatalf("lone open tag must be stripped, got %q", got)
	}
	got = StripResidualMemoTags("plan" + memoCloseTag)
	if got != "plan" {
		t.Fatalf("lone close tag must be stripped, got %q", got)
	}
}

func TestStripResidualMemoTags_PreservesIncompleteFragments(t *testing.T) {
	t.Parallel()
	got := StripResidualMemoTags("<proxy_thinker_m")
	if got != "<proxy_thinker_m" {
		t.Fatalf("incomplete tag fragment must be preserved, got %q", got)
	}
}

func TestStripResidualMemoTags_LookalikePreserved(t *testing.T) {
	t.Parallel()
	got := StripResidualMemoTags("<proxy_thinker_memoX not a tag>")
	if got != "<proxy_thinker_memoX not a tag>" {
		t.Fatalf("lookalike tag must be preserved, got %q", got)
	}
}

func TestStripResidualMemoTags_CaseInsensitive(t *testing.T) {
	t.Parallel()
	got := StripResidualMemoTags("A<PROXY_THINKER_MEMO>plan</proxy_thinker_memo>Z")
	if got != "AplanZ" {
		t.Fatalf("mixed-case tags must be stripped, got %q", got)
	}
}

func TestStripResidualMemoTags_AttributeTagsStripped(t *testing.T) {
	t.Parallel()
	got := StripResidualMemoTags("<proxy_thinker_memo id=\"1\">plan</proxy_thinker_memo>")
	if got != "plan" {
		t.Fatalf("attribute tags must be stripped, got %q", got)
	}
}

func TestStripResidualMemoTags_EmptyInput(t *testing.T) {
	t.Parallel()
	if got := StripResidualMemoTags(""); got != "" {
		t.Fatalf("empty input must stay empty, got %q", got)
	}
}

func TestStripResidualMemoTags_NoTagsUnchanged(t *testing.T) {
	t.Parallel()
	if got := StripResidualMemoTags("## Session Steering Memo\nplain text"); got != "## Session Steering Memo\nplain text" {
		t.Fatalf("untagged text must be unchanged, got %q", got)
	}
	if strings.Contains(StripResidualMemoTags("plain"), memoOpenTag) {
		t.Fatal("strip must not introduce tags")
	}
}
