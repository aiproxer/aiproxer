package aipapi_test

import (
	"math"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestCallReasoningPayloadBytes_aggregatesReasoningOnly(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleAssistant,
			Parts: []aipapi.Part{
				aipapi.TextPart("ignored"),
				{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Text: "ab", Signature: "cd"}},
			},
		}},
		Instructions: []aipapi.Message{{
			Role: aipapi.RoleAssistant,
			Parts: []aipapi.Part{
				{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Opaque: []byte("ef")}},
			},
		}},
	}
	if got := aipapi.CallReasoningPayloadBytes(call); got != 6 {
		t.Fatalf("got %d want 6", got)
	}
	if got := aipapi.CallReasoningPayloadBytes(nil); got != 0 {
		t.Fatalf("nil got %d", got)
	}
}

func TestSaturatingAddInt64_clamps(t *testing.T) {
	t.Parallel()
	if got := aipapi.SaturatingAddInt64(math.MaxInt64-1, 5); got != math.MaxInt64 {
		t.Fatalf("got %d", got)
	}
}
