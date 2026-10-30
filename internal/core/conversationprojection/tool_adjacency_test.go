package conversationprojection_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Handoff fix-steering-tool-adjacency (#1102 follow-up): an AfterMessage steering
// overlay anchored to an assistant tool-call message must never be injected
// between that call and the tool-result messages answering it. Chat-family,
// Anthropic-messages and strict Responses upstreams reject a split pair, and the
// projection is the only seam that decides where the overlay bytes land, so the
// insertion point moves past the contiguous answering results while the client
// messages themselves are never reordered or rewritten.

const toolAdjacencySteerText = "tool-adjacency-steering"

// toolCallMessage builds one assistant message carrying n legacy tool calls in
// the canonical shape: PartJSON with a stable ToolCallID/ToolName.
func toolCallMessage(callIDs ...string) aipapi.Message {
	parts := make([]aipapi.Part, 0, len(callIDs))
	for _, id := range callIDs {
		parts = append(parts, aipapi.Part{
			Kind:       aipapi.PartJSON,
			ToolCallID: id,
			ToolName:   "w",
			Content:    json.RawMessage(`{"q":"` + id + `"}`),
		})
	}
	return aipapi.Message{Role: aipapi.RoleAssistant, Parts: parts}
}

// toolResultMessage builds one tool message answering every provided call ID. A
// single message carrying several PartToolResult parts is the canonical
// batch-result shape.
func toolResultMessage(callIDs ...string) aipapi.Message {
	parts := make([]aipapi.Part, 0, len(callIDs))
	for _, id := range callIDs {
		parts = append(parts, aipapi.Part{
			Kind:       aipapi.PartToolResult,
			ToolCallID: id,
			ToolName:   "w",
			Text:       "out-" + id,
		})
	}
	return aipapi.Message{Role: aipapi.RoleTool, Parts: parts}
}

// afterMessageToolOverlay anchors one active fail-closed AfterMessage overlay to
// the given message and returns its snapshot-side record.
func afterMessageToolOverlay(t *testing.T, anchor aipapi.Message) conversationprojection.Overlay {
	t.Helper()
	return conversationprojection.Overlay{
		OverlayID:   "ov-tool-adjacency",
		Revision:    1,
		SlotOrdinal: 1,
		Active:      true,
		Message:     conversationprojection.OverlayMessage{Role: aipapi.RoleDeveloper, Text: toolAdjacencySteerText},
		Placement: conversationprojection.Placement{
			Kind:   conversationprojection.PlacementAfterMessage,
			Anchor: &conversationprojection.MessageAnchor{Identity: mustIdentityForMsg(t, anchor), Occurrence: 1},
		},
		AnchorMissingPolicy: conversationprojection.AnchorFailClosed,
	}
}

// clientMessagesWithoutOverlay removes the injected steering copy so a test can
// assert the client's own message bytes and order survived projection intact.
func clientMessagesWithoutOverlay(call aipapi.Call) []aipapi.Message {
	out := make([]aipapi.Message, 0, len(call.Messages))
	for _, m := range call.Messages {
		if len(m.Parts) == 1 && m.Parts[0].Kind == aipapi.PartText && m.Parts[0].Text == toolAdjacencySteerText {
			continue
		}
		out = append(out, m)
	}
	return out
}

// TestProject_AfterMessageToolCallAnchor_LandsAfterAnsweringResults pins the
// adjacency invariant: when the anchored message carries tool calls, the overlay
// is injected after the contiguous tool-result messages answering those calls.
// Single, batch (N calls in one result message), and missing-result shapes all
// place the overlay after the last present result; a result answering a foreign
// call or a plain message ends the run at its position.
func TestProject_AfterMessageToolCallAnchor_LandsAfterAnsweringResults(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		callIDs   []string
		after     []aipapi.Message
		wantIndex int
	}{
		{
			name:      "single_result",
			callIDs:   []string{"c1"},
			after:     []aipapi.Message{toolResultMessage("c1")},
			wantIndex: 3,
		},
		{
			name:    "all_results_separate_messages",
			callIDs: []string{"c1", "c2", "c3"},
			after: []aipapi.Message{
				toolResultMessage("c1"),
				toolResultMessage("c2"),
				toolResultMessage("c3"),
			},
			wantIndex: 5,
		},
		{
			name:      "all_results_one_message",
			callIDs:   []string{"c1", "c2"},
			after:     []aipapi.Message{toolResultMessage("c1", "c2")},
			wantIndex: 3,
		},
		{
			name:      "incomplete_results_follow_last_present",
			callIDs:   []string{"c1", "c2", "c3"},
			after:     []aipapi.Message{toolResultMessage("c1"), toolResultMessage("c3")},
			wantIndex: 4,
		},
		{
			name:      "foreign_result_does_not_move",
			callIDs:   []string{"c1"},
			after:     []aipapi.Message{toolResultMessage("other")},
			wantIndex: 2,
		},
		{
			name:      "plain_message_ends_the_result_run",
			callIDs:   []string{"c1"},
			after:     []aipapi.Message{toolResultMessage("c1"), textMessage(aipapi.RoleUser, "note")},
			wantIndex: 3,
		},
		{
			name:      "plain_message_limits_run_to_contiguous_results",
			callIDs:   []string{"c1", "c2"},
			after:     []aipapi.Message{toolResultMessage("c1"), textMessage(aipapi.RoleUser, "note"), toolResultMessage("c2")},
			wantIndex: 3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			anchor := toolCallMessage(tc.callIDs...)
			messages := append([]aipapi.Message{
				textMessage(aipapi.RoleUser, "u0"),
				anchor,
			}, tc.after...)
			messages = append(messages, textMessage(aipapi.RoleUser, "tail"))
			call := aipapi.Call{
				Messages: messages,
				Tools:    []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
			}
			snap := snapshotWithTagsAndOverlays(nil, []conversationprojection.Overlay{afterMessageToolOverlay(t, anchor)})

			projected, evidence, err := conversationprojection.Project(call, snap)
			require.NoError(t, err)
			require.Len(t, projected.Messages, len(messages)+1)

			steer := projected.Messages[tc.wantIndex]
			require.Equal(t, aipapi.RoleDeveloper, steer.Role)
			require.Len(t, steer.Parts, 1)
			require.Equal(t, toolAdjacencySteerText, steer.Parts[0].Text,
				"overlay must land after the answering tool results, never between a call and its result")
			require.Equal(t, messages, clientMessagesWithoutOverlay(projected),
				"only the overlay may move; client message bytes and order must be untouched")

			require.Len(t, evidence.Provenance, 1)
			p := evidence.Provenance[0]
			require.Equal(t, conversationprojection.PlacementAfterMessage, p.ResolvedKind)
			require.NotNil(t, p.ResolvedAnchor)
			require.Equal(t, mustIdentityForMsg(t, anchor), p.ResolvedAnchor.Identity,
				"provenance keeps naming the stored anchor even though the injection point moved")
			require.NotNil(t, p.InjectedLegacyIndex)
			require.Equal(t, tc.wantIndex, *p.InjectedLegacyIndex)
			require.Equal(t, tc.wantIndex, p.InjectedTrajectoryIndex)
		})
	}
}

// TestProject_AfterMessageOrdinaryAnchor_NotMovedByFollowingToolResult pins the
// condition half of the invariant: the skip is driven by the ANCHOR carrying
// tool calls, not by a tool-result message merely following it.
func TestProject_AfterMessageOrdinaryAnchor_NotMovedByFollowingToolResult(t *testing.T) {
	t.Parallel()
	anchor := textMessage(aipapi.RoleAssistant, "reply")
	messages := []aipapi.Message{
		textMessage(aipapi.RoleUser, "u0"),
		anchor,
		toolResultMessage("c1"),
		textMessage(aipapi.RoleUser, "tail"),
	}
	call := aipapi.Call{Messages: messages}
	snap := snapshotWithTagsAndOverlays(nil, []conversationprojection.Overlay{afterMessageToolOverlay(t, anchor)})

	projected, evidence, err := conversationprojection.Project(call, snap)
	require.NoError(t, err)
	require.Len(t, projected.Messages, 5)
	require.Equal(t, toolAdjacencySteerText, projected.Messages[2].Parts[0].Text,
		"an anchor without tool calls keeps its immediate-after placement")
	require.Equal(t, messages, clientMessagesWithoutOverlay(projected))
	require.Equal(t, 2, *evidence.Provenance[0].InjectedLegacyIndex)
}

// TestProject_AfterMessageToolCallAnchor_ReplayStable pins determinism: the
// identical input produces identical model-visible bytes and provenance on every
// projection, so prompt-cache prefixes stay stable across retries of the turn.
func TestProject_AfterMessageToolCallAnchor_ReplayStable(t *testing.T) {
	t.Parallel()
	anchor := toolCallMessage("c1", "c2")
	call := aipapi.Call{Messages: []aipapi.Message{
		textMessage(aipapi.RoleUser, "u0"),
		anchor,
		toolResultMessage("c1"),
		toolResultMessage("c2"),
		textMessage(aipapi.RoleUser, "tail"),
	}}
	snap := snapshotWithTagsAndOverlays(nil, []conversationprojection.Overlay{afterMessageToolOverlay(t, anchor)})

	first, firstEv, err := conversationprojection.Project(call, snap)
	require.NoError(t, err)
	second, secondEv, err := conversationprojection.Project(call, snap)
	require.NoError(t, err)

	require.Equal(t, first.Messages, second.Messages)
	require.Equal(t, firstEv.Provenance, secondEv.Provenance)
	firstBytes, err := json.Marshal(first.Messages)
	require.NoError(t, err)
	secondBytes, err := json.Marshal(second.Messages)
	require.NoError(t, err)
	require.Equal(t, string(firstBytes), string(secondBytes),
		"identical input must produce byte-identical trajectory for prompt-cache stability")
}

// TestProject_AfterMessageItemMessageAnchor_NotMovedPastToolItems pins the scope
// of the change: item-authority message items cannot carry tool calls at all
// (calls and results are their own items), so a message-bound insertion never
// splits a call from its result and the placement stays immediate.
func TestProject_AfterMessageItemMessageAnchor_NotMovedPastToolItems(t *testing.T) {
	t.Parallel()
	anchor := aipapi.Item{
		Kind:    aipapi.ItemKindMessage,
		ID:      "a1",
		Status:  aipapi.ItemStatusCompleted,
		Role:    aipapi.RoleAssistant,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartJSON, Text: `{"plan":true}`}},
	}
	call := aipapi.Call{Items: []aipapi.Item{
		textItem(aipapi.RoleUser, "u0", "u0"),
		anchor,
		{
			Kind:   aipapi.ItemKindToolCall,
			ID:     "tc1",
			Status: aipapi.ItemStatusCompleted,
			ToolCall: &aipapi.ToolCallItem{
				CallID:    "c1",
				Name:      "w",
				Arguments: json.RawMessage(`{}`),
			},
		},
		{
			Kind:   aipapi.ItemKindToolResult,
			ID:     "tr1",
			Status: aipapi.ItemStatusCompleted,
			ToolResult: &aipapi.ToolResultItem{
				CallID: "c1",
				Name:   "w",
				Output: "ok",
			},
		},
		textItem(aipapi.RoleUser, "u1", "tail"),
	}}
	overlay := conversationprojection.Overlay{
		OverlayID:   "ov-tool-adjacency",
		Revision:    1,
		SlotOrdinal: 1,
		Active:      true,
		Message:     conversationprojection.OverlayMessage{Role: aipapi.RoleDeveloper, Text: toolAdjacencySteerText},
		Placement: conversationprojection.Placement{
			Kind:   conversationprojection.PlacementAfterMessage,
			Anchor: &conversationprojection.MessageAnchor{Identity: mustIdentityForItem(t, anchor), Occurrence: 1},
		},
		AnchorMissingPolicy: conversationprojection.AnchorFailClosed,
	}
	snap := snapshotWithTagsAndOverlays(nil, []conversationprojection.Overlay{overlay})

	projected, evidence, err := conversationprojection.Project(call, snap)
	require.NoError(t, err)
	require.Len(t, projected.Items, 6)
	require.Equal(t, toolAdjacencySteerText, projected.Items[2].Content[0].Text,
		"a message item holds no tool calls, so the placement stays immediately after it")
	require.Equal(t, aipapi.ItemKindToolCall, projected.Items[3].Kind)
	require.Equal(t, aipapi.ItemKindToolResult, projected.Items[4].Kind)
	require.Len(t, evidence.Provenance, 1)
	require.Equal(t, conversationprojection.PlacementAfterMessage, evidence.Provenance[0].ResolvedKind)
	require.Equal(t, 2, evidence.Provenance[0].InjectedItemIndex)
}
