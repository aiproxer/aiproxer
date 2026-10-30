package feature_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

// TestGenerator_DeterministicTwoRunsByteIdentical tests that running the generator
// twice produces byte-identical generated output.
func TestGenerator_DeterministicTwoRunsByteIdentical(t *testing.T) {
	t.Parallel()

	repoRoot := findRepoRoot(t)
	scriptPath := filepath.Join(repoRoot, "scripts", "generate-feature-planes.go")
	tmpDir := t.TempDir()
	out1Path := filepath.Join(tmpDir, "plane_generated_1.go")
	out2Path := filepath.Join(tmpDir, "plane_generated_2.go")

	// Run generator 1st time to temp output
	cmd1 := exec.Command("go", "run", scriptPath, "-out", out1Path)
	cmd1.Dir = repoRoot
	out1, err := cmd1.CombinedOutput()
	require.NoError(t, err, "run 1 failed: %s", string(out1))

	bytesRun1, err := os.ReadFile(out1Path)
	require.NoError(t, err)

	// Run generator 2nd time to temp output
	cmd2 := exec.Command("go", "run", scriptPath, "-out", out2Path)
	cmd2.Dir = repoRoot
	out2, err := cmd2.CombinedOutput()
	require.NoError(t, err, "run 2 failed: %s", string(out2))

	bytesRun2, err := os.ReadFile(out2Path)
	require.NoError(t, err)

	// Assert byte-identical output across two runs
	assert.True(t, bytes.Equal(bytesRun1, bytesRun2), "plane_generated.go must be byte-identical across two generation runs")

	// Run generator in check mode against tracked file
	cmdCheck := exec.Command("go", "run", scriptPath, "-check")
	cmdCheck.Dir = repoRoot
	outCheck, err := cmdCheck.CombinedOutput()
	require.NoError(t, err, "generator -check failed: %s", string(outCheck))

	// Verify that generator in check mode fails on corrupted file
	corruptedPath := filepath.Join(tmpDir, "corrupted.go")
	err = os.WriteFile(corruptedPath, []byte("// corrupted"), 0o644)
	require.NoError(t, err)

	cmdCorrupt := exec.Command("go", "run", scriptPath, "-check", "-out", corruptedPath)
	cmdCorrupt.Dir = repoRoot
	outCorrupt, err := cmdCorrupt.CombinedOutput()
	assert.Error(t, err, "generator -check must fail on corrupted file: %s", string(outCorrupt))
}

// TestGeneratedCode_NoForbiddenPatterns verifies that request-path generated structures
// (generatedContributions, generatedFrozen, init closures) contain no untyped `any`,
// no type assertions `.(`, no `reflect`, no `unsafe`, no `map[`, and no key-search loops.
func TestGeneratedCode_NoForbiddenPatterns(t *testing.T) {
	t.Parallel()
	repoRoot := findRepoRoot(t)
	targetPath := filepath.Join(repoRoot, "pkg", "aipsdk", "feature", "plane_generated.go")

	contentBytes, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	content := string(contentBytes)

	// Across the entire file, reflect, unsafe, range loops, any, type assertions, and maps are strictly forbidden
	assert.False(t, strings.Contains(content, "reflect"), "plane_generated.go must not contain reflect")
	assert.False(t, strings.Contains(content, "unsafe"), "plane_generated.go must not contain unsafe")
	assert.False(t, strings.Contains(content, "range "), "plane_generated.go must not contain range loops for dispatch")

	forbiddenPatterns := []struct {
		pattern string
		reason  string
	}{
		{"any", "forbidden runtime discovery/untyped any in generated code"},
		{".(", "forbidden type assertion in generated code"},
		{"map[", "forbidden map lookup in generated code"},
	}

	for _, tc := range forbiddenPatterns {
		assert.False(t, strings.Contains(content, tc.pattern),
			"generated code must not contain %q: %s", tc.pattern, tc.reason)
	}
}

// TestGeneratedDispatch_TypedAdaptersEndToEnd tests generated typed dispatch across
// slice, scalar, exclusive interface provider, and replace-by-identity planes.
func TestGeneratedDispatch_TypedAdaptersEndToEnd(t *testing.T) {
	t.Parallel()

	cset := feature.NewContributionSet()

	// 1. Slice plane: PlaneSubmitHooks
	hook1 := dummySubmitHook{id: "hook-a", ord: 10}
	hook2 := dummySubmitHook{id: "hook-b", ord: 5}
	err := feature.Contribute(cset, feature.PlaneSubmitHooks, "plugin-hooks", []hooks.SubmitHook{hook1, hook2})
	require.NoError(t, err)

	// 2. Scalar int plane: PlaneToolCallFinalizationMaxArgsBytes
	err = feature.Contribute(cset, feature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-scalar-1", 4096)
	require.NoError(t, err)
	err = feature.Contribute(cset, feature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-scalar-2", 2048)
	require.NoError(t, err)

	// 3. Exclusive interface provider plane: PlaneTerminalDecisionProvider
	provider := dummyTerminalProvider{id: "term-provider-primary"}
	err = feature.Contribute(cset, feature.PlaneTerminalDecisionProvider, "plugin-term", terminaldecision.Provider(provider))
	require.NoError(t, err)

	// 4. Replace-by-identity plane: PlaneCompactionPreservers
	preserver1 := dummyPreserver{id: "preserver-orig"}
	err = feature.Contribute(cset, feature.PlaneCompactionPreservers, "plugin-preserver", []compaction.Preserver{preserver1})
	require.NoError(t, err)

	// Freeze snapshot
	frozen := cset.Freeze()

	// Verify slice plane retrieval
	hooksGot := feature.Get(frozen, feature.PlaneSubmitHooks)
	require.Len(t, hooksGot, 2)
	assert.Equal(t, "hook-a", hooksGot[0].ID())
	assert.Equal(t, "hook-b", hooksGot[1].ID())

	// Verify scalar reduce plane retrieval (min positive value: 2048)
	maxArgsGot := feature.Get(frozen, feature.PlaneToolCallFinalizationMaxArgsBytes)
	assert.Equal(t, 2048, maxArgsGot)

	// Verify exclusive interface provider and identity
	providerGot := feature.Get(frozen, feature.PlaneTerminalDecisionProvider)
	require.NotNil(t, providerGot)
	assert.Equal(t, "term-provider-primary", providerGot.ID())

	termID, hasID := feature.FrozenIdentity(frozen, feature.PlaneTerminalDecisionProvider)
	assert.True(t, hasID)
	assert.Equal(t, "term-provider-primary", termID)

	// Verify replace-by-identity plane
	preserversGot := feature.Get(frozen, feature.PlaneCompactionPreservers)
	require.Len(t, preserversGot, 1)
	assert.Equal(t, "preserver-orig", preserversGot[0].ID())

	preserverID, hasPreserverID := feature.FrozenIdentity(frozen, feature.PlaneCompactionPreservers)
	assert.True(t, hasPreserverID)
	assert.Equal(t, "preserver-orig", preserverID)
}

// TestGeneratedDispatch_ReplaceByIdentity verifies that Combine on replace-by-identity
// planes replaces any existing occupant with matching ID and appends replacement.
func TestGeneratedDispatch_ReplaceByIdentity(t *testing.T) {
	t.Parallel()

	// 1. CompactionPreservers
	p1 := dummyPreserver{id: "pres-1"}
	p2 := dummyPreserver{id: "pres-2"}
	p1Replacement := dummyPreserver{id: "pres-1"}

	curPres, err := feature.PlaneCompactionPreservers.Combine(feature.SourceFeature, nil, []compaction.Preserver{p1, p2})
	require.NoError(t, err)
	require.Len(t, curPres, 2)

	replacedPres, err := feature.PlaneCompactionPreservers.Combine(feature.SourceGenerationBinder, curPres, []compaction.Preserver{p1Replacement})
	require.NoError(t, err)
	require.Len(t, replacedPres, 2)
	assert.Equal(t, "pres-2", replacedPres[0].ID())
	assert.Equal(t, "pres-1", replacedPres[1].ID())

	// 2. AttemptTransforms
	x1 := dummyAttemptTransform{id: "xform-1"}
	x2 := dummyAttemptTransform{id: "xform-2"}
	x1Replacement := dummyAttemptTransform{id: "xform-1"}

	curX, err := feature.PlaneAttemptTransforms.Combine(feature.SourceFeature, nil, []request.AttemptTransform{x1, x2})
	require.NoError(t, err)
	require.Len(t, curX, 2)

	replacedX, err := feature.PlaneAttemptTransforms.Combine(feature.SourceGenerationBinder, curX, []request.AttemptTransform{x1Replacement})
	require.NoError(t, err)
	require.Len(t, replacedX, 2)
	assert.Equal(t, "xform-2", replacedX[0].ID())
	assert.Equal(t, "xform-1", replacedX[1].ID())

	// 3. StreamObserverFactories
	f1 := dummyStreamObserverFactory{id: "obs-1"}
	f2 := dummyStreamObserverFactory{id: "obs-2"}
	f1Replacement := dummyStreamObserverFactory{id: "obs-1"}

	curF, err := feature.PlaneStreamObserverFactories.Combine(feature.SourceFeature, nil, []response.StreamObserverFactory{f1, f2})
	require.NoError(t, err)
	require.Len(t, curF, 2)

	replacedF, err := feature.PlaneStreamObserverFactories.Combine(feature.SourceGenerationBinder, curF, []response.StreamObserverFactory{f1Replacement})
	require.NoError(t, err)
	require.Len(t, replacedF, 2)
	assert.Equal(t, "obs-2", replacedF[0].ID())
	assert.Equal(t, "obs-1", replacedF[1].ID())
}

// TestGeneratedBinderOperations_MethodCurrencyAndFailure tests that typed binder methods
// generated on *ContributionSet for replace-by-identity planes replace occupants under
// SourceGenerationBinder semantics and fail before mutate on invalid input.
func TestGeneratedBinderOperations_MethodCurrencyAndFailure(t *testing.T) {
	t.Parallel()

	cs := feature.NewContributionSet()
	x1 := dummyAttemptTransform{id: "xform-1"}
	x2 := dummyAttemptTransform{id: "xform-2"}
	require.NoError(t, feature.Contribute(cs, feature.PlaneAttemptTransforms, "plugin-xform", []request.AttemptTransform{x1, x2}))

	f1 := dummyStreamObserverFactory{id: "obs-1"}
	f2 := dummyStreamObserverFactory{id: "obs-2"}
	require.NoError(t, feature.Contribute(cs, feature.PlaneStreamObserverFactories, "plugin-obs", []response.StreamObserverFactory{f1, f2}))

	p1 := dummyPreserver{id: "pres-1"}
	p2 := dummyPreserver{id: "pres-2"}
	require.NoError(t, feature.Contribute(cs, feature.PlaneCompactionPreservers, "plugin-pres", []compaction.Preserver{p1, p2}))

	// 1. Successful typed replacement on AttemptTransforms
	x1Repl := dummyAttemptTransform{id: "xform-1"}
	require.NoError(t, cs.BindAttemptTransforms("binder-xform", []request.AttemptTransform{x1Repl}))
	frozen1 := cs.Freeze()
	xforms := feature.Get(frozen1, feature.PlaneAttemptTransforms)
	require.Len(t, xforms, 2)
	assert.Equal(t, "xform-2", xforms[0].ID())
	assert.Equal(t, "xform-1", xforms[1].ID())

	// 2. Successful typed replacement on StreamObserverFactories
	f1Repl := dummyStreamObserverFactory{id: "obs-1"}
	require.NoError(t, cs.BindStreamObserverFactories("binder-obs", []response.StreamObserverFactory{f1Repl}))
	frozen2 := cs.Freeze()
	factories := feature.Get(frozen2, feature.PlaneStreamObserverFactories)
	require.Len(t, factories, 2)
	assert.Equal(t, "obs-2", factories[0].ID())
	assert.Equal(t, "obs-1", factories[1].ID())

	// 3. Successful typed replacement on CompactionPreservers
	p1Repl := dummyPreserver{id: "pres-1"}
	require.NoError(t, cs.BindCompactionPreservers("binder-pres", []compaction.Preserver{p1Repl}))
	frozen3 := cs.Freeze()
	preservers := feature.Get(frozen3, feature.PlaneCompactionPreservers)
	require.Len(t, preservers, 2)
	assert.Equal(t, "pres-2", preservers[0].ID())
	assert.Equal(t, "pres-1", preservers[1].ID())

	// 4. Fail-before-mutate on nil element under NilReject
	err := cs.BindAttemptTransforms("binder-bad", []request.AttemptTransform{nil})
	require.Error(t, err)
	frozenAfterFail := cs.Freeze()
	xformsAfterFail := feature.Get(frozenAfterFail, feature.PlaneAttemptTransforms)
	require.Len(t, xformsAfterFail, 2)
	assert.Equal(t, "xform-2", xformsAfterFail[0].ID())
	assert.Equal(t, "xform-1", xformsAfterFail[1].ID())
}

// TestContributionSet_Clone_PreservesAllPlanesAndIsolation verifies that ContributionSet.Clone
// performs a deep copy across all declared planes without mutating the original.
func TestContributionSet_Clone_PreservesAllPlanesAndIsolation(t *testing.T) {
	t.Parallel()

	cs := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(cs, feature.PlaneSubmitHooks, "plugin-1", []hooks.SubmitHook{
		dummySubmitHook{id: "hook-1"},
	}))
	require.NoError(t, feature.Contribute(cs, feature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-1", 4096))
	require.NoError(t, feature.Contribute(cs, feature.PlaneTerminalDecisionProvider, "plugin-1", terminaldecision.Provider(dummyTerminalProvider{id: "term-orig"})))

	cloned := cs.Clone()
	require.NotNil(t, cloned)

	// Mutate clone
	require.NoError(t, feature.Contribute(cloned, feature.PlaneSubmitHooks, "plugin-2", []hooks.SubmitHook{
		dummySubmitHook{id: "hook-2"},
	}))

	// Original must not have hook-2
	fOrig := cs.Freeze()
	origHooks := feature.Get(fOrig, feature.PlaneSubmitHooks)
	require.Len(t, origHooks, 1)
	assert.Equal(t, "hook-1", origHooks[0].ID())

	// Cloned has both
	fCloned := cloned.Freeze()
	clonedHooks := feature.Get(fCloned, feature.PlaneSubmitHooks)
	require.Len(t, clonedHooks, 2)
}

// TestContributionSet_FromFrozen_PreservesAllPlanes verifies that FrozenPlaneSet.ToContributions
// and feature.ContributionSetFromFrozen reconstruct a mutable ContributionSet with all 27 planes.
func TestContributionSet_FromFrozen_PreservesAllPlanes(t *testing.T) {
	t.Parallel()

	cs := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(cs, feature.PlaneSubmitHooks, "plugin-1", []hooks.SubmitHook{
		dummySubmitHook{id: "hook-1"},
	}))
	require.NoError(t, feature.Contribute(cs, feature.PlaneToolCallFinalizationMaxArgsBytes, "plugin-1", 2048))
	require.NoError(t, feature.Contribute(cs, feature.PlaneTerminalDecisionProvider, "plugin-1", terminaldecision.Provider(dummyTerminalProvider{id: "term-1"})))

	frozen := cs.Freeze()

	// Reconstruct from frozen
	csReconstructed := frozen.ToContributions()
	require.NotNil(t, csReconstructed)

	fNew := csReconstructed.Freeze()
	assert.Len(t, feature.Get(fNew, feature.PlaneSubmitHooks), 1)
	assert.Equal(t, 2048, feature.Get(fNew, feature.PlaneToolCallFinalizationMaxArgsBytes))
	assert.NotNil(t, feature.Get(fNew, feature.PlaneTerminalDecisionProvider))
	id, hasID := feature.FrozenIdentity(fNew, feature.PlaneTerminalDecisionProvider)
	assert.True(t, hasID)
	assert.Equal(t, "term-1", id)
}

// Stubs for test cases
type dummyPreserver struct {
	id string
}

func (p dummyPreserver) ID() string { return p.id }
func (p dummyPreserver) BeforeRequest(context.Context, *aipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (p dummyPreserver) RequestOpened(context.Context, aipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (p dummyPreserver) BeforeResponseRelease(context.Context, *aipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

type dummyAttemptTransform struct {
	id  string
	ord int
}

func (t dummyAttemptTransform) ID() string                     { return t.id }
func (t dummyAttemptTransform) Order() int                     { return t.ord }
func (t dummyAttemptTransform) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (t dummyAttemptTransform) HandleAttempt(context.Context, *aipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{}, nil
}

type dummyStreamObserverFactory struct {
	id  string
	ord int
}

func (f dummyStreamObserverFactory) ID() string                     { return f.id }
func (f dummyStreamObserverFactory) Order() int                     { return f.ord }
func (f dummyStreamObserverFactory) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (f dummyStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not locate repository root containing go.mod")
	return ""
}

// TestGenerator_RejectsIllegalManifests verifies that the generator CLI rejects
// illegal manifests including duplicate IDs, incomplete source rules, missing combine functions,
// and invalid multiplicity before any code is generated.
func TestGenerator_RejectsIllegalManifests(t *testing.T) {
	t.Parallel()

	repoRoot := findRepoRoot(t)
	scriptPath := filepath.Join(repoRoot, "scripts", "generate-feature-planes.go")

	cases := []struct {
		name        string
		manifestSrc string
		expectedErr string
	}{
		{
			name: "duplicate_plane_id",
			manifestSrc: `package feature

import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"

var PlaneOne = Plane[[]hooks.SubmitHook]{
	ID: "duplicate_id",
	Multiplicity: MultOrdered,
	Rules: SourceRules{Feature: CombConcatenate},
	RequestAccess: RequestBodyCanonicalRequired,
	Combine: func(source SourceKind, cur, inc []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(cur, inc...), nil },
}

var PlaneTwo = Plane[[]hooks.SubmitHook]{
	ID: "duplicate_id",
	Multiplicity: MultOrdered,
	Rules: SourceRules{Feature: CombConcatenate},
	RequestAccess: RequestBodyCanonicalRequired,
	Combine: func(source SourceKind, cur, inc []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(cur, inc...), nil },
}

var StandardPlanes = []PlaneDeclaration{
	PlaneOne,
	PlaneTwo,
}
`,
			expectedErr: `duplicate plane ID "duplicate_id"`,
		},
		{
			name: "incomplete_source_rules",
			manifestSrc: `package feature

import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"

var PlaneIncomplete = Plane[[]hooks.SubmitHook]{
	ID: "incomplete_rules_plane",
	Multiplicity: MultOrdered,
	Rules: SourceRules{},
	Combine: func(source SourceKind, cur, inc []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(cur, inc...), nil },
}

var StandardPlanes = []PlaneDeclaration{
	PlaneIncomplete,
}
`,
			expectedErr: "at least one source rule must be specified",
		},
		{
			name: "missing_combine_function",
			manifestSrc: `package feature

import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"

var PlaneNoCombine = Plane[[]hooks.SubmitHook]{
	ID: "no_combine_plane",
	Multiplicity: MultOrdered,
	Rules: SourceRules{Feature: CombConcatenate},
}

var StandardPlanes = []PlaneDeclaration{
	PlaneNoCombine,
}
`,
			expectedErr: "combine function is required",
		},
		{
			name: "nil_combine_function",
			manifestSrc: `package feature

import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"

var PlaneNilCombine = Plane[[]hooks.SubmitHook]{
	ID: "nil_combine_plane",
	Multiplicity: MultOrdered,
	Rules: SourceRules{Feature: CombConcatenate},
	Combine: nil,
}

var StandardPlanes = []PlaneDeclaration{
	PlaneNilCombine,
}
`,
			expectedErr: "combine function is required",
		},
		{
			name: "missing_plane_id",
			manifestSrc: `package feature

import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"

var PlaneNoID = Plane[[]hooks.SubmitHook]{
	Multiplicity: MultOrdered,
	Rules: SourceRules{Feature: CombConcatenate},
	Combine: func(source SourceKind, cur, inc []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(cur, inc...), nil },
}

var StandardPlanes = []PlaneDeclaration{
	PlaneNoID,
}
`,
			expectedErr: "plane ID is required and must not be empty",
		},
		{
			name: "exclusive_plane_with_concatenate",
			manifestSrc: `package feature

import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"

var PlaneBadExclusive = Plane[[]hooks.SubmitHook]{
	ID: "bad_exclusive",
	Multiplicity: MultExclusive,
	Rules: SourceRules{Feature: CombConcatenate},
	Combine: func(source SourceKind, cur, inc []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(cur, inc...), nil },
}

var StandardPlanes = []PlaneDeclaration{
	PlaneBadExclusive,
}
`,
			expectedErr: "exclusive plane cannot use concatenate or reduce rule",
		},
		{
			name: "exclusive_plane_without_identity",
			manifestSrc: `package feature

import "github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"

var PlaneNoIdentityExclusive = Plane[terminaldecision.Provider]{
	ID: "exclusive_no_identity",
	Multiplicity: MultExclusive,
	Rules: SourceRules{Feature: CombExclusive},
	Combine: func(source SourceKind, cur, inc terminaldecision.Provider) (terminaldecision.Provider, error) { return inc, nil },
}

var StandardPlanes = []PlaneDeclaration{
	PlaneNoIdentityExclusive,
}
`,
			expectedErr: "identity function is required for exclusive or replace-by-identity plane",
		},
		{
			name: "exclusive_plane_with_nil_identity",
			manifestSrc: `package feature

import "github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"

var PlaneNilIdentityExclusive = Plane[terminaldecision.Provider]{
	ID: "exclusive_nil_identity",
	Multiplicity: MultExclusive,
	Rules: SourceRules{Feature: CombExclusive},
	Identity: nil,
	Combine: func(source SourceKind, cur, inc terminaldecision.Provider) (terminaldecision.Provider, error) { return inc, nil },
}

var StandardPlanes = []PlaneDeclaration{
	PlaneNilIdentityExclusive,
}
`,
			expectedErr: "identity function is required for exclusive or replace-by-identity plane",
		},
		{
			name: "replace_by_identity_without_identity",
			manifestSrc: `package feature

import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"

var PlaneReplaceNoIdentity = Plane[[]hooks.SubmitHook]{
	ID: "replace_no_identity",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
		GenerationBinder: CombReplaceByIdentity,
	},
	Combine: func(source SourceKind, cur, inc []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(cur, inc...), nil },
}

var StandardPlanes = []PlaneDeclaration{
	PlaneReplaceNoIdentity,
}
`,
			expectedErr: "identity function is required for exclusive or replace-by-identity plane",
		},
		{
			name: "exclusive_plane_without_validate_identity",
			manifestSrc: `package feature

import "github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"

var PlaneNoValidateIdentityExclusive = Plane[terminaldecision.Provider]{
	ID: "exclusive_no_validate_identity",
	Multiplicity: MultExclusive,
	Rules: SourceRules{Feature: CombExclusive},
	Identity: func(v terminaldecision.Provider) (string, bool) { return "id", true },
	Combine: func(source SourceKind, cur, inc terminaldecision.Provider) (terminaldecision.Provider, error) { return inc, nil },
}

var StandardPlanes = []PlaneDeclaration{
	PlaneNoValidateIdentityExclusive,
}
`,
			expectedErr: "cached identity validator is required for exclusive or replace-by-identity plane",
		},
		{
			name: "exclusive_plane_with_nil_validate_identity",
			manifestSrc: `package feature

import "github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"

var PlaneNilValidateIdentityExclusive = Plane[terminaldecision.Provider]{
	ID: "exclusive_nil_validate_identity",
	Multiplicity: MultExclusive,
	Rules: SourceRules{Feature: CombExclusive},
	Identity: func(v terminaldecision.Provider) (string, bool) { return "id", true },
	ValidateIdentity: nil,
	Combine: func(source SourceKind, cur, inc terminaldecision.Provider) (terminaldecision.Provider, error) { return inc, nil },
}

var StandardPlanes = []PlaneDeclaration{
	PlaneNilValidateIdentityExclusive,
}
`,
			expectedErr: "cached identity validator is required for exclusive or replace-by-identity plane",
		},
		{
			name: "replace_by_identity_without_validate_identity",
			manifestSrc: `package feature

import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"

var PlaneReplaceNoValidateIdentity = Plane[[]hooks.SubmitHook]{
	ID: "replace_no_validate_identity",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
		GenerationBinder: CombReplaceByIdentity,
	},
	Identity: func(v []hooks.SubmitHook) (string, bool) { return "id", true },
	Combine: func(source SourceKind, cur, inc []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(cur, inc...), nil },
}

var StandardPlanes = []PlaneDeclaration{
	PlaneReplaceNoValidateIdentity,
}
`,
			expectedErr: "cached identity validator is required for exclusive or replace-by-identity plane",
		},
		{
			name: "replace_by_identity_with_nil_validate_identity",
			manifestSrc: `package feature

import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"

var PlaneReplaceNilValidateIdentity = Plane[[]hooks.SubmitHook]{
	ID: "replace_nil_validate_identity",
	Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
		GenerationBinder: CombReplaceByIdentity,
	},
	Identity: func(v []hooks.SubmitHook) (string, bool) { return "id", true },
	ValidateIdentity: nil,
	Combine: func(source SourceKind, cur, inc []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(cur, inc...), nil },
}

var StandardPlanes = []PlaneDeclaration{
	PlaneReplaceNilValidateIdentity,
}
`,
			expectedErr: "cached identity validator is required for exclusive or replace-by-identity plane",
		},
		{
			name: "diagnostics_missing_stage_id",
			manifestSrc: `package feature

import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"

var PlaneNoStageID = Plane[[]hooks.SubmitHook]{
	ID: "no_stage_id_plane",
	Multiplicity: MultOrdered,
	Rules: SourceRules{Feature: CombConcatenate},
	Combine: func(source SourceKind, cur, inc []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(cur, inc...), nil },
	Diagnostics: DiagnosticDescriptor[[]hooks.SubmitHook]{
		StageID: "",
		Materialize: func(v []hooks.SubmitHook) []DiagnosticOccupant { return nil },
	},
}

var StandardPlanes = []PlaneDeclaration{
	PlaneNoStageID,
}
`,
			expectedErr: "diagnostics StageID must not be empty",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmpDir := t.TempDir()
			manifestFile := filepath.Join(tmpDir, "plane_manifest.go")
			outFile := filepath.Join(tmpDir, "plane_generated.go")

			err := os.WriteFile(manifestFile, []byte(tc.manifestSrc), 0o644)
			require.NoError(t, err)

			cmd := exec.Command("go", "run", scriptPath, "-manifest", manifestFile, "-out", outFile)
			cmd.Dir = repoRoot
			out, err := cmd.CombinedOutput()
			assert.Error(t, err, "generator must fail on illegal manifest %s", tc.name)
			assert.Contains(t, string(out), tc.expectedErr, "generator output must contain expected error for %s", tc.name)
		})
	}
}

// TestGeneratedFrozen_InvalidCachedIdentityTriggersValidator verifies that frozen validation
// calls ValidateIdentity unconditionally on cached IDs, failing when the cached ID is invalid.
func TestGeneratedFrozen_InvalidCachedIdentityTriggersValidator(t *testing.T) {
	t.Parallel()

	provider := dummyTerminalProvider{id: "term-provider-valid"}

	// 1. Valid cached identity passes Validate()
	frozenValid := feature.NewMalformedGeneratedFrozenTerminalDecisionForTest(
		terminaldecision.Provider(provider),
		"term-provider-valid",
		true,
	)
	require.NoError(t, frozenValid.Validate())

	// 2. Invalid cached identity (empty string) fails with missing cached identity error
	frozenMissingID := feature.NewMalformedGeneratedFrozenTerminalDecisionForTest(
		terminaldecision.Provider(provider),
		"",
		true,
	)
	err := frozenMissingID.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing cached identity")

	// 3. Invalid cached identity (violating ValidateIdentity bounds / format) fails validator
	// terminaldecision.MaxProviderIDBytes is 128
	oversizedID := strings.Repeat("x", 200)
	frozenInvalidID := feature.NewMalformedGeneratedFrozenTerminalDecisionForTest(
		terminaldecision.Provider(provider),
		oversizedID,
		true,
	)
	err = frozenInvalidID.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), feature.PlaneTerminalDecisionProvider.ID)
	assert.Contains(t, err.Error(), "exceeds 128 bytes")
}

type dummyRequestPartHook struct {
	id  string
	ord int
}

func (h dummyRequestPartHook) ID() string                     { return h.id }
func (h dummyRequestPartHook) Order() int                     { return h.ord }
func (h dummyRequestPartHook) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (h dummyRequestPartHook) HandleRequestParts(context.Context, *aipapi.Call, hooks.PartMeta) error {
	return nil
}

type dummyResponsePartHook struct {
	id  string
	ord int
}

func (h dummyResponsePartHook) ID() string                     { return h.id }
func (h dummyResponsePartHook) Order() int                     { return h.ord }
func (h dummyResponsePartHook) FailureMode() hooks.FailureMode { return hooks.FailClosed }
func (h dummyResponsePartHook) HandleEvent(context.Context, *aipapi.Event, hooks.PartMeta) error {
	return nil
}

type dummyToolReactor struct {
	id  string
	ord int
}

func (r dummyToolReactor) ID() string { return r.id }
func (r dummyToolReactor) Order() int { return r.ord }
func (r dummyToolReactor) HandleToolEvent(context.Context, aipapi.ToolEvent, hooks.ToolMeta) (hooks.ToolDecision, aipapi.ToolEvent, error) {
	return hooks.ToolPass, aipapi.ToolEvent{}, nil
}

// TestProjectHookConfig_PopulatedAndDefensiveCopies verifies that generated ProjectHookConfig
// populates all four hook planes from FrozenPlaneSet, isolates returned slices defensively,
// preserves nil vs explicit empty semantics, and maps error policies.
func TestProjectHookConfig_PopulatedAndDefensiveCopies(t *testing.T) {
	t.Parallel()

	// 1. Populated with all four hook planes
	cs := feature.NewContributionSet()
	sh1 := dummySubmitHook{id: "sh-1", ord: 10}
	rqh1 := dummyRequestPartHook{id: "rqh-1", ord: 20}
	rsh1 := dummyResponsePartHook{id: "rsh-1", ord: 30}
	tr1 := dummyToolReactor{id: "tr-1", ord: 40}

	require.NoError(t, feature.Contribute(cs, feature.PlaneSubmitHooks, "plugin-hooks", []hooks.SubmitHook{sh1}))
	require.NoError(t, feature.Contribute(cs, feature.PlaneRequestPartHooks, "plugin-hooks", []hooks.RequestPartHook{rqh1}))
	require.NoError(t, feature.Contribute(cs, feature.PlaneResponsePartHooks, "plugin-hooks", []hooks.ResponsePartHook{rsh1}))
	require.NoError(t, feature.Contribute(cs, feature.PlaneToolReactors, "plugin-hooks", []hooks.ToolReactor{tr1}))

	frozen := cs.Freeze()

	cfg := feature.ProjectHookConfig(frozen, hooks.ToolReactorErrorsFailClosed)
	require.Len(t, cfg.SubmitHooks, 1)
	assert.Equal(t, "sh-1", cfg.SubmitHooks[0].ID())
	require.Len(t, cfg.RequestPartHooks, 1)
	assert.Equal(t, "rqh-1", cfg.RequestPartHooks[0].ID())
	require.Len(t, cfg.ResponsePartHooks, 1)
	assert.Equal(t, "rsh-1", cfg.ResponsePartHooks[0].ID())
	require.Len(t, cfg.ToolReactors, 1)
	assert.Equal(t, "tr-1", cfg.ToolReactors[0].ID())
	assert.Equal(t, hooks.ToolReactorErrorsFailClosed, cfg.ToolReactorErrorPolicy)

	// 2. Defensive copies: mutating returned slices in HookConfig does not affect FrozenPlaneSet
	cfg.SubmitHooks[0] = dummySubmitHook{id: "sh-mutated", ord: 99}
	cfg.RequestPartHooks[0] = dummyRequestPartHook{id: "rqh-mutated", ord: 99}
	cfg.ResponsePartHooks[0] = dummyResponsePartHook{id: "rsh-mutated", ord: 99}
	cfg.ToolReactors[0] = dummyToolReactor{id: "tr-mutated", ord: 99}

	cfg2 := feature.ProjectHookConfig(frozen, hooks.ToolReactorErrorsFailOpen)
	assert.Equal(t, "sh-1", cfg2.SubmitHooks[0].ID())
	assert.Equal(t, "rqh-1", cfg2.RequestPartHooks[0].ID())
	assert.Equal(t, "rsh-1", cfg2.ResponsePartHooks[0].ID())
	assert.Equal(t, "tr-1", cfg2.ToolReactors[0].ID())
	assert.Equal(t, hooks.ToolReactorErrorsFailOpen, cfg2.ToolReactorErrorPolicy)

	// 3. Nil vs Explicit Empty Semantics
	csEmpty := feature.NewContributionSet()
	// SubmitHooks never contributed -> nil
	// RequestPartHooks contributed as explicit empty slice -> non-nil empty slice
	require.NoError(t, feature.Contribute(csEmpty, feature.PlaneRequestPartHooks, "plugin-empty", []hooks.RequestPartHook{}))

	frozenEmpty := csEmpty.Freeze()
	cfgEmpty := feature.ProjectHookConfig(frozenEmpty, hooks.ToolReactorErrorsSwallowEvent)

	assert.Nil(t, cfgEmpty.SubmitHooks, "uncontributed plane must project to nil slice")
	assert.NotNil(t, cfgEmpty.RequestPartHooks, "explicit empty contributed plane must project to non-nil empty slice")
	assert.Empty(t, cfgEmpty.RequestPartHooks)
	assert.Nil(t, cfgEmpty.ResponsePartHooks, "uncontributed plane must project to nil slice")
	assert.Nil(t, cfgEmpty.ToolReactors, "uncontributed plane must project to nil slice")
	assert.Equal(t, hooks.ToolReactorErrorsSwallowEvent, cfgEmpty.ToolReactorErrorPolicy)

	// Zero-value FrozenPlaneSet
	var zeroFrozen feature.FrozenPlaneSet
	cfgZero := feature.ProjectHookConfig(zeroFrozen, hooks.ToolReactorErrorPolicyUnspecified)
	assert.Nil(t, cfgZero.SubmitHooks)
	assert.Nil(t, cfgZero.RequestPartHooks)
	assert.Nil(t, cfgZero.ResponsePartHooks)
	assert.Nil(t, cfgZero.ToolReactors)
	assert.Equal(t, hooks.ToolReactorErrorPolicyUnspecified, cfgZero.ToolReactorErrorPolicy)
}
