package openairesponses_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	backend "github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestParamsForCall_reasoningMissingIDFailsClosed_RED(t *testing.T) {
	t.Parallel()
	opaque := json.RawMessage(`{"summary":[{"type":"summary_text","text":"s"}],"encrypted_content":"enc"}`)
	call := aipapi.Call{
		ID: "responses-missing-id",
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{
					{
						Kind: aipapi.PartReasoning,
						Reasoning: &aipapi.ReasoningPart{
							Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
							Opaque:  opaque,
						},
					},
					aipapi.TextPart("answer"),
				},
			},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	_, err := backend.ParamsForCall(&call, cand)
	if err == nil {
		t.Fatal("RED: missing reasoning item id must fail closed")
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "rs_replay") {
		t.Fatalf("RED: must not fabricate rs_replay id, got %v", err)
	}
	if !strings.Contains(msg, "id") {
		t.Fatalf("RED: error must mention missing id, got %v", err)
	}
}
