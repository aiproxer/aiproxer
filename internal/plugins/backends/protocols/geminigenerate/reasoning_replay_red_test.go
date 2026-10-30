package geminigenerate_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/protocols/geminigenerate"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func requireReasoningReplayUnsupported(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("RED: Gemini must fail closed for reasoning replay")
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "must-not-drop") || strings.Contains(msg, "must-not-convert") {
		t.Fatalf("Gemini must not convert reasoning into text payload: %v", err)
	}
	if !strings.Contains(msg, "reasoning replay") && !strings.Contains(msg, "reasoning_replay") {
		t.Fatalf("RED: Gemini unsupported error must classify reasoning-replay incompatibility (want substring %q or %q), got %v", "reasoning replay", "reasoning_replay", err)
	}
}

func TestStreamParamsForCall_assistantReasoningReplayUnsupported_RED(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "gemini-reasoning-unsupported",
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{
					{
						Kind: aipapi.PartReasoning,
						Reasoning: &aipapi.ReasoningPart{
							Dialect: aipapi.ReasoningDialectOpenAIChatTextV1,
							Text:    "must-not-drop",
						},
					},
					aipapi.TextPart("answer"),
				},
			},
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gemini-2.0-flash"}}
	_, err := geminigenerate.StreamParamsForCall(&call, cand)
	requireReasoningReplayUnsupported(t, err)
}

func TestStreamParamsForCall_noPositiveReasoningReplayGolden_RED(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleAssistant,
			Parts: []aipapi.Part{{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: "x"}}},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gemini-2.0-flash"}}
	_, err := geminigenerate.StreamParamsForCall(&call, cand)
	requireReasoningReplayUnsupported(t, err)
}
