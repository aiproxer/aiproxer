package anthropic_test

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func assistantReasoningParts(msgs []aipapi.Message) []aipapi.Part {
	var out []aipapi.Part
	for _, m := range msgs {
		if m.Role != aipapi.RoleAssistant {
			continue
		}
		for _, p := range m.Parts {
			if p.Kind == aipapi.PartReasoning {
				out = append(out, p)
			}
		}
	}
	return out
}

func TestDecodeMessage_assistantThinkingAndRedactedInterleaved_RED(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "messages": [
    {"role":"user","content":"plan"},
    {
      "role":"assistant",
      "content": [
        {"type":"thinking","thinking":"think-a","signature":"sig-a"},
        {"type":"text","text":"between"},
        {"type":"tool_use","id":"toolu_1","name":"lookup","input":{"q":"x"}},
        {"type":"redacted_thinking","data":"opaque-blob"},
        {"type":"text","text":"answer"}
      ]
    }
  ]
}`)
	d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:claude-3-5-haiku-20241022",
	})
	if err != nil {
		t.Fatalf("RED: decode interleaved thinking blocks must succeed: %v", err)
	}
	if len(d.Call.Messages) != 2 || d.Call.Messages[1].Role != aipapi.RoleAssistant {
		t.Fatalf("RED: messages: %+v", d.Call.Messages)
	}
	parts := d.Call.Messages[1].Parts
	if len(parts) != 5 {
		t.Fatalf("RED: expected five ordered parts (thinking/text/tool/redacted/text), got %d: %+v", len(parts), parts)
	}
	if parts[0].Kind != aipapi.PartReasoning || parts[0].Reasoning == nil {
		t.Fatalf("RED: parts[0] must be PartReasoning, got %+v", parts[0])
	}
	if parts[0].Reasoning.Dialect != aipapi.ReasoningDialectAnthropicThinkingV1 {
		t.Fatalf("RED: parts[0] dialect = %q, want %q", parts[0].Reasoning.Dialect, aipapi.ReasoningDialectAnthropicThinkingV1)
	}
	if parts[0].Reasoning.Text != "think-a" || parts[0].Reasoning.Signature != "sig-a" {
		t.Fatalf("RED: thinking block payload = %+v", parts[0].Reasoning)
	}
	if parts[1].Kind != aipapi.PartText || parts[1].Text != "between" {
		t.Fatalf("RED: parts[1] = %+v", parts[1])
	}
	if parts[2].Kind != aipapi.PartJSON || parts[2].ToolCallID != "toolu_1" || parts[2].ToolName != "lookup" {
		t.Fatalf("RED: parts[2] must preserve tool_use order as PartJSON, got %+v", parts[2])
	}
	if parts[3].Kind != aipapi.PartReasoning || parts[3].Reasoning == nil {
		t.Fatalf("RED: parts[3] must be PartReasoning, got %+v", parts[3])
	}
	if parts[3].Reasoning.Dialect != aipapi.ReasoningDialectAnthropicRedactedThinkingV1 {
		t.Fatalf("RED: parts[3] dialect = %q, want %q", parts[3].Reasoning.Dialect, aipapi.ReasoningDialectAnthropicRedactedThinkingV1)
	}
	var redacted struct {
		Type string `json:"type"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal(parts[3].Reasoning.Opaque, &redacted); err != nil {
		t.Fatalf("RED: redacted opaque must be valid JSON: %v", err)
	}
	if redacted.Type != "redacted_thinking" || redacted.Data != "opaque-blob" {
		t.Fatalf("RED: redacted opaque = %+v", redacted)
	}
	if parts[4].Kind != aipapi.PartText || parts[4].Text != "answer" {
		t.Fatalf("RED: parts[4] = %+v", parts[4])
	}
	if got := assistantReasoningParts(d.Call.Messages); len(got) != 2 {
		t.Fatalf("RED: expected two reasoning parts, got %d", len(got))
	}
}
