package featureplanegen

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type planeInfo struct {
	varName                string // e.g. PlaneSubmitHooks
	planeID                string // e.g. "submit_hooks"
	fieldName              string // e.g. submitHooks
	typeExpr               string // e.g. []hooks.SubmitHook
	hookTarget             string // e.g. "SubmitHooks"
	hookPkg                string // e.g. "hooks" or "sdkhooks"
	requestAccess          string // e.g. "RequestBodyCanonicalRequired"
	isExclusive            bool   // e.g. terminaldecision.Provider
	hasIdentity            bool   // whether plane has an identity accessor
	hasValidateIdentity    bool   // whether plane has a ValidateIdentity validator
	hasFeatureRule         bool   // whether Feature rule is declared
	featureRule            string // e.g. CombConcatenate, CombExclusive
	hasHostRule            bool   // whether Host rule is declared
	hostRule               string // e.g. CombConcatenate, CombExclusive
	hasGenBinderRule       bool   // whether GenerationBinder rule is declared
	genBinderRule          string // e.g. CombReplaceByIdentity
	candidate              bool   // whether plane allows candidate overlay contribution
	hasRequestMaterializer bool   // whether plane has a RequestMaterializer
	requestBorrow          bool   // whether plane exposes RequestExecutionView method
	hasDiagStageID         bool
	diagStageID            string
	diagOrder              int
	diagCoalesceGroup      string
	hasDiagMaterialize     bool
	hasDiagPrivileges      bool
}

const canonicalHooksImportPath = "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"

var canonicalHookTargetConstants = map[string]string{
	"HookTargetSubmitHooks":       "SubmitHooks",
	"HookTargetRequestPartHooks":  "RequestPartHooks",
	"HookTargetResponsePartHooks": "ResponsePartHooks",
	"HookTargetToolReactors":      "ToolReactors",
}

var closedHookTargets = map[string]struct{}{
	"SubmitHooks":       {},
	"RequestPartHooks":  {},
	"ResponsePartHooks": {},
	"ToolReactors":      {},
}

// closedRequestAccessClasses is the closed set of request-body access class
// identifiers accepted as a Plane RequestAccess annotation. The zero
// Unclassified value is syntactically valid but rejected as a plane
// annotation so unclassified planes fail generation.
var closedRequestAccessClasses = map[string]struct{}{
	"RequestBodyAccessUnclassified": {},
	"RequestBodyCanonicalRequired":  {},
	"RequestBodyMetadataOnly":       {},
	"RequestBodyResponseOnly":       {},
	"RequestBodyWireContract":       {},
}

func parseRequestAccessExpr(varName string, expr ast.Expr) (string, error) {
	expr = unwrapParen(expr)
	ident, ok := expr.(*ast.Ident)
	if !ok {
		return "", fmt.Errorf("plane %s: unsupported RequestAccess expression (%T); must use a bare in-package RequestBodyAccess identifier", varName, expr)
	}
	if _, ok := closedRequestAccessClasses[ident.Name]; !ok {
		return "", fmt.Errorf("plane %s: unknown request access class identifier %q (expected one of RequestBodyAccessUnclassified, RequestBodyCanonicalRequired, RequestBodyMetadataOnly, RequestBodyResponseOnly, RequestBodyWireContract)", varName, ident.Name)
	}
	return ident.Name, nil
}

var expectedHookTargetTypes = map[string]string{
	"SubmitHooks":       "SubmitHook",
	"RequestPartHooks":  "RequestPartHook",
	"ResponsePartHooks": "ResponsePartHook",
	"ToolReactors":      "ToolReactor",
}

func buildImportMap(f *ast.File) map[string]string {
	imports := make(map[string]string)
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		path := strings.Trim(imp.Path.Value, `"`)
		if imp.Name != nil && imp.Name.Name != "" && imp.Name.Name != "_" && imp.Name.Name != "." {
			imports[imp.Name.Name] = path
		} else {
			pkgName := path
			if idx := strings.LastIndex(path, "/"); idx != -1 {
				pkgName = path[idx+1:]
			}
			imports[pkgName] = path
		}
	}
	return imports
}

func renderTypeExpr(expr ast.Expr) (string, error) {
	if expr == nil {
		return "", fmt.Errorf("nil type expression")
	}
	var buf bytes.Buffer
	fset := token.NewFileSet()
	if err := format.Node(&buf, fset, expr); err != nil {
		return "", fmt.Errorf("failed to format type expression: %w", err)
	}
	return buf.String(), nil
}

func parseHookTargetExpr(varName string, expr ast.Expr) (string, error) {
	expr = unwrapParen(expr)
	switch v := expr.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", fmt.Errorf("plane %s: unsupported HookTarget literal kind %v", varName, v.Kind)
		}
		unquoted, err := strconv.Unquote(v.Value)
		if err != nil {
			return "", fmt.Errorf("plane %s: invalid string literal for HookTarget %s: %w", varName, v.Value, err)
		}
		if unquoted == "" {
			return "", nil
		}
		if _, ok := closedHookTargets[unquoted]; !ok {
			return "", fmt.Errorf("plane %s: unknown HookTarget string %q (target %q)", varName, v.Value, unquoted)
		}
		return unquoted, nil

	case *ast.Ident:
		target, ok := canonicalHookTargetConstants[v.Name]
		if !ok {
			return "", fmt.Errorf("plane %s: unsupported HookTarget identifier %q (expected bare in-package constant HookTarget... or valid string literal)", varName, v.Name)
		}
		return target, nil

	case *ast.SelectorExpr:
		selStr := formatSelector(v)
		return "", fmt.Errorf("plane %s: HookTarget selector expression %q not allowed; must use bare in-package identifier or string literal", varName, selStr)

	default:
		return "", fmt.Errorf("plane %s: unsupported HookTarget expression (%T)", varName, expr)
	}
}

func validateHookTargetType(varName string, hookTarget string, typeArgAST ast.Expr, importMap map[string]string) (string, error) {
	expectedElemType, ok := expectedHookTargetTypes[hookTarget]
	if !ok {
		return "", fmt.Errorf("plane %s: unknown hook target %q", varName, hookTarget)
	}

	arrType, ok := typeArgAST.(*ast.ArrayType)
	if !ok || arrType.Len != nil {
		rendered, _ := renderTypeExpr(typeArgAST)
		return "", fmt.Errorf("plane %s: incompatible type %s for HookTarget %s (expected slice of %s from canonical import %q)",
			varName, rendered, hookTarget, expectedElemType, canonicalHooksImportPath)
	}

	selExpr, ok := arrType.Elt.(*ast.SelectorExpr)
	if !ok {
		rendered, _ := renderTypeExpr(typeArgAST)
		return "", fmt.Errorf("plane %s: incompatible type %s for HookTarget %s (expected selector from canonical import %q, got %T)",
			varName, rendered, hookTarget, canonicalHooksImportPath, arrType.Elt)
	}

	pkgIdent, ok := selExpr.X.(*ast.Ident)
	if !ok {
		rendered, _ := renderTypeExpr(typeArgAST)
		return "", fmt.Errorf("plane %s: incompatible type %s for HookTarget %s (selector package must be an identifier)",
			varName, rendered, hookTarget)
	}

	importPath, hasImport := importMap[pkgIdent.Name]
	if !hasImport {
		return "", fmt.Errorf("plane %s: unknown package %q in type for HookTarget %s", varName, pkgIdent.Name, hookTarget)
	}

	if importPath != canonicalHooksImportPath {
		return "", fmt.Errorf("plane %s: package %q in type resolves to %q, not canonical hooks import %q for HookTarget %s",
			varName, pkgIdent.Name, importPath, canonicalHooksImportPath, hookTarget)
	}

	if selExpr.Sel.Name != expectedElemType {
		return "", fmt.Errorf("plane %s: incompatible hook element type %s.%s for HookTarget %s (expected %s.%s)",
			varName, pkgIdent.Name, selExpr.Sel.Name, hookTarget, pkgIdent.Name, expectedElemType)
	}

	return pkgIdent.Name, nil
}

// WriteGeneratedFileAtomic atomically installs one generated file via temp write + sync + rename.
func WriteGeneratedFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("directory %s does not exist: %w", dir, err)
	}
	base := filepath.Base(path)
	f, err := os.CreateTemp(dir, fmt.Sprintf(".%s.tmp-*", base))
	if err != nil {
		return fmt.Errorf("failed to create temp file in %s: %w", dir, err)
	}
	tmpPath := f.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("failed to write temp file %s: %w", tmpPath, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("failed to sync temp file %s: %w", tmpPath, err)
	}
	if err := f.Chmod(0o644); err != nil {
		_ = f.Close()
		return fmt.Errorf("failed to chmod temp file %s: %w", tmpPath, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to close temp file %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to install %s: %w", path, err)
	}
	return nil
}

// GenerateFeaturePlanesCode parses plane_manifest.go source bytes and returns the formatted Go code for plane_generated.go.
func GenerateFeaturePlanesCode(manifestBytes []byte) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "plane_manifest.go", manifestBytes, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	planes, err := extractPlanes(file, manifestBytes)
	if err != nil {
		return nil, fmt.Errorf("extract planes: %w", err)
	}

	sdkImports, err := deriveImports(file)
	if err != nil {
		return nil, fmt.Errorf("derive imports: %w", err)
	}

	generatedCode, err := generatePlanesCode(planes, sdkImports)
	if err != nil {
		return nil, fmt.Errorf("generate code: %w", err)
	}

	formatted, err := format.Source(generatedCode)
	if err != nil {
		return nil, fmt.Errorf("format generated code: %w\n---\n%s\n---", err, string(generatedCode))
	}

	return formatted, nil
}
