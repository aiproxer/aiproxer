package featureplanegen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlaneGenerator_RequestBorrowValidation(t *testing.T) {
	t.Parallel()

	makeManifest := func(typeParam, mult, rules string, hasMat, reqBorrow bool) string {
		matField := ""
		if hasMat {
			matField = "\n\tRequestMaterializer: func(v " + typeParam + ") " + typeParam + " { return v },"
		}
		borrowField := ""
		if reqBorrow {
			borrowField = "\n\tRequestBorrow: true,"
		}
		return `package feature
import (
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
)
var PlaneTest = Plane[` + typeParam + `]{
	ID: "test_policies", Multiplicity: ` + mult + `, Rules: SourceRules{Feature: ` + rules + `},
	NilPolicy: NilReject, RequestAccess: RequestBodyCanonicalRequired,
	Identity: func(v ` + typeParam + `) (string, bool) { return "id", true },
	ValidateIdentity: func(id string) error { return nil },
	Validate: func(v ` + typeParam + `) error { return nil },
	Combine: func(s SourceKind, c, in ` + typeParam + `) (` + typeParam + `, error) { return in, nil },` + matField + borrowField + `
}
var StandardPlanes = []any{PlaneTest}
`
	}

	_, err := GenerateFeaturePlanesCode([]byte(makeManifest("[]toolpolicy.Policy", "MultOrdered", "CombConcatenate", true, true)))
	require.NoError(t, err, "valid slice plane with RequestBorrow and RequestMaterializer should succeed")

	// Non-slice plane with RequestBorrow: true must fail
	_, err = GenerateFeaturePlanesCode([]byte(makeManifest("toolpolicy.Policy", "MultExclusive", "CombExclusive", true, true)))
	require.Error(t, err, "non-slice plane with RequestBorrow: true must fail")
	assert.Contains(t, err.Error(), "RequestBorrow cannot be used on non-slice plane type")

	// Slice plane with RequestBorrow: true but no RequestMaterializer must fail
	_, err = GenerateFeaturePlanesCode([]byte(makeManifest("[]toolpolicy.Policy", "MultOrdered", "CombConcatenate", false, true)))
	require.Error(t, err, "slice plane with RequestBorrow: true but no RequestMaterializer must fail")
	assert.Contains(t, err.Error(), "RequestBorrow requires non-nil RequestMaterializer")
}

// TestPlaneGenerator_ValidateIdentityRequirement tests generator validation of ValidateIdentity presence on identity planes.
func TestPlaneGenerator_ValidateIdentityRequirement(t *testing.T) {
	t.Parallel()

	identManifest := func(mult, rules, extra string) string {
		return `package feature
import "github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
var PlaneEx = Plane[terminaldecision.Provider]{
	ID: "test_id_plane", Multiplicity: ` + mult + `, Rules: SourceRules{` + rules + `},
	RequestAccess: RequestBodyCanonicalRequired,
	Identity: func(v terminaldecision.Provider) (string, bool) { return "id", true },` + extra + `
	Combine: func(s SourceKind, c, in terminaldecision.Provider) (terminaldecision.Provider, error) { return in, nil },
}
var StandardPlanes = []any{PlaneEx}
`
	}

	// 1. Exclusive plane missing ValidateIdentity fails
	_, err := GenerateFeaturePlanesCode([]byte(identManifest("MultExclusive", "Feature: CombExclusive", "")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cached identity validator is required for exclusive or replace-by-identity plane")

	// 2. Replace-by-identity plane missing ValidateIdentity fails
	_, err = GenerateFeaturePlanesCode([]byte(identManifest("MultOrdered", "GenerationBinder: CombReplaceByIdentity", "")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cached identity validator is required for exclusive or replace-by-identity plane")

	// 3. Identity plane with ValidateIdentity succeeds
	_, err = GenerateFeaturePlanesCode([]byte(identManifest("MultExclusive", "Feature: CombExclusive", "\n\tValidateIdentity: func(id string) error { return nil },")))
	require.NoError(t, err)

	// 4. Ordered plane with Host: CombExclusive, Identity present, ValidateIdentity missing => fail
	_, err = GenerateFeaturePlanesCode([]byte(identManifest("MultOrdered", "Host: CombExclusive", "")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cached identity validator is required for exclusive or replace-by-identity plane")

	// 5. Ordered plane with GenerationBinder: CombExclusive, Identity present, ValidateIdentity missing => fail
	_, err = GenerateFeaturePlanesCode([]byte(identManifest("MultOrdered", "GenerationBinder: CombExclusive", "")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cached identity validator is required for exclusive or replace-by-identity plane")

	// 6. Ordered plane with Host: CombExclusive with ValidateIdentity validator => succeeds
	_, err = GenerateFeaturePlanesCode([]byte(identManifest("MultOrdered", "Host: CombExclusive", "\n\tValidateIdentity: func(id string) error { return nil },")))
	require.NoError(t, err)

	// 7. Ordered plane with GenerationBinder: CombExclusive with ValidateIdentity validator => succeeds
	_, err = GenerateFeaturePlanesCode([]byte(identManifest("MultOrdered", "GenerationBinder: CombExclusive", "\n\tValidateIdentity: func(id string) error { return nil },")))
	require.NoError(t, err)

	// 8. Ordered plane with Feature: CombExclusive is rejected earlier by multiplicity contract
	_, err = GenerateFeaturePlanesCode([]byte(identManifest("MultOrdered", "Feature: CombExclusive", "")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ordered plane cannot use CombExclusive rule on feature source")
}

// TestPlaneGenerator_FreezeRequestMaterializerWiring verifies that slice planes with RequestMaterializer
// emit freezeRequest using materializeRequestSlice rather than calling RequestMaterializer directly.
func TestPlaneGenerator_FreezeRequestMaterializerWiring(t *testing.T) {
	t.Parallel()

	manifest := `package feature
import (
	"context"
	"fmt"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
)
var PlaneSyntheticSlice = Plane[[]toolpolicy.Policy]{
	ID: "synthetic_slice", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilReject, RequestAccess: RequestBodyCanonicalRequired, Identity: func(v []toolpolicy.Policy) (string, bool) { return "", false },
	Validate: func(v []toolpolicy.Policy) error { return nil },
	Combine: func(s SourceKind, c, in []toolpolicy.Policy) ([]toolpolicy.Policy, error) { return append(c, in...), nil },
	RequestMaterializer: func(v []toolpolicy.Policy) []toolpolicy.Policy { return v },
}
var StandardPlanes = []any{PlaneSyntheticSlice}
`
	generatedBytes, err := GenerateFeaturePlanesCode([]byte(manifest))
	require.NoError(t, err)
	generatedCode := string(generatedBytes)

	assert.Contains(t, generatedCode, "materializeRequestSlice(gf.syntheticSlice, canonicalPlaneSyntheticSlicePolicy.requestMaterializer)")
	assert.NotContains(t, generatedCode, "cloneSlice(canonicalPlaneSyntheticSlicePolicy.requestMaterializer(gf.syntheticSlice))")
	assert.NotContains(t, generatedCode, "canonicalPlaneSyntheticSlicePolicy.requestMaterializer(gf.syntheticSlice)")
	assert.NotContains(t, generatedCode, "PlaneSyntheticSlice.RequestMaterializer(gf.syntheticSlice)")
}

// TestPlaneGenerator_DiagnosticsValidationErrors verifies cross-plane and privilege AST validation.
func TestPlaneGenerator_DiagnosticsValidationErrors(t *testing.T) {
	t.Parallel()

	diagManifest := func(diagBody string) string {
		return `package feature
import (
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
)
var PlaneA = Plane[[]toolpolicy.Policy]{
	ID: "plane_a", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilReject, Identity: func(v []toolpolicy.Policy) (string, bool) { return "", false },
	Validate: func(v []toolpolicy.Policy) error { return nil },
	Combine: func(s SourceKind, c, in []toolpolicy.Policy) ([]toolpolicy.Policy, error) { return append(c, in...), nil },
	Diagnostics: DiagnosticDescriptor[[]toolpolicy.Policy]{` + diagBody + `},
}
var StandardPlanes = []any{PlaneA}
`
	}

	twoPlaneManifest := func(diagA, diagB string) string {
		return `package feature
import (
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
)
var PlaneA = Plane[[]toolpolicy.Policy]{
	ID: "plane_a", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilReject, RequestAccess: RequestBodyCanonicalRequired, Identity: func(v []toolpolicy.Policy) (string, bool) { return "", false },
	Validate: func(v []toolpolicy.Policy) error { return nil },
	Combine: func(s SourceKind, c, in []toolpolicy.Policy) ([]toolpolicy.Policy, error) { return append(c, in...), nil },
	Diagnostics: DiagnosticDescriptor[[]toolpolicy.Policy]{` + diagA + `},
}
var PlaneB = Plane[[]toolpolicy.Policy]{
	ID: "plane_b", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilReject, RequestAccess: RequestBodyCanonicalRequired, Identity: func(v []toolpolicy.Policy) (string, bool) { return "", false },
	Validate: func(v []toolpolicy.Policy) error { return nil },
	Combine: func(s SourceKind, c, in []toolpolicy.Policy) ([]toolpolicy.Policy, error) { return append(c, in...), nil },
	Diagnostics: DiagnosticDescriptor[[]toolpolicy.Policy]{` + diagB + `},
}
var StandardPlanes = []any{PlaneA, PlaneB}
`
	}

	tests := []struct {
		name        string
		manifest    string
		wantErrPart string
	}{
		{name: "duplicate positive order without coalesce group fails", manifest: twoPlaneManifest("StageID: StageIDToolEventReaction, Order: 10, Materialize: func(v []toolpolicy.Policy) []DiagnosticOccupant { return nil }", "StageID: StageIDToolEventReaction, Order: 10, Materialize: func(v []toolpolicy.Policy) []DiagnosticOccupant { return nil }"), wantErrPart: "duplicate diagnostic order 10"},
		{name: "same coalesce group with mismatching StageID fails", manifest: twoPlaneManifest(`StageID: StageIDToolEventReaction, Order: 10, CoalesceGroup: "shared_group", Materialize: func(v []toolpolicy.Policy) []DiagnosticOccupant { return nil }`, `StageID: StageIDPreRequest, Order: 20, CoalesceGroup: "shared_group", Materialize: func(v []toolpolicy.Policy) []DiagnosticOccupant { return nil }`), wantErrPart: "mismatching stage IDs for coalesce group"},
		{name: "unknown privilege flag string is rejected", manifest: diagManifest(`StageID: StageIDToolEventReaction, Order: 10, Materialize: func(v []toolpolicy.Policy) []DiagnosticOccupant { return nil }, Privileges: func(v []toolpolicy.Policy) PrivilegeProjection { return PrivilegeProjection{Flags: []string{"invalid_privilege_typo"}} }`), wantErrPart: `plane PlaneA: unknown privilege flag "invalid_privilege_typo"`},
		{name: "unknown privilege flag identifier is rejected", manifest: diagManifest(`StageID: StageIDToolEventReaction, Order: 10, Materialize: func(v []toolpolicy.Policy) []DiagnosticOccupant { return nil }, Privileges: func(v []toolpolicy.Policy) PrivilegeProjection { return PrivilegeProjection{Flags: []string{PrivilegeTypo}} }`), wantErrPart: `plane PlaneA: unknown privilege flag identifier "PrivilegeTypo"`},
		{name: "foreign selector privilege is rejected", manifest: diagManifest(`StageID: StageIDToolEventReaction, Order: 10, Materialize: func(v []toolpolicy.Policy) []DiagnosticOccupant { return nil }, Privileges: func(v []toolpolicy.Policy) PrivilegeProjection { return PrivilegeProjection{Flags: []string{foreign.PrivilegeRawCapture}} }`), wantErrPart: `plane PlaneA: privilege selector expression "foreign.PrivilegeRawCapture" not allowed; must use bare identifier or string literal`},
		{name: "order provided without StageID is rejected", manifest: diagManifest(`Order: 10,`), wantErrPart: "diagnostics StageID must not be empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := GenerateFeaturePlanesCode([]byte(tt.manifest))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErrPart)
		})
	}
}

// TestPlaneGenerator_PrivilegesFunctionValidation verifies the strict control-flow AST validation
// for Privileges function definitions against evasion techniques and dynamic bypasses.
func TestPlaneGenerator_PrivilegesFunctionValidation(t *testing.T) {
	t.Parallel()

	privManifest := func(privFuncBody string) string {
		return `package feature
import (
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
)
var PlaneA = Plane[[]toolpolicy.Policy]{
	ID: "plane_a", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilReject, RequestAccess: RequestBodyCanonicalRequired, Identity: func(v []toolpolicy.Policy) (string, bool) { return "", false },
	Validate: func(v []toolpolicy.Policy) error { return nil },
	Combine: func(s SourceKind, c, in []toolpolicy.Policy) ([]toolpolicy.Policy, error) { return append(c, in...), nil },
	Diagnostics: DiagnosticDescriptor[[]toolpolicy.Policy]{
		StageID: StageIDToolEventReaction, Order: 10,
		Materialize: func(v []toolpolicy.Policy) []DiagnosticOccupant { return nil },
		Privileges: ` + privFuncBody + `,
	},
}
var StandardPlanes = []any{PlaneA}
`
	}

	tests := []struct {
		name        string
		body        string
		wantErrPart string
	}{
		{name: "1. Valid current if+two static returns accepted", body: `func(v []toolpolicy.Policy) PrivilegeProjection { if len(v) > 0 { return PrivilegeProjection{Flags: []string{PrivilegeRawCapture}} }; return PrivilegeProjection{} }`},
		{name: "2. Assignment bypass rejected", body: `func(v []toolpolicy.Policy) PrivilegeProjection { p := PrivilegeProjection{}; p.Flags = []string{PrivilegeRawCapture}; return p }`, wantErrPart: "plane PlaneA: unsupported statement type *ast.AssignStmt"},
		{name: "5. Direct dynamic return rejected", body: `func(v []toolpolicy.Policy) PrivilegeProjection { return helper(v) }`, wantErrPart: "plane PlaneA: unsupported return expression (*ast.CallExpr)"},
		{name: "6. Identifier return rejected", body: `func(v []toolpolicy.Policy) PrivilegeProjection { return p }`, wantErrPart: "plane PlaneA: unsupported return expression (*ast.Ident)"},
		{name: "7. Foreign projection type rejected", body: `func(v []toolpolicy.Policy) PrivilegeProjection { return foreign.PrivilegeProjection{Flags: []string{"raw_capture"}} }`, wantErrPart: `plane PlaneA: foreign projection type "foreign.PrivilegeProjection" not allowed; must use local PrivilegeProjection`},
		{name: "8. Unsupported loop statement rejected", body: `func(v []toolpolicy.Policy) PrivilegeProjection { for i := 0; i < len(v); i++ { return PrivilegeProjection{} }; return PrivilegeProjection{} }`, wantErrPart: "plane PlaneA: unsupported statement type *ast.ForStmt"},
		{name: "9. Unsupported switch statement rejected", body: `func(v []toolpolicy.Policy) PrivilegeProjection { switch len(v) { case 0: return PrivilegeProjection{}; default: return PrivilegeProjection{Flags: []string{PrivilegeRawCapture}} } }`, wantErrPart: "plane PlaneA: unsupported statement type *ast.SwitchStmt"},
		{name: "10. Direct static canonical literal/identifier returns accepted", body: `func(v []toolpolicy.Policy) PrivilegeProjection { return PrivilegeProjection{Flags: []string{"raw_capture", "auxiliary_requests"}} }`},
		{name: "11. Condition helper call rejected", body: `func(v []toolpolicy.Policy) PrivilegeProjection { if helper(v) { return PrivilegeProjection{Flags: []string{PrivilegeRawCapture}} }; return PrivilegeProjection{} }`, wantErrPart: `plane PlaneA: call to helper function "helper" is not allowed in privilege condition`},
		{name: "12. Condition foreign selector call rejected", body: `func(v []toolpolicy.Policy) PrivilegeProjection { if foreign.Mutate(v) { return PrivilegeProjection{Flags: []string{PrivilegeRawCapture}} }; return PrivilegeProjection{} }`, wantErrPart: `plane PlaneA: selector/method call "foreign.Mutate" is not allowed in privilege condition`},
		{name: "13. Condition len with helper argument rejected", body: `func(v []toolpolicy.Policy) PrivilegeProjection { if len(helper(v)) > 0 { return PrivilegeProjection{Flags: []string{PrivilegeRawCapture}} }; return PrivilegeProjection{} }`, wantErrPart: `plane PlaneA: len argument in privilege condition must be a bare parameter identifier, got *ast.CallExpr`},
		{name: "14. Condition method call rejected", body: `func(v []toolpolicy.Policy) PrivilegeProjection { if v.Check() { return PrivilegeProjection{Flags: []string{PrivilegeRawCapture}} }; return PrivilegeProjection{} }`, wantErrPart: `plane PlaneA: selector/method call "v.Check" is not allowed in privilege condition`},
		{name: "15. If statement with init statement rejected", body: `func(v []toolpolicy.Policy) PrivilegeProjection { if x := 1; len(v) > x { return PrivilegeProjection{Flags: []string{PrivilegeRawCapture}} }; return PrivilegeProjection{} }`, wantErrPart: `plane PlaneA: unsupported statement type *ast.AssignStmt`},
		{name: "16. Condition nested function call rejected", body: `func(v []toolpolicy.Policy) PrivilegeProjection { if func() bool { return true }() { return PrivilegeProjection{Flags: []string{PrivilegeRawCapture}} }; return PrivilegeProjection{} }`, wantErrPart: `plane PlaneA: dynamic function literal call in privilege condition is not allowed`},
		{name: "17. Valid parenthesized condition with boolean literal accepted", body: `func(v []toolpolicy.Policy) PrivilegeProjection { if (len(v) > 0) && true { return PrivilegeProjection{Flags: []string{PrivilegeRawCapture}} }; return PrivilegeProjection{} }`},
		{name: "18. Untyped elided Flags literal rejected", body: `func(v []toolpolicy.Policy) PrivilegeProjection { return PrivilegeProjection{Flags: {"raw_capture"}} }`, wantErrPart: `plane PlaneA: Flags must use explicit []string literal`},
		{name: "19. Array Flags type rejected", body: `func(v []toolpolicy.Policy) PrivilegeProjection { return PrivilegeProjection{Flags: [1]string{"raw_capture"}} }`, wantErrPart: `plane PlaneA: Flags must be a slice of string ([]string), got *ast.ArrayType`},
		{name: "20. Foreign Flags element type rejected", body: `func(v []toolpolicy.Policy) PrivilegeProjection { return PrivilegeProjection{Flags: []foreign.String{"raw_capture"}} }`, wantErrPart: `plane PlaneA: Flags element type must be string, got *ast.SelectorExpr`},
		{name: "21. Non-string Flags element type rejected", body: `func(v []toolpolicy.Policy) PrivilegeProjection { return PrivilegeProjection{Flags: []int{1}} }`, wantErrPart: `plane PlaneA: Flags element type must be string, got *ast.Ident`},
		{name: "22. Variadic len in condition rejected", body: `func(v []toolpolicy.Policy) PrivilegeProjection { if len(v...) > 0 { return PrivilegeProjection{Flags: []string{PrivilegeRawCapture}} }; return PrivilegeProjection{} }`, wantErrPart: `plane PlaneA: variadic/ellipsis len is unsupported in privilege condition`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := GenerateFeaturePlanesCode([]byte(privManifest(tt.body)))
			if tt.wantErrPart != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrPart)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestPlaneGenerator_InvalidManifestDeclarationsRejected verifies that malformed
// manifest declarations fail generation with a stable, actionable error instead
// of emitting code with missing wiring or ambiguous plane identity.
func TestPlaneGenerator_InvalidManifestDeclarationsRejected(t *testing.T) {
	t.Parallel()

	const header = `package feature
import "github.com/aiproxer/aiproxer/pkg/aipsdk/session"
`

	tests := []struct {
		name        string
		manifest    string
		wantErrPart string
	}{
		{
			name:        "missing StandardPlanes declaration",
			manifest:    `var PlaneA = Plane[[]session.Opener]{ID: "plane_a", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyMetadataOnly, Combine: func(s SourceKind, c, in []session.Opener) ([]session.Opener, error) { return in, nil }}`,
			wantErrPart: "StandardPlanes slice declaration not found in manifest",
		},
		{
			name:        "empty StandardPlanes",
			manifest:    "var StandardPlanes = []any{}",
			wantErrPart: "StandardPlanes composite literal is empty",
		},
		{
			name:        "non-identifier StandardPlanes entry",
			manifest:    `var StandardPlanes = []any{"plane_a"}`,
			wantErrPart: "expected identifier in StandardPlanes",
		},
		{
			name: "duplicate plane in StandardPlanes",
			manifest: `var PlaneA = Plane[[]session.Opener]{ID: "plane_a", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyMetadataOnly, Combine: func(s SourceKind, c, in []session.Opener) ([]session.Opener, error) { return in, nil }}
var StandardPlanes = []any{PlaneA, PlaneA}`,
			wantErrPart: "duplicate plane PlaneA in StandardPlanes",
		},
		{
			name:        "StandardPlanes entry not declared",
			manifest:    "var StandardPlanes = []any{PlaneGhost}",
			wantErrPart: "plane PlaneGhost referenced in StandardPlanes was not declared in manifest",
		},
		{
			name: "declared plane absent from StandardPlanes",
			manifest: `var PlaneA = Plane[[]session.Opener]{ID: "plane_a", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyMetadataOnly, Combine: func(s SourceKind, c, in []session.Opener) ([]session.Opener, error) { return in, nil }}
var PlaneB = Plane[[]session.Opener]{ID: "plane_b", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyMetadataOnly, Combine: func(s SourceKind, c, in []session.Opener) ([]session.Opener, error) { return in, nil }}
var StandardPlanes = []any{PlaneA}`,
			wantErrPart: "declared plane PlaneB is not present in StandardPlanes",
		},
		{
			name: "duplicate plane ID",
			manifest: `var PlaneA = Plane[[]session.Opener]{ID: "dup", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyMetadataOnly, Combine: func(s SourceKind, c, in []session.Opener) ([]session.Opener, error) { return in, nil }}
var PlaneB = Plane[[]session.Opener]{ID: "dup", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyMetadataOnly, Combine: func(s SourceKind, c, in []session.Opener) ([]session.Opener, error) { return in, nil }}
var StandardPlanes = []any{PlaneA, PlaneB}`,
			wantErrPart: `duplicate plane ID "dup"`,
		},
		{
			name: "missing plane ID",
			manifest: `var PlaneA = Plane[[]session.Opener]{Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyMetadataOnly, Combine: func(s SourceKind, c, in []session.Opener) ([]session.Opener, error) { return in, nil }}
var StandardPlanes = []any{PlaneA}`,
			wantErrPart: "plane ID is required and must not be empty",
		},
		{
			name: "missing multiplicity",
			manifest: `var PlaneA = Plane[[]session.Opener]{ID: "plane_a", Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyMetadataOnly, Combine: func(s SourceKind, c, in []session.Opener) ([]session.Opener, error) { return in, nil }}
var StandardPlanes = []any{PlaneA}`,
			wantErrPart: "invalid or missing Multiplicity",
		},
		{
			name: "missing source rules",
			manifest: `var PlaneA = Plane[[]session.Opener]{ID: "plane_a", Multiplicity: MultOrdered, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyMetadataOnly, Combine: func(s SourceKind, c, in []session.Opener) ([]session.Opener, error) { return in, nil }}
var StandardPlanes = []any{PlaneA}`,
			wantErrPart: "at least one source rule must be specified in Rules",
		},
		{
			name: "exclusive plane with concatenate feature rule",
			manifest: `var PlaneA = Plane[[]session.Opener]{ID: "plane_a", Multiplicity: MultExclusive, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyMetadataOnly, Combine: func(s SourceKind, c, in []session.Opener) ([]session.Opener, error) { return in, nil }}
var StandardPlanes = []any{PlaneA}`,
			wantErrPart: "exclusive plane cannot use concatenate or reduce rule on feature source",
		},
		{
			name: "exclusive plane without exclusive or unsupported feature rule",
			manifest: `var PlaneA = Plane[[]session.Opener]{ID: "plane_a", Multiplicity: MultExclusive, Rules: SourceRules{Host: CombExclusive}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyMetadataOnly, Combine: func(s SourceKind, c, in []session.Opener) ([]session.Opener, error) { return in, nil }}
var StandardPlanes = []any{PlaneA}`,
			wantErrPart: "exclusive plane must use CombExclusive or CombUnsupported on feature source",
		},
		{
			name: "exclusive plane without identity function",
			manifest: `var PlaneA = Plane[[]session.Opener]{ID: "plane_a", Multiplicity: MultExclusive, Rules: SourceRules{Feature: CombExclusive}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyMetadataOnly, ValidateIdentity: func(id string) error { return nil }, Combine: func(s SourceKind, c, in []session.Opener) ([]session.Opener, error) { return in, nil }}
var StandardPlanes = []any{PlaneA}`,
			wantErrPart: "identity function is required for exclusive or replace-by-identity plane",
		},
		{
			name: "missing combine function",
			manifest: `var PlaneA = Plane[[]session.Opener]{ID: "plane_a", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyMetadataOnly}
var StandardPlanes = []any{PlaneA}`,
			wantErrPart: "combine function is required",
		},
		{
			name: "diagnostics StageID without Materialize",
			manifest: `var PlaneA = Plane[[]session.Opener]{ID: "plane_a", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyMetadataOnly, Combine: func(s SourceKind, c, in []session.Opener) ([]session.Opener, error) { return in, nil }}
var PlaneA2 = Plane[[]session.Opener]{ID: "plane_a2", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyMetadataOnly, Combine: func(s SourceKind, c, in []session.Opener) ([]session.Opener, error) { return in, nil }, Diagnostics: DiagnosticDescriptor[[]session.Opener]{StageID: StageIDToolEventReaction, Order: 10}}
var StandardPlanes = []any{PlaneA, PlaneA2}`,
			wantErrPart: "diagnostics StageID is set but Materialize function is missing",
		},
		{
			name: "diagnostics StageID with zero Order",
			manifest: `var PlaneA = Plane[[]session.Opener]{ID: "plane_a", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyMetadataOnly, Combine: func(s SourceKind, c, in []session.Opener) ([]session.Opener, error) { return in, nil }}
var PlaneA2 = Plane[[]session.Opener]{ID: "plane_a2", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyMetadataOnly, Combine: func(s SourceKind, c, in []session.Opener) ([]session.Opener, error) { return in, nil }, Diagnostics: DiagnosticDescriptor[[]session.Opener]{StageID: StageIDToolEventReaction, Materialize: func(v []session.Opener) []DiagnosticOccupant { return nil }}}
var StandardPlanes = []any{PlaneA, PlaneA2}`,
			wantErrPart: "diagnostics StageID is set but Order must be > 0",
		},
		{
			name: "candidate plane not declared",
			manifest: `var PlaneA = Plane[[]session.Opener]{ID: "plane_a", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyMetadataOnly, Combine: func(s SourceKind, c, in []session.Opener) ([]session.Opener, error) { return in, nil }}
var StandardPlanes = []any{PlaneA}
var StandardCandidatePlanes = []string{"ghost_plane"}`,
			wantErrPart: `candidate plane ID "ghost_plane" in StandardCandidatePlanes was not declared in manifest`,
		},
		{
			name: "duplicate candidate plane ID",
			manifest: `var PlaneA = Plane[[]session.Opener]{ID: "plane_a", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyMetadataOnly, Combine: func(s SourceKind, c, in []session.Opener) ([]session.Opener, error) { return in, nil }}
var StandardPlanes = []any{PlaneA}
var StandardCandidatePlanes = []string{"plane_a", "plane_a"}`,
			wantErrPart: `duplicate candidate plane ID "plane_a"`,
		},
		{
			name: "empty candidate plane ID",
			manifest: `var PlaneA = Plane[[]session.Opener]{ID: "plane_a", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyMetadataOnly, Combine: func(s SourceKind, c, in []session.Opener) ([]session.Opener, error) { return in, nil }}
var StandardPlanes = []any{PlaneA}
var StandardCandidatePlanes = []string{""}`,
			wantErrPart: "StandardCandidatePlanes contains empty string",
		},
		{
			name: "non-string candidate plane ID",
			manifest: `var PlaneA = Plane[[]session.Opener]{ID: "plane_a", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyMetadataOnly, Combine: func(s SourceKind, c, in []session.Opener) ([]session.Opener, error) { return in, nil }}
var StandardPlanes = []any{PlaneA}
var StandardCandidatePlanes = []string{42}`,
			wantErrPart: "expected string literal in StandardCandidatePlanes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := GenerateFeaturePlanesCode([]byte(header + tt.manifest))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErrPart)
		})
	}
}

// TestWriteGeneratedFileAtomic_RejectsMissingDirectory verifies the generator's
// install seam refuses a missing target directory with an error naming it, and
// creates neither the directory nor a stray temp file.
func TestWriteGeneratedFileAtomic_RejectsMissingDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	missingDir := filepath.Join(dir, "missing")
	target := filepath.Join(missingDir, "plane_generated.go")

	err := WriteGeneratedFileAtomic(target, []byte("package feature"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
	assert.Contains(t, err.Error(), missingDir)

	_, statErr := os.Stat(missingDir)
	assert.True(t, os.IsNotExist(statErr), "missing target directory must not be created")
	entries, readErr := os.ReadDir(dir)
	require.NoError(t, readErr)
	assert.Empty(t, entries, "no temp file may be left behind")
}
