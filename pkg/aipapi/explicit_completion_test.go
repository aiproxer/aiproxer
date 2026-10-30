package aipapi_test

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsExplicitCompletionToolName_Normalization(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"lowercase attempt_completion", "attempt_completion", true},
		{"case-fold Attempt_Completion", "Attempt_Completion", true},
		{"whitespace trimmed attempt_complete", "  attempt_complete  ", true},
		{"uppercase ATTEMPT_COMPLETE", "ATTEMPT_COMPLETE", true},
		{"generic finish rejected", "finish", false},
		{"task_complete rejected (conservative)", "task_complete", false},
		{"complete_task rejected", "complete_task", false},
		{"task_completion rejected", "task_completion", false},
		{"empty", "", false},
		{"whitespace only", "   ", false},
		{"unknown tool", "read", false},
		{"prefix not inferred", "attempt_completion_extra", false},
		{"substring not inferred", "my_attempt_completion", false},
		{"done not in set", "done", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, aipapi.IsExplicitCompletionToolName(tc.input))
		})
	}
}

func TestIsExplicitCompletionItem_ValidAndMalformed(t *testing.T) {
	t.Parallel()
	valid := aipapi.Item{
		Kind: aipapi.ItemKindToolCall,
		ToolCall: &aipapi.ToolCallItem{
			CallID:    "call-1",
			Name:      "attempt_completion",
			Arguments: json.RawMessage(`{"result":"done"}`),
		},
	}
	assert.True(t, aipapi.IsExplicitCompletionItem(valid), "valid explicit name should be true (name-only predicate)")

	malformedCallID := aipapi.Item{
		Kind: aipapi.ItemKindToolCall,
		ToolCall: &aipapi.ToolCallItem{
			CallID: "   ",
			Name:   "attempt_completion",
		},
	}
	assert.False(t, aipapi.IsExplicitCompletionItem(malformedCallID), "missing call ID is malformed -> false")

	malformedArgs := aipapi.Item{
		Kind: aipapi.ItemKindToolCall,
		ToolCall: &aipapi.ToolCallItem{
			CallID:    "call-1",
			Name:      "attempt_completion",
			Arguments: json.RawMessage(`{invalid json`),
		},
	}
	assert.False(t, aipapi.IsExplicitCompletionItem(malformedArgs), "invalid JSON args is malformed -> false")

	unknownName := aipapi.Item{
		Kind:     aipapi.ItemKindToolCall,
		ToolCall: &aipapi.ToolCallItem{CallID: "c1", Name: "read"},
	}
	assert.False(t, aipapi.IsExplicitCompletionItem(unknownName))

	wrongKind := aipapi.Item{
		Kind:     aipapi.ItemKindMessage,
		Role:     aipapi.RoleAssistant,
		ToolCall: &aipapi.ToolCallItem{CallID: "c1", Name: "attempt_completion"},
	}
	assert.False(t, aipapi.IsExplicitCompletionItem(wrongKind))

	nilToolCall := aipapi.Item{Kind: aipapi.ItemKindToolCall}
	assert.False(t, aipapi.IsExplicitCompletionItem(nilToolCall))

	// Case-folded name should still be valid
	caseFolded := aipapi.Item{
		Kind:     aipapi.ItemKindToolCall,
		ToolCall: &aipapi.ToolCallItem{CallID: "c2", Name: "Attempt_Complete"},
	}
	assert.True(t, aipapi.IsExplicitCompletionItem(caseFolded))

	// Generic finish must be rejected (name-only predicate now false)
	finish := aipapi.Item{
		Kind:     aipapi.ItemKindToolCall,
		ToolCall: &aipapi.ToolCallItem{CallID: "c3", Name: "finish"},
	}
	assert.False(t, aipapi.IsExplicitCompletionItem(finish), "generic finish must not be explicit completion")
}

func TestHasExplicitCompletion_RequiresCorrelatedResult(t *testing.T) {
	t.Parallel()
	// Call only -> false (no executed evidence)
	callOnly := []aipapi.Item{
		{Kind: aipapi.ItemKindToolCall, Status: aipapi.ItemStatusCompleted, ToolCall: &aipapi.ToolCallItem{CallID: "c1", Name: "attempt_completion", Arguments: json.RawMessage(`{}`)}},
	}
	assert.False(t, aipapi.HasExplicitCompletion(callOnly), "call without matching result must be false")

	// Matching completed result -> true
	correlated := []aipapi.Item{
		{Kind: aipapi.ItemKindToolCall, Status: aipapi.ItemStatusCompleted, ToolCall: &aipapi.ToolCallItem{CallID: "c1", Name: "attempt_completion", Arguments: json.RawMessage(`{"result":"done"}`)}},
		{Kind: aipapi.ItemKindToolResult, Status: aipapi.ItemStatusCompleted, ToolResult: &aipapi.ToolResultItem{CallID: "c1", Name: "attempt_completion", Output: "ok"}},
	}
	assert.True(t, aipapi.HasExplicitCompletion(correlated), "correlated completed call+result must be true")

	// Orphan result (no matching call) -> false
	orphan := []aipapi.Item{
		{Kind: aipapi.ItemKindToolResult, Status: aipapi.ItemStatusCompleted, ToolResult: &aipapi.ToolResultItem{CallID: "c1", Name: "attempt_completion", Output: "ok"}},
	}
	assert.False(t, aipapi.HasExplicitCompletion(orphan), "orphan result must be false")

	// In-progress call -> false even with result
	inProgress := []aipapi.Item{
		{Kind: aipapi.ItemKindToolCall, Status: aipapi.ItemStatusInProgress, ToolCall: &aipapi.ToolCallItem{CallID: "c1", Name: "attempt_completion"}},
		{Kind: aipapi.ItemKindToolResult, Status: aipapi.ItemStatusCompleted, ToolResult: &aipapi.ToolResultItem{CallID: "c1", Name: "attempt_completion", Output: "ok"}},
	}
	assert.False(t, aipapi.HasExplicitCompletion(inProgress), "in-progress call must be false")

	// Incomplete result -> false
	incompleteResult := []aipapi.Item{
		{Kind: aipapi.ItemKindToolCall, Status: aipapi.ItemStatusCompleted, ToolCall: &aipapi.ToolCallItem{CallID: "c1", Name: "attempt_completion"}},
		{Kind: aipapi.ItemKindToolResult, Status: aipapi.ItemStatusIncomplete, ToolResult: &aipapi.ToolResultItem{CallID: "c1", Name: "attempt_completion", Output: "ok"}},
	}
	assert.False(t, aipapi.HasExplicitCompletion(incompleteResult), "incomplete result must be false")

	// Mismatched CallID -> false
	mismatched := []aipapi.Item{
		{Kind: aipapi.ItemKindToolCall, Status: aipapi.ItemStatusCompleted, ToolCall: &aipapi.ToolCallItem{CallID: "c1", Name: "attempt_completion"}},
		{Kind: aipapi.ItemKindToolResult, Status: aipapi.ItemStatusCompleted, ToolResult: &aipapi.ToolResultItem{CallID: "c2", Name: "attempt_completion", Output: "ok"}},
	}
	assert.False(t, aipapi.HasExplicitCompletion(mismatched), "mismatched CallID must be false")

	// Generic finish correlation must still be false (conservative)
	finishCorrelated := []aipapi.Item{
		{Kind: aipapi.ItemKindToolCall, Status: aipapi.ItemStatusCompleted, ToolCall: &aipapi.ToolCallItem{CallID: "c1", Name: "finish"}},
		{Kind: aipapi.ItemKindToolResult, Status: aipapi.ItemStatusCompleted, ToolResult: &aipapi.ToolResultItem{CallID: "c1", Name: "finish", Output: "ok"}},
	}
	assert.False(t, aipapi.HasExplicitCompletion(finishCorrelated), "generic finish must be rejected even when correlated")

	assert.False(t, aipapi.HasExplicitCompletion(nil))
	assert.False(t, aipapi.HasExplicitCompletion([]aipapi.Item{}))
}

func TestHasExplicitCompletion_MalformedFallsBack(t *testing.T) {
	t.Parallel()
	malformed := []aipapi.Item{
		{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{CallID: "", Name: "attempt_completion"}},
		{Kind: aipapi.ItemKindToolResult, Status: aipapi.ItemStatusCompleted, ToolResult: &aipapi.ToolResultItem{CallID: "", Name: "attempt_completion", Output: "ok"}},
	}
	assert.False(t, aipapi.HasExplicitCompletion(malformed), "malformed/absent explicit signal falls back to false")

	invalidJSON := []aipapi.Item{
		{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{CallID: "c1", Name: "attempt_completion", Arguments: json.RawMessage(`not json`)}},
		{Kind: aipapi.ItemKindToolResult, Status: aipapi.ItemStatusCompleted, ToolResult: &aipapi.ToolResultItem{CallID: "c1", Name: "attempt_completion", Output: "ok"}},
	}
	assert.False(t, aipapi.HasExplicitCompletion(invalidJSON))
}

func TestHasExplicitCompletion_VerifyPassesStrongEvidence(t *testing.T) {
	t.Parallel()
	correlated := []aipapi.Item{
		{Kind: aipapi.ItemKindToolCall, Status: aipapi.ItemStatusCompleted, ToolCall: &aipapi.ToolCallItem{CallID: "c1", Name: "attempt_completion"}},
		{Kind: aipapi.ItemKindToolResult, Status: aipapi.ItemStatusCompleted, ToolResult: &aipapi.ToolResultItem{CallID: "c1", Name: "attempt_completion", Output: "done"}},
	}
	require.True(t, aipapi.HasExplicitCompletion(correlated))
}

func TestExplicitCompletion_NoProviderNamesInHelper(t *testing.T) {
	t.Parallel()
	providers := []string{"openai", "anthropic", "gemini", "bedrock", "openresponses", "claude"}
	for _, p := range providers {
		assert.False(t, aipapi.IsExplicitCompletionToolName(p), "provider name %q must not be treated as explicit completion", p)
	}
}
