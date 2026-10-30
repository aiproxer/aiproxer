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

// TestExecutorConstructionNoPostConstructionMutation enforces that the
// runtimebundle composition root does not mutate runtime.Executor fields
// after calling runtime.NewExecutor. All fields must pass through
// ExecutorConfig at construction time (Phase 2 Task 2.7 / PR 7 hardening).
//
// The test walks non-test .go files in internal/infra/runtimebundle/ and
// rejects any assignment expression where:
//   - the left-hand side is a selector (obj.Field) whose receiver is the
//     variable holding the *runtime.Executor result from NewExecutor, OR
//   - the left-hand side is a chained selector (struct.Exec.Field) where
//     the intermediate field is named "Exec" (the executorRuntime.Exec field).
//
// This catches regressions like `exec.Preflight = ...` or `rt.Exec.Metrics = ...`
// that were removed during the Executor construction hardening.
func TestExecutorConstructionNoPostConstructionMutation(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "infra", "runtimebundle")

	var bad []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		violations := findPostConstructionMutations(t, path, src)
		bad = append(bad, violations...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf(
			"runtimebundle must not mutate runtime.Executor fields after NewExecutor "+
				"(all fields must pass through ExecutorConfig at construction): \n%s",
			strings.Join(bad, "\n"),
		)
	}
}

// findPostConstructionMutations inspects a single file for assignments to
// executor fields that occur after a runtime.NewExecutor call within the
// same function. It returns a list of human-readable violation strings.
func findPostConstructionMutations(t *testing.T, filename string, src []byte) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}

	var violations []string

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}

		// Find the NewExecutor call position and the variable receiving its result.
		newExecutorPos, newExecutorRecv := findNewExecutorCall(t, fset, fn.Body)
		if newExecutorPos == token.NoPos {
			continue // No NewExecutor in this function.
		}

		// Scan for assignments to executor fields after NewExecutor.
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			// Skip assignments at or before the NewExecutor call.
			if assign.Pos() <= newExecutorPos {
				return true
			}
			for _, lhs := range assign.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok {
					continue
				}

				// Pattern 1: direct mutation — exec.Field = ...
				// sel.X is an *ast.Ident matching newExecutorRecv.
				recvName := identName(sel.X)
				if newExecutorRecv != "" && recvName == newExecutorRecv {
					pos := fset.Position(lhs.Pos())
					violations = append(violations, formatViolation(filename, pos, sel))
					continue
				}

				// Pattern 2: struct field mutation — rt.Exec.Field = ...
				// sel.X is itself a *ast.SelectorExpr whose Sel.Name == "Exec".
				if inner, ok := sel.X.(*ast.SelectorExpr); ok {
					if inner.Sel != nil && inner.Sel.Name == "Exec" {
						pos := fset.Position(lhs.Pos())
						violations = append(violations, formatViolation(filename, pos, sel))
					}
				}
			}
			return true
		})
	}

	return violations
}

// findNewExecutorCall locates the first runtime.NewExecutor call in the
// function body and identifies the variable name that receives its result.
func findNewExecutorCall(t *testing.T, fset *token.FileSet, body *ast.BlockStmt) (token.Pos, string) {
	t.Helper()
	var callPos token.Pos
	var recvName string

	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, rhs := range assign.Rhs {
			call, ok := rhs.(*ast.CallExpr)
			if !ok {
				continue
			}
			if callName(call.Fun) != "NewExecutor" {
				continue
			}
			// Verify it's runtime.NewExecutor.
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if xid, ok := sel.X.(*ast.Ident); ok && xid.Name == "runtime" {
					callPos = call.Pos()
					if i < len(assign.Lhs) {
						recvName = identName(assign.Lhs[i])
					}
					return false
				}
			}
		}
		return true
	})

	return callPos, recvName
}

func callName(fun ast.Expr) string {
	switch x := fun.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		if x.Sel != nil {
			return x.Sel.Name
		}
	}
	return ""
}

// formatViolation produces a human-readable string for a detected mutation.
func formatViolation(filename string, pos token.Position, sel *ast.SelectorExpr) string {
	field := ""
	if sel.Sel != nil {
		field = sel.Sel.Name
	}
	return filename + ":" + pos.String() + ": post-construction mutation of Executor." + field
}
