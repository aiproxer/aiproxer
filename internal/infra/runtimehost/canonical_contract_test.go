package runtimehost_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

// Compile-time proof: Coordinator reload/status surface uses canonical SDK types directly.
var (
	_ interface {
		Reload(context.Context, sdkreload.Trigger) sdkreload.Result
		Status() sdkreload.Status
	} = (*runtimehost.Coordinator)(nil)
)

func TestCoordinator_ReloadStatusSignaturesUseCanonicalPackage(t *testing.T) {
	t.Parallel()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	coordPath := filepath.Join(filepath.Dir(thisFile), "coordinator.go")
	src, err := os.ReadFile(coordPath)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, coordPath, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	sdkImportLocal := ""
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if path != "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload" {
			continue
		}
		if imp.Name != nil {
			sdkImportLocal = imp.Name.Name
		} else {
			sdkImportLocal = "configreload"
		}
	}
	if sdkImportLocal == "" {
		t.Fatal("coordinator.go must import pkg/aipsdk/configreload for canonical Trigger/Result/Status")
	}

	need := map[string]bool{"Reload": false, "Status": false}
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Name == nil {
			return true
		}
		if !recvIsCoordinator(fn) {
			return true
		}
		switch fn.Name.Name {
		case "Reload":
			need["Reload"] = true
			assertCanonicalSig(t, fn, sdkImportLocal, "Trigger", "Result")
		case "Status":
			need["Status"] = true
			assertCanonicalResultType(t, fn, sdkImportLocal, "Status")
		}
		return true
	})
	for name, seen := range need {
		if !seen {
			t.Fatalf("Coordinator.%s not found", name)
		}
	}
}

func recvIsCoordinator(fn *ast.FuncDecl) bool {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return false
	}
	switch t := fn.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		id, ok := t.X.(*ast.Ident)
		return ok && id.Name == "Coordinator"
	case *ast.Ident:
		return t.Name == "Coordinator"
	default:
		return false
	}
}

func assertCanonicalSig(t *testing.T, fn *ast.FuncDecl, pkg, inType, outType string) {
	t.Helper()
	if fn.Type.Params == nil || len(fn.Type.Params.List) < 2 {
		t.Fatalf("%s: want context + %s.%s params", fn.Name.Name, pkg, inType)
	}
	in := fn.Type.Params.List[len(fn.Type.Params.List)-1].Type
	if !isPkgSelector(in, pkg, inType) {
		t.Fatalf("%s trigger param type=%s want %s.%s (canonical pkg/aipsdk/configreload)", fn.Name.Name, exprString(in), pkg, inType)
	}
	assertCanonicalResultType(t, fn, pkg, outType)
}

func assertCanonicalResultType(t *testing.T, fn *ast.FuncDecl, pkg, outType string) {
	t.Helper()
	if fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
		t.Fatalf("%s: want single result %s.%s", fn.Name.Name, pkg, outType)
	}
	out := fn.Type.Results.List[0].Type
	if !isPkgSelector(out, pkg, outType) {
		t.Fatalf("%s result type=%s want %s.%s (canonical pkg/aipsdk/configreload)", fn.Name.Name, exprString(out), pkg, outType)
	}
}

func isPkgSelector(expr ast.Expr, pkg, name string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == pkg && sel.Sel.Name == name
}

func exprString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.SelectorExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return id.Name + "." + t.Sel.Name
		}
	case *ast.Ident:
		return t.Name
	}
	return "complex"
}
