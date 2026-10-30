package conversationprojection_test

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/require"
)

// F4: optional IDs leave same-shaped items interchangeable under the payload-free
// proof. A correct exact anchor must win over an unproven ordinal correspondence.
func TestReassert_IDlessItemReorderRetainsExactBoundary(t *testing.T) {
	t.Parallel()
	ingress := aipapi.Call{Items: []aipapi.Item{idlessTextItem("original anchor"), idlessTextItem("sibling")}}
	identity, err := conversationprojection.ItemIdentityOf(ingress.Items[0])
	require.NoError(t, err)
	snap := tsSnapshot(t, identity, "", tsOverlayID)
	snap.NeverBackend = nil
	projected, early, frozen := tsEarly(t, ingress, snap)
	stored, err := json.Marshal(snap)
	require.NoError(t, err)
	frozenEvidence, err := json.Marshal(early)
	require.NoError(t, err)

	cleaned := aipapi.CloneCall(projected)
	cleaned.Items[0], cleaned.Items[2] = cleaned.Items[2], cleaned.Items[0]
	require.NoError(t, cleaned.Validate())
	got, ev, err := conversationprojection.Reassert(cleaned, snap, early.Provenance, frozen)
	require.NoError(t, err)
	want := []aipapi.Item{ingress.Items[1], ingress.Items[0], projected.Items[1]}
	require.Equal(t, want, got.Items, "steering must follow the original message, not its former ordinal")
	require.Len(t, ev.Provenance, 1)
	require.Equal(t, early.Provenance[0].ResolvedAnchor, ev.Provenance[0].ResolvedAnchor)
	require.Equal(t, 2, ev.Provenance[0].InjectedTrajectoryIndex)
	after, err := json.Marshal(snap)
	require.NoError(t, err)
	require.Equal(t, stored, after)
	after, err = json.Marshal(early)
	require.NoError(t, err)
	require.Equal(t, frozenEvidence, after)
}

func TestReassert_IDlessItemCarryRequiresDistinguishableAnchor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		sibling  func() aipapi.Item
		policy   conversationprojection.AnchorMissingPolicy
		refused  bool
		fallback bool
	}{
		{name: "ambiguous_fail_closed", refused: true},
		{name: "ambiguous_prefix_fallback", fallback: true, policy: conversationprojection.AnchorStablePrefixFallback},
		{name: "distinct_role", sibling: func() aipapi.Item {
			it := idlessTextItem("sibling")
			it.Role = aipapi.RoleAssistant
			return it
		}},
		{name: "distinct_part_kind", sibling: func() aipapi.Item {
			it := idlessTextItem(`{"value":1}`)
			it.Content[0].Kind = aipapi.ContentPartJSON
			return it
		}},
		{name: "distinct_part_count", sibling: func() aipapi.Item {
			it := idlessTextItem("sibling")
			it.Content = append(it.Content, aipapi.ContentPart{Kind: aipapi.ContentPartText, Text: "second part"})
			return it
		}},
		{name: "distinct_optional_id", sibling: func() aipapi.Item {
			it := idlessTextItem("sibling")
			it.ID = "stable-sibling"
			return it
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sibling := idlessTextItem("sibling")
			if tc.sibling != nil {
				sibling = tc.sibling()
			}
			// Duplicate siblings intentionally remain ambiguous with each other:
			// only the anchored slot must be distinguishable, not every item.
			other := aipapi.CloneCall(aipapi.Call{Items: []aipapi.Item{sibling}}).Items[0]
			if other.ID != "" {
				other.ID = "other-stable-sibling"
			}
			ingress := aipapi.Call{Items: []aipapi.Item{idlessTextItem("original anchor"), sibling, other}}
			identity, err := conversationprojection.ItemIdentityOf(ingress.Items[0])
			require.NoError(t, err)
			snap := tsSnapshot(t, identity, "", tsOverlayID)
			snap.NeverBackend = nil
			if tc.fallback {
				snap.Steering[0].AnchorMissingPolicy = tc.policy
			}
			projected, early, frozen := tsEarly(t, ingress, snap)
			cleaned := aipapi.CloneCall(projected)
			cleaned.Items[0].Content[0].Text = "rewritten anchor"
			require.NoError(t, cleaned.Validate())
			rewritten, err := conversationprojection.ItemIdentityOf(cleaned.Items[0])
			require.NoError(t, err)
			require.NotEqual(t, identity, rewritten)
			got, ev, err := conversationprojection.Reassert(cleaned, snap, early.Provenance, frozen)
			if tc.refused {
				require.ErrorIs(t, err, conversationprojection.ErrAnchorMissing)
				require.Equal(t, aipapi.Call{}, got)
				require.Nil(t, ev)
				return
			}
			require.NoError(t, err)
			require.NoError(t, got.Validate())
			require.Len(t, ev.Provenance, 1)
			if tc.fallback {
				require.Equal(t, []aipapi.Item{projected.Items[1], cleaned.Items[0], sibling, other}, got.Items)
				require.Equal(t, conversationprojection.PlacementStablePrefix, ev.Provenance[0].ResolvedKind)
				require.Len(t, ev.Fallbacks, 1)
			} else {
				require.Equal(t, cleaned.Items, got.Items)
				require.Equal(t, &conversationprojection.MessageAnchor{Identity: rewritten, Occurrence: 1}, ev.Provenance[0].ResolvedAnchor)
				require.Equal(t, 1, ev.Provenance[0].InjectedTrajectoryIndex)
			}
			require.Equal(t, identity, snap.Steering[0].Placement.Anchor.Identity)
			require.Equal(t, identity, early.Provenance[0].ResolvedAnchor.Identity)
			require.Equal(t, "original anchor", frozen.Items[0].Content[0].Text)
		})
	}
}

func idlessTextItem(text string) aipapi.Item {
	return aipapi.Item{
		Kind:    aipapi.ItemKindMessage,
		Role:    aipapi.RoleUser,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: text}},
	}
}
