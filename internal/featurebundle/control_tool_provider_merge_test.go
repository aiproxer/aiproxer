package featurebundle_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

type mergeControlToolProvider struct {
	id      string
	spec    controltool.Spec
	handles int
}

func (p *mergeControlToolProvider) ID() string { return p.id }

func (p *mergeControlToolProvider) Spec() controltool.Spec { return p.spec }

func (p *mergeControlToolProvider) Handle(context.Context, controltool.CompletedCall, controltool.Meta) (controltool.Outcome, error) {
	p.handles++
	return controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "done", ReasonCode: "complete"}, nil
}

func validMergeControlToolSpec() controltool.Spec {
	return controltool.Spec{
		Tool: aipapi.ToolDef{
			Name:        "proxy_control",
			Description: "Call this only when the assigned proxy-local control action is complete.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"note": {
						"type": "string",
						"description": "Bounded control result."
					}
				},
				"required": ["note"],
				"additionalProperties": false
			}`),
		},
		Instruction: controltool.Instruction{
			Role: aipapi.RoleSystem,
			Text: "Call proxy_control only when the assigned work is complete.",
		},
		MaxArgsBytes: controltool.DefaultMaxArgsBytes,
	}
}

func controlToolBundle(provider controltool.Provider) aipfeature.FeatureBundle {
	return makeBundle(func(cs *aipfeature.ContributionSet) {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneControlToolProvider, "control-tool", provider)
	})
}

// TestPlaneParity_GeneratedSurfaceControlToolProviderExclusiveSlot proves the
// generated merge path enforces the exclusive control-tool slot: one provider
// composes, a second provider (even with an identical identity) is rejected
// deterministically, and the candidate is discarded rather than mutated.
func TestPlaneParity_GeneratedSurfaceControlToolProviderExclusiveSlot(t *testing.T) {
	t.Parallel()

	t.Run("single_provider", func(t *testing.T) {
		t.Parallel()

		provider := &mergeControlToolProvider{id: "proxy.control.example", spec: validMergeControlToolSpec()}
		gen, err := featurebundle.MergeBundlesGenerated(controlToolBundle(controltool.Provider(provider)))
		require.NoError(t, err)

		got := aipfeature.Get(gen.Frozen, aipfeature.PlaneControlToolProvider)
		require.NotNil(t, got, "occupied control_tool_provider must read back")
		assert.Equal(t, "proxy.control.example", got.ID())
		id, ok := aipfeature.FrozenIdentity(gen.Frozen, aipfeature.PlaneControlToolProvider)
		assert.True(t, ok)
		assert.Equal(t, "proxy.control.example", id)
		assert.Zero(t, provider.handles, "merge must not invoke the provider")
	})

	t.Run("distinct_providers_conflict", func(t *testing.T) {
		t.Parallel()

		first := &mergeControlToolProvider{id: "proxy.control.a", spec: validMergeControlToolSpec()}
		second := &mergeControlToolProvider{id: "proxy.control.b", spec: validMergeControlToolSpec()}
		gen, err := featurebundle.MergeBundlesGenerated(
			controlToolBundle(controltool.Provider(first)),
			controlToolBundle(controltool.Provider(second)),
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, aipfeature.ErrExclusiveConflict)
		assert.ErrorIs(t, err, aipfeature.ErrControlToolProviderConflict)
		assert.Equal(t, featurebundle.GeneratedMergeSurface{}, gen, "candidate must be discarded on conflict")
		assert.Zero(t, second.handles)
	})

	t.Run("same_identity_recontribution_conflict", func(t *testing.T) {
		t.Parallel()

		spec := validMergeControlToolSpec()
		first := &mergeControlToolProvider{id: "proxy.control.same", spec: spec}
		second := &mergeControlToolProvider{id: "proxy.control.same", spec: spec}
		gen, err := featurebundle.MergeBundlesGenerated(
			controlToolBundle(controltool.Provider(first)),
			controlToolBundle(controltool.Provider(second)),
		)
		require.Error(t, err, "an identical second provider must still be rejected deterministically")
		assert.ErrorIs(t, err, aipfeature.ErrExclusiveConflict)
		assert.Equal(t, featurebundle.GeneratedMergeSurface{}, gen)
	})

	t.Run("invalid_provider_rejected_before_publication", func(t *testing.T) {
		t.Parallel()

		var typedNil *mergeControlToolProvider
		for name, provider := range map[string]controltool.Provider{
			"typed_nil": controltool.Provider(typedNil),
			"nil":       controltool.Provider(nil),
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				cs := aipfeature.NewContributionSet()
				err := aipfeature.Contribute(cs, aipfeature.PlaneControlToolProvider, "control-tool", provider)
				require.Error(t, err, "invalid provider must be rejected at composition")
				assert.False(t, cs.Has("control_tool_provider"))

				// A rejected contribution cannot reach the merged surface.
				gen, mergeErr := featurebundle.MergeBundlesGenerated(aipfeature.BundleFromPlanes(cs.Freeze(), nil))
				require.NoError(t, mergeErr)
				assert.Nil(t, aipfeature.Get(gen.Frozen, aipfeature.PlaneControlToolProvider))
			})
		}
	})
}

// TestPlaneParity_GeneratedSurfaceControlToolProviderRemoval proves a generation
// that contributes no control-tool provider is a zero-work no-op, and that
// withdrawing the provider in a newly admitted generation leaves previously
// published immutable snapshots intact (requirements 10.5 and 12.3).
func TestPlaneParity_GeneratedSurfaceControlToolProviderRemoval(t *testing.T) {
	t.Parallel()

	provider := &mergeControlToolProvider{id: "proxy.control.example", spec: validMergeControlToolSpec()}
	occupied, err := featurebundle.MergeBundlesGenerated(controlToolBundle(controltool.Provider(provider)))
	require.NoError(t, err)
	require.NotNil(t, aipfeature.Get(occupied.Frozen, aipfeature.PlaneControlToolProvider))

	// A new generation without the feature contributes nothing.
	withdrawn, err := featurebundle.MergeBundlesGenerated()
	require.NoError(t, err)
	assert.Nil(t, aipfeature.Get(withdrawn.Frozen, aipfeature.PlaneControlToolProvider))
	_, ok := aipfeature.FrozenIdentity(withdrawn.Frozen, aipfeature.PlaneControlToolProvider)
	assert.False(t, ok, "withdrawn generation must carry no control-tool identity")

	// Previously published snapshots stay immutable and occupied.
	for name, snapshot := range map[string]aipfeature.FrozenPlaneSet{
		"frozen":         occupied.Frozen,
		"clone":          occupied.Frozen.Clone(),
		"request_frozen": aipfeature.FreezeRequestPlanes(occupied.Frozen),
	} {
		got := aipfeature.Get(snapshot, aipfeature.PlaneControlToolProvider)
		require.NotNil(t, got, "snapshot %s must retain the control-tool provider", name)
		assert.Equal(t, "proxy.control.example", got.ID(), "snapshot %s", name)
	}

	assert.Zero(t, provider.handles, "removal must not invoke the provider")
}
