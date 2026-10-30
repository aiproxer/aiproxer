package sdkadapter_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview/sdkadapter"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/nonforwardable"
)

func mustIdentity(n int) conversationview.MessageIdentity {
	return conversationview.MessageIdentity("v1:" + formatHex64(n))
}

func formatHex64(n int) string {
	const digits = "0123456789abcdef"
	b := make([]byte, 64)
	for i := 63; i >= 0; i-- {
		b[i] = digits[n&0xf]
		n >>= 4
	}
	return string(b)
}

func TestRegistrar_Construction(t *testing.T) {
	t.Parallel()
	_, err := sdkadapter.NewRegistrar(nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "tagger is required")

	store := conversationview.NewReferenceStore()
	reg, err := sdkadapter.NewRegistrar(store)
	require.NoError(t, err)
	require.NotNil(t, reg)
	var _ nonforwardable.Registrar = reg
}

func TestRegistrar_TagMessages_Validation(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	reg, err := sdkadapter.NewRegistrar(store)
	require.NoError(t, err)
	ctx := context.Background()
	aLeg := "a_test_001"
	require.NoError(t, store.CreateALeg(ctx, aLeg))

	tests := []struct {
		name    string
		aLeg    nonforwardable.ALegRef
		msgs    []nonforwardable.MessageRef
		reason  nonforwardable.ReasonCode
		wantErr bool
	}{
		{
			name:    "empty a-leg id",
			aLeg:    nonforwardable.ALegRef{ID: ""},
			msgs:    []nonforwardable.MessageRef{{Identity: string(mustIdentity(1))}},
			reason:  "test_reason",
			wantErr: true,
		},
		{
			name:    "whitespace a-leg",
			aLeg:    nonforwardable.ALegRef{ID: "   "},
			msgs:    []nonforwardable.MessageRef{{Identity: string(mustIdentity(1))}},
			reason:  "test_reason",
			wantErr: true,
		},
		{
			name:    "invalid reason empty",
			aLeg:    nonforwardable.ALegRef{ID: aLeg},
			msgs:    []nonforwardable.MessageRef{{Identity: string(mustIdentity(1))}},
			reason:  "",
			wantErr: true,
		},
		{
			name:    "invalid reason chars",
			aLeg:    nonforwardable.ALegRef{ID: aLeg},
			msgs:    []nonforwardable.MessageRef{{Identity: string(mustIdentity(1))}},
			reason:  "bad/reason",
			wantErr: true,
		},
		{
			name:    "invalid message ref empty",
			aLeg:    nonforwardable.ALegRef{ID: aLeg},
			msgs:    []nonforwardable.MessageRef{{Identity: ""}},
			reason:  "test_reason",
			wantErr: true,
		},
		{
			name:    "invalid message ref oversized",
			aLeg:    nonforwardable.ALegRef{ID: aLeg},
			msgs:    []nonforwardable.MessageRef{{Identity: string(make([]byte, 513))}},
			reason:  "test_reason",
			wantErr: true,
		},
		{
			name:    "valid single",
			aLeg:    nonforwardable.ALegRef{ID: aLeg},
			msgs:    []nonforwardable.MessageRef{{Identity: string(mustIdentity(2))}},
			reason:  "test_reason",
			wantErr: false,
		},
		{
			name:    "valid multiple",
			aLeg:    nonforwardable.ALegRef{ID: aLeg},
			msgs:    []nonforwardable.MessageRef{{Identity: string(mustIdentity(3))}, {Identity: string(mustIdentity(4))}},
			reason:  "test_reason",
			wantErr: false,
		},
		{
			name:    "empty batch valid (no-op)",
			aLeg:    nonforwardable.ALegRef{ID: aLeg},
			msgs:    nil,
			reason:  "test_reason",
			wantErr: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := reg.TagMessages(ctx, tc.aLeg, tc.msgs, tc.reason)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRegistrar_TagMessages_SuccessAndBatchAtomicity(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	reg, err := sdkadapter.NewRegistrar(store)
	require.NoError(t, err)
	ctx := context.Background()
	aLeg := "a_reg_batch"
	require.NoError(t, store.CreateALeg(ctx, aLeg))

	// Valid batch commits
	refs := []nonforwardable.MessageRef{
		{Identity: string(mustIdentity(10))},
		{Identity: string(mustIdentity(11))},
	}
	require.NoError(t, reg.TagMessages(ctx, nonforwardable.ALegRef{ID: aLeg}, refs, "r1"))
	snap, err := store.Snapshot(ctx, aLeg)
	require.NoError(t, err)
	require.Len(t, snap.NeverBackend, 2)

	// Idempotent re-tag same identities with different reason must succeed and not bump revision
	revBefore := snap.StateRevision
	require.NoError(t, reg.TagMessages(ctx, nonforwardable.ALegRef{ID: aLeg}, refs, "r2"))
	snap2, err := store.Snapshot(ctx, aLeg)
	require.NoError(t, err)
	assert.Equal(t, revBefore, snap2.StateRevision, "idempotent re-tag must not bump revision")
	require.Len(t, snap2.NeverBackend, 2)

	// Batch atomicity: one invalid identity in batch must fail atomically and not mutate store
	// Use an identity that fails domain validation (not v1 hex) – sdkadapter passes to store which validates TagRequest
	invalidRefs := []nonforwardable.MessageRef{
		{Identity: string(mustIdentity(12))},
		{Identity: "invalid-identity-not-v1"},
	}
	snapBefore, _ := store.Snapshot(ctx, aLeg)
	err = reg.TagMessages(ctx, nonforwardable.ALegRef{ID: aLeg}, invalidRefs, "r1")
	require.Error(t, err)
	// Ensure typed error is wrapped
	assert.ErrorIs(t, err, conversationview.ErrInvalidTagRequest)
	snapAfter, _ := store.Snapshot(ctx, aLeg)
	assert.Equal(t, snapBefore.StateRevision, snapAfter.StateRevision, "atomic failure must not bump revision")
	assert.Len(t, snapAfter.NeverBackend, len(snapBefore.NeverBackend))
}

func TestRegistrar_TagMessages_ErrorMapping(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	reg, err := sdkadapter.NewRegistrar(store)
	require.NoError(t, err)
	ctx := context.Background()
	aLeg := "a_reg_errmap"
	require.NoError(t, store.CreateALeg(ctx, aLeg))
	// Fill to cap
	batch := make([]nonforwardable.MessageRef, conversationview.MaxNeverBackendTags)
	for i := range conversationview.MaxNeverBackendTags {
		batch[i] = nonforwardable.MessageRef{Identity: string(mustIdentity(1000 + i))}
	}
	require.NoError(t, reg.TagMessages(ctx, nonforwardable.ALegRef{ID: aLeg}, batch, "r"))

	// One more should exceed cap and return typed sentinel via %w
	err = reg.TagMessages(ctx, nonforwardable.ALegRef{ID: aLeg}, []nonforwardable.MessageRef{{Identity: string(mustIdentity(99999))}}, "r")
	require.Error(t, err)
	assert.ErrorIs(t, err, conversationview.ErrTagLimitExceeded)
	assert.Contains(t, err.Error(), "tag")

	// Unknown A-leg should map ErrALegNotFound
	err = reg.TagMessages(ctx, nonforwardable.ALegRef{ID: "a_unknown_xxx"}, []nonforwardable.MessageRef{{Identity: string(mustIdentity(1))}}, "r")
	require.Error(t, err)
	assert.ErrorIs(t, err, conversationview.ErrALegNotFound)
}

func TestRegistrar_TagMessages_ContextCancellation(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	reg, err := sdkadapter.NewRegistrar(store)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	aLeg := "a_any"
	_ = store.CreateALeg(context.Background(), aLeg)
	err = reg.TagMessages(ctx, nonforwardable.ALegRef{ID: aLeg}, []nonforwardable.MessageRef{{Identity: string(mustIdentity(1))}}, "r")
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
}

func TestRegistrar_NoGlobalLocator(t *testing.T) {
	t.Parallel()
	// Ensure no global variable or registry exposes registrar: just compile-time check that package does not export a global.
	// This test pins that NewRegistrar requires explicit tagger injection.
	store := conversationview.NewReferenceStore()
	reg1, err := sdkadapter.NewRegistrar(store)
	require.NoError(t, err)
	reg2, err := sdkadapter.NewRegistrar(store)
	require.NoError(t, err)
	assert.NotSame(t, reg1, reg2, "each construction must produce distinct instance, no global singleton")
}
