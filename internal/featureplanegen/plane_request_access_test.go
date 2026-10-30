package featureplanegen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPlaneGenerator_RejectsMissingRequestAccess verifies that a plane without
// an explicit RequestAccess annotation fails generation (fail closed for new
// or unclassified planes).
func TestPlaneGenerator_RejectsMissingRequestAccess(t *testing.T) {
	t.Parallel()

	manifest := `package feature
import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
var PlaneNoAccess = Plane[[]hooks.SubmitHook]{
	ID: "no_access_plane", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable,
	Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(c, in...), nil },
}
var StandardPlanes = []any{PlaneNoAccess}
`
	_, err := GenerateFeaturePlanesCode([]byte(manifest))
	require.Error(t, err, "plane without RequestAccess must fail generation")
	assert.Contains(t, err.Error(), "PlaneNoAccess")
	assert.Contains(t, err.Error(), "request access")
}

// TestPlaneGenerator_RejectsUnclassifiedRequestAccess verifies that an
// explicit Unclassified annotation fails generation.
func TestPlaneGenerator_RejectsUnclassifiedRequestAccess(t *testing.T) {
	t.Parallel()

	manifest := `package feature
import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
var PlaneUnclassified = Plane[[]hooks.SubmitHook]{
	ID: "unclassified_plane", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, RequestAccess: RequestBodyAccessUnclassified,
	Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(c, in...), nil },
}
var StandardPlanes = []any{PlaneUnclassified}
`
	_, err := GenerateFeaturePlanesCode([]byte(manifest))
	require.Error(t, err, "Unclassified plane must fail generation")
	assert.Contains(t, err.Error(), "PlaneUnclassified")
	assert.Contains(t, err.Error(), "Unclassified")
}

// TestPlaneGenerator_RejectsUnknownRequestAccessIdentifier verifies that an
// unknown access identifier fails generation.
func TestPlaneGenerator_RejectsUnknownRequestAccessIdentifier(t *testing.T) {
	t.Parallel()

	manifest := `package feature
import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
var PlaneBadAccess = Plane[[]hooks.SubmitHook]{
	ID: "bad_access_plane", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, RequestAccess: RequestBodyAccessTypo,
	Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(c, in...), nil },
}
var StandardPlanes = []any{PlaneBadAccess}
`
	_, err := GenerateFeaturePlanesCode([]byte(manifest))
	require.Error(t, err, "unknown request access identifier must fail generation")
	assert.Contains(t, err.Error(), "PlaneBadAccess")
	assert.Contains(t, err.Error(), "RequestBodyAccessTypo")
}

// TestPlaneGenerator_AnnotatedPlaneEmitsRequestAccessBinding verifies that an
// annotated plane generates a canonical policy binding carrying the declared
// request access class from the manifest plane.
func TestPlaneGenerator_AnnotatedPlaneEmitsRequestAccessBinding(t *testing.T) {
	t.Parallel()

	manifest := `package feature
import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
var PlaneSyntheticAccess = Plane[[]hooks.SubmitHook]{
	ID: "synthetic_access", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, RequestAccess: RequestBodyResponseOnly,
	Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(c, in...), nil },
}
var StandardPlanes = []any{PlaneSyntheticAccess}
`
	codeBytes, err := GenerateFeaturePlanesCode([]byte(manifest))
	require.NoError(t, err, "annotated plane must generate successfully")
	code := string(codeBytes)
	assert.Contains(t, code, "requestAccess:", "generated policy must bind requestAccess")
	assert.Contains(t, code, "PlaneSyntheticAccess.RequestAccess",
		"generated policy must capture requestAccess from the manifest plane")
}
