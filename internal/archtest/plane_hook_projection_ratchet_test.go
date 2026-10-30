package archtest

import (
	"go/ast"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHookProjectionRatchet_RealBuildFeatureHooksZeroMirrors verifies Requirement 3.4, 3.5, 5.1 (Task 3.4):
// The real production build_feature_hooks.go contains zero forbidden mirror violations
// under Wave 1 (HookBus) and Wave 5c (Residual) when scanned without any allowlist exemption.
func TestHookProjectionRatchet_RealBuildFeatureHooksZeroMirrors(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	relPath := "internal/infra/runtimebundle/build_feature_hooks.go"
	absPath := filepath.Join(root, filepath.FromSlash(relPath))

	src, err := os.ReadFile(absPath)
	require.NoError(t, err, "failed to read production file %s", absPath)

	fset, f, err := ParseGoSource(absPath, src)
	require.NoError(t, err, "failed to parse production file %s", absPath)

	t.Run("Wave1_HookBus_ScanWithoutAllowlist", func(t *testing.T) {
		t.Parallel()
		findings := ScanFileForForbiddenMirrors(relPath, src, fset, f, Wave1_HookBus)
		require.Empty(t, findings, "production build_feature_hooks.go must contain zero forbidden hook projection mirrors without allowlist exemption")
	})

	t.Run("Wave5c_Residual_ScanWithoutAllowlist", func(t *testing.T) {
		t.Parallel()
		findings := ScanFileForForbiddenMirrors(relPath, src, fset, f, Wave5c_Residual)
		require.Empty(t, findings, "production build_feature_hooks.go must contain zero forbidden hook projection mirrors at Wave5c")
	})
}

// TestHookProjectionRatchet_SyntheticHandwrittenProjectionRejected verifies Requirement 3.4, 5.1 (Task 3.4):
// Any synthetic handwritten hook projection that reads the four declared hook planes via Get is detected
// and rejected with MirrorProjectionBranch violations at Wave 1.
func TestHookProjectionRatchet_SyntheticHandwrittenProjectionRejected(t *testing.T) {
	t.Parallel()

	syntheticHookProjSrc := `package runtimebundle

import (
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

func ManualHookProjection(f aipfeature.FrozenPlaneSet, p sdkhooks.ToolReactorErrorPolicy) hooks.Config {
	return hooks.Config{
		SubmitHooks:       aipfeature.Get(f, aipfeature.PlaneSubmitHooks),
		RequestPartHooks:  aipfeature.Get(f, aipfeature.PlaneRequestPartHooks),
		ResponsePartHooks: aipfeature.Get(f, aipfeature.PlaneResponsePartHooks),
		ToolReactors:      aipfeature.Get(f, aipfeature.PlaneToolReactors),
	}
}
`

	fset, f, err := ParseGoSource("internal/infra/runtimebundle/build_feature_hooks.go", []byte(syntheticHookProjSrc))
	require.NoError(t, err)

	findings := ScanFileForForbiddenMirrors("internal/infra/runtimebundle/build_feature_hooks.go", []byte(syntheticHookProjSrc), fset, f, Wave1_HookBus)

	require.Len(t, findings, 4, "must detect exactly four forbidden hook projection branches")

	planeIDsFound := make(map[string]bool)
	for _, finding := range findings {
		assert.Equal(t, MirrorProjectionBranch, finding.ShapeKind)
		planeIDsFound[finding.PlaneID] = true
	}

	assert.True(t, planeIDsFound["submit_hooks"], "must find forbidden projection branch for submit_hooks")
	assert.True(t, planeIDsFound["request_part_hooks"], "must find forbidden projection branch for request_part_hooks")
	assert.True(t, planeIDsFound["response_part_hooks"], "must find forbidden projection branch for response_part_hooks")
	assert.True(t, planeIDsFound["tool_reactors"], "must find forbidden projection branch for tool_reactors")
	assert.False(t, planeIDsFound["tool_reactor_error_policy"], "host tool reactor error policy is not a feature plane")
	assert.Equal(t, map[string]bool{
		"submit_hooks":        true,
		"request_part_hooks":  true,
		"response_part_hooks": true,
		"tool_reactors":       true,
	}, planeIDsFound, "must find exactly the four declared feature hook plane IDs")
}

// TestHookProjectionRatchet_GeneratedVsHandwrittenContract verifies that ProjectHookConfig in
// pkg/aipsdk/feature/plane_generated.go is ignored solely due to the generated-file contract
// (marker/name), whereas an identical handwritten copy located under a production path (e.g.
// internal/infra/runtimebundle/build_feature_hooks.go) without the generated marker is strictly rejected.
func TestHookProjectionRatchet_GeneratedVsHandwrittenContract(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	genRelPath := "pkg/aipsdk/feature/plane_generated.go"
	genAbsPath := filepath.Join(root, filepath.FromSlash(genRelPath))

	genSrc, err := os.ReadFile(genAbsPath)
	require.NoError(t, err, "failed to read %s", genAbsPath)

	// 1. Scanned as generated file at its actual path: 0 findings (skipped by IsGeneratedFile)
	genFset, genF, err := ParseGoSource(genAbsPath, genSrc)
	require.NoError(t, err, "failed to parse generated file %s", genAbsPath)
	genFindings := ScanFileForForbiddenMirrors(genRelPath, genSrc, genFset, genF, Wave1_HookBus)
	require.Empty(t, genFindings, "generated plane_generated.go must produce 0 findings at actual path")

	// Locate exact ProjectHookConfig FuncDecl in the parsed generated file
	var projFn *ast.FuncDecl
	for _, decl := range genF.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "ProjectHookConfig" {
			projFn = fn
			break
		}
	}
	require.NotNil(t, projFn, "ProjectHookConfig must be declared in %s", genRelPath)
	assert.Equal(t, "ProjectHookConfig", projFn.Name.Name)
	assert.Nil(t, projFn.Recv, "ProjectHookConfig must be a package-level function")

	// Assert extracted function contains exactly one return statement
	var returnCount int
	ast.Inspect(projFn.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.ReturnStmt); ok {
			returnCount++
		}
		return true
	})
	assert.Equal(t, 1, returnCount, "ProjectHookConfig must contain exactly one return statement")

	// Safely extract exact function source text from generated bytes using token offsets
	fnStart := genFset.Position(projFn.Pos()).Offset
	fnEnd := genFset.Position(projFn.End()).Offset
	require.True(t, fnStart >= 0 && fnEnd > fnStart && fnEnd <= len(genSrc), "valid byte offsets for ProjectHookConfig")
	extractedFnSrc := string(genSrc[fnStart:fnEnd])

	// 2. An isolated handwritten copy containing only ProjectHookConfig placed in production runtimebundle without generated marker is REJECTED
	syntheticHandwritten := "package runtimebundle\n\n" + extractedFnSrc + "\n"

	prodRelPath := "internal/infra/runtimebundle/build_feature_hooks.go"
	hwFset, hwF, err := ParseGoSource(filepath.Join(root, filepath.FromSlash(prodRelPath)), []byte(syntheticHandwritten))
	require.NoError(t, err, "failed to parse synthetic handwritten source")

	hwFindings := ScanFileForForbiddenMirrors(prodRelPath, []byte(syntheticHandwritten), hwFset, hwF, Wave1_HookBus)
	require.Len(t, hwFindings, 4, "handwritten copy of ProjectHookConfig in production path without generated marker must produce mirror findings for the four hook feature planes")

	var hwProjFn *ast.FuncDecl
	for _, decl := range hwF.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "ProjectHookConfig" {
			hwProjFn = fn
			break
		}
	}
	require.NotNil(t, hwProjFn, "synthetic source must declare ProjectHookConfig")
	hwFnStartLine := hwFset.Position(hwProjFn.Pos()).Line
	hwFnEndLine := hwFset.Position(hwProjFn.End()).Line

	foundPlanes := make(map[string]bool)
	for _, f := range hwFindings {
		assert.Equal(t, MirrorProjectionBranch, f.ShapeKind, "finding shape kind must be MirrorProjectionBranch")
		assert.True(t, f.Line >= hwFnStartLine && f.Line <= hwFnEndLine, "finding line %d must belong within ProjectHookConfig lines [%d, %d]", f.Line, hwFnStartLine, hwFnEndLine)
		foundPlanes[f.PlaneID] = true
	}
	assert.True(t, foundPlanes["submit_hooks"], "must detect submit_hooks")
	assert.True(t, foundPlanes["request_part_hooks"], "must detect request_part_hooks")
	assert.True(t, foundPlanes["response_part_hooks"], "must detect response_part_hooks")
	assert.True(t, foundPlanes["tool_reactors"], "must detect tool_reactors")
	assert.False(t, foundPlanes["tool_reactor_error_policy"], "host tool reactor error policy is not a feature plane")
	assert.Equal(t, map[string]bool{
		"submit_hooks":        true,
		"request_part_hooks":  true,
		"response_part_hooks": true,
		"tool_reactors":       true,
	}, foundPlanes, "must detect exactly the four declared feature hook plane IDs in ProjectHookConfig")
}
