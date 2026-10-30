package pluginreg_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aiproxer/aiproxer/internal/pluginreg"
)

func TestDynamic_BackendFactoryDepsAliasesGeneric(t *testing.T) {
	t.Parallel()
	var zero pluginreg.BackendFactoryDeps
	_ = zero.Identity

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	regPath := filepath.Join(filepath.Dir(thisFile), "reg.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, regPath, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != "BackendFactoryDeps" {
			return true
		}
		found = true
		id, ok := ts.Type.(*ast.Ident)
		if !ok || id.Name != "GenericBackendFactoryDeps" {
			t.Fatalf("BackendFactoryDeps=%v want GenericBackendFactoryDeps alias", ts.Type)
		}
		return false
	})
	if !found {
		t.Fatal("BackendFactoryDeps missing")
	}
}
