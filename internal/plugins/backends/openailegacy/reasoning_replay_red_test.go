package openailegacy_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	backend "github.com/aiproxer/aiproxer/internal/plugins/backends/openailegacy"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func reasoningPart(dialect aipapi.ReasoningDialect, text string) aipapi.Part {
	return aipapi.Part{
		Kind: aipapi.PartReasoning,
		Reasoning: &aipapi.ReasoningPart{
			Dialect: dialect,
			Text:    text,
		},
	}
}

func TestParamsForCall_assistantReasoningChatDialect_RED(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "reasoning-replay",
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{
					reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, "think"),
					aipapi.TextPart("answer"),
				},
			},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatalf("RED: ParamsForCall must encode assistant PartReasoning: %v", err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"reasoning_content":"think"`) {
		t.Fatalf("RED: chat replay body must include reasoning_content, got %s", s)
	}
}
