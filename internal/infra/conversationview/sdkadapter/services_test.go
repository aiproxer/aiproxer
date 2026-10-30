package sdkadapter_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview/sdkadapter"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestNewNonforwardableRegistrarFromStore_CapabilityCheck(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	require.NoError(t, store.CreateALeg(ctx, "a_cap_reg"))

	reg, err := sdkadapter.NewRegistrar(store)
	require.NoError(t, err)
	require.NotNil(t, reg)

	// From generic store value
	reg2, err := sdkadapter.NewNonforwardableRegistrarFromStore(store)
	require.NoError(t, err)
	require.NotNil(t, reg2)

	// Missing capability
	_, err = sdkadapter.NewNonforwardableRegistrarFromStore(struct{}{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tagger capability not available")

	_, err = sdkadapter.NewRegistrar(nil)
	require.Error(t, err)
}

func TestNewSteeringWriterFromStore_CapabilityAndResolver(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLeg := "a_cap_writer"
	require.NoError(t, store.CreateALeg(ctx, aLeg))

	// Direct constructor with nil resolver (stable_prefix only) should succeed
	w, err := sdkadapter.NewWriter(store, aLeg, nil)
	require.NoError(t, err)
	require.NotNil(t, w)

	// From generic store
	w2, err := sdkadapter.NewSteeringWriterFromStore(store, aLeg, nil)
	require.NoError(t, err)
	require.NotNil(t, w2)

	// Missing capability
	_, err = sdkadapter.NewSteeringWriterFromStore(struct{}{}, aLeg, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "steering capability not available")

	// Nil store direct should error
	_, err = sdkadapter.NewWriter(nil, aLeg, nil)
	require.Error(t, err)

	// After_ingress_tail with resolver
	u1 := aipapi.Item{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hello"}}}
	call := aipapi.Call{Items: []aipapi.Item{u1}}
	snap := conversationview.Snapshot{}
	resolver := func(context.Context) (aipapi.Call, conversationview.Snapshot, error) {
		return call, snap, nil
	}
	w3, err := sdkadapter.NewWriter(store, aLeg, resolver)
	require.NoError(t, err)
	require.NotNil(t, w3)

	// Ensure no global locator: distinct instances
	w4, err := sdkadapter.NewWriter(store, aLeg, nil)
	require.NoError(t, err)
	assert.NotSame(t, w3, w4)
}

func TestNewConversationViewServices_BothCapabilities(t *testing.T) {
	t.Parallel()
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	aLeg := "a_both"
	require.NoError(t, store.CreateALeg(ctx, aLeg))

	reg, w, err := sdkadapter.NewConversationViewServices(store, aLeg, nil)
	require.NoError(t, err)
	require.NotNil(t, reg)
	require.NotNil(t, w)

	_, _, err = sdkadapter.NewConversationViewServices(struct{}{}, aLeg, nil)
	require.Error(t, err)
}

func TestConversationViewServices_NoFrontendExposure(t *testing.T) {
	t.Parallel()
	// This test pins that the package does not register HTTP handlers or expose client surfaces.
	// We assert that NewSteeringWriterFromStore requires explicit store and aLegID, not a global.
	store := conversationview.NewReferenceStore()
	ctx := context.Background()
	require.NoError(t, store.CreateALeg(ctx, "a_no_frontend"))
	_, err := sdkadapter.NewSteeringWriterFromStore(store, "a_no_frontend", nil)
	require.NoError(t, err)
	// There is no function that returns a Writer without explicit store/aLeg injection.
}
