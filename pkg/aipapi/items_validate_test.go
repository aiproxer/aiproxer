package aipapi_test

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestCallValidate_itemFieldLabels locks the diagnostic field labels rendered
// lazily for ordered item authority: a valid history must not build them, and a
// violation must still name the same field as the previously eager form.
func TestCallValidate_itemFieldLabels(t *testing.T) {
	t.Parallel()

	validCall := aipapi.Call{
		ID: "call-1",
		Items: []aipapi.Item{{
			Kind: aipapi.ItemKindMessage,
			ID:   "item-1",
			Role: aipapi.RoleUser,
			Content: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartText, Text: "hello"},
			},
		}},
	}
	if err := validCall.Validate(); err != nil {
		t.Fatalf("valid item call rejected: %v", err)
	}

	cases := []struct {
		name  string
		call  aipapi.Call
		field string
	}{
		{
			name: "content part field",
			call: aipapi.Call{Items: []aipapi.Item{{
				Kind:    aipapi.ItemKindMessage,
				ID:      "m",
				Role:    aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartImageRef}},
			}}},
			field: "Items[0].Content[0].ImageRef",
		},
		{
			name: "tool result part field",
			call: aipapi.Call{Items: []aipapi.Item{
				{Kind: aipapi.ItemKindToolCall, ID: "c", ToolCall: &aipapi.ToolCallItem{CallID: "call_1", Name: "fn"}},
				{Kind: aipapi.ItemKindToolResult, ID: "r", ToolResult: &aipapi.ToolResultItem{
					CallID: "call_1",
					Parts:  []aipapi.ContentPart{{Kind: aipapi.ContentPartText}},
				}},
			}},
			field: "Items[1].ToolResult.Parts[0].Text",
		},
		{
			name: "tool call name whitespace",
			call: aipapi.Call{Items: []aipapi.Item{{
				Kind:     aipapi.ItemKindToolCall,
				ID:       "c",
				ToolCall: &aipapi.ToolCallItem{CallID: "call_1", Name: " fn"},
			}}},
			field: "Items[0].ToolCall.Name",
		},
		{
			name: "duplicate item ID",
			call: aipapi.Call{Items: []aipapi.Item{
				{Kind: aipapi.ItemKindMessage, ID: "dup", Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "a"}}},
				{Kind: aipapi.ItemKindMessage, ID: "dup", Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "b"}}},
			}},
			field: "Items[1].ID",
		},
		{
			name: "orphan tool result",
			call: aipapi.Call{Items: []aipapi.Item{{
				Kind:       aipapi.ItemKindToolResult,
				ID:         "r",
				ToolResult: &aipapi.ToolResultItem{CallID: "missing", Output: "out"},
			}}},
			field: "Items[0].ToolResult.CallID",
		},
		{
			name: "extension item field",
			call: aipapi.Call{Items: []aipapi.Item{{
				Kind:      aipapi.ItemKindExtension,
				ID:        "e",
				Extension: &aipapi.OpaqueExtension{},
			}}},
			field: "Items[0].Extension.Namespace",
		},
		{
			name: "item level variant conflict",
			call: aipapi.Call{Items: []aipapi.Item{{
				Kind:     aipapi.ItemKindMessage,
				ID:       "m",
				Role:     aipapi.RoleUser,
				Content:  []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "a"}},
				ToolCall: &aipapi.ToolCallItem{CallID: "call_1", Name: "fn"},
			}}},
			field: "Items[0]",
		},
		{
			name: "reasoning item field",
			call: aipapi.Call{Items: []aipapi.Item{{
				Kind: aipapi.ItemKindReasoning,
				ID:   "r",
			}}},
			field: "Items[0].Reasoning",
		},
		{
			name: "compaction item field",
			call: aipapi.Call{Items: []aipapi.Item{{
				Kind:       aipapi.ItemKindCompaction,
				ID:         "c",
				Compaction: &aipapi.CompactionItem{Opaque: []byte("{")},
			}}},
			field: "Items[0].Compaction.Opaque",
		},
		{
			name: "unknown item kind",
			call: aipapi.Call{Items: []aipapi.Item{{
				Kind: "mystery",
				ID:   "x",
			}}},
			field: "Items[0].Kind",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.call.Validate()
			if err == nil {
				t.Fatal("expected validation error")
			}
			var v *aipapi.ValidationError
			if !errors.As(err, &v) {
				t.Fatalf("error type = %T, want *aipapi.ValidationError", err)
			}
			if v.Field != tc.field {
				t.Fatalf("Field = %q, want %q (message %q)", v.Field, tc.field, v.Message)
			}
		})
	}
}

// TestValidateEventEnvelope_itemFieldNamesStandaloneItem locks the standalone
// event-item labels, which use "Item" rather than an "Items[index]" path.
func TestValidateEventEnvelope_itemFieldNamesStandaloneItem(t *testing.T) {
	t.Parallel()

	ev := &aipapi.Event{
		Kind: aipapi.EventItem,
		Item: &aipapi.Item{
			Kind: aipapi.ItemKindMessage,
			ID:   "m",
			Role: aipapi.RoleUser,
		},
	}
	err := aipapi.ValidateEventEnvelope(ev)
	if err == nil {
		t.Fatal("expected validation error")
	}
	var v *aipapi.ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("error type = %T, want *aipapi.ValidationError", err)
	}
	if v.Field != "Item" {
		t.Fatalf("Field = %q, want %q", v.Field, "Item")
	}
	const want = "Item.Content: at least one content part is required for message item"
	if v.Message != want {
		t.Fatalf("Message = %q, want %q", v.Message, want)
	}
}

// TestCallValidate_itemHistoryAllocBudget ratchets the valid ordered-item path.
// Two duplicate-detection maps sized to the history remain; field labels are
// rendered only on violation, so the budget does not grow per item or part.
//
//nolint:paralleltest // AllocsPerRun forbids parallel tests.
func TestCallValidate_itemHistoryAllocBudget(t *testing.T) {
	call := benchValidateItemHistoryCall(300)
	allocs := testing.AllocsPerRun(5, func() {
		if err := call.Validate(); err != nil {
			t.Fatalf("valid item call rejected: %v", err)
		}
	})
	// Pre-lazy-labels baseline was ~1,450 allocs per run for this history. The
	// cap leaves headroom for duplicate-map growth across Go versions while
	// still failing on any restored per-item or per-part label allocation.
	const maxAllocs = 64
	if allocs > maxAllocs {
		t.Fatalf("allocs per run=%g for a 300-item history, want <= %d", allocs, maxAllocs)
	}
}
