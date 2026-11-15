package runtime

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

// legacyToolCallConversation is a chat-completions conversation in message
// authority, built the way the openai-legacy frontend builds it: prior tool
// calls are PartJSON parts with ToolCallID and ToolName on assistant messages,
// and tool results are tool-role messages.
func legacyToolCallConversation() aipapi.Call {
	return aipapi.Call{
		ID: "legacy-evidence",
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("finish the migration")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "call-1", ToolName: "read_file", Content: json.RawMessage(`{"path":"a.go"}`)}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "call-1", Content: json.RawMessage(`"file body"`)}}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "call-2", ToolName: "attempt_completion", Content: json.RawMessage(`{}`)}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "call-2", Content: json.RawMessage(`"migration complete"`)}}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("again?")}},
		},
	}
}

// itemToolCallConversation is the same conversation in item authority. Tool
// results carry no name, as the legacy projection produces them for chat tools.
func itemToolCallConversation() aipapi.Call {
	message := func(id string, role aipapi.Role, text string) aipapi.Item {
		return aipapi.Item{
			Kind: aipapi.ItemKindMessage, ID: id, Status: aipapi.ItemStatusCompleted, Role: role,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: text}},
		}
	}
	toolCall := func(callID, name string) aipapi.Item {
		return aipapi.Item{
			Kind: aipapi.ItemKindToolCall, ID: "tool-call-" + callID, Status: aipapi.ItemStatusCompleted,
			ToolCall: &aipapi.ToolCallItem{CallID: callID, Name: name, Arguments: json.RawMessage(`{}`)},
		}
	}
	toolResult := func(callID, output string) aipapi.Item {
		return aipapi.Item{
			Kind: aipapi.ItemKindToolResult, ID: "tool-result-" + callID, Status: aipapi.ItemStatusCompleted,
			ToolResult: &aipapi.ToolResultItem{CallID: callID, Output: output},
		}
	}
	return aipapi.Call{
		ID: "legacy-evidence",
		Items: []aipapi.Item{
			message("msg-0", aipapi.RoleUser, "finish the migration"),
			toolCall("call-1", "read_file"),
			toolResult("call-1", `"file body"`),
			toolCall("call-2", "attempt_completion"),
			toolResult("call-2", `"migration complete"`),
			message("msg-5", aipapi.RoleUser, "again?"),
		},
	}
}

// Suspect: pendingActionNameIndex walks NormalizedItems for ItemKindToolCall.
// Message-authority prior calls must yield the same callID-to-name index.
func TestPendingActionNameIndex_legacyToolCallsMatchItemAuthority(t *testing.T) {
	t.Parallel()
	legacy := newResponsePipeline().pendingActionNameIndex(recvTurnFacts{baseline: legacyToolCallConversation()}, nil)
	items := newResponsePipeline().pendingActionNameIndex(recvTurnFacts{baseline: itemToolCallConversation()}, nil)
	if !maps.Equal(legacy, items) {
		t.Fatalf("name index: message authority = %v, item authority = %v", legacy, items)
	}
}

// evidenceToolAction is the tool-facing part of an action fact. Message items
// keep their own ids, so comparing across authorities uses kind, call, name and
// status only.
type evidenceToolAction struct {
	Kind   aipapi.ItemKind
	CallID string
	Name   string
	Status aipapi.ItemStatus
}

// Suspects: terminalDecisionActionFromItem and HasExplicitCompletion both read
// ItemKindToolCall and ItemKindToolResult from NormalizedItems. The same
// conversation must give the same actions and the same explicit-completion
// evidence in either authority.
func TestTerminalDecisionEvidence_legacyToolCallsMatchItemAuthority(t *testing.T) {
	t.Parallel()
	attempt := &attemptSession{bleg: b2bua.BLegRecord{BLegID: "b-leg-2", ALegID: "a-leg-1", Seq: 2}}
	legacy := projectTerminalDecisionEvidence(requestTerminalFacts{call: legacyToolCallConversation()}, attempt, newResponsePipeline())
	items := projectTerminalDecisionEvidence(requestTerminalFacts{call: itemToolCallConversation()}, attempt, newResponsePipeline())

	if legacy.ExplicitCompletion != items.ExplicitCompletion {
		t.Errorf("explicit completion: message authority = %v, item authority = %v", legacy.ExplicitCompletion, items.ExplicitCompletion)
	}
	if legacy.Objective != items.Objective || legacy.RecentText != items.RecentText {
		t.Errorf("text evidence differs: objective %q vs %q, recent %q vs %q",
			legacy.Objective, items.Objective, legacy.RecentText, items.RecentText)
	}
	gotTools := toolActionsOf(legacy)
	wantTools := toolActionsOf(items)
	if !slices.Equal(gotTools, wantTools) {
		t.Errorf("tool actions: message authority = %+v, item authority = %+v", gotTools, wantTools)
	}
}

func toolActionsOf(ev terminaldecision.Evidence) []evidenceToolAction {
	var out []evidenceToolAction
	for _, a := range ev.Actions[:ev.ActionCount] {
		if a.Kind == aipapi.ItemKindMessage {
			continue
		}
		out = append(out, evidenceToolAction{Kind: a.Kind, CallID: a.CallID, Name: a.Name, Status: a.Status})
	}
	return out
}
