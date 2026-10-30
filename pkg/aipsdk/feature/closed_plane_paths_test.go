package feature

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

type closedPlaneSubmitHook struct {
	id  string
	ord int
}

func (h closedPlaneSubmitHook) ID() string                     { return h.id }
func (h closedPlaneSubmitHook) Order() int                     { return h.ord }
func (h closedPlaneSubmitHook) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (h closedPlaneSubmitHook) Handle(ctx context.Context, call *aipapi.Call, meta *hooks.SubmitMeta) (hooks.SubmitDecision, error) {
	return hooks.SubmitDecision{}, nil
}

type closedPlaneAttemptTransform struct {
	id  string
	ord int
}

func (t closedPlaneAttemptTransform) ID() string                     { return t.id }
func (t closedPlaneAttemptTransform) Order() int                     { return t.ord }
func (t closedPlaneAttemptTransform) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (t closedPlaneAttemptTransform) HandleAttempt(context.Context, *aipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{}, nil
}

type closedPlaneFinalizer struct {
	id string
}

func (f closedPlaneFinalizer) ID() string { return f.id }
func (closedPlaneFinalizer) Order() int   { return 0 }
func (closedPlaneFinalizer) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{}, nil
}

type closedPlaneTerminalDecisionProvider struct {
	id string
}

func (p closedPlaneTerminalDecisionProvider) ID() string { return p.id }
func (p closedPlaneTerminalDecisionProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop}, nil
}

// TestClosedPlanePaths_FrozenPlaneSet_Structure verifies that FrozenPlaneSet contains no arbitrary
// values or identities maps (structural gate for Task 2.3).
func TestClosedPlanePaths_FrozenPlaneSet_Structure(t *testing.T) {
	t.Parallel()

	fpsType := reflect.TypeFor[FrozenPlaneSet]()
	_, hasValues := fpsType.FieldByName("values")
	assert.False(t, hasValues, "FrozenPlaneSet must not have 'values' map field")

	_, hasIdentities := fpsType.FieldByName("identities")
	assert.False(t, hasIdentities, "FrozenPlaneSet must not have 'identities' map field")
}

// TestClosedPlanePaths_UngeneratedPlane_GetAndIdentityReturnZero verifies that Get and FrozenIdentity
// on an ungenerated plane return zero values without searching any map-backed storage.
func TestClosedPlanePaths_UngeneratedPlane_GetAndIdentityReturnZero(t *testing.T) {
	t.Parallel()

	unbound := Plane[string]{
		ID:           "test.unbound.plane",
		Multiplicity: MultOrdered,
	}

	cs := NewContributionSet()
	require.NoError(t, Contribute(cs, PlaneSubmitHooks, "plugin-1", []hooks.SubmitHook{
		closedPlaneSubmitHook{id: "hook-1", ord: 1},
	}))
	frozen := cs.Freeze()

	val := Get(frozen, unbound)
	assert.Empty(t, val, "Get on ungenerated plane must return zero value")

	id, ok := FrozenIdentity(frozen, unbound)
	assert.False(t, ok, "FrozenIdentity on ungenerated plane must return false")
	assert.Empty(t, id, "FrozenIdentity on ungenerated plane must return empty string")
}

// TestClosedPlanePaths_ContributionFreezeAndClone verifies that ContributionSet.Freeze and FrozenPlaneSet.Clone
// operate solely on generated typed storage and isolate backing slices.
func TestClosedPlanePaths_ContributionFreezeAndClone(t *testing.T) {
	t.Parallel()

	cs := NewContributionSet()
	hookSlice := []hooks.SubmitHook{closedPlaneSubmitHook{id: "hook-1", ord: 1}}
	require.NoError(t, Contribute(cs, PlaneSubmitHooks, "plugin-1", hookSlice))
	require.NoError(t, Contribute(cs, PlaneAttemptTransforms, "plugin-1", []request.AttemptTransform{
		closedPlaneAttemptTransform{id: "at-1", ord: 10},
	}))
	require.NoError(t, Contribute(cs, PlaneToolCallFinalizationMaxArgsBytes, "plugin-1", 4096))
	require.NoError(t, Contribute(cs, PlaneTerminalDecisionProvider, "plugin-1", terminaldecision.Provider(closedPlaneTerminalDecisionProvider{id: "term-1"})))

	frozen := cs.Freeze()
	require.False(t, frozen.IsZero())

	// Mutate input slice after freeze to prove isolation
	hookSlice[0] = closedPlaneSubmitHook{id: "mutated-hook", ord: 99}

	gotHooks := Get(frozen, PlaneSubmitHooks)
	require.Len(t, gotHooks, 1)
	assert.Equal(t, "hook-1", gotHooks[0].ID(), "Freeze must isolate source slice")

	gotID, ok := FrozenIdentity(frozen, PlaneAttemptTransforms)
	assert.True(t, ok)
	assert.Equal(t, "at-1", gotID)

	termID, ok := FrozenIdentity(frozen, PlaneTerminalDecisionProvider)
	assert.True(t, ok)
	assert.Equal(t, "term-1", termID)

	maxArgs := Get(frozen, PlaneToolCallFinalizationMaxArgsBytes)
	assert.Equal(t, 4096, maxArgs)

	// Clone verification
	cloned := frozen.Clone()
	require.False(t, cloned.IsZero())
	clonedHooks := Get(cloned, PlaneSubmitHooks)
	require.Len(t, clonedHooks, 1)
	assert.Equal(t, "hook-1", clonedHooks[0].ID())

	clonedTermID, ok := FrozenIdentity(cloned, PlaneTerminalDecisionProvider)
	assert.True(t, ok)
	assert.Equal(t, "term-1", clonedTermID)
}

// TestClosedPlanePaths_ToContributions_RoundTrip verifies that FrozenPlaneSet.ToContributions roundtrips
// typed state and plugin attribution into a mutable ContributionSet.
func TestClosedPlanePaths_ToContributions_RoundTrip(t *testing.T) {
	t.Parallel()

	cs := NewContributionSet()
	require.NoError(t, Contribute(cs, PlaneSubmitHooks, "plugin-hooks", []hooks.SubmitHook{
		closedPlaneSubmitHook{id: "hook-1", ord: 1},
	}))
	require.NoError(t, Contribute(cs, PlaneAttemptTransforms, "plugin-transforms", []request.AttemptTransform{
		closedPlaneAttemptTransform{id: "at-1", ord: 10},
	}))

	frozen := cs.Freeze()
	thawed := frozen.ToContributions()
	require.NotNil(t, thawed)
	assert.True(t, thawed.Has(PlaneSubmitHooks.ID))
	assert.True(t, thawed.Has(PlaneAttemptTransforms.ID))

	// Re-freeze thawed set and assert values survive
	refrozen := thawed.Freeze()
	gotHooks := Get(refrozen, PlaneSubmitHooks)
	require.Len(t, gotHooks, 1)
	assert.Equal(t, "hook-1", gotHooks[0].ID())

	atID, ok := FrozenIdentity(refrozen, PlaneAttemptTransforms)
	assert.True(t, ok)
	assert.Equal(t, "at-1", atID)
}

// TestClosedPlanePaths_RequestFreezeAndExecutionView verifies FreezeRequestPlanes and RequestExecutionView
// over request-materialized planes.
func TestClosedPlanePaths_RequestFreezeAndExecutionView(t *testing.T) {
	t.Parallel()

	cs := NewContributionSet()
	require.NoError(t, Contribute(cs, PlaneSubmitHooks, "plugin-1", []hooks.SubmitHook{
		closedPlaneSubmitHook{id: "hook-1", ord: 1},
	}))
	require.NoError(t, Contribute(cs, PlaneToolCallFinalizers, "plugin-1", []toolcall.Finalizer{
		closedPlaneFinalizer{id: "fin-1"},
	}))

	frozen := cs.Freeze()
	reqFrozen := FreezeRequestPlanes(frozen)
	require.False(t, reqFrozen.IsZero())

	// Read through RequestExecutionView
	view := RequestExecution(reqFrozen)
	borrowedFins := view.ToolCallFinalizers()
	require.Len(t, borrowedFins, 1)
	assert.Equal(t, "fin-1", borrowedFins[0].ID())

	// Plain Get also retrieves the request view
	gotHooks := Get(reqFrozen, PlaneSubmitHooks)
	require.Len(t, gotHooks, 1)
	assert.Equal(t, "hook-1", gotHooks[0].ID())
}

// TestClosedPlanePaths_Validate_FrozenAndBundle verifies FrozenPlaneSet.Validate and FeatureBundle.Validate
// using typed state only, ensuring fail-before-mutate behavior on invalid state.
func TestClosedPlanePaths_Validate_FrozenAndBundle(t *testing.T) {
	t.Parallel()

	// 1. Valid frozen set passes Validate and FeatureBundle.Validate
	cs := NewContributionSet()
	require.NoError(t, Contribute(cs, PlaneSubmitHooks, "plugin-1", []hooks.SubmitHook{
		closedPlaneSubmitHook{id: "hook-1", ord: 1},
	}))
	frozen := cs.Freeze()
	require.NoError(t, frozen.Validate())

	bundle := BundleFromPlanes(frozen, nil)
	require.NoError(t, bundle.Validate())

	// 2. Malformed frozen set (missing identity for exclusive provider) fails Validate
	prov := closedPlaneTerminalDecisionProvider{id: "term-no-id"}
	malformedFrozen := NewMalformedGeneratedFrozenTerminalDecisionMissingIdentityForTest(prov)
	err := malformedFrozen.Validate()
	require.Error(t, err)

	var valErr *planeValidationError
	require.True(t, errors.As(err, &valErr))
	assert.Equal(t, PlaneTerminalDecisionProvider.ID, valErr.planeID)

	// 3. FeatureBundle.Validate fails closed when PlaneSet fails validation
	badBundle := FeatureBundle{
		SchemaVersion: SchemaVersionV1,
		PlaneSet:      malformedFrozen,
	}
	bundleErr := badBundle.Validate()
	require.Error(t, bundleErr)
	assert.Contains(t, bundleErr.Error(), PlaneTerminalDecisionProvider.ID)
}

// TestClosedPlanePaths_OrdinaryReplay_GeneratedTypedOnly_FailBeforeMutate verifies that FrozenPlaneSet.ReplayTo
// uses generated typed state only, and preserves fail-before-mutate when an exclusive collision occurs.
func TestClosedPlanePaths_OrdinaryReplay_GeneratedTypedOnly_FailBeforeMutate(t *testing.T) {
	t.Parallel()

	// Destination starts with an initial submit hook and an exclusive terminal provider
	dst := NewContributionSet()
	require.NoError(t, Contribute(dst, PlaneSubmitHooks, "base-plugin", []hooks.SubmitHook{
		closedPlaneSubmitHook{id: "base-hook", ord: 1},
	}))
	require.NoError(t, Contribute(dst, PlaneTerminalDecisionProvider, "base-plugin", terminaldecision.Provider(closedPlaneTerminalDecisionProvider{id: "base-provider"})))
	dstBefore := dst.Freeze()

	// Source set has a new submit hook AND a conflicting exclusive terminal provider
	src := NewContributionSet()
	require.NoError(t, Contribute(src, PlaneSubmitHooks, "src-plugin", []hooks.SubmitHook{
		closedPlaneSubmitHook{id: "src-hook", ord: 2},
	}))
	require.NoError(t, Contribute(src, PlaneTerminalDecisionProvider, "src-plugin", terminaldecision.Provider(closedPlaneTerminalDecisionProvider{id: "conflicting-provider"})))
	srcFrozen := src.Freeze()

	// Replaying srcFrozen into dst must fail with ErrExclusiveConflict
	err := srcFrozen.ReplayTo(dst, "replayer")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrExclusiveConflict)

	var attrErr *AttributedError
	require.True(t, errors.As(err, &attrErr))
	assert.Equal(t, "replayer", attrErr.PluginID)
	assert.Equal(t, PlaneTerminalDecisionProvider.ID, attrErr.PlaneID)

	// FAIL-BEFORE-MUTATE: Destination must be completely untouched!
	dstAfter := dst.Freeze()
	hooksBefore := Get(dstBefore, PlaneSubmitHooks)
	hooksAfter := Get(dstAfter, PlaneSubmitHooks)
	require.Len(t, hooksAfter, 1)
	assert.Equal(t, hooksBefore[0].ID(), hooksAfter[0].ID(), "SubmitHooks must not have been modified")

	provAfter := Get(dstAfter, PlaneTerminalDecisionProvider)
	require.NotNil(t, provAfter)
	assert.Equal(t, "base-provider", provAfter.ID(), "TerminalDecisionProvider must retain base provider")
}

// TestClosedPlanePaths_CandidateReplay_GeneratedTypedOnly_FailBeforeMutate verifies that FrozenPlaneSet.ContributeCandidateTo
// operates on generated typed storage and preserves fail-before-mutate on conflict.
func TestClosedPlanePaths_CandidateReplay_GeneratedTypedOnly_FailBeforeMutate(t *testing.T) {
	t.Parallel()

	// Destination has initial hook and initial exclusive provider
	dst := NewContributionSet()
	require.NoError(t, Contribute(dst, PlaneSubmitHooks, "base-plugin", []hooks.SubmitHook{
		closedPlaneSubmitHook{id: "base-hook", ord: 1},
	}))
	require.NoError(t, Contribute(dst, PlaneTerminalDecisionProvider, "base-plugin", terminaldecision.Provider(closedPlaneTerminalDecisionProvider{id: "base-provider"})))
	dstBefore := dst.Freeze()

	// Candidate has a new hook and a conflicting terminal decision provider
	candSrc := NewContributionSet()
	require.NoError(t, Contribute(candSrc, PlaneSubmitHooks, "cand-plugin", []hooks.SubmitHook{
		closedPlaneSubmitHook{id: "cand-hook", ord: 2},
	}))
	require.NoError(t, Contribute(candSrc, PlaneTerminalDecisionProvider, "cand-plugin", terminaldecision.Provider(closedPlaneTerminalDecisionProvider{id: "cand-provider"})))
	candFrozen := candSrc.Freeze()

	// Candidate replay must fail on exclusive conflict
	err := candFrozen.ContributeCandidateTo(dst, SourceFeature, "candidate")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrExclusiveConflict)

	// FAIL-BEFORE-MUTATE: Destination must be completely untouched!
	dstAfter := dst.Freeze()
	hooksBefore := Get(dstBefore, PlaneSubmitHooks)
	hooksAfter := Get(dstAfter, PlaneSubmitHooks)
	assert.Equal(t, hooksBefore[0].ID(), hooksAfter[0].ID())

	provBefore := Get(dstBefore, PlaneTerminalDecisionProvider)
	provAfter := Get(dstAfter, PlaneTerminalDecisionProvider)
	assert.Equal(t, provBefore.ID(), provAfter.ID())
}
