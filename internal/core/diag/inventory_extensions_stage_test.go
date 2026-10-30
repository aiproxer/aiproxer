package diag

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/localturn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildInventoryExtensions_ConsumesFinishedRowsOnly pins the diagnostics
// contract established by the inventory-featurehost relocation: the extension
// DTO is assembled exclusively from finished compiled rows supplied by the
// feature/composition authority. Diagnostics performs no feature-plane
// reduction of its own, so the supplied occupancy, privileges, bundle error,
// and secret-guard row pass through verbatim.
func TestBuildInventoryExtensions_ConsumesFinishedRowsOnly(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{
				{ID: "feat-mixed", Kind: "mixed", Enabled: true},
				{ID: "sg-main", Kind: "secrets-guard", Enabled: true},
			},
		},
	}
	extras := &InventoryExtras{
		FeatureMetadata: []InventoryFeatureMetadata{
			{
				InstanceID: "feat-mixed",
				StageOccupancy: []InventoryStageOccupancy{
					{
						StageID:    aipfeature.StageIDSubmit,
						HandlerIDs: []string{"sub-a", "sub-z"},
						Count:      2,
					},
					{
						StageID:    extensions.StageToolEventReaction,
						HandlerIDs: []string{"tool_policy:pol-a", "tool_policy:pol-b"},
						Count:      2,
					},
					{
						StageID:    extensions.StageCandidateAttemptTransform,
						HandlerIDs: []string{"attempt_transform:att-a", "attempt_transform:att-z"},
						Count:      2,
					},
					{
						StageID:    extensions.StageFinalStreamObservation,
						HandlerIDs: []string{"stream_observer:so-a", "stream_observer:so-z"},
						Count:      2,
					},
				},
				Privileges: InventoryPrivileges{AuxiliaryRequests: true},
			},
			{
				InstanceID:     "sg-main",
				StageOccupancy: []InventoryStageOccupancy{},
				SecretGuard:    &InventorySecretGuard{InstanceID: "sg-main", CatalogEntryCount: 3, Action: "block"},
			},
		},
	}

	ext := buildInventoryExtensions(t.Context(), cfg, extras)
	require.Len(t, ext.Features, 2)

	f0 := ext.Features[0]
	require.Equal(t, "feat-mixed", f0.InstanceID)
	require.Empty(t, f0.BundleError)
	assert.Equal(t, extras.FeatureMetadata[0].StageOccupancy, f0.StageOccupancy,
		"supplied occupancy must pass through unmodified")
	assert.Equal(t, extras.FeatureMetadata[0].Privileges, f0.Privileges,
		"supplied privileges must pass through unmodified")
	assert.Nil(t, f0.SecretGuard)

	f1 := ext.Features[1]
	require.Equal(t, "sg-main", f1.InstanceID)
	require.NotNil(t, f1.SecretGuard)
	assert.Equal(t, 3, f1.SecretGuard.CatalogEntryCount)
	assert.Equal(t, "block", f1.SecretGuard.Action)

	// Generic ports aggregate only the supplied rows.
	assert.True(t, ext.GenericPorts.AttemptTransformOccupied)
	assert.Equal(t, 2, ext.GenericPorts.AttemptTransformHandlers)
	assert.True(t, ext.GenericPorts.FinalStreamObservationOccupied)
	assert.Equal(t, 2, ext.GenericPorts.FinalStreamObservationHandlers)

	// A configured feature without a supplied row keeps an empty DTO row: the
	// snapshot shape does not depend on any diagnostics-side projection.
	ext2 := buildInventoryExtensions(t.Context(), cfg, &InventoryExtras{})
	require.Len(t, ext2.Features, 2)
	require.Empty(t, ext2.Features[0].StageOccupancy)
	assert.Nil(t, ext2.Features[1].SecretGuard)
}

type diagDummyLTHandler struct {
	id  string
	ord int
}

func (h diagDummyLTHandler) ID() string                      { return h.id }
func (h diagDummyLTHandler) Order() int                      { return h.ord }
func (diagDummyLTHandler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (diagDummyLTHandler) Match(context.Context, aipapi.Call, localturn.Meta) (localturn.MatchResult, error) {
	return localturn.MatchResult{Claimed: false}, nil
}

func (diagDummyLTHandler) Handle(context.Context, localturn.HandleInput) (localturn.Reply, error) {
	return localturn.Reply{}, nil
}

func TestPlaneLocalTurnHandlers_OrderingAndMaterialize(t *testing.T) {
	t.Parallel()

	assert.Equal(t, aipfeature.StageIDPreRequest, aipfeature.PlaneLocalTurnHandlers.Diagnostics.StageID)
	assert.Equal(t, "", aipfeature.PlaneLocalTurnHandlers.Diagnostics.CoalesceGroup)

	var typedNil *diagDummyLTHandler
	handlers := []localturn.Handler{
		diagDummyLTHandler{id: "lt-z", ord: 20},
		nil,
		diagDummyLTHandler{id: "lt-a", ord: 10},
		typedNil,
		diagDummyLTHandler{id: "lt-m", ord: 10},
	}

	require.NotNil(t, aipfeature.PlaneLocalTurnHandlers.Diagnostics.Materialize)
	occupants := aipfeature.PlaneLocalTurnHandlers.Diagnostics.Materialize(handlers)
	require.Len(t, occupants, 3)
	assert.Equal(t, "local_turn:lt-a", occupants[0].Label)
	assert.Equal(t, "local_turn:lt-m", occupants[1].Label)
	assert.Equal(t, "local_turn:lt-z", occupants[2].Label)

	jsonBytes, err := json.Marshal(occupants)
	require.NoError(t, err)
	expectedJSON := `[{"Label":"local_turn:lt-a","PluginID":"","Privileges":null},{"Label":"local_turn:lt-m","PluginID":"","Privileges":null},{"Label":"local_turn:lt-z","PluginID":"","Privileges":null}]`
	assert.Equal(t, expectedJSON, string(jsonBytes))
}
