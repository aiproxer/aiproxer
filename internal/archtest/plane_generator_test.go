package archtest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPlaneGenerator_DisposableDiagnosticPlaneProof verifies that a synthetic manifest
// with one disposable diagnostic plane automatically generates projection logic in ProjectDiagnostics
// without any code in internal/core/diag/inventory_extensions.go knowing about it.
func TestPlaneGenerator_DisposableDiagnosticPlaneProof(t *testing.T) {
	t.Parallel()

	syntheticManifest := `package feature
import (
	"context"
	"fmt"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
)
var PlaneDisposableProbe = Plane[[]toolpolicy.Policy]{
	ID: "disposable_probe", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilReject, RequestAccess: RequestBodyCanonicalRequired, Identity: func(v []toolpolicy.Policy) (string, bool) { return "", false },
	Validate: func(v []toolpolicy.Policy) error { return nil },
	Combine: func(s SourceKind, c, in []toolpolicy.Policy) ([]toolpolicy.Policy, error) { return append(c, in...), nil },
	Diagnostics: DiagnosticDescriptor[[]toolpolicy.Policy]{
		StageID: StageIDToolEventReaction, CoalesceGroup: "probe_group", Order: 85,
		Materialize: func(v []toolpolicy.Policy) []DiagnosticOccupant { return []DiagnosticOccupant{{Label: "probe_label"}} },
		Privileges: func(v []toolpolicy.Policy) PrivilegeProjection { return PrivilegeProjection{Flags: []string{PrivilegeAuxiliaryRequests}} },
	},
}
var PlaneNonDiag = Plane[[]toolpolicy.Policy]{
	ID: "non_diag_plane", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilReject, RequestAccess: RequestBodyCanonicalRequired, Identity: func(v []toolpolicy.Policy) (string, bool) { return "", false },
	Validate: func(v []toolpolicy.Policy) error { return nil },
	Combine: func(s SourceKind, c, in []toolpolicy.Policy) ([]toolpolicy.Policy, error) { return append(c, in...), nil },
}
var StandardPlanes = []any{PlaneDisposableProbe, PlaneNonDiag}
`
	generatedBytes, err := GenerateFeaturePlanesCode([]byte(syntheticManifest))
	require.NoError(t, err)
	code := string(generatedBytes)

	assert.Contains(t, code, "func ProjectDiagnostics(")
	assert.Contains(t, code, "canonicalPlaneDisposableProbePolicy.materializeOccupants(")
	assert.Contains(t, code, "canonicalPlaneDisposableProbePolicy.projectPrivileges(")
	assert.Contains(t, code, "canonicalPlaneDisposableProbePolicy.diagOrder")
	assert.Contains(t, code, "canonicalPlaneDisposableProbePolicy.diagCoalesceGroup")
	assert.NotContains(t, code, "canonicalPlaneNonDiagPolicy.materializeOccupants(")
	assert.NotContains(t, code, "PlaneDisposableProbe.MaterializeOccupants(")
	assert.NotContains(t, code, "PlaneDisposableProbe.ProjectPrivileges(")

	root := repoRoot(t)
	diagPath := filepath.Join(root, "internal", "core", "diag", "inventory_extensions.go")
	prodSrc, err := os.ReadFile(diagPath)
	require.NoError(t, err)
	assert.NotContains(t, string(prodSrc), "disposable_probe", "production reducer must never reference disposable probe plane")
}
