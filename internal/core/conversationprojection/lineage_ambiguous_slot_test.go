package conversationprojection_test

// Spec: b-leg-path-virtualization post-certification review, defect (P2) —
// ambiguous correspondence between structurally indistinguishable legacy slots.
// Requirements 5.9, 5.10; design.md "4A. Transform-Safe Conversation-View
// Reassertion" (the correspondence must be unambiguous, or declined).
//
// WHAT THIS FILE PROVES
//
// proveTrajectoryLineage establishes the correspondence as the IDENTITY on
// canonical trajectory slot order, and its comparison deliberately excludes
// payload bytes because those are exactly what a backend-only content rewrite is
// allowed to change. Those two facts together leave one hole: two legacy
// messages of the SAME structural shape are interchangeable under that
// comparison, so exchanging them satisfies every slot-wise check while moving
// the logical message each slot stands for.
//
// A frozen anchor that names one of such a pair can therefore be carried onto
// the wrong message: the proof accepts the trajectory, the anchor is re-derived
// from whatever now occupies the frozen slot, and the steering overlay silently
// moves to a different message instead of the configured AnchorFailClosed
// denial that requirements.md 5.10 asks for.
//
// The two tests below pin the decision in both directions:
//
//   - the AMBIGUOUS case declines carry-forward and lets the untouched
//     exact-resolution / anchor-missing policy decide (ErrAnchorMissing under
//     AnchorFailClosed). Payload bytes cannot break the tie - any two same-shaped
//     slots are compatible under SOME correspondence - so the ambiguity is
//     genuinely undecidable from the descriptors and declining is correct;
//   - the UNAMBIGUOUS control, which contains the very same pair of same-shaped
//     messages and swaps them, still carries its placement forward, proving the
//     guard is scoped to the ANCHORED slot's distinguishability rather than being
//     a blanket refusal for any trajectory that happens to hold two same-shaped
//     messages. That control is what keeps requirement 5.9 satisfiable.
//
// NOTHING HERE FORMATS CONTENT. Every payload token is a fixed fixture string
// with no path, alias, workspace tag, or argument document in it, and every
// failure message carries counts and stage-free ordinals only.

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/require"
)

const (
	ambOverlayID     = "ov-amb"
	ambFixtureReason = "ambiguous_slot_fixture"
	ambRewrittenTag  = "-rewritten"

	ambAlphaText = "amb-alpha"
	ambBetaText  = "amb-beta"
	ambLocalText = "amb-local-only"
	ambSteerText = "amb-steering"
)

// ambSameShapedPair returns the two legacy user text messages the ambiguous
// fixtures share. Their text differs, so their content-derived identities
// differ, while every field the structural descriptor compares - role, part
// cardinality, part kind, and the reference/tool identity fields, all empty here
// - is identical. That is what makes them indistinguishable to the proof and
// distinguishable to MessageIdentityOf.
func ambSameShapedPair() (alpha, beta aipapi.Message) {
	return textMessage(aipapi.RoleUser, ambAlphaText), textMessage(aipapi.RoleUser, ambBetaText)
}

// ambLocalMessage is the never_backend message. It carries the same structural
// descriptor as the pair above, which is deliberate: it proves the guard reads
// the FROZEN filtered baseline (where this message is absent) rather than any
// unfiltered trajectory.
func ambLocalMessage() aipapi.Message { return textMessage(aipapi.RoleUser, ambLocalText) }

// ambSnapshot is the frozen per-turn conversation view: one never_backend tag
// plus one active after-message steering overlay anchored on the PRE-rewrite
// identity of the anchored message under the strict fail-closed policy.
func ambSnapshot(t *testing.T, anchorID, localID conversationprojection.MessageIdentity) conversationprojection.Snapshot {
	t.Helper()
	anchor := conversationprojection.MessageAnchor{Identity: anchorID, Occurrence: 1}
	return conversationprojection.Snapshot{
		StateRevision: 1,
		NeverBackend:  []conversationprojection.Tag{{Identity: localID, Reason: ambFixtureReason}},
		Steering: []conversationprojection.Overlay{{
			OverlayID:   ambOverlayID,
			Revision:    1,
			SlotOrdinal: 1,
			Active:      true,
			Message:     conversationprojection.OverlayMessage{Role: aipapi.RoleSystem, Text: ambSteerText},
			Placement: conversationprojection.Placement{
				Kind:   conversationprojection.PlacementAfterMessage,
				Anchor: &anchor,
			},
			AnchorMissingPolicy: conversationprojection.AnchorFailClosed,
		}},
	}
}

// ambFixture bundles the frozen request-local evidence Reassert receives, built
// through the same early-projection sequence the executor performs.
type ambFixture struct {
	snap      conversationprojection.Snapshot
	stored    conversationprojection.MessageAnchor
	projected aipapi.Call
	evidence  []conversationprojection.OverlayProvenance
	filtered  aipapi.Call
}

// ambFixtureFor builds the fixture from the ordered complete messages that
// precede the never_backend one. The FIRST of them is the anchored message, so a
// caller states the trajectory it needs and the frozen baseline follows.
func ambFixtureFor(t *testing.T, messages []aipapi.Message) ambFixture {
	t.Helper()
	local := ambLocalMessage()
	ingress := aipapi.Call{Messages: append(append([]aipapi.Message(nil), messages...), local)}

	anchorID := mustIdentityForMsg(t, messages[0])
	localID := mustIdentityForMsg(t, local)
	snap := ambSnapshot(t, anchorID, localID)
	projected, evidence, filtered := tsEarly(t, ingress, snap)
	require.Len(t, evidence.Provenance, 1, "fixture: early projection must resolve the single overlay")
	require.Equal(t, conversationprojection.PlacementAfterMessage, evidence.Provenance[0].ResolvedKind)
	require.NotNil(t, evidence.Provenance[0].ResolvedAnchor)
	require.Equal(t, conversationprojection.MessageAnchor{Identity: anchorID, Occurrence: 1}, *evidence.Provenance[0].ResolvedAnchor,
		"fixture: early projection must carry the stored anchor forward verbatim")
	return ambFixture{
		snap:      snap,
		stored:    conversationprojection.MessageAnchor{Identity: anchorID, Occurrence: 1},
		projected: projected,
		evidence:  evidence.Provenance,
		filtered:  filtered,
	}
}

// ambSwapSameShapedUserTexts exchanges the two same-shaped user messages and
// rewrites BOTH payloads, which is exactly what a backend-only content rewrite
// combined with a reorder of same-shaped messages produces: identical structural
// descriptors at every slot, drifted identities, and one member of the pair now
// standing where the other used to be.
func ambSwapSameShapedUserTexts(t *testing.T, call aipapi.Call) aipapi.Call {
	t.Helper()
	out := aipapi.CloneCall(call)
	alphaAt, betaAt := -1, -1
	for i := range out.Messages {
		if len(out.Messages[i].Parts) != 1 || out.Messages[i].Parts[0].Kind != aipapi.PartText {
			continue
		}
		switch out.Messages[i].Parts[0].Text {
		case ambAlphaText:
			alphaAt = i
		case ambBetaText:
			betaAt = i
		}
	}
	require.GreaterOrEqual(t, alphaAt, 0, "fixture: the first same-shaped message must exist")
	require.GreaterOrEqual(t, betaAt, 0, "fixture: the second same-shaped message must exist")
	out.Messages[alphaAt], out.Messages[betaAt] = out.Messages[betaAt], out.Messages[alphaAt]
	out.Messages[alphaAt].Parts[0].Text = ambBetaText + ambRewrittenTag
	out.Messages[betaAt].Parts[0].Text = ambAlphaText + ambRewrittenTag
	return out
}

// ambSwapSingleUserText exchanges the same-shaped pair with the anchored message
// and rewrites the anchored payload only. It is the controlled non-vacuity twin
// of ambSwapSameShapedUserTexts: the anchored message's identity drifts, so
// carry-forward is the only remaining path, and the two same-shaped user
// messages are left exactly where they were.
func ambSwapSingleUserText(t *testing.T, call aipapi.Call, anchored, rewritten aipapi.Message) aipapi.Call {
	t.Helper()
	out := aipapi.CloneCall(call)
	found := false
	for i := range out.Messages {
		if tsMessageEqual(out.Messages[i], anchored) {
			out.Messages[i] = rewritten
			found = true
			break
		}
	}
	require.True(t, found, "fixture: the anchored message must exist in the projected call")
	return out
}

// ambIndexOfText returns the trajectory index of the single message whose first
// part text is exactly text, or -1.
func ambIndexOfText(call aipapi.Call, text string) int {
	combined := append(append([]aipapi.Message(nil), call.Instructions...), call.Messages...)
	for i, m := range combined {
		for _, p := range m.Parts {
			if p.Kind == aipapi.PartText && p.Text == text {
				return i
			}
		}
	}
	return -1
}

// TestReassert_RefusesCarryForwardWhenTheAnchoredSlotIsNotDistinguishable is the
// RED for the ambiguity defect: an anchored legacy message that shares its
// structural descriptor with another slot of the same region has no frozen
// position the proof can pin, so the placement must be declined rather than
// carried onto whichever message now occupies that ordinal.
//
// Without the fix the assembler-free path here is the one the review reproduced:
// proveTrajectoryLineage accepts the swapped trajectory, the anchor is re-derived
// from the message that took the anchored slot, and the overlay is placed after
// that WRONG message with no error at all.
func TestReassert_RefusesCarryForwardWhenTheAnchoredSlotIsNotDistinguishable(t *testing.T) {
	t.Parallel()

	alpha, beta := ambSameShapedPair()
	fx := ambFixtureFor(t, []aipapi.Message{alpha, beta})
	require.Equal(t, 2, len(fx.filtered.Messages),
		"fixture: the frozen baseline must hold exactly the anchored message and the one same-shaped partner")

	// Non-vacuity, part one: the anchored message really is one of a pair that the
	// structural descriptor cannot tell apart. If the descriptors differed this
	// case would be an ordinary distinguishable reorder and would be expected to
	// carry forward.
	partner := fx.filtered.Messages[1]
	require.Equal(t, alpha.Role, partner.Role)
	require.Equal(t, len(alpha.Parts), len(partner.Parts))
	require.Equal(t, alpha.Parts[0].Kind, partner.Parts[0].Kind)
	require.NotEqual(t, alpha.Parts[0].Text, partner.Parts[0].Text,
		"fixture: the pair must differ in payload, or their identities would collide instead of the descriptors matching")

	cleaned := ambSwapSameShapedUserTexts(t, fx.projected)

	// Non-vacuity, part two: the stored anchor genuinely stopped resolving, so
	// carry-forward is the only route that could still place this overlay.
	cleanedAlpha := mustIdentityForMsg(t, textMessage(aipapi.RoleUser, ambAlphaText+ambRewrittenTag))
	require.NotEqual(t, fx.stored.Identity, cleanedAlpha,
		"fixture: the rewritten anchored payload must drift the stored identity")

	got, ev, err := conversationprojection.Reassert(cleaned, fx.snap, fx.evidence, fx.filtered)
	require.Error(t, err,
		"requirements.md 5.10 - a same-shaped slot pair leaves the correspondence ambiguous, so no placement may be carried onto the ordinal")
	require.ErrorIs(t, err, conversationprojection.ErrAnchorMissing,
		"requirements.md 5.10 - the configured anchor-missing policy must stay authoritative when the descriptors cannot distinguish the messages")
	require.Nil(t, ev, "a declined carry-forward must publish no evidence")
	require.Equal(t, aipapi.Call{}, got, "no placement may be relocated by ordinal")
}

// TestReassert_StillCarriesAPlacementWhoseAnchoredSlotIsDistinguishable is the
// scoping control for the guard above, and the reason the guard declines only
// the ambiguous slot instead of the whole trajectory.
//
// The fixture holds the SAME pair of same-shaped user messages and swaps them,
// so an implementation that refused every trajectory containing two same-shaped
// messages would fail here. The anchored message is structurally unique, so its
// correspondence is pinned and the placement must still be carried forward onto
// the very message the frozen anchor named.
func TestReassert_StillCarriesAPlacementWhoseAnchoredSlotIsDistinguishable(t *testing.T) {
	t.Parallel()

	alpha, beta := ambSameShapedPair()
	fx := ambFixtureFor(t, []aipapi.Message{tsJSONAnchorMessage(), alpha, beta})
	cleaned := ambSwapSameShapedUserTexts(t, fx.projected)
	cleaned = ambSwapSingleUserText(t, cleaned, tsJSONAnchorMessage(), tsJSONRewrittenAnchorMessage())
	require.NotEqual(t, fx.stored.Identity, tsAnchorIdentityOf(t, cleaned, aipapi.PartJSON, tsCallID),
		"fixture: the stored anchor must genuinely stop resolving, or exact resolution would place the overlay and prove nothing")

	got, ev, err := conversationprojection.Reassert(cleaned, fx.snap, fx.evidence, fx.filtered)
	require.NoError(t, err,
		"requirements.md 5.9 - a structurally unique anchored slot stays carryable even beside same-shaped messages that were reordered")
	require.NotNil(t, ev)
	steerAt := ambIndexOfText(got, ambSteerText)
	require.Equal(t, 1, steerAt, "exactly one steering copy must be present")
	anchorAt := tsAnchorIndex(t, got, aipapi.PartJSON, tsCallID)
	require.GreaterOrEqual(t, anchorAt, 0, "the anchored message must still be present")
	require.Equal(t, anchorAt+1, steerAt,
		"the overlay must land immediately after the anchored message itself, not after whichever message took its frozen slot")
}
