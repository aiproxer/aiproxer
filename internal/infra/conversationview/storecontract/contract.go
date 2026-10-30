// Package storecontract holds reusable contract tests for [conversationview.Store]
// implementations (ReferenceStore, MemoryStore, Bun). The driver is exercised by
// both the canonical ReferenceStore and the b2bua MemoryStore capability to
// satisfy Req 13.8.
package storecontract

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Deps is one isolated store under test plus A-leg lifecycle hooks.
// CreateALeg must ensure an A-leg with the deterministic ID exists for
// subsequent Snapshot/Tag/Put operations. DeleteALeg removes its
// conversation-view state and makes subsequent Snapshot return ErrALegNotFound.
// GetOverlay is an optional test seam to inspect deactivated overlay
// persistence; if nil, the deactivation persistence check is skipped.
type Deps struct {
	Store      conversationview.Store
	CreateALeg func(ctx context.Context, aLegID string) error
	DeleteALeg func(ctx context.Context, aLegID string) error
	GetOverlay func(ctx context.Context, aLegID, overlayID string) (conversationview.SteeringOverlay, error)
}

// Env constructs a fresh Deps per subtest.
type Env struct {
	New   func(t *testing.T) Deps
	Spawn func(fn func())
}

func testALegID(seed string) string {
	h := fmt.Sprintf("%032x", 0)
	if seed != "" {
		var v uint64
		for _, c := range seed {
			v = v*31 + uint64(c)
		}
		h = fmt.Sprintf("%032x", v)
		if len(h) > 32 {
			h = h[len(h)-32:]
		}
	}
	return "a_" + h
}

func testIdentity(n int) conversationview.MessageIdentity {
	hex := fmt.Sprintf("%064x", n)
	return conversationview.MessageIdentity("v1:" + hex)
}

func testReason(s string) conversationview.ReasonCode {
	if s == "" {
		s = "test_reason"
	}
	return conversationview.ReasonCode(s)
}

func testMessage(text string, role aipapi.Role) conversationview.StoredMessageV1 {
	if role == "" {
		role = aipapi.RoleUser
	}
	return conversationview.StoredMessageV1{Role: role, Text: text}
}

func stablePlacement() conversationview.StoredPlacement {
	return conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix}
}

func afterPlacement(identity conversationview.MessageIdentity, occ uint32) conversationview.StoredPlacement {
	return conversationview.StoredPlacement{
		Kind:   conversationview.PlacementAfterMessage,
		Anchor: &conversationview.MessageAnchor{Identity: identity, Occurrence: occ},
	}
}

func snapshotMust(t *testing.T, s conversationview.Reader, aLegID string) conversationview.Snapshot {
	t.Helper()
	snap, err := s.Snapshot(context.Background(), aLegID)
	require.NoError(t, err)
	return snap
}

func createALegMust(t *testing.T, deps Deps, aLegID string) {
	t.Helper()
	require.NoError(t, deps.CreateALeg(context.Background(), aLegID))
}

// Run exercises the full conversation-view store contract against one adapter.
// It preserves all pinned semantics from the original pinned suite in
// storecontract_test.go plus two tightenings:
//   - ConcurrentSmoke asserts exact count (10) rather than ≤10.
//   - Steering aggregate 256KiB cap includes a replace-overflow atomicity assertion.
func Run(t *testing.T, env Env) {
	t.Helper()
	if env.New == nil {
		t.Fatal("ContractEnv.New is required")
	}
	if env.Spawn == nil {
		env.Spawn = func(fn func()) { go fn() }
	}

	t.Run("SnapshotNotFound", func(t *testing.T) {
		t.Parallel()
		deps := env.New(t)
		unknown := testALegID("unknown-leg")
		_, err := deps.Store.Snapshot(context.Background(), unknown)
		require.ErrorIs(t, err, conversationview.ErrALegNotFound)

		_, err = deps.Store.Snapshot(context.Background(), "")
		require.ErrorIs(t, err, conversationview.ErrALegNotFound)
		_, err = deps.Store.Snapshot(context.Background(), "   ")
		require.ErrorIs(t, err, conversationview.ErrALegNotFound)

		aLeg := testALegID("leg-notfound-after-create")
		createALegMust(t, deps, aLeg)
		snap := snapshotMust(t, deps.Store, aLeg)
		assert.Equal(t, uint64(0), snap.StateRevision)
		assert.Empty(t, snap.NeverBackend)
		assert.Empty(t, snap.Steering)
	})

	t.Run("SnapshotDeepCopy", func(t *testing.T) {
		t.Parallel()
		deps := env.New(t)
		aLeg := testALegID("deep-copy")
		createALegMust(t, deps, aLeg)

		_, err := deps.Store.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{{Identity: testIdentity(1), Reason: testReason("reason_one")}})
		require.NoError(t, err)
		_, err = deps.Store.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
			OverlayID:           "ov1",
			Message:             testMessage("hello steering", aipapi.RoleUser),
			Placement:           stablePlacement(),
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              testReason("r1"),
		})
		require.NoError(t, err)

		snap1 := snapshotMust(t, deps.Store, aLeg)
		require.Len(t, snap1.NeverBackend, 1)
		require.Len(t, snap1.Steering, 1)

		snap1.NeverBackend[0].Reason = "mutated"
		snap1.NeverBackend = append(snap1.NeverBackend, conversationview.Tag{Identity: testIdentity(999), Reason: "evil"})
		snap1.Steering[0].Message.Text = "mutated text"
		snap1.Steering = append(snap1.Steering, conversationview.Overlay{OverlayID: "evil"})

		snap2 := snapshotMust(t, deps.Store, aLeg)
		require.Len(t, snap2.NeverBackend, 1)
		assert.Equal(t, conversationview.ReasonCode("reason_one"), snap2.NeverBackend[0].Reason)
		assert.Equal(t, "hello steering", snap2.Steering[0].Message.Text)
		assert.Equal(t, snap1.StateRevision, snap2.StateRevision)

		if snap1.Steering[0].Placement.Anchor != nil {
			// stable has no anchor, placeholder
		} else {
			_, err = deps.Store.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
				OverlayID:           "ov2",
				Message:             testMessage("second", aipapi.RoleUser),
				Placement:           afterPlacement(testIdentity(10), 1),
				AnchorMissingPolicy: conversationview.AnchorFailClosed,
				Reason:              testReason("r2"),
			})
			require.NoError(t, err)
			snap3 := snapshotMust(t, deps.Store, aLeg)
			require.Len(t, snap3.Steering, 2)
			for i := range snap3.Steering {
				if snap3.Steering[i].OverlayID == "ov2" && snap3.Steering[i].Placement.Anchor != nil {
					snap3.Steering[i].Placement.Anchor.Occurrence = 999
				}
			}
			snap4 := snapshotMust(t, deps.Store, aLeg)
			for _, ov := range snap4.Steering {
				if ov.OverlayID == "ov2" {
					assert.Equal(t, uint32(1), ov.Placement.Anchor.Occurrence)
				}
			}
		}
	})

	t.Run("TagBatchAtomicity", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name  string
			batch []conversationview.TagRequest
		}{
			{
				name: "invalid identity in batch",
				batch: []conversationview.TagRequest{
					{Identity: testIdentity(1), Reason: testReason("ok")},
					{Identity: "invalid-identity", Reason: testReason("ok")},
				},
			},
			{
				name: "invalid reason in batch",
				batch: []conversationview.TagRequest{
					{Identity: testIdentity(1), Reason: testReason("ok")},
					{Identity: testIdentity(2), Reason: "bad reason with spaces!"},
				},
			},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				deps := env.New(t)
				aLeg := testALegID("atomic-" + tc.name)
				createALegMust(t, deps, aLeg)
				_, err := deps.Store.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{{Identity: testIdentity(100), Reason: testReason("seed")}})
				require.NoError(t, err)
				snapBefore := snapshotMust(t, deps.Store, aLeg)
				_, err = deps.Store.TagNeverBackend(context.Background(), aLeg, tc.batch)
				require.Error(t, err)
				snapAfter := snapshotMust(t, deps.Store, aLeg)
				assert.Equal(t, snapBefore.StateRevision, snapAfter.StateRevision, "revision must not change on atomic failure")
				assert.Equal(t, len(snapBefore.NeverBackend), len(snapAfter.NeverBackend))
				for _, tag := range snapAfter.NeverBackend {
					assert.NotEqual(t, testIdentity(2), tag.Identity)
				}
			})
		}
	})

	t.Run("TagIdempotency", func(t *testing.T) {
		t.Parallel()
		deps := env.New(t)
		aLeg := testALegID("idempotent")
		createALegMust(t, deps, aLeg)

		res1, err := deps.Store.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{
			{Identity: testIdentity(1), Reason: testReason("r1")},
			{Identity: testIdentity(2), Reason: testReason("r1")},
		})
		require.NoError(t, err)
		rev1 := res1.StateRevision
		snap1 := snapshotMust(t, deps.Store, aLeg)
		require.Len(t, snap1.NeverBackend, 2)

		res2, err := deps.Store.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{
			{Identity: testIdentity(1), Reason: testReason("different_reason")},
			{Identity: testIdentity(2), Reason: testReason("r1")},
		})
		require.NoError(t, err)
		assert.Equal(t, rev1, res2.StateRevision, "idempotent re-tag must not bump revision")
		snap2 := snapshotMust(t, deps.Store, aLeg)
		assert.Equal(t, snap1.StateRevision, snap2.StateRevision)
		require.Len(t, snap2.NeverBackend, 2)

		res3, err := deps.Store.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{
			{Identity: testIdentity(1), Reason: testReason("r1")},
			{Identity: testIdentity(1), Reason: testReason("r1")},
			{Identity: testIdentity(2), Reason: testReason("r1")},
		})
		require.NoError(t, err)
		assert.Equal(t, rev1, res3.StateRevision)
	})

	t.Run("TagCap4096", func(t *testing.T) {
		t.Parallel()
		deps := env.New(t)
		aLeg := testALegID("cap4096")
		createALegMust(t, deps, aLeg)

		batch := make([]conversationview.TagRequest, conversationview.MaxNeverBackendTags)
		for i := range conversationview.MaxNeverBackendTags {
			batch[i] = conversationview.TagRequest{Identity: testIdentity(i + 1), Reason: testReason("r")}
		}
		res, err := deps.Store.TagNeverBackend(context.Background(), aLeg, batch)
		require.NoError(t, err)
		assert.Equal(t, conversationview.MaxNeverBackendTags, len(res.Tags))
		snap := snapshotMust(t, deps.Store, aLeg)
		assert.Equal(t, conversationview.MaxNeverBackendTags, len(snap.NeverBackend))

		_, err = deps.Store.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{{Identity: testIdentity(999999), Reason: testReason("r")}})
		require.ErrorIs(t, err, conversationview.ErrTagLimitExceeded)
		snap2 := snapshotMust(t, deps.Store, aLeg)
		assert.Equal(t, snap.StateRevision, snap2.StateRevision)
		assert.Len(t, snap2.NeverBackend, conversationview.MaxNeverBackendTags)

		revBefore := snap2.StateRevision
		res2, err := deps.Store.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{{Identity: testIdentity(1), Reason: testReason("r")}})
		require.NoError(t, err)
		assert.Equal(t, revBefore, res2.StateRevision)

		_, err = deps.Store.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{
			{Identity: testIdentity(1), Reason: testReason("r")},
			{Identity: testIdentity(999998), Reason: testReason("r")},
		})
		require.ErrorIs(t, err, conversationview.ErrTagLimitExceeded)
		snap3 := snapshotMust(t, deps.Store, aLeg)
		assert.Equal(t, revBefore, snap3.StateRevision)
	})

	t.Run("SteeringSlotOrdering", func(t *testing.T) {
		t.Parallel()
		deps := env.New(t)
		aLeg := testALegID("slot-order")
		createALegMust(t, deps, aLeg)

		st1, err := deps.Store.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
			OverlayID:           "ov-a",
			Message:             testMessage("first", aipapi.RoleUser),
			Placement:           stablePlacement(),
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              testReason("r1"),
		})
		require.NoError(t, err)
		st2, err := deps.Store.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
			OverlayID:           "ov-b",
			Message:             testMessage("second", aipapi.RoleUser),
			Placement:           stablePlacement(),
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              testReason("r1"),
		})
		require.NoError(t, err)
		assert.Greater(t, st2.SlotOrdinal, st1.SlotOrdinal, "slot must be monotonic")
		st3, err := deps.Store.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
			OverlayID:           "ov-c",
			Message:             testMessage("third", aipapi.RoleUser),
			Placement:           afterPlacement(testIdentity(10), 1),
			AnchorMissingPolicy: conversationview.AnchorFailClosed,
			Reason:              testReason("r1"),
		})
		require.NoError(t, err)
		assert.Greater(t, st3.SlotOrdinal, st2.SlotOrdinal)

		snap := snapshotMust(t, deps.Store, aLeg)
		require.Len(t, snap.Steering, 3)
		for i := 1; i < len(snap.Steering); i++ {
			assert.Greater(t, snap.Steering[i].SlotOrdinal, snap.Steering[i-1].SlotOrdinal)
		}
	})

	t.Run("SteeringReplaceRetainsSlot", func(t *testing.T) {
		t.Parallel()
		deps := env.New(t)
		aLeg := testALegID("replace-slot")
		createALegMust(t, deps, aLeg)

		st1, err := deps.Store.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
			OverlayID:           "ov1",
			Message:             testMessage("v1", aipapi.RoleUser),
			Placement:           stablePlacement(),
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              testReason("r1"),
		})
		require.NoError(t, err)
		slot1 := st1.SlotOrdinal
		rev1 := st1.Revision

		st2, err := deps.Store.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
			OverlayID:           "ov1",
			Message:             testMessage("v2", aipapi.RoleUser),
			Placement:           stablePlacement(),
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              testReason("r1"),
		})
		require.NoError(t, err)
		assert.Equal(t, slot1, st2.SlotOrdinal, "replace must retain slot when placement unchanged")
		assert.Greater(t, st2.Revision, rev1)

		st3, err := deps.Store.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
			OverlayID:           "ov1",
			Message:             testMessage("v2", aipapi.RoleUser),
			Placement:           afterPlacement(testIdentity(20), 2),
			AnchorMissingPolicy: conversationview.AnchorFailClosed,
			Reason:              testReason("r1"),
		})
		require.NoError(t, err)
		assert.NotEqual(t, slot1, st3.SlotOrdinal, "placement change must allocate new slot")
		assert.Greater(t, st3.SlotOrdinal, slot1)
	})

	t.Run("SteeringNoOpDoesNotBumpRevision", func(t *testing.T) {
		t.Parallel()
		deps := env.New(t)
		aLeg := testALegID("noop")
		createALegMust(t, deps, aLeg)

		req := conversationview.PutSteeringRequest{
			OverlayID:           "ov-noop",
			Message:             testMessage("same text", aipapi.RoleUser),
			Placement:           stablePlacement(),
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              testReason("r1"),
		}
		st1, err := deps.Store.PutSteering(context.Background(), aLeg, req)
		require.NoError(t, err)
		snap1 := snapshotMust(t, deps.Store, aLeg)
		rev1 := snap1.StateRevision

		st2, err := deps.Store.PutSteering(context.Background(), aLeg, req)
		require.NoError(t, err)
		assert.Equal(t, st1.Revision, st2.Revision, "semantic no-op must not bump overlay revision")
		assert.Equal(t, st1.SlotOrdinal, st2.SlotOrdinal)
		snap2 := snapshotMust(t, deps.Store, aLeg)
		assert.Equal(t, rev1, snap2.StateRevision, "no-op must not bump StateRevision")
	})

	t.Run("SteeringRevisionBumpsOnChange", func(t *testing.T) {
		t.Parallel()
		deps := env.New(t)
		aLeg := testALegID("rev-bump")
		createALegMust(t, deps, aLeg)

		req1 := conversationview.PutSteeringRequest{
			OverlayID:           "ov-rev",
			Message:             testMessage("text1", aipapi.RoleUser),
			Placement:           stablePlacement(),
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              testReason("r1"),
		}
		st1, err := deps.Store.PutSteering(context.Background(), aLeg, req1)
		require.NoError(t, err)

		req2 := req1
		req2.Message = testMessage("text2", aipapi.RoleUser)
		st2, err := deps.Store.PutSteering(context.Background(), aLeg, req2)
		require.NoError(t, err)
		assert.Greater(t, st2.Revision, st1.Revision)

		req3 := req2
		req3.Reason = testReason("r2")
		st3, err := deps.Store.PutSteering(context.Background(), aLeg, req3)
		require.NoError(t, err)
		assert.Greater(t, st3.Revision, st2.Revision)

		req4 := req3
		req4.AnchorMissingPolicy = conversationview.AnchorFailClosed
		st4, err := deps.Store.PutSteering(context.Background(), aLeg, req4)
		require.NoError(t, err)
		assert.Greater(t, st4.Revision, st3.Revision)

		snap := snapshotMust(t, deps.Store, aLeg)
		assert.GreaterOrEqual(t, snap.StateRevision, uint64(4))
	})

	t.Run("SteeringDeactivate", func(t *testing.T) {
		t.Parallel()
		deps := env.New(t)
		aLeg := testALegID("deactivate")
		createALegMust(t, deps, aLeg)

		_, err := deps.Store.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
			OverlayID:           "ov-deact",
			Message:             testMessage("to be deactivated", aipapi.RoleUser),
			Placement:           stablePlacement(),
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              testReason("r1"),
		})
		require.NoError(t, err)
		snapBefore := snapshotMust(t, deps.Store, aLeg)
		require.Len(t, snapBefore.Steering, 1)

		st, err := deps.Store.DeactivateSteering(context.Background(), aLeg, "ov-deact")
		require.NoError(t, err)
		assert.False(t, st.Active)
		assert.Greater(t, st.Revision, uint64(1))

		snapAfter := snapshotMust(t, deps.Store, aLeg)
		assert.Empty(t, snapAfter.Steering, "deactivated overlay must be absent from snapshot")

		// Record remains queryable via GetOverlay when seam is available.
		if deps.GetOverlay != nil {
			ov, err := deps.GetOverlay(context.Background(), aLeg, "ov-deact")
			require.NoError(t, err)
			assert.False(t, ov.Active)
			assert.Equal(t, st.Revision, ov.Revision)
		}

		revBefore := st.Revision
		snapRevBefore := snapAfter.StateRevision
		st2, err := deps.Store.DeactivateSteering(context.Background(), aLeg, "ov-deact")
		require.NoError(t, err)
		assert.Equal(t, revBefore, st2.Revision)
		snapAfter2 := snapshotMust(t, deps.Store, aLeg)
		assert.Equal(t, snapRevBefore, snapAfter2.StateRevision)

		_, err = deps.Store.DeactivateSteering(context.Background(), aLeg, "nonexistent")
		require.ErrorIs(t, err, conversationview.ErrOverlayNotFound)
	})

	t.Run("SteeringCaps", func(t *testing.T) {
		t.Parallel()
		t.Run("64 overlay count cap", func(t *testing.T) {
			t.Parallel()
			deps := env.New(t)
			aLeg := testALegID("cap-64")
			createALegMust(t, deps, aLeg)
			for i := range conversationview.MaxActiveOverlays {
				_, err := deps.Store.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
					OverlayID:           fmt.Sprintf("ov-%d", i),
					Message:             testMessage(fmt.Sprintf("text-%d", i), aipapi.RoleUser),
					Placement:           stablePlacement(),
					AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
					Reason:              testReason("r"),
				})
				require.NoError(t, err)
			}
			snap := snapshotMust(t, deps.Store, aLeg)
			assert.Len(t, snap.Steering, conversationview.MaxActiveOverlays)
			revBefore := snap.StateRevision
			_, err := deps.Store.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
				OverlayID:           "ov-overflow",
				Message:             testMessage("overflow", aipapi.RoleUser),
				Placement:           stablePlacement(),
				AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
				Reason:              testReason("r"),
			})
			require.ErrorIs(t, err, conversationview.ErrSteeringLimitExceeded)
			snap2 := snapshotMust(t, deps.Store, aLeg)
			assert.Equal(t, revBefore, snap2.StateRevision)
			assert.Len(t, snap2.Steering, conversationview.MaxActiveOverlays)

			_, err = deps.Store.DeactivateSteering(context.Background(), aLeg, "ov-0")
			require.NoError(t, err)
			_, err = deps.Store.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
				OverlayID:           "ov-overflow",
				Message:             testMessage("overflow", aipapi.RoleUser),
				Placement:           stablePlacement(),
				AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
				Reason:              testReason("r"),
			})
			require.NoError(t, err)
		})
		t.Run("64KiB per message limit", func(t *testing.T) {
			t.Parallel()
			deps := env.New(t)
			aLeg := testALegID("cap-64k")
			createALegMust(t, deps, aLeg)
			bigText := strings.Repeat("a", conversationview.MaxSteeringTextBytes+1)
			_, err := deps.Store.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
				OverlayID:           "ov-big",
				Message:             testMessage(bigText, aipapi.RoleUser),
				Placement:           stablePlacement(),
				AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
				Reason:              testReason("r"),
			})
			require.Error(t, err)
			snap := snapshotMust(t, deps.Store, aLeg)
			assert.Empty(t, snap.Steering)
			assert.Equal(t, uint64(0), snap.StateRevision)
		})
		t.Run("256KiB total active payload limit", func(t *testing.T) {
			t.Parallel()
			deps := env.New(t)
			aLeg := testALegID("cap-256k")
			createALegMust(t, deps, aLeg)
			text64k := strings.Repeat("b", conversationview.MaxSteeringTextBytes)
			for i := range 4 {
				_, err := deps.Store.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
					OverlayID:           fmt.Sprintf("ov%d", i),
					Message:             testMessage(text64k, aipapi.RoleUser),
					Placement:           stablePlacement(),
					AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
					Reason:              testReason("r"),
				})
				require.NoError(t, err)
			}
			snap := snapshotMust(t, deps.Store, aLeg)
			assert.Len(t, snap.Steering, 4)
			revBefore := snap.StateRevision
			_, err := deps.Store.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
				OverlayID:           "ov-overflow-bytes",
				Message:             testMessage("x", aipapi.RoleUser),
				Placement:           stablePlacement(),
				AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
				Reason:              testReason("r"),
			})
			require.ErrorIs(t, err, conversationview.ErrSteeringLimitExceeded)
			snap2 := snapshotMust(t, deps.Store, aLeg)
			assert.Equal(t, revBefore, snap2.StateRevision)
		})
		t.Run("256KiB aggregate replace overflow is atomic", func(t *testing.T) {
			t.Parallel()
			deps := env.New(t)
			aLeg := testALegID("cap-256k-replace")
			createALegMust(t, deps, aLeg)
			// Fill with many small overlays to leave count headroom but approach byte cap.
			// Use 5 overlays of 50KiB = 256000 bytes, just under 256KiB, so a replace with 64KiB pushes over.
			text50k := strings.Repeat("c", 50*1024)
			for i := range 5 {
				_, err := deps.Store.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
					OverlayID:           fmt.Sprintf("ovr-%d", i),
					Message:             testMessage(text50k, aipapi.RoleUser),
					Placement:           stablePlacement(),
					AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
					Reason:              testReason("r"),
				})
				require.NoError(t, err)
			}
			snap := snapshotMust(t, deps.Store, aLeg)
			require.Len(t, snap.Steering, 5)
			revBefore := snap.StateRevision
			// Collect total bytes to assert near limit
			total := 0
			for _, ov := range snap.Steering {
				total += len(ov.Message.Text)
			}
			require.Less(t, total, conversationview.MaxTotalSteeringBytes)
			// Replace one 50KiB overlay with a 64KiB payload -> would exceed aggregate.
			big := strings.Repeat("d", conversationview.MaxSteeringTextBytes)
			_, err := deps.Store.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
				OverlayID:           "ovr-0",
				Message:             testMessage(big, aipapi.RoleUser),
				Placement:           stablePlacement(),
				AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
				Reason:              testReason("r"),
			})
			require.ErrorIs(t, err, conversationview.ErrSteeringLimitExceeded)
			snap2 := snapshotMust(t, deps.Store, aLeg)
			assert.Equal(t, revBefore, snap2.StateRevision, "aggregate overflow via replace must be atomic")
			assert.Len(t, snap2.Steering, 5)
			// Verify original content unchanged
			for _, ov := range snap2.Steering {
				if ov.OverlayID == "ovr-0" {
					assert.Equal(t, text50k, ov.Message.Text)
					assert.Equal(t, uint64(1), ov.Revision)
				}
			}
		})
	})

	t.Run("ALegDeleteRecreate", func(t *testing.T) {
		t.Parallel()
		deps := env.New(t)
		aLeg := testALegID("delete-recreate")
		createALegMust(t, deps, aLeg)

		_, err := deps.Store.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{{Identity: testIdentity(1), Reason: testReason("r")}})
		require.NoError(t, err)
		_, err = deps.Store.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
			OverlayID:           "ov1",
			Message:             testMessage("steering", aipapi.RoleUser),
			Placement:           stablePlacement(),
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              testReason("r"),
		})
		require.NoError(t, err)
		snapBefore := snapshotMust(t, deps.Store, aLeg)
		require.NotEmpty(t, snapBefore.NeverBackend)
		require.NotEmpty(t, snapBefore.Steering)

		require.NoError(t, deps.DeleteALeg(context.Background(), aLeg))
		_, err = deps.Store.Snapshot(context.Background(), aLeg)
		require.ErrorIs(t, err, conversationview.ErrALegNotFound)
		_, err = deps.Store.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{{Identity: testIdentity(2), Reason: testReason("r")}})
		require.ErrorIs(t, err, conversationview.ErrALegNotFound)
		_, err = deps.Store.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
			OverlayID:           "ov2",
			Message:             testMessage("x", aipapi.RoleUser),
			Placement:           stablePlacement(),
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              testReason("r"),
		})
		require.ErrorIs(t, err, conversationview.ErrALegNotFound)

		createALegMust(t, deps, aLeg)
		snapAfter := snapshotMust(t, deps.Store, aLeg)
		assert.Empty(t, snapAfter.NeverBackend)
		assert.Empty(t, snapAfter.Steering)
		assert.Equal(t, uint64(0), snapAfter.StateRevision)
		st, err := deps.Store.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
			OverlayID:           "ov1",
			Message:             testMessage("new", aipapi.RoleUser),
			Placement:           stablePlacement(),
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              testReason("r"),
		})
		require.NoError(t, err)
		assert.Equal(t, uint64(1), st.SlotOrdinal, "slot should reset after delete/recreate")
	})

	t.Run("Linearization", func(t *testing.T) {
		t.Parallel()
		deps := env.New(t)
		aLeg := testALegID("linearization")
		createALegMust(t, deps, aLeg)

		_, err := deps.Store.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
			OverlayID:           "ov-linear",
			Message:             testMessage("linear-text", aipapi.RoleUser),
			Placement:           stablePlacement(),
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              testReason("r"),
		})
		require.NoError(t, err)
		snap1 := snapshotMust(t, deps.Store, aLeg)
		require.Len(t, snap1.Steering, 1)
		assert.Equal(t, "linear-text", snap1.Steering[0].Message.Text)

		snapBefore := snapshotMust(t, deps.Store, aLeg)
		_, err = deps.Store.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{{Identity: testIdentity(999), Reason: testReason("r")}})
		require.NoError(t, err)
		assert.Len(t, snapBefore.NeverBackend, 0)
		snapAfter := snapshotMust(t, deps.Store, aLeg)
		require.Len(t, snapAfter.NeverBackend, 1)
		assert.Equal(t, testIdentity(999), snapAfter.NeverBackend[0].Identity)

		snapBefore2 := snapshotMust(t, deps.Store, aLeg)
		_, err = deps.Store.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
			OverlayID:           "ov-linear2",
			Message:             testMessage("second-linear", aipapi.RoleUser),
			Placement:           stablePlacement(),
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              testReason("r"),
		})
		require.NoError(t, err)
		assert.Len(t, snapBefore2.Steering, 1, "snapshot before must stay on old count")
		snapAfter2 := snapshotMust(t, deps.Store, aLeg)
		assert.Len(t, snapAfter2.Steering, 2)
	})

	t.Run("StateRevisionMonotonic", func(t *testing.T) {
		t.Parallel()
		deps := env.New(t)
		aLeg := testALegID("revision")
		createALegMust(t, deps, aLeg)

		snap0 := snapshotMust(t, deps.Store, aLeg)
		assert.Equal(t, uint64(0), snap0.StateRevision)

		res, err := deps.Store.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{{Identity: testIdentity(1), Reason: testReason("r")}})
		require.NoError(t, err)
		assert.Equal(t, uint64(1), res.StateRevision)
		snap1 := snapshotMust(t, deps.Store, aLeg)
		assert.Equal(t, uint64(1), snap1.StateRevision)

		snapRead := snapshotMust(t, deps.Store, aLeg)
		assert.Equal(t, snap1.StateRevision, snapRead.StateRevision)

		req := conversationview.PutSteeringRequest{
			OverlayID:           "ov-rev",
			Message:             testMessage("text", aipapi.RoleUser),
			Placement:           stablePlacement(),
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              testReason("r"),
		}
		st1, err := deps.Store.PutSteering(context.Background(), aLeg, req)
		require.NoError(t, err)
		snap2 := snapshotMust(t, deps.Store, aLeg)
		assert.Equal(t, uint64(2), snap2.StateRevision)
		assert.Equal(t, snap2.StateRevision, st1.StateRevision)
		st2, err := deps.Store.PutSteering(context.Background(), aLeg, req)
		require.NoError(t, err)
		assert.Equal(t, st1.Revision, st2.Revision)
		snap3 := snapshotMust(t, deps.Store, aLeg)
		assert.Equal(t, snap2.StateRevision, snap3.StateRevision, "no-op Put must not bump StateRevision")

		req.Message = testMessage("text2", aipapi.RoleUser)
		st3, err := deps.Store.PutSteering(context.Background(), aLeg, req)
		require.NoError(t, err)
		assert.Greater(t, st3.Revision, st2.Revision)
		snap4 := snapshotMust(t, deps.Store, aLeg)
		assert.Greater(t, snap4.StateRevision, snap3.StateRevision)

		stDeact, err := deps.Store.DeactivateSteering(context.Background(), aLeg, "ov-rev")
		require.NoError(t, err)
		assert.Greater(t, stDeact.Revision, st3.Revision)
		snap5 := snapshotMust(t, deps.Store, aLeg)
		assert.Greater(t, snap5.StateRevision, snap4.StateRevision)

		stDeact2, err := deps.Store.DeactivateSteering(context.Background(), aLeg, "ov-rev")
		require.NoError(t, err)
		assert.Equal(t, stDeact.Revision, stDeact2.Revision)
		snap6 := snapshotMust(t, deps.Store, aLeg)
		assert.Equal(t, snap5.StateRevision, snap6.StateRevision)
	})

	t.Run("ConcurrentSmoke", func(t *testing.T) {
		t.Parallel()
		deps := env.New(t)
		aLeg := testALegID("concurrent")
		createALegMust(t, deps, aLeg)

		var wg sync.WaitGroup
		errs := make(chan error, 20)
		for n := range 10 {
			wg.Add(1)
			env.Spawn(func() {
				defer wg.Done()
				_, err := deps.Store.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{{Identity: testIdentity(1000 + n), Reason: testReason("r")}})
				if err != nil {
					errs <- err
				}
			})
		}
		for range 10 {
			wg.Add(1)
			env.Spawn(func() {
				defer wg.Done()
				_, err := deps.Store.Snapshot(context.Background(), aLeg)
				if err != nil {
					errs <- err
				}
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			require.NoError(t, err)
		}
		snap := snapshotMust(t, deps.Store, aLeg)
		require.Len(t, snap.NeverBackend, 10, "concurrent tag/snapshot must converge to exact count")
	})

	t.Run("InvalidInputsAtomic", func(t *testing.T) {
		t.Parallel()
		deps := env.New(t)
		aLeg := testALegID("invalid-atomic")
		createALegMust(t, deps, aLeg)

		_, err := deps.Store.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
			OverlayID:           "",
			Message:             testMessage("text", aipapi.RoleUser),
			Placement:           stablePlacement(),
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              testReason("r"),
		})
		require.Error(t, err)
		snap := snapshotMust(t, deps.Store, aLeg)
		assert.Empty(t, snap.Steering)
		assert.Equal(t, uint64(0), snap.StateRevision)

		_, err = deps.Store.PutSteering(context.Background(), aLeg, conversationview.PutSteeringRequest{
			OverlayID:           "ov-invalid-placement",
			Message:             testMessage("text", aipapi.RoleUser),
			Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: nil},
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              testReason("r"),
		})
		require.Error(t, err)
		snap2 := snapshotMust(t, deps.Store, aLeg)
		assert.Equal(t, snap.StateRevision, snap2.StateRevision)

		_, err = deps.Store.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{{Identity: "bad", Reason: testReason("r")}})
		require.Error(t, err)
		snap3 := snapshotMust(t, deps.Store, aLeg)
		assert.Equal(t, snap2.StateRevision, snap3.StateRevision)
	})

	t.Run("SnapshotIsolationAcrossALegs", func(t *testing.T) {
		t.Parallel()
		deps := env.New(t)
		aLeg1 := testALegID("isolation-1")
		aLeg2 := testALegID("isolation-2")
		createALegMust(t, deps, aLeg1)
		createALegMust(t, deps, aLeg2)

		_, err := deps.Store.TagNeverBackend(context.Background(), aLeg1, []conversationview.TagRequest{{Identity: testIdentity(1), Reason: testReason("r")}})
		require.NoError(t, err)
		_, err = deps.Store.PutSteering(context.Background(), aLeg1, conversationview.PutSteeringRequest{
			OverlayID:           "ov1",
			Message:             testMessage("leg1-steering", aipapi.RoleUser),
			Placement:           stablePlacement(),
			AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
			Reason:              testReason("r"),
		})
		require.NoError(t, err)

		snap1 := snapshotMust(t, deps.Store, aLeg1)
		snap2 := snapshotMust(t, deps.Store, aLeg2)
		require.Len(t, snap1.NeverBackend, 1)
		require.Len(t, snap1.Steering, 1)
		assert.Empty(t, snap2.NeverBackend)
		assert.Empty(t, snap2.Steering)
		assert.Equal(t, uint64(0), snap2.StateRevision)
	})

	t.Run("AnchorExcludedRegistration", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()

		t.Run("tag before put rejects atomically", func(t *testing.T) {
			t.Parallel()
			deps := env.New(t)
			aLeg := testALegID("anchor-excluded-create")
			createALegMust(t, deps, aLeg)
			u := testIdentity(77)

			// Step 2 of the TOCTOU sequence: exclusion commits before registration.
			_, err := deps.Store.TagNeverBackend(ctx, aLeg, []conversationview.TagRequest{{Identity: u, Reason: testReason("late_tag")}})
			require.NoError(t, err)
			before := snapshotMust(t, deps.Store, aLeg)

			// Step 3: persisting an after_message anchor to the excluded identity must fail.
			_, err = deps.Store.PutSteering(ctx, aLeg, conversationview.PutSteeringRequest{
				OverlayID:           "ov-excluded",
				Message:             testMessage("anchored steering", aipapi.RoleSystem),
				Placement:           afterPlacement(u, 1),
				AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
				Reason:              testReason("r"),
			})
			require.ErrorIs(t, err, conversationview.ErrSteeringAnchorExcluded)

			after := snapshotMust(t, deps.Store, aLeg)
			assert.Equal(t, before.StateRevision, after.StateRevision, "rejected Put must not bump StateRevision")
			assert.Empty(t, after.Steering, "rejected Put must not create an overlay")
			_, getErr := deps.GetOverlay(ctx, aLeg, "ov-excluded")
			require.ErrorIs(t, getErr, conversationview.ErrOverlayNotFound)
		})

		t.Run("unrelated exclusion does not block anchor registration", func(t *testing.T) {
			t.Parallel()
			deps := env.New(t)
			aLeg := testALegID("anchor-unrelated-tag")
			createALegMust(t, deps, aLeg)
			u := testIdentity(78)
			other := testIdentity(79)

			_, err := deps.Store.TagNeverBackend(ctx, aLeg, []conversationview.TagRequest{{Identity: other, Reason: testReason("r")}})
			require.NoError(t, err)
			st, err := deps.Store.PutSteering(ctx, aLeg, conversationview.PutSteeringRequest{
				OverlayID:           "ov-ok",
				Message:             testMessage("ok steering", aipapi.RoleUser),
				Placement:           afterPlacement(u, 1),
				AnchorMissingPolicy: conversationview.AnchorFailClosed,
				Reason:              testReason("r"),
			})
			require.NoError(t, err)
			assert.True(t, st.Active)
		})

		t.Run("put then tag is legitimate later anchor loss", func(t *testing.T) {
			t.Parallel()
			deps := env.New(t)
			aLeg := testALegID("anchor-later-loss")
			createALegMust(t, deps, aLeg)
			v := testIdentity(80)

			_, err := deps.Store.PutSteering(ctx, aLeg, conversationview.PutSteeringRequest{
				OverlayID:           "ov-loss",
				Message:             testMessage("loss steering", aipapi.RoleUser),
				Placement:           afterPlacement(v, 1),
				AnchorMissingPolicy: conversationview.AnchorFailClosed,
				Reason:              testReason("r"),
			})
			require.NoError(t, err)
			// Exclusion arriving after registration must succeed (no cascade).
			_, err = deps.Store.TagNeverBackend(ctx, aLeg, []conversationview.TagRequest{{Identity: v, Reason: testReason("r")}})
			require.NoError(t, err)
			overlay, err := deps.GetOverlay(ctx, aLeg, "ov-loss")
			require.NoError(t, err)
			assert.True(t, overlay.Active, "later tag must not deactivate a registered overlay")
		})

		t.Run("placement change into excluded anchor rejects; same-anchor replace after tag succeeds", func(t *testing.T) {
			t.Parallel()
			deps := env.New(t)
			aLeg := testALegID("anchor-replace-cases")
			createALegMust(t, deps, aLeg)
			w := testIdentity(81)

			_, err := deps.Store.PutSteering(ctx, aLeg, conversationview.PutSteeringRequest{
				OverlayID:           "ov-move",
				Message:             testMessage("stable first", aipapi.RoleUser),
				Placement:           stablePlacement(),
				AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
				Reason:              testReason("r"),
			})
			require.NoError(t, err)
			_, err = deps.Store.TagNeverBackend(ctx, aLeg, []conversationview.TagRequest{{Identity: w, Reason: testReason("r")}})
			require.NoError(t, err)

			// Moving the overlay onto an now-excluded anchor must reject.
			_, err = deps.Store.PutSteering(ctx, aLeg, conversationview.PutSteeringRequest{
				OverlayID:           "ov-move",
				Message:             testMessage("stable first", aipapi.RoleUser),
				Placement:           afterPlacement(w, 1),
				AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
				Reason:              testReason("r"),
			})
			require.ErrorIs(t, err, conversationview.ErrSteeringAnchorExcluded)

			// Content replacement keeping the unchanged (pre-tag) placement stays exempt.
			_, err = deps.Store.PutSteering(ctx, aLeg, conversationview.PutSteeringRequest{
				OverlayID:           "ov-move",
				Message:             testMessage("stable second", aipapi.RoleUser),
				Placement:           stablePlacement(),
				AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
				Reason:              testReason("r"),
			})
			require.NoError(t, err)
			overlay, err := deps.GetOverlay(ctx, aLeg, "ov-move")
			require.NoError(t, err)
			assert.Equal(t, "stable second", overlay.Message.Text)
		})

		t.Run("same after_message anchor replacement after tag succeeds (exempt)", func(t *testing.T) {
			t.Parallel()
			deps := env.New(t)
			aLeg := testALegID("anchor-same-after-exempt")
			createALegMust(t, deps, aLeg)
			v := testIdentity(82)

			_, err := deps.Store.PutSteering(ctx, aLeg, conversationview.PutSteeringRequest{
				OverlayID:           "ov-same-after",
				Message:             testMessage("first anchored", aipapi.RoleUser),
				Placement:           afterPlacement(v, 1),
				AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
				Reason:              testReason("r"),
			})
			require.NoError(t, err)
			beforeSnap := snapshotMust(t, deps.Store, aLeg)
			require.Len(t, beforeSnap.Steering, 1)
			beforeRev := beforeSnap.Steering[0].Revision
			beforeSlot := beforeSnap.Steering[0].SlotOrdinal

			_, err = deps.Store.TagNeverBackend(ctx, aLeg, []conversationview.TagRequest{{Identity: v, Reason: testReason("r")}})
			require.NoError(t, err)

			// Existing after_message(V) -> tag V -> content replacement retaining after_message(V) is exempt.
			st, err := deps.Store.PutSteering(ctx, aLeg, conversationview.PutSteeringRequest{
				OverlayID:           "ov-same-after",
				Message:             testMessage("second anchored", aipapi.RoleUser),
				Placement:           afterPlacement(v, 1),
				AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
				Reason:              testReason("r"),
			})
			require.NoError(t, err)
			assert.Equal(t, beforeRev+1, st.Revision)
			assert.Equal(t, beforeSlot, st.SlotOrdinal, "same-anchor replacement must retain SlotOrdinal")
			overlay, err := deps.GetOverlay(ctx, aLeg, "ov-same-after")
			require.NoError(t, err)
			assert.Equal(t, "second anchored", overlay.Message.Text)
			assert.Equal(t, v, overlay.Placement.Anchor.Identity)
		})
	})
}
