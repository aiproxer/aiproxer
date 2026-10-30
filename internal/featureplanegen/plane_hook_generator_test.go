package featureplanegen

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type hookGenExpectedField struct {
	Name string
	Type string
}

type hookGenExpectedKV struct {
	Key      string
	PlaneVar string
}

func hookGenFindStructType(f *ast.File, name string) *ast.StructType {
	if f == nil {
		return nil
	}
	for _, decl := range f.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.TYPE {
			continue
		}
		for _, spec := range genDecl.Specs {
			if typeSpec, ok := spec.(*ast.TypeSpec); ok && typeSpec.Name.Name == name {
				if st, ok := typeSpec.Type.(*ast.StructType); ok {
					return st
				}
			}
		}
	}
	return nil
}

func hookGenFindFuncDecl(f *ast.File, name string) *ast.FuncDecl {
	if f == nil {
		return nil
	}
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

func hookGenExprToString(expr ast.Expr) string {
	if expr == nil {
		return ""
	}
	var buf bytes.Buffer
	fset := token.NewFileSet()
	if err := format.Node(&buf, fset, expr); err != nil {
		return ""
	}
	return buf.String()
}

func hookGenAssertStructFields(t *testing.T, st *ast.StructType, expected []hookGenExpectedField) {
	t.Helper()
	require.NotNil(t, st, "HookConfig struct must not be nil")
	require.NotNil(t, st.Fields, "HookConfig struct fields must not be nil")
	require.Equal(t, len(expected), len(st.Fields.List), "field count mismatch")

	seenNames := make(map[string]bool, len(st.Fields.List))
	for i, exp := range expected {
		field := st.Fields.List[i]
		require.Len(t, field.Names, 1, "field at index %d must declare exactly one name", i)
		name := field.Names[0].Name
		assert.False(t, seenNames[name], "duplicate field name %s at index %d", name, i)
		seenNames[name] = true

		assert.Equal(t, exp.Name, name, "field name mismatch at index %d", i)
		assert.Equal(t, exp.Type, hookGenExprToString(field.Type), "type mismatch for field %s at index %d", exp.Name, i)

		if exp.Name == "ToolReactorErrorPolicy" {
			sel, ok := field.Type.(*ast.SelectorExpr)
			require.True(t, ok, "ToolReactorErrorPolicy field type must be a SelectorExpr")
			pkgIdent, ok := sel.X.(*ast.Ident)
			require.True(t, ok, "ToolReactorErrorPolicy package qualifier must be an Ident")
			assert.NotEmpty(t, pkgIdent.Name, "ToolReactorErrorPolicy package qualifier must be nonempty")
			assert.Equal(t, "ToolReactorErrorPolicy", sel.Sel.Name)
		}
	}
}

func hookGenAssertProjectHookConfigSignature(t *testing.T, fn *ast.FuncDecl, expectedPolicyPkg string) {
	t.Helper()
	require.NotNil(t, fn, "ProjectHookConfig must be declared")
	require.Nil(t, fn.Recv, "ProjectHookConfig must be package-level")
	require.NotNil(t, fn.Type, "signature type required")
	require.NotNil(t, fn.Type.Params, "params required")
	require.Len(t, fn.Type.Params.List, 2, "must accept 2 params")

	assert.Equal(t, "frozen", fn.Type.Params.List[0].Names[0].Name)
	assert.Equal(t, "FrozenPlaneSet", hookGenExprToString(fn.Type.Params.List[0].Type))

	assert.Equal(t, "policy", fn.Type.Params.List[1].Names[0].Name)
	require.NotEmpty(t, expectedPolicyPkg)
	expectedPolicyType := expectedPolicyPkg + ".ToolReactorErrorPolicy"
	assert.Equal(t, expectedPolicyType, hookGenExprToString(fn.Type.Params.List[1].Type))

	sel, ok := fn.Type.Params.List[1].Type.(*ast.SelectorExpr)
	require.True(t, ok, "policy parameter type must be a SelectorExpr")
	pkgIdent, ok := sel.X.(*ast.Ident)
	require.True(t, ok, "policy qualifier must be Ident")
	assert.NotEmpty(t, pkgIdent.Name)
	assert.Equal(t, expectedPolicyPkg, pkgIdent.Name)
	assert.Equal(t, "ToolReactorErrorPolicy", sel.Sel.Name)

	require.NotNil(t, fn.Type.Results)
	require.Len(t, fn.Type.Results.List, 1)
	assert.Equal(t, "HookConfig", hookGenExprToString(fn.Type.Results.List[0].Type))
}

func hookGenExtractReturnHookConfigLit(t *testing.T, fn *ast.FuncDecl) *ast.CompositeLit {
	t.Helper()
	require.NotNil(t, fn)
	require.NotNil(t, fn.Body)
	require.Len(t, fn.Body.List, 1)
	retStmt, ok := fn.Body.List[0].(*ast.ReturnStmt)
	require.True(t, ok)
	require.Len(t, retStmt.Results, 1)
	compLit, ok := retStmt.Results[0].(*ast.CompositeLit)
	require.True(t, ok)
	typeIdent, ok := compLit.Type.(*ast.Ident)
	require.True(t, ok)
	assert.Equal(t, "HookConfig", typeIdent.Name)
	return compLit
}

func hookGenAssertHookConfigLiteral(t *testing.T, comp *ast.CompositeLit, expectedOrder []hookGenExpectedKV) {
	t.Helper()
	require.NotNil(t, comp)
	require.Equal(t, len(expectedOrder)+1, len(comp.Elts), "literal element count mismatch")
	seenKeys := make(map[string]bool, len(comp.Elts))
	for i, exp := range expectedOrder {
		elt := comp.Elts[i]
		kv, ok := elt.(*ast.KeyValueExpr)
		require.True(t, ok, "element at index %d must be KeyValueExpr", i)
		keyIdent, ok := kv.Key.(*ast.Ident)
		require.True(t, ok, "key at index %d must be Ident", i)
		assert.False(t, seenKeys[keyIdent.Name], "duplicate key %s at index %d", keyIdent.Name, i)
		seenKeys[keyIdent.Name] = true
		assert.Equal(t, exp.Key, keyIdent.Name, "key mismatch at index %d", i)
		call, ok := kv.Value.(*ast.CallExpr)
		require.True(t, ok, "field %s value must be CallExpr", keyIdent.Name)
		sel, ok := call.Fun.(*ast.SelectorExpr)
		require.True(t, ok)
		assert.Equal(t, "get", sel.Sel.Name)
		accessIdent, ok := sel.X.(*ast.Ident)
		require.True(t, ok)
		assert.Equal(t, "canonical"+exp.PlaneVar+"Access", accessIdent.Name)
		require.Len(t, call.Args, 1)
		argSel, ok := call.Args[0].(*ast.SelectorExpr)
		require.True(t, ok)
		assert.Equal(t, "frozen", argSel.Sel.Name)
		xIdent, ok := argSel.X.(*ast.Ident)
		require.True(t, ok, "call argument selector X must be an Ident")
		assert.Equal(t, "frozen", xIdent.Name)
	}
	lastIdx := len(expectedOrder)
	lastKV, ok := comp.Elts[lastIdx].(*ast.KeyValueExpr)
	require.True(t, ok)
	lastKeyIdent, ok := lastKV.Key.(*ast.Ident)
	require.True(t, ok)
	assert.Equal(t, "ToolReactorErrorPolicy", lastKeyIdent.Name)
	assert.False(t, seenKeys["ToolReactorErrorPolicy"])
	seenKeys["ToolReactorErrorPolicy"] = true

	lastValIdent, ok := lastKV.Value.(*ast.Ident)
	require.True(t, ok)
	assert.Equal(t, "policy", lastValIdent.Name)
}

func TestPlaneGenerator_HookViewMetadata_DeterministicEmission(t *testing.T) {
	t.Parallel()

	validHookManifest := `package feature
import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
var PlaneSubmitHooks = Plane[[]hooks.SubmitHook]{ID: "submit_hooks", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: "SubmitHooks", Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return in, nil }}
var PlaneRequestPartHooks = Plane[[]hooks.RequestPartHook]{ID: "request_part_hooks", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: "RequestPartHooks", Combine: func(s SourceKind, c, in []hooks.RequestPartHook) ([]hooks.RequestPartHook, error) { return in, nil }}
var PlaneResponsePartHooks = Plane[[]hooks.ResponsePartHook]{ID: "response_part_hooks", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: "ResponsePartHooks", Combine: func(s SourceKind, c, in []hooks.ResponsePartHook) ([]hooks.ResponsePartHook, error) { return in, nil }}
var PlaneToolReactors = Plane[[]hooks.ToolReactor]{ID: "tool_reactors", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: "ToolReactors", Combine: func(s SourceKind, c, in []hooks.ToolReactor) ([]hooks.ToolReactor, error) { return in, nil }}
var StandardPlanes = []any{PlaneSubmitHooks, PlaneRequestPartHooks, PlaneResponsePartHooks, PlaneToolReactors}`

	t.Run("EmitsHookConfigAndProjectHookConfig", func(t *testing.T) {
		t.Parallel()
		codeBytes, err := GenerateFeaturePlanesCode([]byte(validHookManifest))
		require.NoError(t, err)

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "plane_generated.go", codeBytes, 0)
		require.NoError(t, err)

		st := hookGenFindStructType(f, "HookConfig")
		require.NotNil(t, st)
		hookGenAssertStructFields(t, st, []hookGenExpectedField{
			{Name: "SubmitHooks", Type: "[]hooks.SubmitHook"},
			{Name: "RequestPartHooks", Type: "[]hooks.RequestPartHook"},
			{Name: "ResponsePartHooks", Type: "[]hooks.ResponsePartHook"},
			{Name: "ToolReactors", Type: "[]hooks.ToolReactor"},
			{Name: "ToolReactorErrorPolicy", Type: "hooks.ToolReactorErrorPolicy"},
		})

		fn := hookGenFindFuncDecl(f, "ProjectHookConfig")
		hookGenAssertProjectHookConfigSignature(t, fn, "hooks")

		comp := hookGenExtractReturnHookConfigLit(t, fn)
		hookGenAssertHookConfigLiteral(t, comp, []hookGenExpectedKV{
			{Key: "SubmitHooks", PlaneVar: "PlaneSubmitHooks"},
			{Key: "RequestPartHooks", PlaneVar: "PlaneRequestPartHooks"},
			{Key: "ResponsePartHooks", PlaneVar: "PlaneResponsePartHooks"},
			{Key: "ToolReactors", PlaneVar: "PlaneToolReactors"},
		})
	})

	t.Run("DeterministicEmission", func(t *testing.T) {
		t.Parallel()
		pass1, err1 := GenerateFeaturePlanesCode([]byte(validHookManifest))
		require.NoError(t, err1)
		pass2, err2 := GenerateFeaturePlanesCode([]byte(validHookManifest))
		require.NoError(t, err2)
		assert.True(t, bytes.Equal(pass1, pass2))
	})
}

func TestPlaneGenerator_HookViewMetadata_SubsetManifest(t *testing.T) {
	t.Parallel()

	t.Run("SubsetOrder_Standard", func(t *testing.T) {
		t.Parallel()
		subsetManifest := `package feature
import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
var PlaneSubmitHooks = Plane[[]hooks.SubmitHook]{ID: "submit_hooks", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: "SubmitHooks", Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return in, nil }}
var PlaneToolReactors = Plane[[]hooks.ToolReactor]{ID: "tool_reactors", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: "ToolReactors", Combine: func(s SourceKind, c, in []hooks.ToolReactor) ([]hooks.ToolReactor, error) { return in, nil }}
var StandardPlanes = []any{PlaneSubmitHooks, PlaneToolReactors}`
		codeBytes, err := GenerateFeaturePlanesCode([]byte(subsetManifest))
		require.NoError(t, err)

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "plane_generated.go", codeBytes, 0)
		require.NoError(t, err)

		st := hookGenFindStructType(f, "HookConfig")
		require.NotNil(t, st)
		hookGenAssertStructFields(t, st, []hookGenExpectedField{
			{Name: "SubmitHooks", Type: "[]hooks.SubmitHook"},
			{Name: "ToolReactors", Type: "[]hooks.ToolReactor"},
			{Name: "ToolReactorErrorPolicy", Type: "hooks.ToolReactorErrorPolicy"},
		})

		fn := hookGenFindFuncDecl(f, "ProjectHookConfig")
		hookGenAssertProjectHookConfigSignature(t, fn, "hooks")

		comp := hookGenExtractReturnHookConfigLit(t, fn)
		hookGenAssertHookConfigLiteral(t, comp, []hookGenExpectedKV{
			{Key: "SubmitHooks", PlaneVar: "PlaneSubmitHooks"},
			{Key: "ToolReactors", PlaneVar: "PlaneToolReactors"},
		})
	})

	t.Run("SubsetOrder_CustomSequence", func(t *testing.T) {
		t.Parallel()
		customManifest := `package feature
import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
var PlaneSubmitHooks = Plane[[]hooks.SubmitHook]{ID: "submit_hooks", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: "SubmitHooks", Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return in, nil }}
var PlaneToolReactors = Plane[[]hooks.ToolReactor]{ID: "tool_reactors", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: "ToolReactors", Combine: func(s SourceKind, c, in []hooks.ToolReactor) ([]hooks.ToolReactor, error) { return in, nil }}
var StandardPlanes = []any{PlaneToolReactors, PlaneSubmitHooks}`
		codeBytes, err := GenerateFeaturePlanesCode([]byte(customManifest))
		require.NoError(t, err)

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "plane_generated.go", codeBytes, 0)
		require.NoError(t, err)

		st := hookGenFindStructType(f, "HookConfig")
		require.NotNil(t, st)
		hookGenAssertStructFields(t, st, []hookGenExpectedField{
			{Name: "ToolReactors", Type: "[]hooks.ToolReactor"},
			{Name: "SubmitHooks", Type: "[]hooks.SubmitHook"},
			{Name: "ToolReactorErrorPolicy", Type: "hooks.ToolReactorErrorPolicy"},
		})

		fn := hookGenFindFuncDecl(f, "ProjectHookConfig")
		hookGenAssertProjectHookConfigSignature(t, fn, "hooks")

		comp := hookGenExtractReturnHookConfigLit(t, fn)
		hookGenAssertHookConfigLiteral(t, comp, []hookGenExpectedKV{
			{Key: "ToolReactors", PlaneVar: "PlaneToolReactors"},
			{Key: "SubmitHooks", PlaneVar: "PlaneSubmitHooks"},
		})
	})
}

func TestPlaneGenerator_HookViewMetadata_NoTargetManifest_EmitsNeither(t *testing.T) {
	t.Parallel()

	noTargetManifest := `package feature
import "github.com/aiproxer/aiproxer/pkg/aipsdk/session"
var PlaneSessionOpeners = Plane[[]session.Opener]{ID: "session_openers", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyMetadataOnly, Combine: func(s SourceKind, c, in []session.Opener) ([]session.Opener, error) { return in, nil }}
var StandardPlanes = []any{PlaneSessionOpeners}`
	codeBytes, err := GenerateFeaturePlanesCode([]byte(noTargetManifest))
	require.NoError(t, err)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "plane_generated.go", codeBytes, 0)
	require.NoError(t, err)

	assert.Nil(t, hookGenFindStructType(f, "HookConfig"))
	assert.Nil(t, hookGenFindFuncDecl(f, "ProjectHookConfig"))
}

func TestPlaneGenerator_HookViewMetadata_CanonicalAndAliasedImports(t *testing.T) {
	t.Parallel()

	t.Run("CanonicalDefaultImport", func(t *testing.T) {
		t.Parallel()
		manifest := `package feature
import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
var PlaneSubmitHooks = Plane[[]hooks.SubmitHook]{ID: "submit_hooks", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: "SubmitHooks", Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return in, nil }}
var StandardPlanes = []any{PlaneSubmitHooks}`
		codeBytes, err := GenerateFeaturePlanesCode([]byte(manifest))
		require.NoError(t, err)

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "plane_generated.go", codeBytes, 0)
		require.NoError(t, err)

		hasCanonicalImport := false
		for _, imp := range f.Imports {
			if imp.Path != nil && strings.Trim(imp.Path.Value, `"`) == canonicalHooksImportPath {
				assert.Nil(t, imp.Name)
				hasCanonicalImport = true
			}
		}
		assert.True(t, hasCanonicalImport)

		st := hookGenFindStructType(f, "HookConfig")
		require.NotNil(t, st)
		hookGenAssertStructFields(t, st, []hookGenExpectedField{
			{Name: "SubmitHooks", Type: "[]hooks.SubmitHook"},
			{Name: "ToolReactorErrorPolicy", Type: "hooks.ToolReactorErrorPolicy"},
		})

		fn := hookGenFindFuncDecl(f, "ProjectHookConfig")
		hookGenAssertProjectHookConfigSignature(t, fn, "hooks")
	})

	t.Run("AliasedImport_Sdkhooks", func(t *testing.T) {
		t.Parallel()
		aliasedManifest := `package feature
import sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
var PlaneSubmitHooks = Plane[[]sdkhooks.SubmitHook]{ID: "submit_hooks", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: "SubmitHooks", Combine: func(s SourceKind, c, in []sdkhooks.SubmitHook) ([]sdkhooks.SubmitHook, error) { return in, nil }}
var PlaneToolReactors = Plane[[]sdkhooks.ToolReactor]{ID: "tool_reactors", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: "ToolReactors", Combine: func(s SourceKind, c, in []sdkhooks.ToolReactor) ([]sdkhooks.ToolReactor, error) { return in, nil }}
var StandardPlanes = []any{PlaneSubmitHooks, PlaneToolReactors}`
		codeBytes, err := GenerateFeaturePlanesCode([]byte(aliasedManifest))
		require.NoError(t, err)

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "plane_generated.go", codeBytes, 0)
		require.NoError(t, err)

		hasAliasedImport := false
		for _, imp := range f.Imports {
			if imp.Path != nil && strings.Trim(imp.Path.Value, `"`) == canonicalHooksImportPath {
				require.NotNil(t, imp.Name)
				assert.Equal(t, "sdkhooks", imp.Name.Name)
				hasAliasedImport = true
			}
		}
		assert.True(t, hasAliasedImport)

		st := hookGenFindStructType(f, "HookConfig")
		require.NotNil(t, st)
		hookGenAssertStructFields(t, st, []hookGenExpectedField{
			{Name: "SubmitHooks", Type: "[]sdkhooks.SubmitHook"},
			{Name: "ToolReactors", Type: "[]sdkhooks.ToolReactor"},
			{Name: "ToolReactorErrorPolicy", Type: "sdkhooks.ToolReactorErrorPolicy"},
		})

		fn := hookGenFindFuncDecl(f, "ProjectHookConfig")
		hookGenAssertProjectHookConfigSignature(t, fn, "sdkhooks")

		comp := hookGenExtractReturnHookConfigLit(t, fn)
		hookGenAssertHookConfigLiteral(t, comp, []hookGenExpectedKV{
			{Key: "SubmitHooks", PlaneVar: "PlaneSubmitHooks"},
			{Key: "ToolReactors", PlaneVar: "PlaneToolReactors"},
		})

		structCode := hookGenExprToString(st)
		assert.NotContains(t, structCode, "[]hooks.")
		assert.NotContains(t, structCode, " hooks.ToolReactorErrorPolicy")
	})
}

func TestPlaneGenerator_HookViewMetadata_ValidationRejections(t *testing.T) {
	t.Parallel()

	t.Run("DuplicateHookTarget_Rejected", func(t *testing.T) {
		t.Parallel()
		dupManifest := `package feature
import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
var PlaneSubmit1 = Plane[[]hooks.SubmitHook]{ID: "s1", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: "SubmitHooks", Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return in, nil }}
var PlaneSubmit2 = Plane[[]hooks.SubmitHook]{ID: "s2", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: "SubmitHooks", Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return in, nil }}
var StandardPlanes = []any{PlaneSubmit1, PlaneSubmit2}`
		_, err := GenerateFeaturePlanesCode([]byte(dupManifest))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "SubmitHooks")
	})

	t.Run("UnknownHookTarget_Rejected", func(t *testing.T) {
		t.Parallel()
		unknownManifest := `package feature
import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
var PlaneUnknown = Plane[[]hooks.SubmitHook]{ID: "u1", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: "UnknownTargetTypo", Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return in, nil }}
var StandardPlanes = []any{PlaneUnknown}`
		_, err := GenerateFeaturePlanesCode([]byte(unknownManifest))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "UnknownTargetTypo")
	})

	t.Run("IncompatibleHookType_Rejected", func(t *testing.T) {
		t.Parallel()
		incompatibleManifest := `package feature
import "github.com/aiproxer/aiproxer/pkg/aipsdk/session"
var PlaneIncompatible = Plane[[]session.Opener]{ID: "i1", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: "SubmitHooks", Combine: func(s SourceKind, c, in []session.Opener) ([]session.Opener, error) { return in, nil }}
var StandardPlanes = []any{PlaneIncompatible}`
		_, err := GenerateFeaturePlanesCode([]byte(incompatibleManifest))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "SubmitHooks")
	})

	t.Run("InconsistentHookAliases_RejectedWithAttribution", func(t *testing.T) {
		t.Parallel()
		manifest := `package feature
import (
	hooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	otherhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)
var PlaneSubmit = Plane[[]hooks.SubmitHook]{ID: "s", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: "SubmitHooks", Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return in, nil }}
var PlaneRequest = Plane[[]otherhooks.RequestPartHook]{ID: "r", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: "RequestPartHooks", Combine: func(s SourceKind, c, in []otherhooks.RequestPartHook) ([]otherhooks.RequestPartHook, error) { return in, nil }}
var StandardPlanes = []any{PlaneSubmit, PlaneRequest}`
		_, err := GenerateFeaturePlanesCode([]byte(manifest))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "inconsistent hook package aliases")
		assert.Contains(t, err.Error(), `"hooks" in PlaneSubmit`)
		assert.Contains(t, err.Error(), `"otherhooks" in PlaneRequest`)
	})

	t.Run("UnimportedHookPackage_Rejected", func(t *testing.T) {
		t.Parallel()
		manifest := `package feature
var PlaneUnimported = Plane[[]unimported.SubmitHook]{ID: "u", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: "SubmitHooks", Combine: func(s SourceKind, c, in []unimported.SubmitHook) ([]unimported.SubmitHook, error) { return in, nil }}
var StandardPlanes = []any{PlaneUnimported}`
		_, err := GenerateFeaturePlanesCode([]byte(manifest))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown package \"unimported\"")
		assert.Contains(t, err.Error(), "PlaneUnimported")
	})

	t.Run("BareUnqualifiedHookType_Rejected", func(t *testing.T) {
		t.Parallel()
		manifest := `package feature
import "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
type SubmitHook string
var PlaneBare = Plane[[]SubmitHook]{ID: "b", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: "SubmitHooks", Combine: func(s SourceKind, c, in []SubmitHook) ([]SubmitHook, error) { return in, nil }}
var StandardPlanes = []any{PlaneBare}`
		_, err := GenerateFeaturePlanesCode([]byte(manifest))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "expected selector from canonical import")
		assert.Contains(t, err.Error(), "PlaneBare")
	})

	t.Run("ForeignPackageHookType_Rejected", func(t *testing.T) {
		t.Parallel()
		manifest := `package feature
import hooks "github.com/foreign/package/hooks"
var PlaneForeign = Plane[[]hooks.SubmitHook]{ID: "f", Multiplicity: MultOrdered, Rules: SourceRules{Feature: CombConcatenate}, NilPolicy: NilNotApplicable, RequestAccess: RequestBodyCanonicalRequired, HookTarget: "SubmitHooks", Combine: func(s SourceKind, c, in []hooks.SubmitHook) ([]hooks.SubmitHook, error) { return in, nil }}
var StandardPlanes = []any{PlaneForeign}`
		_, err := GenerateFeaturePlanesCode([]byte(manifest))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "github.com/foreign/package/hooks")
		assert.Contains(t, err.Error(), "PlaneForeign")
	})
}
