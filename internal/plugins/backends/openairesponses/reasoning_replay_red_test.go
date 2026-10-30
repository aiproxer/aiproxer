package openairesponses_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	backend "github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func responsesReasoningPart(t *testing.T) aipapi.Part {
	t.Helper()
	opaque := json.RawMessage(`{"id":"r1","summary":[{"type":"summary_text","text":"s"}],"content":[{"type":"reasoning_text","text":"c"}],"encrypted_content":"enc"}`)
	return aipapi.Part{
		Kind: aipapi.PartReasoning,
		Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  opaque,
		},
	}
}

func TestParamsForCall_assistantReasoningResponsesDialect_RED(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "responses-reasoning-replay",
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{
					responsesReasoningPart(t),
					aipapi.TextPart("answer"),
				},
			},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatalf("RED: ParamsForCall must encode responses reasoning input item: %v", err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"type":"reasoning"`) {
		t.Fatalf("RED: params must include reasoning input item, got %s", s)
	}
	if !strings.Contains(s, `"id":"r1"`) || !strings.Contains(s, `"encrypted_content":"enc"`) {
		t.Fatalf("RED: reasoning input item must preserve id and encrypted_content, got %s", s)
	}
}
