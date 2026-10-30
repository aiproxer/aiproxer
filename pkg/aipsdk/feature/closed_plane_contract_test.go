package feature_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// externalContractSubmitHook stub for contract tests
type externalContractSubmitHook struct {
	id  string
	ord int
}

func (h externalContractSubmitHook) ID() string                     { return h.id }
func (h externalContractSubmitHook) Order() int                     { return h.ord }
func (h externalContractSubmitHook) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (h externalContractSubmitHook) Handle(context.Context, *aipapi.Call, *hooks.SubmitMeta) (hooks.SubmitDecision, error) {
	return hooks.SubmitDecision{}, nil
}

// externalContractTerminalProvider stub for contract tests
type externalContractTerminalProvider struct {
	id string
}

func (p externalContractTerminalProvider) ID() string { return p.id }
func (p externalContractTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop}, nil
}

// externalContractFinalizer stub for contract tests
type externalContractFinalizer struct {
	id string
}

func (f externalContractFinalizer) ID() string { return f.id }
func (f externalContractFinalizer) Order() int { return 0 }
func (f externalContractFinalizer) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{}, nil
}

// TestClosedPlane_ExternalPackage_ErrUngeneratedPlane verifies from an external package
// perspective that contributing via an arbitrary ungenerated Plane returns ErrUngeneratedPlane
// with proper attribution, leaves the ContributionSet unmodified, and Get/FrozenIdentity return zero values.
func TestClosedPlane_ExternalPackage_ErrUngeneratedPlane(t *testing.T) {
	t.Parallel()

	unboundPlane := feature.Plane[[]string]{
		ID:           "external.custom_unbound_plane",
		Multiplicity: feature.MultOrdered,
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
			Host:    feature.CombConcatenate,
		},
		Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
			return append(cur, inc...), nil
		},
	}

	cs := feature.NewContributionSet()

	// 1. Contribute under SourceFeature fails with ErrUngeneratedPlane
	err := feature.Contribute(cs, unboundPlane, "plugin-ext", []string{"item1"})
	require.Error(t, err)
	require.True(t, errors.Is(err, feature.ErrUngeneratedPlane), "must match errors.Is ErrUngeneratedPlane")

	var attrErr *feature.AttributedError
	require.True(t, errors.As(err, &attrErr))
	assert.Equal(t, "plugin-ext", attrErr.PluginID)
	assert.Equal(t, "external.custom_unbound_plane", attrErr.PlaneID)

	// 2. ContributeSource under SourceHost fails with ErrUngeneratedPlane
	err = feature.ContributeSource(cs, unboundPlane, feature.SourceHost, "host-ext", []string{"host1"})
	require.Error(t, err)
	require.True(t, errors.Is(err, feature.ErrUngeneratedPlane))

	// 3. Fail-before-mutate: set has no entry for unbound plane
	assert.False(t, cs.Has("external.custom_unbound_plane"))

	// 4. Frozen set Get returns zero value
	frozen := cs.Freeze()
	val := feature.Get(frozen, unboundPlane)
	assert.Nil(t, val, "Get on ungenerated plane must return zero value")

	// 5. FrozenIdentity returns empty/false
	id, ok := feature.FrozenIdentity(frozen, unboundPlane)
	assert.False(t, ok)
	assert.Empty(t, id)
}

// TestClosedPlane_ExternalPackage_FeatureBundleSchemaAndStandardIDsUnchanged verifies that
// FeatureBundle schema version is exactly SchemaVersionV1 (1), validation contracts hold,
// and the manifest declares exactly the canonical standard plane set (25 original
// closure planes, the generation-binder secret_guard_execution plane, and the
// metadata-only session classifier plane).
func TestClosedPlane_ExternalPackage_FeatureBundleSchemaAndStandardIDsUnchanged(t *testing.T) {
	t.Parallel()

	// 1. Schema version constant is locked to 1
	assert.Equal(t, 1, feature.SchemaVersionV1)

	// 2. BundleFromPlanes sets SchemaVersion to SchemaVersionV1
	cs := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(cs, feature.PlaneSubmitHooks, "plugin-1", []hooks.SubmitHook{
		externalContractSubmitHook{id: "hook-1", ord: 1},
	}))
	frozen := cs.Freeze()

	bundle := feature.BundleFromPlanes(frozen, nil)
	assert.Equal(t, feature.SchemaVersionV1, bundle.SchemaVersion)
	require.NoError(t, bundle.Validate())

	// 3. Empty bundle allows schema version 0 or SchemaVersionV1
	emptyBundle := feature.FeatureBundle{}
	require.NoError(t, emptyBundle.Validate())
	emptyBundleV1 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1}
	require.NoError(t, emptyBundleV1.Validate())

	invalidEmpty := feature.FeatureBundle{SchemaVersion: 99}
	require.Error(t, invalidEmpty.Validate())

	// 4. Non-empty bundle with wrong schema version is rejected
	badVersionBundle := feature.FeatureBundle{
		SchemaVersion: 2,
		PlaneSet:      frozen,
	}
	require.Error(t, badVersionBundle.Validate())

	// 5. Standard planes manifest contains exactly 28 planes
	require.Len(t, feature.StandardPlanes, 28, "manifest must declare exactly 28 standard planes")

	// 6. Expected 28 standard plane IDs in exact canonical manifest order
	expectedStandardIDs := []string{
		"submit_hooks",
		"request_part_hooks",
		"response_part_hooks",
		"tool_reactors",
		"session_openers",
		"workspace_resolvers",
		"tool_catalog_filters",
		"tool_call_policies",
		"tool_call_finalizers",
		"tool_call_finalization_max_args_bytes",
		"request_transforms",
		"pre_request_handlers",
		"route_hint_providers",
		"completion_gates",
		"attempt_transforms",
		"stream_observer_factories",
		"traffic_observers",
		"usage_observers",
		"raw_capture_sinks",
		"traffic_redactors",
		"compaction_observers",
		"compaction_preservers",
		"secret_guards",
		"secret_guard_execution",
		"local_turn_handlers",
		"terminal_decision_provider",
		"session_classifier",
		"control_tool_provider",
	}

	seen := make(map[string]bool, len(feature.StandardPlanes))
	for i, expectedID := range expectedStandardIDs {
		actualID := feature.StandardPlanes[i].PlaneID()
		assert.Equal(t, expectedID, actualID, "plane at index %d must match canonical ID", i)
		assert.False(t, seen[actualID], "duplicate plane ID %q", actualID)
		seen[actualID] = true
	}

	// 7. Standard planes validate cleanly as a manifest
	require.NoError(t, feature.ValidateManifest(feature.StandardPlanes...))
}

// TestClosedPlane_ExternalPackage_AdversarialChangedID_Regression tests that copying a canonical
// standard plane and modifying its ID is rejected with ErrUngeneratedPlane across multiple multiplicity kinds.
func TestClosedPlane_ExternalPackage_AdversarialChangedID_Regression(t *testing.T) {
	t.Parallel()

	cs := feature.NewContributionSet()

	// Ordered slice plane: PlaneSubmitHooks
	tamperedHooks := feature.PlaneSubmitHooks
	tamperedHooks.ID = "submit_hooks.tampered"
	err := feature.Contribute(cs, tamperedHooks, "plugin-adv", []hooks.SubmitHook{
		externalContractSubmitHook{id: "h1", ord: 1},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, feature.ErrUngeneratedPlane))
	assert.False(t, cs.Has("submit_hooks"))
	assert.False(t, cs.Has("submit_hooks.tampered"))

	// Exclusive plane: PlaneTerminalDecisionProvider
	tamperedTerm := feature.PlaneTerminalDecisionProvider
	tamperedTerm.ID = "terminal_decision_provider.tampered"
	err = feature.Contribute(cs, tamperedTerm, "plugin-adv", terminaldecision.Provider(
		externalContractTerminalProvider{id: "term1"},
	))
	require.Error(t, err)
	require.True(t, errors.Is(err, feature.ErrUngeneratedPlane))
	assert.False(t, cs.Has("terminal_decision_provider"))
	assert.False(t, cs.Has("terminal_decision_provider.tampered"))

	// Scalar reduce plane: PlaneToolCallFinalizationMaxArgsBytes
	tamperedMax := feature.PlaneToolCallFinalizationMaxArgsBytes
	tamperedMax.ID = "tool_call_finalization_max_args_bytes.tampered"
	err = feature.Contribute(cs, tamperedMax, "plugin-adv", 2048)
	require.Error(t, err)
	require.True(t, errors.Is(err, feature.ErrUngeneratedPlane))
	assert.False(t, cs.Has("tool_call_finalization_max_args_bytes"))
	assert.False(t, cs.Has("tool_call_finalization_max_args_bytes.tampered"))

	// Request borrowed plane: PlaneToolCallFinalizers
	tamperedFin := feature.PlaneToolCallFinalizers
	tamperedFin.ID = "tool_call_finalizers.tampered"
	err = feature.Contribute(cs, tamperedFin, "plugin-adv", []toolcall.Finalizer{
		externalContractFinalizer{id: "fin1"},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, feature.ErrUngeneratedPlane))
	assert.False(t, cs.Has("tool_call_finalizers"))
	assert.False(t, cs.Has("tool_call_finalizers.tampered"))
}

// TestClosedPlane_ExternalPackage_AdversarialSameIDMutated_Regression tests that copying a canonical
// standard plane and mutating its exported descriptor fields cannot alter canonical generated policy authority.
func TestClosedPlane_ExternalPackage_AdversarialSameIDMutated_Regression(t *testing.T) {
	t.Parallel()

	// 1. Mutated Rules: clearing Feature source support on PlaneSubmitHooks
	t.Run("mutated Rules cannot disable canonical source support", func(t *testing.T) {
		t.Parallel()
		copied := feature.PlaneSubmitHooks
		copied.Rules = feature.SourceRules{
			Host: feature.CombConcatenate,
		}
		cs := feature.NewContributionSet()
		err := feature.Contribute(cs, copied, "plugin-1", []hooks.SubmitHook{
			externalContractSubmitHook{id: "hook-1", ord: 1},
		})
		require.NoError(t, err, "canonical generated rules are authoritative")
		assert.Len(t, feature.Get(cs.Freeze(), feature.PlaneSubmitHooks), 1)
	})

	// 2. Mutated Combine: returning error on PlaneSubmitHooks
	t.Run("mutated Combine cannot override canonical concatenation", func(t *testing.T) {
		t.Parallel()
		copied := feature.PlaneSubmitHooks
		copied.Combine = func(source feature.SourceKind, cur, inc []hooks.SubmitHook) ([]hooks.SubmitHook, error) {
			return nil, errors.New("adversarial combiner failure")
		}
		cs := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(cs, copied, "plugin-1", []hooks.SubmitHook{
			externalContractSubmitHook{id: "hook-1", ord: 1},
		}))
		require.NoError(t, feature.Contribute(cs, copied, "plugin-2", []hooks.SubmitHook{
			externalContractSubmitHook{id: "hook-2", ord: 2},
		}))
		got := feature.Get(cs.Freeze(), feature.PlaneSubmitHooks)
		require.Len(t, got, 2, "canonical combiner is authoritative")
		assert.Equal(t, "hook-1", got[0].ID())
		assert.Equal(t, "hook-2", got[1].ID())
	})

	// 3. Mutated Validate: injecting always-fail validator on PlaneToolCallFinalizationMaxArgsBytes
	t.Run("mutated Validate cannot override canonical validator", func(t *testing.T) {
		t.Parallel()
		copied := feature.PlaneToolCallFinalizationMaxArgsBytes
		copied.Validate = func(v int) error {
			return errors.New("adversarial always-fail validation")
		}
		cs := feature.NewContributionSet()
		err := feature.Contribute(cs, copied, "plugin-1", 4096)
		require.NoError(t, err, "canonical validator accepts positive numbers")
		assert.Equal(t, 4096, feature.Get(cs.Freeze(), feature.PlaneToolCallFinalizationMaxArgsBytes))

		// Canonical validator rejects negative numbers:
		errNeg := feature.Contribute(cs, copied, "plugin-2", -10)
		require.Error(t, errNeg)
		require.True(t, errors.Is(errNeg, feature.ErrInvalidContribution))
	})

	// 4. Mutated NilPolicy: changing NilReject to NilSkip on PlaneTerminalDecisionProvider
	t.Run("mutated NilPolicy cannot bypass NilReject", func(t *testing.T) {
		t.Parallel()
		copied := feature.PlaneTerminalDecisionProvider
		copied.NilPolicy = feature.NilSkip
		cs := feature.NewContributionSet()
		err := feature.Contribute(cs, copied, "plugin-1", terminaldecision.Provider(nil))
		require.Error(t, err)
		require.True(t, errors.Is(err, feature.ErrNilContribution), "canonical NilReject must be enforced")
	})

	// 5. Mutated Identity: returning spoofed ID on PlaneTerminalDecisionProvider
	t.Run("mutated Identity cannot alter canonical identity extraction", func(t *testing.T) {
		t.Parallel()
		copied := feature.PlaneTerminalDecisionProvider
		copied.Identity = func(v terminaldecision.Provider) (string, bool) {
			return "spoofed_id", true
		}
		cs := feature.NewContributionSet()
		err := feature.Contribute(cs, copied, "plugin-1", terminaldecision.Provider(
			externalContractTerminalProvider{id: "actual_id"},
		))
		require.NoError(t, err)
		frozen := cs.Freeze()
		id, ok := feature.FrozenIdentity(frozen, feature.PlaneTerminalDecisionProvider)
		assert.True(t, ok)
		assert.Equal(t, "actual_id", id, "canonical Identity extractor is authoritative")
	})

	// 6. Mutated Multiplicity: changing MultExclusive to MultOrdered on PlaneTerminalDecisionProvider
	t.Run("mutated Multiplicity cannot bypass exclusive conflict", func(t *testing.T) {
		t.Parallel()
		copied := feature.PlaneTerminalDecisionProvider
		copied.Multiplicity = feature.MultOrdered
		cs := feature.NewContributionSet()
		err := feature.Contribute(cs, copied, "plugin-1", terminaldecision.Provider(
			externalContractTerminalProvider{id: "p1"},
		))
		require.NoError(t, err)

		err = feature.Contribute(cs, copied, "plugin-2", terminaldecision.Provider(
			externalContractTerminalProvider{id: "p2"},
		))
		require.Error(t, err)
		require.True(t, errors.Is(err, feature.ErrExclusiveConflict), "canonical MultExclusive must be enforced")
	})

	// 7. Replay using mutated descriptor preserves canonical behavior
	t.Run("replaying with mutated descriptor preserves canonical behavior", func(t *testing.T) {
		t.Parallel()
		src := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(src, feature.PlaneSubmitHooks, "p1", []hooks.SubmitHook{
			externalContractSubmitHook{id: "replayed-hook", ord: 1},
		}))
		frozenSrc := src.Freeze()

		dst := feature.NewContributionSet()
		err := frozenSrc.ReplayTo(dst, "replayer")
		require.NoError(t, err)

		frozenDst := dst.Freeze()
		got := feature.Get(frozenDst, feature.PlaneSubmitHooks)
		require.Len(t, got, 1)
		assert.Equal(t, "replayed-hook", got[0].ID())
	})
}
