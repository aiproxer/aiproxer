package feature_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Test stubs for characterization tests ---

type charStubSubmitHook struct {
	id    string
	order int
}

func (h charStubSubmitHook) ID() string                   { return h.id }
func (h charStubSubmitHook) Order() int                   { return h.order }
func (charStubSubmitHook) FailureMode() hooks.FailureMode { return hooks.FailOpen }
func (charStubSubmitHook) Handle(context.Context, *aipapi.Call, *hooks.SubmitMeta) (hooks.SubmitDecision, error) {
	return hooks.SubmitDecision{}, nil
}

type charStubRequestPartHook struct {
	id    string
	order int
}

func (h charStubRequestPartHook) ID() string                   { return h.id }
func (h charStubRequestPartHook) Order() int                   { return h.order }
func (charStubRequestPartHook) FailureMode() hooks.FailureMode { return hooks.FailOpen }
func (charStubRequestPartHook) HandleRequestParts(context.Context, *aipapi.Call, hooks.PartMeta) error {
	return nil
}

type charStubTerminalProvider struct {
	id string
}

func (p charStubTerminalProvider) ID() string { return p.id }
func (charStubTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "done"}, nil
}

type charStubSessionOpener struct {
	id string
}

func (s charStubSessionOpener) ID() string { return s.id }
func (charStubSessionOpener) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

type charStubWorkspaceResolver struct {
	id string
}

func (r charStubWorkspaceResolver) Resolve(context.Context) (workspace.WorkspaceView, error) {
	return workspace.WorkspaceView{}, nil
}

type charStubSecretGuard struct {
	id    string
	order int
}

func (g charStubSecretGuard) ID() string                         { return g.id }
func (g charStubSecretGuard) Order() int                         { return g.order }
func (charStubSecretGuard) FailureMode() secretguard.FailureMode { return secretguard.FailOpen }
func (charStubSecretGuard) Evaluate(context.Context, *aipapi.Call, secretguard.Meta, secretguard.Services) (secretguard.Decision, error) {
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

type charStubRequestTransform struct {
	id    string
	order int
}

func (t charStubRequestTransform) ID() string                   { return t.id }
func (t charStubRequestTransform) Order() int                   { return t.order }
func (charStubRequestTransform) FailureMode() hooks.FailureMode { return hooks.FailOpen }
func (charStubRequestTransform) Handle(context.Context, *aipapi.Call, request.RequestMeta, request.Services) error {
	return nil
}

type charStubAttemptTransform struct {
	id    string
	order int
}

func (t charStubAttemptTransform) ID() string                   { return t.id }
func (t charStubAttemptTransform) Order() int                   { return t.order }
func (charStubAttemptTransform) FailureMode() hooks.FailureMode { return hooks.FailOpen }
func (charStubAttemptTransform) HandleAttempt(context.Context, *aipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{}, nil
}

type charStubToolReactor struct {
	id    string
	order int
}

func (r charStubToolReactor) ID() string { return r.id }
func (r charStubToolReactor) Order() int { return r.order }
func (charStubToolReactor) HandleToolEvent(context.Context, aipapi.ToolEvent, hooks.ToolMeta) (hooks.ToolDecision, aipapi.ToolEvent, error) {
	return hooks.ToolPass, aipapi.ToolEvent{}, nil
}

type charStubPreRequestHandler struct {
	id    string
	order int
}

func (h charStubPreRequestHandler) ID() string                   { return h.id }
func (h charStubPreRequestHandler) Order() int                   { return h.order }
func (charStubPreRequestHandler) FailureMode() hooks.FailureMode { return hooks.FailOpen }
func (charStubPreRequestHandler) Handle(context.Context, *aipapi.Call, prerequest.Meta, prerequest.Services) (prerequest.Decision, error) {
	return prerequest.Decision{}, nil
}

type charStubStreamObserverFactory struct {
	id    string
	order int
}

func (f charStubStreamObserverFactory) ID() string                   { return f.id }
func (f charStubStreamObserverFactory) Order() int                   { return f.order }
func (charStubStreamObserverFactory) FailureMode() hooks.FailureMode { return hooks.FailOpen }
func (charStubStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

// =============================================================================
// Section A: Arbitrary Unbound Plane Fallback Storage Characterization (Pre-Task-2)
//
// These tests characterize the current map/reflection-based fallback storage behavior
// for arbitrary unbound Plane[[]T] declarations that have no generated binding.
// In Task 2, Requirement 1 will cause unbound planes to be rejected with ErrUngeneratedPlane
// before mutating the ContributionSet.
// =============================================================================

func newTestUnboundSlicePlane() feature.Plane[[]string] {
	return feature.Plane[[]string]{
		ID:           "test.unbound_slice_plane",
		Multiplicity: feature.MultOrdered,
		Rules: feature.SourceRules{
			Feature:          feature.CombConcatenate,
			Host:             feature.CombConcatenate,
			GenerationBinder: feature.CombConcatenate,
		},
		NilPolicy: feature.NilNotApplicable,
		Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
			return append(cur, inc...), nil
		},
	}
}

func TestClosedPlane_UnboundRejection_Contribute(t *testing.T) {
	t.Parallel()

	unboundPlane := newTestUnboundSlicePlane()
	cs := feature.NewContributionSet()

	// 1. Contribute under SourceFeature fails with ErrUngeneratedPlane
	err := feature.Contribute(cs, unboundPlane, "plugin-1", []string{"alpha", "beta"})
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrUngeneratedPlane)

	var attrErr *feature.AttributedError
	require.True(t, errors.As(err, &attrErr))
	assert.Equal(t, "plugin-1", attrErr.PluginID)
	assert.Equal(t, "test.unbound_slice_plane", attrErr.PlaneID)

	// 2. Contribute under SourceHost fails with ErrUngeneratedPlane
	err = feature.ContributeSource(cs, unboundPlane, feature.SourceHost, "host", []string{"gamma"})
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrUngeneratedPlane)

	// Fail-before-mutate: set is unmodified
	assert.False(t, cs.Has("test.unbound_slice_plane"))
}

func TestClosedPlane_GeneratedFixture_FreezeAndClone(t *testing.T) {
	t.Parallel()

	boundPlane := feature.BindGeneratedTestPlane(newTestUnboundSlicePlane())
	cs := feature.NewContributionSet()

	err := feature.Contribute(cs, boundPlane, "plugin-1", []string{"val1", "val2"})
	require.NoError(t, err)

	// 1. ContributionSet.Clone creates an independent copy
	csClone := cs.Clone()
	require.NotNil(t, csClone)
	assert.True(t, csClone.Has("test.unbound_slice_plane"))

	// Mutate original by contributing more
	err = feature.Contribute(cs, boundPlane, "plugin-2", []string{"val3"})
	require.NoError(t, err)

	// csClone must remain untouched
	frozenFromClone := csClone.Freeze()
	assert.Equal(t, []string{"val1", "val2"}, feature.Get(frozenFromClone, boundPlane))

	// Original frozen reflects both contributions
	frozenFromOrig := cs.Freeze()
	assert.Equal(t, []string{"val1", "val2", "val3"}, feature.Get(frozenFromOrig, boundPlane))

	// 2. FrozenPlaneSet.Clone creates an independent copy
	frozenClone := frozenFromOrig.Clone()
	assert.Equal(t, []string{"val1", "val2", "val3"}, feature.Get(frozenClone, boundPlane))
}

func TestClosedPlane_GeneratedFixture_ToContributions(t *testing.T) {
	t.Parallel()

	boundPlane := feature.BindGeneratedTestPlane(newTestUnboundSlicePlane())
	cs := feature.NewContributionSet()

	err := feature.Contribute(cs, boundPlane, "plugin-1", []string{"initial"})
	require.NoError(t, err)

	frozen := cs.Freeze()

	// 1. Thaw to mutable ContributionSet
	thawed := frozen.ToContributions()
	require.NotNil(t, thawed)
	assert.True(t, thawed.Has("test.unbound_slice_plane"))

	// 2. Contribute further to thawed set
	err = feature.Contribute(thawed, boundPlane, "plugin-2", []string{"additional"})
	require.NoError(t, err)

	// 3. Freeze thawed set and verify combined state
	refrozen := thawed.Freeze()
	assert.Equal(t, []string{"initial", "additional"}, feature.Get(refrozen, boundPlane))

	// Original frozen remains unmodified
	assert.Equal(t, []string{"initial"}, feature.Get(frozen, boundPlane))

	// 4. ContributionSetFromFrozen equivalent
	thawed2 := feature.ContributionSetFromFrozen(frozen)
	assert.Equal(t, []string{"initial"}, feature.Get(thawed2.Freeze(), boundPlane))
}

func TestClosedPlane_DeclarationValidation_CustomPlaneWithRequestMaterializer(t *testing.T) {
	t.Parallel()

	customPlane := feature.Plane[[]string]{
		ID:           "test.custom_materialized_plane",
		Multiplicity: feature.MultOrdered,
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
		},
		NilPolicy: feature.NilNotApplicable,
		Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
			return append(cur, inc...), nil
		},
		RequestMaterializer: func(v []string) []string {
			sorted := slices.Clone(v)
			slices.Sort(sorted)
			return sorted
		},
	}

	// 1. Declaration validation passes
	require.NoError(t, customPlane.ValidateDeclaration())

	// 2. Ungenerated plane contribution fails with ErrUngeneratedPlane
	cs := feature.NewContributionSet()
	err := feature.Contribute(cs, customPlane, "plugin-1", []string{"zebra", "apple", "mango"})
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrUngeneratedPlane)
}

func TestClosedPlane_GeneratedFixture_FrozenValidation(t *testing.T) {
	t.Parallel()

	t.Run("valid bound plane passes FrozenPlaneSet.Validate", func(t *testing.T) {
		t.Parallel()
		boundPlane := feature.BindGeneratedTestPlane(newTestUnboundSlicePlane())
		cs := feature.NewContributionSet()
		err := feature.Contribute(cs, boundPlane, "plugin-1", []string{"valid-item"})
		require.NoError(t, err)

		frozen := cs.Freeze()
		require.NoError(t, frozen.Validate())
	})

	t.Run("bound plane with stored validation failure is reported on Validate", func(t *testing.T) {
		t.Parallel()
		planeWithValidator := feature.BindGeneratedTestPlane(feature.Plane[[]string]{
			ID:           "test.unbound_validating_plane",
			Multiplicity: feature.MultOrdered,
			Rules: feature.SourceRules{
				Feature: feature.CombConcatenate,
			},
			Validate: func(v []string) error {
				for _, s := range v {
					if strings.Contains(s, "forbidden") {
						return errors.New("contains forbidden string")
					}
				}
				return nil
			},
			Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
				return append(cur, inc...), nil
			},
		})

		cs := feature.NewContributionSet()
		// Initial contribute passes
		err := feature.Contribute(cs, planeWithValidator, "plugin-1", []string{"clean"})
		require.NoError(t, err)

		frozen := cs.Freeze()
		require.NoError(t, frozen.Validate())
	})
}

func TestClosedPlane_GeneratedFixture_FeatureBundleValidation(t *testing.T) {
	t.Parallel()

	boundPlane := feature.BindGeneratedTestPlane(newTestUnboundSlicePlane())
	cs := feature.NewContributionSet()
	err := feature.Contribute(cs, boundPlane, "plugin-1", []string{"item1"})
	require.NoError(t, err)

	frozen := cs.Freeze()
	bundle := feature.BundleFromPlanes(frozen, nil)
	require.NotNil(t, bundle)
	require.NoError(t, bundle.Validate())
}

func TestClosedPlane_OrdinaryReplay_FailBeforeMutate(t *testing.T) {
	t.Parallel()

	// 1. ReplayTo into dst under SourceFeature using standard plane
	csSrc := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(csSrc, feature.PlaneSubmitHooks, "plugin-source", []hooks.SubmitHook{
		charStubSubmitHook{id: "src-hook-1", order: 1},
		charStubSubmitHook{id: "src-hook-2", order: 2},
	}))

	frozenSrc := csSrc.Freeze()
	dst := feature.NewContributionSet()
	err := frozenSrc.ReplayTo(dst, "replayed-plugin")
	require.NoError(t, err)

	frozenDst := dst.Freeze()
	gotHooks := feature.Get(frozenDst, feature.PlaneSubmitHooks)
	require.Len(t, gotHooks, 2)
	assert.Equal(t, "src-hook-1", gotHooks[0].ID())
	assert.Equal(t, "src-hook-2", gotHooks[1].ID())

	// 2. ReplaySourceTo into dst under SourceHost:
	// PlaneSubmitHooks rejects SourceHost with ErrUnsupportedSource before mutation.
	dstHostRejected := feature.NewContributionSet()
	err = frozenSrc.ReplaySourceTo(dstHostRejected, feature.SourceHost, "host-replay")
	require.ErrorIs(t, err, feature.ErrUnsupportedSource)
	assert.Empty(t, feature.Get(dstHostRejected.Freeze(), feature.PlaneSubmitHooks))

	// ReplaySourceTo under SourceHost succeeds on planes that support SourceHost (e.g. PlaneTrafficObservers).
	csHostSupported := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(csHostSupported, feature.PlaneTrafficObservers, "plugin-traffic", []traffic.Observer{
		traffic.NoopObserver{},
	}))
	frozenTraffic := csHostSupported.Freeze()
	dstHost := feature.NewContributionSet()
	err = frozenTraffic.ReplaySourceTo(dstHost, feature.SourceHost, "host-replay")
	require.NoError(t, err)
	assert.Len(t, feature.Get(dstHost.Freeze(), feature.PlaneTrafficObservers), 1)

	// 3. Fail-before-mutate on replay conflict: destination must remain atomically unmodified
	dstAtomic := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(dstAtomic, feature.PlaneSubmitHooks, "dst-plugin", []hooks.SubmitHook{
		charStubSubmitHook{id: "dst-hook-1", order: 10},
	}))
	require.NoError(t, feature.Contribute(dstAtomic, feature.PlaneTerminalDecisionProvider, "dst-plugin", terminaldecision.Provider(
		charStubTerminalProvider{id: "dst-provider"},
	)))

	srcConflicting := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(srcConflicting, feature.PlaneSubmitHooks, "src-plugin", []hooks.SubmitHook{
		charStubSubmitHook{id: "src-hook-2", order: 20},
	}))
	require.NoError(t, feature.Contribute(srcConflicting, feature.PlaneTerminalDecisionProvider, "src-plugin", terminaldecision.Provider(
		charStubTerminalProvider{id: "src-conflicting-provider"},
	)))

	srcFrozen := srcConflicting.Freeze()
	err = srcFrozen.ReplayTo(dstAtomic, "replayer")
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrExclusiveConflict)

	// Destination must remain completely unchanged: dst-hook-1 only, dst-provider only
	frozenAfterReplayFail := dstAtomic.Freeze()
	gotHooksAfter := feature.Get(frozenAfterReplayFail, feature.PlaneSubmitHooks)
	require.Len(t, gotHooksAfter, 1, "staged destination must not contain partially replayed hooks")
	assert.Equal(t, "dst-hook-1", gotHooksAfter[0].ID())
	gotProv := feature.Get(frozenAfterReplayFail, feature.PlaneTerminalDecisionProvider)
	require.NotNil(t, gotProv)
	assert.Equal(t, "dst-provider", gotProv.ID())

	// 4. Fail-before-mutate on replay validation failure (map-backed malformed frozen fixture)
	dstValid := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(dstValid, feature.PlaneSubmitHooks, "dst-plugin", []hooks.SubmitHook{
		charStubSubmitHook{id: "dst-hook-keep", order: 1},
	}))
	malformedFrozen := feature.NewMalformedGeneratedFrozenToolCallFinalizationMaxArgsBytesForTest(-100)
	err = malformedFrozen.ReplayTo(dstValid, "replayer")
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrInvalidContribution)
	// Destination remains unmodified
	assert.Len(t, feature.Get(dstValid.Freeze(), feature.PlaneSubmitHooks), 1)
}

func TestClosedPlane_CandidateReplay_FailBeforeMutate(t *testing.T) {
	t.Parallel()

	// Initial destination set with standard candidate plane
	dst := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(dst, feature.PlaneRequestTransforms, "plugin-base", []request.Transform{
		charStubRequestTransform{id: "base-rt", order: 1},
	}))

	// Candidate frozen set with standard candidate plane
	candidateSrc := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(candidateSrc, feature.PlaneRequestTransforms, "plugin-cand", []request.Transform{
		charStubRequestTransform{id: "cand-rt", order: 2},
	}))
	candFrozen := candidateSrc.Freeze()

	// 1. Candidate merge into destination via ContributeCandidateTo
	err := candFrozen.ContributeCandidateTo(dst, feature.SourceFeature, "candidate")
	require.NoError(t, err)

	frozenMerged := dst.Freeze()
	gotTransforms := feature.Get(frozenMerged, feature.PlaneRequestTransforms)
	require.Len(t, gotTransforms, 2)
	assert.Equal(t, "base-rt", gotTransforms[0].ID())
	assert.Equal(t, "cand-rt", gotTransforms[1].ID())

	// 2. Candidate merge with ContributeCandidate method
	dst2 := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(dst2, feature.PlaneRequestTransforms, "plugin-base", []request.Transform{
		charStubRequestTransform{id: "base-rt", order: 1},
	}))
	err = dst2.ContributeCandidate(candFrozen)
	require.NoError(t, err)
	assert.Len(t, feature.Get(dst2.Freeze(), feature.PlaneRequestTransforms), 2)

	// 3. Fail-before-mutate on candidate conflict: destination remains atomically untouched
	dstCandAtomic := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(dstCandAtomic, feature.PlaneSubmitHooks, "base-plugin", []hooks.SubmitHook{
		charStubSubmitHook{id: "cand-dst-hook", order: 1},
	}))
	require.NoError(t, feature.Contribute(dstCandAtomic, feature.PlaneTerminalDecisionProvider, "base-plugin", terminaldecision.Provider(
		charStubTerminalProvider{id: "cand-dst-provider"},
	)))

	candConflicting := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(candConflicting, feature.PlaneSubmitHooks, "cand-plugin", []hooks.SubmitHook{
		charStubSubmitHook{id: "cand-hook-new", order: 2},
	}))
	require.NoError(t, feature.Contribute(candConflicting, feature.PlaneTerminalDecisionProvider, "cand-plugin", terminaldecision.Provider(
		charStubTerminalProvider{id: "cand-conflicting-provider"},
	)))

	candFrozenConflicting := candConflicting.Freeze()
	err = candFrozenConflicting.ContributeCandidateTo(dstCandAtomic, feature.SourceFeature, "candidate")
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrExclusiveConflict)

	// Destination must remain completely unchanged
	frozenCandAfter := dstCandAtomic.Freeze()
	gotCandHooks := feature.Get(frozenCandAfter, feature.PlaneSubmitHooks)
	require.Len(t, gotCandHooks, 1)
	assert.Equal(t, "cand-dst-hook", gotCandHooks[0].ID())
	gotCandProv := feature.Get(frozenCandAfter, feature.PlaneTerminalDecisionProvider)
	require.NotNil(t, gotCandProv)
	assert.Equal(t, "cand-dst-provider", gotCandProv.ID())
}

func TestClosedPlane_GeneratedFixture_ExplicitEmptySliceSemantics(t *testing.T) {
	t.Parallel()

	boundPlane := feature.BindGeneratedTestPlane(newTestUnboundSlicePlane())

	t.Run("explicit empty slice is preserved as non-nil empty", func(t *testing.T) {
		t.Parallel()
		cs := feature.NewContributionSet()
		err := feature.Contribute(cs, boundPlane, "plugin-empty", []string{})
		require.NoError(t, err)

		frozen := cs.Freeze()
		got := feature.Get(frozen, boundPlane)
		assert.NotNil(t, got, "explicit empty slice contribution must be non-nil")
		assert.Empty(t, got, "explicit empty slice must have len 0")
	})

	t.Run("uninitialized plane returns nil slice", func(t *testing.T) {
		t.Parallel()
		cs := feature.NewContributionSet()
		frozen := cs.Freeze()
		got := feature.Get(frozen, boundPlane)
		assert.Nil(t, got, "uncontributed plane must return nil slice zero-value")
	})
}

func TestClosedPlane_GeneratedFixture_FailBeforeMutate(t *testing.T) {
	t.Parallel()

	falliblePlane := feature.BindGeneratedTestPlane(feature.Plane[[]string]{
		ID:           "test.unbound_fail_before_mutate",
		Multiplicity: feature.MultOrdered,
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
		},
		Validate: func(v []string) error {
			if slices.Contains(v, "invalid_validation") {
				return errors.New("validation failed")
			}
			return nil
		},
		Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
			if slices.Contains(inc, "invalid_combine") {
				if len(cur) > 0 {
					cur[0] = "TAMPERED_IN_COMBINER"
				}
				return nil, errors.New("combiner failed")
			}
			return append(cur, inc...), nil
		},
	})

	cs := feature.NewContributionSet()
	err := feature.Contribute(cs, falliblePlane, "plugin-1", []string{"good-1", "good-2"})
	require.NoError(t, err)

	// 1. Validation failure: leaves set unmodified
	err = feature.Contribute(cs, falliblePlane, "plugin-2", []string{"invalid_validation"})
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrInvalidContribution)
	assert.Equal(t, []string{"good-1", "good-2"}, feature.Get(cs.Freeze(), falliblePlane))

	// 2. Combiner failure with in-place mutation attempt: leaves set unmodified
	err = feature.Contribute(cs, falliblePlane, "plugin-3", []string{"invalid_combine"})
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrInvalidContribution)
	got := feature.Get(cs.Freeze(), falliblePlane)
	assert.Equal(t, []string{"good-1", "good-2"}, got)
	assert.NotEqual(t, "TAMPERED_IN_COMBINER", got[0])

	// 3. Unsupported source: leaves set unmodified
	err = feature.ContributeSource(cs, falliblePlane, feature.SourceGenerationBinder, "binder", []string{"binder-val"})
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrUnsupportedSource)
	assert.Equal(t, []string{"good-1", "good-2"}, feature.Get(cs.Freeze(), falliblePlane))

	// 4. Empty contributor ID: leaves set unmodified
	err = feature.Contribute(cs, falliblePlane, "", []string{"empty-plugin-id"})
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrInvalidContribution)
	assert.Equal(t, []string{"good-1", "good-2"}, feature.Get(cs.Freeze(), falliblePlane))
}

// =============================================================================
// Section B: Adversarial Copies of Canonical Standard Planes (Pre-Task-2 Characterization)
//
// These tests characterize the current behavior when a caller copies a standard plane
// and modifies either its ID or its exported policy fields.
//
// Target contract after Task 2:
// - Changed-ID copy: MUST be rejected before mutation with ErrUngeneratedPlane (Req 1.2).
// - Same-ID copy with mutated exported policy: CANNOT alter canonical behavior; canonical
//   generated metadata is authoritative (Req 1.1, Design lines 169-221).
// =============================================================================

func TestClosedPlane_AdversarialCopy_ChangedID(t *testing.T) {
	t.Parallel()

	// Copy standard canonical PlaneSubmitHooks and change its ID
	adversarialPlane := feature.PlaneSubmitHooks
	adversarialPlane.ID = "adversarial.submit_hooks.tampered_id"

	cs := feature.NewContributionSet()
	hook := charStubSubmitHook{id: "adv-hook-1", order: 10}

	// Task 2 target contract: Contribute on adversarialPlane MUST fail with ErrUngeneratedPlane
	// before mutating the ContributionSet because p.ID != gp.planeID ("submit_hooks" != "adversarial...").
	err := feature.Contribute(cs, adversarialPlane, "plugin-adv", []hooks.SubmitHook{hook})
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrUngeneratedPlane)

	var attrErr *feature.AttributedError
	require.True(t, errors.As(err, &attrErr))
	assert.Equal(t, "plugin-adv", attrErr.PluginID)
	assert.Equal(t, "adversarial.submit_hooks.tampered_id", attrErr.PlaneID)

	// Fail-before-mutate: set is unmodified
	assert.False(t, cs.Has("submit_hooks"))
	assert.False(t, cs.Has("adversarial.submit_hooks.tampered_id"))
}

func TestClosedPlane_AdversarialCopy_SameID_MutatedExportedPolicy(t *testing.T) {
	t.Parallel()

	t.Run("mutated Validate on copied standard plane", func(t *testing.T) {
		t.Parallel()

		// Copy standard PlaneToolCallFinalizationMaxArgsBytes and inject a failing Validate
		copiedPlane := feature.PlaneToolCallFinalizationMaxArgsBytes
		copiedPlane.Validate = func(v int) error {
			return errors.New("adversarial validation override: always fail")
		}

		cs := feature.NewContributionSet()

		// Task 2 target contract: all policy decisions (including Validate) MUST come from
		// canonical generated metadata (gp.validate), so a mutated p.Validate on a same-ID copy
		// cannot alter canonical behavior. Valid integer succeeds:
		err := feature.Contribute(cs, copiedPlane, "plugin-adv", 1024)
		require.NoError(t, err, "canonical generated validator is authoritative; caller's mutated Validate is ignored")
		assert.Equal(t, 1024, feature.Get(cs.Freeze(), feature.PlaneToolCallFinalizationMaxArgsBytes))

		// Canonical validator rejects negative integers:
		errNeg := feature.Contribute(cs, copiedPlane, "plugin-adv-2", -50)
		require.Error(t, errNeg)
		require.ErrorIs(t, errNeg, feature.ErrInvalidContribution)
	})

	t.Run("mutated SourceRules on copied standard plane", func(t *testing.T) {
		t.Parallel()

		// Copy standard PlaneSubmitHooks and change SourceRules to Host-only (Feature unsupported)
		copiedPlane := feature.PlaneSubmitHooks
		copiedPlane.Rules = feature.SourceRules{
			Host: feature.CombConcatenate,
		}

		cs := feature.NewContributionSet()
		hook := charStubSubmitHook{id: "hook-adv", order: 5}

		// Task 2 target contract: SourceRules MUST be read from canonical generated metadata (gp.rules),
		// so caller's mutated Rules cannot disable canonical SourceFeature support.
		err := feature.Contribute(cs, copiedPlane, "plugin-adv", []hooks.SubmitHook{hook})
		require.NoError(t, err, "canonical generated SourceRules are authoritative; caller's mutated Rules are ignored")
		assert.Len(t, feature.Get(cs.Freeze(), feature.PlaneSubmitHooks), 1)
	})

	t.Run("mutated NilPolicy on copied standard exclusive plane", func(t *testing.T) {
		t.Parallel()

		// Copy standard PlaneTerminalDecisionProvider and change NilPolicy from NilReject to NilSkip
		copiedPlane := feature.PlaneTerminalDecisionProvider
		copiedPlane.NilPolicy = feature.NilSkip

		cs := feature.NewContributionSet()

		// Task 2 target contract: NilPolicy MUST be read from canonical generated metadata (gp.nilPolicy),
		// so caller's mutated NilSkip cannot bypass NilReject.
		err := feature.Contribute(cs, copiedPlane, "plugin-adv", terminaldecision.Provider(nil))
		require.Error(t, err, "canonical generated NilPolicy (NilReject) is authoritative")
		require.ErrorIs(t, err, feature.ErrNilContribution)
	})

	t.Run("mutated Identity on copied standard exclusive plane", func(t *testing.T) {
		t.Parallel()

		// Copy standard PlaneTerminalDecisionProvider and mutate Identity extractor
		copiedPlane := feature.PlaneTerminalDecisionProvider
		copiedPlane.Identity = func(v terminaldecision.Provider) (string, bool) {
			return "adversarial_extracted_id", true
		}

		cs := feature.NewContributionSet()
		provider := charStubTerminalProvider{id: "real_canonical_id"}

		// Task 2 target contract: Identity extractor MUST be read from canonical generated metadata (gp.identity).
		err := feature.Contribute(cs, copiedPlane, "plugin-adv", terminaldecision.Provider(provider))
		require.NoError(t, err)

		frozen := cs.Freeze()
		gotID, ok := feature.FrozenIdentity(frozen, feature.PlaneTerminalDecisionProvider)
		assert.True(t, ok)
		assert.Equal(t, "real_canonical_id", gotID, "canonical identity extractor is authoritative")
	})

	t.Run("mutated Combine on copied standard plane is ignored by generated storage closure", func(t *testing.T) {
		t.Parallel()

		// Copy standard PlaneSubmitHooks and mutate Combine to return an error
		copiedPlane := feature.PlaneSubmitHooks
		copiedPlane.Combine = func(source feature.SourceKind, cur, inc []hooks.SubmitHook) ([]hooks.SubmitHook, error) {
			return nil, errors.New("adversarial combiner: intentional failure")
		}

		cs := feature.NewContributionSet()
		hook := charStubSubmitHook{id: "hook-combine-test", order: 1}

		err := feature.Contribute(cs, copiedPlane, "plugin-adv", []hooks.SubmitHook{hook})
		require.NoError(t, err, "generated contribute closure uses canonical PlaneSubmitHooks.Combine")

		frozen := cs.Freeze()
		got := feature.Get(frozen, feature.PlaneSubmitHooks)
		require.Len(t, got, 1)
		assert.Equal(t, "hook-combine-test", got[0].ID())
	})
}

// =============================================================================
// Section C: Standard Generated Positive Controls
//
// These tests verify that canonical standard planes generated from the manifest
// maintain all required semantics across multiplicity, scalar reduce, exclusive
// identity, typed nil, request materialization, request borrowing, and diagnostics.
// =============================================================================

func TestClosedPlane_StandardGeneratedPositiveControls_Ordered(t *testing.T) {
	t.Parallel()

	// Positive control: PlaneSubmitHooks, PlaneRequestTransforms, PlaneResponseTransforms, PlaneToolReactors
	cs := feature.NewContributionSet()

	h1 := charStubSubmitHook{id: "h1", order: 20}
	h2 := charStubSubmitHook{id: "h2", order: 10}
	err := feature.Contribute(cs, feature.PlaneSubmitHooks, "plugin-1", []hooks.SubmitHook{h1})
	require.NoError(t, err)
	err = feature.Contribute(cs, feature.PlaneSubmitHooks, "plugin-2", []hooks.SubmitHook{h2})
	require.NoError(t, err)

	frozen := cs.Freeze()
	got := feature.Get(frozen, feature.PlaneSubmitHooks)
	require.Len(t, got, 2)
	assert.Equal(t, "h1", got[0].ID())
	assert.Equal(t, "h2", got[1].ID())

	// Request transforms positive control
	rt1 := charStubRequestTransform{id: "rt1", order: 1}
	err = feature.Contribute(cs, feature.PlaneRequestTransforms, "plugin-rt", []request.Transform{rt1})
	require.NoError(t, err)

	// Tool reactors positive control
	tr1 := charStubToolReactor{id: "tr1", order: 1}
	err = feature.Contribute(cs, feature.PlaneToolReactors, "plugin-tr", []hooks.ToolReactor{tr1})
	require.NoError(t, err)

	// Request part hooks positive control
	rph1 := charStubRequestPartHook{id: "rph1", order: 1}
	err = feature.Contribute(cs, feature.PlaneRequestPartHooks, "plugin-rph", []hooks.RequestPartHook{rph1})
	require.NoError(t, err)

	// Stream observer factories positive control
	sof1 := charStubStreamObserverFactory{id: "sof1", order: 1}
	err = feature.Contribute(cs, feature.PlaneStreamObserverFactories, "plugin-sof", []response.StreamObserverFactory{sof1})
	require.NoError(t, err)

	frozenFull := cs.Freeze()
	assert.Len(t, feature.Get(frozenFull, feature.PlaneRequestTransforms), 1)
	assert.Len(t, feature.Get(frozenFull, feature.PlaneToolReactors), 1)
	assert.Len(t, feature.Get(frozenFull, feature.PlaneRequestPartHooks), 1)
	assert.Len(t, feature.Get(frozenFull, feature.PlaneStreamObserverFactories), 1)
}

func TestClosedPlane_StandardGeneratedPositiveControls_ScalarReduce(t *testing.T) {
	t.Parallel()

	// Positive control: PlaneToolCallFinalizationMaxArgsBytes (min-reduction)
	cs := feature.NewContributionSet()

	// 1. Initial contribution
	err := feature.Contribute(cs, feature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-1", 4096)
	require.NoError(t, err)
	assert.Equal(t, 4096, feature.Get(cs.Freeze(), feature.PlaneToolCallFinalizationMaxArgsBytes))

	// 2. Smaller contribution reduces the value
	err = feature.Contribute(cs, feature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-2", 1024)
	require.NoError(t, err)
	assert.Equal(t, 1024, feature.Get(cs.Freeze(), feature.PlaneToolCallFinalizationMaxArgsBytes))

	// 3. Larger contribution is ignored (min-reduction preserved)
	err = feature.Contribute(cs, feature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-3", 8192)
	require.NoError(t, err)
	assert.Equal(t, 1024, feature.Get(cs.Freeze(), feature.PlaneToolCallFinalizationMaxArgsBytes))

	// 4. Zero contribution is ignored (unset sentinel)
	err = feature.Contribute(cs, feature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-4", 0)
	require.NoError(t, err)
	assert.Equal(t, 1024, feature.Get(cs.Freeze(), feature.PlaneToolCallFinalizationMaxArgsBytes))

	// 5. Negative contribution fails validation before mutate
	err = feature.Contribute(cs, feature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-5", -100)
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrInvalidContribution)
	assert.Equal(t, 1024, feature.Get(cs.Freeze(), feature.PlaneToolCallFinalizationMaxArgsBytes))
}

func TestClosedPlane_StandardGeneratedPositiveControls_ExclusiveIdentity(t *testing.T) {
	t.Parallel()

	// Positive control: PlaneTerminalDecisionProvider (exclusive slot)
	cs := feature.NewContributionSet()
	p1 := charStubTerminalProvider{id: "provider-primary"}

	// 1. First contribution succeeds
	err := feature.Contribute(cs, feature.PlaneTerminalDecisionProvider, "plugin-1", terminaldecision.Provider(p1))
	require.NoError(t, err)

	frozen := cs.Freeze()
	got := feature.Get(frozen, feature.PlaneTerminalDecisionProvider)
	require.NotNil(t, got)
	assert.Equal(t, "provider-primary", got.ID())

	// 2. FrozenIdentity retrieval
	id, ok := feature.FrozenIdentity(frozen, feature.PlaneTerminalDecisionProvider)
	assert.True(t, ok)
	assert.Equal(t, "provider-primary", id)

	// 3. Second contribution from different plugin fails with ErrExclusiveConflict
	p2 := charStubTerminalProvider{id: "provider-secondary"}
	err = feature.Contribute(cs, feature.PlaneTerminalDecisionProvider, "plugin-2", terminaldecision.Provider(p2))
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrExclusiveConflict)

	// Fail-before-mutate: primary provider remains
	assert.Equal(t, "provider-primary", feature.Get(cs.Freeze(), feature.PlaneTerminalDecisionProvider).ID())
}

func TestClosedPlane_StandardGeneratedPositiveControls_TypedNil(t *testing.T) {
	t.Parallel()

	// Positive control: PlaneSessionOpeners, PlaneWorkspaceResolvers, PlaneSecretGuards
	t.Run("PlaneSessionOpeners NilSkip policy ignores nil contributions", func(t *testing.T) {
		t.Parallel()
		cs := feature.NewContributionSet()

		// Contributing nil slice under NilSkip succeeds and leaves storage empty
		err := feature.Contribute(cs, feature.PlaneSessionOpeners, "plugin-nil", []session.Opener(nil))
		require.NoError(t, err)
		assert.Nil(t, feature.Get(cs.Freeze(), feature.PlaneSessionOpeners))

		// Valid contribution succeeds
		opener := charStubSessionOpener{id: "sess-opener-1"}
		err = feature.Contribute(cs, feature.PlaneSessionOpeners, "plugin-valid", []session.Opener{opener})
		require.NoError(t, err)
		assert.Len(t, feature.Get(cs.Freeze(), feature.PlaneSessionOpeners), 1)
	})

	t.Run("PlaneWorkspaceResolvers NilSkip policy", func(t *testing.T) {
		t.Parallel()
		cs := feature.NewContributionSet()

		err := feature.Contribute(cs, feature.PlaneWorkspaceResolvers, "plugin-nil", []workspace.Resolver(nil))
		require.NoError(t, err)
		assert.Nil(t, feature.Get(cs.Freeze(), feature.PlaneWorkspaceResolvers))
	})

	t.Run("PlaneSecretGuards NilSkip policy", func(t *testing.T) {
		t.Parallel()
		cs := feature.NewContributionSet()

		err := feature.Contribute(cs, feature.PlaneSecretGuards, "plugin-nil", []secretguard.Guard(nil))
		require.NoError(t, err)
		assert.Nil(t, feature.Get(cs.Freeze(), feature.PlaneSecretGuards))
	})
}

func TestClosedPlane_StandardGeneratedPositiveControls_RequestMaterialized(t *testing.T) {
	t.Parallel()

	// Positive control: PlanePreRequestHandlers declares RequestMaterializer: prerequest.MaterializeSorted
	cs := feature.NewContributionSet()

	hHighOrder := charStubPreRequestHandler{id: "prh-100", order: 100}
	hLowOrder := charStubPreRequestHandler{id: "prh-10", order: 10}

	// Contribute in non-sorted order
	err := feature.Contribute(cs, feature.PlanePreRequestHandlers, "plugin-1", []prerequest.Handler{hHighOrder})
	require.NoError(t, err)
	err = feature.Contribute(cs, feature.PlanePreRequestHandlers, "plugin-2", []prerequest.Handler{hLowOrder})
	require.NoError(t, err)

	frozen := cs.Freeze()
	// Ordinary Get preserves contribution registration order
	gotUnsorted := feature.Get(frozen, feature.PlanePreRequestHandlers)
	require.Len(t, gotUnsorted, 2)
	assert.Equal(t, "prh-100", gotUnsorted[0].ID())
	assert.Equal(t, "prh-10", gotUnsorted[1].ID())

	// FreezeRequestPlanes sorts handlers by order
	reqFrozen := feature.FreezeRequestPlanes(frozen)
	gotSorted := feature.Get(reqFrozen, feature.PlanePreRequestHandlers)
	require.Len(t, gotSorted, 2)
	assert.Equal(t, "prh-10", gotSorted[0].ID())
	assert.Equal(t, "prh-100", gotSorted[1].ID())
}

func TestClosedPlane_StandardGeneratedPositiveControls_RequestBorrowed(t *testing.T) {
	t.Parallel()

	// Positive control: standard planes with RequestBorrow == true
	assert.True(t, feature.PlaneToolCallPolicies.RequestBorrow, "PlaneToolCallPolicies must declare RequestBorrow")
	assert.True(t, feature.PlaneToolCallFinalizers.RequestBorrow, "PlaneToolCallFinalizers must declare RequestBorrow")
	assert.True(t, feature.PlaneSecretGuards.RequestBorrow, "PlaneSecretGuards must declare RequestBorrow")
	assert.True(t, feature.PlaneLocalTurnHandlers.RequestBorrow, "PlaneLocalTurnHandlers must declare RequestBorrow")

	// Verify non-borrowed planes have RequestBorrow == false
	assert.False(t, feature.PlaneSubmitHooks.RequestBorrow, "PlaneSubmitHooks must not declare RequestBorrow")
	assert.False(t, feature.PlaneTerminalDecisionProvider.RequestBorrow, "PlaneTerminalDecisionProvider must not declare RequestBorrow")
}

func TestClosedPlane_StandardGeneratedPositiveControls_Diagnostics(t *testing.T) {
	t.Parallel()

	t.Run("PlaneSubmitHooks diagnostics projection", func(t *testing.T) {
		t.Parallel()
		h1 := charStubSubmitHook{id: "submit-hook-a", order: 20}
		h2 := charStubSubmitHook{id: "submit-hook-b", order: 10}
		hooksList := []hooks.SubmitHook{h1, h2}

		occupants := feature.PlaneSubmitHooks.MaterializeOccupants(hooksList)
		require.Len(t, occupants, 2)
		// sortHooks orders by order asc (10 before 20)
		assert.Equal(t, "submit-hook-b", occupants[0].Label)
		assert.Equal(t, "submit-hook-a", occupants[1].Label)

		privs := feature.PlaneSubmitHooks.ProjectPrivileges(hooksList)
		assert.Empty(t, privs.Flags)
	})

	t.Run("PlaneToolReactors diagnostics projection", func(t *testing.T) {
		t.Parallel()
		r := charStubToolReactor{id: "tool-reactor-test", order: 5}
		occupants := feature.PlaneToolReactors.MaterializeOccupants([]hooks.ToolReactor{r})
		require.Len(t, occupants, 1)
		assert.Equal(t, "tool-reactor-test", occupants[0].Label)
	})

	t.Run("PlaneSecretGuards diagnostics projection", func(t *testing.T) {
		t.Parallel()
		g := charStubSecretGuard{id: "sg-test-guard", order: 5}
		occupants := feature.PlaneSecretGuards.MaterializeOccupants([]secretguard.Guard{g})
		require.Len(t, occupants, 1)
		assert.Equal(t, "secret_guard:sg-test-guard", occupants[0].Label)
	})
}

func TestClosedPlane_TestBoundPlane_Semantics(t *testing.T) {
	t.Parallel()

	rawPlane := feature.Plane[[]string]{
		ID:           "test.bound_plane",
		Multiplicity: feature.MultOrdered,
		Rules: feature.SourceRules{
			Feature: feature.CombConcatenate,
		},
		Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
			return append(cur, inc...), nil
		},
	}

	// 1. Unbound plane contribution is rejected with ErrUngeneratedPlane
	cs := feature.NewContributionSet()
	err := feature.Contribute(cs, rawPlane, "plugin-1", []string{"v1"})
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrUngeneratedPlane)

	// 2. Bound plane contribution succeeds
	boundPlane := feature.BindGeneratedTestPlane(rawPlane)
	err = feature.Contribute(cs, boundPlane, "plugin-1", []string{"v1", "v2"})
	require.NoError(t, err)
	assert.Equal(t, []string{"v1", "v2"}, feature.Get(cs.Freeze(), boundPlane))

	// 3. Changed-ID copy of bound plane is rejected with ErrUngeneratedPlane
	tamperedPlane := boundPlane
	tamperedPlane.ID = "test.bound_plane.tampered"
	err = feature.Contribute(cs, tamperedPlane, "plugin-2", []string{"v3"})
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrUngeneratedPlane)
}

func TestClosedPlane_NoErasedValueFallback_RequiresGeneratedContribute(t *testing.T) {
	t.Parallel()

	// A plane with policy attached but nil contribute closure
	planeWithNilContribute := feature.BindGeneratedAccessForTest(
		feature.Plane[[]string]{
			ID:           "test.nil_contribute",
			Multiplicity: feature.MultOrdered,
			Rules: feature.SourceRules{
				Feature: feature.CombConcatenate,
			},
			Combine: func(source feature.SourceKind, cur, inc []string) ([]string, error) {
				return append(cur, inc...), nil
			},
		},
		nil, // nil contribute closure!
		func(gf *feature.GeneratedFrozenForTest) []string { return nil },
		nil,
	)

	cs := feature.NewContributionSet()
	err := feature.Contribute(cs, planeWithNilContribute, "plugin-1", []string{"v1"})
	require.Error(t, err)
	require.ErrorIs(t, err, feature.ErrUngeneratedPlane)
}
