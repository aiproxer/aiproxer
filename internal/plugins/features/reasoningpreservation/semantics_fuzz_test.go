package reasoningpreservation_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func clonePartForFuzz(p aipapi.Part) aipapi.Part {
	out := p
	if p.Reasoning != nil {
		cp := *p.Reasoning
		if len(p.Reasoning.Opaque) > 0 {
			cp.Opaque = append(json.RawMessage(nil), p.Reasoning.Opaque...)
		}
		if len(p.Reasoning.Summary) > 0 {
			cp.Summary = append(json.RawMessage(nil), p.Reasoning.Summary...)
		}
		if len(p.Reasoning.Content) > 0 {
			cp.Content = append(json.RawMessage(nil), p.Reasoning.Content...)
		}
		if len(p.Reasoning.EncryptedContent) > 0 {
			cp.EncryptedContent = append(json.RawMessage(nil), p.Reasoning.EncryptedContent...)
		}
		out.Reasoning = &cp
	}
	return out
}

func FuzzClassifyReasoningPart(f *testing.F) {
	// Seeds covering each dialect class and edge cases.
	f.Add(string(aipapi.ReasoningDialectOpenAIChatTextV1), "hello", "", []byte(""), false, false, false)
	f.Add(string(aipapi.ReasoningDialectOpenAIChatTextV1), "   ", "", []byte(""), false, false, false)
	f.Add(string(aipapi.ReasoningDialectOpenAIChatTextV1), "", "", []byte(""), false, false, false)
	f.Add(string(aipapi.ReasoningDialectOpenAIChatTextV1), "hello", "sig-123", []byte(""), false, false, false)
	f.Add(string(aipapi.ReasoningDialectOpenAIChatTextV1), "hello", "", []byte(`{"x":1}`), false, false, false)
	f.Add(string(aipapi.ReasoningDialectOpenAIChatTextV1), "hello", "", []byte(""), true, false, false)
	f.Add(string(aipapi.ReasoningDialectOpenAIChatTextV1), "hello", "", []byte(""), false, true, false)
	f.Add(string(aipapi.ReasoningDialectOpenAIChatTextV1), "hello", "", []byte(""), false, false, true)
	f.Add(string(aipapi.ReasoningDialectOpenAIResponsesItemV1), "hello", "", []byte(""), false, false, false)
	f.Add(string(aipapi.ReasoningDialectAnthropicThinkingV1), "think", "", []byte(""), false, false, false)
	f.Add(string(aipapi.ReasoningDialectAnthropicThinkingV1), "think", "sig", []byte(""), false, false, false)
	f.Add(string(aipapi.ReasoningDialectAnthropicRedactedThinkingV1), "text", "", []byte(`{"redacted":true}`), false, false, false)
	f.Add("vendor.custom.reasoning.v9", "hello", "", []byte(""), false, false, false)
	f.Add("unknown.dialect.v1", "hello", "sig", []byte("opaque"), false, false, false)
	f.Add("  openai.chat.reasoning_text.v1  ", "  hello world  ", "", []byte(""), false, false, false)
	f.Add("", "hello", "", []byte(""), false, false, false)
	f.Add("   ", "hello", "", []byte(""), false, false, false)

	f.Fuzz(func(t *testing.T, dialect, text, signature string, opaque []byte, summaryPresent, contentPresent, encryptedPresent bool) {
		if len(dialect) > 256 || len(text) > 4096 || len(signature) > 1024 || len(opaque) > 4096 {
			return
		}
		var opaqueMsg json.RawMessage
		if len(opaque) > 0 {
			opaqueMsg = json.RawMessage(append([]byte(nil), opaque...))
		}
		rp := &aipapi.ReasoningPart{
			Dialect:                 aipapi.ReasoningDialect(dialect),
			Text:                    text,
			Signature:               signature,
			Opaque:                  opaqueMsg,
			SummaryPresent:          summaryPresent,
			ContentPresent:          contentPresent,
			EncryptedContentPresent: encryptedPresent,
		}
		// Also set Summary/Content/EncryptedContent length to test exact path when present flags false but raw non-empty.
		// For fuzz, we keep them empty unless flag true; fuzz already covers len via opaque.
		part := aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: rp}

		// Non-mutation: clone before.
		before := clonePartForFuzz(part)
		got1 := reasoningpreservation.ClassifyReasoningPart(part)
		got2 := reasoningpreservation.ClassifyReasoningPart(part)

		// Purity: same input => same output.
		if got1 != got2 {
			t.Fatalf("purity fail: %v vs %v for dialect=%q text=%q sig=%q opaqueLen=%d summary=%v content=%v enc=%v", got1, got2, dialect, text, signature, len(opaque), summaryPresent, contentPresent, encryptedPresent)
		}
		// Non-mutation of inputs.
		if part.Reasoning.Text != before.Reasoning.Text || part.Reasoning.Signature != before.Reasoning.Signature || !bytes.Equal(part.Reasoning.Opaque, before.Reasoning.Opaque) {
			t.Fatalf("ClassifyReasoningPart mutated input")
		}
		if part.Kind != before.Kind {
			t.Fatalf("Kind mutated")
		}

		// Invariant: never SemanticText when signature/opaque present or dialect unknown or exact fields present or whitespace-only text.
		if got1 == reasoningpreservation.ReplaySemanticText {
			if signature != "" {
				t.Fatalf("must not be SemanticText when signature present dialect=%q", dialect)
			}
			if len(opaque) > 0 {
				t.Fatalf("must not be SemanticText when opaque present dialect=%q opaque=%q", dialect, string(opaque))
			}
			if summaryPresent || contentPresent || encryptedPresent {
				t.Fatalf("must not be SemanticText when exact responses fields present")
			}
			normalized := aipapi.NormalizeReasoningDialect(aipapi.ReasoningDialect(dialect))
			if normalized == "" {
				t.Fatalf("must not be SemanticText when dialect empty/unknown")
			}
			switch normalized {
			case aipapi.ReasoningDialectOpenAIChatTextV1:
			default:
				t.Fatalf("must not be SemanticText for non chat-text dialect %q normalized %q", dialect, normalized)
			}
			// Text must be non-whitespace.
			if len(text) == 0 {
				t.Fatalf("must not be SemanticText when text empty")
			}
			trimmed := ""
			for _, ch := range text {
				if ch != ' ' && ch != '\n' && ch != '\r' && ch != '\t' && ch != '\v' && ch != '\f' {
					trimmed = "nonws"
					break
				}
			}
			if trimmed == "" {
				t.Fatalf("must not be SemanticText when text whitespace-only %q", text)
			}
		}

		// Unknown dialect => never SemanticText.
		normalized := aipapi.NormalizeReasoningDialect(aipapi.ReasoningDialect(dialect))
		if normalized == "" {
			if got1 == reasoningpreservation.ReplaySemanticText {
				t.Fatalf("unknown empty dialect must not be SemanticText")
			}
		} else {
			switch normalized {
			case aipapi.ReasoningDialectOpenAIChatTextV1, aipapi.ReasoningDialectOpenAIResponsesItemV1, aipapi.ReasoningDialectAnthropicThinkingV1, aipapi.ReasoningDialectAnthropicRedactedThinkingV1:
			default:
				if got1 == reasoningpreservation.ReplaySemanticText {
					t.Fatalf("unknown dialect %q normalized %q must not be SemanticText", dialect, normalized)
				}
			}
		}
	})
}

func FuzzClassifyPlacements(f *testing.F) {
	f.Add(string(aipapi.ReasoningDialectOpenAIChatTextV1), "hello", "", []byte(""), 1, true)
	f.Add(string(aipapi.ReasoningDialectOpenAIChatTextV1), "   ", "", []byte(""), 2, true)
	f.Add(string(aipapi.ReasoningDialectOpenAIResponsesItemV1), "hello", "", []byte(""), 1, true)
	f.Add(string(aipapi.ReasoningDialectAnthropicThinkingV1), "think", "sig", []byte(""), 3, true)
	f.Add("unknown.v1", "hello", "", []byte(""), 1, true)
	f.Add(string(aipapi.ReasoningDialectOpenAIChatTextV1), "hello", "", []byte(`{"x":1}`), 1, false)
	f.Add("", "", "", []byte(""), 0, true)
	f.Add("  openai.chat.reasoning_text.v1  ", "hello", "", []byte(""), 2, true)

	f.Fuzz(func(t *testing.T, dialect, text, signature string, opaque []byte, count int, isReasoningKind bool) {
		if len(dialect) > 256 || len(text) > 4096 || len(signature) > 1024 || len(opaque) > 4096 {
			return
		}
		if count < 0 {
			count = -count
		}
		count = count % 6 // 0..5
		var opaqueMsg json.RawMessage
		if len(opaque) > 0 {
			opaqueMsg = json.RawMessage(append([]byte(nil), opaque...))
		}
		kind := aipapi.PartText
		if isReasoningKind {
			kind = aipapi.PartReasoning
		}
		placements := make([]reasoningpreservation.PlacedReasoning, 0, count)
		for i := 0; i < count; i++ {
			// Vary dialect slightly per index to exercise per-placement semantics.
			d := dialect
			if i == 1 {
				d = string(aipapi.ReasoningDialectAnthropicThinkingV1)
			}
			rp := &aipapi.ReasoningPart{
				Dialect:   aipapi.ReasoningDialect(d),
				Text:      text,
				Signature: signature,
				Opaque:    opaqueMsg,
			}
			p := aipapi.Part{Kind: kind, Reasoning: rp}
			if !isReasoningKind && i%2 == 0 {
				// Mix in a non-reasoning kind with nil Reasoning to test contradictory.
				p = aipapi.Part{Kind: aipapi.PartText, Reasoning: nil}
			}
			pr := reasoningpreservation.PlacedReasoning{
				BeforeNonReasoningPart: i * 10, // not used for PlacementIndex, must be ignored.
				Part:                   clonePartForFuzz(p),
			}
			placements = append(placements, pr)
		}
		// Save copy for non-mutation check.
		beforeLen := len(placements)
		var beforeFirst aipapi.Part
		if beforeLen > 0 {
			beforeFirst = clonePartForFuzz(placements[0].Part)
		}

		// Purity: same input => same output.
		got1 := reasoningpreservation.ClassifyPlacements(placements)
		got2 := reasoningpreservation.ClassifyPlacements(placements)
		if len(got1) != len(got2) {
			t.Fatalf("purity len mismatch %d vs %d", len(got1), len(got2))
		}
		for i := range got1 {
			if got1[i] != got2[i] {
				t.Fatalf("purity mismatch at %d: %+v vs %+v", i, got1[i], got2[i])
			}
		}
		// Non-mutation of input slice and parts.
		if len(placements) != beforeLen {
			t.Fatalf("placements mutated len")
		}
		if beforeLen > 0 {
			if placements[0].Part.Kind != beforeFirst.Kind {
				t.Fatalf("placements mutated kind")
			}
			if placements[0].Part.Reasoning != nil && beforeFirst.Reasoning != nil {
				if placements[0].Part.Reasoning.Text != beforeFirst.Reasoning.Text {
					t.Fatalf("placements mutated text")
				}
			}
		}

		// Invariants.
		if len(got1) != len(placements) {
			t.Fatalf("output len %d != input len %d", len(got1), len(placements))
		}
		for i, seg := range got1 {
			if seg.PlacementIndex != i {
				t.Fatalf("PlacementIndex passthrough want %d got %d", i, seg.PlacementIndex)
			}
			if seg.SourceBytes < 0 {
				t.Fatalf("SourceBytes negative at %d: %d", i, seg.SourceBytes)
			}
			// Dialect should be normalized or empty; source bytes consistency.
			if placements[i].Part.Kind != aipapi.PartReasoning || placements[i].Part.Reasoning == nil {
				if seg.Dialect != "" {
					t.Fatalf("non-reasoning placement dialect must be empty got %q", seg.Dialect)
				}
				if seg.SourceBytes != 0 {
					t.Fatalf("non-reasoning placement SourceBytes must be 0 got %d", seg.SourceBytes)
				}
			}
			// Never SemanticText when signature/opaque present or dialect unknown.
			if seg.Semantics == reasoningpreservation.ReplaySemanticText {
				if signature != "" {
					t.Fatalf("placement %d must not be SemanticText when signature present", i)
				}
				if len(opaque) > 0 {
					// Note: per-placement may have mixed kinds, but if kind is non-reasoning we already assert SourceBytes 0 and semantics Unknown.
					// So SemanticText should not happen with opaque when kind is reasoning.
					if placements[i].Part.Kind == aipapi.PartReasoning && placements[i].Part.Reasoning != nil && len(placements[i].Part.Reasoning.Opaque) > 0 {
						t.Fatalf("placement %d must not be SemanticText when opaque present", i)
					}
				}
				// Check dialect unknown case: if we forced unknown dialect, must not be semantic.
				// For mixed placements we created second dialect as anthropic which is exact, so also not semantic.
				if seg.Dialect == "" {
					t.Fatalf("placement %d SemanticText with empty dialect", i)
				}
				switch seg.Dialect {
				case aipapi.ReasoningDialectOpenAIChatTextV1:
				default:
					t.Fatalf("placement %d SemanticText for non-chat dialect %q", i, seg.Dialect)
				}
			}
			// Also ensure Semantics never out of bounded enum (0..2). Already typed but check.
			if seg.Semantics != reasoningpreservation.ReplayUnknown && seg.Semantics != reasoningpreservation.ReplayExactRequired && seg.Semantics != reasoningpreservation.ReplaySemanticText {
				t.Fatalf("invalid semantics %v", seg.Semantics)
			}
		}

		// Also verify empty/nil placements invariant: no panic and len 0.
		if count == 0 {
			if len(got1) != 0 {
				t.Fatalf("empty placements must give len 0")
			}
		}
	})
}
