package openailegacy_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	backend "github.com/aiproxer/aiproxer/internal/plugins/backends/openailegacy"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestParamsForCall_assistantReasoningWithToolCall_RED(t *testing.T) {
	t.Parallel()
	toolJSON, err := json.Marshal(map[string]any{
		"id":   "call_1",
		"type": "function",
		"function": map[string]any{
			"name":      "lookup",
			"arguments": `{"q":"x"}`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	call := aipapi.Call{
		ID: "reasoning-tool-replay",
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{
					{
						Kind: aipapi.PartReasoning,
						Reasoning: &aipapi.ReasoningPart{
							Dialect: aipapi.ReasoningDialectOpenAIChatTextV1,
							Text:    "think",
						},
					},
					aipapi.TextPart("checking"),
					{Kind: aipapi.PartJSON, ToolCallID: "call_1", ToolName: "lookup", Content: []byte(`{"q":"x"}`)},
				},
			},
			{
				Role: aipapi.RoleTool,
				Parts: []aipapi.Part{{
					Kind:       aipapi.PartToolResult,
					ToolCallID: "call_1",
					Content:    []byte(`{"ok":true}`),
				}},
			},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "moonshot-v1-8k"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatalf("RED: ParamsForCall must encode reasoning+tool assistant: %v", err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, need := range []string{
		`"reasoning_content":"think"`,
		`"content":"checking"`,
		`"id":"call_1"`,
		`"name":"lookup"`,
		`"arguments":"{\"q\":\"x\"}"`,
	} {
		if !strings.Contains(s, need) {
			t.Fatalf("RED: body missing %s; got %s", need, s)
		}
	}
	_ = toolJSON
}
