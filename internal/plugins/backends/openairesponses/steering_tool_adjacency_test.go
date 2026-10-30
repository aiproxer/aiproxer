package openairesponses_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	backend "github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Invariant (fix-steering-tool-adjacency, #1102 follow-up): an AfterMessage
// steering overlay anchored to an assistant tool-call message must be projected
// AFTER the contiguous tool-result messages answering that call, never between
// the call and its result. Chat-family, Anthropic-messages and strict Responses
// upstreams reject a split pair, and this test drives the strict-Responses wire
// shape (function_call immediately followed by function_call_output) through the
// real ParamsForCall projection.
//
// #1102 kept the inverted order as a characterization while production anchors
// observed were StablePrefix / terminal-user only. Projection now owns the
// invariant for every anchor a pkg/aipsdk/steering producer may store.
func TestSteeringAnchoredToToolCallNeverSplitsCallFromResult(t *testing.T) {
	t.Parallel()
	toolCall := aipapi.Part{Kind: aipapi.PartJSON, ToolCallID: "c1", ToolName: "w", Content: []byte(`{"type":"function_call","call_id":"c1","name":"w","arguments":"{}"}`)}
	assistantMsg := aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{toolCall}}
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("go")}},
			assistantMsg,
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "c1", Content: []byte(`"ok"`)}}},
		},
		Tools: []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
	}
	anchorID, err := conversationprojection.MessageIdentityOf(assistantMsg)
	if err != nil {
		t.Fatalf("anchor identity: %v", err)
	}
	snap := conversationprojection.Snapshot{
		StateRevision: 1,
		Steering: []conversationprojection.Overlay{
			{
				OverlayID:   "tool-adjacency-probe",
				Active:      true,
				Revision:    1,
				SlotOrdinal: 1,
				Placement: conversationprojection.Placement{
					Kind:   conversationprojection.PlacementAfterMessage,
					Anchor: &conversationprojection.MessageAnchor{Identity: anchorID, Occurrence: 1},
				},
				AnchorMissingPolicy: conversationprojection.AnchorFailClosed,
				Message:             conversationprojection.OverlayMessage{Role: aipapi.RoleDeveloper, Text: "tool-adjacency-steering"},
			},
		},
	}
	projected, _, err := conversationprojection.Project(call, snap)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	// The tool result stays adjacent to its call (index 2) and steering lands after it.
	if len(projected.Messages) != 4 {
		t.Fatalf("projected messages=%d, want 4", len(projected.Messages))
	}
	if projected.Messages[2].Role != aipapi.RoleTool {
		t.Fatalf("tool result must immediately follow its call: %+v", projected.Messages)
	}
	if projected.Messages[3].Role != aipapi.RoleDeveloper {
		t.Fatalf("steering must land after the tool result: %+v", projected.Messages)
	}
	// And the wire must reflect it: user, function_call, function_call_output, trapped.
	params, err := backend.ParamsForCall(&projected, routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}})
	if err != nil {
		t.Fatalf("ParamsForCall: %v", err)
	}
	items := params.Input.OfInputItemList
	if len(items) != 4 {
		t.Fatalf("input items=%d, want 4 (user, call, output, trapped)", len(items))
	}
	if items[1].OfFunctionCall == nil || items[2].OfFunctionCallOutput == nil {
		t.Fatalf("function_call and its output must stay adjacent on the wire")
	}
	if items[3].OfFunctionCall != nil || items[3].OfFunctionCallOutput != nil {
		t.Fatalf("steering must never occupy a call/output slot, got %+v", items[3])
	}
	if items[3].OfMessage == nil || string(items[3].OfMessage.Role) != "developer" {
		t.Fatalf("trailing item must be the trapped developer steering, got %+v", items[3])
	}
}
