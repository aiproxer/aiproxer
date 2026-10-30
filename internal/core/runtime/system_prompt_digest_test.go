package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/require"
)

// instructionCall builds a legacy call with one system instruction and the
// supplied user turns. The instruction is deliberately first so a change to
// the conversation cannot be mistaken for an instruction change.
func instructionCall(system string, userTurns ...string) aipapi.Call {
	call := aipapi.Call{
		Instructions: []aipapi.Message{{
			Role:  aipapi.RoleSystem,
			Parts: []aipapi.Part{aipapi.TextPart(system)},
		}},
	}
	for _, text := range userTurns {
		call.Messages = append(call.Messages, aipapi.Message{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart(text)},
		})
	}
	return call
}

// TestSystemPromptDigest_StableAcrossConversationGrowth proves the identity
// names the provider-bound instruction prefix, not the whole request: growing
// the user and assistant conversation must not shift it, or every turn would
// look like a cross-turn system-prompt change.
func TestSystemPromptDigest_StableAcrossConversationGrowth(t *testing.T) {
	t.Parallel()
	key := seamTestKey()
	first, ok := digestWithoutProjectionEvidence(instructionCall("be terse", "hello"), key)
	if !ok {
		t.Fatal("digest must be computable")
	}
	second, ok := digestWithoutProjectionEvidence(instructionCall("be terse", "hello", "more", "even more"), key)
	if !ok {
		t.Fatal("digest must be computable")
	}
	if first != second {
		t.Fatal("conversation growth must not change the instruction identity")
	}
}

// TestSystemPromptDigest_TracksInstructionContent proves the positive switch:
// changing the system or developer instruction changes the identity, so the
// diagnostic can attribute a cross-turn change at the leg where it happened.
// The replacement instructions have the same length, so only the content
// itself can distinguish them.
func TestSystemPromptDigest_TracksInstructionContent(t *testing.T) {
	t.Parallel()
	key := seamTestKey()
	withDeveloper := func(system, developer string) aipapi.Call {
		call := instructionCall(system, "hi")
		call.Instructions = append(call.Instructions, aipapi.Message{
			Role:  aipapi.RoleDeveloper,
			Parts: []aipapi.Part{aipapi.TextPart(developer)},
		})
		return call
	}
	base, ok := digestWithoutProjectionEvidence(withDeveloper("be terse", "cite sources"), key)
	if !ok {
		t.Fatal("digest must be computable")
	}
	changedSystem, ok := digestWithoutProjectionEvidence(withDeveloper("be vivid", "cite sources"), key)
	if !ok {
		t.Fatal("digest must be computable")
	}
	if base == changedSystem {
		t.Fatal("a changed system instruction must change the identity")
	}
	changedDeveloper, ok := digestWithoutProjectionEvidence(withDeveloper("be terse", "cite quotes!"), key)
	if !ok {
		t.Fatal("digest must be computable")
	}
	if base == changedDeveloper {
		t.Fatal("a changed developer instruction must change the identity")
	}
}

// TestSystemPromptDigest_ItemAuthorityTracksInstructions proves the identity
// is not a legacy-only mechanism: an item-authoritative request that changes a
// system message changes the identity, while an item change outside the
// instruction prefix does not.
func TestSystemPromptDigest_ItemAuthorityTracksInstructions(t *testing.T) {
	t.Parallel()
	key := seamTestKey()
	itemCall := func(system, user string) aipapi.Call {
		return aipapi.Call{Items: []aipapi.Item{
			{
				Kind:    aipapi.ItemKindMessage,
				ID:      "msg-1",
				Status:  aipapi.ItemStatusCompleted,
				Role:    aipapi.RoleSystem,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: system}},
			},
			{
				Kind:    aipapi.ItemKindMessage,
				ID:      "msg-2",
				Status:  aipapi.ItemStatusCompleted,
				Role:    aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: user}},
			},
		}}
	}
	base, ok := digestWithoutProjectionEvidence(itemCall("be terse", "hi"), key)
	if !ok {
		t.Fatal("digest must be computable")
	}
	grown, ok := digestWithoutProjectionEvidence(itemCall("be terse", "different user turn"), key)
	if !ok {
		t.Fatal("digest must be computable")
	}
	if base != grown {
		t.Fatal("a changed user item must not change the instruction identity")
	}
	changed, ok := digestWithoutProjectionEvidence(itemCall("be vivid", "hi"), key)
	if !ok {
		t.Fatal("digest must be computable")
	}
	if base == changed {
		t.Fatal("a changed system item must change the instruction identity")
	}
}

// TestSystemPromptDigest_ExcludesProxyTraceability proves volatile item
// bookkeeping never shifts the identity: item identity and lifecycle status
// are not instruction content, so rewriting them between turns cannot fabricate
// a system-prompt change.
func TestSystemPromptDigest_ExcludesProxyTraceability(t *testing.T) {
	t.Parallel()
	key := seamTestKey()
	first := aipapi.Call{Items: []aipapi.Item{{
		Kind:    aipapi.ItemKindMessage,
		ID:      "first-id",
		Status:  aipapi.ItemStatusCompleted,
		Role:    aipapi.RoleSystem,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "be terse"}},
	}}}
	second := aipapi.Call{Items: []aipapi.Item{{
		Kind:    aipapi.ItemKindMessage,
		ID:      "second-id",
		Status:  aipapi.ItemStatusInProgress,
		Role:    aipapi.RoleSystem,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "be terse"}},
	}}}
	a, ok := digestWithoutProjectionEvidence(first, key)
	if !ok {
		t.Fatal("digest must be computable")
	}
	b, ok := digestWithoutProjectionEvidence(second, key)
	if !ok {
		t.Fatal("digest must be computable")
	}
	if a != b {
		t.Fatal("item identity and status are proxy bookkeeping and must not change the instruction identity")
	}
}

// TestSystemPromptDigest_EmptyInstructionsIsAnIdentity proves removing every
// instruction is observable: the digest of an empty instruction list is a
// stable non-zero value, so a call that dropped its instructions is compared
// rather than silently treated as unobserved.
func TestSystemPromptDigest_EmptyInstructionsIsAnIdentity(t *testing.T) {
	t.Parallel()
	key := seamTestKey()
	empty := aipapi.Call{Messages: []aipapi.Message{{
		Role:  aipapi.RoleUser,
		Parts: []aipapi.Part{aipapi.TextPart("hi")},
	}}}
	first, ok := digestWithoutProjectionEvidence(empty, key)
	if !ok {
		t.Fatal("digest must be computable")
	}
	var none [32]byte
	if first == none {
		t.Fatal("an empty instruction list must still carry a non-zero identity")
	}
	second, ok := digestWithoutProjectionEvidence(empty, key)
	if !ok {
		t.Fatal("digest must be computable")
	}
	if first != second {
		t.Fatal("the empty instruction identity must be stable")
	}
	withInstruction, ok := digestWithoutProjectionEvidence(instructionCall("be terse", "hi"), key)
	if !ok {
		t.Fatal("digest must be computable")
	}
	if first == withInstruction {
		t.Fatal("adding an instruction must change the identity")
	}
}

// TestSystemPromptDigest_Keyed proves the identity is a keyed fingerprint: the
// same instructions under different process keys yield different digests, so a
// digest cannot be matched with a dictionary built under another process.
func TestSystemPromptDigest_Keyed(t *testing.T) {
	t.Parallel()
	call := instructionCall("be terse", "hi")
	first, ok := digestWithoutProjectionEvidence(call, seamTestKey())
	if !ok {
		t.Fatal("digest must be computable")
	}
	second, ok := digestWithoutProjectionEvidence(call, seamOtherKey())
	if !ok {
		t.Fatal("digest must be computable")
	}
	if first == second {
		t.Fatal("different process keys must yield different instruction identities")
	}
}

// TestSystemPromptDigest_MarshalFailurePublishesNothing proves a hostile
// instruction that cannot be encoded yields no identity rather than a
// fabricated one: the caller must leave the diagnostic's baseline untouched.
func TestSystemPromptDigest_MarshalFailurePublishesNothing(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{Instructions: []aipapi.Message{{
		Role: aipapi.RoleSystem,
		Parts: []aipapi.Part{{
			Kind:    aipapi.PartJSON,
			Content: json.RawMessage(`{`),
		}},
	}}}
	if _, ok := digestWithoutProjectionEvidence(call, seamTestKey()); ok {
		t.Fatal("an instruction that cannot be encoded must not produce an identity")
	}
}

// TestSystemPromptDigest_PlacementChange proves placement is part of the
// identity: moving an unchanged system or developer instruction between two
// distinct anchors changes the digest, for both legacy and item authority. The
// same-turn continuation comparison classifies the same move as a
// system-prompt event; the cross-turn identity must agree.
func TestSystemPromptDigest_PlacementChange(t *testing.T) {
	t.Parallel()
	key := seamTestKey()
	for _, role := range []aipapi.Role{aipapi.RoleSystem, aipapi.RoleDeveloper} {
		t.Run(string(role), func(t *testing.T) {
			legacy := func(instructionFirst bool) aipapi.Call {
				messages := []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("alpha")}},
				}
				if instructionFirst {
					messages = append(messages, aipapi.Message{Role: role, Parts: []aipapi.Part{aipapi.TextPart("policy X")}})
				}
				messages = append(messages, aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("beta")}})
				if !instructionFirst {
					messages = append(messages, aipapi.Message{Role: role, Parts: []aipapi.Part{aipapi.TextPart("policy X")}})
				}
				return aipapi.Call{Messages: messages}
			}
			items := func(instructionFirst bool) aipapi.Call {
				item := func(text string, itemRole aipapi.Role) aipapi.Item {
					return aipapi.Item{Kind: aipapi.ItemKindMessage, Role: itemRole, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: text}}}
				}
				order := []aipapi.Item{item("alpha", aipapi.RoleUser)}
				if instructionFirst {
					order = append(order, item("policy X", role))
				}
				order = append(order, item("beta", aipapi.RoleUser))
				if !instructionFirst {
					order = append(order, item("policy X", role))
				}
				return aipapi.Call{Items: order}
			}
			for name, build := range map[string]func(bool) aipapi.Call{"legacy": legacy, "item": items} {
				before, ok := digestWithoutProjectionEvidence(build(true), key)
				if !ok {
					t.Fatal("digest must be computable")
				}
				after, ok := digestWithoutProjectionEvidence(build(false), key)
				if !ok {
					t.Fatal("digest must be computable")
				}
				if before == after {
					t.Fatalf("%s: moving an unchanged instruction between anchors must change the identity", name)
				}
			}
		})
	}
}

// TestSystemPromptDigest_MixedLegacyInstructionSections proves the identity
// accounts for an instruction carried in the legacy Messages section after the
// Instructions section. Its trajectory position includes the instruction-section
// offset, so a change to that later instruction is observable instead of
// falling outside the hashed scan.
func TestSystemPromptDigest_MixedLegacyInstructionSections(t *testing.T) {
	t.Parallel()
	key := seamTestKey()
	call := func(lateSystem string) aipapi.Call {
		return aipapi.Call{
			Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("policy A")}}},
			Messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
				{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart(lateSystem)}},
			},
		}
	}
	base, ok := digestWithoutProjectionEvidence(call("policy B"), key)
	if !ok {
		t.Fatal("digest must be computable")
	}
	changed, ok := digestWithoutProjectionEvidence(call("policy C"), key)
	if !ok {
		t.Fatal("digest must be computable")
	}
	if base == changed {
		t.Fatal("a changed instruction in the legacy Messages section must change the identity")
	}
}

// TestSystemPromptDigest_DetectsLateInstructionChange proves no instruction is
// silently outside the identity: a change to the last instruction of a
// maximum-size legacy request changes the digest. There is no truncation
// blind spot, because only instructions are hashed.
func TestSystemPromptDigest_DetectsLateInstructionChange(t *testing.T) {
	t.Parallel()
	key := seamTestKey()
	instructions := func(changeLast bool) aipapi.Call {
		call := aipapi.Call{}
		for i := range aipapi.MaxInstructionMessages {
			text := "instruction-tttt"
			if changeLast && i == aipapi.MaxInstructionMessages-1 {
				text = "instruction-vvvv"
			}
			call.Instructions = append(call.Instructions, aipapi.Message{
				Role:  aipapi.RoleSystem,
				Parts: []aipapi.Part{aipapi.TextPart(text)},
			})
		}
		return call
	}
	base, ok := digestWithoutProjectionEvidence(instructions(false), key)
	if !ok {
		t.Fatal("digest must be computable")
	}
	changed, ok := digestWithoutProjectionEvidence(instructions(true), key)
	if !ok {
		t.Fatal("digest must be computable")
	}
	if base == changed {
		t.Fatal("a change to the last canonical instruction must change the identity")
	}
}

// TestSystemPromptDigest_LargeConversationWithoutInstructions proves the
// identity of a no-instruction request is the empty identity regardless of
// conversation size: ordinary content never contributes, so a huge trajectory
// cannot make an instruction change look present or absent.
func TestSystemPromptDigest_LargeConversationWithoutInstructions(t *testing.T) {
	t.Parallel()
	key := seamTestKey()
	small := aipapi.Call{Messages: []aipapi.Message{{
		Role:  aipapi.RoleUser,
		Parts: []aipapi.Part{aipapi.TextPart("hi")},
	}}}
	large := aipapi.Call{Messages: make([]aipapi.Message, 0, aipapi.MaxMessages)}
	for range aipapi.MaxMessages {
		large.Messages = append(large.Messages, aipapi.Message{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		})
	}
	smallDigest, ok := digestWithoutProjectionEvidence(small, key)
	if !ok {
		t.Fatal("digest must be computable")
	}
	largeDigest, ok := digestWithoutProjectionEvidence(large, key)
	if !ok {
		t.Fatal("digest must be computable")
	}
	if smallDigest != largeDigest {
		t.Fatal("conversation-only content must not change the empty instruction identity")
	}
}

// BenchmarkSystemPromptDigest_MaxLegacyInstructions bounds the cost of the
// maximum valid legacy instruction prefix.
func BenchmarkSystemPromptDigest_MaxLegacyInstructions(b *testing.B) {
	call := aipapi.Call{}
	for range aipapi.MaxInstructionMessages {
		call.Instructions = append(call.Instructions, aipapi.Message{
			Role:  aipapi.RoleSystem,
			Parts: []aipapi.Part{aipapi.TextPart("instruction-payload-" + strings.Repeat("x", 32))},
		})
	}
	key := seamTestKey()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, ok := digestWithoutProjectionEvidence(call, key); !ok {
			b.Fatal("digest must be computable")
		}
	}
}

// BenchmarkSystemPromptDigest_MaxMessagesNoInstructions bounds the cost of a
// maximum valid legacy conversation with no instructions: the scan is linear
// but must not allocate per message.
func BenchmarkSystemPromptDigest_MaxMessagesNoInstructions(b *testing.B) {
	call := aipapi.Call{Messages: make([]aipapi.Message, 0, aipapi.MaxMessages)}
	for range aipapi.MaxMessages {
		call.Messages = append(call.Messages, aipapi.Message{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("user-payload")},
		})
	}
	key := seamTestKey()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, ok := digestWithoutProjectionEvidence(call, key); !ok {
			b.Fatal("digest must be computable")
		}
	}
}

// BenchmarkSystemPromptDigest_MaxItemsNoInstructions bounds the cost of a
// maximum valid item-authoritative trajectory with no instructions.
func BenchmarkSystemPromptDigest_MaxItemsNoInstructions(b *testing.B) {
	call := aipapi.Call{Items: make([]aipapi.Item, 0, aipapi.MaxItems)}
	for range aipapi.MaxItems {
		call.Items = append(call.Items, aipapi.Item{
			Kind:    aipapi.ItemKindMessage,
			Role:    aipapi.RoleUser,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "user-payload"}},
		})
	}
	key := seamTestKey()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, ok := digestWithoutProjectionEvidence(call, key); !ok {
			b.Fatal("digest must be computable")
		}
	}
}

// digestWithoutProjectionEvidence computes the identity of a bare call: shape
// tests that do not exercise steering provenance pass no overlay evidence.
func digestWithoutProjectionEvidence(call aipapi.Call, key SeamDigestKey) ([32]byte, bool) {
	return systemPromptDigest(call, nil, key)
}

// TestSystemPromptDigest_VolatileUserOverlayInsertionIsStable proves a user-role
// steering overlay inserted before an unchanged system instruction does not
// change the instruction identity. The same-turn authority removes volatile
// user-role overlay emissions before judging an instruction move, so the
// cross-turn identity uses the same effective placement rather than a raw
// ordinal that the insertion would shift.
func TestSystemPromptDigest_VolatileUserOverlayInsertionIsStable(t *testing.T) {
	t.Parallel()
	key := seamTestKey()
	for _, authority := range []string{"legacy", "item"} {
		t.Run(authority, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			store := conversationview.NewReferenceStore()
			const aLegID = "digest-volatile-overlay"
			require.NoError(t, store.CreateALeg(ctx, aLegID))

			var ingress aipapi.Call
			var anchor conversationprojection.MessageAnchor
			if authority == "item" {
				ingress = aipapi.Call{Items: []aipapi.Item{
					{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "alpha"}}},
					{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "beta"}}},
				}}
				id, err := conversationprojection.ItemIdentityOf(ingress.Items[0])
				require.NoError(t, err)
				anchor = conversationprojection.MessageAnchor{Identity: id, Occurrence: 1}
			} else {
				ingress = aipapi.Call{Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("alpha")}},
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("beta")}},
				}}
				id, err := conversationprojection.MessageIdentityOf(ingress.Messages[0])
				require.NoError(t, err)
				anchor = conversationprojection.MessageAnchor{Identity: id, Occurrence: 1}
			}
			put := func(overlayID string, role aipapi.Role, text string) {
				_, err := store.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
					OverlayID:           overlayID,
					Message:             conversationview.StoredMessageV1{Role: role, Text: text},
					Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
					AnchorMissingPolicy: conversationview.AnchorFailClosed,
					Reason:              "identity_test",
				})
				require.NoError(t, err)
			}
			// The memo is created first (lower slot ordinal) and the system
			// instruction second, so an active memo precedes the instruction.
			put("memo", aipapi.RoleUser, "thinker memo")
			put("instruction", aipapi.RoleSystem, "policy X")
			_, err := store.DeactivateSteering(ctx, aLegID, "memo")
			require.NoError(t, err)
			beforeSnap, err := store.Snapshot(ctx, aLegID)
			require.NoError(t, err)
			beforeCall, beforeEvidence, err := conversationprojection.Project(ingress, beforeSnap)
			require.NoError(t, err)

			put("memo", aipapi.RoleUser, "thinker memo")
			afterSnap, err := store.Snapshot(ctx, aLegID)
			require.NoError(t, err)
			afterCall, afterEvidence, err := conversationprojection.Project(ingress, afterSnap)
			require.NoError(t, err)

			beforeVolatile, ok := adaptedVolatileEmissionIndices(beforeCall, beforeCall, beforeSnap, beforeEvidence.Provenance)
			require.True(t, ok)
			before, ok := systemPromptDigest(beforeCall, beforeVolatile, key)
			require.True(t, ok)
			afterVolatile, ok := adaptedVolatileEmissionIndices(afterCall, afterCall, afterSnap, afterEvidence.Provenance)
			require.True(t, ok)
			after, ok := systemPromptDigest(afterCall, afterVolatile, key)
			require.True(t, ok)

			// The projected trajectories really differ, so a stable identity
			// is not vacuous.
			require.False(t, callHasText(beforeCall, "thinker memo"))
			require.True(t, callHasText(afterCall, "thinker memo"))
			require.Equal(t, before, after)
		})
	}
}

// TestSystemPromptDigest_VolatilePositionsRequireResolution pins the volatile
// exclusion policy: an active user-role overlay with no resolved final-call
// position makes the identity unverifiable, so the producer must publish
// nothing rather than hash a guessed placement. System/developer overlays and
// inactive overlays are not volatile, and a resolved position is excluded.
func TestSystemPromptDigest_VolatilePositionsRequireResolution(t *testing.T) {
	t.Parallel()
	activeMemo := conversationprojection.Overlay{
		OverlayID: "memo", Active: true,
		Message: conversationprojection.OverlayMessage{Role: aipapi.RoleUser, Text: "memo"},
	}
	activeSystem := conversationprojection.Overlay{
		OverlayID: "policy", Active: true,
		Message: conversationprojection.OverlayMessage{Role: aipapi.RoleSystem, Text: "policy"},
	}
	inactiveMemo := activeMemo
	inactiveMemo.Active = false
	cases := []struct {
		name      string
		snapshot  conversationprojection.Snapshot
		positions []conversationprojection.InjectedPosition
		wantOK    bool
		want      map[int]struct{}
	}{
		{
			name:     "active volatile overlay without a position refuses to publish",
			snapshot: conversationprojection.Snapshot{Steering: []conversationprojection.Overlay{activeMemo}},
			wantOK:   false,
		},
		{
			name:     "active volatile overlay missing from resolved positions refuses to publish",
			snapshot: conversationprojection.Snapshot{Steering: []conversationprojection.Overlay{activeMemo}},
			positions: []conversationprojection.InjectedPosition{
				{OverlayID: "other", TrajectoryIndex: 1},
			},
			wantOK: false,
		},
		{
			name:     "system overlay is not volatile",
			snapshot: conversationprojection.Snapshot{Steering: []conversationprojection.Overlay{activeSystem}},
			wantOK:   true,
		},
		{
			name:     "inactive volatile overlay is not volatile",
			snapshot: conversationprojection.Snapshot{Steering: []conversationprojection.Overlay{inactiveMemo}},
			wantOK:   true,
		},
		{
			name:     "resolved volatile position is excluded",
			snapshot: conversationprojection.Snapshot{Steering: []conversationprojection.Overlay{activeMemo}},
			positions: []conversationprojection.InjectedPosition{
				{OverlayID: "memo", TrajectoryIndex: 3},
			},
			wantOK: true,
			want:   map[int]struct{}{3: {}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := volatileEmissionIndices(tc.positions, tc.snapshot)
			require.Equal(t, tc.wantOK, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestSystemPromptLaunchSeq_ZeroWithoutSequence pins the chronology fallback:
// zero is reserved for "no chronology available", so a nil context stays nil,
// an unsequenced context reads zero, and a nil sequence yields zero. A
// fabricated non-zero fallback would make an unsequenced observation look like
// a real launch order.
func TestSystemPromptLaunchSeq_ZeroWithoutSequence(t *testing.T) {
	t.Parallel()
	var nilCtx context.Context
	if got := withSystemPromptLaunchSeq(nilCtx, 7); got != nil {
		t.Fatalf("withSystemPromptLaunchSeq(nil) = %v, want nil", got)
	}
	if got := systemPromptLaunchSeqFromContext(nilCtx); got != 0 {
		t.Fatalf("nil-context launch sequence = %d, want 0", got)
	}
	if got := systemPromptLaunchSeqFromContext(context.Background()); got != 0 {
		t.Fatalf("unsequenced launch sequence = %d, want 0", got)
	}
	if got := systemPromptLaunchSeqFromContext(withSystemPromptLaunchSeq(context.Background(), 9)); got != 9 {
		t.Fatalf("sequenced launch sequence = %d, want 9", got)
	}
	var nilSequence *RequestLaunchSequence
	if got := nilSequence.Next(); got != 0 {
		t.Fatalf("nil RequestLaunchSequence.Next() = %d, want 0", got)
	}
}

// callHasText reports whether any normalized trajectory entry carries text.
func callHasText(call aipapi.Call, text string) bool {
	for _, item := range aipapi.NormalizedItems(call) {
		for _, part := range item.Content {
			if part.Text == text {
				return true
			}
		}
	}
	return false
}

// adaptRuntimeCallToItems runs the production legacy-to-item authority
// adaptation used for ordered-item candidates.
func adaptRuntimeCallToItems(t *testing.T, call aipapi.Call) aipapi.Call {
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

// TestSystemPromptDigest_VolatilePositionsAfterAuthorityAdaptation proves the
// volatile exclusion is resolved in the final provider-bound call, not reused
// from the pre-adaptation projection. A tool-call expansion shifts the memo's
// trajectory index, so the stale index addresses a different entry and changes
// the instruction identity.
func TestSystemPromptDigest_VolatilePositionsAfterAuthorityAdaptation(t *testing.T) {
	t.Parallel()
	key := seamTestKey()
	u0 := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("u0")}}
	toolCalls := aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
		{Kind: aipapi.PartJSON, ToolCallID: "c1", ToolName: "w", Content: json.RawMessage(`{}`)},
		{Kind: aipapi.PartJSON, ToolCallID: "c2", ToolName: "w", Content: json.RawMessage(`{}`)},
	}}
	toolResults := aipapi.Message{Role: aipapi.RoleTool, Parts: []aipapi.Part{
		{Kind: aipapi.PartToolResult, ToolCallID: "c1", ToolName: "w", Text: "r1"},
		{Kind: aipapi.PartToolResult, ToolCallID: "c2", ToolName: "w", Text: "r2"},
	}}
	middlePolicy := aipapi.Message{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("policy A")}}
	anchor := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("u1")}}
	trailingPolicy := aipapi.Message{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("policy B")}}
	ingress := aipapi.Call{
		Messages: []aipapi.Message{u0, toolCalls, toolResults, middlePolicy, anchor, trailingPolicy},
		Tools:    []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
	}
	anchorID, err := conversationprojection.MessageIdentityOf(anchor)
	require.NoError(t, err)
	snap := conversationprojection.Snapshot{Steering: []conversationprojection.Overlay{{
		OverlayID:   "memo",
		SlotOrdinal: 1,
		Active:      true,
		Message:     conversationprojection.OverlayMessage{Role: aipapi.RoleUser, Text: "thinker memo"},
		Placement: conversationprojection.Placement{
			Kind:   conversationprojection.PlacementAfterMessage,
			Anchor: &conversationprojection.MessageAnchor{Identity: anchorID, Occurrence: 1},
		},
		AnchorMissingPolicy: conversationprojection.AnchorFailClosed,
	}}}
	projected, evidence, err := conversationprojection.Project(ingress, snap)
	require.NoError(t, err)
	require.Len(t, evidence.Provenance, 1)
	// The memo lands at pre-adaptation legacy index 5; the tool-call expansion
	// moves it to adapted item index 7.
	require.Equal(t, 5, evidence.Provenance[0].InjectedTrajectoryIndex)
	adapted := adaptRuntimeCallToItems(t, projected)
	require.Greater(t, len(adapted.Items), len(projected.Instructions)+len(projected.Messages))

	volatile, ok := adaptedVolatileEmissionIndices(adapted, projected, snap, evidence.Provenance)
	require.True(t, ok)
	require.Equal(t, map[int]struct{}{7: {}}, volatile)

	got, ok := systemPromptDigest(adapted, volatile, key)
	require.True(t, ok)
	withoutMemo := aipapi.CloneCall(adapted)
	withoutMemo.Items = append(append([]aipapi.Item(nil), adapted.Items[:7]...), adapted.Items[8:]...)
	want, ok := systemPromptDigest(withoutMemo, nil, key)
	require.True(t, ok)
	require.Equal(t, want, got, "the adapted memo position must reproduce the memo-free identity")

	// The pre-adaptation index addresses the first policy instruction in the
	// final trajectory; reusing it would count the memo and shift the last
	// instruction's effective position.
	stale, ok := systemPromptDigest(adapted, map[int]struct{}{5: {}}, key)
	require.True(t, ok)
	require.NotEqual(t, want, stale, "a pre-adaptation index must not be blindly reused")
}
