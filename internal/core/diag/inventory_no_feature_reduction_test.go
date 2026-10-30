package diag_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestInventorySurface_NoFeaturePlaneReductionInDiagnostics pins the boundary
// established by the inventory-featurehost relocation: diagnostics consumes
// finished inventory rows only. The compiled-inventory read model (diagnostic
// projection reduction and the secret-guard instance association) is computed
// by the feature/composition authority, so this package must contain neither
// the feature-plane projection type nor the concrete secret-guard factory
// literal.
func TestInventorySurface_NoFeaturePlaneReductionInDiagnostics(t *testing.T) {
	t.Parallel()

	root := diagRepoRoot(t)
	dir := filepath.Join(root, "internal", "core", "diag")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	scanned := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, err)
		scanned++
		text := string(src)
		require.NotContains(t, text, "DiagnosticPlaneProjection",
			"%s must not reduce feature-plane projections; featurehost computes the read model", entry.Name())
		require.NotContains(t, strings.ToLower(text), "secrets-guard",
			"%s must not know the concrete secret-guard factory; featurehost associates the posture", entry.Name())
	}
	require.Positive(t, scanned, "the diagnostics package must be scanned")

	// The relocated authority must define the computation instead.
	authority, err := os.ReadFile(filepath.Join(root, "internal", "standardplugins", "featurehost", "inventory.go"))
	require.NoError(t, err)
	for _, decl := range []string{
		"func InventoryMetadataFromMergeSurface(",
		"func InventoryMetadataForConfig(",
		"func InventoryFeatureMetadataFromProjections(",
	} {
		require.Contains(t, string(authority), decl,
			"the compiled inventory read model must be computed in the featurehost authority")
	}
}
