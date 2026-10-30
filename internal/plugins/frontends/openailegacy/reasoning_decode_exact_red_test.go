package openailegacy_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestDecodeChat_reasoningContentPreservesExactText_RED(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [
    {"role": "user", "content": "hi"},
    {"role": "assistant", "content": "answer", "reasoning_content": " think "}
  ]
}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatalf("RED: decode must succeed: %v", err)
	}
	parts := reasoningParts(d.Call.Messages)
	if len(parts) != 1 || parts[0].Reasoning == nil {
		t.Fatalf("RED: expected one PartReasoning, got %+v", d.Call.Messages)
	}
	if parts[0].Reasoning.Text != " think " {
		t.Fatalf("RED: reasoning text must be preserved exactly, got %q", parts[0].Reasoning.Text)
	}
}

func TestDecodeChat_emptyReasoningContentWinsOverAlias_RED(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [
    {"role": "user", "content": "hi"},
    {"role": "assistant", "content": "answer", "reasoning_content": "", "reasoning": "alias"}
  ]
}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatalf("RED: decode must succeed: %v", err)
	}
	parts := reasoningParts(d.Call.Messages)
	if len(parts) != 0 {
		t.Fatalf("RED: present empty reasoning_content must win over alias (no PartReasoning), got %+v", parts)
	}
	for _, m := range d.Call.Messages {
		if m.Role != aipapi.RoleAssistant {
			continue
		}
		for _, p := range m.Parts {
			if p.Kind == aipapi.PartText && p.Text == "alias" {
				t.Fatal("RED: alias must not be converted into text either")
			}
		}
	}
}
