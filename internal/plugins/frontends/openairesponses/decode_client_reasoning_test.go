package openairesponses_test

import (
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Claim: a Codex-style tool-loop body carrying a reasoning item decodes to
// canonical PartReasoning (responses dialect), which is what requires the
// hard reasoning_replay capability the per-attempt policy downgrades.
// Source: req 1.1 + handoff Track 1 (decode.go:304).
// Mutant: decode reasoning as text -> FAIL (no reasoning_replay required).
func TestDecodeCreate_CodexReasoningHistoryRequiresReplay(t *testing.T) {
	t.Parallel()
	const body = `{"model":"gpt-4o-mini","input":[
		{"type":"function_call","call_id":"cA","name":"w","arguments":"{}"},
		{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"thought"}]},
		{"type":"function_call","call_id":"cB","name":"w","arguments":"{}"},
		{"type":"function_call_output","call_id":"cA","output":"ok-a"},
		{"type":"function_call_output","call_id":"cB","output":"ok-b"}
	]}`
	d, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, m := range d.Call.Messages {
		for _, p := range m.Parts {
			if p.Kind == aipapi.PartReasoning && p.Reasoning != nil &&
				p.Reasoning.Dialect == aipapi.ReasoningDialectOpenAIResponsesItemV1 {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("decoded history must carry responses-dialect PartReasoning: %+v", d.Call.Messages)
	}
	if !slices.Contains(aipapi.RequiredCapabilities(*d.Call), aipapi.CapabilityReasoningReplay) {
		t.Fatal("decoded history must require reasoning_replay")
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatalf("decoded call must stay valid: %v", err)
	}
}
