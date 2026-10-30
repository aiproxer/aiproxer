package conversationprojection_test

// Spec: b-leg-path-virtualization Task 5.3, generic reassertion seam.
// Requirements 5.2, 5.4, 5.9, 5.10, 8.5.
//
// Design section consulted: "4A. Transform-Stable Conversation-View Reassertion",
// plus "Existing Architecture and Placement" step 6 and
// "Boundary Commitments / Out of Boundary".
//
// WHAT THIS FILE PROVES
//
// Reassert keeps exact identity-based resolution as its normal path. When - and only
// when - the frozen filtered baseline and the cleaned, backend-shaped trajectory
// prove a one-to-one structural correspondence, an after-message placement that
// early projection already resolved is carried through a backend-only payload
// rewrite even though MessageIdentityOf (a content hash) drifted.
//
// The negatives are the load-bearing half. A single overlay on a single message
// with no insertion, deletion, reorder, role/kind change, or stable-ID change
// cannot distinguish "carried forward by proven lineage" from "relocated by
// ordinal", because in that shape the two coincide. Each negative below breaks the
// structural proof while keeping the trajectory length - and therefore the frozen
// anchor's ordinal slot - plausible, so an implementation that relocated by
// position would succeed and be caught here.
//
// IDENTITY EQUALITY IS NOT THE PROOF. The direction that decides the design is this:
// whenever the anchored message's payload is rewritten, MessageIdentityOf drifts, so an
// identity-equality proof would refuse EVERY legitimate rewrite and requirements.md
// 5.9 would be unsatisfiable. Substituting an identity comparison for the structural
// one is therefore caught below by the positive cases. In the other direction identity
// equality is merely WEAKER than the structural proof - AtomOfMessage projects a legacy
// PartToolResult from its Text field only and drops the stable tool identity, so two
// messages differing only there share one identity - which is why the structural
// comparison must still compare every stable identity identity cannot see.
//
// Nothing here formats a path, an alias, a workspace tag, an identity, an overlay
// ID, or an anchor string. Every failure message carries booleans, counts, and
// stage-free ordinals only, and every identity is compared rather than printed.

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// tsRealRoot is a long POSIX workspace root, so a reserved alias would be
	// strictly shorter. The rewritten payload carries the same fixed alias in every
	// fixture; only its PRESENCE is asserted, never its value.
	tsRealRoot = "/home/dev/workspaces/aip-transform-stable-regression/packages/agent-runtime"
	tsAlias    = "/.__aip_v1__/w_0123456789abcdefghij/"

	tsFirstUserText = "ts-first-user"
	tsLocalText     = "ts-local-only"
	tsTailText      = "ts-tail-user"
	tsSteerText     = "ts-steering"
	tsSteerTextTwo  = "ts-steering-two"

	tsOverlayID     = "ov-ts"
	tsOverlayIDTwo  = "ov-ts-two"
	tsFixtureReason = "transform_stable_fixture"

	tsToolName  = "read_file"
	tsCallID    = "ts-call-1"
	tsResultID  = "ts-result-1"
	tsPathField = "file_path"
	tsLimit     = 10
	tsResultKey = "bytes"
	tsResultNum = 42

	tsItemAnchorID = "item-anchor-1"
	tsItemFirstID  = "item-first-1"
	tsItemTailID   = "item-tail-1"
)

// tsArgumentDocument is the anchored legacy PartJSON argument document in real-root form.
func tsArgumentDocument() []byte {
	return []byte(`{"` + tsPathField + `":"` + tsRealRoot + `/src/anchor.go","limit":` +
		strconv.Itoa(tsLimit) + `}`)
}

// tsVirtualArgumentDocument is the same document after a backend-only prefix rewrite.
func tsVirtualArgumentDocument() []byte {
	return []byte(`{"` + tsPathField + `":"` + tsAlias + `src/anchor.go","limit":` +
		strconv.Itoa(tsLimit) + `}`)
}

// tsStructuredResultDocument is the anchored legacy PartToolResult structured Content
// document in real-root form.
func tsStructuredResultDocument() []byte {
	return []byte(`{"` + tsPathField + `":"` + tsRealRoot + `/src/result.go","` +
		tsResultKey + `":` + strconv.Itoa(tsResultNum) + `}`)
}

// tsVirtualStructuredResultDocument is the same document after a backend-only prefix rewrite.
func tsVirtualStructuredResultDocument() []byte {
	return []byte(`{"` + tsPathField + `":"` + tsAlias + `src/result.go","` +
		tsResultKey + `":` + strconv.Itoa(tsResultNum) + `}`)
}

// tsJSONAnchorMessage is the stock-reachable drifting surface: a historical tool call
// whose selected argument member a backend-only rewrite virtualizes. Its identity is a
// content hash over that member, so the rewrite drifts it.
func tsJSONAnchorMessage() aipapi.Message {
	return aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
		Kind:       aipapi.PartJSON,
		ToolCallID: tsCallID,
		ToolName:   tsToolName,
		Content:    tsArgumentDocument(),
	}}}
}

// tsJSONRewrittenAnchorMessage is tsJSONAnchorMessage after the backend-only rewrite.
func tsJSONRewrittenAnchorMessage() aipapi.Message {
	return aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
		Kind:       aipapi.PartJSON,
		ToolCallID: tsCallID,
		ToolName:   tsToolName,
		Content:    tsVirtualArgumentDocument(),
	}}}
}

// tsOpaqueResultAnchorMessage is the legacy PartToolResult opaque-text surface. Its
// identity projects from the Text field, so a rewrite of that line drifts it.
func tsOpaqueResultAnchorMessage() aipapi.Message {
	return aipapi.Message{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
		Kind:       aipapi.PartToolResult,
		ToolCallID: tsResultID,
		ToolName:   tsToolName,
		Text:       tsRealRoot + "/src/result.go",
	}}}
}

// tsOpaqueResultRewrittenAnchorMessage is tsOpaqueResultAnchorMessage after the rewrite.
func tsOpaqueResultRewrittenAnchorMessage() aipapi.Message {
	return aipapi.Message{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
		Kind:       aipapi.PartToolResult,
		ToolCallID: tsResultID,
		ToolName:   tsToolName,
		Text:       tsAlias + "src/result.go",
	}}}
}

// tsStructuredResultAnchorMessage is the legacy PartToolResult structured-Content surface.
// AtomOfMessage projects a PartToolResult from its Text field only, so rewriting its
// structured Content changes model-visible bytes with ZERO identity drift.
func tsStructuredResultAnchorMessage() aipapi.Message {
	return aipapi.Message{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
		Kind:       aipapi.PartToolResult,
		ToolCallID: tsResultID,
		ToolName:   tsToolName,
		Text:       "structured-result-summary",
		Content:    tsStructuredResultDocument(),
	}}}
}

// tsStructuredResultRewrittenAnchorMessage is tsStructuredResultAnchorMessage after the
// structured-member rewrite.
func tsStructuredResultRewrittenAnchorMessage() aipapi.Message {
	return aipapi.Message{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
		Kind:       aipapi.PartToolResult,
		ToolCallID: tsResultID,
		ToolName:   tsToolName,
		Text:       "structured-result-summary",
		Content:    tsVirtualStructuredResultDocument(),
	}}}
}

// tsItemAnchor returns the item-authority anchored message item.
func tsItemAnchor(document []byte) aipapi.Item {
	return aipapi.Item{
		Kind:   aipapi.ItemKindMessage,
		ID:     tsItemAnchorID,
		Status: aipapi.ItemStatusCompleted,
		Role:   aipapi.RoleAssistant,
		Content: []aipapi.ContentPart{{
			Kind: aipapi.ContentPartJSON,
			Text: string(document),
		}},
	}
}

// tsFirstUserMessage and tsTailMessage are the path-free control messages. They keep
// their identity across every rewrite, which is what makes a rewrite of the anchored
// message attributable to that message alone.
func tsFirstUserMessage() aipapi.Message {
	return textMessage(aipapi.RoleUser, tsFirstUserText)
}

func tsTailMessage() aipapi.Message {
	return textMessage(aipapi.RoleUser, tsTailText)
}

func tsLocalMessage() aipapi.Message {
	return textMessage(aipapi.RoleUser, tsLocalText)
}

// tsIngressCall is the legacy-authority ingress call around one anchored message.
func tsIngressCall(anchor aipapi.Message) aipapi.Call {
	return aipapi.Call{Messages: []aipapi.Message{
		tsFirstUserMessage(),
		anchor,
		tsLocalMessage(),
		tsTailMessage(),
	}}
}

// tsItemIngressCall is the item-authority ingress call around one anchored message item.
func tsItemIngressCall(anchor []byte) aipapi.Call {
	return aipapi.Call{Items: []aipapi.Item{
		{
			Kind: aipapi.ItemKindMessage, ID: tsItemFirstID, Status: aipapi.ItemStatusCompleted,
			Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: tsFirstUserText}},
		},
		tsItemAnchor(anchor),
		{
			Kind: aipapi.ItemKindMessage, ID: "item-local-1", Status: aipapi.ItemStatusCompleted,
			Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: tsLocalText}},
		},
		{
			Kind: aipapi.ItemKindMessage, ID: tsItemTailID, Status: aipapi.ItemStatusCompleted,
			Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: tsTailText}},
		},
	}}
}

// tsItemLocalIdentity is the never_backend-tagged item's identity.
func tsItemLocalIdentity(t *testing.T) conversationprojection.MessageIdentity {
	t.Helper()
	id, err := conversationprojection.ItemIdentityOf(aipapi.Item{
		Kind: aipapi.ItemKindMessage, ID: "item-local-1", Status: aipapi.ItemStatusCompleted,
		Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: tsLocalText}},
	})
	require.NoError(t, err)
	return id
}

// tsSnapshot builds the frozen per-turn conversation view: one never_backend tag plus
// the requested after-message steering overlays, anchored on the PRE-rewrite identity
// of the anchored message under the strict fail-closed policy.
func tsSnapshot(t *testing.T, anchorID conversationprojection.MessageIdentity, localID conversationprojection.MessageIdentity, overlayIDs ...string) conversationprojection.Snapshot {
	t.Helper()
	anchor := conversationprojection.MessageAnchor{Identity: anchorID, Occurrence: 1}
	overlays := make([]conversationprojection.Overlay, 0, len(overlayIDs))
	for i, id := range overlayIDs {
		text := tsSteerText
		if i > 0 {
			text = tsSteerTextTwo
		}
		overlays = append(overlays, conversationprojection.Overlay{
			OverlayID:   id,
			Revision:    1,
			SlotOrdinal: uint64(i + 1),
			Active:      true,
			Message:     conversationprojection.OverlayMessage{Role: aipapi.RoleSystem, Text: text},
			Placement: conversationprojection.Placement{
				Kind:   conversationprojection.PlacementAfterMessage,
				Anchor: &anchor,
			},
			AnchorMissingPolicy: conversationprojection.AnchorFailClosed,
		})
	}
	return conversationprojection.Snapshot{
		StateRevision: 1,
		NeverBackend:  []conversationprojection.Tag{{Identity: localID, Reason: tsFixtureReason}},
		Steering:      overlays,
	}
}

// tsEarly reconstructs the executor's early-projection sequence exactly as
// executor_prepare_secure.go performs it: Project over the UNFILTERED ingress call is
// the anchor-resolution authority, and FilterNeverBackend over the same call is the
// frozen filtered baseline handed to Reassert.
func tsEarly(t *testing.T, ingress aipapi.Call, snap conversationprojection.Snapshot) (projected aipapi.Call, evidence *conversationprojection.ProjectionEvidence, filtered aipapi.Call) {
	t.Helper()
	require.NoError(t, ingress.Validate())
	projected, evidence, err := conversationprojection.Project(ingress, snap)
	require.NoError(t, err, "early projection must resolve the after-message anchor from A-leg/client truth")
	filtered, err = conversationprojection.FilterNeverBackend(ingress, snap)
	require.NoError(t, err)
	return projected, evidence, filtered
}

// tsPlacement measures one call's trajectory placement of the steering copies relative
// to the anchored complete message. It locates the anchor by part kind and stable
// tool-call ID rather than by ordinal, so the measurement is independent of how many
// messages precede it.
func tsPlacement(call aipapi.Call, anchorKind aipapi.PartKind, anchorID string) (copies int, immediatelyAfter bool) {
	combined := append(append([]aipapi.Message(nil), call.Instructions...), call.Messages...)
	anchorAt, steerAt := -1, -1
	for i, msg := range combined {
		for _, part := range msg.Parts {
			switch {
			case part.Kind == aipapi.PartText && part.Text == tsSteerText && steerAt < 0:
				steerAt = i
				copies++
			case part.Kind == aipapi.PartText && part.Text == tsSteerTextTwo && steerAt < 0:
				steerAt = i
				copies++
			case part.Kind == anchorKind && part.ToolCallID == anchorID && anchorAt < 0:
				anchorAt = i
			}
		}
	}
	return copies, anchorAt >= 0 && steerAt == anchorAt+1
}

// tsLegacyFixture bundles the frozen request-local evidence Reassert receives.
type tsLegacyFixture struct {
	ingress   aipapi.Call
	snap      conversationprojection.Snapshot
	stored    conversationprojection.MessageAnchor
	projected aipapi.Call
	evidence  []conversationprojection.OverlayProvenance
	filtered  aipapi.Call
}

func tsLegacyFixtureFor(t *testing.T, anchor aipapi.Message, overlayIDs ...string) tsLegacyFixture {
	t.Helper()
	ingress := tsIngressCall(anchor)
	anchorIdentity := mustIdentityForMsg(t, anchor)
	localIdentity := mustIdentityForMsg(t, tsLocalMessage())
	snap := tsSnapshot(t, anchorIdentity, localIdentity, overlayIDs...)
	projected, evidence, filtered := tsEarly(t, ingress, snap)
	require.Len(t, evidence.Provenance, len(overlayIDs))
	for _, p := range evidence.Provenance {
		require.Equal(t, conversationprojection.PlacementAfterMessage, p.ResolvedKind)
		require.NotNil(t, p.ResolvedAnchor)
		require.True(t, p.ResolvedAnchor.Identity == anchorIdentity && p.ResolvedAnchor.Occurrence == 1,
			"early projection must carry the stored anchor forward verbatim, not a re-derived one")
	}
	return tsLegacyFixture{
		snap:      snap,
		stored:    conversationprojection.MessageAnchor{Identity: anchorIdentity, Occurrence: 1},
		projected: projected,
		evidence:  evidence.Provenance,
		filtered:  filtered,
	}
}

// TestReassert_CarriesResolvedPlacementAcrossProvenOneToOneRewrite is requirements.md 5.9
// at the pure seam, for both canonical authorities and all three drifting surfaces.
//
// The legacy PartJSON and legacy PartToolResult opaque-text surfaces both drift the
// anchored message's identity, and Reassert must still succeed and place the overlay
// immediately after the same LOGICAL message. The legacy PartToolResult
// structured-Content surface is the third case: it changes model-visible bytes with NO
// identity drift, so the repair must not be gated on drift either.
func TestReassert_CarriesResolvedPlacementAcrossProvenOneToOneRewrite(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		label        string
		anchor       aipapi.Message
		rewritten    aipapi.Message
		kind         aipapi.PartKind
		anchorID     string
		wantIdentity bool
	}{
		{
			label:        "legacy_part_json_argument",
			anchor:       tsJSONAnchorMessage(),
			rewritten:    tsJSONRewrittenAnchorMessage(),
			kind:         aipapi.PartJSON,
			anchorID:     tsCallID,
			wantIdentity: true,
		},
		{
			label:        "legacy_part_tool_result_opaque_text",
			anchor:       tsOpaqueResultAnchorMessage(),
			rewritten:    tsOpaqueResultRewrittenAnchorMessage(),
			kind:         aipapi.PartToolResult,
			anchorID:     tsResultID,
			wantIdentity: true,
		},
		{
			label:        "legacy_part_tool_result_structured_content",
			anchor:       tsStructuredResultAnchorMessage(),
			rewritten:    tsStructuredResultRewrittenAnchorMessage(),
			kind:         aipapi.PartToolResult,
			anchorID:     tsResultID,
			wantIdentity: false,
		},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()

			fx := tsLegacyFixtureFor(t, tc.anchor, tsOverlayID)
			cleaned := tsReplaceAnchor(t, fx.projected, tc.anchor, tc.rewritten)

			// The rewrite really happened and really is confined to the anchored message.
			anchorBefore, ok := tsFindAnchor(fx.projected, tc.kind, tc.anchorID)
			require.True(t, ok)
			anchorAfter, ok := tsFindAnchor(cleaned, tc.kind, tc.anchorID)
			require.True(t, ok)
			drifted := mustIdentityForMsg(t, anchorBefore) != mustIdentityForMsg(t, anchorAfter)
			require.Equal(t, tc.wantIdentity, drifted,
				"fixture: whether MessageIdentityOf drifts on this surface is the premise the structural proof must not depend on")
			require.True(t, mustIdentityForMsg(t, tsFirstUserMessage()) == mustIdentityForMsg(t, tsFirstMessageOf(t, cleaned)),
				"fixture: the path-free control message must keep its identity across the rewrite")

			reasserted, ev, err := conversationprojection.Reassert(cleaned, fx.snap, fx.evidence, fx.filtered)
			require.NoError(t, err, "requirements.md 5.9 - a provably one-to-one structure-preserving rewrite must not invalidate the resolved placement")

			copies, immediatelyAfter := tsPlacement(reasserted, tc.kind, tc.anchorID)
			require.Equal(t, 1, copies, "design.md \"4A\" - overlay lifecycle is unchanged: exactly one projection-owned copy")
			require.True(t, immediatelyAfter,
				"requirements.md 5.9 - the resolved placement must be CARRIED through the rewrite, never relocated")

			// The frozen never_backend tag stays authoritative and is not bypassed by
			// carry-forward.
			for _, msg := range reasserted.Messages {
				require.NotEqual(t, tsLocalText, tsMessageText(msg), "design.md \"4A\" constraint 6 - never_backend filtering must survive carry-forward")
			}
			require.NoError(t, reasserted.Validate(), "requirements.md 8.5 - canonical validation after every mutation")

			// The returned provenance must describe THIS call, because downstream
			// candidate-adaptation verification resolves the anchor against it.
			require.NotNil(t, ev)
			require.Len(t, ev.Provenance, 1)
			require.NotNil(t, ev.Provenance[0].ResolvedAnchor)
			_, found, err := resolveAnchorAgainst(reasserted, tc.kind, tc.anchorID, *ev.Provenance[0].ResolvedAnchor)
			require.NoError(t, err)
			require.True(t, found,
				"the carried provenance must name the anchored message in the backend-shaped call so adaptation can verify the placement")

			require.NoError(t, conversationprojection.VerifyAdaptationPreservesProjection(reasserted, reasserted, fx.snap, ev.Provenance),
				"candidate adaptation of a carried placement must still satisfy full projection")
		})
	}
}

// TestReassert_CarriesResolvedPlacementForItemAuthority is requirements.md 5.9 on the
// item authority, where the canonical unit is an item and its stable identity is
// carried by the item ID plus the ordered content-part kinds.
func TestReassert_CarriesResolvedPlacementForItemAuthority(t *testing.T) {
	t.Parallel()

	document := tsArgumentDocument()
	virtual := tsVirtualArgumentDocument()
	ingress := tsItemIngressCall(document)
	anchorIdentity, err := conversationprojection.ItemIdentityOf(tsItemAnchor(document))
	require.NoError(t, err)
	snap := tsSnapshot(t, anchorIdentity, tsItemLocalIdentity(t), tsOverlayID)
	projected, evidence, filtered := tsEarly(t, ingress, snap)

	cleaned := aipapi.CloneCall(projected)
	drifted := false
	for i := range cleaned.Items {
		if cleaned.Items[i].ID != tsItemAnchorID {
			continue
		}
		cleaned.Items[i].Content[0].Text = string(virtual)
	}
	cleanedID, err := conversationprojection.ItemIdentityOf(tsItemAnchor(virtual))
	require.NoError(t, err)
	drifted = cleanedID != anchorIdentity
	require.True(t, drifted, "fixture: rewriting the anchored item's JSON document must drift its content-derived identity")

	reasserted, ev, err := conversationprojection.Reassert(cleaned, snap, evidence.Provenance, filtered)
	require.NoError(t, err, "requirements.md 5.9 on item authority")

	// The overlay is a synthesized system message item right after the anchored item.
	anchorAt, steerAt := -1, -1
	steerCopies := 0
	for i, it := range reasserted.Items {
		if it.ID == tsItemAnchorID {
			anchorAt = i
		}
		if it.Kind == aipapi.ItemKindMessage && len(it.Content) == 1 &&
			it.Content[0].Kind == aipapi.ContentPartText && it.Content[0].Text == tsSteerText {
			steerAt = i
			steerCopies++
		}
	}
	require.NotEqual(t, -1, anchorAt)
	require.Equal(t, 1, steerCopies)
	require.Equal(t, anchorAt+1, steerAt, "requirements.md 5.9 - item authority placement must be carried, not relocated")
	require.NoError(t, reasserted.Validate())
	require.NotNil(t, ev)
	require.NoError(t, conversationprojection.VerifyAdaptationPreservesProjection(reasserted, reasserted, snap, ev.Provenance))
}

// TestReassert_RefusesCarryForwardOnAmbiguousLineage is requirements.md 5.10 sentence 2.
//
// Every case breaks the structural proof while keeping the frozen anchor's ordinal
// slot occupied by SOMETHING, so an implementation that relocated the overlay by
// position would silently succeed. Each must therefore keep the existing
// exact-resolution / fail-closed outcome and produce no call at all.
func TestReassert_RefusesCarryForwardOnAmbiguousLineage(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		label  string
		mutate func(t *testing.T, call aipapi.Call) aipapi.Call
		// wantStableIdentity marks a mutation that leaves MessageIdentityOf
		// byte-identical, which is exactly the case identity cannot see and the
		// structural proof has to reject on its own.
		wantStableIdentity bool
		comment            string
	}{
		{
			label:   "trajectory_cardinality_grew_by_insertion",
			mutate:  tsInsertMessage,
			comment: "an inserted complete message breaks one-to-one cardinality",
		},
		{
			label:   "trajectory_cardinality_shrank_by_deletion",
			mutate:  tsDeleteMessage,
			comment: "a deleted complete message breaks one-to-one cardinality",
		},
		{
			label:   "equal_cardinality_ordinal_shift_with_role_change",
			mutate:  tsEqualCardinalityRoleShift,
			comment: "an equal-length trajectory whose first slot changed role keeps the anchor's ordinal plausible while its structure changed",
		},
		{
			label:   "equal_cardinality_reorder",
			mutate:  tsEqualCardinalityReorder,
			comment: "a swap of the anchored message with its predecessor keeps every ordinal occupied but changes the structure at those slots",
		},
		{
			label:   "equal_cardinality_part_kind_change",
			mutate:  tsEqualCardinalityPartKindChange,
			comment: "the anchored message's ordered part kind changed",
		},
		{
			label:   "equal_cardinality_stable_tool_call_id_change",
			mutate:  tsEqualCardinalityToolCallIDChange,
			comment: "the anchored part's stable tool-call ID changed",
		},
		{
			label:              "equal_cardinality_tool_name_change_with_identical_identity",
			mutate:             tsEqualCardinalityToolNameChange,
			wantStableIdentity: true,
			comment:            "AtomOfMessage drops a part's ToolName, so this change leaves MessageIdentityOf byte-identical and only the structural comparison can see it",
		},
		{
			label:   "instruction_message_partition_swap",
			mutate:  tsPartitionSwap,
			comment: "a complete message moved between the instruction and message regions",
		},
		{
			label:   "authority_form_changed",
			mutate:  tsAuthorityChange,
			comment: "the canonical authority form changed between the frozen baseline and the cleaned call",
		},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()

			fx := tsLegacyFixtureFor(t, tsJSONAnchorMessage(), tsOverlayID)
			// Start from a rewritten anchored message, so the stored anchor genuinely
			// stops resolving and only carry-forward could possibly rescue it.
			cleaned := tsReplaceAnchor(t, fx.projected, tsJSONAnchorMessage(), tsJSONRewrittenAnchorMessage())
			before := tsAnchorIdentityOf(t, cleaned, aipapi.PartJSON, tsCallID)
			require.False(t, fx.stored.Identity == before,
				"fixture: the stored anchor must genuinely not resolve against the cleaned call")
			ambiguous := tc.mutate(t, cleaned)
			if tc.wantStableIdentity {
				require.True(t, before == tsAnchorIdentityOf(t, ambiguous, aipapi.PartJSON, tsCallID),
					"fixture: this mutation must leave MessageIdentityOf unchanged, or identity would already be able to see it")
			}

			got, ev, err := conversationprojection.Reassert(ambiguous, fx.snap, fx.evidence, fx.filtered)
			require.Error(t, err, "requirements.md 5.10 - ambiguous lineage must preserve the existing fail-closed behavior (%s)", tc.comment)
			require.ErrorIs(t, err, conversationprojection.ErrAnchorMissing,
				"requirements.md 5.10 - the configured anchor-missing policy must remain authoritative (%s)", tc.comment)
			require.Nil(t, ev, "a refused carry-forward must publish no evidence")
			require.Equal(t, aipapi.Call{}, got,
				"requirements.md 5.10 - no placement may be relocated by ordinal when the structural proof fails (%s)", tc.comment)
		})
	}
}

// TestReassert_StableToolIdentityChangeIsRefusedEvenThoughIdentityCannotSeeIt pins the
// half of the proof that identity cannot supply.
//
// partToNormalized drops a legacy part's ToolName from the normalized atom, so this
// mutation leaves MessageIdentityOf byte-identical while changing the stable tool
// identity the structural proof compares. Identity therefore has no opinion about the
// change at all, and the structural proof must refuse the carry-forward on its own and
// keep the fail-closed outcome.
func TestReassert_StableToolIdentityChangeIsRefusedEvenThoughIdentityCannotSeeIt(t *testing.T) {
	t.Parallel()

	fx := tsLegacyFixtureFor(t, tsJSONAnchorMessage(), tsOverlayID)
	cleaned := tsReplaceAnchor(t, fx.projected, tsJSONAnchorMessage(), tsJSONRewrittenAnchorMessage())
	before := tsAnchorIdentityOf(t, cleaned, aipapi.PartJSON, tsCallID)

	ambiguous := tsEqualCardinalityToolNameChange(t, cleaned)
	after := tsAnchorIdentityOf(t, ambiguous, aipapi.PartJSON, tsCallID)
	require.True(t, before == after,
		"fixture: AtomOfMessage drops a part's ToolName, so this mutation must leave MessageIdentityOf unchanged - that is the whole premise")

	got, ev, err := conversationprojection.Reassert(ambiguous, fx.snap, fx.evidence, fx.filtered)
	require.Error(t, err, "requirements.md 5.10 - a stable tool-identity change invalidates lineage even though no identity changed")
	require.ErrorIs(t, err, conversationprojection.ErrAnchorMissing)
	require.Nil(t, ev)
	require.Equal(t, aipapi.Call{}, got, "no placement may be relocated by ordinal")
}

// TestReassert_PayloadOnlyChangeAlongsideTheDriftIsStillCarried is the direction that
// makes an identity-keyed proof unusable: a payload-only rewrite of a message the anchor
// does NOT name leaves that message's content-derived identity changed, and it must not
// stop the already-drifting anchor's placement from being carried. Substituting an
// identity comparison for the structural one fails this test and every positive case.
func TestReassert_PayloadOnlyChangeAlongsideTheDriftIsStillCarried(t *testing.T) {
	t.Parallel()

	fx := tsLegacyFixtureFor(t, tsJSONAnchorMessage(), tsOverlayID)
	// Rewrite a message that the anchor does NOT name, then also drift the anchored
	// message, so the trajectory contains both a payload-only change and a drift.
	cleaned := tsReplaceAnchor(t, fx.projected, tsJSONAnchorMessage(), tsJSONRewrittenAnchorMessage())
	control := tsReplaceFirstUserText(t, cleaned, tsFirstUserText+"-rewritten")
	require.False(t, mustIdentityForMsg(t, tsFirstUserMessage()) == mustIdentityForMsg(t, tsFirstMessageOf(t, control)),
		"fixture: the control rewrite must change that message's content-derived identity, or the case proves nothing")

	got, ev, err := conversationprojection.Reassert(control, fx.snap, fx.evidence, fx.filtered)
	require.NoError(t, err, "a payload-only change alongside the drift is still a proven one-to-one rewrite")
	require.NotNil(t, ev)
	copies, immediatelyAfter := tsPlacement(got, aipapi.PartJSON, tsCallID)
	require.Equal(t, 1, copies)
	require.True(t, immediatelyAfter)
}

// TestReassert_RefusesCarryForwardForItemAuthorityAmbiguity repeats the negatives on the
// item authority, where the stable identity lives on the item itself.
func TestReassert_RefusesCarryForwardForItemAuthorityAmbiguity(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		label  string
		mutate func(t *testing.T, call aipapi.Call) aipapi.Call
	}{
		{label: "item_inserted", mutate: tsInsertMessage},
		{label: "item_deleted", mutate: tsDeleteMessage},
		{label: "item_stable_id_changed", mutate: tsItemStableIDChange},
		{label: "item_role_changed", mutate: tsItemRoleChange},
		{label: "item_reordered", mutate: tsEqualCardinalityReorder},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()

			document := tsArgumentDocument()
			ingress := tsItemIngressCall(document)
			anchorIdentity, err := conversationprojection.ItemIdentityOf(tsItemAnchor(document))
			require.NoError(t, err)
			snap := tsSnapshot(t, anchorIdentity, tsItemLocalIdentity(t), tsOverlayID)
			projected, evidence, filtered := tsEarly(t, ingress, snap)

			cleaned := aipapi.CloneCall(projected)
			for i := range cleaned.Items {
				if cleaned.Items[i].ID == tsItemAnchorID {
					cleaned.Items[i].Content[0].Text = string(tsVirtualArgumentDocument())
				}
			}
			ambiguous := tc.mutate(t, cleaned)

			got, ev, err := conversationprojection.Reassert(ambiguous, snap, evidence.Provenance, filtered)
			require.Error(t, err, "requirements.md 5.10 on item authority: %s", tc.label)
			require.ErrorIs(t, err, conversationprojection.ErrAnchorMissing)
			require.Nil(t, ev)
			require.Equal(t, aipapi.Call{}, got)
		})
	}
}

// TestReassert_CarryForwardRequiresTheFrozenFilteredBaseline pins the only admissible
// basis. Without the request-local frozen filtered baseline there is no pre-rewrite
// evidence, so nothing may be carried even when the cleaned call looks structurally
// compatible with itself.
func TestReassert_CarryForwardRequiresTheFrozenFilteredBaseline(t *testing.T) {
	t.Parallel()

	fx := tsLegacyFixtureFor(t, tsJSONAnchorMessage(), tsOverlayID)
	cleaned := tsReplaceAnchor(t, fx.projected, tsJSONAnchorMessage(), tsJSONRewrittenAnchorMessage())

	got, ev, err := conversationprojection.Reassert(cleaned, fx.snap, fx.evidence, aipapi.Call{})
	require.Error(t, err, "design.md \"4A\" constraint 3 - carry-forward has no basis without the frozen baseline")
	require.ErrorIs(t, err, conversationprojection.ErrAnchorMissing)
	require.Nil(t, ev)
	require.Equal(t, aipapi.Call{}, got)
}

// TestReassert_ExactResolutionRemainsTheNormalPath keeps stored evidence for an
// unchanged boundary, but not when proven lineage identifies a conflicting boundary.
func TestReassert_ExactResolutionRemainsTheNormalPath(t *testing.T) {
	t.Parallel()

	fx := tsLegacyFixtureFor(t, tsJSONAnchorMessage(), tsOverlayID)

	// (1) No rewrite at all.
	untouched, ev, err := conversationprojection.Reassert(fx.projected, fx.snap, fx.evidence, fx.filtered)
	require.NoError(t, err)
	require.Len(t, ev.Provenance, 1)
	require.NotNil(t, ev.Provenance[0].ResolvedAnchor)
	require.True(t, fx.stored == *ev.Provenance[0].ResolvedAnchor,
		"exact resolution must keep recording the STORED anchor, never a derived one")
	copies, immediatelyAfter := tsPlacement(untouched, aipapi.PartJSON, tsCallID)
	require.Equal(t, 1, copies)
	require.True(t, immediatelyAfter)

	// (2) Proven lineage takes precedence when BOTH an exact match and a carried
	// candidate exist. Two distinguishable legacy PartJSON messages occupy adjacent
	// slots. The anchored slot's payload is rewritten, so its identity drifts and
	// lineage has a candidate for it; the sibling message is rewritten to the
	// anchored message's PRE-rewrite payload, so the stored identity still resolves
	// exactly - at that sibling slot. The distinguishable original slot remains
	// authoritative, and provenance must describe its rewritten content.
	secondCallID := "ts-call-2"
	secondAnchor := tsJSONAnchorMessage()
	secondAnchor.Parts[0].ToolCallID = secondCallID
	collisionIngress := aipapi.Call{Messages: []aipapi.Message{
		tsFirstUserMessage(),
		tsJSONAnchorMessage(),
		secondAnchor,
		tsLocalMessage(),
		tsTailMessage(),
	}}
	anchorIdentity := mustIdentityForMsg(t, tsJSONAnchorMessage())
	collisionSnap := tsSnapshot(t, anchorIdentity, mustIdentityForMsg(t, tsLocalMessage()), tsOverlayID)
	collisionProjected, collisionEvidence, collisionFiltered := tsEarly(t, collisionIngress, collisionSnap)
	stored := conversationprojection.MessageAnchor{Identity: anchorIdentity, Occurrence: 1}

	cleaned := aipapi.CloneCall(collisionProjected)
	for i := range cleaned.Messages {
		if len(cleaned.Messages[i].Parts) != 1 || cleaned.Messages[i].Parts[0].Kind != aipapi.PartJSON {
			continue
		}
		switch cleaned.Messages[i].Parts[0].ToolCallID {
		case tsCallID:
			cleaned.Messages[i].Parts[0].Content = tsVirtualArgumentDocument()
		case secondCallID:
			cleaned.Messages[i].Parts[0].Content = tsArgumentDocument()
		}
	}
	require.NotEqual(t, stored.Identity, tsAnchorIdentityOf(t, cleaned, aipapi.PartJSON, tsCallID),
		"fixture: the anchored slot's payload rewrite must drift its identity so lineage has a candidate")
	require.Equal(t, stored.Identity, tsAnchorIdentityOf(t, cleaned, aipapi.PartJSON, secondCallID),
		"fixture: the sibling rewrite must reproduce the stored identity so exact resolution still finds a match")

	reasserted, ev2, err := conversationprojection.Reassert(cleaned, collisionSnap, collisionEvidence.Provenance, collisionFiltered)
	require.NoError(t, err)
	require.Len(t, ev2.Provenance, 1)
	require.NotNil(t, ev2.Provenance[0].ResolvedAnchor)
	wantEffective := conversationprojection.MessageAnchor{
		Identity: tsAnchorIdentityOf(t, cleaned, aipapi.PartJSON, tsCallID), Occurrence: 1,
	}
	require.Equal(t, wantEffective, *ev2.Provenance[0].ResolvedAnchor)
	require.Equal(t, stored, *collisionSnap.Steering[0].Placement.Anchor, "stored evidence is immutable")
	copies, immediatelyAfter = tsPlacement(reasserted, aipapi.PartJSON, tsCallID)
	require.Equal(t, 1, copies)
	require.True(t, immediatelyAfter,
		"proven lineage must retain the original boundary despite a conflicting exact sibling")
	_, afterSibling := tsPlacement(reasserted, aipapi.PartJSON, secondCallID)
	require.False(t, afterSibling)
}

// Requirements 3.4: rewriting only the middle duplicate must not move steering
// to the last duplicate, even though stored occurrence 2 still resolves there.
func TestReassert_PreservesRewrittenMiddleBoundaryAcrossOccurrenceDrift(t *testing.T) {
	t.Parallel()
	for _, items := range []bool{false, true} {
		name := "legacy"
		if items {
			name = "items"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var ingress aipapi.Call
			for _, id := range []string{"result-first", "result-middle", "result-last"} {
				if items {
					ingress.Items = append(ingress.Items, aipapi.Item{
						Kind: aipapi.ItemKindMessage, ID: id, Status: aipapi.ItemStatusCompleted,
						Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "same result"}},
					})
				} else {
					ingress.Messages = append(ingress.Messages, aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
						Kind: aipapi.PartToolResult, ToolCallID: id, ToolName: tsToolName, Text: "same result",
					}}})
				}
			}
			var identity conversationprojection.MessageIdentity
			var err error
			if items {
				identity, err = conversationprojection.ItemIdentityOf(ingress.Items[1])
			} else {
				identity, err = conversationprojection.MessageIdentityOf(ingress.Messages[1])
			}
			require.NoError(t, err)
			snap := tsSnapshot(t, identity, mustIdentityForMsg(t, tsLocalMessage()), tsOverlayID)
			snap.Steering[0].Placement.Anchor.Occurrence = 2
			stored := *snap.Steering[0].Placement.Anchor
			projected, early, frozen := tsEarly(t, ingress, snap)
			cleaned := aipapi.CloneCall(projected)
			var rewritten conversationprojection.MessageIdentity
			if items {
				cleaned.Items[1].Content[0].Text = "rewritten result"
				rewritten, err = conversationprojection.ItemIdentityOf(cleaned.Items[1])
			} else {
				cleaned.Messages[1].Parts[0].Text = "rewritten result"
				rewritten, err = conversationprojection.MessageIdentityOf(cleaned.Messages[1])
			}
			require.NoError(t, err)
			require.NotEqual(t, identity, rewritten)
			got, ev, err := conversationprojection.Reassert(cleaned, snap, early.Provenance, frozen)
			require.NoError(t, err)
			// Reassert must change no content or order: only restore the original
			// steering boundary already present in the rewritten projected call.
			if items {
				require.Equal(t, cleaned.Items, got.Items)
			} else {
				require.Equal(t, cleaned.Messages, got.Messages)
			}
			require.Len(t, ev.Provenance, 1)
			require.Equal(t, &conversationprojection.MessageAnchor{Identity: rewritten, Occurrence: 1}, ev.Provenance[0].ResolvedAnchor)
			require.Equal(t, 2, ev.Provenance[0].InjectedTrajectoryIndex)
			require.Equal(t, stored, *snap.Steering[0].Placement.Anchor)
			require.Equal(t, &stored, early.Provenance[0].ResolvedAnchor)

			// Project has no lineage evidence and must still use exact occurrence 2.
			unprojected := aipapi.CloneCall(cleaned)
			if items {
				unprojected.Items = append(unprojected.Items[:2], unprojected.Items[3:]...)
			} else {
				unprojected.Messages = append(unprojected.Messages[:2], unprojected.Messages[3:]...)
			}
			_, exact, err := conversationprojection.Project(unprojected, snap)
			require.NoError(t, err)
			require.Equal(t, &stored, exact.Provenance[0].ResolvedAnchor)
			require.Equal(t, 3, exact.Provenance[0].InjectedTrajectoryIndex)
		})
	}
}

// TestReassert_StablePrefixOverlaysAreUnaffected pins design.md "4A"'s last constraint:
// a stable-prefix overlay does not depend on message identity, so it must resolve
// exactly and unchanged while a sibling after-message overlay is carried forward.
func TestReassert_StablePrefixOverlaysAreUnaffected(t *testing.T) {
	t.Parallel()

	anchor := tsJSONAnchorMessage()
	anchorIdentity := mustIdentityForMsg(t, anchor)
	snap := tsSnapshot(t, anchorIdentity, mustIdentityForMsg(t, tsLocalMessage()), tsOverlayID, tsOverlayIDTwo)
	snap.Steering[1].Placement = conversationprojection.Placement{Kind: conversationprojection.PlacementStablePrefix}
	// Put the stable overlay first so it is the lower slot ordinal, matching a view
	// that mixes both placements.
	snap.Steering[0].SlotOrdinal = 2
	snap.Steering[1].SlotOrdinal = 1

	ingress := tsIngressCall(anchor)
	projected, evidence, filtered := tsEarly(t, ingress, snap)
	require.Len(t, evidence.Provenance, 2)

	cleaned := tsReplaceAnchor(t, projected, anchor, tsJSONRewrittenAnchorMessage())
	reasserted, ev, err := conversationprojection.Reassert(cleaned, snap, evidence.Provenance, filtered)
	require.NoError(t, err, "a stable-prefix overlay must not need carry-forward, and must not be blocked by it")
	require.Len(t, ev.Provenance, 2)
	// The stable-prefix overlay lives in the instruction region and the carried
	// after-message overlay immediately follows the anchored message in the message
	// region. Each is located independently, because the two share a trajectory.
	anchorAt := tsAnchorIndex(t, reasserted, aipapi.PartJSON, tsCallID)
	afterAt := tsTextIndex(reasserted, tsSteerText)
	stableAt := tsTextIndex(reasserted, tsSteerTextTwo)
	require.NotEqual(t, -1, afterAt)
	require.NotEqual(t, -1, stableAt)
	require.Less(t, stableAt, anchorAt, "a stable-prefix overlay stays ahead of the anchored message")
	require.Equal(t, anchorAt+1, afterAt,
		"the after-message overlay is the only one that needed carry-forward, and it must land immediately after the anchor")
	var stableFound bool
	for _, p := range ev.Provenance {
		switch p.ResolvedKind {
		case conversationprojection.PlacementStablePrefix:
			stableFound = true
			require.Nil(t, p.ResolvedAnchor)
		case conversationprojection.PlacementAfterMessage:
			require.NotNil(t, p.ResolvedAnchor)
			_, found, err := resolveAnchorAgainst(reasserted, aipapi.PartJSON, tsCallID, *p.ResolvedAnchor)
			require.NoError(t, err)
			require.True(t, found,
				"the carried after-message provenance must name the rewritten anchor in the reasserted call, so downstream adaptation can verify it")
		}
	}
	require.True(t, stableFound, "the stable-prefix placement must remain stable-prefix")
}

// TestReassert_CarriesEveryOverlayAtTheSameDriftedAnchor proves the carry-forward keeps
// the frozen slot ordering when several overlays share one drifted after-message anchor.
func TestReassert_CarriesEveryOverlayAtTheSameDriftedAnchor(t *testing.T) {
	t.Parallel()

	fx := tsLegacyFixtureFor(t, tsJSONAnchorMessage(), tsOverlayID, tsOverlayIDTwo)
	cleaned := tsReplaceAnchor(t, fx.projected, tsJSONAnchorMessage(), tsJSONRewrittenAnchorMessage())

	reasserted, ev, err := conversationprojection.Reassert(cleaned, fx.snap, fx.evidence, fx.filtered)
	require.NoError(t, err)
	require.Len(t, ev.Provenance, 2)

	combined := append(append([]aipapi.Message(nil), reasserted.Instructions...), reasserted.Messages...)
	firstText, secondText := -1, -1
	for i, msg := range combined {
		if len(msg.Parts) == 0 {
			continue
		}
		switch msg.Parts[0].Text {
		case tsSteerText:
			if firstText < 0 {
				firstText = i
			}
		case tsSteerTextTwo:
			if secondText < 0 {
				secondText = i
			}
		}
	}
	require.NotEqual(t, -1, firstText)
	require.NotEqual(t, -1, secondText)
	require.Equal(t, firstText+1, secondText,
		"both overlays of one drifted anchor must land immediately after it in frozen SlotOrdinal order")
	require.NoError(t, conversationprojection.VerifyAdaptationPreservesProjection(reasserted, reasserted, fx.snap, ev.Provenance))
}

// TestReassert_CarryForwardNeverResurrectsANeverBackendMessage pins design.md "4A"
// constraint 6: existing never_backend filtering remains mandatory and must not be
// bypassed by lineage carry-forward. The reintroduced never_backend message is filtered
// out before the lineage proof runs, so the already-resolved placement is still carried
// and the filtered message never reappears in the reasserted call.
func TestReassert_CarryForwardNeverResurrectsANeverBackendMessage(t *testing.T) {
	t.Parallel()

	fx := tsLegacyFixtureFor(t, tsJSONAnchorMessage(), tsOverlayID)
	cleaned := tsReplaceAnchor(t, fx.projected, tsJSONAnchorMessage(), tsJSONRewrittenAnchorMessage())
	reintroduced := aipapi.CloneCall(cleaned)
	reintroduced.Messages = append(reintroduced.Messages, tsLocalMessage())

	reasserted, ev, err := conversationprojection.Reassert(reintroduced, fx.snap, fx.evidence, fx.filtered)
	require.NoError(t, err, "the reintroduced never_backend message is filtered before lineage, so the resolved placement is still carried")
	require.NotNil(t, ev)
	for _, msg := range reasserted.Messages {
		require.NotEqual(t, tsLocalText, tsMessageText(msg),
			"design.md \"4A\" constraint 6 - never_backend filtering must not be bypassed by carry-forward")
	}
	copies, immediatelyAfter := tsPlacement(reasserted, aipapi.PartJSON, tsCallID)
	require.Equal(t, 1, copies)
	require.True(t, immediatelyAfter,
		"the carried placement must still land immediately after the anchored message")
}

// ---------------------------------------------------------------------------
// structural mutations used by the negative regressions
// ---------------------------------------------------------------------------

// tsReplaceFirstUserText rewrites the path-free control message's payload. It changes
// model-visible bytes and that message's content-derived identity while leaving every
// structural descriptor intact, which is the benign shape the proof must accept.
func tsReplaceFirstUserText(t *testing.T, call aipapi.Call, text string) aipapi.Call {
	t.Helper()
	out := aipapi.CloneCall(call)
	for i := range out.Messages {
		if len(out.Messages[i].Parts) == 1 && out.Messages[i].Parts[0].Text == tsFirstUserText {
			out.Messages[i].Parts[0].Text = text
			return out
		}
	}
	t.Fatal("fixture: the path-free control message must exist")
	return out
}

func tsInsertMessage(t *testing.T, call aipapi.Call) aipapi.Call {
	t.Helper()
	out := aipapi.CloneCall(call)
	if out.HasItemAuthority() {
		out.Items = append(out.Items, aipapi.Item{
			Kind: aipapi.ItemKindMessage, ID: "item-extra-1", Status: aipapi.ItemStatusCompleted,
			Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "ts-extra"}},
		})
		return out
	}
	out.Messages = append(out.Messages, textMessage(aipapi.RoleUser, "ts-extra"))
	return out
}

func tsDeleteMessage(t *testing.T, call aipapi.Call) aipapi.Call {
	t.Helper()
	out := aipapi.CloneCall(call)
	if out.HasItemAuthority() {
		out.Items = out.Items[:len(out.Items)-1]
		return out
	}
	out.Messages = out.Messages[:len(out.Messages)-1]
	return out
}

// tsEqualCardinalityRoleShift keeps the trajectory length identical while moving the
// anchored message off its frozen ordinal slot and changing a slot's role. A positional
// implementation would place the overlay after whatever now occupies the frozen slot.
func tsEqualCardinalityRoleShift(t *testing.T, call aipapi.Call) aipapi.Call {
	t.Helper()
	out := aipapi.CloneCall(call)
	if out.HasItemAuthority() {
		out.Items[0].Role = aipapi.RoleSystem
		return out
	}
	// Messages: [first, anchor, steering, tail]; drop the tail and prepend a system
	// message so the length is unchanged and every frozen ordinal is still occupied.
	tail := out.Messages[len(out.Messages)-1]
	out.Messages = append([]aipapi.Message{textMessage(aipapi.RoleSystem, "ts-prepended")}, out.Messages[:len(out.Messages)-1]...)
	require.Equal(t, aipapi.RoleUser, tail.Role)
	return out
}

func tsEqualCardinalityReorder(t *testing.T, call aipapi.Call) aipapi.Call {
	t.Helper()
	out := aipapi.CloneCall(call)
	if out.HasItemAuthority() {
		out.Items[0], out.Items[1] = out.Items[1], out.Items[0]
		return out
	}
	out.Messages[0], out.Messages[1] = out.Messages[1], out.Messages[0]
	return out
}

func tsEqualCardinalityPartKindChange(t *testing.T, call aipapi.Call) aipapi.Call {
	t.Helper()
	out := aipapi.CloneCall(call)
	for i := range out.Messages {
		for j := range out.Messages[i].Parts {
			if out.Messages[i].Parts[j].Kind == aipapi.PartJSON {
				out.Messages[i].Parts[j].Kind = aipapi.PartText
				out.Messages[i].Parts[j].Content = nil
				out.Messages[i].Parts[j].Text = "ts-kind-changed"
				return out
			}
		}
	}
	t.Fatal("fixture: the anchored PartJSON part must exist")
	return out
}

func tsEqualCardinalityToolCallIDChange(t *testing.T, call aipapi.Call) aipapi.Call {
	t.Helper()
	out := aipapi.CloneCall(call)
	for i := range out.Messages {
		for j := range out.Messages[i].Parts {
			if out.Messages[i].Parts[j].Kind == aipapi.PartJSON {
				out.Messages[i].Parts[j].ToolCallID = "ts-call-renamed"
				return out
			}
		}
	}
	t.Fatal("fixture: the anchored PartJSON part must exist")
	return out
}

// tsEqualCardinalityToolNameChange changes only a part's ToolName, which
// partToNormalized drops from the normalized atom for both the legacy PartJSON and
// PartToolResult surfaces. MessageIdentityOf is therefore byte-identical before and
// after, so an identity-keyed proof would accept this trajectory and an ordinal
// relocation would pass unnoticed.
func tsEqualCardinalityToolNameChange(t *testing.T, call aipapi.Call) aipapi.Call {
	t.Helper()
	out := aipapi.CloneCall(call)
	found := false
	for i := range out.Messages {
		for j := range out.Messages[i].Parts {
			if out.Messages[i].Parts[j].ToolName == "" {
				continue
			}
			out.Messages[i].Parts[j].ToolName = "grep"
			found = true
		}
	}
	require.True(t, found, "fixture: the anchored part must carry a stable tool name")
	return out
}

func tsPartitionSwap(t *testing.T, call aipapi.Call) aipapi.Call {
	t.Helper()
	out := aipapi.CloneCall(call)
	if out.HasItemAuthority() {
		t.Fatal("fixture: the partition negative applies to legacy authority only")
	}
	out.Instructions = []aipapi.Message{out.Messages[0]}
	out.Messages = out.Messages[1:]
	return out
}

func tsAuthorityChange(t *testing.T, call aipapi.Call) aipapi.Call {
	t.Helper()
	out := aipapi.CloneCall(call)
	items := make([]aipapi.Item, 0, len(out.Messages)+1)
	for _, msg := range out.Messages {
		item := aipapi.Item{
			Kind: aipapi.ItemKindMessage, Status: aipapi.ItemStatusCompleted, Role: msg.Role,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: tsMessageText(msg)}},
		}
		for _, part := range msg.Parts {
			if part.Kind == aipapi.PartJSON {
				item.Content = []aipapi.ContentPart{{Kind: aipapi.ContentPartJSON, Text: string(part.Content)}}
				break
			}
		}
		items = append(items, item)
	}
	out.Instructions = nil
	out.Messages = nil
	out.Items = items
	return out
}

func tsItemStableIDChange(t *testing.T, call aipapi.Call) aipapi.Call {
	t.Helper()
	out := aipapi.CloneCall(call)
	for i := range out.Items {
		if out.Items[i].ID == tsItemAnchorID {
			out.Items[i].ID = "item-anchor-renamed"
			return out
		}
	}
	t.Fatal("fixture: the anchored item must exist")
	return out
}

func tsItemRoleChange(t *testing.T, call aipapi.Call) aipapi.Call {
	t.Helper()
	out := aipapi.CloneCall(call)
	for i := range out.Items {
		if out.Items[i].ID == tsItemAnchorID {
			out.Items[i].Role = aipapi.RoleTool
			return out
		}
	}
	t.Fatal("fixture: the anchored item must exist")
	return out
}

// ---------------------------------------------------------------------------
// measurement helpers
// ---------------------------------------------------------------------------

func tsReplaceAnchor(t *testing.T, call aipapi.Call, from, to aipapi.Message) aipapi.Call {
	t.Helper()
	out := aipapi.CloneCall(call)
	replaced := false
	for i := range out.Messages {
		if tsMessageEqual(out.Messages[i], from) {
			out.Messages[i] = to
			replaced = true
			break
		}
	}
	require.True(t, replaced, "fixture: the anchored message must exist on the projected call")
	return out
}

func tsFindAnchor(call aipapi.Call, kind aipapi.PartKind, id string) (aipapi.Message, bool) {
	for _, msg := range append(append([]aipapi.Message(nil), call.Instructions...), call.Messages...) {
		for _, part := range msg.Parts {
			if part.Kind == kind && part.ToolCallID == id {
				return msg, true
			}
		}
	}
	return aipapi.Message{}, false
}

func tsAnchorIdentityOf(t *testing.T, call aipapi.Call, kind aipapi.PartKind, id string) conversationprojection.MessageIdentity {
	t.Helper()
	msg, ok := tsFindAnchor(call, kind, id)
	require.True(t, ok)
	return mustIdentityForMsg(t, msg)
}

func tsFirstMessageOf(t *testing.T, call aipapi.Call) aipapi.Message {
	t.Helper()
	require.NotEmpty(t, call.Messages)
	return call.Messages[0]
}

// tsAnchorIndex locates the anchored complete message by part kind and stable
// tool-call ID rather than by ordinal.
func tsAnchorIndex(t *testing.T, call aipapi.Call, kind aipapi.PartKind, id string) int {
	t.Helper()
	combined := append(append([]aipapi.Message(nil), call.Instructions...), call.Messages...)
	for i, msg := range combined {
		for _, part := range msg.Parts {
			if part.Kind == kind && part.ToolCallID == id {
				return i
			}
		}
	}
	return -1
}

func tsTextIndex(call aipapi.Call, text string) int {
	combined := append(append([]aipapi.Message(nil), call.Instructions...), call.Messages...)
	for i, msg := range combined {
		for _, part := range msg.Parts {
			if part.Text == text {
				return i
			}
		}
	}
	return -1
}

func tsMessageText(msg aipapi.Message) string {
	if len(msg.Parts) == 0 {
		return ""
	}
	return msg.Parts[0].Text
}

func tsMessageEqual(a, b aipapi.Message) bool {
	if a.Role != b.Role || len(a.Parts) != len(b.Parts) {
		return false
	}
	for i := range a.Parts {
		if a.Parts[i].Kind != b.Parts[i].Kind || a.Parts[i].Text != b.Parts[i].Text ||
			a.Parts[i].ToolCallID != b.Parts[i].ToolCallID || a.Parts[i].ToolName != b.Parts[i].ToolName ||
			!jsonEqual(a.Parts[i].Content, b.Parts[i].Content) {
			return false
		}
	}
	return true
}

func jsonEqual(a, b json.RawMessage) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	return string(a) == string(b)
}

// resolveAnchorAgainst locates the message item named by an anchor inside one call's
// backend-shaped trajectory, which is exactly what downstream adaptation verification
// does before trusting a provenance entry.
func resolveAnchorAgainst(call aipapi.Call, kind aipapi.PartKind, id string, anchor conversationprojection.MessageAnchor) (int, bool, error) {
	combined := append(append([]aipapi.Message(nil), call.Instructions...), call.Messages...)
	counts := make(map[conversationprojection.MessageIdentity]uint32)
	for i, msg := range combined {
		matches := false
		for _, part := range msg.Parts {
			if part.Kind == kind && part.ToolCallID == id {
				matches = true
				break
			}
		}
		if !matches {
			continue
		}
		identity, err := conversationprojection.MessageIdentityOf(msg)
		if err != nil {
			return -1, false, err
		}
		counts[identity]++
		if identity == anchor.Identity && counts[identity] == anchor.Occurrence {
			return i, true, nil
		}
	}
	return -1, false, nil
}

// TestReassert_LineageRefusalReasonsAreDeterministic runs every negative regression five
// times and requires an identical outcome, so no map-iteration or pointer-ordering
// dependence can hide behind a passing run.
func TestReassert_LineageRefusalReasonsAreDeterministic(t *testing.T) {
	t.Parallel()

	var first strings.Builder
	for run := range 5 {
		var seen strings.Builder
		for _, mutate := range []func(*testing.T, aipapi.Call) aipapi.Call{
			tsInsertMessage,
			tsDeleteMessage,
			tsEqualCardinalityRoleShift,
			tsEqualCardinalityReorder,
			tsEqualCardinalityPartKindChange,
			tsEqualCardinalityToolCallIDChange,
			tsEqualCardinalityToolNameChange,
			tsPartitionSwap,
			tsAuthorityChange,
		} {
			fx := tsLegacyFixtureFor(t, tsJSONAnchorMessage(), tsOverlayID)
			cleaned := tsReplaceAnchor(t, fx.projected, tsJSONAnchorMessage(), tsJSONRewrittenAnchorMessage())
			ambiguous := mutate(t, cleaned)
			got, ev, err := conversationprojection.Reassert(ambiguous, fx.snap, fx.evidence, fx.filtered)
			seen.WriteString(strconv.FormatBool(err != nil))
			seen.WriteString(strconv.FormatBool(errors.Is(err, conversationprojection.ErrAnchorMissing)))
			seen.WriteString(strconv.FormatBool(ev == nil))
			seen.WriteString(strconv.FormatBool(got.Messages == nil && got.Items == nil))
			seen.WriteString(";")
		}
		if run == 0 {
			first.WriteString(seen.String())
			continue
		}
		assert.Equal(t, first.String(), seen.String(),
			"every ambiguity regression must fail closed identically on every run")
	}
}
