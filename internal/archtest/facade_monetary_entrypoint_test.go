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

// TestBuildWithBillingIsTheSingleMonetaryEntrypoint proves exactly one
// exported pkg/aipruntime function touches the typed billing binding, that it
// is named BuildWithBilling, and that stock aipstd startup never calls it.
// Ordinary Options and stock startup stay non-money (see
// TestPhase1PublicOptionsStayNonMonetary); this gate names the one exception
// and forbids any second one.
func TestBuildWithBillingIsTheSingleMonetaryEntrypoint(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "pkg", "aipruntime")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	referencing := map[string]bool{}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		billingLocal := ""
		for _, imp := range file.Imports {
			impPath := strings.Trim(imp.Path.Value, `"`)
			if strings.HasSuffix(impPath, "/pkg/aipsdk/billing") {
				if imp.Name != nil {
					billingLocal = imp.Name.Name
				} else {
					billingLocal = "billing"
				}
			}
		}
		if billingLocal == "" {
			continue
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil || !ast.IsExported(fn.Name.Name) || fn.Body == nil {
				continue
			}
			touches := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if ok && id.Name == billingLocal {
					touches = true
					return false
				}
				return true
			})
			ast.Inspect(fn.Type, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if ok && id.Name == billingLocal {
					touches = true
					return false
				}
				return true
			})
			if touches {
				referencing[fn.Name.Name] = true
			}
		}
	}
	if len(referencing) != 1 || !referencing["BuildWithBilling"] {
		t.Fatalf("monetary entrypoints = %v, want exactly [BuildWithBilling]", referencing)
	}
	aipstd, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(aipstdCommandPath)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(aipstd), "BuildWithBilling") {
		t.Fatal("stock aipstd startup must remain non-money: must not call BuildWithBilling")
	}
}

// TestFacadeConvergesOnOneBuildHost proves Build and BuildWithBilling share a
// single BuildHost invocation in the public facade: no second host, copied
// runtime stack, or forked reload path.
func TestFacadeConvergesOnOneBuildHost(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "pkg", "aipruntime")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	sites := 0
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel != nil && sel.Sel.Name == "BuildHost" {
				sites++
			}
			return true
		})
	}
	if sites != 1 {
		t.Fatalf("pkg/aipruntime BuildHost call sites = %d, want exactly 1 shared assembly", sites)
	}
}
