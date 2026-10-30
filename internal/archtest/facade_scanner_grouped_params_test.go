package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestArchTypeStringGroupedParams pins per-name expansion so grouped
// parameters cannot collapse through the facade-shape scanner.
func TestArchTypeStringGroupedParams(t *testing.T) {
	t.Parallel()
	parse := func(src string) string {
		expr, err := parser.ParseExprFrom(token.NewFileSet(), "src.go", src, 0)
		if err != nil {
			t.Fatalf("ParseExpr(%s): %v", src, err)
		}
		ft, ok := expr.(*ast.FuncType)
		if !ok {
			t.Fatalf("not a func type: %s", src)
		}
		return archTypeString(ft)
	}
	grouped := parse("func(a, b int)")
	single := parse("func(a int)")
	if grouped == single {
		t.Fatalf("grouped params collapse: func(a,b int)=%q same as func(a int)", grouped)
	}
}
