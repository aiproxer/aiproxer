package archtest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDiagnosticsArchitecture_ProductionZeroForbiddenMirrors asserts that
// internal/core/diag/inventory_extensions.go contains no selectors of known FeatureBundle plane fields,
// no manual branches on plane ID string literals, and no calls to family materializers.
func TestDiagnosticsArchitecture_ProductionZeroForbiddenMirrors(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	diagPath := filepath.Join(root, "internal", "core", "diag", "inventory_extensions.go")
	src, err := os.ReadFile(diagPath)
	require.NoError(t, err)

	fset, f, err := ParseGoSource(diagPath, src)
	require.NoError(t, err)

	findings := ScanFileForForbiddenMirrors("internal/core/diag/inventory_extensions.go", src, fset, f, Wave5b_LocalTurnTerminal)
	require.Empty(t, findings, "internal/core/diag/inventory_extensions.go must contain zero forbidden diagnostics mirrors")
}

// TestDiagnosticsArchitecture_TargetFileManualFieldAccessRejected verifies that manual selector
// expressions on FeatureBundle in internal/core/diag/inventory_extensions.go are rejected by the ratchet.
func TestDiagnosticsArchitecture_TargetFileManualFieldAccessRejected(t *testing.T) {
	t.Parallel()

	syntheticSrc := `package diag
import aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"

func readBundleFields(b aipfeature.FeatureBundle) {
	_ = b.ToolCatalogFilters
}
`
	findings := scanSyntheticSource(t, "internal/core/diag/inventory_extensions.go", syntheticSrc, Wave5b_LocalTurnTerminal)
	require.NotEmpty(t, findings, "target file manual plane field access must be rejected")
	assert.Equal(t, MirrorDiagArm, findings[0].ShapeKind)
	assert.Equal(t, "ToolCatalogFilters", findings[0].Identifier)
}

// TestDiagnosticsArchitecture_TargetFileBranchOnPlaneIDRejected verifies that branching on
// raw plane ID string literals in internal/core/diag/inventory_extensions.go is rejected.
func TestDiagnosticsArchitecture_TargetFileBranchOnPlaneIDRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		src       string
		wantPlane string
	}{
		{
			name: "equality comparison against plane ID literal",
			src: `package diag
func checkPlane(planeID string) bool {
	return planeID == "tool_catalog_filters"
}
`,
			wantPlane: "tool_catalog_filters",
		},
		{
			name: "switch case on plane ID literal",
			src: `package diag
func handlePlane(planeID string) {
	switch planeID {
	case "session_openers":
		// manual branch
	}
}
`,
			wantPlane: "session_openers",
		},
		{
			name: "map indexing with plane ID literal",
			src: `package diag
func indexPlane(m map[string]int) int {
	return m["secret_guards"]
}
`,
			wantPlane: "secret_guards",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			findings := scanSyntheticSource(t, "internal/core/diag/inventory_extensions.go", tt.src, Wave5b_LocalTurnTerminal)
			require.NotEmpty(t, findings, "branching on plane ID literal must be rejected")
			assert.Equal(t, MirrorDiagArm, findings[0].ShapeKind)
			assert.Equal(t, tt.wantPlane, findings[0].Identifier)
		})
	}
}

// TestDiagnosticsArchitecture_TargetFileFamilyMaterializerCallRejected verifies that calls to
// family-specific materializers in internal/core/diag/inventory_extensions.go are rejected.
func TestDiagnosticsArchitecture_TargetFileFamilyMaterializerCallRejected(t *testing.T) {
	t.Parallel()

	syntheticSrc := `package diag
import "github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"

func callMaterializer(policies []toolpolicy.Policy) {
	_ = toolpolicy.MaterializeSorted(policies)
}
`
	findings := scanSyntheticSource(t, "internal/core/diag/inventory_extensions.go", syntheticSrc, Wave5b_LocalTurnTerminal)
	require.NotEmpty(t, findings, "calling family materializer in target file must be rejected")
	assert.Equal(t, MirrorDiagArm, findings[0].ShapeKind)
	assert.Equal(t, "MaterializeSorted", findings[0].Identifier)
}

// TestDiagnosticsArchitecture_TargetFileAliasedMaterializerCallRejected verifies that aliased imports of
// canonical SDK packages calling materializers in internal/core/diag/inventory_extensions.go are rejected.
func TestDiagnosticsArchitecture_TargetFileAliasedMaterializerCallRejected(t *testing.T) {
	t.Parallel()

	syntheticSrc := `package diag
import tp "github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"

func callMaterializer(policies []tp.Policy) {
	_ = tp.MaterializeSorted(policies)
}
`
	findings := scanSyntheticSource(t, "internal/core/diag/inventory_extensions.go", syntheticSrc, Wave5b_LocalTurnTerminal)
	require.NotEmpty(t, findings, "calling canonical aliased materializer in target file must be rejected")
	assert.Equal(t, MirrorDiagArm, findings[0].ShapeKind)
	assert.Equal(t, "MaterializeSorted", findings[0].Identifier)
}

// TestDiagnosticsArchitecture_TargetFileForeignMaterializerAccepted verifies that unrelated imported
// materializers (e.g. foreign.MaterializeSorted) in internal/core/diag/inventory_extensions.go are accepted.
func TestDiagnosticsArchitecture_TargetFileForeignMaterializerAccepted(t *testing.T) {
	t.Parallel()

	syntheticSrc := `package diag
import foreign "github.com/example/foreign"

func callForeignMaterializer(items []string) {
	_ = foreign.MaterializeSorted(items)
}
`
	findings := scanSyntheticSource(t, "internal/core/diag/inventory_extensions.go", syntheticSrc, Wave5b_LocalTurnTerminal)
	assert.Empty(t, findings, "unrelated imported foreign.MaterializeSorted in target file must be accepted")
}

// TestDiagnosticsArchitecture_TargetFileForeignStructAccepted verifies that a foreign struct with same-named
// fields inside the target file internal/core/diag/inventory_extensions.go is accepted by the ratchet.
func TestDiagnosticsArchitecture_TargetFileForeignStructAccepted(t *testing.T) {
	t.Parallel()

	syntheticSrc := `package diag
import foreign "github.com/example/foreign"

func readForeign(x foreign.ForeignStruct) []string {
	return x.ToolCatalogFilters
}
`
	findings := scanSyntheticSource(t, "internal/core/diag/inventory_extensions.go", syntheticSrc, Wave5b_LocalTurnTerminal)
	assert.Empty(t, findings, "foreign struct field access in target file must be accepted")
}

// TestDiagnosticsArchitecture_ForeignFileWithSameFieldNameNotFlagged verifies that a foreign struct
// with same-named fields inside a non-target package is not falsely flagged by the diagnostics ratchet.
func TestDiagnosticsArchitecture_ForeignFileWithSameFieldNameNotFlagged(t *testing.T) {
	t.Parallel()

	foreignSrc := `package foreignpkg
import foreign "github.com/example/foreign"

func ReadForeign(f foreign.ForeignStruct) []string {
	return f.ToolCatalogFilters
}
`
	findings := scanSyntheticSource(t, "internal/foreignpkg/foreign.go", foreignSrc, Wave5b_LocalTurnTerminal)
	assert.Empty(t, findings, "foreign non-diag selector must not be flagged")
}

// TestDiagnosticsArchitecture_ForeignSameNamedFunctionNotTrusted verifies that a function in another file
// with the same name as an inventory function is not treated as the diagnostics target file.
func TestDiagnosticsArchitecture_ForeignSameNamedFunctionNotTrusted(t *testing.T) {
	t.Parallel()

	foreignSrc := `package otherpkg
import foreign "github.com/example/foreign"

func buildInventoryExtensions(b foreign.CustomBundle) []string {
	return b.ToolCatalogFilters
}
`
	findings := scanSyntheticSource(t, "internal/otherpkg/other.go", foreignSrc, Wave5b_LocalTurnTerminal)
	assert.Empty(t, findings, "function in foreign file must not trigger diagnostics ratchet")
}

// TestDiagnosticsArchitecture_CommentWithPlaneIDNotFlagged verifies that plane ID strings inside
// comments in the target file do not trigger false positive findings.
func TestDiagnosticsArchitecture_CommentWithPlaneIDNotFlagged(t *testing.T) {
	t.Parallel()

	syntheticSrc := `package diag

// This comment mentions tool_catalog_filters and session_openers without code branching.
func harmlessFunction() string {
	return "ok"
}
`
	findings := scanSyntheticSource(t, "internal/core/diag/inventory_extensions.go", syntheticSrc, Wave5b_LocalTurnTerminal)
	assert.Empty(t, findings, "plane IDs in comments must not trigger diagnostics ratchet findings")
}

// TestDiagnosticsArchitecture_GenericReducerAccepted verifies that the generic reducer pattern
// operating over DiagnosticPlaneProjection fields is fully accepted by the ratchet.
func TestDiagnosticsArchitecture_GenericReducerAccepted(t *testing.T) {
	t.Parallel()

	syntheticSrc := `package diag
import (
	"cmp"
	"slices"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

func genericReducer(projections []aipfeature.DiagnosticPlaneProjection) []InventoryStageOccupancy {
	sorted := make([]aipfeature.DiagnosticPlaneProjection, len(projections))
	copy(sorted, projections)
	slices.SortStableFunc(sorted, func(a, b aipfeature.DiagnosticPlaneProjection) int {
		if c := cmp.Compare(a.Order, b.Order); c != 0 {
			return c
		}
		return cmp.Compare(a.PlaneID, b.PlaneID)
	})
	var result []InventoryStageOccupancy
	for _, p := range sorted {
		_ = p.StageID
		_ = p.CoalesceGroup
		_ = p.Occupants
		_ = p.Privileges
	}
	return result
}
`
	findings := scanSyntheticSource(t, "internal/core/diag/inventory_extensions.go", syntheticSrc, Wave5b_LocalTurnTerminal)
	assert.Empty(t, findings, "generic reducer pattern must be accepted without findings")
}
