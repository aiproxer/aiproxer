package feature

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

type regressSubmitHook struct {
	id  string
	ord int
}

func (h regressSubmitHook) ID() string                     { return h.id }
func (h regressSubmitHook) Order() int                     { return h.ord }
func (h regressSubmitHook) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (h regressSubmitHook) Handle(context.Context, *aipapi.Call, *hooks.SubmitMeta) (hooks.SubmitDecision, error) {
	return hooks.SubmitDecision{}, nil
}

type regressTerminalProvider struct {
	id string
}

func (p regressTerminalProvider) ID() string { return p.id }
func (p regressTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{}, nil
}

type regressAttemptTransform struct {
	id  string
	ord int
}

func (t regressAttemptTransform) ID() string                     { return t.id }
func (t regressAttemptTransform) Order() int                     { return t.ord }
func (t regressAttemptTransform) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (t regressAttemptTransform) HandleAttempt(context.Context, *aipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{}, nil
}

type regressSessionOpener struct {
	id string
}

func (o regressSessionOpener) ID() string { return o.id }
func (regressSessionOpener) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

type regressPreRequestHandler struct {
	id  string
	ord int
}

func (h regressPreRequestHandler) ID() string                     { return h.id }
func (h regressPreRequestHandler) Order() int                     { return h.ord }
func (h regressPreRequestHandler) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (h regressPreRequestHandler) Handle(context.Context, *aipapi.Call, prerequest.Meta, prerequest.Services) (prerequest.Decision, error) {
	return prerequest.Decision{}, nil
}

func TestCanonicalPlanePolicy_ChangedID_DirectGetAndFrozenIdentity(t *testing.T) {
	t.Parallel()

	cs := NewContributionSet()
	require.NoError(t, Contribute(cs, PlaneSubmitHooks, "plugin-1", []hooks.SubmitHook{
		regressSubmitHook{id: "hook-1", ord: 1},
	}))
	require.NoError(t, Contribute(cs, PlaneTerminalDecisionProvider, "plugin-2", terminaldecision.Provider(regressTerminalProvider{id: "term-1"})))
	require.NoError(t, Contribute(cs, PlaneAttemptTransforms, "plugin-3", []request.AttemptTransform{
		regressAttemptTransform{id: "attempt-1", ord: 1},
	}))

	frozen := cs.Freeze()

	// Normal reads succeed
	assert.Len(t, Get(frozen, PlaneSubmitHooks), 1)
	assert.NotNil(t, Get(frozen, PlaneTerminalDecisionProvider))
	assert.Equal(t, "term-1", Get(frozen, PlaneTerminalDecisionProvider).ID())
	termID, ok := FrozenIdentity(frozen, PlaneTerminalDecisionProvider)
	assert.True(t, ok)
	assert.Equal(t, "term-1", termID)
	attemptID, ok := FrozenIdentity(frozen, PlaneAttemptTransforms)
	assert.True(t, ok)
	assert.Equal(t, "attempt-1", attemptID)

	// Changed-ID copies MUST return zero values and false
	mutSubmit := PlaneSubmitHooks
	mutSubmit.ID = "tampered_submit_hooks"
	assert.Nil(t, Get(frozen, mutSubmit), "Get on changed-ID plane copy must return nil")

	mutTerm := PlaneTerminalDecisionProvider
	mutTerm.ID = "tampered_terminal_decision_provider"
	assert.Nil(t, Get(frozen, mutTerm), "Get on changed-ID exclusive plane copy must return nil")
	id, ok := FrozenIdentity(frozen, mutTerm)
	assert.False(t, ok, "FrozenIdentity on changed-ID exclusive plane must return false")
	assert.Empty(t, id)

	mutAttempt := PlaneAttemptTransforms
	mutAttempt.ID = "tampered_attempt_transforms"
	assert.Nil(t, Get(frozen, mutAttempt), "Get on changed-ID replace-by-identity plane copy must return nil")
	id, ok = FrozenIdentity(frozen, mutAttempt)
	assert.False(t, ok, "FrozenIdentity on changed-ID replace-by-identity plane must return false")
	assert.Empty(t, id)

	// Nil policy plane
	nilPolicyPlane := PlaneSubmitHooks
	nilPolicyPlane.generated.policy = nil
	assert.Nil(t, Get(frozen, nilPolicyPlane), "Get on nil policy plane must return nil")
	id, ok = FrozenIdentity(frozen, nilPolicyPlane)
	assert.False(t, ok, "FrozenIdentity on nil policy plane must return false")
	assert.Empty(t, id)

	// Unbound plane
	unbound := Plane[[]hooks.SubmitHook]{ID: "unbound_plane"}
	assert.Nil(t, Get(frozen, unbound))
	id, ok = FrozenIdentity(frozen, unbound)
	assert.False(t, ok)
	assert.Empty(t, id)
}

func TestCanonicalPlanePolicy_GlobalPlaneXMutation_ChildProcess(t *testing.T) {
	if os.Getenv("GO_WANT_PLANE_MUTATION_HELPER") == "1" {
		runGlobalPlaneXMutationSubprocess(t)
		return
	}

	t.Parallel()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCanonicalPlanePolicy_GlobalPlaneXMutation_ChildProcess$", "-test.v")
	cmd.Env = append(os.Environ(), "GO_WANT_PLANE_MUTATION_HELPER=1")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "child process failed:\n%s", string(out))
	assert.Contains(t, string(out), "PASS")
}

func runGlobalPlaneXMutationSubprocess(t *testing.T) {
	t.Helper()

	// Ensure initialization captured canonical policies before any mutations occur
	require.NotNil(t, PlaneSubmitHooks.generated.policy, "initialization must capture canonical policy before mutations")
	require.Equal(t, "submit_hooks", PlaneSubmitHooks.generated.policy.planeID)
	require.NotNil(t, PlaneAttemptTransforms.generated.policy)
	require.Equal(t, "attempt_transforms", PlaneAttemptTransforms.generated.policy.planeID)
	require.NotNil(t, PlaneTerminalDecisionProvider.generated.policy)
	require.Equal(t, "terminal_decision_provider", PlaneTerminalDecisionProvider.generated.policy.planeID)
	require.NotNil(t, PlaneSessionOpeners.generated.policy)
	require.Equal(t, "session_openers", PlaneSessionOpeners.generated.policy.planeID)
	require.NotNil(t, PlanePreRequestHandlers.generated.policy)
	require.Equal(t, "pre_request_handlers", PlanePreRequestHandlers.generated.policy.planeID)
	require.NotNil(t, PlaneToolCallFinalizationMaxArgsBytes.generated.policy)
	require.Equal(t, "tool_call_finalization_max_args_bytes", PlaneToolCallFinalizationMaxArgsBytes.generated.policy.planeID)

	// Save original globals and reliably restore in child process
	origSubmitHooks := PlaneSubmitHooks
	origAttemptTransforms := PlaneAttemptTransforms
	origTerminalProvider := PlaneTerminalDecisionProvider
	origSessionOpeners := PlaneSessionOpeners
	origPreRequestHandlers := PlanePreRequestHandlers
	origMaxArgsBytes := PlaneToolCallFinalizationMaxArgsBytes
	t.Cleanup(func() {
		PlaneSubmitHooks = origSubmitHooks
		PlaneAttemptTransforms = origAttemptTransforms
		PlaneTerminalDecisionProvider = origTerminalProvider
		PlaneSessionOpeners = origSessionOpeners
		PlanePreRequestHandlers = origPreRequestHandlers
		PlaneToolCallFinalizationMaxArgsBytes = origMaxArgsBytes
	})

	// 1. Global Combiner
	PlaneSubmitHooks.Combine = func(source SourceKind, current, incoming []hooks.SubmitHook) ([]hooks.SubmitHook, error) {
		return nil, errors.New("mutated global PlaneSubmitHooks combiner")
	}

	// 2. Global Validator (positive and inverse mutations)
	PlaneSubmitHooks.Validate = func(v []hooks.SubmitHook) error {
		return errors.New("mutated global PlaneSubmitHooks validator")
	}
	PlaneToolCallFinalizationMaxArgsBytes.Validate = func(v int) error {
		return nil // mutated to allow malformed negative integers
	}

	// 3. Global RequestMaterializer
	PlaneSessionOpeners.RequestMaterializer = func(v []session.Opener) []session.Opener {
		return []session.Opener{}
	}
	PlanePreRequestHandlers.RequestMaterializer = func(v []prerequest.Handler) []prerequest.Handler {
		return []prerequest.Handler{regressPreRequestHandler{id: "mutated-materializer", ord: 999}}
	}

	// 4. Replay & Candidate Combiner/Rules
	PlaneAttemptTransforms.Combine = func(source SourceKind, current, incoming []request.AttemptTransform) ([]request.AttemptTransform, error) {
		return nil, errors.New("mutated global PlaneAttemptTransforms combiner")
	}
	PlaneAttemptTransforms.Rules.Feature = CombUnsupported
	PlaneAttemptTransforms.Rules.GenerationBinder = CombUnsupported

	// 5. Terminal Decision Provider validate identity & conflict error
	PlaneTerminalDecisionProvider.ValidateIdentity = func(string) error {
		return nil // mutated to accept invalid identifiers
	}
	PlaneTerminalDecisionProvider.ExclusiveConflictError = errors.New("mutated conflict error")

	// 6. Diagnostics metadata
	PlaneSubmitHooks.Diagnostics = DiagnosticDescriptor[[]hooks.SubmitHook]{
		StageID:       "mutated_stage_id",
		CoalesceGroup: "mutated_group",
		Order:         99999,
		Materialize: func(v []hooks.SubmitHook) []DiagnosticOccupant {
			return []DiagnosticOccupant{{Label: "mutated_occupant"}}
		},
		Privileges: func(v []hooks.SubmitHook) PrivilegeProjection {
			return PrivilegeProjection{Flags: []string{"mutated_flag"}}
		},
	}

	// Now run verification asserting that all canonical generated paths are immune to the mutations above:
	csInitial := NewContributionSet()
	require.NoError(t, csInitial.BindAttemptTransforms("binder-1", []request.AttemptTransform{
		regressAttemptTransform{id: "at-1", ord: 1},
	}))
	require.NoError(t, Contribute(csInitial, PlaneSubmitHooks, "plugin-submit", []hooks.SubmitHook{
		regressSubmitHook{id: "sub-1", ord: 1},
	}), "Contribute must use canonical combiner and validator, ignoring mutated global PlaneSubmitHooks")
	require.NoError(t, Contribute(csInitial, PlaneTerminalDecisionProvider, "plugin-term", terminaldecision.Provider(regressTerminalProvider{id: "term-1"})))
	require.NoError(t, Contribute(csInitial, PlaneSessionOpeners, "plugin-session", []session.Opener{
		regressSessionOpener{id: "op-1"},
	}))
	require.NoError(t, Contribute(csInitial, PlanePreRequestHandlers, "plugin-pre", []prerequest.Handler{
		regressPreRequestHandler{id: "handler-late", ord: 50},
		regressPreRequestHandler{id: "handler-early", ord: 10},
	}))

	// Inverse validator test 1: Contributing malformed value must fail despite mutated permissive global validator
	errInvalidMax := Contribute(csInitial, PlaneToolCallFinalizationMaxArgsBytes, "plugin-max", -10)
	assert.Error(t, errInvalidMax, "Contribute must enforce canonical validator rejecting negative max args bytes despite mutated global validator")

	// Inverse validator test 2: Malformed state fixture validation must fail using test exports
	malformedMaxFrozen := NewMalformedGeneratedFrozenToolCallFinalizationMaxArgsBytesForTest(-5)
	assert.Error(t, malformedMaxFrozen.Validate(), "Validate must reject malformed max args bytes fixture")

	// Inverse validator test 3: Malformed terminal decision fixture with missing cached identity must fail
	malformedTermMissingID := NewMalformedGeneratedFrozenTerminalDecisionMissingIdentityForTest(regressTerminalProvider{id: "term-1"})
	assert.Error(t, malformedTermMissingID.Validate(), "Validate must reject exclusive plane missing cached identity")

	// Inverse validator test 4: Malformed terminal decision fixture with invalid blank identity must fail canonical ValidateIdentity
	malformedTermBadID := NewMalformedGeneratedFrozenTerminalDecisionForTest(regressTerminalProvider{id: "   "}, "   ", true)
	assert.Error(t, malformedTermBadID.Validate(), "Validate must enforce canonical ValidateIdentity rejecting blank identity despite mutated global ValidateIdentity")

	frozen := csInitial.Freeze()

	// Same-ID policy-mutated reads
	termVal := Get(frozen, PlaneTerminalDecisionProvider)
	require.NotNil(t, termVal)
	assert.Equal(t, "term-1", termVal.ID())
	termID, ok := FrozenIdentity(frozen, PlaneTerminalDecisionProvider)
	assert.True(t, ok)
	assert.Equal(t, "term-1", termID)

	// Direct changed-ID Get: mutate ID on copy and verify zero value
	changedIDSubmit := PlaneSubmitHooks
	changedIDSubmit.ID = "mutated_submit_hooks_id"
	assert.Nil(t, Get(frozen, changedIDSubmit), "Get with changed ID must return zero value")

	// FreezeRequestPlanes must use canonical RequestMaterializer with meaningful sorting (prerequest.MaterializeSorted)
	reqFrozen := FreezeRequestPlanes(frozen)
	reqOpeners := Get(reqFrozen, PlaneSessionOpeners)
	assert.Len(t, reqOpeners, 1, "FreezeRequestPlanes must use canonical materializer on PlaneSessionOpeners")

	reqHandlers := Get(reqFrozen, PlanePreRequestHandlers)
	require.Len(t, reqHandlers, 2, "FreezeRequestPlanes must execute canonical request materializer on PlanePreRequestHandlers")
	assert.Equal(t, "handler-early", reqHandlers[0].ID(), "canonical request materializer must sort handlers by Order ascending")
	assert.Equal(t, "handler-late", reqHandlers[1].ID(), "canonical request materializer must sort handlers by Order ascending")

	// Candidate replay
	candCS := NewContributionSet()
	require.NoError(t, Contribute(candCS, PlaneSessionOpeners, "cand-session", []session.Opener{
		regressSessionOpener{id: "op-cand"},
	}))
	candFrozen := candCS.Freeze()
	dstCS := csInitial.Clone()
	err := candFrozen.ContributeCandidateTo(dstCS, SourceFeature, "candidate")
	assert.NoError(t, err, "ContributeCandidateTo must succeed using canonical policy")

	// Replay under SourceFeature succeeds using canonical policy
	replayDst := NewContributionSet()
	err = frozen.ReplaySourceTo(replayDst, SourceFeature, "replayer")
	assert.NoError(t, err, "ReplaySourceTo must succeed under SourceFeature using canonical policy")

	// Replay under SourceGenerationBinder: PlaneAttemptTransforms has canonical CombReplaceByIdentity for GenerationBinder.
	// Even though global PlaneAttemptTransforms.Rules.GenerationBinder was mutated to CombUnsupported,
	// canonical policy enforcement still detects CombReplaceByIdentity and returns ErrUnsupportedReplaySource.
	err = frozen.ReplaySourceTo(replayDst, SourceGenerationBinder, "replayer")
	assert.ErrorIs(t, err, ErrUnsupportedReplaySource, "ReplaySourceTo must enforce canonical CombReplaceByIdentity rule despite mutated global rules")

	// hasIdentityReplayRule directly on frozen.frozen
	rulePlaneID, hasRule := frozen.frozen.hasIdentityReplayRule(SourceGenerationBinder, CombReplaceByIdentity)
	assert.True(t, hasRule, "hasIdentityReplayRule must use canonical rules, not mutated global rules")
	assert.Equal(t, "attempt_transforms", rulePlaneID)

	// Now mutate global PlaneX IDs to assert that hook projection and diagnostics retain canonical state
	PlaneSubmitHooks.ID = "mutated_global_submit_hooks_id"
	PlaneAttemptTransforms.ID = "mutated_global_attempt_transforms_id"
	PlaneTerminalDecisionProvider.ID = "mutated_global_terminal_decision_id"
	PlaneSessionOpeners.ID = "mutated_global_session_openers_id"
	PlanePreRequestHandlers.ID = "mutated_global_prerequest_handlers_id"
	PlaneToolCallFinalizationMaxArgsBytes.ID = "mutated_global_max_args_id"

	// Ordinary Get on globally mutated plane returns zero value (policy-ID mismatch)
	assert.Nil(t, Get(frozen, PlaneSubmitHooks), "Get with globally mutated PlaneSubmitHooks.ID must return zero value")

	// Hook projection directly retains hooks even when PlaneSubmitHooks.ID is globally mutated
	hookCfg := ProjectHookConfig(frozen, hooks.ToolReactorErrorsFailClosed)
	require.Len(t, hookCfg.SubmitHooks, 1, "ProjectHookConfig must retain submit hooks even under global PlaneSubmitHooks.ID mutation")
	assert.Equal(t, "sub-1", hookCfg.SubmitHooks[0].ID())

	// Diagnostics retain canonical plane ID and diagnostics metadata under global PlaneSubmitHooks.ID mutation
	projections := ProjectDiagnostics(frozen)
	foundSubmitHooks := false
	for _, proj := range projections {
		if proj.PlaneID == "submit_hooks" {
			foundSubmitHooks = true
			assert.Equal(t, StageIDSubmit, proj.StageID)
			assert.Equal(t, 10, proj.Order)
			require.Len(t, proj.Occupants, 1)
			assert.Equal(t, "sub-1", proj.Occupants[0].Label)
		}
		assert.NotEqual(t, "mutated_global_submit_hooks_id", proj.PlaneID, "diagnostics must retain canonical plane ID, not mutated global PlaneSubmitHooks.ID")
		assert.NotEqual(t, "mutated_stage_id", proj.StageID)
		assert.NotEqual(t, 99999, proj.Order)
		for _, occ := range proj.Occupants {
			assert.NotEqual(t, "mutated_occupant", occ.Label)
		}
	}
	assert.True(t, foundSubmitHooks, "must find submit_hooks projection in diagnostics with canonical plane ID")
}
