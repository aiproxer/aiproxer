package reasoningpreservation_test

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestPhase3_itemAuthorityIsNotMutatedByMessagePlacementRestore(t *testing.T) {
	t.Parallel()
	reasoning := reasoningPart(aipapi.ReasoningDialectOpenAIResponsesItemV1, "", "", mustOpaqueJSON(t, `{"id":"r1","type":"reasoning","summary":[],"encrypted_content":"stored"}`))
	callPart := jsonPart(`{"type":"function_call","call_id":"call-1","name":"lookup","arguments":"{}"}`)
	callPart.ToolCallID = "call-1"
	callPart.ToolName = "lookup"
	call := aipapi.Call{Items: []aipapi.Item{
		{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{CallID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{}`)}},
	}}
	before := cloneCall(t, call)
	got, err := reasoningpreservation.RestoreMissingReasoning(reasoningpreservation.RestoreInput{
		Action:        reasoningpreservation.ActionRestore,
		Call:          &call,
		Artifacts:     []reasoningpreservation.TurnArtifact{turnArtifact("stored", anchorFor(t, callPart), placedReasoning(0, reasoning))},
		ReplaySupport: exactResponsesSupport(),
		Eligible:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mutated || got.RestoredCount != 0 || len(call.Items) != len(before.Items) || call.Items[0].Kind != before.Items[0].Kind {
		t.Fatalf("item authority must not be rewritten by message placement restore: result=%+v items=%+v", got, call.Items)
	}
}

func TestPhase3_legacyTrajectoryMutationCasesNeverGuess(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		call        aipapi.Call
		artifacts   []reasoningpreservation.TurnArtifact
		wantOutcome reasoningpreservation.SafeOutcome
	}{
		{
			name:        "edited trajectory is unmatched",
			call:        aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("edited")}}}},
			artifacts:   []reasoningpreservation.TurnArtifact{turnArtifact("original", anchorFor(t, aipapi.TextPart("original")), placedReasoning(0, reasoningPart(aipapi.ReasoningDialectOpenAIResponsesItemV1, "", "", mustOpaqueJSON(t, `{"id":"r-edit","type":"reasoning","summary":[]}`))))},
			wantOutcome: reasoningpreservation.OutcomeUnmatched,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := reasoningpreservation.RestoreMissingReasoning(reasoningpreservation.RestoreInput{
				Action:        reasoningpreservation.ActionRestore,
				Call:          &tc.call,
				Artifacts:     tc.artifacts,
				ReplaySupport: exactResponsesSupport(),
				Eligible:      true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.Mutated || len(got.Outcomes) != 1 || got.Outcomes[0] != tc.wantOutcome {
				t.Fatalf("trajectory decision = %+v", got)
			}
		})
	}
}

func TestPhase3_itemAuthorityClientReasoningIsNeverDuplicated(t *testing.T) {
	t.Parallel()
	reasoning := reasoningPart(aipapi.ReasoningDialectOpenAIResponsesItemV1, "", "", mustOpaqueJSON(t, `{"id":"r1","type":"reasoning","summary":[],"encrypted_content":"client"}`))
	call := aipapi.Call{Items: []aipapi.Item{
		{Kind: aipapi.ItemKindReasoning, Reasoning: &aipapi.ReasoningItem{Reasoning: reasoning.Reasoning}},
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleAssistant, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "done"}}},
	}}
	anchor := anchorFor(t, aipapi.TextPart("done"))
	got, err := reasoningpreservation.RestoreMissingReasoning(reasoningpreservation.RestoreInput{
		Action:        reasoningpreservation.ActionRestore,
		Call:          &call,
		Artifacts:     []reasoningpreservation.TurnArtifact{turnArtifact("same", anchor, placedReasoning(0, reasoning))},
		ReplaySupport: exactResponsesSupport(),
		Eligible:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mutated || got.RestoredCount != 0 || len(call.Items) != 2 {
		t.Fatalf("client reasoning must remain exact and singular: result=%+v items=%+v", got, call.Items)
	}
}

func TestPhase3_restoreLegacyMultipleReasoningAroundFunctionCallAndToolOutput(t *testing.T) {
	t.Parallel()
	r1 := reasoningPart(aipapi.ReasoningDialectOpenAIResponsesItemV1, "", "", mustOpaqueJSON(t, `{"id":"r1","type":"reasoning","summary":[],"encrypted_content":"one"}`))
	r2 := reasoningPart(aipapi.ReasoningDialectOpenAIResponsesItemV1, "", "", mustOpaqueJSON(t, `{"id":"r2","type":"reasoning","summary":[],"encrypted_content":"two"}`))
	text := aipapi.TextPart("before call")
	callPart := jsonPart(`{}`)
	callPart.ToolCallID = "call-1"
	callPart.ToolName = "lookup"
	toolOutput := aipapi.Part{Kind: aipapi.PartToolResult, ToolCallID: "call-1", Content: json.RawMessage(`"result"`)}
	call := aipapi.Call{Messages: []aipapi.Message{
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{text, callPart}},
		{Role: aipapi.RoleTool, Parts: []aipapi.Part{toolOutput}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("after tool")}},
	}}
	artifacts := []reasoningpreservation.TurnArtifact{
		turnArtifact("first", anchorFor(t, text, callPart), placedReasoning(0, r1), placedReasoning(1, r2)),
		turnArtifact("second", anchorFor(t, aipapi.TextPart("after tool")), placedReasoning(0, reasoningPart(aipapi.ReasoningDialectOpenAIResponsesItemV1, "", "", mustOpaqueJSON(t, `{"id":"r3","type":"reasoning","summary":[],"encrypted_content":"three"}`)))),
	}
	got, err := reasoningpreservation.RestoreMissingReasoning(reasoningpreservation.RestoreInput{
		Action:        reasoningpreservation.ActionRestore,
		Call:          &call,
		Artifacts:     artifacts,
		ReplaySupport: exactResponsesSupport(),
		Eligible:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.RestoredCount != 2 {
		t.Fatalf("restore result = %+v", got)
	}
	parts := call.Messages[0].Parts
	if len(parts) != 4 || parts[0].Kind != aipapi.PartReasoning || parts[1].Kind != aipapi.PartText || parts[2].Kind != aipapi.PartReasoning || parts[3].Kind != aipapi.PartJSON {
		t.Fatalf("reasoning/function-call order = %+v", parts)
	}
	if len(call.Messages[2].Parts) != 2 || call.Messages[2].Parts[0].Kind != aipapi.PartReasoning {
		t.Fatalf("later reasoning after tool output = %+v", call.Messages[2].Parts)
	}
}
