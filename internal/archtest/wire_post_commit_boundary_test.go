package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

// whitelistedLargebodyCallFunctions maps function/method names in
// internal/core/largebody to their architectural rationale.
// Requirement 19/22: Post-commit wire functions must NOT accept or return
// aipapi.Call. Only explicitly whitelisted pre-commit canonical oracle/test
// comparison functions may accept aipapi.Call.
var whitelistedLargebodyCallFunctions = map[string]string{
	"CanonicalCallIdentity":   "pre-commit canonical comparison identity oracle (identity.go:257)",
	"WriteCall":               "pre-commit canonical comparison identity writer oracle (identity.go:607)",
	"ClientTurnShapeFromCall": "pre-commit canonical turn shape extractor for parity testing (turn_shape.go:39)",
}

// whitelistedRuntimeWireAdapters maps whitelisted response-only wire adapters
// in internal/core/runtime to their architectural justification.
var whitelistedRuntimeWireAdapters = map[string]string{
	"responseEvidenceFromWire": "Task 12.3 bounded response evidence adapter from WireTurnFacts (Requirement 19.4)",
}

var (
	archPkgCacheMu sync.Mutex
	archPkgCache   = make(map[string]*packages.Package)
)

func loadPackageForArchTest(t *testing.T, pkgPath string) *packages.Package {
	t.Helper()
	archPkgCacheMu.Lock()
	defer archPkgCacheMu.Unlock()

	if pkg, ok := archPkgCache[pkgPath]; ok {
		return pkg
	}

	cfg := &packages.Config{
		Mode:  packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		Tests: false,
	}
	pkgs, err := packages.Load(cfg, pkgPath)
	if err != nil {
		t.Fatalf("load package %s: %v", pkgPath, err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("load package %s returned %d packages", pkgPath, len(pkgs))
	}
	if pkgs[0].Types == nil || pkgs[0].TypesInfo == nil {
		t.Fatalf("package %s has nil Types or TypesInfo", pkgPath)
	}
	archPkgCache[pkgPath] = pkgs[0]
	return pkgs[0]
}

// isAipapiCallType checks if a types.Type resolves to aipapi.Call or *aipapi.Call.
// It unwraps pointers, slices, arrays, and maps to catch nested and alias forms.
func isAipapiCallType(t types.Type) bool {
	if t == nil {
		return false
	}
	for {
		if u, ok := t.(interface{ Elem() types.Type }); ok {
			t = u.Elem()
			continue
		}
		break
	}
	if named, ok := t.(*types.Named); ok {
		obj := named.Obj()
		if obj != nil && obj.Pkg() != nil {
			return obj.Pkg().Path() == "github.com/aiproxer/aiproxer/pkg/aipapi" && obj.Name() == "Call"
		}
	}
	return false
}

// isAipapiCloneCall verifies if an object or expression resolves to aipapi.CloneCall.
func isAipapiCloneCall(info *types.Info, call *ast.CallExpr) bool {
	if info == nil || call == nil {
		return false
	}
	var fnObj types.Object
	switch f := call.Fun.(type) {
	case *ast.Ident:
		fnObj = info.ObjectOf(f)
	case *ast.SelectorExpr:
		fnObj = info.ObjectOf(f.Sel)
	}
	if fnObj != nil && fnObj.Pkg() != nil {
		return fnObj.Pkg().Path() == "github.com/aiproxer/aiproxer/pkg/aipapi" && fnObj.Name() == "CloneCall"
	}
	// Fallback when type info object is unavailable
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		if sel.Sel != nil && sel.Sel.Name == "CloneCall" {
			return true
		}
	} else if ident, ok := call.Fun.(*ast.Ident); ok {
		if ident.Name == "CloneCall" {
			return true
		}
	}
	return false
}

// TestArch_WirePostCommit_LargebodyPackageIsCallFree enforces Requirement 19/22:
// No struct in internal/core/largebody may retain aipapi.Call, and no wire
// post-commit function/method may accept/return aipapi.Call or invoke aipapi.CloneCall.
func TestArch_WirePostCommit_LargebodyPackageIsCallFree(t *testing.T) {
	t.Parallel()
	pkg := loadPackageForArchTest(t, "github.com/aiproxer/aiproxer/internal/core/largebody")

	var violations []string

	// 1. Struct field scan: every struct declared in largebody must be free of aipapi.Call.
	scope := pkg.Types.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		typeName, ok := obj.(*types.TypeName)
		if !ok {
			continue
		}
		named, ok := typeName.Type().(*types.Named)
		if !ok {
			continue
		}
		st, ok := named.Underlying().(*types.Struct)
		if !ok {
			continue
		}
		for field := range st.Fields() {
			if isAipapiCallType(field.Type()) {
				violations = append(violations, fmt.Sprintf("struct %s: field %s has forbidden type %s",
					name, field.Name(), field.Type().String()))
			}
		}
	}

	// 2. Function / method signature scan: only explicitly whitelisted functions may accept aipapi.Call.
	for ident, obj := range pkg.TypesInfo.Defs {
		fn, ok := obj.(*types.Func)
		if !ok || fn == nil || ident == nil {
			continue
		}
		sig, ok := fn.Type().(*types.Signature)
		if !ok || sig == nil {
			continue
		}

		funcName := fn.Name()
		hasCall := false
		var callDetails []string

		// Check parameters
		params := sig.Params()
		for p := range params.Variables() {
			if isAipapiCallType(p.Type()) {
				hasCall = true
				callDetails = append(callDetails, fmt.Sprintf("param %s: %s", p.Name(), p.Type().String()))
			}
		}
		// Check results
		results := sig.Results()
		for i := 0; i < results.Len(); i++ {
			r := results.At(i)
			if isAipapiCallType(r.Type()) {
				hasCall = true
				callDetails = append(callDetails, fmt.Sprintf("result %d: %s", i, r.Type().String()))
			}
		}

		if hasCall {
			justification, whitelisted := whitelistedLargebodyCallFunctions[funcName]
			if !whitelisted {
				pos := pkg.Fset.Position(ident.Pos())
				violations = append(violations, fmt.Sprintf("%s:%d: function %q accepts/returns aipapi.Call but is not whitelisted: %s",
					filepath.Base(pos.Filename), pos.Line, funcName, strings.Join(callDetails, ", ")))
			} else if justification == "" {
				violations = append(violations, fmt.Sprintf("function %q is whitelisted without justification", funcName))
			}
		}
	}

	// 3. CloneCall invocation scan: zero CloneCall invocations in production largebody code.
	for _, file := range pkg.Syntax {
		filename := filepath.Base(pkg.Fset.Position(file.Pos()).Filename)
		if strings.HasSuffix(filename, "_test.go") {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if isAipapiCloneCall(pkg.TypesInfo, call) {
				pos := pkg.Fset.Position(call.Pos())
				violations = append(violations, fmt.Sprintf("%s:%d: forbidden invocation of aipapi.CloneCall in largebody",
					filename, pos.Line))
			}
			return true
		})
	}

	if len(violations) > 0 {
		t.Fatalf("internal/core/largebody Call-free boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

// isRuntimeWireFunction determines if a function in internal/core/runtime
// is a wire post-commit function/method.
func isRuntimeWireFunction(fn *types.Func) bool {
	if fn == nil {
		return false
	}
	name := fn.Name()
	if strings.Contains(name, "Wire") || strings.Contains(name, "wire") || strings.Contains(name, "LargeBody") || strings.Contains(name, "largeBody") {
		return true
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig == nil {
		return false
	}
	params := sig.Params()
	for p := range params.Variables() {
		typeName := p.Type().String()
		if strings.Contains(typeName, "largebody.Wire") || strings.Contains(typeName, "largebody.Assessment") ||
			strings.Contains(typeName, "WireBilling") || strings.Contains(typeName, "WireExposure") ||
			strings.Contains(typeName, "WireFrontendIngressInput") || strings.Contains(typeName, "WireBackendIngressInput") {
			return true
		}
	}
	return false
}

// TestArch_WirePostCommit_RuntimeWireFunctionsAreCallFree verifies that all
// wire post-commit functions in internal/core/runtime are completely Call-free:
// they do not accept aipapi.Call, dereference fields of type aipapi.Call,
// or invoke aipapi.CloneCall, except explicitly whitelisted response-only adapters.
func TestArch_WirePostCommit_RuntimeWireFunctionsAreCallFree(t *testing.T) {
	t.Parallel()
	pkg := loadPackageForArchTest(t, "github.com/aiproxer/aiproxer/internal/core/runtime")

	var violations []string

	for ident, obj := range pkg.TypesInfo.Defs {
		fn, ok := obj.(*types.Func)
		if !ok || fn == nil || ident == nil {
			continue
		}
		if !isRuntimeWireFunction(fn) {
			continue
		}

		funcName := fn.Name()
		if justification, whitelisted := whitelistedRuntimeWireAdapters[funcName]; whitelisted {
			if strings.TrimSpace(justification) == "" {
				violations = append(violations, fmt.Sprintf("wire function %q is whitelisted without justification", funcName))
			}
			continue
		}
		sig, ok := fn.Type().(*types.Signature)
		if !ok {
			t.Fatalf("wire function %q has non-signature type %T", funcName, fn.Type())
		}

		// 1. Check parameters and return types
		params := sig.Params()
		for p := range params.Variables() {
			if isAipapiCallType(p.Type()) {
				pos := pkg.Fset.Position(ident.Pos())
				violations = append(violations, fmt.Sprintf("%s:%d: wire function %q accepts forbidden parameter %s: %s",
					filepath.Base(pos.Filename), pos.Line, funcName, p.Name(), p.Type().String()))
			}
		}
		results := sig.Results()
		for r := range results.Variables() {
			if isAipapiCallType(r.Type()) {
				pos := pkg.Fset.Position(ident.Pos())
				violations = append(violations, fmt.Sprintf("%s:%d: wire function %q returns forbidden type %s",
					filepath.Base(pos.Filename), pos.Line, funcName, r.Type().String()))
			}
		}
	}

	// 2. Scan AST function bodies of wire functions for CloneCall and Call dereferences
	for _, file := range pkg.Syntax {
		filename := filepath.Base(pkg.Fset.Position(file.Pos()).Filename)
		if strings.HasSuffix(filename, "_test.go") {
			continue
		}

		for _, decl := range file.Decls {
			fnDecl, ok := decl.(*ast.FuncDecl)
			if !ok || fnDecl.Body == nil {
				continue
			}
			fnObj := pkg.TypesInfo.Defs[fnDecl.Name]
			fn, _ := fnObj.(*types.Func)
			if !isRuntimeWireFunction(fn) {
				continue
			}

			funcName := fnDecl.Name.Name
			if justification, whitelisted := whitelistedRuntimeWireAdapters[funcName]; whitelisted {
				if strings.TrimSpace(justification) == "" {
					violations = append(violations, fmt.Sprintf("wire function %q is whitelisted without justification", funcName))
				}
				continue
			}
			ast.Inspect(fnDecl.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					if isAipapiCloneCall(pkg.TypesInfo, x) {
						pos := pkg.Fset.Position(x.Pos())
						violations = append(violations, fmt.Sprintf("%s:%d: wire function %q invokes forbidden aipapi.CloneCall",
							filename, pos.Line, funcName))
					}
				case *ast.SelectorExpr:
					// Check if selecting a field/method on an expression of type aipapi.Call or *aipapi.Call
					receiverType := pkg.TypesInfo.TypeOf(x.X)
					if isAipapiCallType(receiverType) {
						pos := pkg.Fset.Position(x.Pos())
						violations = append(violations, fmt.Sprintf("%s:%d: wire function %q dereferences aipapi.Call field %q",
							filename, pos.Line, funcName, x.Sel.Name))
					}
				case *ast.StarExpr:
					// Check if dereferencing a pointer to aipapi.Call (*call)
					elemType := pkg.TypesInfo.TypeOf(x.X)
					if isAipapiCallType(elemType) {
						pos := pkg.Fset.Position(x.Pos())
						violations = append(violations, fmt.Sprintf("%s:%d: wire function %q dereferences *aipapi.Call pointer",
							filename, pos.Line, funcName))
					}
				}
				return true
			})
		}
	}

	if len(violations) > 0 {
		t.Fatalf("internal/core/runtime wire Call-free boundary violations:\n%s", strings.Join(violations, "\n"))
	}
}

// TestArch_WirePostCommit_CatchPrepCallClonesAndRenames_TypeOriented proves that
// our boundary check is type/dataflow oriented rather than textual string grep:
// any field access or dereference on a aipapi.Call receiver is caught regardless
// of field or variable names.
func TestArch_WirePostCommit_CatchPrepCallClonesAndRenames_TypeOriented(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	overlayPath := filepath.Join(root, "pkg", "aipapi", "synthetic_type_test_overlay.go")
	overlay := map[string][]byte{
		overlayPath: []byte(`package aipapi

type RenamedCarrier struct {
	ArbitraryCallName *Call
	UnrelatedField    int
}

func WireFunctionWithRenamedField(c *RenamedCarrier) {
	_ = c.ArbitraryCallName.Route.Selector
}

func WireFunctionWithRenamedClone(c *RenamedCarrier) {
	_ = CloneCall(*c.ArbitraryCallName)
}
`),
	}

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		Tests:   false,
		Overlay: overlay,
	}
	pkgs, err := packages.Load(cfg, "github.com/aiproxer/aiproxer/pkg/aipapi")
	require.NoError(t, err)
	require.Len(t, pkgs, 1)

	pkg := pkgs[0]
	var detectedDereference bool
	var detectedCloneCall bool

	for _, file := range pkg.Syntax {
		filename := filepath.Base(pkg.Fset.Position(file.Pos()).Filename)
		if filename != "synthetic_type_test_overlay.go" {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if isAipapiCloneCall(pkg.TypesInfo, x) {
					detectedCloneCall = true
				}
			case *ast.SelectorExpr:
				if isAipapiCallType(pkg.TypesInfo.TypeOf(x.X)) {
					detectedDereference = true
				}
			}
			return true
		})
	}

	assert.True(t, detectedDereference, "type-oriented checker must catch dereferencing renamed field of type *aipapi.Call")
	assert.True(t, detectedCloneCall, "type-oriented checker must catch CloneCall invocation on renamed field")
}

// TestArch_FrontendCandidate_NoLegacyRouteResolverBodyMaterialization enforces
// Requirement 22.4 & Task 12.6: neither custom route hook may run in the
// candidate lane; Gate 5 nil-checks are the only allowed appearance, and the
// old ResolveRouteSelector must not appear at all.
func TestArch_FrontendCandidate_NoLegacyRouteResolverBodyMaterialization(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	candidatePath := filepath.Join(root, "internal", "plugins", "frontends", "frontendpipe", "candidate.go")
	content, err := os.ReadFile(candidatePath)
	require.NoError(t, err)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, candidatePath, content, parser.ParseComments)
	require.NoError(t, err)
	if violations := candidateHookViolations(fset, file, filepath.Base(candidatePath)); len(violations) > 0 {
		t.Fatalf("frontend candidate custom route hook violations:\n%s", strings.Join(violations, "\n"))
	}
}

// TestArch_WirePostCommit_RogueViolationDetection_MutationTest proves the
// production candidate-lane rule flags rogue hook uses. It runs
// candidateHookViolations itself against synthetic good and bad files, so a
// rule regression fails here, not just a parallel synthetic search.
func TestArch_WirePostCommit_RogueViolationDetection_MutationTest(t *testing.T) {
	t.Parallel()
	parse := func(src string) (*token.FileSet, *ast.File) {
		t.Helper()
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "synthetic.go", src, 0)
		require.NoError(t, err)
		return fset, file
	}
	// Allowed: hook mentioned only inside a nil comparison.
	goodSrc := "package badcode\ntype Spec struct { ResolveBodyRoute func() string }\nfunc ok(s *Spec) bool { return s.ResolveBodyRoute != nil }\n"
	fset, file := parse(goodSrc)
	assert.Empty(t, candidateHookViolations(fset, file, "synthetic.go"), "nil-checked hook reference must be allowed")
	// Forbidden: hook invocation, bare reference, and legacy resolver.
	for _, bad := range []string{
		"package badcode\ntype Spec struct { ResolveBodyRoute func() string }\nfunc rogue(s *Spec) { _ = s.ResolveBodyRoute() }\n",
		"package badcode\ntype Spec struct { ResolveBodyRoute func() string }\nfunc rogue(s *Spec) { _ = s.ResolveBodyRoute }\n",
		"package badcode\ntype Spec struct { ResolvePreAdmissionRoute func() string }\nfunc rogue(s *Spec) bool { return s.ResolvePreAdmissionRoute == nil || s.ResolveBodyRoute == nil }\nfunc rogue2(s *Spec) { _ = s.ResolvePreAdmissionRoute }\n",
		"package badcode\ntype Spec struct { ResolveRouteSelector func() string }\nfunc rogue(s *Spec) { _ = s.ResolveRouteSelector }\n",
	} {
		fset, file := parse(bad)
		assert.NotEmpty(t, candidateHookViolations(fset, file, "synthetic.go"), "rogue hook use must be flagged")
	}
}
