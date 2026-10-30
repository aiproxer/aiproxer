package feature_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// planeControlToolProviderFake is a generic valid control-tool provider. It is
// deliberately provider-neutral: no ALG, terminal, or completion vocabulary.
type planeControlToolProviderFake struct {
	id      string
	spec    controltool.Spec
	handles int
}

func (p *planeControlToolProviderFake) ID() string { return p.id }

func (p *planeControlToolProviderFake) Spec() controltool.Spec { return p.spec }

func (p *planeControlToolProviderFake) Handle(context.Context, controltool.CompletedCall, controltool.Meta) (controltool.Outcome, error) {
	p.handles++
	return controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "done", ReasonCode: "complete"}, nil
}

type planeControlToolProviderPanicSpec struct{ id string }

func (p planeControlToolProviderPanicSpec) ID() string { return p.id }
func (planeControlToolProviderPanicSpec) Spec() controltool.Spec {
	panic("spec unavailable")
}

func (planeControlToolProviderPanicSpec) Handle(context.Context, controltool.CompletedCall, controltool.Meta) (controltool.Outcome, error) {
	return controltool.Outcome{}, nil
}

type planeControlToolProviderPanicID struct{}

func (planeControlToolProviderPanicID) ID() string { panic("id unavailable") }
func (planeControlToolProviderPanicID) Spec() controltool.Spec {
	return planeControlToolProviderValidSpec()
}

func (planeControlToolProviderPanicID) Handle(context.Context, controltool.CompletedCall, controltool.Meta) (controltool.Outcome, error) {
	return controltool.Outcome{}, nil
}

func planeControlToolProviderValidSpec() controltool.Spec {
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

func newPlaneControlToolProviderFake(id string) *planeControlToolProviderFake {
	return &planeControlToolProviderFake{id: id, spec: planeControlToolProviderValidSpec()}
}

// TestPlaneControlToolProvider_Declaration pins the exclusive plane contract:
// one provider slot, feature-only exclusive admission, canonical-required
// request access, nil rejection, and a dedicated conflict sentinel.
func TestPlaneControlToolProvider_Declaration(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "control_tool_provider", feature.PlaneControlToolProvider.PlaneID())
	assert.Equal(t, feature.MultExclusive, feature.PlaneControlToolProvider.Multiplicity)
	assert.Equal(t, feature.CombExclusive, feature.PlaneControlToolProvider.Rules.Feature)
	assert.Equal(t, feature.CombUnsupported, feature.PlaneControlToolProvider.Rules.Host)
	assert.Equal(t, feature.CombUnsupported, feature.PlaneControlToolProvider.Rules.GenerationBinder)
	assert.Equal(t, feature.NilReject, feature.PlaneControlToolProvider.NilPolicy)
	assert.Equal(t, feature.RequestBodyCanonicalRequired, feature.PlaneControlToolProvider.RequestAccess)
	assert.Equal(t, feature.ErrControlToolProviderConflict, feature.PlaneControlToolProvider.ExclusiveConflictError)
	assert.NotNil(t, feature.PlaneControlToolProvider.Identity)
	assert.NotNil(t, feature.PlaneControlToolProvider.ValidateIdentity)
	require.NoError(t, feature.PlaneControlToolProvider.ValidateDeclaration())

	declared := false
	for _, decl := range feature.StandardPlanes {
		if decl.PlaneID() == "control_tool_provider" {
			declared = true
		}
	}
	assert.True(t, declared, "control_tool_provider must be declared in StandardPlanes")
}

// TestPlaneControlToolProvider_AbsentSlotIsZeroWorkNoOp proves an unoccupied
// generation reads nil without any provider method call. Absence is proven by
// the plane read alone; Handle is never invoked to inspect absence.
func TestPlaneControlToolProvider_AbsentSlotIsZeroWorkNoOp(t *testing.T) {
	t.Parallel()

	empty := feature.NewContributionSet().Freeze()
	assert.Nil(t, feature.Get(empty, feature.PlaneControlToolProvider))
	id, ok := feature.FrozenIdentity(empty, feature.PlaneControlToolProvider)
	assert.False(t, ok, "absent provider must carry no frozen identity")
	assert.Empty(t, id)
}

// TestPlaneControlToolProvider_SingleValidContribution proves one valid
// contribution composes, freezes, and reads back with its identity.
func TestPlaneControlToolProvider_SingleValidContribution(t *testing.T) {
	t.Parallel()

	provider := newPlaneControlToolProviderFake("proxy.control.example")
	cs := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(cs, feature.PlaneControlToolProvider, "plugin-1", controltool.Provider(provider)))

	frozen := cs.Freeze()
	got := feature.Get(frozen, feature.PlaneControlToolProvider)
	require.NotNil(t, got, "occupied control_tool_provider must read back")
	assert.Equal(t, "proxy.control.example", got.ID())

	id, ok := feature.FrozenIdentity(frozen, feature.PlaneControlToolProvider)
	assert.True(t, ok)
	assert.Equal(t, "proxy.control.example", id)
	assert.Zero(t, provider.handles, "composition must not invoke the provider")
}

// TestPlaneControlToolProvider_SecondProviderRejectedDeterministically proves a
// second provider is rejected even when it carries the same object identity,
// and that rejection never mutates the accumulated set.
func TestPlaneControlToolProvider_SecondProviderRejectedDeterministically(t *testing.T) {
	t.Parallel()

	first := newPlaneControlToolProviderFake("proxy.control.example")
	cs := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(cs, feature.PlaneControlToolProvider, "plugin-1", controltool.Provider(first)))
	occupied := cs.Freeze()

	sameIdentity := newPlaneControlToolProviderFake("proxy.control.example")
	err := feature.Contribute(cs, feature.PlaneControlToolProvider, "plugin-2", controltool.Provider(sameIdentity))
	require.Error(t, err, "second provider must be rejected even with an identical identity")
	assert.ErrorIs(t, err, feature.ErrExclusiveConflict)
	assert.ErrorIs(t, err, feature.ErrControlToolProviderConflict)

	other := newPlaneControlToolProviderFake("proxy.control.other")
	err = feature.Contribute(cs, feature.PlaneControlToolProvider, "plugin-3", controltool.Provider(other))
	require.Error(t, err)
	assert.ErrorIs(t, err, feature.ErrExclusiveConflict)

	// Fail-before-mutate: the accumulated set still reads the first provider.
	after := cs.Freeze()
	assert.Equal(t, "proxy.control.example", feature.Get(after, feature.PlaneControlToolProvider).ID())
	assert.Zero(t, sameIdentity.handles)
	assert.Zero(t, other.handles)

	// The previously frozen snapshot is untouched by the rejected contributions.
	assert.Equal(t, "proxy.control.example", feature.Get(occupied, feature.PlaneControlToolProvider).ID())
}

// TestPlaneControlToolProvider_DefensiveRejectionBeforePublication proves typed
// nils, invalid identities, invalid specs, and panicking identity/spec accessors
// are all rejected before anything is published.
func TestPlaneControlToolProvider_DefensiveRejectionBeforePublication(t *testing.T) {
	t.Parallel()

	invalidSpec := newPlaneControlToolProviderFake("proxy.control.example")
	invalidSpec.spec = controltool.Spec{}

	badRole := newPlaneControlToolProviderFake("proxy.control.example")
	badRole.spec.Instruction.Role = aipapi.RoleUser

	oversizedArgs := newPlaneControlToolProviderFake("proxy.control.example")
	oversizedArgs.spec.MaxArgsBytes = controltool.DefaultMaxArgsBytes + 1

	var typedNil *planeControlToolProviderFake

	cases := map[string]controltool.Provider{
		"nil_provider":       controltool.Provider(nil),
		"typed_nil_provider": controltool.Provider(typedNil),
		"empty_id":           controltool.Provider(&planeControlToolProviderFake{spec: planeControlToolProviderValidSpec()}),
		"oversized_id":       controltool.Provider(newPlaneControlToolProviderFake(string(make([]byte, controltool.MaxProviderIDBytes+1)))),
		"invalid_spec":       controltool.Provider(invalidSpec),
		"instruction_role":   controltool.Provider(badRole),
		"oversized_args":     controltool.Provider(oversizedArgs),
		"panicking_id":       planeControlToolProviderPanicID{},
		"panicking_spec":     planeControlToolProviderPanicSpec{id: "proxy.control.example"},
	}

	for name, provider := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cs := feature.NewContributionSet()
			err := feature.Contribute(cs, feature.PlaneControlToolProvider, "plugin-invalid", provider)
			require.Error(t, err, "invalid control-tool provider must be rejected")
			assert.False(t, cs.Has("control_tool_provider"), "rejected contribution must not publish the plane")
			assert.Nil(t, feature.Get(cs.Freeze(), feature.PlaneControlToolProvider))
		})
	}

	t.Run("host_source_unsupported", func(t *testing.T) {
		t.Parallel()

		cs := feature.NewContributionSet()
		err := feature.ContributeSource(cs, feature.PlaneControlToolProvider, feature.SourceHost, "host",
			controltool.Provider(newPlaneControlToolProviderFake("proxy.control.example")))
		require.ErrorIs(t, err, feature.ErrUnsupportedSource)
		assert.False(t, cs.Has("control_tool_provider"))
	})

	t.Run("generation_binder_source_unsupported", func(t *testing.T) {
		t.Parallel()

		cs := feature.NewContributionSet()
		err := feature.ContributeSource(cs, feature.PlaneControlToolProvider, feature.SourceGenerationBinder, "binder",
			controltool.Provider(newPlaneControlToolProviderFake("proxy.control.example")))
		require.ErrorIs(t, err, feature.ErrUnsupportedSource)
		assert.False(t, cs.Has("control_tool_provider"))
	})

	t.Run("reject_does_not_disturb_an_occupied_slot", func(t *testing.T) {
		t.Parallel()

		cs := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(cs, feature.PlaneControlToolProvider, "plugin-1",
			controltool.Provider(newPlaneControlToolProviderFake("proxy.control.kept"))))
		occupied := cs.Freeze()

		require.Error(t, feature.Contribute(cs, feature.PlaneControlToolProvider, "plugin-2",
			controltool.Provider(typedNil)))

		after := cs.Freeze()
		assert.Equal(t, "proxy.control.kept", feature.Get(after, feature.PlaneControlToolProvider).ID())
		assert.Equal(t, "proxy.control.kept", feature.Get(occupied, feature.PlaneControlToolProvider).ID())
	})
}

// TestPlaneControlToolProvider_SnapshotImmutabilityAndRemoval proves the frozen
// snapshot is immutable and that withdrawing the provider in a new empty
// snapshot leaves the previously occupied snapshot intact (requirement 10.5).
func TestPlaneControlToolProvider_SnapshotImmutabilityAndRemoval(t *testing.T) {
	t.Parallel()

	provider := newPlaneControlToolProviderFake("proxy.control.example")
	cs := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(cs, feature.PlaneControlToolProvider, "plugin-1", controltool.Provider(provider)))
	occupied := cs.Freeze()
	occupiedClone := occupied.Clone()
	occupiedRequestFrozen := feature.FreezeRequestPlanes(occupied)

	// A new empty generation withdraws the provider.
	withdrawn := feature.NewContributionSet().Freeze()
	assert.Nil(t, feature.Get(withdrawn, feature.PlaneControlToolProvider))
	withdrawnID, withdrawnOK := feature.FrozenIdentity(withdrawn, feature.PlaneControlToolProvider)
	assert.False(t, withdrawnOK)
	assert.Empty(t, withdrawnID)

	// Every previously published snapshot still reads the occupied provider.
	for name, snap := range map[string]feature.FrozenPlaneSet{
		"occupied":       occupied,
		"occupied_clone": occupiedClone,
		"request_frozen": occupiedRequestFrozen,
	} {
		got := feature.Get(snap, feature.PlaneControlToolProvider)
		require.NotNil(t, got, "snapshot %s must retain the occupied provider", name)
		assert.Equal(t, "proxy.control.example", got.ID(), "snapshot %s identity", name)
		id, ok := feature.FrozenIdentity(snap, feature.PlaneControlToolProvider)
		assert.True(t, ok, "snapshot %s must retain a frozen identity", name)
		assert.Equal(t, "proxy.control.example", id, "snapshot %s frozen identity", name)
	}

	assert.Zero(t, provider.handles, "no snapshot read may invoke the provider")
}
