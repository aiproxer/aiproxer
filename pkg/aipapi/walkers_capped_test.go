package aipapi

import (
	"testing"
)

// TestNormalizedItemsCapped_PrefixMatchesFull proves the bounded projection
// pays only for the retained prefix: the first max entries equal the full
// trajectory's prefix, for both legacy and item-authoritative calls.
func TestNormalizedItemsCapped_PrefixMatchesFull(t *testing.T) {
	t.Parallel()
	legacy := Call{
		Instructions: []Message{{Role: RoleSystem, Parts: []Part{TextPart("sys")}}},
		Messages: []Message{
			{Role: RoleUser, Parts: []Part{TextPart("a")}},
			{Role: RoleUser, Parts: []Part{TextPart("b")}},
			{Role: RoleUser, Parts: []Part{TextPart("c")}},
		},
	}
	full := NormalizedItems(legacy)
	if len(full) != 4 {
		t.Fatalf("full=%d want 4", len(full))
	}
	capped := NormalizedItemsCapped(legacy, 2)
	if len(capped) != 2 {
		t.Fatalf("capped=%d want 2", len(capped))
	}
	for i := range capped {
		if capped[i].Role != full[i].Role || len(capped[i].Content) != len(full[i].Content) {
			t.Fatalf("capped[%d] diverges from full prefix", i)
		}
	}
	if got := NormalizedItemsCapped(legacy, 0); len(got) != len(full) {
		t.Fatalf("non-positive max=%d want full %d", len(got), len(full))
	}
	if got := NormalizedItemsCapped(legacy, 100); len(got) != len(full) {
		t.Fatalf("overlarge max=%d want full %d", len(got), len(full))
	}

	items := Call{Items: []Item{
		{Kind: ItemKindMessage, Role: RoleUser, Content: []ContentPart{{Kind: ContentPartText, Text: "a"}}},
		{Kind: ItemKindToolCall, ToolCall: &ToolCallItem{CallID: "c1", Name: "read"}},
		{Kind: ItemKindToolResult, ToolResult: &ToolResultItem{CallID: "c1", Output: "out"}},
	}}
	if got := NormalizedItemsCapped(items, 2); len(got) != 2 {
		t.Fatalf("item capped=%d want 2", len(got))
	}
	if got := NormalizedItemsCapped(items, 0); len(got) != 3 {
		t.Fatalf("item uncapped=%d want 3", len(got))
	}
}
