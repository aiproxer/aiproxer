package conversationprojection_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Handoff fix-steering-tool-anchor-adaptation: VerifyAdaptationPreservesProjection
// resolved an AfterMessage anchor by message identity in the adapted trajectory.
// A legacy assistant tool-call message projects to tool_call items across
// authority adaptation, so its message identity cannot resolve in item space and
// every cross-authority candidate with such an anchor failed closed even though
// the adapted trajectory keeps the pair adjacent with the overlay after the
// output. The verification must map the anchor to its adapted tool_call span
// via stable call IDs and check the same placement point there.

// adaptToOrderedItems runs the production legacy-to-item authority adaptation
// used for OpenResponses-style candidates.
func adaptToOrderedItems(t *testing.T, call aipapi.Call) aipapi.Call {
	t.Helper()
	target := aipapi.DefaultLegacyProjectionTarget(
		aipapi.NewBackendCaps(aipapi.CapabilityOrderedItems),
		aipapi.ReasoningReplaySupport{},
	)
	adapted, err := aipapi.AdaptCallForCandidate(aipapi.CloneCall(call), target)
	require.NoError(t, err)
	require.True(t, adapted.HasItemAuthority(), "adaptation must yield item authority")
	return adapted
}

// adaptedItemKinds renders the adapted trajectory as role/kind tokens for
// hard-coded order assertions.
func adaptedItemKinds(items []aipapi.Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		switch it.Kind {
		case aipapi.ItemKindMessage:
			out = append(out, string(it.Role)+":message")
		default:
			out = append(out, string(it.Kind))
		}
	}
	return out
}

// adaptedSteeringIndexes locates every injected steering copy in adapted items
// by its hard-coded overlay text.
func adaptedSteeringIndexes(t *testing.T, items []aipapi.Item) []int {
	t.Helper()
	var found []int
	for i, it := range items {
		if it.Kind != aipapi.ItemKindMessage || len(it.Content) != 1 {
			continue
		}
		if it.Content[0].Kind == aipapi.ContentPartText && it.Content[0].Text == toolAdjacencySteerText {
			found = append(found, i)
		}
	}
	require.NotEmpty(t, found, "adapted trajectory must contain the steering copies")
	return found
}

// TestVerifyAdaptation_ToolCallAnchorAcrossAuthority pins the fix: a legacy
// AfterMessage overlay anchored to a tool-call message verifies after
// authority adaptation, with the steering copy after the last answering tool
// result in item space.
func TestVerifyAdaptation_ToolCallAnchorAcrossAuthority(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		callIDs   []string
		after     []aipapi.Message
		wantKinds []string
		wantSteer []int
		overlays  int
	}{
		{
			name:      "single_result",
			callIDs:   []string{"c1"},
			after:     []aipapi.Message{toolResultMessage("c1")},
			wantKinds: []string{"user:message", "tool_call", "tool_result", "developer:message", "user:message"},
			wantSteer: []int{3},
			overlays:  1,
		},
		{
			name:      "all_results_one_message",
			callIDs:   []string{"c1", "c2"},
			after:     []aipapi.Message{toolResultMessage("c1", "c2")},
			wantKinds: []string{"user:message", "tool_call", "tool_call", "tool_result", "tool_result", "developer:message", "user:message"},
			wantSteer: []int{5},
			overlays:  1,
		},
		{
			name:      "incomplete_results_follow_last_present",
			callIDs:   []string{"c1", "c2", "c3"},
			after:     []aipapi.Message{toolResultMessage("c1"), toolResultMessage("c3")},
			wantKinds: []string{"user:message", "tool_call", "tool_call", "tool_call", "tool_result", "tool_result", "developer:message", "user:message"},
			wantSteer: []int{6},
			overlays:  1,
		},
		{
			name:      "two_overlays_share_one_tool_anchor",
			callIDs:   []string{"c1"},
			after:     []aipapi.Message{toolResultMessage("c1")},
			wantKinds: []string{"user:message", "tool_call", "tool_result", "developer:message", "developer:message", "user:message"},
			wantSteer: []int{3, 4},
			overlays:  2,
		},
		{
			name:    "non_contiguous_results_end_the_run",
			callIDs: []string{"c1", "c2"},
			after: []aipapi.Message{
				toolResultMessage("c1"),
				textMessage(aipapi.RoleUser, "note"),
				toolResultMessage("c2"),
			},
			wantKinds: []string{"user:message", "tool_call", "tool_call", "tool_result", "developer:message", "user:message", "tool_result", "user:message"},
			wantSteer: []int{4},
			overlays:  1,
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
			overlays := make([]conversationprojection.Overlay, 0, tc.overlays)
			for i := range tc.overlays {
				ov := afterMessageToolOverlay(t, anchor)
				ov.OverlayID = "ov-tool-adapt"
				if i > 0 {
					ov.OverlayID = "ov-tool-adapt-two"
				}
				ov.SlotOrdinal = uint64(i + 1)
				overlays = append(overlays, ov)
			}
			snap := snapshotWithTagsAndOverlays(nil, overlays)

			reasserted, evidence, err := conversationprojection.Project(call, snap)
			require.NoError(t, err)
			require.Len(t, evidence.Provenance, tc.overlays)
			adapted := adaptToOrderedItems(t, reasserted)

			require.Equal(t, tc.wantKinds, adaptedItemKinds(adapted.Items),
				"adaptation must keep call and output adjacent with steering after")
			require.Equal(t, tc.wantSteer, adaptedSteeringIndexes(t, adapted.Items),
				"the steering copies must sit right after the last answering result")

			require.NoError(t,
				conversationprojection.VerifyAdaptationPreservesProjection(reasserted, adapted, snap, evidence.Provenance),
				"a tool-call anchor must verify across authority adaptation at its result-side placement point")
		})
	}
}

// TestVerifyAdaptation_ToolAnchorMissingCallFailsClosed pins the fail-closed
// half: when the adapted trajectory has no tool_call for the anchor's stable
// IDs, verification must keep refusing rather than placing by guess.
func TestVerifyAdaptation_ToolAnchorMissingCallFailsClosed(t *testing.T) {
	t.Parallel()
	anchor := toolCallMessage("c1")
	call := aipapi.Call{Messages: []aipapi.Message{
		textMessage(aipapi.RoleUser, "u0"),
		anchor,
		toolResultMessage("c1"),
		textMessage(aipapi.RoleUser, "tail"),
	}}
	snap := snapshotWithTagsAndOverlays(nil, []conversationprojection.Overlay{afterMessageToolOverlay(t, anchor)})
	reasserted, evidence, err := conversationprojection.Project(call, snap)
	require.NoError(t, err)
	adapted := adaptToOrderedItems(t, reasserted)

	tampered := aipapi.CloneCall(adapted)
	kept := tampered.Items[:0]
	for _, it := range tampered.Items {
		if it.Kind == aipapi.ItemKindToolCall {
			continue
		}
		kept = append(kept, it)
	}
	tampered.Items = kept

	err = conversationprojection.VerifyAdaptationPreservesProjection(reasserted, tampered, snap, evidence.Provenance)
	require.Error(t, err, "an unmappable tool anchor must keep failing closed")
	require.ErrorIs(t, err, conversationprojection.ErrProjectionFailed)
}

// TestVerifyAdaptation_PlainAnchorAcrossAuthorityUnchanged guards the existing
// path: a non-tool anchor keeps verifying by identity across adaptation.
func TestVerifyAdaptation_PlainAnchorAcrossAuthorityUnchanged(t *testing.T) {
	t.Parallel()
	anchor := textMessage(aipapi.RoleAssistant, "reply")
	call := aipapi.Call{Messages: []aipapi.Message{
		textMessage(aipapi.RoleUser, "u0"),
		anchor,
		textMessage(aipapi.RoleUser, "tail"),
	}}
	snap := snapshotWithTagsAndOverlays(nil, []conversationprojection.Overlay{afterMessageToolOverlay(t, anchor)})
	reasserted, evidence, err := conversationprojection.Project(call, snap)
	require.NoError(t, err)
	adapted := adaptToOrderedItems(t, reasserted)
	require.Equal(t,
		[]string{"user:message", "assistant:message", "developer:message", "user:message"},
		adaptedItemKinds(adapted.Items))
	require.NoError(t,
		conversationprojection.VerifyAdaptationPreservesProjection(reasserted, adapted, snap, evidence.Provenance),
		"ordinary anchors must keep verifying exactly as before")
}

// TestVerifyAdaptation_DuplicateCallIDsStayFailClosed pins the ambiguity rule:
// when the anchor's stable IDs match more than one adapted tool_call, the span
// cannot be identified and verification keeps refusing. The duplicate is placed
// BEFORE the anchor on purpose: without the uniqueness rule the mapping would
// land on the anchor's own (last) span and wrongly verify, so this ordering is
// what makes the rule load-bearing.
func TestVerifyAdaptation_DuplicateCallIDsStayFailClosed(t *testing.T) {
	t.Parallel()
	anchor := toolCallMessage("c1")
	duplicate := aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
		Kind:       aipapi.PartJSON,
		ToolCallID: "c1",
		ToolName:   "w",
		Content:    []byte(`{"other":true}`),
	}}}
	call := aipapi.Call{Messages: []aipapi.Message{
		textMessage(aipapi.RoleUser, "u0"),
		duplicate,
		toolResultMessage("c1"),
		anchor,
		toolResultMessage("c1"),
		textMessage(aipapi.RoleUser, "tail"),
	}}
	snap := snapshotWithTagsAndOverlays(nil, []conversationprojection.Overlay{afterMessageToolOverlay(t, anchor)})
	reasserted, evidence, err := conversationprojection.Project(call, snap)
	require.NoError(t, err, "projection itself stays contiguous-based and resolves")
	adapted := adaptToOrderedItems(t, reasserted)

	err = conversationprojection.VerifyAdaptationPreservesProjection(reasserted, adapted, snap, evidence.Provenance)
	require.Error(t, err, "duplicate stable call IDs must keep the fail-closed outcome")
	require.ErrorIs(t, err, conversationprojection.ErrProjectionFailed)
}

// TestVerifyAdaptation_ToolCallAnchorWithoutResult pins the no-result branch of the
// adapted mapping: when no tool_result item answers the anchor's calls, the placement
// point is the last anchor tool_call item, so an overlay projected immediately after a
// pending tool call verifies across authority adaptation instead of failing closed.
func TestVerifyAdaptation_ToolCallAnchorWithoutResult(t *testing.T) {
	t.Parallel()
	anchor := toolCallMessage("c1")
	call := aipapi.Call{
		Messages: []aipapi.Message{
			textMessage(aipapi.RoleUser, "u0"),
			anchor,
			textMessage(aipapi.RoleUser, "tail"),
		},
		Tools: []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
	}
	snap := snapshotWithTagsAndOverlays(nil, []conversationprojection.Overlay{afterMessageToolOverlay(t, anchor)})
	reasserted, evidence, err := conversationprojection.Project(call, snap)
	require.NoError(t, err)
	require.Len(t, evidence.Provenance, 1)
	require.NotNil(t, evidence.Provenance[0].InjectedLegacyIndex)
	require.Equal(t, 2, *evidence.Provenance[0].InjectedLegacyIndex,
		"fixture: with no answering result the overlay stays immediately after the anchor")
	adapted := adaptToOrderedItems(t, reasserted)
	require.Equal(t,
		[]string{"user:message", "tool_call", "developer:message", "user:message"},
		adaptedItemKinds(adapted.Items))
	require.NoError(t,
		conversationprojection.VerifyAdaptationPreservesProjection(reasserted, adapted, snap, evidence.Provenance),
		"a pending tool call with no result must verify after its last tool_call item")
}

// TestVerifyAdaptation_LegacyToolCallAnchorResultSidePlacement pins the same-authority
// half of the adjacency invariant: VerifyAdaptationPreservesProjection must resolve a
// legacy tool-call anchor at its result-side placement point, not at the anchor message
// itself, when the trajectory keeps the call and its answering result adjacent.
func TestVerifyAdaptation_LegacyToolCallAnchorResultSidePlacement(t *testing.T) {
	t.Parallel()
	anchor := toolCallMessage("c1")
	call := aipapi.Call{Messages: []aipapi.Message{
		textMessage(aipapi.RoleUser, "u0"),
		anchor,
		toolResultMessage("c1"),
		textMessage(aipapi.RoleUser, "tail"),
	}}
	snap := snapshotWithTagsAndOverlays(nil, []conversationprojection.Overlay{afterMessageToolOverlay(t, anchor)})
	reasserted, evidence, err := conversationprojection.Project(call, snap)
	require.NoError(t, err)
	require.Len(t, evidence.Provenance, 1)
	require.NotNil(t, evidence.Provenance[0].InjectedLegacyIndex)
	require.Equal(t, 3, *evidence.Provenance[0].InjectedLegacyIndex,
		"fixture: the overlay must land after the answering tool result")

	require.NoError(t,
		conversationprojection.VerifyAdaptationPreservesProjection(reasserted, reasserted, snap, evidence.Provenance),
		"a legacy tool-call anchor must verify at its result-side placement point")
}
