package reasoningpreservation_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func reasoningPartWithSummary(dialect aipapi.ReasoningDialect, text string, summary json.RawMessage) aipapi.Part {
	rp := &aipapi.ReasoningPart{
		Dialect:        dialect,
		Text:           text,
		Summary:        summary,
		SummaryPresent: true,
	}
	if summary == nil {
		rp.Summary = json.RawMessage(`[]`)
	}
	return aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: rp}
}

func reasoningPartWithContent(dialect aipapi.ReasoningDialect, text string, content json.RawMessage) aipapi.Part {
	return aipapi.Part{
		Kind: aipapi.PartReasoning,
		Reasoning: &aipapi.ReasoningPart{
			Dialect:        dialect,
			Text:           text,
			Content:        content,
			ContentPresent: true,
		},
	}
}

func reasoningPartWithEncrypted(dialect aipapi.ReasoningDialect, text string, encrypted json.RawMessage, present bool) aipapi.Part {
	return aipapi.Part{
		Kind: aipapi.PartReasoning,
		Reasoning: &aipapi.ReasoningPart{
			Dialect:                 dialect,
			Text:                    text,
			EncryptedContent:        encrypted,
			EncryptedContentPresent: present,
		},
	}
}

func TestReplaySemantics_ExactNativeSignedOpaqueFailClosed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		part aipapi.Part
		want reasoningpreservation.ReplaySemantics
	}{
		{
			name: "openai_responses_item_plain_text_still_exact",
			part: reasoningPart(aipapi.ReasoningDialectOpenAIResponsesItemV1, "readable", "", nil),
			want: reasoningpreservation.ReplayExactRequired,
		},
		{
			name: "openai_responses_summary_exact",
			part: reasoningPartWithSummary(aipapi.ReasoningDialectOpenAIChatTextV1, "hello", json.RawMessage(`[{"type":"summary_text"}]`)),
			want: reasoningpreservation.ReplayExactRequired,
		},
		{
			name: "openai_responses_content_exact",
			part: reasoningPartWithContent(aipapi.ReasoningDialectOpenAIChatTextV1, "hello", json.RawMessage(`[{"type":"reasoning_text"}]`)),
			want: reasoningpreservation.ReplayExactRequired,
		},
		{
			name: "openai_responses_encrypted_present_null_exact",
			part: reasoningPartWithEncrypted(aipapi.ReasoningDialectOpenAIChatTextV1, "hello", json.RawMessage(`null`), true),
			want: reasoningpreservation.ReplayExactRequired,
		},
		{
			name: "openai_responses_encrypted_present_value_exact",
			part: reasoningPartWithEncrypted(aipapi.ReasoningDialectOpenAIChatTextV1, "", json.RawMessage(`"enc"`), true),
			want: reasoningpreservation.ReplayExactRequired,
		},
		{
			name: "codex_native_opaque_exact",
			part: reasoningPart(aipapi.ReasoningDialectOpenAIResponsesItemV1, "", "", mustOpaqueJSON(t, `{"type":"reasoning","encrypted_content":"private"}`)),
			want: reasoningpreservation.ReplayExactRequired,
		},
		{
			name: "codex_native_text_plus_opaque_exact",
			part: func() aipapi.Part {
				p := reasoningPart(aipapi.ReasoningDialectOpenAIResponsesItemV1, "readable", "", mustOpaqueJSON(t, `{"id":"x"}`))
				return p
			}(),
			want: reasoningpreservation.ReplayExactRequired,
		},
		{
			name: "anthropic_signed_thinking",
			part: reasoningPart(aipapi.ReasoningDialectAnthropicThinkingV1, "think", "sig-123", nil),
			want: reasoningpreservation.ReplayExactRequired,
		},
		{
			name: "anthropic_thinking_dialect_always_exact",
			part: reasoningPart(aipapi.ReasoningDialectAnthropicThinkingV1, "plain", "", nil),
			want: reasoningpreservation.ReplayExactRequired,
		},
		{
			name: "anthropic_redacted_thinking_opaque",
			part: reasoningPart(aipapi.ReasoningDialectAnthropicRedactedThinkingV1, "", "", mustOpaqueJSON(t, `{"redacted":true}`)),
			want: reasoningpreservation.ReplayExactRequired,
		},
		{
			name: "anthropic_redacted_dialect_plain_still_exact",
			part: reasoningPart(aipapi.ReasoningDialectAnthropicRedactedThinkingV1, "text", "", nil),
			want: reasoningpreservation.ReplayExactRequired,
		},
		{
			name: "opaque_only_exact",
			part: reasoningPart(aipapi.ReasoningDialectAnthropicRedactedThinkingV1, "", "", mustOpaqueJSON(t, `{"a":1}`)),
			want: reasoningpreservation.ReplayExactRequired,
		},
		{
			name: "unknown_dialect_plain_text",
			part: reasoningPart(aipapi.ReasoningDialect("vendor.custom.reasoning.v9"), "hello", "", nil),
			want: reasoningpreservation.ReplayUnknown,
		},
		{
			name: "unknown_dialect_with_signature",
			part: reasoningPart(aipapi.ReasoningDialect("unknown.dialect.v1"), "hello", "sig", nil),
			want: reasoningpreservation.ReplayUnknown,
		},
		{
			name: "empty_dialect_unknown",
			part: reasoningPart(aipapi.ReasoningDialect(""), "hello", "", nil),
			want: reasoningpreservation.ReplayUnknown,
		},
		{
			name: "plain_chat_with_signature_exact",
			part: reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "readable", "sig", nil),
			want: reasoningpreservation.ReplayExactRequired,
		},
		{
			name: "plain_chat_with_opaque_exact",
			part: reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "readable", "", mustOpaqueJSON(t, `{"x":1}`)),
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

func TestReplaySemantics_PlainHistoricalReasoningPositiveCase(t *testing.T) {
	t.Parallel()
	part := reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "ordinary reasoning text", "", nil)
	got := reasoningpreservation.ClassifyReasoningPart(part)
	if got != reasoningpreservation.ReplaySemanticText {
		t.Fatalf("plain chat text should be SemanticText, got %v", got)
	}
	empty := reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "", "", nil)
	if got2 := reasoningpreservation.ClassifyReasoningPart(empty); got2 == reasoningpreservation.ReplaySemanticText {
		t.Fatalf("empty text must not be SemanticText, got %v", got2)
	}
}

func TestReplaySemantics_ReadableTextInsideExactBearingNeverCompressible(t *testing.T) {
	t.Parallel()
	cases := []aipapi.Part{
		reasoningPartWithSummary(aipapi.ReasoningDialectOpenAIChatTextV1, "readable text but exact summary", json.RawMessage(`[]`)),
		reasoningPartWithContent(aipapi.ReasoningDialectOpenAIChatTextV1, "readable but content", json.RawMessage(`[{"type":"x"}]`)),
		reasoningPartWithEncrypted(aipapi.ReasoningDialectOpenAIChatTextV1, "readable but encrypted", json.RawMessage(`null`), true),
		reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "readable but signed", "sig", nil),
		reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "readable but opaque", "", mustOpaqueJSON(t, `{"k":"v"}`)),
		reasoningPart(aipapi.ReasoningDialectOpenAIResponsesItemV1, "readable but responses dialect", "", nil),
		reasoningPart(aipapi.ReasoningDialectAnthropicThinkingV1, "readable but anthropic", "", nil),
	}
	for i, p := range cases {
		got := reasoningpreservation.ClassifyReasoningPart(p)
		if got == reasoningpreservation.ReplaySemanticText {
			t.Fatalf("case %d exact-bearing with readable text must not be SemanticText, part=%+v got=%v", i, p.Reasoning, got)
		}
		if got != reasoningpreservation.ReplayExactRequired && got != reasoningpreservation.ReplayUnknown {
			t.Fatalf("case %d must fail closed to Exact or Unknown, got %v", i, got)
		}
	}
}

func TestReplaySemantics_MixedExactAndSemanticPerPlacement(t *testing.T) {
	t.Parallel()
	exactPart := reasoningPart(aipapi.ReasoningDialectAnthropicThinkingV1, "signed", "sig-1", nil)
	semanticPart := reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "plain text", "", nil)
	placements := []reasoningpreservation.PlacedReasoning{
		placedReasoning(0, exactPart),
		placedReasoning(1, semanticPart),
		placedReasoning(1, reasoningPart(aipapi.ReasoningDialectOpenAIResponsesItemV1, "responses", "", nil)),
		placedReasoning(2, reasoningPart(aipapi.ReasoningDialect("unknown.v1"), "unknown", "", nil)),
	}
	segs := reasoningpreservation.ClassifyPlacements(placements)
	if len(segs) != 4 {
		t.Fatalf("len=%d want 4", len(segs))
	}
	wantSemantics := []reasoningpreservation.ReplaySemantics{
		reasoningpreservation.ReplayExactRequired,
		reasoningpreservation.ReplaySemanticText,
		reasoningpreservation.ReplayExactRequired,
		reasoningpreservation.ReplayUnknown,
	}
	for i, want := range wantSemantics {
		if segs[i].Semantics != want {
			t.Fatalf("placement %d semantics=%v want %v", i, segs[i].Semantics, want)
		}
		if segs[i].PlacementIndex != i {
			t.Fatalf("placement index %d got %d", i, segs[i].PlacementIndex)
		}
		if segs[i].SourceBytes == 0 {
			t.Fatalf("placement %d SourceBytes should be >0", i)
		}
	}
	if segs[1].Dialect != aipapi.NormalizeReasoningDialect(aipapi.ReasoningDialectOpenAIChatTextV1) {
		t.Fatalf("dialect preserved incorrectly %q", segs[1].Dialect)
	}
}

func TestReplaySemantics_ClassifierIsPureAndNonMutating(t *testing.T) {
	t.Parallel()
	part := reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "hello", "", nil)
	before := aipapi.Part{Kind: part.Kind, Reasoning: &aipapi.ReasoningPart{Dialect: part.Reasoning.Dialect, Text: part.Reasoning.Text, Signature: part.Reasoning.Signature, Opaque: append(json.RawMessage(nil), part.Reasoning.Opaque...)}}
	got1 := reasoningpreservation.ClassifyReasoningPart(part)
	got2 := reasoningpreservation.ClassifyReasoningPart(part)
	if got1 != got2 {
		t.Fatalf("classifier must be pure: %v vs %v", got1, got2)
	}
	if part.Reasoning.Text != before.Reasoning.Text || part.Reasoning.Signature != before.Reasoning.Signature {
		t.Fatalf("classifier mutated input")
	}
	placements := []reasoningpreservation.PlacedReasoning{placedReasoning(0, part)}
	segs := reasoningpreservation.ClassifyPlacements(placements)
	if len(segs) != 1 || segs[0].Semantics != reasoningpreservation.ReplaySemanticText {
		t.Fatalf("expected semantic text")
	}
	placements[0].Part.Reasoning.Text = "mutated"
	if segs[0].SourceBytes == 0 {
		t.Fatal("SourceBytes missing")
	}
	part2 := reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "other", "", nil)
	segs2 := reasoningpreservation.ClassifyPlacements([]reasoningpreservation.PlacedReasoning{placedReasoning(5, part2)})
	if segs2[0].PlacementIndex != 0 {
		t.Fatalf("PlacementIndex should reflect input order 0 got %d", segs2[0].PlacementIndex)
	}
	if segs2[0].Dialect != aipapi.ReasoningDialectOpenAIChatTextV1 {
		t.Fatalf("dialect mismatch")
	}
}

func TestReplaySemantics_CompressionDisabledByteStructureEquivalence(t *testing.T) {
	t.Parallel()
	now, _ := newTestClock(time.Unix(1_700_000_000, 0).UTC())
	st := newMemoryStore(t, StoreOptionsWithNow(now))
	partition := reasoningpreservation.NewSessionPartition("session-equivalence")
	ctx := context.Background()
	plainPart := reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "plain reasoning", "", nil)
	exactPart := reasoningPart(aipapi.ReasoningDialectAnthropicThinkingV1, "signed", "sig", nil)
	placements := []reasoningpreservation.PlacedReasoning{
		placedReasoning(0, plainPart),
		placedReasoning(1, exactPart),
	}
	semantics := reasoningpreservation.ClassifyPlacements(placements)
	if semantics[0].Semantics != reasoningpreservation.ReplaySemanticText {
		t.Fatalf("expected semantic")
	}
	if semantics[1].Semantics != reasoningpreservation.ReplayExactRequired {
		t.Fatalf("expected exact")
	}
	wantPlacements := []reasoningpreservation.PlacedReasoning{
		placedReasoning(0, reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "plain reasoning", "", nil)),
		placedReasoning(1, reasoningPart(aipapi.ReasoningDialectAnthropicThinkingV1, "signed", "sig", nil)),
	}
	art := reasoningpreservation.TurnArtifact{
		ID:             "eq-1",
		Anchor:         anchorFor(t, aipapi.TextPart("visible")),
		SourceBackend:  "backend",
		SourceModel:    "model",
		Reasoning:      placements,
		CreatedAt:      now().UTC(),
		ReasoningBytes: aipapi.ReasoningPayloadBytes(plainPart.Reasoning) + aipapi.ReasoningPayloadBytes(exactPart.Reasoning),
	}
	if _, err := st.Append(ctx, partition, art); err != nil {
		t.Fatalf("Append: %v", err)
	}
	art.Reasoning[0].Part.Reasoning.Text = "mutated-after-append"
	snap, err := st.Snapshot(ctx, partition)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap) != 1 {
		t.Fatalf("len snap %d", len(snap))
	}
	if snap[0].Reasoning[0].Part.Reasoning.Text == "mutated-after-append" {
		t.Fatal("store must preserve original byte-identical artifact despite classifier presence")
	}
	if !reflect.DeepEqual(snap[0].Reasoning, wantPlacements) {
		t.Fatalf("snapshot reasoning not byte/structure identical: got %+v want %+v", snap[0].Reasoning, wantPlacements)
	}
	call := aipapi.Call{Messages: []aipapi.Message{assistantMsg(aipapi.TextPart("visible"))}}
	got, err := reasoningpreservation.RestoreMissingReasoning(reasoningpreservation.RestoreInput{
		Action:        reasoningpreservation.ActionRestore,
		Call:          &call,
		Artifacts:     snap,
		Eligible:      true,
		ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1, aipapi.ReasoningDialectAnthropicThinkingV1}},
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !got.Mutated || got.RestoredCount != 1 {
		t.Fatalf("restore must be byte/structure identical with classifier present, got %+v", got)
	}
	if len(call.Messages[0].Parts) != 3 {
		t.Fatalf("restored parts len %d want 3", len(call.Messages[0].Parts))
	}
	if call.Messages[0].Parts[0].Reasoning == nil || call.Messages[0].Parts[0].Reasoning.Text != "plain reasoning" {
		t.Fatalf("restored plain payload mutated: %+v", call.Messages[0].Parts)
	}
	if call.Messages[0].Parts[2].Reasoning == nil || call.Messages[0].Parts[2].Reasoning.Signature != "sig" {
		t.Fatalf("restored exact payload mutated: %+v", call.Messages[0].Parts)
	}
	snap[0].Reasoning[0].Part.Reasoning.Text = "second-mutation"
	snap2, _ := st.Snapshot(ctx, partition)
	if snap2[0].Reasoning[0].Part.Reasoning.Text == "second-mutation" {
		t.Fatal("snapshot defensive copy must remain")
	}
}

func StoreOptionsWithNow(now func() time.Time) reasoningpreservation.StoreOptions {
	return reasoningpreservation.StoreOptions{
		TTL:                      time.Hour,
		MaxTurnsPerSession:       4,
		MaxReasoningBytesPerTurn: 1024,
		MaxSessionBytes:          4096,
		Now:                      now,
	}
}
