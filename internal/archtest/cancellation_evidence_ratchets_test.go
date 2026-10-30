package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

var evidenceAttributionFunctions = map[string]bool{
	"prepareRecvEvent":                      true,
	"emitUsage":                             true,
	"emitUsageEvidence":                     true,
	"emitUsageTerminal":                     true,
	"consumeBackendUsageEvidenceForAttempt": true,
}

func TestArch_ResponsePipelineEvidenceUsesExplicitAttempt(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	path := filepath.Join(repoRoot(t), "internal", "core", "runtime", "response_pipeline_observations.go")
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse response_pipeline_observations.go: %v", err)
	}

	var found int
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || !evidenceAttributionFunctions[fn.Name.Name] {
			continue
		}
		found++
		if !hasAttemptParameter(fn) {
			t.Errorf("%s must receive an explicit *attemptSession", fn.Name.Name)
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if ok && sel.Sel.Name == "attempt" {
				pos := fset.Position(sel.Pos())
				t.Errorf("%s:%d: mutable current-attempt slot used for evidence attribution", path, pos.Line)
			}
			return true
		})
	}
	if found != len(evidenceAttributionFunctions) {
		t.Fatalf("found %d/%d evidence attribution functions", found, len(evidenceAttributionFunctions))
	}
}

func TestArch_ResponsePipelineSidebandIsSwallowedBeforeClientTransform(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	path := filepath.Join(repoRoot(t), "internal", "core", "runtime", "response_pipeline_observations.go")
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse response_pipeline_observations.go: %v", err)
	}

	var prepare, transform *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		switch fn.Name.Name {
		case "prepareRecvEvent":
			prepare = fn
		case "transformClientEvent":
			transform = fn
		}
	}
	if prepare == nil || transform == nil {
		t.Fatal("response pipeline must expose prepareRecvEvent and transformClientEvent")
	}
	if !hasSwallowedAssignment(prepare) {
		t.Fatal("prepareRecvEvent must mark duplicate/internal evidence swallowed")
	}
	if !hasSwallowedGuard(transform) {
		t.Fatal("transformClientEvent must stop before client emission when swallowed")
	}
}

func hasAttemptParameter(fn *ast.FuncDecl) bool {
	if fn.Type == nil || fn.Type.Params == nil {
		return false
	}
	for _, field := range fn.Type.Params.List {
		if !isEvidenceAttemptSessionType(field.Type) {
			continue
		}
		for _, name := range field.Names {
			if name.Name == "attempt" {
				return true
			}
		}
	}
	return false
}

func isEvidenceAttemptSessionType(expr ast.Expr) bool {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == "attemptSession"
}

func hasSwallowedAssignment(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range assign.Lhs {
			if i >= len(assign.Rhs) || !isField(lhs, "swallowed") {
				continue
			}
			if id, ok := assign.Rhs[i].(*ast.Ident); ok && id.Name == "true" {
				found = true
				return false
			}
		}
		return !found
	})
	return found
}

func hasSwallowedGuard(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		ast.Inspect(ifStmt.Cond, func(child ast.Node) bool {
			if isField(child, "swallowed") {
				found = true
				return false
			}
			return !found
		})
		return !found
	})
	return found
}

func isField(expr ast.Node, name string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == name
}
