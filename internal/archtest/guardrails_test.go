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

func TestStandardBundlePackagesHaveNoInitFunctions(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dirs := []string{
		filepath.Join(root, "internal", "pluginreg"),
		filepath.Join(root, "internal", "standardplugins"),
		filepath.Join(root, "cmd", "aipstd"),
	}
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			t.Parallel()
			err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
					return nil
				}
				if hasInitFunc(path) {
					t.Fatalf("forbid init() in standard bundle path: %s", path)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTestsMustNotRegisterStandardBundleInInit(t *testing.T) {
	t.Parallel()
	initDecl := "func init" + "("
	regStd := "RegisterStandard" + "Bundle()"
	root := repoRoot(t)
	var bad []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := info.Name()
			if base == "vendor" || base == ".git" || base == ".worktrees" || base == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		s := string(src)
		if strings.Contains(s, initDecl) && strings.Contains(s, regStd) {
			bad = append(bad, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("tests must not register standard bundle in init():\n%s", strings.Join(bad, "\n"))
	}
}

func TestRuntimebundleDoesNotSelectPluginregDefault(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "infra", "runtimebundle")
	var bad []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
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
		if strings.Contains(string(src), "pluginreg.Default") {
			bad = append(bad, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("runtimebundle must not reference pluginreg.Default:\n%s", strings.Join(bad, "\n"))
	}
}

func TestCompositionLayersDoNotRegisterStandardBundle(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	allowed := map[string]bool{
		filepath.Join(root, "internal", "infra", "runtimebundle", "composition_root.go"): true,
	}
	var bad []string
	for _, dir := range []string{
		filepath.Join(root, "internal", "infra", "runtimebundle"),
		filepath.Join(root, "internal", "stdhttp"),
	} {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			if allowed[path] {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(src), "InstallStandardBundleOn") {
				bad = append(bad, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(bad) != 0 {
		t.Fatalf("InstallStandardBundleOn only allowed in composition_root.go:\n%s", strings.Join(bad, "\n"))
	}
}

func TestWiringRootsHaveNoPackageLevelPluginRegistryOrSyncOnce(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dirs := []string{
		filepath.Join(root, "internal", "infra", "runtimebundle"),
		filepath.Join(root, "internal", "stdhttp"),
		filepath.Join(root, "cmd", "aipstd"),
	}
	var bad []string
	for _, dir := range dirs {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
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
			_, f, err := ParseGoSource(path, src)
			if err != nil {
				return err
			}
			if fileHasPackageLevelRegistryOrOnce(f) {
				bad = append(bad, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(bad) != 0 {
		t.Fatalf("wiring roots must not declare package-level pluginreg.Registry or sync.Once:\n%s", strings.Join(bad, "\n"))
	}
}

func TestCompositionRootDoesNotPairSyncOnceWithStandardBundleInstall(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "cmd", "aipstd")
	err := filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		s := string(src)
		if strings.Contains(s, "sync.Once") && strings.Contains(s, "InstallStandardBundleOn") {
			t.Errorf("%s pairs sync.Once with InstallStandardBundleOn", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPolicyDiagnosticsEnabledNotReferencedFromFrontendOrStdhttp(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	var bad []string
	for _, dir := range []string{
		filepath.Join(root, "internal", "plugins", "frontends"),
		filepath.Join(root, "internal", "stdhttp"),
	} {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
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
			if referencesIdent(path, src, "PolicyDiagnosticsEnabled") {
				bad = append(bad, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(bad) != 0 {
		t.Fatalf("PolicyDiagnosticsEnabled must not be referenced from frontend/stdhttp:\n%s", strings.Join(bad, "\n"))
	}
}

func TestFailureModeAndTimeoutBudgetNotClientSourced(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "plugins", "frontends")
	forbidden := []string{"FailOpen", "FailClosed", "FailureMode", "TimeoutBudget", "TimeoutFor"}
	var bad []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
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
		for _, name := range forbidden {
			if referencesIdent(path, src, name) {
				bad = append(bad, path+" ("+name+")")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("frontend adapters must not reference failure-mode/timeout identifiers:\n%s", strings.Join(bad, "\n"))
	}
}

func hasInitFunc(path string) bool {
	src, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(src), "func init(")
}

func referencesIdent(filename string, src []byte, name string) bool {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return false
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if id.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}

func fileHasPackageLevelRegistryOrOnce(f *ast.File) bool {
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if typeMentions(vs.Type, "Registry") || typeMentions(vs.Type, "Once") {
				return true
			}
			for _, v := range vs.Values {
				if callMentions(v, "NewRegistry") || callMentions(v, "Once") {
					return true
				}
			}
		}
	}
	return false
}

func typeMentions(expr ast.Expr, name string) bool {
	if expr == nil {
		return false
	}
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == name
	case *ast.SelectorExpr:
		return t.Sel != nil && t.Sel.Name == name
	case *ast.StarExpr:
		return typeMentions(t.X, name)
	}
	return false
}

func callMentions(expr ast.Expr, name string) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name == name
	case *ast.SelectorExpr:
		return fun.Sel != nil && fun.Sel.Name == name
	}
	return false
}
