package reasoningpreservation_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/internal/plugins/protocols/openairesponsesitem"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestExactHardening_errorsNeverLeakSensitiveTokens(t *testing.T) {
	t.Parallel()
	const (
		secretID   = "rs_secret_leak_id"
		secretSum  = "SECRET_SUMMARY_TEXT"
		secretBody = "SECRET_CONTENT_BODY"
		secretEnc  = "SECRET_ENCRYPTED_VALUE"
	)
	forbidden := []string{secretID, secretSum, secretBody, secretEnc}

	assertSafe := func(t *testing.T, label, msg string) {
		t.Helper()
		for _, needle := range forbidden {
			if strings.Contains(msg, needle) {
				t.Fatalf("%s leaked %q in %q", label, needle, msg)
			}
		}
	}

	t.Run("neutral_parser", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{"id":"` + secretID + `","summary":[{"type":"summary_text","text":"` + secretSum + `"}],"content":[{"type":"reasoning_text","text":"` + secretBody + `"}],"encrypted_content":"` + secretEnc + `","extra":1}`)
		_, err := openairesponsesitem.CanonizeReasoningItemOpaque(raw)
		if err == nil {
			t.Fatal("expected unknown field error")
		}
		assertSafe(t, "canonize", err.Error())
	})

	t.Run("feature_unrepresentable_restore", func(t *testing.T) {
		t.Parallel()
		visible := []aipapi.Part{aipapi.TextPart("visible")}
		anchor := anchorFor(t, visible...)
		opaque := mustOpaqueJSON(t, `{"id":"`+secretID+`","type":"reasoning","summary":[{"type":"summary_text","text":"`+secretSum+`"}],"content":[{"type":"reasoning_text","text":"`+secretBody+`"}],"encrypted_content":"`+secretEnc+`"}`)
		art := reasoningpreservation.TurnArtifact{
			ID:            "art-1",
			Anchor:        anchor,
			SourceBackend: "chat-be",
			SourceModel:   "m",
			Reasoning: []reasoningpreservation.PlacedReasoning{{
				BeforeNonReasoningPart: 0,
				Part: aipapi.Part{
					Kind: aipapi.PartReasoning,
					Reasoning: &aipapi.ReasoningPart{
						Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
						Opaque:  opaque,
					},
				},
			}},
			ReasoningBytes: len(opaque),
		}
		got, err := reasoningpreservation.RestoreMissingReasoning(reasoningpreservation.RestoreInput{
			Action:            reasoningpreservation.ActionRestore,
			OnUnrepresentable: "reject",
			Call: &aipapi.Call{
				Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: visible}},
			},
			Artifacts:     []reasoningpreservation.TurnArtifact{art},
			Eligible:      true,
			ReplaySupport: aipapi.ReasoningReplaySupport{Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1}},
		})
		if err != nil {
			t.Fatalf("RestoreMissingReasoning: %v", err)
		}
		assertSafe(t, "restore_reason", got.ReasonCode)
		for _, o := range got.Outcomes {
			assertSafe(t, "restore_outcome", string(o))
		}
		diag, err := reasoningpreservation.FormatSafeDiagnostic(reasoningpreservation.OutcomeUnrepresentable, "rule-x", map[string]int{"count": 1})
		if err != nil {
			t.Fatalf("FormatSafeDiagnostic: %v", err)
		}
		assertSafe(t, "diagnostic", diag)
	})
}
