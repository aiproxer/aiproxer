package aipapi

import (
	"encoding/json"
	"strings"
)

// explicitCompletionNames is the minimal conservative canonical contract for
// explicit completion signals. Only unambiguous agent-harness completion tools
// are recognized; generic verbs like "finish"/"done" are intentionally excluded
// to avoid user-tool opt-out of verification. Versioning: new aliases may be
// added only after project-documented survey and spec update.
//
// Established aliases (v1):
//   - attempt_completion (primary, documented in spec example)
//   - attempt_complete   (common alias)
var explicitCompletionNames = map[string]struct{}{
	"attempt_completion": {},
	"attempt_complete":   {},
}

// IsExplicitCompletionToolName reports whether name, after trim and case-fold,
// matches a known explicit completion alias. Exact-match only, no substring
// or provider-specific logic.
func IsExplicitCompletionToolName(name string) bool {
	norm := strings.ToLower(strings.TrimSpace(name))
	if norm == "" {
		return false
	}
	_, ok := explicitCompletionNames[norm]
	return ok
}

// IsExplicitCompletionItem reports whether item is a valid explicit completion
// signal. Requirements for a valid signal:
//   - Kind is ToolCall
//   - ToolCall non-nil, CallID non-empty after trim, Name matches explicit set
//   - Arguments if present must be valid JSON (malformed -> false)
//
// Absent, nameless, unknown, or malformed items return false and fall back to
// normal semantic policy per requirements 5.7/7.1.
func IsExplicitCompletionItem(item Item) bool {
	if item.Kind != ItemKindToolCall {
		return false
	}
	if item.ToolCall == nil {
		return false
	}
	if strings.TrimSpace(item.ToolCall.CallID) == "" {
		return false
	}
	if !IsExplicitCompletionToolName(item.ToolCall.Name) {
		return false
	}
	if len(item.ToolCall.Arguments) > 0 && !json.Valid(item.ToolCall.Arguments) {
		return false
	}
	return true
}

// HasExplicitCompletion reports whether items contain at least one
// correlated completed explicit completion signal. A valid signal requires
// a completed ToolCall with an explicit name AND a matching completed
// ToolResult with the same CallID. Call-only, orphan, malformed, failed,
// or in-progress items return false and fall back to normal semantic policy.
func HasExplicitCompletion(items []Item) bool {
	explicitCalls := make(map[string]struct{})
	for _, it := range items {
		if it.Kind != ItemKindToolCall {
			continue
		}
		if !IsExplicitCompletionItem(it) {
			continue
		}
		if it.Status == ItemStatusInProgress || it.Status == ItemStatusIncomplete {
			continue
		}
		id := strings.TrimSpace(it.ToolCall.CallID)
		if id == "" {
			continue
		}
		explicitCalls[id] = struct{}{}
	}
	if len(explicitCalls) == 0 {
		return false
	}
	for _, it := range items {
		if it.Kind != ItemKindToolResult {
			continue
		}
		if it.ToolResult == nil {
			continue
		}
		if it.Status == ItemStatusInProgress || it.Status == ItemStatusIncomplete {
			continue
		}
		id := strings.TrimSpace(it.ToolResult.CallID)
		if _, ok := explicitCalls[id]; !ok {
			continue
		}
		// ToolResult must be well-formed (validated elsewhere) and present.
		// Require at least output or parts to count as executed evidence.
		if it.ToolResult.Output == "" && len(it.ToolResult.Parts) == 0 {
			continue
		}
		return true
	}
	return false
}
