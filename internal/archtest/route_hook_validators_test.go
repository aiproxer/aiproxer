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

// Shared validators for #1385 phased route hooks. Kept in one file so the
// per-test maintainability limit (500 lines) is not exceeded by duplication.

func validatePreAdmissionFieldType(field *ast.Field) error {
	ft, ok := field.Type.(*ast.FuncType)
	if !ok {
		return fmt.Errorf("ResolvePreAdmissionRoute not a func type")
	}
	if ft.Params == nil || len(ft.Params.List) != 2 {
		return fmt.Errorf("ResolvePreAdmissionRoute must have exactly 2 params (http.Header, PathMatch)")
	}
	sel, ok := ft.Params.List[0].Type.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "Header" {
		return fmt.Errorf("ResolvePreAdmissionRoute first param must be http.Header")
	}
	x, ok := sel.X.(*ast.Ident)
	if !ok || x.Name != "http" {
		return fmt.Errorf("ResolvePreAdmissionRoute first param must be http.Header")
	}
	switch t := ft.Params.List[1].Type.(type) {
	case *ast.Ident:
		if t.Name != "PathMatch" {
			return fmt.Errorf("ResolvePreAdmissionRoute second param must be PathMatch")
		}
	case *ast.SelectorExpr:
		if t.Sel == nil || t.Sel.Name != "PathMatch" {
			return fmt.Errorf("ResolvePreAdmissionRoute second param must be PathMatch")
		}
	default:
		return fmt.Errorf("ResolvePreAdmissionRoute second param must be PathMatch")
	}
	if ft.Results == nil || len(ft.Results.List) != 1 {
		return fmt.Errorf("ResolvePreAdmissionRoute must return routing.ResolvedRoute")
	}
	rsel, ok := ft.Results.List[0].Type.(*ast.SelectorExpr)
	if !ok || rsel.Sel == nil || rsel.Sel.Name != "ResolvedRoute" {
		return fmt.Errorf("ResolvePreAdmissionRoute must return routing.ResolvedRoute")
	}
	rx, ok := rsel.X.(*ast.Ident)
	if !ok || rx.Name != "routing" {
		return fmt.Errorf("ResolvePreAdmissionRoute must return routing.ResolvedRoute")
	}
	return nil
}

func validatePreAdmissionImpl(body ast.Node) error {
	var violation string
	ast.Inspect(body, func(n ast.Node) bool {
		if violation != "" {
			return false
		}
		switch x := n.(type) {
		case *ast.CallExpr:
			_, cname := qualifiedCall(x.Fun)
			if cname == "Unmarshal" || cname == "FromModelOrDefault" || cname == "ResolveFromModelOrDefault" {
				violation = "forbidden body parse call " + cname
				return false
			}
		case *ast.Ident:
			if x.Name == "body" {
				violation = "forbidden reference to body bytes"
				return false
			}
		case *ast.SelectorExpr:
			if x.Sel != nil && (x.Sel.Name == "Body" || x.Sel.Name == "RawBody") {
				violation = "forbidden reference to request body"
				return false
			}
		}
		return true
	})
	if violation != "" {
		return fmt.Errorf("ResolvePreAdmissionRoute %s", violation)
	}
	return nil
}

// findPreAdmissionImpls returns every pre-admission callback implementation
// (func literal) assigned in non-test frontend code, plus a violation for
// every non-literal assignment. A named resolver would otherwise escape the
// body-access inspection entirely, so it is refused rather than skipped.
func findPreAdmissionImpls(root string) ([]*ast.FuncLit, []string, error) {
	var out []*ast.FuncLit
	var violations []string
	err := filepath.Walk(filepath.Join(root, "internal", "plugins", "frontends"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		record := func(value ast.Expr) {
			if lit, ok := value.(*ast.FuncLit); ok {
				out = append(out, lit)
				return
			}
			pos := fset.Position(value.Pos())
			violations = append(violations, fmt.Sprintf("%s:%d: non-literal ResolvePreAdmissionRoute implementation is not inspected", filepath.Base(path), pos.Line))
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if kv, ok := n.(*ast.KeyValueExpr); ok {
				if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "ResolvePreAdmissionRoute" {
					record(kv.Value)
				}
			}
			if as, ok := n.(*ast.AssignStmt); ok {
				for _, lhs := range as.Lhs {
					if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel != nil && sel.Sel.Name == "ResolvePreAdmissionRoute" {
						for _, rhs := range as.Rhs {
							record(rhs)
						}
					}
				}
			}
			return true
		})
		return nil
	})
	return out, violations, err
}

func isNilIdent1385(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
}

// candidateHookRefAllowed reports whether the hook reference at target is
// exactly an operand of a nil comparison. It deliberately does not search
// inside the other operand: passing the hook to a function that calls it
// (invokeAndReturn(spec.ResolveBodyRoute) != nil) is not a nil check and must
// stay forbidden.
func candidateHookRefAllowed(file *ast.File, target token.Pos) bool {
	allowed := false
	ast.Inspect(file, func(n ast.Node) bool {
		if allowed {
			return false
		}
		be, ok := n.(*ast.BinaryExpr)
		if !ok || (be.Op != token.NEQ && be.Op != token.EQL) {
			return true
		}
		var other ast.Expr
		if isNilIdent1385(be.X) {
			other = be.Y
		} else if isNilIdent1385(be.Y) {
			other = be.X
		} else {
			return true
		}
		if sel, ok := other.(*ast.SelectorExpr); ok && sel.Pos() == target {
			allowed = true
			return false
		}
		return true
	})
	return allowed
}

func candidateHookViolations(fset *token.FileSet, file *ast.File, base string) []string {
	var violations []string
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
				if sel.Sel != nil && (sel.Sel.Name == "ResolveRouteSelector" || sel.Sel.Name == "ResolvePreAdmissionRoute" || sel.Sel.Name == "ResolveBodyRoute") {
					pos := fset.Position(x.Pos())
					violations = append(violations, fmt.Sprintf("%s:%d: forbidden call to %s in candidate lane",
						base, pos.Line, sel.Sel.Name))
				}
			}
		case *ast.SelectorExpr:
			if x.Sel != nil && x.Sel.Name == "ResolveRouteSelector" {
				pos := fset.Position(x.Pos())
				violations = append(violations, fmt.Sprintf("%s:%d: forbidden legacy resolver in candidate lane",
					base, pos.Line))
			}
			if x.Sel != nil && (x.Sel.Name == "ResolvePreAdmissionRoute" || x.Sel.Name == "ResolveBodyRoute") {
				if !candidateHookRefAllowed(file, x.Pos()) {
					pos := fset.Position(x.Pos())
					violations = append(violations, fmt.Sprintf("%s:%d: %s outside nil check",
						base, pos.Line, x.Sel.Name))
				}
			}
		}
		return true
	})
	return violations
}

// TestCandidateHookRef_RejectsNestedInvocation proves a hook reference passed
// to another call is not mistaken for a nil check. Only the hook selector
// itself may be the compared operand.
func TestCandidateHookRef_RejectsNestedInvocation(t *testing.T) {
	t.Parallel()
	parse := func(src string) (*token.FileSet, *ast.File) {
		t.Helper()
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "synthetic.go", src, 0)
		if err != nil {
			t.Fatalf("parse synthetic file: %v", err)
		}
		return fset, file
	}
	src := "package badcode\n" +
		"type Spec struct { ResolveBodyRoute func() string }\n" +
		"func invokeAndReturn(f func() string) *string { return nil }\n" +
		"func rogue(s *Spec) { if invokeAndReturn(s.ResolveBodyRoute) != nil { _ = 1 } }\n"
	fset, file := parse(src)
	if violations := candidateHookViolations(fset, file, "synthetic.go"); len(violations) == 0 {
		t.Fatal("nested hook invocation must be flagged: only a direct nil comparison is allowed")
	}
}

// TestFindPreAdmissionImpls_RejectsNonLiteral proves a named resolver is
// refused rather than silently escaping inspection.
func TestFindPreAdmissionImpls_RejectsNonLiteral(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "plugins", "frontends", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	src := "package demo\nvar _ = Spec{ResolvePreAdmissionRoute: namedResolver}\n"
	if err := os.WriteFile(filepath.Join(dir, "demo.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, violations, err := findPreAdmissionImpls(root)
	if err != nil {
		t.Fatalf("findPreAdmissionImpls: %v", err)
	}
	if len(violations) == 0 {
		t.Fatal("non-literal ResolvePreAdmissionRoute must be refused, not skipped")
	}
}
