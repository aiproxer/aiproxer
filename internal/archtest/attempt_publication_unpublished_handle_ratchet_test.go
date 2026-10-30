package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnpublishedRegistrationUsesReadyLifecycleHandle(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	runtimeDir := filepath.Join(root, "internal", "core", "runtime")
	entries, err := os.ReadDir(runtimeDir)
	if err != nil {
		t.Fatalf("read runtime dir: %v", err)
	}
	var violations []string
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".go") || strings.HasSuffix(ent.Name(), "_test.go") {
			continue
		}
		if ent.Name() == "attempt_session.go" {
			continue
		}
		path := filepath.Join(runtimeDir, ent.Name())
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", ent.Name(), err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "RegisterBLeg" {
				return true
			}
			for _, arg := range call.Args {
				argText := nodeText(arg)
				if strings.Contains(argText, "sess.lifecycleHandle") || strings.Contains(argText, "session.lifecycleHandle") {
					pos := fset.Position(call.Pos())
					violations = append(violations, fmt.Sprintf("%s:%d: RegisterBLeg calls sess.lifecycleHandle directly (must use ready.lifecycleHandle): %s", ent.Name(), pos.Line, argText))
				}
			}
			return true
		})
	}
	if len(violations) > 0 {
		t.Fatalf("unpublished RegisterBLeg sites must use ready.lifecycleHandle (%d violations):\n%s", len(violations), strings.Join(violations, "\n"))
	}
}
