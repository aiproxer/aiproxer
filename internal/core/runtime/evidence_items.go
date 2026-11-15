package runtime

import (
	"encoding/json"
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// evidenceItems returns the item trajectory that terminal-decision and pending
// action evidence read. Item-authority calls return aipapi.NormalizedItems
// unchanged.
//
// Message-authority calls (chat-completions) carry prior tool calls as PartJSON
// parts with ToolCallID and ToolName on assistant messages, and prior results as
// tool-role parts. aipapi.NormalizedItems projects those messages to message
// items only, so consumers that look for ItemKindToolCall or ItemKindToolResult
// never see them. This walk emits those parts as tool items at their message
// position and keeps every other part in a message item.
//
// The walk never fails. aipapi.ProjectLegacyToOrderedItems is fallible and
// rejects a legacy function_call that has no call id, which is a valid client
// history. A call with no legacy tool parts keeps its NormalizedItems IDs.
func evidenceItems(call aipapi.Call) []aipapi.Item {
	if call.HasItemAuthority() || !hasLegacyToolParts(call) {
		return aipapi.NormalizedItems(call)
	}
	var items []aipapi.Item
	seq := 0
	nextID := func(prefix string) string {
		id := fmt.Sprintf("%s-%d", prefix, seq)
		seq++
		return id
	}
	appendMessage := func(role aipapi.Role, parts []aipapi.Part) {
		if len(parts) == 0 {
			return
		}
		item := aipapi.NormalizedItems(aipapi.Call{Messages: []aipapi.Message{{Role: role, Parts: parts}}})[0]
		item.ID = nextID("msg")
		items = append(items, item)
	}
	walk := func(role aipapi.Role, parts []aipapi.Part) {
		var pending []aipapi.Part
		for _, p := range parts {
			if !isLegacyToolPart(role, p) {
				pending = append(pending, p)
				continue
			}
			appendMessage(role, pending)
			pending = nil
			item := legacyToolItem(role, p)
			if item.Kind == aipapi.ItemKindToolCall {
				item.ID = nextID("tool-call")
			} else {
				item.ID = nextID("tool-result")
			}
			items = append(items, item)
		}
		appendMessage(role, pending)
	}
	for _, msg := range call.Instructions {
		role := msg.Role
		if role == "" {
			role = aipapi.RoleSystem
		}
		walk(role, msg.Parts)
	}
	for _, msg := range call.Messages {
		walk(msg.Role, msg.Parts)
	}
	return items
}

func hasLegacyToolParts(call aipapi.Call) bool {
	for _, msg := range call.Instructions {
		for _, p := range msg.Parts {
			if isLegacyToolPart(msg.Role, p) {
				return true
			}
		}
	}
	for _, msg := range call.Messages {
		for _, p := range msg.Parts {
			if isLegacyToolPart(msg.Role, p) {
				return true
			}
		}
	}
	return false
}

// isLegacyToolPart reports whether a part is a message-authority tool call on an
// assistant message or a tool result on a tool-role message.
func isLegacyToolPart(role aipapi.Role, p aipapi.Part) bool {
	switch role {
	case aipapi.RoleAssistant:
		return p.Kind == aipapi.PartJSON && p.ToolCallID != "" && p.ToolName != ""
	case aipapi.RoleTool:
		return p.Kind == aipapi.PartToolResult
	default:
		return false
	}
}

// legacyToolItem builds the item for a part isLegacyToolPart accepted. Its ID is
// assigned by the caller. Tool results carry no name, as the chat tool message
// does not carry one.
func legacyToolItem(role aipapi.Role, p aipapi.Part) aipapi.Item {
	if role == aipapi.RoleTool {
		return aipapi.Item{
			Kind:   aipapi.ItemKindToolResult,
			Status: aipapi.ItemStatusCompleted,
			ToolResult: &aipapi.ToolResultItem{
				CallID: p.ToolCallID,
				Output: legacyToolResultOutput(p),
			},
		}
	}
	return aipapi.Item{
		Kind:   aipapi.ItemKindToolCall,
		Status: aipapi.ItemStatusCompleted,
		ToolCall: &aipapi.ToolCallItem{
			CallID:    p.ToolCallID,
			Name:      p.ToolName,
			Arguments: append(json.RawMessage(nil), p.Content...),
		},
	}
}

// legacyToolResultOutput returns the text of a chat tool result. A JSON string
// keeps its decoded text, and any other document keeps its bytes.
func legacyToolResultOutput(p aipapi.Part) string {
	if p.Text != "" {
		return p.Text
	}
	if len(p.Content) > 0 && p.Content[0] == '"' {
		var s string
		if err := json.Unmarshal(p.Content, &s); err == nil {
			return s
		}
	}
	return string(p.Content)
}
