package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
	"github.com/stretchr/testify/require"
)

// The interleaved refresh is a system-prompt authority only for demonstrated
// changes in the effective provider-bound system/developer instructions. A
// state-revision mismatch alone is not instruction-change evidence: it can
// also come from ordinary user-role steering, exclusion changes, or metadata
// such as a steering reason that never reaches the provider message. The
// refresh therefore compares the projected instruction content rather than
// trusting the revision or the persisted steering record.

type memoVisibilityProcessor struct{ visible bool }

func (memoVisibilityProcessor) BeginTurn(context.Context, InterleavedTurnInput) (InterleavedTurn, error) {
	return nil, nil
}

func (p memoVisibilityProcessor) IsMemoVisibleToClient(context.Context, string) bool {
	return p.visible
}

func (memoVisibilityProcessor) MemoSteeringPutRequest(string) steering.PutRequest {
	return steering.PutRequest{}
}

func (memoVisibilityProcessor) MemoSteeringOverlayID() steering.OverlayID { return "memo" }

func (memoVisibilityProcessor) IsMemoSteeringOverlay(overlayID string) bool {
	return overlayID == "memo"
}

func memoInstructionOverlay(id, text string, slot uint64, role aipapi.Role) conversationprojection.Overlay {
	return conversationprojection.Overlay{
		OverlayID: id, Revision: 1, SlotOrdinal: slot, Active: true,
		Message:             conversationprojection.OverlayMessage{Role: role, Text: text},
		Placement:           conversationprojection.Placement{Kind: conversationprojection.PlacementStablePrefix},
		AnchorMissingPolicy: conversationprojection.AnchorStablePrefixFallback,
	}
}

// TestRefreshMemoSteeringFacts_GenuineInstructionChangeRecordsCause proves the
// refresh can still attribute a demonstrated system- or developer-instruction
// change once the memo-authority overreach is removed.
func TestRefreshMemoSteeringFacts_GenuineInstructionChangeRecordsCause(t *testing.T) {
	for _, role := range []aipapi.Role{aipapi.RoleSystem, aipapi.RoleDeveloper} {
		t.Run(string(role), func(t *testing.T) {
			t.Parallel()
			ex := memoRefreshExecutor(memoVisibilityProcessor{}, conversationprojection.Snapshot{
				StateRevision: 8,
				Steering:      []conversationprojection.Overlay{memoInstructionOverlay("instruction-change", "new instruction", 1, role)},
			})
			facts := memoRefreshFacts(t, conversationprojection.Snapshot{StateRevision: 7})
			refreshed, ok := ex.refreshMemoSteeringFacts(t.Context(), facts, interleavedstate.State{}, false)
			require.True(t, ok)
			require.Equal(t, causeSystemPrompt, refreshed.baseline.StructuralCause)
		})
	}
}

// TestRefreshMemoSteeringFacts_SuppressedVisibleMemoDoesNotMaskSystemChange
// proves memo suppression only removes the memo overlay. A separate system
// instruction change in the same refreshed snapshot is still attributed.
func TestRefreshMemoSteeringFacts_SuppressedVisibleMemoDoesNotMaskSystemChange(t *testing.T) {
	t.Parallel()
	ex := memoRefreshExecutor(memoVisibilityProcessor{visible: true}, conversationprojection.Snapshot{
		StateRevision: 8,
		Steering: []conversationprojection.Overlay{
			{
				OverlayID: "memo", Revision: 1, SlotOrdinal: 1, Active: true,
				Message:             conversationprojection.OverlayMessage{Role: aipapi.RoleUser, Text: "ordinary memo"},
				Placement:           conversationprojection.Placement{Kind: conversationprojection.PlacementStablePrefix},
				AnchorMissingPolicy: conversationprojection.AnchorStablePrefixFallback,
			},
			memoInstructionOverlay("system-change", "new instruction", 2, aipapi.RoleSystem),
		},
	})
	facts := memoRefreshFacts(t, conversationprojection.Snapshot{StateRevision: 7})
	refreshed, ok := ex.refreshMemoSteeringFacts(t.Context(), facts, interleavedstate.State{}, true)
	require.True(t, ok)
	require.Equal(t, causeSystemPrompt, refreshed.baseline.StructuralCause)
	require.Len(t, refreshed.baseline.Instructions, 2)
	require.Contains(t, refreshed.baseline.Instructions[1].Parts[0].Text, "new instruction")
}

func memoCauseLegacyCall(role aipapi.Role) aipapi.Call {
	return aipapi.Call{
		Instructions: []aipapi.Message{{Role: role, Parts: []aipapi.Part{aipapi.TextPart("standing instruction")}}},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("follow-up")}},
		},
	}
}

func memoCauseItemCall(role aipapi.Role) aipapi.Call {
	return aipapi.Call{
		Items: []aipapi.Item{
			{Kind: aipapi.ItemKindMessage, Role: role, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "standing instruction"}}},
			{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hello"}}},
		},
	}
}

func memoRefreshFacts(t *testing.T, snapshot conversationprojection.Snapshot) recvTurnFacts {
	t.Helper()
	call := memoCauseLegacyCall(aipapi.RoleSystem)
	return memoRefreshFactsWithCall(t, snapshot, call)
}

func memoRefreshFactsWithCall(t *testing.T, snapshot conversationprojection.Snapshot, call aipapi.Call) recvTurnFacts {
	t.Helper()
	return memoRefreshFactsWithIngress(t, snapshot, call, call, nil)
}

func memoRefreshFactsWithIngress(t *testing.T, snapshot conversationprojection.Snapshot, baseline, ingress aipapi.Call, provenance []conversationprojection.OverlayProvenance) recvTurnFacts {
	t.Helper()
	return newRecvTurnFacts(t.Context(), recvTurnFactsInput{
		baseline:               baseline,
		ingressCall:            ingress,
		traceID:                "trace-memo-cause",
		aLegID:                 "a-leg-memo-cause",
		conversationSnapshot:   snapshot,
		conversationProvenance: provenance,
	})
}

func memoRefreshExecutor(processor InterleavedProcessor, snapshot conversationprojection.Snapshot) *Executor {
	ex := TestExecutor()
	ex.InterleavedProcessor = processor
	ex.ConversationViewReader = &countingReaderWithSnap{snap: snapshot, count: &atomic.Int32{}}
	return ex
}

// TestRefreshMemoSteeringFacts_OrdinaryMemoUpdateRecordsNothing proves a
// same-turn memo is not a system-prompt authority. The new user-role overlay
// reaches the refreshed continuation, but the proxy has not demonstrated a
// system or developer instruction change.
func TestRefreshMemoSteeringFacts_OrdinaryMemoUpdateRecordsNothing(t *testing.T) {
	t.Parallel()
	overlay := conversationprojection.Overlay{
		OverlayID: "memo", Revision: 1, SlotOrdinal: 1, Active: true,
		Message:             conversationprojection.OverlayMessage{Role: aipapi.RoleUser, Text: "ordinary memo"},
		Placement:           conversationprojection.Placement{Kind: conversationprojection.PlacementStablePrefix},
		AnchorMissingPolicy: conversationprojection.AnchorStablePrefixFallback,
	}
	ex := memoRefreshExecutor(memoVisibilityProcessor{}, conversationprojection.Snapshot{StateRevision: 8, Steering: []conversationprojection.Overlay{overlay}})
	facts := memoRefreshFacts(t, conversationprojection.Snapshot{StateRevision: 7})
	refreshed, ok := ex.refreshMemoSteeringFacts(t.Context(), facts, interleavedstate.State{}, false)
	require.True(t, ok)
	require.Len(t, refreshed.baseline.Instructions, 2, "the standing instruction and memo fixture must reach the refreshed continuation")
	require.Equal(t, aipapi.RoleSystem, refreshed.baseline.Instructions[0].Role)
	require.Equal(t, aipapi.RoleUser, refreshed.baseline.Instructions[1].Role)
	require.Len(t, refreshed.baseline.Messages, 2)
	require.Empty(t, refreshed.baseline.StructuralCause)
}

// TestRefreshMemoSteeringFacts_UserExclusionRecordsNothing proves an exclusion
// that leaves the effective system/developer instructions unchanged is not
// instruction-change evidence. The user message is removed from the refreshed
// continuation, but the standing instruction remains.
func TestRefreshMemoSteeringFacts_UserExclusionRecordsNothing(t *testing.T) {
	t.Parallel()
	call := memoCauseLegacyCall(aipapi.RoleSystem)
	helloID, err := conversationprojection.MessageIdentityOf(call.Messages[0])
	require.NoError(t, err)
	ex := memoRefreshExecutor(memoVisibilityProcessor{}, conversationprojection.Snapshot{
		StateRevision: 8,
		NeverBackend:  []conversationprojection.Tag{{Identity: helloID, Reason: "test"}},
	})
	facts := memoRefreshFactsWithCall(t, conversationprojection.Snapshot{StateRevision: 7}, call)
	refreshed, ok := ex.refreshMemoSteeringFacts(t.Context(), facts, interleavedstate.State{}, false)
	require.True(t, ok)
	require.Len(t, refreshed.baseline.Messages, 1, "the excluded user message must leave the refreshed continuation")
	require.Equal(t, "follow-up", refreshed.baseline.Messages[0].Parts[0].Text)
	require.Len(t, refreshed.baseline.Instructions, 1)
	require.Empty(t, refreshed.baseline.StructuralCause)
}

// TestRefreshMemoSteeringFacts_InstructionExclusionRecordsCause proves an
// exclusion that removes a system/developer instruction is instruction-change
// evidence, even when the steering record is unchanged.
func TestRefreshMemoSteeringFacts_InstructionExclusionRecordsCause(t *testing.T) {
	for _, role := range []aipapi.Role{aipapi.RoleSystem, aipapi.RoleDeveloper} {
		t.Run(string(role), func(t *testing.T) {
			t.Parallel()
			call := memoCauseLegacyCall(role)
			instructionID, err := conversationprojection.MessageIdentityOf(call.Instructions[0])
			require.NoError(t, err)
			ex := memoRefreshExecutor(memoVisibilityProcessor{}, conversationprojection.Snapshot{
				StateRevision: 8,
				NeverBackend:  []conversationprojection.Tag{{Identity: instructionID, Reason: "test"}},
			})
			facts := memoRefreshFactsWithCall(t, conversationprojection.Snapshot{StateRevision: 7}, call)
			refreshed, ok := ex.refreshMemoSteeringFacts(t.Context(), facts, interleavedstate.State{}, false)
			require.True(t, ok)
			require.Empty(t, refreshed.baseline.Instructions, "the excluded instruction must leave the refreshed continuation")
			require.Equal(t, causeSystemPrompt, refreshed.baseline.StructuralCause)
		})
	}
}

// TestRefreshMemoSteeringFacts_ItemUserExclusionRecordsNothing covers the same
// exclusion boundary for item authority: removing a user message leaves the
// effective system instruction unchanged.
func TestRefreshMemoSteeringFacts_ItemUserExclusionRecordsNothing(t *testing.T) {
	t.Parallel()
	call := memoCauseItemCall(aipapi.RoleSystem)
	helloID, err := conversationprojection.ItemIdentityOf(call.Items[1])
	require.NoError(t, err)
	ex := memoRefreshExecutor(memoVisibilityProcessor{}, conversationprojection.Snapshot{
		StateRevision: 8,
		NeverBackend:  []conversationprojection.Tag{{Identity: helloID, Reason: "test"}},
	})
	facts := memoRefreshFactsWithCall(t, conversationprojection.Snapshot{StateRevision: 7}, call)
	refreshed, ok := ex.refreshMemoSteeringFacts(t.Context(), facts, interleavedstate.State{}, false)
	require.True(t, ok)
	require.Len(t, refreshed.baseline.Items, 1)
	require.Equal(t, aipapi.RoleSystem, refreshed.baseline.Items[0].Role)
	require.Empty(t, refreshed.baseline.StructuralCause)
}

// TestRefreshMemoSteeringFacts_ItemInstructionExclusionRecordsCause covers an
// instruction removal for item authority.
func TestRefreshMemoSteeringFacts_ItemInstructionExclusionRecordsCause(t *testing.T) {
	for _, role := range []aipapi.Role{aipapi.RoleSystem, aipapi.RoleDeveloper} {
		t.Run(string(role), func(t *testing.T) {
			t.Parallel()
			call := memoCauseItemCall(role)
			instructionID, err := conversationprojection.ItemIdentityOf(call.Items[0])
			require.NoError(t, err)
			ex := memoRefreshExecutor(memoVisibilityProcessor{}, conversationprojection.Snapshot{
				StateRevision: 8,
				NeverBackend:  []conversationprojection.Tag{{Identity: instructionID, Reason: "test"}},
			})
			facts := memoRefreshFactsWithCall(t, conversationprojection.Snapshot{StateRevision: 7}, call)
			refreshed, ok := ex.refreshMemoSteeringFacts(t.Context(), facts, interleavedstate.State{}, false)
			require.True(t, ok)
			require.Len(t, refreshed.baseline.Items, 1)
			require.Equal(t, aipapi.RoleUser, refreshed.baseline.Items[0].Role)
			require.Equal(t, causeSystemPrompt, refreshed.baseline.StructuralCause)
		})
	}
}

// TestRefreshMemoSteeringFacts_ReasonOnlySteeringReplacementRecordsNothing
// uses a real conversation-view store. Only the overlay reason changes, so the
// persisted revision advances while the projected provider-bound instructions
// remain identical. Administrative metadata is not instruction-change evidence.
func TestRefreshMemoSteeringFacts_ReasonOnlySteeringReplacementRecordsNothing(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := conversationview.NewReferenceStore()
	const aLegID = "reason-only-cause"
	require.NoError(t, store.CreateALeg(ctx, aLegID))
	put := func(reason conversationview.ReasonCode) conversationview.SteeringState {
		t.Helper()
		state, err := store.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
			OverlayID:           "reason-only",
			Message:             conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "standing instruction"},
			Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              reason,
		})
		require.NoError(t, err)
		return state
	}
	first := put("initial_reason")
	oldSnap, err := store.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	second := put("refreshed_reason")
	require.Equal(t, first.Revision+1, second.Revision)
	require.Greater(t, second.StateRevision, first.StateRevision)
	newSnap, err := store.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	require.Greater(t, newSnap.StateRevision, oldSnap.StateRevision)

	for _, authority := range []string{"legacy", "item"} {
		t.Run(authority, func(t *testing.T) {
			var ingress aipapi.Call
			if authority == "item" {
				ingress = aipapi.Call{Items: []aipapi.Item{{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hello"}}}}}
			} else {
				ingress = aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}}}
			}
			oldProjected, oldEv, err := conversationprojection.Project(ingress, oldSnap)
			require.NoError(t, err)
			ex := memoRefreshExecutor(memoVisibilityProcessor{}, newSnap)
			facts := memoRefreshFactsWithIngress(t, oldSnap, oldProjected, ingress, oldEv.Provenance)
			refreshed, ok := ex.refreshMemoSteeringFacts(t.Context(), facts, interleavedstate.State{}, false)
			require.True(t, ok)
			instructions := providerInstructions(refreshed.baseline)
			require.Len(t, instructions, 1, "the unchanged instruction must reach the refreshed continuation")
			require.Equal(t, aipapi.RoleSystem, instructionRole(instructions[0]))
			require.Equal(t, "standing instruction", instructionText(instructions[0]))
			require.Empty(t, refreshed.baseline.StructuralCause)
		})
	}
}

func instructionRole(instruction providerInstruction) aipapi.Role {
	if instruction.isItem {
		return instruction.item.Role
	}
	return instruction.message.Role
}

func instructionText(instruction providerInstruction) string {
	if instruction.isItem {
		for _, part := range instruction.item.Content {
			if part.Kind == aipapi.ContentPartText {
				return part.Text
			}
		}
		return ""
	}
	for _, part := range instruction.message.Parts {
		if part.Kind == aipapi.PartText {
			return part.Text
		}
	}
	return ""
}

// TestRefreshMemoSteeringFacts_InstructionMoveRecordsCause proves a genuine
// move of a same-text, same-role steering overlay between two distinct
// after-message anchors is instruction-change evidence in both authorities.
// The projected trajectory actually changes: the instruction follows a
// different anchor message. This uses a real conversation-view store, which
// classifies the replacement as a move.
func TestRefreshMemoSteeringFacts_InstructionMoveRecordsCause(t *testing.T) {
	t.Parallel()
	for _, role := range []aipapi.Role{aipapi.RoleSystem, aipapi.RoleDeveloper} {
		t.Run(string(role), func(t *testing.T) {
			for _, authority := range []string{"legacy", "item"} {
				t.Run(authority, func(t *testing.T) {
					t.Parallel()
					ctx := t.Context()
					store := conversationview.NewReferenceStore()
					const aLegID = "moved-instruction-cause"
					require.NoError(t, store.CreateALeg(ctx, aLegID))

					var ingress aipapi.Call
					var anchorAID, anchorBID conversationprojection.MessageIdentity
					if authority == "item" {
						ingress = aipapi.Call{Items: []aipapi.Item{
							{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "alpha"}}},
							{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "beta"}}},
						}}
						var err error
						anchorAID, err = conversationprojection.ItemIdentityOf(ingress.Items[0])
						require.NoError(t, err)
						anchorBID, err = conversationprojection.ItemIdentityOf(ingress.Items[1])
						require.NoError(t, err)
					} else {
						ingress = aipapi.Call{Messages: []aipapi.Message{
							{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("alpha")}},
							{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("beta")}},
						}}
						var err error
						anchorAID, err = conversationprojection.MessageIdentityOf(ingress.Messages[0])
						require.NoError(t, err)
						anchorBID, err = conversationprojection.MessageIdentityOf(ingress.Messages[1])
						require.NoError(t, err)
					}

					put := func(anchor conversationprojection.MessageAnchor) conversationview.SteeringState {
						state, err := store.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
							OverlayID:           "moved-instruction",
							Message:             conversationview.StoredMessageV1{Role: role, Text: "policy X"},
							Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
							AnchorMissingPolicy: conversationview.AnchorFailClosed,
							Reason:              "move_test",
						})
						require.NoError(t, err)
						return state
					}
					put(conversationprojection.MessageAnchor{Identity: anchorAID, Occurrence: 1})
					oldSnap, err := store.Snapshot(ctx, aLegID)
					require.NoError(t, err)
					moved := put(conversationprojection.MessageAnchor{Identity: anchorBID, Occurrence: 1})
					require.Equal(t, conversationview.CacheDiscontinuityMove, moved.CacheDiscontinuityKind)
					newSnap, err := store.Snapshot(ctx, aLegID)
					require.NoError(t, err)

					oldProjected, oldEv, err := conversationprojection.Project(ingress, oldSnap)
					require.NoError(t, err)
					ex := memoRefreshExecutor(memoVisibilityProcessor{}, newSnap)
					facts := memoRefreshFactsWithIngress(t, oldSnap, oldProjected, ingress, oldEv.Provenance)
					refreshed, ok := ex.refreshMemoSteeringFacts(t.Context(), facts, interleavedstate.State{}, false)
					require.True(t, ok)
					require.Equal(t, causeSystemPrompt, refreshed.baseline.StructuralCause)
					require.Equal(t, []string{"alpha", "beta", "policy X"}, trajectoryTexts(refreshed.baseline, authority == "item"))
				})
			}
		})
	}
}

// trajectoryTexts returns the ordered model-facing texts of a projected call
// for move assertions: legacy messages in order, or item contents in order.
func trajectoryTexts(call aipapi.Call, itemAuthority bool) []string {
	if itemAuthority {
		texts := make([]string, 0, len(call.Items))
		for _, item := range call.Items {
			if len(item.Content) > 0 {
				texts = append(texts, item.Content[0].Text)
			}
		}
		return texts
	}
	texts := make([]string, 0, len(call.Messages))
	for _, message := range call.Messages {
		if len(message.Parts) > 0 {
			texts = append(texts, message.Parts[0].Text)
		}
	}
	return texts
}

// TestRefreshMemoSteeringFacts_FallbackSlotChangeRecordsNothing is the F9
// regression: a store replacement that changes the slot number without
// changing the effective placement must not publish. The overlay moves from
// stable_prefix to after_message with an anchor that is absent from the
// ingress, so projection falls back to stable_prefix; the slot ordinal still
// advances, but the model-facing trajectory is identical. Uses a real
// conversation-view store, for both legacy and item authority.
func TestRefreshMemoSteeringFacts_FallbackSlotChangeRecordsNothing(t *testing.T) {
	t.Parallel()
	for _, authority := range []string{"legacy", "item"} {
		t.Run(authority, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			store := conversationview.NewReferenceStore()
			const aLegID = "fallback-slot-cause"
			require.NoError(t, store.CreateALeg(ctx, aLegID))

			var ingress aipapi.Call
			var absentID conversationprojection.MessageIdentity
			if authority == "item" {
				ingress = aipapi.Call{Items: []aipapi.Item{{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hello"}}}}}
				absent, err := conversationprojection.ItemIdentityOf(aipapi.Item{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "absent"}}})
				require.NoError(t, err)
				absentID = absent
			} else {
				ingress = aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}}}
				absent, err := conversationprojection.MessageIdentityOf(aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("absent")}})
				require.NoError(t, err)
				absentID = absent
			}

			put := func(anchor conversationprojection.MessageAnchor, kind conversationprojection.PlacementKind) conversationview.SteeringState {
				t.Helper()
				placement := conversationview.StoredPlacement{Kind: kind}
				if kind == conversationview.PlacementAfterMessage {
					placement.Anchor = &anchor
				}
				state, err := store.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
					OverlayID:           "fallback-slot",
					Message:             conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "standing instruction"},
					Placement:           placement,
					AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
					Reason:              "fallback_test",
				})
				require.NoError(t, err)
				return state
			}
			first := put(conversationprojection.MessageAnchor{}, conversationview.PlacementStablePrefix)
			oldSnap, err := store.Snapshot(ctx, aLegID)
			require.NoError(t, err)
			oldProjected, oldEv, err := conversationprojection.Project(ingress, oldSnap)
			require.NoError(t, err)

			absent := conversationprojection.MessageAnchor{Identity: absentID, Occurrence: 1}
			second := put(absent, conversationview.PlacementAfterMessage)
			require.Equal(t, conversationview.CacheDiscontinuityMove, second.CacheDiscontinuityKind)
			require.NotEqual(t, first.SlotOrdinal, second.SlotOrdinal, "the placement change must reallocate the slot")
			newSnap, err := store.Snapshot(ctx, aLegID)
			require.NoError(t, err)
			require.Greater(t, newSnap.StateRevision, oldSnap.StateRevision)

			ex := memoRefreshExecutor(memoVisibilityProcessor{}, newSnap)
			facts := memoRefreshFactsWithIngress(t, oldSnap, oldProjected, ingress, oldEv.Provenance)
			refreshed, ok := ex.refreshMemoSteeringFacts(t.Context(), facts, interleavedstate.State{}, false)
			require.True(t, ok)
			before, after := providerInstructions(oldProjected), providerInstructions(refreshed.baseline)
			require.Len(t, before, 1)
			require.Len(t, after, 1)
			require.Equal(t, before, after, "the fallback must project the identical instruction")
			require.Empty(t, refreshed.baseline.StructuralCause)
		})
	}
}

// TestRefreshMemoSteeringFacts_InstructionReorderRecordsCause proves a genuine
// relative reorder of retained system instructions is detected even when the
// instruction content is identical. The two overlays carry the same text, so
// the content comparison is blind; only their projected order changed. In
// item authority the reordered item identities change the provider-bound
// trajectory, so the cause is published. In legacy authority the same reorder
// is byte-identical on the wire, so no cause is published: slot bookkeeping
// alone never attributes.
func TestRefreshMemoSteeringFacts_InstructionReorderRecordsCause(t *testing.T) {
	t.Parallel()
	for _, authority := range []string{"legacy", "item"} {
		t.Run(authority, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			store := conversationview.NewReferenceStore()
			const aLegID = "reorder-cause"
			require.NoError(t, store.CreateALeg(ctx, aLegID))

			var ingress aipapi.Call
			var anchorAID, anchorBID conversationprojection.MessageIdentity
			if authority == "item" {
				ingress = aipapi.Call{Items: []aipapi.Item{
					{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "alpha"}}},
					{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "beta"}}},
				}}
				var err error
				anchorAID, err = conversationprojection.ItemIdentityOf(ingress.Items[0])
				require.NoError(t, err)
				anchorBID, err = conversationprojection.ItemIdentityOf(ingress.Items[1])
				require.NoError(t, err)
			} else {
				ingress = aipapi.Call{Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("alpha")}},
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("beta")}},
				}}
				var err error
				anchorAID, err = conversationprojection.MessageIdentityOf(ingress.Messages[0])
				require.NoError(t, err)
				anchorBID, err = conversationprojection.MessageIdentityOf(ingress.Messages[1])
				require.NoError(t, err)
			}
			anchor := func(id conversationprojection.MessageIdentity) *conversationprojection.MessageAnchor {
				return &conversationprojection.MessageAnchor{Identity: id, Occurrence: 1}
			}
			put := func(id, text string, a *conversationprojection.MessageAnchor) conversationview.SteeringState {
				t.Helper()
				state, err := store.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
					OverlayID:           id,
					Message:             conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: text},
					Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: a},
					AnchorMissingPolicy: conversationview.AnchorFailClosed,
					Reason:              "reorder_test",
				})
				require.NoError(t, err)
				return state
			}
			put("moved-first", "same", anchor(anchorAID))
			put("moved-second", "same", anchor(anchorAID))
			oldSnap, err := store.Snapshot(ctx, aLegID)
			require.NoError(t, err)
			oldProjected, oldEv, err := conversationprojection.Project(ingress, oldSnap)
			require.NoError(t, err)
			// Reallocate the first overlay's slot by moving it away and back.
			// Its text, role and anchor end up identical; only its position
			// among the retained overlays changes.
			put("moved-first", "same", anchor(anchorBID))
			put("moved-first", "same", anchor(anchorAID))
			newSnap, err := store.Snapshot(ctx, aLegID)
			require.NoError(t, err)

			ex := memoRefreshExecutor(memoVisibilityProcessor{}, newSnap)
			facts := memoRefreshFactsWithIngress(t, oldSnap, oldProjected, ingress, oldEv.Provenance)
			refreshed, ok := ex.refreshMemoSteeringFacts(t.Context(), facts, interleavedstate.State{}, false)
			require.True(t, ok)
			if authority == "item" {
				require.Len(t, refreshed.baseline.Items, 4)
				require.Contains(t, refreshed.baseline.Items[1].ID, "moved-second")
				require.Contains(t, refreshed.baseline.Items[2].ID, "moved-first")
				require.Equal(t, causeSystemPrompt, refreshed.baseline.StructuralCause)
			} else {
				require.Equal(t, "same", refreshed.baseline.Messages[1].Parts[0].Text)
				require.Equal(t, "same", refreshed.baseline.Messages[2].Parts[0].Text)
				require.Empty(t, refreshed.baseline.StructuralCause)
			}
		})
	}
}

// TestRefreshMemoSteeringFacts_EquivalentPlacementMoveRecordsNothing proves a
// placement-description change with an identical effective placement records
// no cause. Moving a system overlay from stable_prefix to after_message,
// anchored to the last instruction it already follows, produces the identical
// model-facing trajectory on both sides. Uses a real conversation-view store,
// for both legacy and item authority.
func TestRefreshMemoSteeringFacts_EquivalentPlacementMoveRecordsNothing(t *testing.T) {
	t.Parallel()
	for _, authority := range []string{"legacy", "item"} {
		t.Run(authority, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			store := conversationview.NewReferenceStore()
			const aLegID = "equivalent-placement-cause"
			require.NoError(t, store.CreateALeg(ctx, aLegID))

			var ingress aipapi.Call
			var baseID conversationprojection.MessageIdentity
			if authority == "item" {
				ingress = aipapi.Call{Items: []aipapi.Item{
					{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleSystem, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "Base instruction"}}},
					{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "Hello"}}},
				}}
				var err error
				baseID, err = conversationprojection.ItemIdentityOf(ingress.Items[0])
				require.NoError(t, err)
			} else {
				ingress = aipapi.Call{
					Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("Base instruction")}}},
					Messages:     []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Hello")}}},
				}
				var err error
				baseID, err = conversationprojection.MessageIdentityOf(ingress.Instructions[0])
				require.NoError(t, err)
			}
			put := func(kind conversationprojection.PlacementKind, anchor *conversationprojection.MessageAnchor) conversationview.SteeringState {
				t.Helper()
				placement := conversationview.StoredPlacement{Kind: kind}
				if anchor != nil {
					placement.Anchor = anchor
				}
				state, err := store.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
					OverlayID:           "policy",
					Message:             conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "Policy X"},
					Placement:           placement,
					AnchorMissingPolicy: conversationview.AnchorFailClosed,
					Reason:              "placement_test",
				})
				require.NoError(t, err)
				return state
			}
			put(conversationview.PlacementStablePrefix, nil)
			oldSnap, err := store.Snapshot(ctx, aLegID)
			require.NoError(t, err)
			oldProjected, oldEv, err := conversationprojection.Project(ingress, oldSnap)
			require.NoError(t, err)

			moved := put(conversationview.PlacementAfterMessage, &conversationprojection.MessageAnchor{Identity: baseID, Occurrence: 1})
			require.Equal(t, conversationview.CacheDiscontinuityMove, moved.CacheDiscontinuityKind)
			newSnap, err := store.Snapshot(ctx, aLegID)
			require.NoError(t, err)

			ex := memoRefreshExecutor(memoVisibilityProcessor{}, newSnap)
			facts := memoRefreshFactsWithIngress(t, oldSnap, oldProjected, ingress, oldEv.Provenance)
			refreshed, ok := ex.refreshMemoSteeringFacts(t.Context(), facts, interleavedstate.State{}, false)
			require.True(t, ok)
			require.Equal(t, oldProjected.Instructions, refreshed.baseline.Instructions, "the equivalent placement must project identically")
			require.Equal(t, oldProjected.Messages, refreshed.baseline.Messages, "the equivalent placement must project identically")
			require.Equal(t, oldProjected.Items, refreshed.baseline.Items, "the equivalent placement must project identically")
			require.Empty(t, refreshed.baseline.StructuralCause)
		})
	}
}

// TestRefreshMemoSteeringFacts_TransformedBaselineRecordsNothing proves a
// request-wide transformation applied after the snapshot projection cannot
// leak into the attribution decision. The live baseline carries user text
// augmented by an earlier request transform (for example ref-autoappend),
// while the refresh re-projects the pre-transform ingress; only the snapshot
// change — a user-role memo — differs, so no cause is recorded.
func TestRefreshMemoSteeringFacts_TransformedBaselineRecordsNothing(t *testing.T) {
	t.Parallel()
	ingress := aipapi.Call{
		Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("Follow policy A")}}},
		Messages:     []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Analyze this")}}},
	}
	baseline := aipapi.CloneCall(ingress)
	baseline.Messages[0].Parts[0].Text = "Analyze this [appended reference text]"
	memo := conversationprojection.Overlay{
		OverlayID: "memo", Revision: 1, SlotOrdinal: 1, Active: true,
		Message:             conversationprojection.OverlayMessage{Role: aipapi.RoleUser, Text: "ordinary memo"},
		Placement:           conversationprojection.Placement{Kind: conversationprojection.PlacementStablePrefix},
		AnchorMissingPolicy: conversationprojection.AnchorStablePrefixFallback,
	}
	ex := memoRefreshExecutor(memoVisibilityProcessor{}, conversationprojection.Snapshot{StateRevision: 8, Steering: []conversationprojection.Overlay{memo}})
	facts := memoRefreshFactsWithIngress(t, conversationprojection.Snapshot{StateRevision: 7}, baseline, ingress, nil)
	refreshed, ok := ex.refreshMemoSteeringFacts(t.Context(), facts, interleavedstate.State{}, false)
	require.True(t, ok)
	require.Equal(t, "Follow policy A", refreshed.baseline.Instructions[0].Parts[0].Text)
	require.Empty(t, refreshed.baseline.StructuralCause)
}

// TestRefreshMemoSteeringFacts_DuplicateClientMessageMoveRecordsCause proves
// a system overlay moved between two identical client messages is still
// detected when a user-role overlay shares the clients' exact text. Memo
// emissions are removed by their exact projected occurrence from the
// projection's own provenance — never by semantic identity — so the
// colliding client duplicates keep their trajectory positions and the move
// remains visible. Uses a real conversation-view store, for both legacy and
// item authority.
func TestRefreshMemoSteeringFacts_DuplicateClientMessageMoveRecordsCause(t *testing.T) {
	t.Parallel()
	for _, authority := range []string{"legacy", "item"} {
		t.Run(authority, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			store := conversationview.NewReferenceStore()
			const aLegID = "duplicate-anchor-cause"
			require.NoError(t, store.CreateALeg(ctx, aLegID))

			var ingress aipapi.Call
			var anchorID conversationprojection.MessageIdentity
			if authority == "item" {
				ingress = aipapi.Call{Items: []aipapi.Item{
					{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "alpha"}}},
					{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "alpha"}}},
				}}
				var err error
				anchorID, err = conversationprojection.ItemIdentityOf(ingress.Items[0])
				require.NoError(t, err)
			} else {
				ingress = aipapi.Call{Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("alpha")}},
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("alpha")}},
				}}
				var err error
				anchorID, err = conversationprojection.MessageIdentityOf(ingress.Messages[0])
				require.NoError(t, err)
			}
			put := func(id, text string, role aipapi.Role, anchor *conversationprojection.MessageAnchor, anchorMissingPolicy conversationprojection.AnchorMissingPolicy) conversationview.SteeringState {
				t.Helper()
				placement := conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix}
				if anchor != nil {
					placement = conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: anchor}
				}
				state, err := store.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
					OverlayID:           id,
					Message:             conversationview.StoredMessageV1{Role: role, Text: text},
					Placement:           placement,
					AnchorMissingPolicy: anchorMissingPolicy,
					Reason:              "duplicate_test",
				})
				require.NoError(t, err)
				return state
			}
			put("memo", "alpha", aipapi.RoleUser, nil, conversationview.AnchorStablePrefixFallback)
			put("policy", "Policy X", aipapi.RoleSystem, &conversationprojection.MessageAnchor{Identity: anchorID, Occurrence: 1}, conversationview.AnchorFailClosed)
			oldSnap, err := store.Snapshot(ctx, aLegID)
			require.NoError(t, err)
			oldProjected, oldEv, err := conversationprojection.Project(ingress, oldSnap)
			require.NoError(t, err)
			moved := put("policy", "Policy X", aipapi.RoleSystem, &conversationprojection.MessageAnchor{Identity: anchorID, Occurrence: 2}, conversationview.AnchorFailClosed)
			require.Equal(t, conversationview.CacheDiscontinuityMove, moved.CacheDiscontinuityKind)
			newSnap, err := store.Snapshot(ctx, aLegID)
			require.NoError(t, err)

			ex := memoRefreshExecutor(memoVisibilityProcessor{}, newSnap)
			facts := memoRefreshFactsWithIngress(t, oldSnap, oldProjected, ingress, oldEv.Provenance)
			refreshed, ok := ex.refreshMemoSteeringFacts(t.Context(), facts, interleavedstate.State{}, false)
			require.True(t, ok)
			require.Equal(t, causeSystemPrompt, refreshed.baseline.StructuralCause)
			if authority == "item" {
				require.Len(t, refreshed.baseline.Items, 4)
				require.Equal(t, "aip-steering-policy", refreshed.baseline.Items[3].ID)
			} else {
				texts := trajectoryTexts(refreshed.baseline, false)
				require.Equal(t, []string{"alpha", "alpha", "Policy X"}, texts)
			}
		})
	}
}

// TestRefreshMemoSteeringFacts_StalePreparedProvenanceRecordsNothing proves
// the comparison uses the provenance generated by its own re-projection. The
// frozen provenance belongs to the prepared backend call, which can differ
// from the ingress by a request-wide insertion (for example a submit hook
// adding a user message). Removing the memo by a position that belongs to a
// different trajectory would drop a client message and fabricate a trajectory
// change. Only the memo text changes here, so no cause may be recorded.
func TestRefreshMemoSteeringFacts_StalePreparedProvenanceRecordsNothing(t *testing.T) {
	t.Parallel()
	ingress := aipapi.Call{
		Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("Follow policy A")}}},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("alpha")}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("beta")}},
		},
	}
	// The prepared backend call carries an extra user message inserted by a
	// request-wide hook before the memo's anchor position.
	baseline := aipapi.CloneCall(ingress)
	baseline.Messages = append([]aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hook-injected")}}}, baseline.Messages...)

	alphaID, err := conversationprojection.MessageIdentityOf(ingress.Messages[0])
	require.NoError(t, err)
	anchor := &conversationprojection.MessageAnchor{Identity: alphaID, Occurrence: 1}
	oldOverlay := conversationprojection.Overlay{
		OverlayID: "memo", Revision: 1, SlotOrdinal: 1, Active: true,
		Message:             conversationprojection.OverlayMessage{Role: aipapi.RoleUser, Text: "memo one"},
		Placement:           conversationprojection.Placement{Kind: conversationprojection.PlacementAfterMessage, Anchor: anchor},
		AnchorMissingPolicy: conversationprojection.AnchorFailClosed,
	}
	newOverlay := oldOverlay
	newOverlay.Revision = 2
	newOverlay.Message.Text = "memo two"
	oldSnap := conversationprojection.Snapshot{StateRevision: 7, Steering: []conversationprojection.Overlay{oldOverlay}}

	baselineProjected, baselineEv, err := conversationprojection.Project(baseline, oldSnap)
	require.NoError(t, err)
	oldProjected, oldEv, err := conversationprojection.Project(ingress, oldSnap)
	require.NoError(t, err)
	require.Equal(t, []string{"alpha", "memo one", "beta"}, trajectoryTexts(oldProjected, false))
	require.Len(t, baselineEv.Provenance, 1)
	require.Len(t, oldEv.Provenance, 1)
	require.NotEqual(t, baselineEv.Provenance[0].InjectedTrajectoryIndex, oldEv.Provenance[0].InjectedTrajectoryIndex,
		"the prepared projection must place the memo at a different trajectory position than the ingress projection")

	ex := memoRefreshExecutor(memoVisibilityProcessor{}, conversationprojection.Snapshot{StateRevision: 8, Steering: []conversationprojection.Overlay{newOverlay}})
	facts := memoRefreshFactsWithIngress(t, oldSnap, baselineProjected, ingress, baselineEv.Provenance)
	refreshed, ok := ex.refreshMemoSteeringFacts(t.Context(), facts, interleavedstate.State{}, false)
	require.True(t, ok)
	require.Equal(t, []string{"alpha", "memo two", "beta"}, trajectoryTexts(refreshed.baseline, false))
	require.Empty(t, refreshed.baseline.StructuralCause)
}

// TestRefreshMemoSteeringFacts_NeverBackendCollisionKeepsSteeringMoveRecordsCause
// proves a NeverBackend tag on an ingress instruction cannot erase a steering
// overlay that happens to share its role and text. Exclusion applies to input
// before injection, so the injected overlay is projection-owned content: moving
// it between two user anchors still changes the provider-bound trajectory. Uses
// a real conversation-view store, for both legacy and item authority.
func TestRefreshMemoSteeringFacts_NeverBackendCollisionKeepsSteeringMoveRecordsCause(t *testing.T) {
	t.Parallel()
	for _, authority := range []string{"legacy", "item"} {
		t.Run(authority, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			store := conversationview.NewReferenceStore()
			const aLegID = "never-backend-collision-cause"
			require.NoError(t, store.CreateALeg(ctx, aLegID))

			var ingress aipapi.Call
			var policyID, anchorAID, anchorBID conversationprojection.MessageIdentity
			if authority == "item" {
				ingress = aipapi.Call{Items: []aipapi.Item{
					{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleSystem, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "Policy X"}}},
					{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "alpha"}}},
					{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "beta"}}},
				}}
				var err error
				policyID, err = conversationprojection.ItemIdentityOf(ingress.Items[0])
				require.NoError(t, err)
				anchorAID, err = conversationprojection.ItemIdentityOf(ingress.Items[1])
				require.NoError(t, err)
				anchorBID, err = conversationprojection.ItemIdentityOf(ingress.Items[2])
				require.NoError(t, err)
			} else {
				ingress = aipapi.Call{
					Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("Policy X")}}},
					Messages: []aipapi.Message{
						{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("alpha")}},
						{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("beta")}},
					},
				}
				var err error
				policyID, err = conversationprojection.MessageIdentityOf(ingress.Instructions[0])
				require.NoError(t, err)
				anchorAID, err = conversationprojection.MessageIdentityOf(ingress.Messages[0])
				require.NoError(t, err)
				anchorBID, err = conversationprojection.MessageIdentityOf(ingress.Messages[1])
				require.NoError(t, err)
			}
			_, err := store.TagNeverBackend(ctx, aLegID, []conversationview.TagRequest{{Identity: policyID, Reason: "collision_test"}})
			require.NoError(t, err)

			put := func(anchor conversationprojection.MessageAnchor) conversationview.SteeringState {
				t.Helper()
				state, err := store.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
					OverlayID:           "policy",
					Message:             conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "Policy X"},
					Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
					AnchorMissingPolicy: conversationview.AnchorFailClosed,
					Reason:              "never_backend_collision",
				})
				require.NoError(t, err)
				return state
			}
			put(conversationprojection.MessageAnchor{Identity: anchorAID, Occurrence: 1})
			oldSnap, err := store.Snapshot(ctx, aLegID)
			require.NoError(t, err)
			oldProjected, oldEv, err := conversationprojection.Project(ingress, oldSnap)
			require.NoError(t, err)
			require.Len(t, oldEv.Provenance, 1)
			require.Equal(t, oldEv.Provenance[0].InjectedIdentity, policyID,
				"the fixture must collide the tagged input identity with the emitted overlay identity")
			require.Equal(t, []string{"alpha", "Policy X", "beta"}, trajectoryTexts(oldProjected, authority == "item"))

			moved := put(conversationprojection.MessageAnchor{Identity: anchorBID, Occurrence: 1})
			require.Equal(t, conversationview.CacheDiscontinuityMove, moved.CacheDiscontinuityKind)
			newSnap, err := store.Snapshot(ctx, aLegID)
			require.NoError(t, err)

			ex := memoRefreshExecutor(memoVisibilityProcessor{}, newSnap)
			facts := memoRefreshFactsWithIngress(t, oldSnap, oldProjected, ingress, oldEv.Provenance)
			refreshed, ok := ex.refreshMemoSteeringFacts(t.Context(), facts, interleavedstate.State{}, false)
			require.True(t, ok)
			require.Equal(t, causeSystemPrompt, refreshed.baseline.StructuralCause)
			require.Equal(t, []string{"alpha", "beta", "Policy X"}, trajectoryTexts(refreshed.baseline, authority == "item"))
			if authority == "item" {
				require.Equal(t, "aip-steering-policy", refreshed.baseline.Items[2].ID)
			}
		})
	}
}

// TestRefreshMemoSteeringFacts_NonInstructionOverlayRecordsNothing proves a
// new overlay that injects conversational content is not system-prompt
// evidence, whatever its non-instruction role. Assistant and tool steering are
// valid records, so injecting one must not publish: only a demonstrated change
// to system/developer instructions is an instruction-placement change.
func TestRefreshMemoSteeringFacts_NonInstructionOverlayRecordsNothing(t *testing.T) {
	t.Parallel()
	for _, role := range []aipapi.Role{aipapi.RoleAssistant, aipapi.RoleTool} {
		for _, authority := range []string{"legacy", "item"} {
			t.Run(string(role)+"/"+authority, func(t *testing.T) {
				t.Parallel()
				var ingress aipapi.Call
				var anchorID conversationprojection.MessageIdentity
				if authority == "item" {
					ingress = aipapi.Call{Items: []aipapi.Item{
						{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleSystem, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "Follow policy A"}}},
						{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "alpha"}}},
					}}
					var err error
					anchorID, err = conversationprojection.ItemIdentityOf(ingress.Items[1])
					require.NoError(t, err)
				} else {
					ingress = aipapi.Call{
						Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("Follow policy A")}}},
						Messages:     []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("alpha")}}},
					}
					var err error
					anchorID, err = conversationprojection.MessageIdentityOf(ingress.Messages[0])
					require.NoError(t, err)
				}
				overlay := conversationprojection.Overlay{
					OverlayID: "assistant-note", Revision: 1, SlotOrdinal: 1, Active: true,
					Message:             conversationprojection.OverlayMessage{Role: role, Text: "Assistant note"},
					Placement:           conversationprojection.Placement{Kind: conversationprojection.PlacementAfterMessage, Anchor: &conversationprojection.MessageAnchor{Identity: anchorID, Occurrence: 1}},
					AnchorMissingPolicy: conversationprojection.AnchorFailClosed,
				}
				ex := memoRefreshExecutor(memoVisibilityProcessor{}, conversationprojection.Snapshot{StateRevision: 8, Steering: []conversationprojection.Overlay{overlay}})
				facts := memoRefreshFactsWithCall(t, conversationprojection.Snapshot{StateRevision: 7}, ingress)
				refreshed, ok := ex.refreshMemoSteeringFacts(t.Context(), facts, interleavedstate.State{}, false)
				require.True(t, ok)
				require.Contains(t, trajectoryTexts(refreshed.baseline, authority == "item"), "Assistant note",
					"the non-instruction overlay must still reach the refreshed continuation")
				require.Empty(t, refreshed.baseline.StructuralCause)
			})
		}
	}
}

// TestRefreshMemoSteeringFacts_ItemReferenceCleanupRecordsNothing proves the
// symmetric NeverBackend normalization reproduces the projection's dependency
// cleanup. Excluding a message item also removes the item_reference that points
// at it; without that, the leftover reference on the un-excluded side would
// look like an instruction change. Only user content is excluded here, so no
// cause may be recorded.
func TestRefreshMemoSteeringFacts_ItemReferenceCleanupRecordsNothing(t *testing.T) {
	t.Parallel()
	ingress := aipapi.Call{Items: []aipapi.Item{
		{Kind: aipapi.ItemKindMessage, ID: "m1", Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "alpha"}}},
		{Kind: aipapi.ItemKindItemReference, ID: "ref-1", Status: aipapi.ItemStatusCompleted, Reference: &aipapi.ItemReference{ID: "m1"}},
		{Kind: aipapi.ItemKindMessage, ID: "m2", Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "beta"}}},
	}}
	alphaID, err := conversationprojection.ItemIdentityOf(ingress.Items[0])
	require.NoError(t, err)
	oldProjected, _, err := conversationprojection.Project(ingress, conversationprojection.Snapshot{StateRevision: 7})
	require.NoError(t, err)
	require.Len(t, oldProjected.Items, 3, "the un-excluded projection must retain the reference")

	ex := memoRefreshExecutor(memoVisibilityProcessor{}, conversationprojection.Snapshot{
		StateRevision: 8,
		NeverBackend:  []conversationprojection.Tag{{Identity: alphaID, Reason: "test"}},
	})
	facts := memoRefreshFactsWithCall(t, conversationprojection.Snapshot{StateRevision: 7}, ingress)
	refreshed, ok := ex.refreshMemoSteeringFacts(t.Context(), facts, interleavedstate.State{}, false)
	require.True(t, ok)
	require.Len(t, refreshed.baseline.Items, 1, "the excluded message and its dependent reference must both leave")
	require.Equal(t, "m2", refreshed.baseline.Items[0].ID)
	require.Empty(t, refreshed.baseline.StructuralCause)
}

func TestRefreshMemoSteeringFacts_UnchangedSteeringRecordsNothing(t *testing.T) {
	t.Parallel()
	snapshot := conversationprojection.Snapshot{StateRevision: 7}
	ex := memoRefreshExecutor(memoVisibilityProcessor{}, snapshot)
	facts := memoRefreshFacts(t, snapshot)
	refreshed, ok := ex.refreshMemoSteeringFacts(t.Context(), facts, interleavedstate.State{}, false)
	require.True(t, ok)
	require.Empty(t, refreshed.baseline.StructuralCause)
}

// TestRefreshMemoSteeringFacts_SuppressedVisibleMemoStaysOutOfContinuation
// pins the conservative boundary: a visible-mode memo is stripped from the
// immediate continuation, so that continuation does not carry the memo and
// records no cause. The change is deferred to later turns.
func TestRefreshMemoSteeringFacts_SuppressedVisibleMemoStaysOutOfContinuation(t *testing.T) {
	t.Parallel()
	overlay := conversationprojection.Overlay{
		OverlayID: "memo", Revision: 1, SlotOrdinal: 1, Active: true,
		Message:             conversationprojection.OverlayMessage{Role: aipapi.RoleUser, Text: "ordinary memo"},
		Placement:           conversationprojection.Placement{Kind: conversationprojection.PlacementStablePrefix},
		AnchorMissingPolicy: conversationprojection.AnchorStablePrefixFallback,
	}
	ex := memoRefreshExecutor(memoVisibilityProcessor{visible: true}, conversationprojection.Snapshot{StateRevision: 8, Steering: []conversationprojection.Overlay{overlay}})
	facts := memoRefreshFacts(t, conversationprojection.Snapshot{StateRevision: 7})
	refreshed, ok := ex.refreshMemoSteeringFacts(t.Context(), facts, interleavedstate.State{}, true)
	require.True(t, ok)
	require.Len(t, refreshed.baseline.Instructions, 1, "the suppressed memo must not reach the immediate continuation")
	require.Equal(t, "standing instruction", refreshed.baseline.Instructions[0].Parts[0].Text)
	require.Empty(t, refreshed.baseline.StructuralCause)
}

func TestRefreshMemoSteeringFacts_KeepsTheFirstCause(t *testing.T) {
	t.Parallel()
	ex := memoRefreshExecutor(memoVisibilityProcessor{}, conversationprojection.Snapshot{
		StateRevision: 8,
		Steering:      []conversationprojection.Overlay{memoInstructionOverlay("instruction-change", "new instruction", 1, aipapi.RoleSystem)},
	})
	facts := memoRefreshFacts(t, conversationprojection.Snapshot{StateRevision: 7})
	facts.baseline.StructuralCause = "compaction"
	refreshed, ok := ex.refreshMemoSteeringFacts(t.Context(), facts, interleavedstate.State{}, false)
	require.True(t, ok)
	require.Equal(t, "compaction", refreshed.baseline.StructuralCause,
		"an earlier structural cause must survive a later instruction change")
}

// TestProviderInstructionsChanged_IgnoresLegacyMessageMetadata proves
// proxy-owned message traceability is not instruction content for the same-turn
// authority either. Rewriting only metadata must not fabricate a system-prompt
// cause; the shared normalization is the one the digest uses too.
func TestProviderInstructionsChanged_IgnoresLegacyMessageMetadata(t *testing.T) {
	t.Parallel()
	call := func(trace string) aipapi.Call {
		return aipapi.Call{Instructions: []aipapi.Message{{
			Role:     aipapi.RoleSystem,
			Parts:    []aipapi.Part{aipapi.TextPart("be terse")},
			Metadata: map[string]string{"trace": trace},
		}}}
	}
	if providerInstructionsChanged(call("trace-a"), nil, conversationprojection.Snapshot{}, call("trace-b"), nil, conversationprojection.Snapshot{}) {
		t.Fatal("proxy-owned message metadata must not count as an instruction change")
	}
}

// TestRefreshMemoSteeringFacts_SnapshotFailureKeepsFrozenFacts proves the
// same-turn refresh is soft-fail: when the conversation-view read fails, the
// continuation keeps the pre-capture facts instead of publishing a partial or
// fabricated system-prompt cause.
func TestRefreshMemoSteeringFacts_SnapshotFailureKeepsFrozenFacts(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	ex.InterleavedProcessor = memoVisibilityProcessor{}
	ex.ConversationViewReader = &failingSnapshotReader{err: errors.New("snapshot boom")}
	facts := memoRefreshFacts(t, conversationprojection.Snapshot{StateRevision: 7})
	refreshed, ok := ex.refreshMemoSteeringFacts(t.Context(), facts, interleavedstate.State{}, false)
	require.False(t, ok, "a failed snapshot read must report no refresh")
	require.Equal(t, facts.baseline, refreshed.baseline)
	require.Equal(t, facts.conversationSnapshot, refreshed.conversationSnapshot)
	require.Empty(t, refreshed.baseline.StructuralCause)
}

type failingSteeringWriter struct{ err error }

func (w failingSteeringWriter) Put(context.Context, steering.PutRequest) (steering.State, error) {
	return steering.State{}, w.err
}

func (w failingSteeringWriter) Deactivate(context.Context, steering.OverlayID) (steering.State, error) {
	return steering.State{}, w.err
}

// TestPublishMemoSteeringOverlay_ReturnsWriterFailure proves a failed steering
// write is surfaced to the caller, which then logs and skips linking the memo.
// Silently returning success would leave the continuation planning against an
// overlay that was never committed.
func TestPublishMemoSteeringOverlay_ReturnsWriterFailure(t *testing.T) {
	t.Parallel()
	want := errors.New("steering store unavailable")
	ex := TestExecutor()
	ex.InterleavedProcessor = memoVisibilityProcessor{}
	ex.SteeringWriterFactory = func(context.Context, string, SteeringWriterResolver) (steering.Writer, error) {
		return failingSteeringWriter{err: want}, nil
	}
	err := ex.publishMemoSteeringOverlay(t.Context(), "a-leg-publish-failure", aipapi.Call{}, conversationprojection.Snapshot{}, "memo")
	if !errors.Is(err, want) {
		t.Fatalf("publish error = %v, want the writer failure %v", err, want)
	}
}

// TestCauseSystemPromptMatchesThePublishedVocabulary pins the fixed token the
// runtime records for the bootstrap authority. The runtime cannot import the
// diagnostic, so the value itself is the contract; a drift would silently turn
// the attribution into `unexplained`.
func TestCauseSystemPromptMatchesThePublishedVocabulary(t *testing.T) {
	t.Parallel()
	require.Equal(t, "system-prompt", causeSystemPrompt)
}
