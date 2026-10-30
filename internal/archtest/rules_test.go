package archtest

import (
	"go/ast"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := FindRepoRoot(wd)
	if err != nil {
		t.Fatal("could not find go.mod above", wd)
	}
	return root
}

func TestForbiddenDeclarationsAbsent(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got, err := ScanForbiddenDeclarationsIncludingTests(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 0 {
		var b strings.Builder
		for _, f := range got {
			b.WriteString(f.String())
			b.WriteByte('\n')
		}
		t.Fatalf("forbidden declarations present (%d):\n%s", len(got), b.String())
	}
}

func TestAbsentFilesStayDeleted(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got, err := ScanAbsentFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 0 {
		t.Fatalf("deleted files reappeared: %v", got)
	}
}

func TestForbiddenImportsAbsent(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got, err := ScanForbiddenImports(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 0 {
		var b strings.Builder
		for _, f := range got {
			b.WriteString(f.String())
			b.WriteByte('\n')
		}
		t.Fatalf("forbidden imports present (%d):\n%s", len(got), b.String())
	}
}

func TestPackageTreeBudgetsExact(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	if len(PackageTreeBudgets) != 5 {
		t.Fatalf("PackageTreeBudgets: want 5 entries, got %d", len(PackageTreeBudgets))
	}
	for _, tc := range PackageTreeBudgets {
		t.Run(tc.Tree, func(t *testing.T) {
			t.Parallel()
			n, err := CountNonTestGoLines(filepath.Join(root, filepath.FromSlash(tc.Tree)))
			if err != nil {
				t.Fatal(err)
			}
			if n > tc.Max {
				t.Fatalf("%s: measured %d exceeds budget ceiling %d", tc.Tree, n, tc.Max)
			}
			found := false
			for _, b := range LineBudgets {
				if b.Dir == tc.Tree {
					found = true
					if b.Max != tc.Max {
						t.Fatalf("%s: LineBudgets max=%d must equal PackageTreeBudgets Max=%d", tc.Tree, b.Max, tc.Max)
					}
				}
			}
			if !found {
				t.Fatalf("%s missing from LineBudgets", tc.Tree)
			}
		})
	}
}

func TestCriticalFileBudgets(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, b := range CriticalFileBudgets {
		t.Run(b.Path, func(t *testing.T) {
			t.Parallel()
			n, err := CountFileLines(filepath.Join(root, filepath.FromSlash(b.Path)))
			if err != nil {
				t.Fatal(err)
			}
			if n > b.Max {
				t.Fatalf("%s: measured %d exceeds budget ceiling %d", b.Path, n, b.Max)
			}
		})
	}
}

func TestLineComplexityBudgets(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, b := range LineBudgets {
		t.Run(b.Dir, func(t *testing.T) {
			t.Parallel()
			n, err := CountNonTestGoLines(filepath.Join(root, filepath.FromSlash(b.Dir)))
			if err != nil {
				t.Fatal(err)
			}
			if n > b.Max {
				t.Fatalf("%s: measured %d exceeds budget ceiling %d", b.Dir, n, b.Max)
			}
		})
	}
}

func TestPackageTreeBudgetsReportSection(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	section, err := FormatRuntimeConvergencePackageBudgets(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(section, "## Runtime-convergence package budgets") {
		t.Fatalf("missing heading:\n%s", section)
	}
	for _, b := range PackageTreeBudgets {
		n, err := CountNonTestGoLines(filepath.Join(root, filepath.FromSlash(b.Tree)))
		if err != nil {
			t.Fatal(err)
		}
		if n > b.Max {
			t.Fatalf("%s: measured %d exceeds budget ceiling %d", b.Tree, n, b.Max)
		}
		want := "| `" + b.Tree + "` | " + strconv.Itoa(n) + " | " + strconv.Itoa(b.Max) + " |"
		if !strings.Contains(section, want) {
			t.Fatalf("want row %q in:\n%s", want, section)
		}
	}
}

func TestLegacyOptionsFieldsAbsent(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "pkg/aipruntime/options.go")
	_, f, err := ParseGoSource(path, mustRead(t, path))
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]bool{
		"RequestProviders": true, "AttemptProviders": true, "ConcurrencyProvider": true,
		"Rater": true, "ProviderDescriptors": true,
	}
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != "Options" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, field := range st.Fields.List {
			for _, name := range field.Names {
				if name != nil && forbidden[name.Name] {
					t.Errorf("Options field %s must stay deleted", name.Name)
				}
			}
		}
		return false
	})
}

func TestRuntimeFacadeOneHostDependency(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "pkg/aipruntime/build.go")
	_, f, err := ParseGoSource(path, mustRead(t, path))
	if err != nil {
		t.Fatal(err)
	}
	var rt *ast.StructType
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != "Runtime" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		rt = st
		return false
	})
	if rt == nil {
		t.Fatal("missing Runtime struct")
	}
	forbidden := map[string]bool{
		"Manager": true, "Process": true, "Executor": true, "Coordinator": true,
		"ShutdownTracing": true, "closers": true, "ledger": true,
	}
	hostFields := 0
	for _, field := range rt.Fields.List {
		for _, name := range field.Names {
			if name == nil {
				continue
			}
			if forbidden[name.Name] {
				t.Errorf("Runtime must not expose ownership field %s", name.Name)
			}
			if name.Name == "host" {
				hostFields++
			}
		}
	}
	if hostFields != 1 {
		t.Fatalf("Runtime must have exactly one host field, got %d", hostFields)
	}
}

func TestServerGoListenAndServeSeamOnly(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	path := filepath.Join(root, "internal/stdhttp/server.go")
	src := string(mustRead(t, path))
	for _, bad := range []string{"RunWithRuntime", "releaseBuiltResources", "runClosers"} {
		if strings.Contains(src, bad) {
			t.Fatalf("server.go must not contain %s", bad)
		}
	}
	if !strings.Contains(src, "listenAndServe") {
		t.Fatal("server.go must retain listenAndServe seam")
	}
	n, err := CountFileLines(path)
	if err != nil {
		t.Fatal(err)
	}
	if n != 8 {
		t.Fatalf("server.go: measured %d, want exact 8", n)
	}
}

func TestSoleDataPlaneServeAPI(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	var decls []string
	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		if PackageDirFromRel(rel) != "internal/stdhttp" {
			return nil
		}
		_, f, err := ParseGoSource(abs, src)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || fd.Name == nil {
				continue
			}
			switch fd.Name.Name {
			case "RunWithGenerationHost", "RunWithRuntime", "NewStandardHandler":
				decls = append(decls, fd.Name.Name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(decls) != 1 || decls[0] != "RunWithGenerationHost" {
		t.Fatalf("stdhttp data-plane serve surface must be exactly [RunWithGenerationHost], got %v", decls)
	}
}

func TestBuildHostIsSoleHostConstructor(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	var buildHostFiles []string
	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		if PackageDirFromRel(rel) != "internal/infra/runtimebundle" {
			return nil
		}
		_, f, err := ParseGoSource(abs, src)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if ok && fd.Recv == nil && fd.Name != nil && fd.Name.Name == "BuildHost" {
				buildHostFiles = append(buildHostFiles, rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(buildHostFiles) != 1 || buildHostFiles[0] != "internal/infra/runtimebundle/host_build.go" {
		t.Fatalf("want exactly one BuildHost at host_build.go, got %v", buildHostFiles)
	}
}

func TestArchtestMaintainabilityLimits(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal/archtest")
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		base := filepath.Base(path)
		if strings.HasPrefix(base, "task") && (strings.Contains(base, "_scan") || strings.Contains(base, "_detector") || strings.Contains(base, "_gate")) {
			t.Errorf("task-number-specific architecture scanner must not remain: %s", base)
		}
		n, err := CountFileLines(path)
		if err != nil {
			return err
		}
		if n > 500 {
			t.Errorf("%s has %d lines (limit 500)", SlashPath(strings.TrimPrefix(path, root+string(os.PathSeparator))), n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
