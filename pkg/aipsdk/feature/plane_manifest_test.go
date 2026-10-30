package feature_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

// TestStandardPlanes_ManifestCompletenessAndValidation tests that the hand-authored
// plane manifest contains all 28 standard planes in stable ordinal order,
// and that all declarations pass ValidateDeclaration and ValidateManifest without error.
func TestStandardPlanes_ManifestCompletenessAndValidation(t *testing.T) {
	t.Parallel()

	require.Len(t, feature.StandardPlanes, 28, "manifest must declare exactly 28 standard planes")

	// Validate the entire manifest
	err := feature.ValidateManifest(feature.StandardPlanes...)
	require.NoError(t, err, "StandardPlanes manifest must pass ValidateManifest")

	seenIDs := make(map[string]bool, len(feature.StandardPlanes))
	for _, p := range feature.StandardPlanes {
		assert.False(t, seenIDs[p.PlaneID()], "duplicate plane ID in manifest: %s", p.PlaneID())
		seenIDs[p.PlaneID()] = true
	}

	expectedPlanes := []struct {
		id           string
		multiplicity feature.Multiplicity
		featComb     feature.Combination
		hasDiagStage bool
	}{
		{id: "submit_hooks", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "request_part_hooks", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "response_part_hooks", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "tool_reactors", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "session_openers", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "workspace_resolvers", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "tool_catalog_filters", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "tool_call_policies", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "tool_call_finalizers", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "tool_call_finalization_max_args_bytes", multiplicity: feature.MultOrdered, featComb: feature.CombReduce, hasDiagStage: false},
		{id: "request_transforms", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "pre_request_handlers", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "route_hint_providers", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "completion_gates", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "attempt_transforms", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "stream_observer_factories", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "traffic_observers", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "usage_observers", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "raw_capture_sinks", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "traffic_redactors", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "compaction_observers", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: false},
		{id: "compaction_preservers", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: false},
		{id: "secret_guards", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: true},
		{id: "secret_guard_execution", multiplicity: feature.MultExclusive, featComb: feature.CombExclusive, hasDiagStage: false},
		{id: "local_turn_handlers", multiplicity: feature.MultOrdered, featComb: feature.CombConcatenate, hasDiagStage: false},
		{id: "terminal_decision_provider", multiplicity: feature.MultExclusive, featComb: feature.CombExclusive, hasDiagStage: false},
		{id: "session_classifier", multiplicity: feature.MultExclusive, featComb: feature.CombExclusive, hasDiagStage: true},
		{id: "control_tool_provider", multiplicity: feature.MultExclusive, featComb: feature.CombExclusive, hasDiagStage: false},
	}

	for i, exp := range expectedPlanes {
		decl := feature.StandardPlanes[i]
		t.Run(exp.id, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, exp.id, decl.PlaneID())

			err := decl.ValidateDeclaration()
			assert.NoError(t, err)
		})
	}
}

// TestStandardPlanes_SourceRulesPins verifies per-source rules (Feature, Host, GenerationBinder)
// for planes that require host injection or generation binder replace-by-identity semantics.
func TestStandardPlanes_SourceRulesPins(t *testing.T) {
	t.Parallel()

	// Host injection planes: TrafficObservers, UsageObservers
	assert.Equal(t, feature.CombConcatenate, feature.PlaneTrafficObservers.Rules.Host)
	assert.Equal(t, feature.CombConcatenate, feature.PlaneUsageObservers.Rules.Host)

	// GenerationBinder replace-by-identity planes: CompactionPreservers, AttemptTransforms, StreamObserverFactories
	assert.Equal(t, feature.CombReplaceByIdentity, feature.PlaneCompactionPreservers.Rules.GenerationBinder)
	assert.NotNil(t, feature.PlaneCompactionPreservers.ValidateIdentity)
	assert.Equal(t, feature.CombReplaceByIdentity, feature.PlaneAttemptTransforms.Rules.GenerationBinder)
	assert.NotNil(t, feature.PlaneAttemptTransforms.ValidateIdentity)
	assert.Equal(t, feature.CombReplaceByIdentity, feature.PlaneStreamObserverFactories.Rules.GenerationBinder)
	assert.NotNil(t, feature.PlaneStreamObserverFactories.ValidateIdentity)

	// Exclusive plane: TerminalDecisionProvider
	assert.Equal(t, feature.CombExclusive, feature.PlaneTerminalDecisionProvider.Rules.Feature)
	assert.Equal(t, feature.MultExclusive, feature.PlaneTerminalDecisionProvider.Multiplicity)
	assert.Equal(t, feature.ErrTerminalDecisionProviderConflict, feature.PlaneTerminalDecisionProvider.ExclusiveConflictError)
	assert.NotNil(t, feature.PlaneTerminalDecisionProvider.ValidateIdentity)

	// Exclusive plane: ControlToolProvider
	assert.Equal(t, feature.CombExclusive, feature.PlaneControlToolProvider.Rules.Feature)
	assert.Equal(t, feature.MultExclusive, feature.PlaneControlToolProvider.Multiplicity)
	assert.Equal(t, feature.ErrControlToolProviderConflict, feature.PlaneControlToolProvider.ExclusiveConflictError)
	assert.NotNil(t, feature.PlaneControlToolProvider.ValidateIdentity)
}

// TestStandardCandidatePlanes_CanonicalDeclaration verifies the exact canonical candidate plane IDs.
func TestStandardCandidatePlanes_CanonicalDeclaration(t *testing.T) {
	t.Parallel()

	expected := []string{
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
		"secret_guards",
		"compaction_observers",
		"compaction_preservers",
		"local_turn_handlers",
		"terminal_decision_provider",
	}
	assert.Equal(t, expected, feature.StandardCandidatePlanes)
}

// TestStandardCandidatePlanes_GeneratedDispatchCurrency verifies that the generated
// candidate dispatch logic in plane_generated.go contains branches exactly matching StandardCandidatePlanes
// and that the removed contributeCandidateMapTo helper is absent.
func TestStandardCandidatePlanes_GeneratedDispatchCurrency(t *testing.T) {
	t.Parallel()
	repoRoot := findRepoRoot(t)
	genPath := filepath.Join(repoRoot, "pkg", "aipsdk", "feature", "plane_generated.go")
	genContentBytes, err := os.ReadFile(genPath)
	require.NoError(t, err)
	genContent := string(genContentBytes)

	assert.False(t, strings.Contains(genContent, "func contributeCandidateMapTo("),
		"plane_generated.go must not contain contributeCandidateMapTo")

	sections := strings.Split(genContent, "func (gf *generatedFrozen) contributeCandidateTo(")
	require.Len(t, sections, 2, "plane_generated.go must contain contributeCandidateTo")
	methodBody, _, _ := strings.Cut(sections[1], "\nfunc (gf *generatedFrozen)")

	// Candidate branches must exactly match StandardCandidatePlanes
	for _, candID := range feature.StandardCandidatePlanes {
		// Convert snake_case ID to PascalCase plane var name e.g. request_transforms -> PlaneRequestTransforms.ID
		parts := strings.Split(candID, "_")
		for i, part := range parts {
			if len(part) > 0 {
				parts[i] = strings.ToUpper(part[:1]) + part[1:]
			}
		}
		varRef := "canonicalPlane" + strings.Join(parts, "") + "Policy.planeID"
		assert.True(t, strings.Contains(methodBody, varRef),
			"contributeCandidateTo must check candidate plane %s (%q)", varRef, candID)
	}
}

// TestStandardPlanes_HookTargetDeclarationAndCompleteness verifies that exactly the four
// canonical hook planes are annotated with their corresponding HookTarget constants,
// and all other standard planes have empty HookTarget metadata.
func TestStandardPlanes_HookTargetDeclarationAndCompleteness(t *testing.T) {
	t.Parallel()

	expectedHookPlanes := map[string]feature.HookTarget{
		"submit_hooks":        feature.HookTargetSubmitHooks,
		"request_part_hooks":  feature.HookTargetRequestPartHooks,
		"response_part_hooks": feature.HookTargetResponsePartHooks,
		"tool_reactors":       feature.HookTargetToolReactors,
	}

	annotatedCount := 0
	for _, decl := range feature.StandardPlanes {
		target := feature.DeclaredHookTargetForTest(decl)
		if expTarget, ok := expectedHookPlanes[decl.PlaneID()]; ok {
			assert.Equal(t, expTarget, target, "plane %s must have expected HookTarget", decl.PlaneID())
			annotatedCount++
		} else {
			assert.Empty(t, target, "plane %s must not have HookTarget annotation", decl.PlaneID())
		}
	}

	assert.Equal(t, 4, annotatedCount, "exactly four canonical hook planes must be annotated")
	assert.Equal(t, feature.HookTargetSubmitHooks, feature.PlaneSubmitHooks.HookTarget)
	assert.Equal(t, feature.HookTargetRequestPartHooks, feature.PlaneRequestPartHooks.HookTarget)
	assert.Equal(t, feature.HookTargetResponsePartHooks, feature.PlaneResponsePartHooks.HookTarget)
	assert.Equal(t, feature.HookTargetToolReactors, feature.PlaneToolReactors.HookTarget)
}

// TestPlaneDeclarationValidation_HookTarget verifies that Plane.ValidateDeclaration validates
// canonical HookTarget constants, rejects unknown HookTarget metadata, and ValidateManifest rejects
// duplicate HookTarget declarations.
func TestPlaneDeclarationValidation_HookTarget(t *testing.T) {
	t.Parallel()

	t.Run("ValidHookTargetsPassValidateDeclaration", func(t *testing.T) {
		t.Parallel()
		validTargets := []feature.HookTarget{
			"",
			feature.HookTargetSubmitHooks,
			feature.HookTargetRequestPartHooks,
			feature.HookTargetResponsePartHooks,
			feature.HookTargetToolReactors,
		}

		for _, target := range validTargets {
			plane := feature.Plane[string]{
				ID:           "test_plane",
				Multiplicity: feature.MultOrdered,
				Rules:        feature.SourceRules{Feature: feature.CombConcatenate},
				Combine:      func(s feature.SourceKind, c, in string) (string, error) { return c + in, nil },
				HookTarget:   target,
			}
			assert.NoError(t, plane.ValidateDeclaration(), "valid hook target %q must pass validation", target)
		}
	})

	t.Run("UnknownHookTargetRejectedInValidateDeclaration", func(t *testing.T) {
		t.Parallel()
		invalidTargets := []feature.HookTarget{
			"UnknownTargetTypo",
			"submit_hooks",
			"SubmitHook",
			"ToolReactorErrorPolicy",
		}
		for _, target := range invalidTargets {
			plane := feature.Plane[string]{
				ID:           "test_plane",
				Multiplicity: feature.MultOrdered,
				Rules:        feature.SourceRules{Feature: feature.CombConcatenate},
				Combine:      func(s feature.SourceKind, c, in string) (string, error) { return c + in, nil },
				HookTarget:   target,
			}
			err := plane.ValidateDeclaration()
			require.Error(t, err, "invalid hook target %q must fail validation", target)
			assert.ErrorIs(t, err, feature.ErrInvalidPlane)
			assert.Contains(t, err.Error(), string(target))
		}
	})

	t.Run("DuplicateHookTargetRejectedInValidateManifest", func(t *testing.T) {
		t.Parallel()
		plane1 := feature.Plane[string]{
			ID:           "test_plane_1",
			Multiplicity: feature.MultOrdered,
			Rules:        feature.SourceRules{Feature: feature.CombConcatenate},
			Combine:      func(s feature.SourceKind, c, in string) (string, error) { return c + in, nil },
			HookTarget:   feature.HookTargetSubmitHooks,
		}
		plane2 := feature.Plane[string]{
			ID:           "test_plane_2",
			Multiplicity: feature.MultOrdered,
			Rules:        feature.SourceRules{Feature: feature.CombConcatenate},
			Combine:      func(s feature.SourceKind, c, in string) (string, error) { return c + in, nil },
			HookTarget:   feature.HookTargetSubmitHooks,
		}
		err := feature.ValidateManifest(plane1, plane2)
		require.Error(t, err)
		assert.ErrorIs(t, err, feature.ErrInvalidPlane)
		assert.Contains(t, err.Error(), "SubmitHooks")
	})
}

// TestSecretGuardExecutionPlane_BinderOnlyAdmission pins that the
// secret_guard_execution plane accepts ONLY generation-binder contributions.
// A feature or host contribution must fail with ErrUnsupportedSource, so no
// plugin can supply execution posture when the standard distribution composed
// none, nor conflict with the standard binder's exclusive slot.
func TestSecretGuardExecutionPlane_BinderOnlyAdmission(t *testing.T) {
	t.Parallel()

	cfg := &secretguard.ExecutionConfig{AccessMode: "single_user", ConfigVersion: "v1"}

	feat := feature.NewContributionSet()
	err := feature.ContributeSource(feat, feature.PlaneSecretGuardExecution, feature.SourceFeature, "plugin-ext", cfg)
	require.ErrorIs(t, err, feature.ErrUnsupportedSource, "feature source must be rejected on the execution plane")

	host := feature.NewContributionSet()
	herr := feature.ContributeSource(host, feature.PlaneSecretGuardExecution, feature.SourceHost, "host-ext", cfg)
	require.ErrorIs(t, herr, feature.ErrUnsupportedSource, "host source must be rejected on the execution plane")

	binder := feature.NewContributionSet()
	require.NoError(t, feature.ContributeSource(binder, feature.PlaneSecretGuardExecution, feature.SourceGenerationBinder, "secret-guard-execution", cfg),
		"generation-binder source must be accepted on the execution plane")
}

// TestSecretGuardExecutionPlane_ContributionIsolation pins frozen-value
// isolation: mutating a contributed ExecutionConfig after contribution must
// not alter the frozen generation's configuration. The frozen set must never
// alias contributor memory.
func TestSecretGuardExecutionPlane_ContributionIsolation(t *testing.T) {
	t.Parallel()

	cfg := &secretguard.ExecutionConfig{
		AccessMode:       "single_user",
		ConfigVersion:    "v1",
		SourceCategories: []string{"env", "catalog"},
	}
	cs := feature.NewContributionSet()
	require.NoError(t, feature.ContributeSource(cs, feature.PlaneSecretGuardExecution, feature.SourceGenerationBinder, "secret-guard-execution", cfg))

	// Mutate the contributor's value after contribution.
	cfg.AccessMode = "mutated"
	cfg.SourceCategories[0] = "mutated"

	frozen := cs.Freeze()
	got := feature.Get(frozen, feature.PlaneSecretGuardExecution)
	require.NotNil(t, got, "expected composed execution config in frozen set")
	assert.Equal(t, "single_user", got.AccessMode, "frozen config must not alias contributor memory")
	require.Len(t, got.SourceCategories, 2, "frozen categories must survive contributor mutation")
	assert.Equal(t, "env", got.SourceCategories[0], "frozen categories must not alias contributor slice")
}

// TestSecretGuardExecutionPlane_ReadIsolation pins frozen-value isolation on
// every read path: mutating a value obtained through feature.Get (from the
// generation set, a cloned set, or a request-frozen set) must not alter
// subsequent reads. Shared engine capabilities are intentionally preserved;
// the mutable container and slice are copied per read via the declared
// RequestMaterializer.
func TestSecretGuardExecutionPlane_ReadIsolation(t *testing.T) {
	t.Parallel()

	seed := &secretguard.ExecutionConfig{
		AccessMode:       "single_user",
		ConfigVersion:    "v1",
		SourceCategories: []string{"env", "catalog"},
	}
	cs := feature.NewContributionSet()
	require.NoError(t, feature.ContributeSource(cs, feature.PlaneSecretGuardExecution, feature.SourceGenerationBinder, "secret-guard-execution", seed))
	frozen := cs.Freeze()

	mutate := func(v *secretguard.ExecutionConfig) {
		v.AccessMode = "mutated"
		if len(v.SourceCategories) > 0 {
			v.SourceCategories[0] = "mutated"
		}
	}
	checkStable := func(v *secretguard.ExecutionConfig, where string) {
		t.Helper()
		require.NotNil(t, v, "expected execution config %s", where)
		assert.Equal(t, "single_user", v.AccessMode, "frozen config mutated %s", where)
		require.Len(t, v.SourceCategories, 2, "frozen categories changed %s", where)
		assert.Equal(t, "env", v.SourceCategories[0], "frozen categories mutated %s", where)
	}

	// Ordinary read: mutate the returned value, reread must be stable.
	first := feature.Get(frozen, feature.PlaneSecretGuardExecution)
	checkStable(first, "on first read")
	mutate(first)
	checkStable(feature.Get(frozen, feature.PlaneSecretGuardExecution), "after mutating a Get result")

	// Cloned set: reads through the clone must be isolated both ways.
	cloned := frozen.Clone()
	mutate(feature.Get(cloned, feature.PlaneSecretGuardExecution))
	checkStable(feature.Get(cloned, feature.PlaneSecretGuardExecution), "after mutating a cloned-set read")
	checkStable(feature.Get(frozen, feature.PlaneSecretGuardExecution), "on the original set after cloned-set mutation")

	// Request-frozen set: materialized at snapshot construction.
	reqFrozen := feature.FreezeRequestPlanes(frozen)
	mutate(feature.Get(reqFrozen, feature.PlaneSecretGuardExecution))
	checkStable(feature.Get(reqFrozen, feature.PlaneSecretGuardExecution), "after mutating a request-frozen read")
}
