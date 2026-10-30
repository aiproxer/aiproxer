package sdkadapter_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview/sdkadapter"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
)

func isTerminalError(err error) bool {
	return errors.Is(err, conversationview.ErrTerminalUserNotFound) || errors.Is(err, conversationview.ErrTerminalNotUser)
}

func textMessage(role aipapi.Role, text string) aipapi.Message {
	return aipapi.Message{Role: role, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: text}}}
}

func textItem(role aipapi.Role, id, text string) aipapi.Item {
	return aipapi.Item{
		Kind:   aipapi.ItemKindMessage,
		ID:     id,
		Status: aipapi.ItemStatusCompleted,
		Role:   role,
		Content: []aipapi.ContentPart{
			{Kind: aipapi.ContentPartText, Text: text},
		},
	}
}

func TestWriter_Construction(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	_, err := sdkadapter.NewWriter(nil, "a_leg", nil)
	require.Error(t, err)
	_, err = sdkadapter.NewWriter(store, "   ", nil)
	require.Error(t, err)
	_, err = sdkadapter.NewWriter(store, strings.Repeat("a", 257), nil)
	require.Error(t, err)
	w, err := sdkadapter.NewWriter(store, "a_leg_001", nil)
	require.NoError(t, err)
	require.NotNil(t, w)
	var _ steering.Writer = w
}

func TestWriter_Put_Validation(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	require.NoError(t, store.CreateALeg(ctx, "a_writer_val"))
	w, err := sdkadapter.NewWriter(store, "a_writer_val", nil)
	require.NoError(t, err)

	tests := []struct {
		name    string
		mutate  func(steering.PutRequest) steering.PutRequest
		wantErr bool
	}{
		{
			name: "empty overlay id",
			mutate: func(r steering.PutRequest) steering.PutRequest {
				r.OverlayID = ""
				return r
			},
			wantErr: true,
		},
		{
			name: "oversized overlay id",
			mutate: func(r steering.PutRequest) steering.PutRequest {
				r.OverlayID = steering.OverlayID(strings.Repeat("a", 129))
				return r
			},
			wantErr: true,
		},
		{
			name: "empty text",
			mutate: func(r steering.PutRequest) steering.PutRequest {
				r.Message.Text = ""
				return r
			},
			wantErr: true,
		},
		{
			name: "oversized text",
			mutate: func(r steering.PutRequest) steering.PutRequest {
				r.Message.Text = strings.Repeat("a", 64*1024+1)
				return r
			},
			wantErr: true,
		},
		{
			name: "unknown placement",
			mutate: func(r steering.PutRequest) steering.PutRequest {
				r.Placement = "unknown"
				return r
			},
			wantErr: true,
		},
		{
			name: "unknown policy",
			mutate: func(r steering.PutRequest) steering.PutRequest {
				r.AnchorMissingPolicy = "unknown"
				return r
			},
			wantErr: true,
		},
		{
			name: "empty reason",
			mutate: func(r steering.PutRequest) steering.PutRequest {
				r.Reason = ""
				return r
			},
			wantErr: true,
		},
	}
	valid := steering.PutRequest{
		OverlayID:           "ov1",
		Message:             steering.Message{Role: aipapi.RoleUser, Text: "hello"},
		Placement:           steering.StablePrefix,
		AnchorMissingPolicy: steering.StablePrefixFallback,
		Reason:              "test_reason",
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := w.Put(ctx, tc.mutate(valid))
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestWriter_Put_StablePrefixPassthrough(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLeg := "a_stable_passthrough"
	require.NoError(t, store.CreateALeg(ctx, aLeg))
	w, err := sdkadapter.NewWriter(store, aLeg, nil)
	require.NoError(t, err)

	req := steering.PutRequest{
		OverlayID:           "ov-stable",
		Message:             steering.Message{Role: aipapi.RoleUser, Text: "stable steering"},
		Placement:           steering.StablePrefix,
		AnchorMissingPolicy: steering.StablePrefixFallback,
		Reason:              "r1",
	}
	st, err := w.Put(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, steering.OverlayID("ov-stable"), st.OverlayID)
	assert.Equal(t, uint64(1), st.Revision)
	assert.True(t, st.Active)
	assert.Greater(t, st.SlotOrdinal, uint64(0))

	// Verify stored placement is stable_prefix without anchor
	ov, err := store.GetOverlay(ctx, aLeg, "ov-stable")
	require.NoError(t, err)
	assert.Equal(t, conversationview.PlacementStablePrefix, ov.Placement.Kind)
	assert.Nil(t, ov.Placement.Anchor)
	assert.Equal(t, "stable steering", ov.Message.Text)
	assert.Equal(t, aipapi.RoleUser, ov.Message.Role)
}

func TestWriter_Put_AfterIngressTailSuccess(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLeg := "a_after_success"
	require.NoError(t, store.CreateALeg(ctx, aLeg))

	u1 := textMessage(aipapi.RoleUser, "hello tail")
	u2 := textMessage(aipapi.RoleUser, "second tail")
	call := aipapi.Call{
		Instructions: []aipapi.Message{textMessage(aipapi.RoleSystem, "sys")},
		Messages:     []aipapi.Message{u1, u2},
	}
	snap := conversationview.Snapshot{
		StateRevision: 0,
		NeverBackend:  nil,
		Steering:      nil,
	}
	resolver := func(context.Context) (aipapi.Call, conversationview.Snapshot, error) {
		return call, snap, nil
	}
	w, err := sdkadapter.NewWriter(store, aLeg, resolver)
	require.NoError(t, err)

	req := steering.PutRequest{
		OverlayID:           "ov-after",
		Message:             steering.Message{Role: aipapi.RoleSystem, Text: "after steering payload"},
		Placement:           steering.AfterIngressTail,
		AnchorMissingPolicy: steering.FailClosed,
		Reason:              "r1",
	}
	st, err := w.Put(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), st.Revision)
	assert.True(t, st.Active)

	// Verify anchor is persisted correctly (terminal user u2)
	ov, err := store.GetOverlay(ctx, aLeg, "ov-after")
	require.NoError(t, err)
	require.NotNil(t, ov.Placement.Anchor)
	expectedID, _ := conversationview.MessageIdentityOf(u2)
	assert.Equal(t, expectedID, ov.Placement.Anchor.Identity)
	assert.Equal(t, uint32(1), ov.Placement.Anchor.Occurrence)
	assert.Equal(t, conversationview.PlacementAfterMessage, ov.Placement.Kind)
	assert.Equal(t, "after steering payload", ov.Message.Text)
	// Ensure verbatim persistence: stored text equals input
	assert.Equal(t, req.Message.Text, ov.Message.Text)
}

func TestWriter_Put_AfterIngressTailRejections(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLegBase := "a_after_reject"
	require.NoError(t, store.CreateALeg(ctx, aLegBase+"_never"))
	require.NoError(t, store.CreateALeg(ctx, aLegBase+"_absent"))
	require.NoError(t, store.CreateALeg(ctx, aLegBase+"_unsafe"))
	require.NoError(t, store.CreateALeg(ctx, aLegBase+"_nil_resolver"))

	// 1) never_backend terminal: snapshot marks terminal as excluded -> ErrTerminalUserNotFound
	t.Run("never_backend terminal", func(t *testing.T) {
		t.Parallel()
		u1 := textMessage(aipapi.RoleUser, "hello never")
		id, _ := conversationview.MessageIdentityOf(u1)
		call := aipapi.Call{Instructions: []aipapi.Message{textMessage(aipapi.RoleSystem, "sys")}, Messages: []aipapi.Message{u1}}
		snap := conversationview.Snapshot{NeverBackend: []conversationview.Tag{{Identity: id, Reason: "r"}}}
		resolver := func(context.Context) (aipapi.Call, conversationview.Snapshot, error) { return call, snap, nil }
		w, _ := sdkadapter.NewWriter(store, aLegBase+"_never", resolver)
		_, err := w.Put(ctx, steering.PutRequest{
			OverlayID:           "ov1",
			Message:             steering.Message{Role: aipapi.RoleUser, Text: "steering"},
			Placement:           steering.AfterIngressTail,
			AnchorMissingPolicy: steering.FailClosed,
			Reason:              "r",
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, conversationview.ErrTerminalUserNotFound)
		// No mutation on failure
		snapStore, _ := store.Snapshot(ctx, aLegBase+"_never")
		assert.Empty(t, snapStore.Steering)
	})

	// 2) absent terminal: no user message
	t.Run("absent terminal", func(t *testing.T) {
		t.Parallel()
		call := aipapi.Call{Instructions: []aipapi.Message{textMessage(aipapi.RoleSystem, "sys")}, Messages: []aipapi.Message{textMessage(aipapi.RoleAssistant, "assistant only")}}
		snap := conversationview.Snapshot{}
		resolver := func(context.Context) (aipapi.Call, conversationview.Snapshot, error) { return call, snap, nil }
		w, _ := sdkadapter.NewWriter(store, aLegBase+"_absent", resolver)
		_, err := w.Put(ctx, steering.PutRequest{
			OverlayID:           "ov1",
			Message:             steering.Message{Role: aipapi.RoleUser, Text: "steering"},
			Placement:           steering.AfterIngressTail,
			AnchorMissingPolicy: steering.FailClosed,
			Reason:              "r",
		})
		require.Error(t, err)
		// Expect terminal not found or not user
		assert.True(t, isTerminalError(err))
	})

	// 3) unsafe anchor: terminal not user (assistant at tail)
	t.Run("terminal not user", func(t *testing.T) {
		t.Parallel()
		u1 := textMessage(aipapi.RoleUser, "user")
		a1 := textMessage(aipapi.RoleAssistant, "assistant tail")
		call := aipapi.Call{Instructions: []aipapi.Message{textMessage(aipapi.RoleSystem, "sys")}, Messages: []aipapi.Message{u1, a1}}
		snap := conversationview.Snapshot{}
		resolver := func(context.Context) (aipapi.Call, conversationview.Snapshot, error) { return call, snap, nil }
		w, _ := sdkadapter.NewWriter(store, aLegBase+"_unsafe", resolver)
		_, err := w.Put(ctx, steering.PutRequest{
			OverlayID:           "ov1",
			Message:             steering.Message{Role: aipapi.RoleUser, Text: "steering"},
			Placement:           steering.AfterIngressTail,
			AnchorMissingPolicy: steering.FailClosed,
			Reason:              "r",
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, conversationview.ErrTerminalNotUser)
	})

	// 4) nil resolver with after_ingress_tail
	t.Run("nil resolver", func(t *testing.T) {
		t.Parallel()
		w, _ := sdkadapter.NewWriter(store, aLegBase+"_nil_resolver", nil)
		_, err := w.Put(ctx, steering.PutRequest{
			OverlayID:           "ov1",
			Message:             steering.Message{Role: aipapi.RoleUser, Text: "steering"},
			Placement:           steering.AfterIngressTail,
			AnchorMissingPolicy: steering.FailClosed,
			Reason:              "r",
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, conversationview.ErrTerminalUserNotFound)
	})

	// 5) resolver returns error
	t.Run("resolver error", func(t *testing.T) {
		t.Parallel()
		aLeg := aLegBase + "_resolver_err"
		require.NoError(t, store.CreateALeg(ctx, aLeg))
		resolver := func(context.Context) (aipapi.Call, conversationview.Snapshot, error) {
			return aipapi.Call{}, conversationview.Snapshot{}, assert.AnError
		}
		w, _ := sdkadapter.NewWriter(store, aLeg, resolver)
		_, err := w.Put(ctx, steering.PutRequest{
			OverlayID:           "ov1",
			Message:             steering.Message{Role: aipapi.RoleUser, Text: "steering"},
			Placement:           steering.AfterIngressTail,
			AnchorMissingPolicy: steering.FailClosed,
			Reason:              "r",
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, assert.AnError)
	})
}

func TestWriter_Put_AfterIngressTailExcludedTerminalNoFallback(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	t.Run("legacy excluded terminal with earlier user must reject not fallback", func(t *testing.T) {
		t.Parallel()
		aLeg := "a_excluded_fallback_legacy"
		require.NoError(t, store.CreateALeg(ctx, aLeg))
		u1 := textMessage(aipapi.RoleUser, "first user")
		u2 := textMessage(aipapi.RoleUser, "second excluded")
		id2, _ := conversationview.MessageIdentityOf(u2)
		call := aipapi.Call{
			Instructions: []aipapi.Message{textMessage(aipapi.RoleSystem, "sys")},
			Messages:     []aipapi.Message{u1, u2},
		}
		snap := conversationview.Snapshot{NeverBackend: []conversationview.Tag{{Identity: id2, Reason: "r"}}}
		resolver := func(context.Context) (aipapi.Call, conversationview.Snapshot, error) { return call, snap, nil }
		w, _ := sdkadapter.NewWriter(store, aLeg, resolver)
		_, err := w.Put(ctx, steering.PutRequest{
			OverlayID:           "ov-fallback-test",
			Message:             steering.Message{Role: aipapi.RoleSystem, Text: "steering"},
			Placement:           steering.AfterIngressTail,
			AnchorMissingPolicy: steering.FailClosed,
			Reason:              "r",
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, conversationview.ErrTerminalUserNotFound)
		assert.False(t, strings.Contains(err.Error(), "second excluded"))
		// No mutation.
		s, _ := store.Snapshot(ctx, aLeg)
		assert.Empty(t, s.Steering)
	})
	t.Run("item authority excluded terminal with earlier user must reject", func(t *testing.T) {
		t.Parallel()
		aLeg := "a_excluded_fallback_item"
		require.NoError(t, store.CreateALeg(ctx, aLeg))
		u1 := textItem(aipapi.RoleUser, "u1", "first user")
		u2 := textItem(aipapi.RoleUser, "u2", "second excluded")
		id2, _ := conversationview.ItemIdentityOf(u2)
		call := aipapi.Call{Items: []aipapi.Item{u1, u2}}
		snap := conversationview.Snapshot{NeverBackend: []conversationview.Tag{{Identity: id2, Reason: "r"}}}
		resolver := func(context.Context) (aipapi.Call, conversationview.Snapshot, error) { return call, snap, nil }
		w, _ := sdkadapter.NewWriter(store, aLeg, resolver)
		_, err := w.Put(ctx, steering.PutRequest{
			OverlayID:           "ov-fallback-item",
			Message:             steering.Message{Role: aipapi.RoleSystem, Text: "steering"},
			Placement:           steering.AfterIngressTail,
			AnchorMissingPolicy: steering.FailClosed,
			Reason:              "r",
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, conversationview.ErrTerminalUserNotFound)
	})
}

// TestWriter_Put_ConcurrentTagBeforePersistRejects reproduces the deterministic
// resolve/tag/persist TOCTOU sequence: the writer resolves a clean anchor at
// state revision N, an exclusion commits at N+1, and the subsequent PutSteering
// must be rejected by the store's atomic registration invariant instead of
// persisting an anchor that projection would immediately remove.
func TestWriter_Put_ConcurrentTagBeforePersistRejects(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLeg := "a_toctou_tag_before_put"
	require.NoError(t, store.CreateALeg(ctx, aLeg))

	u1 := textMessage(aipapi.RoleUser, "turn one")
	call := aipapi.Call{
		Instructions: []aipapi.Message{textMessage(aipapi.RoleSystem, "sys")},
		Messages:     []aipapi.Message{u1},
	}
	anchor, err := conversationview.ResolveAfterIngressTailAnchor(call, conversationview.Snapshot{})
	require.NoError(t, err)

	// Concurrent TagNeverBackend(U) commits between resolution and persistence.
	_, err = store.TagNeverBackend(ctx, aLeg, []conversationview.TagRequest{{Identity: anchor.Identity, Reason: conversationview.ReasonCode("late_tag")}})
	require.NoError(t, err)

	resolver := func(context.Context) (aipapi.Call, conversationview.Snapshot, error) {
		return call, conversationview.Snapshot{}, nil
	}
	w, err := sdkadapter.NewWriter(store, aLeg, resolver)
	require.NoError(t, err)
	_, err = w.Put(ctx, steering.PutRequest{
		OverlayID:           "ov-toctou",
		Message:             steering.Message{Role: aipapi.RoleSystem, Text: "anchored steering"},
		Placement:           steering.AfterIngressTail,
		AnchorMissingPolicy: steering.FailClosed,
		Reason:              "r",
	})
	require.Error(t, err, "persistence after concurrent exclusion must reject")
	assert.ErrorIs(t, err, conversationview.ErrSteeringAnchorExcluded)

	snap, serr := store.Snapshot(ctx, aLeg)
	require.NoError(t, serr)
	assert.Empty(t, snap.Steering, "rejected registration must not persist an overlay")
	assert.Len(t, snap.NeverBackend, 1)
}

func TestWriter_Put_StateMappingAndErrorMapping(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLeg := "a_state_map"
	require.NoError(t, store.CreateALeg(ctx, aLeg))
	w, err := sdkadapter.NewWriter(store, aLeg, nil)
	require.NoError(t, err)

	// Create first overlay
	req := steering.PutRequest{
		OverlayID:           "ov-map",
		Message:             steering.Message{Role: aipapi.RoleUser, Text: "first"},
		Placement:           steering.StablePrefix,
		AnchorMissingPolicy: steering.StablePrefixFallback,
		Reason:              "r1",
	}
	st1, err := w.Put(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, steering.OverlayID("ov-map"), st1.OverlayID)
	assert.Equal(t, uint64(1), st1.Revision)
	assert.True(t, st1.Active)

	// Replace with different text - revision should bump, slot retained
	req.Message.Text = "second"
	st2, err := w.Put(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, st1.SlotOrdinal, st2.SlotOrdinal, "replace must retain slot")
	assert.Greater(t, st2.Revision, st1.Revision)

	// Caps: fill 64 overlays
	aLegCap := "a_cap_test"
	require.NoError(t, store.CreateALeg(ctx, aLegCap))
	wCap, _ := sdkadapter.NewWriter(store, aLegCap, nil)
	for i := range conversationview.MaxActiveOverlays {
		_, err := wCap.Put(ctx, steering.PutRequest{
			OverlayID:           steering.OverlayID(fmt.Sprintf("ov-cap-%03d", i)),
			Message:             steering.Message{Role: aipapi.RoleUser, Text: "text"},
			Placement:           steering.StablePrefix,
			AnchorMissingPolicy: steering.StablePrefixFallback,
			Reason:              "r",
		})
		require.NoError(t, err)
	}
	_, err = wCap.Put(ctx, steering.PutRequest{
		OverlayID:           "ov-overflow",
		Message:             steering.Message{Role: aipapi.RoleUser, Text: "overflow"},
		Placement:           steering.StablePrefix,
		AnchorMissingPolicy: steering.StablePrefixFallback,
		Reason:              "r",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, conversationview.ErrSteeringLimitExceeded)
}

func TestWriter_Put_NoPlaintextInErrors(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLeg := "a_no_plaintext"
	require.NoError(t, store.CreateALeg(ctx, aLeg))
	// Create a writer and try to exceed cap with secret text; error must not contain secret
	w, _ := sdkadapter.NewWriter(store, aLeg, nil)
	secret := "SUPER_SECRET_STEERING_PAYLOAD_123"
	// Fill to trigger limit: we will use a failing after_ingress_tail with secret text but error should not leak
	u1 := textMessage(aipapi.RoleUser, "hello")
	call := aipapi.Call{Instructions: []aipapi.Message{textMessage(aipapi.RoleSystem, "sys")}, Messages: []aipapi.Message{u1}}
	// snapshot marks terminal as never_backend to cause rejection after already having secret in request
	id, _ := conversationview.MessageIdentityOf(u1)
	snap := conversationview.Snapshot{NeverBackend: []conversationview.Tag{{Identity: id, Reason: "r"}}}
	resolver := func(context.Context) (aipapi.Call, conversationview.Snapshot, error) { return call, snap, nil }
	w2, _ := sdkadapter.NewWriter(store, aLeg, resolver)
	_, err := w2.Put(ctx, steering.PutRequest{
		OverlayID:           "ov-secret",
		Message:             steering.Message{Role: aipapi.RoleUser, Text: secret},
		Placement:           steering.AfterIngressTail,
		AnchorMissingPolicy: steering.FailClosed,
		Reason:              "r",
	})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret, "error must not leak steering plaintext")
	_ = w
	_ = secret
}

func TestWriter_Put_IdempotentNoOp(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLeg := "a_noop"
	require.NoError(t, store.CreateALeg(ctx, aLeg))
	w, _ := sdkadapter.NewWriter(store, aLeg, nil)
	req := steering.PutRequest{
		OverlayID:           "ov-noop",
		Message:             steering.Message{Role: aipapi.RoleUser, Text: "same text"},
		Placement:           steering.StablePrefix,
		AnchorMissingPolicy: steering.StablePrefixFallback,
		Reason:              "r1",
	}
	st1, err := w.Put(ctx, req)
	require.NoError(t, err)
	snap1, _ := store.Snapshot(ctx, aLeg)
	rev1 := snap1.StateRevision
	st2, err := w.Put(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, st1.Revision, st2.Revision, "semantic no-op must not bump revision")
	assert.Equal(t, st1.SlotOrdinal, st2.SlotOrdinal)
	snap2, _ := store.Snapshot(ctx, aLeg)
	assert.Equal(t, rev1, snap2.StateRevision, "no-op must not bump StateRevision")
}

func TestWriter_Deactivate(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLeg := "a_deact"
	require.NoError(t, store.CreateALeg(ctx, aLeg))
	w, _ := sdkadapter.NewWriter(store, aLeg, nil)
	// Put then deactivate
	req := steering.PutRequest{
		OverlayID:           "ov-deact",
		Message:             steering.Message{Role: aipapi.RoleUser, Text: "to be deactivated"},
		Placement:           steering.StablePrefix,
		AnchorMissingPolicy: steering.StablePrefixFallback,
		Reason:              "r1",
	}
	stPut, err := w.Put(ctx, req)
	require.NoError(t, err)
	require.True(t, stPut.Active)

	stDeact, err := w.Deactivate(ctx, "ov-deact")
	require.NoError(t, err)
	assert.False(t, stDeact.Active)
	assert.Greater(t, stDeact.Revision, stPut.Revision)
	assert.Equal(t, stPut.SlotOrdinal, stDeact.SlotOrdinal)

	// Idempotent second deactivate
	stDeact2, err := w.Deactivate(ctx, "ov-deact")
	require.NoError(t, err)
	assert.Equal(t, stDeact.Revision, stDeact2.Revision)
	assert.False(t, stDeact2.Active)

	// Invalid ID
	_, err = w.Deactivate(ctx, "")
	require.Error(t, err)

	// Not found
	_, err = w.Deactivate(ctx, "nonexistent")
	require.Error(t, err)
	assert.ErrorIs(t, err, conversationview.ErrOverlayNotFound)

	// Validation of overlay ID via steering
	_, err = w.Deactivate(ctx, steering.OverlayID("bad/id"))
	require.Error(t, err)
}

func TestWriter_ItemAuthorityTrajectory(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLeg := "a_item_traj"
	require.NoError(t, store.CreateALeg(ctx, aLeg))
	u1 := textItem(aipapi.RoleUser, "u1", "hello tail")
	call := aipapi.Call{Items: []aipapi.Item{u1}}
	snap := conversationview.Snapshot{}
	resolver := func(context.Context) (aipapi.Call, conversationview.Snapshot, error) { return call, snap, nil }
	w, _ := sdkadapter.NewWriter(store, aLeg, resolver)
	st, err := w.Put(ctx, steering.PutRequest{
		OverlayID:           "ov-item",
		Message:             steering.Message{Role: aipapi.RoleUser, Text: "steering"},
		Placement:           steering.AfterIngressTail,
		AnchorMissingPolicy: steering.FailClosed,
		Reason:              "r",
	})
	require.NoError(t, err)
	assert.True(t, st.Active)
	ov, _ := store.GetOverlay(ctx, aLeg, "ov-item")
	require.NotNil(t, ov.Placement.Anchor)
}

func TestWriter_TrustedConstructionNoGlobal(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	_ = store.CreateALeg(context.Background(), "a_trusted_1")
	_ = store.CreateALeg(context.Background(), "a_trusted_2")
	w1, err := sdkadapter.NewWriter(store, "a_trusted_1", nil)
	require.NoError(t, err)
	w2, err := sdkadapter.NewWriter(store, "a_trusted_2", nil)
	require.NoError(t, err)
	assert.NotSame(t, w1, w2)
}
