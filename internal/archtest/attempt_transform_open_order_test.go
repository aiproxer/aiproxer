package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

const (
	runCandidateAttemptTransformStage = "RunCandidateAttemptTransformStage"
	evaluateCandidateAdmission        = "evaluateCandidateAdmission"
)

func findFunc(f *ast.File, name string) *ast.FuncDecl {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Body == nil {
			continue
		}
		if fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

func firstCallPos(body *ast.BlockStmt, match func(pkg, name string, call *ast.CallExpr) bool) token.Pos {
	var pos token.Pos
	ast.Inspect(body, func(n ast.Node) bool {
		if pos != 0 {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		pkg, name := qualifiedCall(call.Fun)
		if match(pkg, name, call) {
			pos = call.Pos()
			return false
		}
		return true
	})
	return pos
}

func TestOpenPlannedCandidate_attemptTransformBetweenShapeAndCapabilities(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "core", "runtime", "executor_open_attempt.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	evalFn := findFunc(f, "evaluateCandidate")
	if evalFn == nil {
		t.Fatal("evaluateCandidate not found")
	}

	shapePos := firstCallPos(evalFn.Body, func(_, name string, _ *ast.CallExpr) bool {
		return name == "shapeAttemptCall"
	})
	transformPos := firstCallPos(evalFn.Body, func(_, name string, _ *ast.CallExpr) bool {
		return name == runCandidateAttemptTransformStage
	})
	admitPos := firstCallPos(evalFn.Body, func(_, name string, _ *ast.CallExpr) bool {
		return name == evaluateCandidateAdmission
	})

	if shapePos == 0 {
		t.Fatal("evaluateCandidate must call shapeAttemptCall")
	}
	if transformPos == 0 {
		t.Fatalf("evaluateCandidate must call %s after shapeAttemptCall and before evaluateCandidateAdmission",
			runCandidateAttemptTransformStage)
	}
	if admitPos == 0 {
		t.Fatal("evaluateCandidate must call evaluateCandidateAdmission")
	}
	if shapePos >= transformPos || transformPos >= admitPos {
		t.Fatalf("want shapeAttemptCall < %s < evaluateCandidateAdmission; positions shape=%d transform=%d admission=%d",
			runCandidateAttemptTransformStage, shapePos, transformPos, admitPos)
	}
}

func TestOpenPlannedCandidate_excludeCandidateDecisionHandledBeforeOpen(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "core", "runtime", "executor_open_attempt.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	evalFn := findFunc(f, "evaluateCandidate")
	if evalFn == nil {
		t.Fatal("evaluateCandidate not found")
	}

	transformPos := firstCallPos(evalFn.Body, func(_, name string, _ *ast.CallExpr) bool {
		return name == runCandidateAttemptTransformStage
	})
	var excludedPos token.Pos
	ast.Inspect(evalFn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if excludedPos == 0 && x.Sel != nil && x.Sel.Name == "Excluded" {
				excludedPos = x.Pos()
			}
		}
		return true
	})

	if transformPos == 0 {
		t.Fatalf("evaluateCandidate must call %s", runCandidateAttemptTransformStage)
	}
	if excludedPos == 0 {
		t.Fatal("evaluateCandidate must branch on transform Excluded")
	}
	if transformPos >= excludedPos {
		t.Fatalf("want transform before Excluded check; transform=%d excluded=%d", transformPos, excludedPos)
	}

	// Verify that openAttemptTx calls be.Open
	openFn := findFunc(f, "openAttemptTx")
	if openFn == nil {
		t.Fatal("openAttemptTx not found")
	}
	openPos := firstCallPos(openFn.Body, func(pkg, name string, call *ast.CallExpr) bool {
		if name != "Open" {
			return false
		}
		if pkg == "be" || pkg == "backend" {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		if id, ok := sel.X.(*ast.Ident); ok && (id.Name == "be" || id.Name == "backend") {
			return true
		}
		return false
	})
	if openPos == 0 {
		t.Fatal("openAttemptTx must call backend Open")
	}
}
