package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestPhase1CoreDoesNotCreateAuthoritativeALegEconomicSubject is a structural
// guard for the refined authority boundary. Core may carry an A-leg as
// continuity/correlation, but an authoritative economic subject must remain
// rooted at a BillingCallID/B-leg or an explicitly separate resource subject.
// The runtime continuation test covers the behavior; this guard catches a
// direct metering SubjectALeg construction before it can become a new path.
func TestPhase1CoreDoesNotCreateAuthoritativeALegEconomicSubject(t *testing.T) {
	t.Parallel()
	root := filepath.Join(repoRoot(t), "internal", "core")
	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}
		aliases := phase1MeteringImportAliases(file)
		if len(aliases) == 0 {
			return nil
		}
		rel, err := filepath.Rel(repoRoot(t), path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		// Case values only read the kind for deterministic ordering (for
		// example derived A-leg/call/B-leg display sort); they do not
		// construct an authoritative subject. Collect them so the guard
		// below flags only actual constructions.
		casePositions := make(map[token.Pos]struct{})
		ast.Inspect(file, func(node ast.Node) bool {
			clause, ok := node.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, expr := range clause.List {
				ast.Inspect(expr, func(inner ast.Node) bool {
					sel, ok := inner.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if sel.Sel.Name != "SubjectALeg" && sel.Sel.Name != "SubjectAleg" {
						return true
					}
					casePositions[sel.Pos()] = struct{}{}
					return true
				})
			}
			return true
		})
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok || (selector.Sel.Name != "SubjectALeg" && selector.Sel.Name != "SubjectAleg") {
				return true
			}
			if _, ok := casePositions[selector.Pos()]; ok {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			if _, ok := aliases[pkg.Name]; !ok {
				return true
			}
			pos := fset.Position(selector.Pos())
			offenders = append(offenders, fmt.Sprintf("%s:%d: %s.%s", rel, pos.Line, pkg.Name, selector.Sel.Name))
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) != 0 {
		t.Fatalf("core constructs an authoritative A-leg economic subject; keep inference usage B-leg-rooted:\n%s", strings.Join(offenders, "\n"))
	}
}

func phase1MeteringImportAliases(file *ast.File) map[string]struct{} {
	const suffix = "/pkg/aipsdk/metering"
	aliases := make(map[string]struct{})
	for _, imp := range file.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil || !strings.HasSuffix(importPath, suffix) {
			continue
		}
		alias := filepath.Base(filepath.FromSlash(importPath))
		if imp.Name != nil {
			alias = imp.Name.Name
		}
		if alias != "" && alias != "_" && alias != "." {
			aliases[alias] = struct{}{}
		}
	}
	return aliases
}
