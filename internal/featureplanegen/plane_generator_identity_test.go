package featureplanegen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPlaneGenerator_SyntheticReplaceByIdentityPlaneEmitsIdentityFields asserts that code generation
// emits typed identity fields, copying logic, identity extraction, and validation for ordered replace-by-identity planes.
func TestPlaneGenerator_SyntheticReplaceByIdentityPlaneEmitsIdentityFields(t *testing.T) {
	t.Parallel()

	syntheticManifest := `package feature
import (
	"fmt"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
)
var PlaneSyntheticReplace = Plane[[]toolpolicy.Policy]{
	ID: "synthetic_replace", Multiplicity: MultOrdered,
	Rules: SourceRules{
		Feature: CombConcatenate,
		GenerationBinder: CombReplaceByIdentity,
	},
	NilPolicy: NilReject,
	RequestAccess: RequestBodyCanonicalRequired,
	Identity: func(v []toolpolicy.Policy) (string, bool) {
		if len(v) > 0 && v[0] != nil {
			return v[0].ID(), true
		}
		return "", false
	},
	ValidateIdentity: func(id string) error {
		if id == "" {
			return fmt.Errorf("empty identity")
		}
		return nil
	},
	Validate: func(v []toolpolicy.Policy) error { return nil },
	Combine: func(s SourceKind, c, in []toolpolicy.Policy) ([]toolpolicy.Policy, error) { return append(c, in...), nil },
}
var StandardPlanes = []any{PlaneSyntheticReplace}
`
	generatedBytes, err := GenerateFeaturePlanesCode([]byte(syntheticManifest))
	require.NoError(t, err)
	code := string(generatedBytes)

	// 1. Verify fields in generatedContributions & generatedFrozen
	assert.Contains(t, code, "syntheticReplaceID")
	assert.Contains(t, code, "syntheticReplaceHasID")

	// 2. Verify clone / freeze / toContributions copy identity fields
	assert.Contains(t, code, "next.syntheticReplaceID = gc.syntheticReplaceID")
	assert.Contains(t, code, "next.syntheticReplaceHasID = gc.syntheticReplaceHasID")
	assert.Contains(t, code, "gc.syntheticReplaceID = gf.syntheticReplaceID")
	assert.Contains(t, code, "gc.syntheticReplaceHasID = gf.syntheticReplaceHasID")

	// 3. Verify contribute closure extracts identity
	assert.Contains(t, code, "id, hasID := canonicalPlaneSyntheticReplacePolicy.identity(gc.syntheticReplace)")
	assert.Contains(t, code, "gc.syntheticReplaceID = id")
	assert.Contains(t, code, "gc.syntheticReplaceHasID = hasID")

	// 4. Verify identity closure returns cached fields
	assert.Contains(t, code, "return gf.syntheticReplaceID, gf.syntheticReplaceHasID")

	// 5. Verify validate checks structural metadata and calls ValidateIdentity
	assert.Contains(t, code, "malformed metadata without value")
	assert.Contains(t, code, "missing cached identity")
	assert.Contains(t, code, "canonicalPlaneSyntheticReplacePolicy.validateIdentity(gf.syntheticReplaceID)")

	// 6. Verify replayAllPlanesTo combines slices directly and preserves cached IDs without calling live Identity
	assert.Contains(t, code, "combined, err := canonicalPlaneSyntheticReplacePolicy.combine(source, current, incoming)")
	assert.Contains(t, code, "gc.syntheticReplaceID = gf.syntheticReplaceID")
	assert.Contains(t, code, "gc.syntheticReplaceHasID = gf.syntheticReplaceHasID")

	// 7. Verify hasIdentityReplayRule is generated
	assert.Contains(t, code, "func (gf *generatedFrozen) hasIdentityReplayRule(")
	assert.Contains(t, code, "canonicalPlaneSyntheticReplacePolicy.rules.RuleFor(source) == rule")

	// 8. Verify map-backed replay/validation helpers are NOT generated
	assert.NotContains(t, code, "validateAllPlanesMap")
	assert.NotContains(t, code, "replayAllPlanesMapTo")
	assert.NotContains(t, code, "mapHasIdentityReplayRule")
	assert.NotContains(t, code, "contributeCandidateMapTo")
}
