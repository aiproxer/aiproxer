package conversationprojection_test

// Behavior-preservation coverage for the identity table Project and Reassert now
// derive once per trajectory element. The table replaced a per-phase re-derivation
// and feeds order-sensitive phases: exclusion still fails closed on an element
// whose identity cannot be derived, filtering still keeps an unidentifiable
// element, anchor resolution still counts occurrences across the instruction and
// message regions, retained slices still drop the item_reference of a removed
// message, and provenance-owned copies still cannot delete legitimate
// same-identity client content. Each case below pins one of those answers at the
// seam the table introduced.

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// invalidRoleMessage carries no derivable identity because MessageIdentityOf
// rejects the role. It is the element the phases must skip or fail closed on.
func invalidRoleMessage(text string) aipapi.Message {
	return aipapi.Message{Role: "", Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: text}}}
}

// countMessagesWithText counts legacy messages whose single text part equals text.
func countMessagesWithText(msgs []aipapi.Message, text string) int {
	n := 0
	for _, m := range msgs {
		if len(m.Parts) == 1 && m.Parts[0].Kind == aipapi.PartText && m.Parts[0].Text == text {
			n++
		}
	}
	return n
}

// TestProjectFailsClosedOnTheSameElementTheDerivationRejected pins the fail-closed
// contract of the legacy projection: deriving identities up front must not turn a
// rejected message into a silently dropped one.
func TestProjectFailsClosedOnTheSameElementTheDerivationRejected(t *testing.T) {
	t.Parallel()
	_, _, err := conversationprojection.Project(aipapi.Call{
		Messages: []aipapi.Message{textMessage(aipapi.RoleUser, "ok"), invalidRoleMessage("bad")},
	}, snapshotWithTagsAndOverlays(nil, nil))
	require.Error(t, err)
	require.ErrorIs(t, err, conversationprojection.ErrProjectionFailed)
}

// TestIdentityTableSkipsNonMessageItemsWhileExcludingAMessageItem pins the
// item-authority skip rule the table encodes: a tool_call and its tool_result
// carry no message identity, so exclusion must count only the message item and
// leave the other item kinds in place instead of deriving (and failing on) them.
func TestIdentityTableSkipsNonMessageItemsWhileExcludingAMessageItem(t *testing.T) {
	t.Parallel()
	excluded := textItem(aipapi.RoleUser, "msg-excluded", "secret")
	call := aipapi.Call{Items: []aipapi.Item{
		excluded,
		{
			Kind: aipapi.ItemKindToolCall, ID: "call-1", Status: aipapi.ItemStatusCompleted,
			ToolCall: &aipapi.ToolCallItem{CallID: "call-1", Name: "read_file"},
		},
		{
			Kind: aipapi.ItemKindToolResult, ID: "result-1", Status: aipapi.ItemStatusCompleted,
			ToolResult: &aipapi.ToolResultItem{CallID: "call-1", Name: "read_file", Output: "ok"},
		},
		textItem(aipapi.RoleUser, "msg-kept", "visible"),
	}}
	snap := snapshotWithTagsAndOverlays(
		[]conversationprojection.Tag{{Identity: mustIdentityForItem(t, excluded)}}, nil)
	projected, evidence, err := conversationprojection.Project(call, snap)
	require.NoError(t, err)
	require.Len(t, projected.Items, 3, "only the excluded message item is removed")
	assert.Equal(t, 1, evidence.FilteredCount)
	require.Equal(t, aipapi.ItemKindToolCall, projected.Items[0].Kind)
	require.NotNil(t, projected.Items[0].ToolCall)
	assert.Equal(t, "call-1", projected.Items[0].ToolCall.CallID,
		"a tool_call item carries no message identity and must never be filtered")
	require.Equal(t, aipapi.ItemKindToolResult, projected.Items[1].Kind)
	require.NotNil(t, projected.Items[1].ToolResult)
	assert.Equal(t, "call-1", projected.Items[1].ToolResult.CallID,
		"a tool_result item carries no message identity and must never be filtered")
	assert.Equal(t, "msg-kept", projected.Items[2].ID)
}

// TestAnchorIndexResolvesDuplicateOccurrencesInBothRegions pins the position list
// that replaced the anchor scan: duplicates spanning the instruction region and
// then the message region keep one global occurrence ordinal, so occurrence 2
// resolves at the first message copy rather than at the last duplicate and no
// after-message overlay is injected into the instruction region.
func TestAnchorIndexResolvesDuplicateOccurrencesInBothRegions(t *testing.T) {
	t.Parallel()
	dup := textMessage(aipapi.RoleUser, "duplicate")
	anchor := conversationprojection.MessageAnchor{Identity: mustIdentityForMsg(t, dup), Occurrence: 2}
	snap := snapshotWithTagsAndOverlays(nil, []conversationprojection.Overlay{{
		OverlayID:           "ov-second",
		Revision:            1,
		SlotOrdinal:         1,
		Active:              true,
		Message:             conversationprojection.OverlayMessage{Role: aipapi.RoleSystem, Text: "steer-second"},
		Placement:           conversationprojection.Placement{Kind: conversationprojection.PlacementAfterMessage, Anchor: &anchor},
		AnchorMissingPolicy: conversationprojection.AnchorFailClosed,
	}})
	projected, _, err := conversationprojection.Project(aipapi.Call{
		Instructions: []aipapi.Message{dup},
		Messages:     []aipapi.Message{dup, dup},
	}, snap)
	require.NoError(t, err)
	require.Len(t, projected.Instructions, 1,
		"the instruction region must not receive an after-message overlay")
	require.Len(t, projected.Messages, 3)
	assert.Equal(t, "steer-second", projected.Messages[1].Parts[0].Text,
		"occurrence 2 is the first message copy; the overlay lands after it, not after the last duplicate")
}

// TestAnchorIndexAnswersAnOccurrenceBeyondTheDuplicates pins the ordinal boundary:
// an occurrence past the last duplicate resolves nowhere and the fail-closed
// policy refuses it rather than relocating the overlay to the last copy.
func TestAnchorIndexAnswersAnOccurrenceBeyondTheDuplicates(t *testing.T) {
	t.Parallel()
	dup := textMessage(aipapi.RoleUser, "duplicate")
	anchor := conversationprojection.MessageAnchor{Identity: mustIdentityForMsg(t, dup), Occurrence: 4}
	snap := snapshotWithTagsAndOverlays(nil, []conversationprojection.Overlay{{
		OverlayID:           "ov-fourth",
		Revision:            1,
		SlotOrdinal:         1,
		Active:              true,
		Message:             conversationprojection.OverlayMessage{Role: aipapi.RoleSystem, Text: "steer-fourth"},
		Placement:           conversationprojection.Placement{Kind: conversationprojection.PlacementAfterMessage, Anchor: &anchor},
		AnchorMissingPolicy: conversationprojection.AnchorFailClosed,
	}})
	_, _, err := conversationprojection.Project(aipapi.Call{
		Instructions: []aipapi.Message{dup},
		Messages:     []aipapi.Message{dup, dup},
	}, snap)
	require.Error(t, err)
	require.ErrorIs(t, err, conversationprojection.ErrAnchorMissing)
	require.ErrorIs(t, err, conversationprojection.ErrAnchorNotFound)
}

// TestReassertDropsTheItemReferenceOfAnExcludedMessage pins the retained-slice
// dependency cleanup: the removed message is gone and so is the item_reference
// that targeted it, while an unrelated reference survives.
func TestReassertDropsTheItemReferenceOfAnExcludedMessage(t *testing.T) {
	t.Parallel()
	excluded := textItem(aipapi.RoleUser, "msg-excluded", "secret")
	keep := textItem(aipapi.RoleUser, "msg-keep", "visible")
	refExcluded := aipapi.Item{
		Kind: aipapi.ItemKindItemReference, ID: "ref-excluded", Status: aipapi.ItemStatusCompleted,
		Reference: &aipapi.ItemReference{ID: "msg-excluded"},
	}
	refKeep := aipapi.Item{
		Kind: aipapi.ItemKindItemReference, ID: "ref-keep", Status: aipapi.ItemStatusCompleted,
		Reference: &aipapi.ItemReference{ID: "msg-keep"},
	}
	snap := snapshotWithTagsAndOverlays(
		[]conversationprojection.Tag{{Identity: mustIdentityForItem(t, excluded)}},
		[]conversationprojection.Overlay{{
			OverlayID:           "ov-item",
			Revision:            1,
			SlotOrdinal:         1,
			Active:              true,
			Message:             conversationprojection.OverlayMessage{Role: aipapi.RoleSystem, Text: "steer-item"},
			Placement:           conversationprojection.Placement{Kind: conversationprojection.PlacementStablePrefix},
			AnchorMissingPolicy: conversationprojection.AnchorStablePrefixFallback,
		}},
	)
	call := aipapi.Call{Items: []aipapi.Item{excluded, refExcluded, keep, refKeep}}
	projected, evidence, err := conversationprojection.Project(call, snap)
	require.NoError(t, err)
	frozen, err := conversationprojection.FilterNeverBackend(call, snap)
	require.NoError(t, err)

	reasserted, _, err := conversationprojection.Reassert(projected, snap, evidence.Provenance, frozen)
	require.NoError(t, err)
	for _, it := range reasserted.Items {
		assert.False(t, it.Kind == aipapi.ItemKindMessage && it.ID == "msg-excluded",
			"excluded message reintroduced")
		if it.Kind == aipapi.ItemKindItemReference {
			assert.NotEqual(t, "msg-excluded", it.Reference.ID,
				"reference to an excluded message reintroduced")
		}
	}
	require.Len(t, reasserted.Items, 3,
		"steering item plus the surviving message and its reference")
}

// TestReassertKeepsLegitimateContentThatSharesAProvenanceIdentity pins the
// same-identity collision rule: a client message that carries exactly the overlay
// text survives alongside the steering copy, and an unexplained extra duplicate
// fails closed instead of choosing a copy to delete.
func TestReassertKeepsLegitimateContentThatSharesAProvenanceIdentity(t *testing.T) {
	t.Parallel()
	shared := textMessage(aipapi.RoleUser, "same text")
	snap := snapshotWithTagsAndOverlays(nil, []conversationprojection.Overlay{{
		OverlayID:           "ov-collide",
		Revision:            1,
		SlotOrdinal:         1,
		Active:              true,
		Message:             conversationprojection.OverlayMessage{Role: aipapi.RoleUser, Text: "same text"},
		Placement:           conversationprojection.Placement{Kind: conversationprojection.PlacementStablePrefix},
		AnchorMissingPolicy: conversationprojection.AnchorStablePrefixFallback,
	}})
	call := aipapi.Call{Messages: []aipapi.Message{shared, textMessage(aipapi.RoleAssistant, "reply")}}
	projected, evidence, err := conversationprojection.Project(call, snap)
	require.NoError(t, err)
	frozen, err := conversationprojection.FilterNeverBackend(call, snap)
	require.NoError(t, err)

	reasserted, ev, err := conversationprojection.Reassert(projected, snap, evidence.Provenance, frozen)
	require.NoError(t, err)
	require.Len(t, ev.Provenance, 1)
	// A stable-prefix overlay is injected into the instruction region, so the
	// legitimate client copy stays the only such message in the message region.
	assert.Equal(t, 1, countMessagesWithText(reasserted.Messages, "same text"),
		"the legitimate client copy must survive reassertion")
	assert.Equal(t, 1, countMessagesWithText(reasserted.Instructions, "same text"),
		"the steering copy must stay injected exactly once")

	// An unexplained extra duplicate of the same identity is genuine ambiguity:
	// the collision check must fail closed rather than pick a copy to delete.
	ambiguous := aipapi.CloneCall(projected)
	ambiguous.Messages = append(ambiguous.Messages, shared)
	_, _, err = conversationprojection.Reassert(ambiguous, snap, evidence.Provenance, frozen)
	require.Error(t, err)
	require.ErrorIs(t, err, conversationprojection.ErrProjectionFailed)
}
