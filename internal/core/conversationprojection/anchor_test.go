package conversationprojection_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestMessageAnchor_ValidationAndString(t *testing.T) {
	t.Parallel()

	validID := conversationprojection.MessageIdentity("v1:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")

	tests := []struct {
		name      string
		anchor    conversationprojection.MessageAnchor
		wantValid bool
		wantStr   string
	}{
		{
			name: "valid 1st occurrence anchor",
			anchor: conversationprojection.MessageAnchor{
				Identity:   validID,
				Occurrence: 1,
			},
			wantValid: true,
			wantStr:   fmt.Sprintf("%s#1", validID),
		},
		{
			name: "valid 3rd occurrence anchor",
			anchor: conversationprojection.MessageAnchor{
				Identity:   validID,
				Occurrence: 3,
			},
			wantValid: true,
			wantStr:   fmt.Sprintf("%s#3", validID),
		},
		{
			name: "invalid 0 occurrence anchor",
			anchor: conversationprojection.MessageAnchor{
				Identity:   validID,
				Occurrence: 0,
			},
			wantValid: false,
		},
		{
			name: "invalid identity",
			anchor: conversationprojection.MessageAnchor{
				Identity:   "invalid-id",
				Occurrence: 1,
			},
			wantValid: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.anchor.Validate()
			if tc.wantValid {
				require.NoError(t, err)
				assert.Equal(t, tc.wantStr, tc.anchor.String())
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestMessageAnchor_RepeatedIdenticalMessagesAndOccurrenceOrdinals(t *testing.T) {
	t.Parallel()

	// 5 items with 3 identical "Hello" messages and 2 "World" messages interleaved
	helloItem := func(id string) aipapi.Item {
		return aipapi.Item{
			Kind: aipapi.ItemKindMessage,
			ID:   id,
			Role: aipapi.RoleUser,
			Content: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartText, Text: "Hello"},
			},
		}
	}

	worldItem := func(id string) aipapi.Item {
		return aipapi.Item{
			Kind: aipapi.ItemKindMessage,
			ID:   id,
			Role: aipapi.RoleAssistant,
			Content: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartText, Text: "World"},
			},
		}
	}

	items := []aipapi.Item{
		helloItem("item-1"), // Hello #1 (index 0)
		worldItem("item-2"), // World #1 (index 1)
		helloItem("item-3"), // Hello #2 (index 2)
		worldItem("item-4"), // World #2 (index 3)
		helloItem("item-5"), // Hello #3 (index 4)
	}

	anchors, err := conversationprojection.ComputeItemAnchors(items)
	require.NoError(t, err)
	require.Len(t, anchors, 5)

	helloID, err := conversationprojection.ItemIdentityOf(items[0])
	require.NoError(t, err)
	worldID, err := conversationprojection.ItemIdentityOf(items[1])
	require.NoError(t, err)

	// Verify occurrence ordinals
	assert.Equal(t, conversationprojection.MessageAnchor{Identity: helloID, Occurrence: 1}, anchors[0])
	assert.Equal(t, conversationprojection.MessageAnchor{Identity: worldID, Occurrence: 1}, anchors[1])
	assert.Equal(t, conversationprojection.MessageAnchor{Identity: helloID, Occurrence: 2}, anchors[2])
	assert.Equal(t, conversationprojection.MessageAnchor{Identity: worldID, Occurrence: 2}, anchors[3])
	assert.Equal(t, conversationprojection.MessageAnchor{Identity: helloID, Occurrence: 3}, anchors[4])

	// Test ItemAnchorAt
	anchor0, err := conversationprojection.ItemAnchorAt(items, 0)
	require.NoError(t, err)
	assert.Equal(t, conversationprojection.MessageAnchor{Identity: helloID, Occurrence: 1}, anchor0)

	anchor4, err := conversationprojection.ItemAnchorAt(items, 4)
	require.NoError(t, err)
	assert.Equal(t, conversationprojection.MessageAnchor{Identity: helloID, Occurrence: 3}, anchor4)

	// Test ResolveAnchor
	idx, found, err := conversationprojection.ResolveAnchor(items, conversationprojection.MessageAnchor{Identity: helloID, Occurrence: 1})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, 0, idx)

	idx, found, err = conversationprojection.ResolveAnchor(items, conversationprojection.MessageAnchor{Identity: helloID, Occurrence: 2})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, 2, idx)

	idx, found, err = conversationprojection.ResolveAnchor(items, conversationprojection.MessageAnchor{Identity: helloID, Occurrence: 3})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, 4, idx)

	// Occurrence 4 does not exist -> found = false
	idx, found, err = conversationprojection.ResolveAnchor(items, conversationprojection.MessageAnchor{Identity: helloID, Occurrence: 4})
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, -1, idx)
}

func TestMessageAnchor_CallAnchorsEquivalence(t *testing.T) {
	t.Parallel()

	// Legacy call
	legacyCall := aipapi.Call{
		Instructions: []aipapi.Message{
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "System prompt"}}},
		},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "Hello"}}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "Hi there!"}}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "Hello"}}},
		},
	}

	// Item authority call with exact same semantic messages
	itemCall := aipapi.Call{
		Items: []aipapi.Item{
			{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleSystem, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "System prompt"}}},
			{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "Hello"}}},
			{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleAssistant, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "Hi there!"}}},
			{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "Hello"}}},
		},
	}

	legacyAnchors, err := conversationprojection.ComputeCallAnchors(legacyCall)
	require.NoError(t, err)

	itemAnchors, err := conversationprojection.ComputeCallAnchors(itemCall)
	require.NoError(t, err)

	require.Len(t, legacyAnchors, 4)
	require.Len(t, itemAnchors, 4)
	assert.Equal(t, legacyAnchors, itemAnchors, "legacy and item call anchors must match exactly")
	assert.Equal(t, uint32(1), itemAnchors[1].Occurrence)
	assert.Equal(t, uint32(2), itemAnchors[3].Occurrence)
	assert.Equal(t, itemAnchors[1].Identity, itemAnchors[3].Identity)
}

func TestMessageAnchor_NegativesAndNonMessageItems(t *testing.T) {
	t.Parallel()

	mixedItems := []aipapi.Item{
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "Run tool"}}},
		{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{CallID: "c1", Name: "bash"}},
		{Kind: aipapi.ItemKindToolResult, ToolResult: &aipapi.ToolResultItem{CallID: "c1", Name: "bash", Output: "done"}},
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleAssistant, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "Done!"}}},
	}

	// Asking for anchor of non-message item at index 1 (tool call) must return ErrNonMessageItem
	_, err := conversationprojection.ItemAnchorAt(mixedItems, 1)
	require.ErrorIs(t, err, conversationprojection.ErrNonMessageItem)

	// Asking for anchor of non-message item at index 2 (tool result) must return ErrNonMessageItem
	_, err = conversationprojection.ItemAnchorAt(mixedItems, 2)
	require.ErrorIs(t, err, conversationprojection.ErrNonMessageItem)

	// Index out of bounds
	_, err = conversationprojection.ItemAnchorAt(mixedItems, 10)
	require.Error(t, err)

	_, err = conversationprojection.ItemAnchorAt(mixedItems, -1)
	require.Error(t, err)
}

func TestMessageAnchor_NoPlaintextInString(t *testing.T) {
	t.Parallel()

	validID := conversationprojection.MessageIdentity("v1:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	anchor := conversationprojection.MessageAnchor{
		Identity:   validID,
		Occurrence: 2,
	}

	str := anchor.String()
	assert.False(t, strings.Contains(str, "Hello"), "anchor string must only contain digest and ordinal")
	assert.Equal(t, fmt.Sprintf("%s#2", validID), str)
}
