package conversationview_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview/sdkadapter"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
)

func TestConversationView_RecoverySteeringLifecycle_ReferenceStore(t *testing.T) {
	t.Parallel()

	refStore := conversationview.NewReferenceStore()
	aLegID := "a_recovery_ref_contract_test_leg_123"
	require.NoError(t, refStore.CreateALeg(context.Background(), aLegID))

	runRecoverySteeringStoreLifecycle(t, refStore, aLegID)
}

func runRecoverySteeringStoreLifecycle(t *testing.T, store conversationview.SteeringStore, aLegID string) {
	t.Helper()
	ctx := context.Background()

	reader, ok := conversationview.AsReader(store)
	require.True(t, ok, "store must implement conversationview.Reader")

	// Accepted user ingress call ending in RoleUser
	userMsg := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("user initial prompt")}}
	userCall := aipapi.Call{
		Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("sys")}}},
		Messages:     []aipapi.Message{userMsg},
	}

	resolver := func(ctx context.Context) (aipapi.Call, conversationview.Snapshot, error) {
		snap, err := reader.Snapshot(ctx, aLegID)
		if err != nil {
			return aipapi.Call{}, conversationview.Snapshot{}, err
		}
		return userCall, snap, nil
	}

	writer, err := sdkadapter.NewWriter(store, aLegID, resolver)
	require.NoError(t, err)

	// 1. First Put: registers "alg-rec" with AfterIngressTail and FailClosed
	st1, err := writer.Put(ctx, steering.PutRequest{
		OverlayID: steering.OverlayID("alg-rec"),
		Message: steering.Message{
			Role: aipapi.RoleDeveloper,
			Text: "<automated-recovery>Attempt 1/3: resume unfinished work</automated-recovery>",
		},
		Placement:           steering.AfterIngressTail,
		AnchorMissingPolicy: steering.FailClosed,
		Reason:              steering.ReasonCode("loop_guard_recovery"),
	})
	require.NoError(t, err)
	assert.Equal(t, steering.OverlayID("alg-rec"), st1.OverlayID)
	assert.Equal(t, uint64(1), st1.Revision)
	assert.Equal(t, uint64(1), st1.SlotOrdinal)
	assert.True(t, st1.Active)

	// Verify in Snapshot
	snap1, err := reader.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	require.Len(t, snap1.Steering, 1)
	assert.Equal(t, "alg-rec", snap1.Steering[0].OverlayID)
	assert.Equal(t, aipapi.RoleDeveloper, snap1.Steering[0].Message.Role)
	assert.Contains(t, snap1.Steering[0].Message.Text, "Attempt 1/3")
	assert.Equal(t, conversationview.PlacementAfterMessage, snap1.Steering[0].Placement.Kind)
	require.NotNil(t, snap1.Steering[0].Placement.Anchor)

	// 2. Second Put with updated instruction (Attempt 2/3): reuses SAME slot ordinal, bumps revision
	st2, err := writer.Put(ctx, steering.PutRequest{
		OverlayID: steering.OverlayID("alg-rec"),
		Message: steering.Message{
			Role: aipapi.RoleDeveloper,
			Text: "<automated-recovery>Attempt 2/3: resume unfinished work</automated-recovery>",
		},
		Placement:           steering.AfterIngressTail,
		AnchorMissingPolicy: steering.FailClosed,
		Reason:              steering.ReasonCode("loop_guard_recovery"),
	})
	require.NoError(t, err)
	assert.Equal(t, st1.SlotOrdinal, st2.SlotOrdinal, "slot ordinal must be retained on update")
	assert.Equal(t, uint64(2), st2.Revision, "revision must bump on text update")

	snap2, err := reader.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	require.Len(t, snap2.Steering, 1, "update must not create duplicate overlay")
	assert.Contains(t, snap2.Steering[0].Message.Text, "Attempt 2/3")

	// 3. Semantic no-op Put (identical content): revision and StateRevision must not bump
	st3, err := writer.Put(ctx, steering.PutRequest{
		OverlayID: steering.OverlayID("alg-rec"),
		Message: steering.Message{
			Role: aipapi.RoleDeveloper,
			Text: "<automated-recovery>Attempt 2/3: resume unfinished work</automated-recovery>",
		},
		Placement:           steering.AfterIngressTail,
		AnchorMissingPolicy: steering.FailClosed,
		Reason:              steering.ReasonCode("loop_guard_recovery"),
	})
	require.NoError(t, err)
	assert.Equal(t, st2.Revision, st3.Revision, "no-op Put must not bump revision")

	// 4. Deactivate overlay: absent from snapshot
	stDeact, err := writer.Deactivate(ctx, steering.OverlayID("alg-rec"))
	require.NoError(t, err)
	assert.False(t, stDeact.Active)
	assert.Greater(t, stDeact.Revision, st2.Revision)

	snap3, err := reader.Snapshot(ctx, aLegID)
	require.NoError(t, err)
	assert.Empty(t, snap3.Steering, "deactivated overlay must be absent from active snapshot")

	// 5. Second Deactivate: idempotent no-op
	stDeact2, err := writer.Deactivate(ctx, steering.OverlayID("alg-rec"))
	require.NoError(t, err)
	assert.Equal(t, stDeact.Revision, stDeact2.Revision, "second deactivate must be idempotent no-op")

	// 6. Deactivating unknown overlay: returns ErrOverlayNotFound
	_, err = writer.Deactivate(ctx, steering.OverlayID("non-existent-overlay"))
	require.ErrorIs(t, err, conversationview.ErrOverlayNotFound)
}
