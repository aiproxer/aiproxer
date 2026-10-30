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

const importInternalCoreConfigReload = "github.com/aiproxer/aiproxer/internal/core/configreload"

// TestReloadPublicFacade_SignaturesUseSDKContract proves public reload method
// and interface signatures use SDK types/aliases and do not import the internal
// contract package for vocabulary (Task 2.3).
func TestReloadPublicFacade_SignaturesUseSDKContract(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "pkg", "aipruntime")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var findings []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		rel := "pkg/aipruntime/" + e.Name()
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, rel, src, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		aliases := map[string]string{}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			name := ""
			if imp.Name != nil {
				name = imp.Name.Name
			} else {
				parts := strings.Split(path, "/")
				name = parts[len(parts)-1]
			}
			if path == importInternalCoreConfigReload {
				findings = append(findings, rel+": public production file must not import internal/core/configreload")
			}
			if name != "." && name != "_" {
				aliases[name] = path
			}
		}
		// reloadQuery / ReloadControl / Runtime reload methods must not mention
		// internal configreload selectors in signatures.
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Type == nil {
				continue
			}
			if fd.Name == nil {
				continue
			}
			switch fd.Name.Name {
			case "Reload", "Status", "ReloadStatus", "ReloadControl", "newReloadControl", "bindReloadQuery":
				findings = append(findings, scanSignatureInternalSelectors(rel, fd, aliases)...)
			}
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil || ts.Name.Name != "reloadQuery" {
					continue
				}
				iface, ok := ts.Type.(*ast.InterfaceType)
				if !ok || iface.Methods == nil {
					continue
				}
				for _, field := range iface.Methods.List {
					ft, ok := field.Type.(*ast.FuncType)
					if !ok {
						continue
					}
					findings = append(findings, scanFuncTypeInternalSelectors(rel, "reloadQuery", ft, aliases)...)
				}
			}
		}
	}
	if len(findings) > 0 {
		t.Fatalf("public reload facade signatures must use SDK contract types only:\n%s",
			strings.Join(findings, "\n"))
	}
}

func scanSignatureInternalSelectors(rel string, fd *ast.FuncDecl, aliases map[string]string) []string {
	if fd.Type == nil {
		return nil
	}
	return scanFuncTypeInternalSelectors(rel, fd.Name.Name, fd.Type, aliases)
}

func scanFuncTypeInternalSelectors(rel, name string, ft *ast.FuncType, aliases map[string]string) []string {
	var out []string
	check := func(expr ast.Expr) {
		ast.Inspect(expr, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if aliases[pkg.Name] == importInternalCoreConfigReload {
				out = append(out, rel+": "+name+" signature uses internal configreload."+sel.Sel.Name)
			}
			return true
		})
	}
	if ft.Params != nil {
		for _, f := range ft.Params.List {
			check(f.Type)
		}
	}
	if ft.Results != nil {
		for _, f := range ft.Results.List {
			check(f.Type)
		}
	}
	return out
}
