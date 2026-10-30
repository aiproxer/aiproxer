package hooks_test

import (
	"strings"
	"testing"

	corehooks "github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestValidateEventAfterResponseHook_rejectsOversizedDelta(t *testing.T) {
	t.Parallel()
	ev := &aipapi.Event{
		Kind:  aipapi.EventTextDelta,
		Delta: strings.Repeat("x", aipapi.MaxEventDeltaBytes+1),
	}
	err := corehooks.ValidateEventAfterResponseHook("test-hook", ev)
	if err == nil {
		t.Fatal("expected HookMutationError for oversized delta")
	}
}

func TestValidateEventAfterResponseHook_acceptsReasoningSignatureDelta(t *testing.T) {
	t.Parallel()
	ev := &aipapi.Event{
		Kind:      aipapi.EventReasoningSignatureDelta,
		Signature: "sig-plan",
	}
	if err := corehooks.ValidateEventAfterResponseHook("test-hook", ev); err != nil {
		t.Fatalf("RED: SignatureDelta must be a known post-hook event kind: %v", err)
	}
}

func TestValidateEventAfterResponseHook_acceptsReasoningOpaqueDelta(t *testing.T) {
	t.Parallel()
	ev := &aipapi.Event{
		Kind:   aipapi.EventReasoningOpaqueDelta,
		Opaque: []byte(`{"type":"redacted_thinking","data":"x"}`),
	}
	if err := corehooks.ValidateEventAfterResponseHook("test-hook", ev); err != nil {
		t.Fatalf("RED: OpaqueDelta must be a known post-hook event kind: %v", err)
	}
}

func TestValidateEventAfterResponseHook_acceptsReasoningPart(t *testing.T) {
	t.Parallel()
	ev := &aipapi.Event{
		Kind: aipapi.EventReasoningPart,
		Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  []byte(`{"id":"rs_1","type":"reasoning","summary":[]}`),
		},
	}
	if err := corehooks.ValidateEventAfterResponseHook("parts-noop", ev); err != nil {
		t.Fatalf("reasoning_part must be a known post-hook event kind: %v", err)
	}
}
