package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRuntimehostConcurrencyTests_NoWallClockSync rejects wall-clock polling and
// scheduler steering in runtimehost attempt_gate_* and attempt_runner* tests.
func TestRuntimehostConcurrencyTests_NoWallClockSync(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "infra", "runtimehost")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var scanned int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, "_test.go") {
			continue
		}
		if !strings.HasPrefix(name, "attempt_gate_") && !strings.HasPrefix(name, "attempt_runner") {
			continue
		}
		path := filepath.Join(dir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		scanned++
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			pos := fset.Position(call.Pos())
			switch pkg.Name {
			case "time":
				switch sel.Sel.Name {
				case "Sleep", "After", "NewTicker", "Tick", "AfterFunc", "NewTimer":
					t.Fatalf("%s must not use time.%s for sync at %s:%d (use barriers/channels; context timeout only as post-barrier deadlock guard)",
						name, sel.Sel.Name, filepath.Base(pos.Filename), pos.Line)
				}
			case "runtime":
				switch sel.Sel.Name {
				case "Gosched":
					t.Fatalf("%s must not use runtime.%s scheduler steering at %s:%d",
						name, sel.Sel.Name, filepath.Base(pos.Filename), pos.Line)
				}
			}
			return true
		})
	}
	if scanned == 0 {
		t.Fatal("expected attempt_gate_* / attempt_runner* test files to scan")
	}
}
