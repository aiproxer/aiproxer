package reasoningpreservation_test

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestReplaySemantics_WhitespaceOnlyNotSemantic(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
	}{
		{name: "empty", text: ""},
		{name: "space", text: "   "},
		{name: "tab", text: "\t"},
		{name: "newline", text: "\n"},
		{name: "mixed_ws", text: "  \n\t  "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			part := reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, tc.text, "", nil)
			got := reasoningpreservation.ClassifyReasoningPart(part)
			if got == reasoningpreservation.ReplaySemanticText {
				t.Fatalf("whitespace-only text %q must not be SemanticText, got %v", tc.text, got)
			}
			if got != reasoningpreservation.ReplayUnknown {
				t.Fatalf("whitespace-only text %q must be Unknown, got %v", tc.text, got)
			}
		})
	}
}

func TestReplaySemantics_NilAndContradictoryInputs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		part aipapi.Part
		want reasoningpreservation.ReplaySemantics
	}{
		{
			name: "nil_reasoning",
			part: aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: nil},
			want: reasoningpreservation.ReplayUnknown,
		},
		{
			name: "non_reasoning_kind_with_reasoning_payload",
			part: aipapi.Part{Kind: aipapi.PartText, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: "hello"}},
			want: reasoningpreservation.ReplayUnknown,
		},
		{
			name: "empty_part",
			part: aipapi.Part{},
			want: reasoningpreservation.ReplayUnknown,
		},
		{
			name: "dialect_whitespace_only",
			part: reasoningPart(aipapi.ReasoningDialect("   "), "hello", "", nil),
			want: reasoningpreservation.ReplayUnknown,
		},
		{
			name: "dialect_case_insensitive_semantic",
			part: reasoningPart(aipapi.ReasoningDialect("OpenAI.Chat.Reasoning_Text.V1"), "hello", "", nil),
			want: reasoningpreservation.ReplaySemanticText,
		},
		{
			name: "dialect_with_surrounding_space_semantic",
			part: reasoningPart(aipapi.ReasoningDialect("  openai.chat.reasoning_text.v1  "), "hello", "", nil),
			want: reasoningpreservation.ReplaySemanticText,
		},
		{
			name: "valid_semantic_with_surrounding_space_dialect",
			part: reasoningPart(aipapi.ReasoningDialect("  OPENAI.CHAT.REASONING_TEXT.V1 "), "  hello world  ", "", nil),
			want: reasoningpreservation.ReplaySemanticText,
		},
		{
			name: "whitespace_text_with_signature_still_exact",
			part: reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "   ", "sig", nil),
			want: reasoningpreservation.ReplayExactRequired,
		},
		{
			name: "whitespace_text_with_opaque_still_exact",
			part: reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "   ", "", mustOpaqueJSON(t, `{"x":1}`)),
			want: reasoningpreservation.ReplayExactRequired,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := reasoningpreservation.ClassifyReasoningPart(tc.part)
			if got != tc.want {
				t.Fatalf("ClassifyReasoningPart(%q) = %v want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestClassifyPlacement_ContradictoryAndEmpty(t *testing.T) {
	t.Parallel()
	// Non-reasoning kind with payload must be Unknown and SourceBytes 0 and Dialect "".
	part := aipapi.Part{Kind: aipapi.PartText, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: "hello", Opaque: json.RawMessage(`{"a":1}`)}}
	pr := placedReasoning(0, part)
	seg := reasoningpreservation.ClassifyPlacement(0, pr)
	if seg.Semantics != reasoningpreservation.ReplayUnknown {
		t.Fatalf("non-reasoning kind must be Unknown got %v", seg.Semantics)
	}
	if seg.SourceBytes != 0 {
		t.Fatalf("non-reasoning kind SourceBytes must be 0 got %d", seg.SourceBytes)
	}
	if seg.Dialect != "" {
		t.Fatalf("non-reasoning kind Dialect must be empty got %q", seg.Dialect)
	}
	if seg.PlacementIndex != 0 {
		t.Fatalf("PlacementIndex passthrough want 0 got %d", seg.PlacementIndex)
	}
	if seg.SourceBytes < 0 {
		t.Fatalf("SourceBytes negative")
	}

	// Empty placements slice.
	empty := reasoningpreservation.ClassifyPlacements(nil)
	if len(empty) != 0 {
		t.Fatalf("nil placements must produce empty result len 0 got %d", len(empty))
	}
	empty2 := reasoningpreservation.ClassifyPlacements([]reasoningpreservation.PlacedReasoning{})
	if len(empty2) != 0 {
		t.Fatalf("empty placements must produce len 0 got %d", len(empty2))
	}

	// PlacementIndex reflects input order, not BeforeNonReasoningPart.
	p1 := reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "hello", "", nil)
	pr1 := placedReasoning(99, p1)
	seg1 := reasoningpreservation.ClassifyPlacement(5, pr1)
	if seg1.PlacementIndex != 5 {
		t.Fatalf("PlacementIndex must be passed idx 5 got %d", seg1.PlacementIndex)
	}
	if seg1.Dialect != aipapi.ReasoningDialectOpenAIChatTextV1 {
		t.Fatalf("dialect mismatch got %q", seg1.Dialect)
	}
	placements := []reasoningpreservation.PlacedReasoning{
		placedReasoning(99, p1),
		placedReasoning(0, reasoningPart(aipapi.ReasoningDialectAnthropicThinkingV1, "x", "sig", nil)),
	}
	segs := reasoningpreservation.ClassifyPlacements(placements)
	if len(segs) != 2 {
		t.Fatalf("len 2 want %d", len(segs))
	}
	for i, s := range segs {
		if s.PlacementIndex != i {
			t.Fatalf("index %d PlacementIndex=%d", i, s.PlacementIndex)
		}
		if s.SourceBytes < 0 {
			t.Fatalf("SourceBytes negative at %d", i)
		}
	}
}

func TestClassifyReasoningPart_NeverSemanticWhenSignatureOrOpaque(t *testing.T) {
	t.Parallel()
	sig := reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "hello", "sig", nil)
	if got := reasoningpreservation.ClassifyReasoningPart(sig); got == reasoningpreservation.ReplaySemanticText {
		t.Fatalf("signature present must not be SemanticText")
	}
	op := reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "hello", "", mustOpaqueJSON(t, `{"x":1}`))
	if got := reasoningpreservation.ClassifyReasoningPart(op); got == reasoningpreservation.ReplaySemanticText {
		t.Fatalf("opaque present must not be SemanticText")
	}
	unknownDialect := reasoningPart(aipapi.ReasoningDialect("custom.v1"), "hello", "", nil)
	if got := reasoningpreservation.ClassifyReasoningPart(unknownDialect); got == reasoningpreservation.ReplaySemanticText {
		t.Fatalf("unknown dialect must not be SemanticText")
	}
	// Exact fields win.
	exactSummary := reasoningPartWithSummary(aipapi.ReasoningDialectOpenAIChatTextV1, "hello", json.RawMessage(`[]`))
	if got := reasoningpreservation.ClassifyReasoningPart(exactSummary); got == reasoningpreservation.ReplaySemanticText {
		t.Fatalf("exact summary present must not be SemanticText")
	}
}

func TestClassifyPlacements_PurityAndNonMutation(t *testing.T) {
	t.Parallel()
	part := reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "hello", "", nil)
	before := part.Reasoning.Text
	placements := []reasoningpreservation.PlacedReasoning{placedReasoning(0, part)}
	// Call twice purity.
	a := reasoningpreservation.ClassifyPlacements(placements)
	b := reasoningpreservation.ClassifyPlacements(placements)
	if len(a) != len(b) || a[0].Semantics != b[0].Semantics {
		t.Fatalf("purity failed %v vs %v", a, b)
	}
	if part.Reasoning.Text != before {
		t.Fatalf("mutation of input part")
	}
	// Mutate source slice after classify must not affect returned semantics (they are copies).
	placements[0].Part.Reasoning.Text = "mutated"
	if a[0].SourceBytes == 0 {
		t.Fatalf("SourceBytes missing")
	}
	// ClassifyReasoningPart purity.
	got1 := reasoningpreservation.ClassifyReasoningPart(part)
	got2 := reasoningpreservation.ClassifyReasoningPart(part)
	if got1 != got2 {
		t.Fatalf("purity fail %v vs %v", got1, got2)
	}
}
