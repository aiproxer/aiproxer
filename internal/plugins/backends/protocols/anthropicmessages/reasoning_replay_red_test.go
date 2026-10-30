package anthropicmessages_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/protocols/anthropicmessages"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func thinkingPart(text, signature string) aipapi.Part {
	return aipapi.Part{
		Kind: aipapi.PartReasoning,
		Reasoning: &aipapi.ReasoningPart{
			Dialect:   aipapi.ReasoningDialectAnthropicThinkingV1,
			Text:      text,
			Signature: signature,
		},
	}
}

func redactedPart(data string) aipapi.Part {
	opaque, _ := json.Marshal(map[string]string{"type": "redacted_thinking", "data": data})
	return aipapi.Part{
		Kind: aipapi.PartReasoning,
		Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectAnthropicRedactedThinkingV1,
			Opaque:  opaque,
		},
	}
}

func TestParamsForCall_assistantThinkingAndRedactedBlocks_RED(t *testing.T) {
	t.Parallel()
	toolOpaque := json.RawMessage(`{"q":"x"}`)
	call := aipapi.Call{
		ID: "anthropic-reasoning-replay",
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{
					thinkingPart("plan", "sig-a"),
					aipapi.TextPart("between"),
					{Kind: aipapi.PartJSON, ToolCallID: "toolu_1", ToolName: "lookup", Content: toolOpaque},
					redactedPart("opaque-blob"),
					aipapi.TextPart("answer"),
				},
			},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
	p, err := anthropicmessages.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatalf("RED: ParamsForCall must encode thinking/redacted_thinking blocks: %v", err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"type":"thinking"`) || !strings.Contains(s, `"thinking":"plan"`) || !strings.Contains(s, `"signature":"sig-a"`) {
		t.Fatalf("RED: must encode signed thinking block, got %s", s)
	}
	if !strings.Contains(s, `"type":"tool_use"`) || !strings.Contains(s, `"id":"toolu_1"`) {
		t.Fatalf("RED: must preserve ordered tool_use among thinking blocks, got %s", s)
	}
	if !strings.Contains(s, `"type":"redacted_thinking"`) || !strings.Contains(s, `"data":"opaque-blob"`) {
		t.Fatalf("RED: must encode redacted_thinking block, got %s", s)
	}
}

func TestParamsForCall_userToolResultBlock_RED(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "anthropic-tool-result-user",
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{
					thinkingPart("plan", "sig-a"),
					aipapi.TextPart("checking"),
					{Kind: aipapi.PartJSON, ToolCallID: "toolu_1", ToolName: "lookup", Content: json.RawMessage(`{"q":"x"}`)},
				},
			},
			{
				Role: aipapi.RoleUser,
				Parts: []aipapi.Part{{
					Kind:       aipapi.PartToolResult,
					ToolCallID: "toolu_1",
					Text:       `{"ok":true}`,
				}},
			},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
	p, err := anthropicmessages.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatalf("RED: ParamsForCall must encode user tool_result: %v", err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"type":"tool_result"`) || !strings.Contains(s, `"tool_use_id":"toolu_1"`) {
		t.Fatalf("RED: must encode tool_result block, got_bytes=%d", len(s))
	}
	if !strings.Contains(s, `"type":"thinking"`) || !strings.Contains(s, `"type":"tool_use"`) {
		t.Fatalf("RED: must keep thinking/tool_use with tool_result, got_bytes=%d", len(s))
	}
}

func TestParamsForCall_assistantThinkingEmptySignatureNotFabricated_RED(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "anthropic-empty-signature",
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{
					thinkingPart("plan", ""),
					aipapi.TextPart("answer"),
				},
			},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
	p, err := anthropicmessages.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatalf("RED: ParamsForCall must encode thinking with empty signature: %v", err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"type":"thinking"`) {
		t.Fatalf("RED: must encode thinking block, got %s", s)
	}
	if strings.Contains(s, `"signature":"`) && !strings.Contains(s, `"signature":""`) {
		t.Fatalf("RED: empty signature must stay empty (no fabrication), got %s", s)
	}
}
