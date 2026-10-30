package qa

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// Failed certification must still release its host: a Background close used
// to hide the actual failure behind the entire module's five-minute timeout.
func TestQAFastPreflight_ContractFixtureTeardownBounded(t *testing.T) {
	t.Parallel()
	paths, err := filepath.Glob(filepath.Join("..", "..", "connectors", "*", "contracttest_test.go"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("discover connector contract fixtures: %v (count=%d)", err, len(paths))
	}
	for _, path := range paths {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			method, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || method.Sel.Name != "Close" {
				return true
			}
			argument, ok := call.Args[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := argument.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Background" {
				return true
			}
			if qualifier, ok := selector.X.(*ast.Ident); ok && qualifier.Name == "context" {
				t.Errorf("%s: fixture Close must have a deadline so failed TCK teardown cannot hang", fset.Position(call.Pos()))
			}
			return true
		})
	}
}
