package scopeplan

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
)

func TestChanges_SeparateIndexWorkingAndBranchScopes(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = gitscope.Environ()
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, output)
		}
	}
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	write("deleted.go")
	git("add", ".")
	git("-c", "user.name=Scope", "-c", "user.email=scope@example.invalid", "commit", "-qm", "base")
	git("tag", "base")
	write("branch.go")
	git("add", ".")
	git("-c", "user.name=Scope", "-c", "user.email=scope@example.invalid", "commit", "-qm", "branch")
	write("staged.go")
	git("add", "staged.go")
	write("untracked space.go")
	if err := os.Remove(filepath.Join(root, "deleted.go")); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		mode string
		want []string
	}{
		{"staged", []string{"staged.go"}},
		{"changed", []string{"deleted.go", "staged.go", "untracked space.go"}},
		{"base", []string{"branch.go"}},
		{"branch", []string{"branch.go", "deleted.go", "staged.go", "untracked space.go"}},
	} {
		paths, _, err := Changes(context.Background(), root, scenario.mode, "base")
		if err != nil || !reflect.DeepEqual(paths, scenario.want) {
			t.Fatalf("%s: %v %v want %v", scenario.mode, paths, err, scenario.want)
		}
	}
}

func TestDirect_NestedMetadataAndDeletedPackageOwnership(t *testing.T) {
	root := t.TempDir()
	for name, data := range map[string]string{"go.mod": "module root", "rootpkg/a.go": "package rootpkg", "connectors/test/go.mod": "module nested", "connectors/test/pkg/a.go": "package nested"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := Direct(root, []string{"connectors/test/go.mod", "rootpkg/deleted.go", "removed/all.go"}, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []Module{{Directory: ".", Packages: []string{"./rootpkg"}}, {Directory: "connectors/test", Packages: []string{"./..."}}}
	if !reflect.DeepEqual(plan.Modules, want) {
		t.Fatalf("ownership=%+v want %+v", plan.Modules, want)
	}
	if err := os.Remove(filepath.Join(root, "connectors/test/go.mod")); err != nil {
		t.Fatal(err)
	}
	plan, err = Direct(root, []string{"connectors/test/go.mod"}, true)
	if err != nil || len(plan.Modules) != 1 || plan.Modules[0].Directory != "." || !reflect.DeepEqual(plan.Modules[0].Packages, []string{"./..."}) {
		t.Fatalf("deleted module must check surviving parent: %+v %v", plan, err)
	}
}

func TestDirect_SkipsTestdataPackagesLikeTheGoTool(t *testing.T) {
	root := t.TempDir()
	for name, data := range map[string]string{
		"go.mod":                             "module root",
		"internal/gen/testdata/tool/main.go": "//go:build ignore\n\npackage main",
		"internal/gen/a.go":                  "package gen",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := Direct(root, []string{"internal/gen/testdata/tool/main.go", "internal/gen/a.go"}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []Module{{Directory: ".", Packages: []string{"./internal/gen"}}}
	if !reflect.DeepEqual(plan.Modules, want) {
		t.Fatalf("plan=%+v want %+v (testdata is never a package pattern match)", plan.Modules, want)
	}
}
