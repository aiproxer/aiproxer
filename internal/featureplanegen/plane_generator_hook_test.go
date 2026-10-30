package featureplanegen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPlaneGenerator_HookTargetExpressionParsing tests generator parsing and validation of HookTarget expressions,
// including bare in-package constants, raw/escaped string literals, and adversarial rejections.
func TestPlaneGenerator_HookTargetExpressionParsing(t *testing.T) {
	t.Parallel()

	t.Run("BareInPackageConstants_Accepted", func(t *testing.T) {
		t.Parallel()
		manifest := `package feature

import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"

var PlaneSubmit = Plane[[]hooks.SubmitHook]{
	ID: "submit_hooks", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: HookTargetSubmitHooks,
	Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(c, in...), nil },
}
var PlaneRequest = Plane[[]hooks.RequestPartHook]{
	ID: "request_part_hooks", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: HookTargetRequestPartHooks,
	Combine: func(s SourceKind, c, in []hooks.RequestPartHook) ([]hooks.RequestPartHook, error) { return append(c, in...), nil },
}
var PlaneResponse = Plane[[]hooks.ResponsePartHook]{
	ID: "response_part_hooks", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: HookTargetResponsePartHooks,
	Combine: func(s SourceKind, c, in []hooks.ResponsePartHook) ([]hooks.ResponsePartHook, error) { return append(c, in...), nil },
}
var PlaneTool = Plane[[]hooks.ToolReactor]{
	ID: "tool_reactors", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: HookTargetToolReactors,
	Combine: func(s SourceKind, c, in []hooks.ToolReactor) ([]hooks.ToolReactor, error) { return append(c, in...), nil },
}
var StandardPlanes = []any{PlaneSubmit, PlaneRequest, PlaneResponse, PlaneTool}
`
		_, err := GenerateFeaturePlanesCode([]byte(manifest))
		require.NoError(t, err, "all exact bare in-package HookTarget constants must be accepted")
	})

	t.Run("RawAndEscapedStringLiterals_Accepted", func(t *testing.T) {
		t.Parallel()
		manifest := "package feature\n\n" +
			"import \"github.com/aiproxer/aiproxer/pkg/aipsdk/hooks\"\n\n" +
			"var PlaneRaw = Plane[[]hooks.SubmitHook]{\n" +
			"	ID: \"submit_raw\", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},\n" +
			"	NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: `SubmitHooks`,\n" +
			"	Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(c, in...), nil },\n" +
			"}\n" +
			"var PlaneEscaped = Plane[[]hooks.RequestPartHook]{\n" +
			"	ID: \"req_escaped\", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},\n" +
			"	NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: \"Request\\x50artHooks\",\n" +
			"	Combine: func(s SourceKind, c, in []hooks.RequestPartHook) ([]hooks.RequestPartHook, error) { return append(c, in...), nil },\n" +
			"}\n" +
			"var StandardPlanes = []any{PlaneRaw, PlaneEscaped}\n"
		_, err := GenerateFeaturePlanesCode([]byte(manifest))
		require.NoError(t, err, "raw and escaped valid string literals must be decoded and accepted")
	})

	t.Run("Adversarial_BareTerminalName_Rejected", func(t *testing.T) {
		t.Parallel()
		manifest := `package feature
import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
var PlaneBad = Plane[[]hooks.SubmitHook]{
	ID: "submit_bad", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: SubmitHooks,
	Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(c, in...), nil },
}
var StandardPlanes = []any{PlaneBad}
`
		_, err := GenerateFeaturePlanesCode([]byte(manifest))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "SubmitHooks", "error must identify the rejected bare terminal identifier")
		assert.Contains(t, err.Error(), "PlaneBad")
	})

	t.Run("Adversarial_ArbitrarySelector_Rejected", func(t *testing.T) {
		t.Parallel()
		manifest := `package feature
import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
var PlaneBad = Plane[[]hooks.SubmitHook]{
	ID: "submit_bad", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: arbitrary.HookTargetSubmitHooks,
	Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(c, in...), nil },
}
var StandardPlanes = []any{PlaneBad}
`
		_, err := GenerateFeaturePlanesCode([]byte(manifest))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "arbitrary.HookTargetSubmitHooks")
		assert.Contains(t, err.Error(), "PlaneBad")
	})

	t.Run("Adversarial_SpoofedConstantIdentifier_Rejected", func(t *testing.T) {
		t.Parallel()
		manifest := `package feature
import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
var PlaneBad = Plane[[]hooks.SubmitHook]{
	ID: "submit_bad", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: HookTargetSubmitHooksSpoofed,
	Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(c, in...), nil },
}
var StandardPlanes = []any{PlaneBad}
`
		_, err := GenerateFeaturePlanesCode([]byte(manifest))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "HookTargetSubmitHooksSpoofed")
		assert.Contains(t, err.Error(), "PlaneBad")
	})

	t.Run("Adversarial_UnknownStringLiteral_Rejected", func(t *testing.T) {
		t.Parallel()
		manifest := `package feature
import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
var PlaneBad = Plane[[]hooks.SubmitHook]{
	ID: "submit_bad", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: "NonExistentHookTarget",
	Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(c, in...), nil },
}
var StandardPlanes = []any{PlaneBad}
`
		_, err := GenerateFeaturePlanesCode([]byte(manifest))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "NonExistentHookTarget")
		assert.Contains(t, err.Error(), "PlaneBad")
	})

	t.Run("Adversarial_EscapedUnknownString_Rejected", func(t *testing.T) {
		t.Parallel()
		manifest := `package feature
import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
var PlaneBad = Plane[[]hooks.SubmitHook]{
	ID: "submit_bad", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: "Unknown\x54arget",
	Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(c, in...), nil },
}
var StandardPlanes = []any{PlaneBad}
`
		_, err := GenerateFeaturePlanesCode([]byte(manifest))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "UnknownTarget")
		assert.Contains(t, err.Error(), "PlaneBad")
	})
}

// TestPlaneGenerator_StructuralImportAwareTypeValidation tests generic type argument AST validation,
// including canonical aliases, formatting whitespace tolerance, and rejection of foreign/spoofed packages.
func TestPlaneGenerator_StructuralImportAwareTypeValidation(t *testing.T) {
	t.Parallel()

	t.Run("CanonicalImportAlias_Accepted", func(t *testing.T) {
		t.Parallel()
		manifest := `package feature

import sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"

var PlaneSubmitHooks = Plane[[]sdkhooks.SubmitHook]{
	ID: "submit_hooks", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: HookTargetSubmitHooks,
	Combine: func(s SourceKind, c, in []sdkhooks.SubmitHook) ([]sdkhooks.SubmitHook, error) { return append(c, in...), nil },
}
var StandardPlanes = []any{PlaneSubmitHooks}
`
		code, err := GenerateFeaturePlanesCode([]byte(manifest))
		require.NoError(t, err, "canonical hook import alias sdkhooks must be accepted")
		assert.Contains(t, string(code), `sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"`)
		assert.Contains(t, string(code), `submitHooks []sdkhooks.SubmitHook`)
	})

	t.Run("FormattingWhitespace_NormalizedAndAccepted", func(t *testing.T) {
		t.Parallel()
		manifest := `package feature

import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"

var PlaneSubmitHooks = Plane[  [ ]   hooks.SubmitHook  ]{
	ID: "submit_hooks", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: HookTargetSubmitHooks,
	Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(c, in...), nil },
}
var StandardPlanes = []any{PlaneSubmitHooks}
`
		code, err := GenerateFeaturePlanesCode([]byte(manifest))
		require.NoError(t, err, "formatting whitespace inside generic type argument must be tolerated")
		assert.Contains(t, string(code), `submitHooks []hooks.SubmitHook`)
	})

	t.Run("ForeignPathAliasedAsHooks_Rejected", func(t *testing.T) {
		t.Parallel()
		manifest := `package feature

import hooks "github.com/foreign/package/hooks"

var PlaneSubmit = Plane[[]hooks.SubmitHook]{
	ID: "submit_hooks", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: HookTargetSubmitHooks,
	Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return append(c, in...), nil },
}
var StandardPlanes = []any{PlaneSubmit}
`
		_, err := GenerateFeaturePlanesCode([]byte(manifest))
		require.Error(t, err, "foreign package aliased as hooks must be rejected")
		assert.Contains(t, err.Error(), "github.com/foreign/package/hooks")
		assert.Contains(t, err.Error(), "PlaneSubmit")
	})

	t.Run("LocalIdentOrSpoof_Rejected", func(t *testing.T) {
		t.Parallel()
		manifest := `package feature

type SubmitHook struct{}

var PlaneSubmit = Plane[[]SubmitHook]{
	ID: "submit_hooks", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: HookTargetSubmitHooks,
	Combine: func(s SourceKind, c, in []SubmitHook) ([]SubmitHook, error) { return append(c, in...), nil },
}
var StandardPlanes = []any{PlaneSubmit}
`
		_, err := GenerateFeaturePlanesCode([]byte(manifest))
		require.Error(t, err, "local bare identifier SubmitHook must be rejected")
		assert.Contains(t, err.Error(), "PlaneSubmit")
	})

	t.Run("IncompatibleElementType_Rejected", func(t *testing.T) {
		t.Parallel()
		manifest := `package feature

import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"

var PlaneSubmit = Plane[[]hooks.ToolReactor]{
	ID: "submit_hooks", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: HookTargetSubmitHooks,
	Combine: func(s SourceKind, c, in []hooks.ToolReactor) ([]hooks.ToolReactor, error) { return append(c, in...), nil },
}
var StandardPlanes = []any{PlaneSubmit}
`
		_, err := GenerateFeaturePlanesCode([]byte(manifest))
		require.Error(t, err, "mismatched element type for SubmitHooks must be rejected")
		assert.Contains(t, err.Error(), "ToolReactor")
		assert.Contains(t, err.Error(), "PlaneSubmit")
	})

	t.Run("NonSliceType_Rejected", func(t *testing.T) {
		t.Parallel()
		manifest := `package feature

import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"

var PlaneSubmit = Plane[hooks.SubmitHook]{
	ID: "submit_hooks", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate},
	NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: HookTargetSubmitHooks,
	Combine: func(s SourceKind, c, in hooks.SubmitHook) (hooks.SubmitHook, error) { return in, nil },
}
var StandardPlanes = []any{PlaneSubmit}
`
		_, err := GenerateFeaturePlanesCode([]byte(manifest))
		require.Error(t, err, "non-slice hook plane type must be rejected")
		assert.Contains(t, err.Error(), "PlaneSubmit")
	})
}
