package reasoningpreservation_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func classifyTurns(t *testing.T, messages []aipapi.Message, artifacts []reasoningpreservation.TurnArtifact) []reasoningpreservation.ClassifiedTurn {
	t.Helper()
	got, err := reasoningpreservation.ClassifyAssistantTurns(messages, artifacts)
	redNotImplemented(t, err, "ClassifyAssistantTurns must be implemented")
	if err != nil {
		t.Fatalf("ClassifyAssistantTurns: %v", err)
	}
	return got
}

func anchorFor(t *testing.T, parts ...aipapi.Part) [32]byte {
	t.Helper()
	return computeAnchor(t, assistantMsg(parts...))
}

func TestClassifyAssistantTurns_missing(t *testing.T) {
	t.Parallel()
	parts := []aipapi.Part{aipapi.TextPart("visible answer")}
	anchor := anchorFor(t, parts...)
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("art-1", anchor, placedReasoning(0,
			reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "stored-thought", "", nil))),
	}
	got := classifyTurns(t, []aipapi.Message{assistantMsg(parts...)}, artifacts)
	if len(got) != 1 || got[0].Classification != reasoningpreservation.ClassMissing {
		t.Fatalf("got=%+v want missing", got)
	}
}

func TestClassifyAssistantTurns_preserved(t *testing.T) {
	t.Parallel()
	stored := reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "stored-thought", "", nil)
	parts := []aipapi.Part{
		stored,
		aipapi.TextPart("visible answer"),
	}
	anchor := anchorFor(t, aipapi.TextPart("visible answer"))
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("art-1", anchor, placedReasoning(0, stored)),
	}
	got := classifyTurns(t, []aipapi.Message{assistantMsg(parts...)}, artifacts)
	if len(got) != 1 || got[0].Classification != reasoningpreservation.ClassPreserved {
		t.Fatalf("got=%+v want preserved", got)
	}
}

func TestClassifyAssistantTurns_conflictingContent(t *testing.T) {
	t.Parallel()
	anchor := anchorFor(t, aipapi.TextPart("visible answer"))
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("art-1", anchor, placedReasoning(0,
			reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "stored-thought", "", nil))),
	}
	client := assistantMsg(
		reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "different-thought", "", nil),
		aipapi.TextPart("visible answer"),
	)
	got := classifyTurns(t, []aipapi.Message{client}, artifacts)
	if len(got) != 1 || got[0].Classification != reasoningpreservation.ClassConflicting {
		t.Fatalf("got=%+v want conflicting content", got)
	}
}

func TestClassifyAssistantTurns_conflictingPlacement(t *testing.T) {
	t.Parallel()
	stored := reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "stored-thought", "", nil)
	anchor := anchorFor(t, aipapi.TextPart("before"), aipapi.TextPart("after"))
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact(
			"art-1", anchor,
			placedReasoning(0, stored),
		),
	}
	client := assistantMsg(
		aipapi.TextPart("before"),
		stored,
		aipapi.TextPart("after"),
	)
	got := classifyTurns(t, []aipapi.Message{client}, artifacts)
	if len(got) != 1 || got[0].Classification != reasoningpreservation.ClassConflicting {
		t.Fatalf("got=%+v want conflicting placement", got)
	}
}

func TestClassifyAssistantTurns_conflictingDialect(t *testing.T) {
	t.Parallel()
	anchor := anchorFor(t, aipapi.TextPart("visible answer"))
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("art-1", anchor, placedReasoning(0,
			reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "same-text", "", nil))),
	}
	client := assistantMsg(
		reasoningPart(aipapi.ReasoningDialectAnthropicThinkingV1, "same-text", "", nil),
		aipapi.TextPart("visible answer"),
	)
	got := classifyTurns(t, []aipapi.Message{client}, artifacts)
	if len(got) != 1 || got[0].Classification != reasoningpreservation.ClassConflicting {
		t.Fatalf("got=%+v want conflicting dialect", got)
	}
}

func TestClassifyAssistantTurns_conflictingSignature(t *testing.T) {
	t.Parallel()
	anchor := anchorFor(t, aipapi.TextPart("visible answer"))
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("art-1", anchor, placedReasoning(0,
			reasoningPart(aipapi.ReasoningDialectAnthropicThinkingV1, "think", "sig-a", nil))),
	}
	client := assistantMsg(
		reasoningPart(aipapi.ReasoningDialectAnthropicThinkingV1, "think", "sig-b", nil),
		aipapi.TextPart("visible answer"),
	)
	got := classifyTurns(t, []aipapi.Message{client}, artifacts)
	if len(got) != 1 || got[0].Classification != reasoningpreservation.ClassConflicting {
		t.Fatalf("got=%+v want conflicting signature", got)
	}
}

func TestClassifyAssistantTurns_conflictingOpaque(t *testing.T) {
	t.Parallel()
	anchor := anchorFor(t, aipapi.TextPart("visible answer"))
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("art-1", anchor, placedReasoning(0,
			reasoningPart(aipapi.ReasoningDialectAnthropicRedactedThinkingV1, "", "", mustOpaqueJSON(t, `{"a":1}`)))),
	}
	client := assistantMsg(
		reasoningPart(aipapi.ReasoningDialectAnthropicRedactedThinkingV1, "", "", mustOpaqueJSON(t, `{"a":2}`)),
		aipapi.TextPart("visible answer"),
	)
	got := classifyTurns(t, []aipapi.Message{client}, artifacts)
	if len(got) != 1 || got[0].Classification != reasoningpreservation.ClassConflicting {
		t.Fatalf("got=%+v want conflicting opaque", got)
	}
}

func TestClassifyAssistantTurns_ambiguousDuplicateMessages(t *testing.T) {
	t.Parallel()
	parts := []aipapi.Part{aipapi.TextPart("duplicate visible")}
	anchor := anchorFor(t, parts...)
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("art-1", anchor, placedReasoning(0,
			reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "thought-1", "", nil))),
		turnArtifact("art-2", anchor, placedReasoning(0,
			reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "thought-2", "", nil))),
	}
	messages := []aipapi.Message{
		assistantMsg(parts...),
		assistantMsg(parts...),
	}
	got := classifyTurns(t, messages, artifacts)
	for _, c := range got {
		if c.Classification != reasoningpreservation.ClassAmbiguous {
			t.Fatalf("duplicate association must be ambiguous, got=%+v", got)
		}
	}
}

func TestClassifyAssistantTurns_unmatched(t *testing.T) {
	t.Parallel()
	got := classifyTurns(t, []aipapi.Message{
		assistantMsg(aipapi.TextPart("no artifact for this")),
	}, []reasoningpreservation.TurnArtifact{
		turnArtifact("art-1", anchorFor(t, aipapi.TextPart("other")), placedReasoning(0,
			reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "x", "", nil))),
	})
	if len(got) != 1 || got[0].Classification != reasoningpreservation.ClassUnmatched {
		t.Fatalf("got=%+v want unmatched", got)
	}
}

func TestClassifyAssistantTurns_ignoresNonAssistantMessages(t *testing.T) {
	t.Parallel()
	anchor := anchorFor(t, aipapi.TextPart("assistant visible"))
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("art-1", anchor, placedReasoning(0,
			reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "stored", "", nil))),
	}
	messages := []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("question")}},
		assistantMsg(aipapi.TextPart("assistant visible")),
	}
	got := classifyTurns(t, messages, artifacts)
	if len(got) != 1 {
		t.Fatalf("expected one assistant classification, got=%+v", got)
	}
}
