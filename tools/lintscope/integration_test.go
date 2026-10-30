package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
)

// scopeGit runs a git command against a fixture repository rooted at root.
//
// The git repository-location variables are cleared on purpose: git exports
// GIT_DIR to every hook it runs, so without this a fixture `git init` created
// under the pre-commit hook initialises and commits inside the real repository
// instead of the temporary fixture, destroying its refs and index.
func scopeGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = gitscope.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func scopeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":                        "module example.com/scope\n\ngo 1.26.0\n",
		"base/base.go":                  "package base\nconst Value = 1\n",
		"consumer/consumer.go":          "package consumer\nimport \"example.com/scope/base\"\nconst Value = base.Value\n",
		"testconsumer/consumer.go":      "package testconsumer\n",
		"testconsumer/consumer_test.go": "package testconsumer_test\nimport (\"testing\"; \"example.com/scope/consumer\")\nfunc TestValue(t *testing.T) { if consumer.Value != 1 { t.Fatal(consumer.Value) } }\n",
		"unrelated/unrelated.go":        "package unrelated\n",
	}
	for name, body := range files {
		writeScopeFile(t, root, name, body)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Scope Test", "-c", "user.email=scope@example.invalid", "commit", "-qm", "fixture"}} {
		scopeGit(t, root, args...)
	}
	return root
}

func writeScopeFile(t *testing.T, root, name, body string) {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLocalLintPlanReadsActualGitAndTestImportGraph(t *testing.T) {
	root := scopeFixture(t)
	if runtime.GOOS != "windows" {
		link := filepath.Join(t.TempDir(), "repository-link")
		if err := os.Symlink(root, link); err != nil {
			t.Fatal(err)
		}
		root = link
	}
	writeScopeFile(t, root, "base/base.go", "package base\nconst Value = 2\n")
	plan, err := buildLintPlan(context.Background(), root, "changed", "", false)
	if err != nil {
		t.Fatal(err)
	}
	want := []moduleScope{{Directory: ".", Packages: []string{"./base", "./consumer", "./testconsumer"}}}
	if plan.Full || !reflect.DeepEqual(plan.Modules, want) {
		t.Fatalf("plan=%+v want %v", plan, want)
	}
}

func TestLocalLintPlanUntrackedNestedModuleUsesModuleRelativeScope(t *testing.T) {
	root := scopeFixture(t)
	writeScopeFile(t, root, "connectors/test/go.mod", "module example.com/connector\n\ngo 1.26.0\n")
	writeScopeFile(t, root, "connectors/test/backend/backend.go", "package backend\n")
	// Dependency changes deliberately force the comprehensive fallback.
	plan, err := buildLintPlan(context.Background(), root, "changed", "", false)
	if err != nil || !plan.Full {
		t.Fatalf("untracked dependency change plan=%+v err=%v", plan, err)
	}
	scopeGit(t, root, "add", ".")
	scopeGit(t, root, "-c", "user.name=Scope Test", "-c", "user.email=scope@example.invalid", "commit", "-qm", "connector")
	writeScopeFile(t, root, "connectors/test/backend/backend.go", "package backend\nconst Value = 2\n")
	plan, err = buildLintPlan(context.Background(), root, "changed", "", false)
	if err != nil {
		t.Fatal(err)
	}
	want := []moduleScope{{Directory: "connectors/test", Packages: []string{"./backend"}}}
	if plan.Full || !reflect.DeepEqual(plan.Modules, want) {
		t.Fatalf("nested plan=%+v want %v", plan, want)
	}
}

func TestLocalLintPlanDoesNotHideDiscoveryFailure(t *testing.T) {
	root := scopeFixture(t)
	writeScopeFile(t, root, "base/base.go", "package base\nimport _ \"example.invalid/unavailable\"\n")
	if _, err := buildLintPlan(context.Background(), root, "changed", "", false); err == nil {
		t.Fatal("failed graph discovery must fail closed")
	}
}

func TestLocalLintPlanDocumentationAndEmptyIndex(t *testing.T) {
	root := scopeFixture(t)
	writeScopeFile(t, root, "docs/note.md", "documentation\n")
	for _, mode := range []string{"changed", "staged"} {
		plan, err := buildLintPlan(context.Background(), root, mode, "", false)
		if err != nil || plan.Full || len(plan.Modules) != 0 {
			t.Fatalf("%s plan=%+v err=%v", mode, plan, err)
		}
	}
}

func TestLocalLintPlanCleanCheckoutRetainsOnlyTheRootGate(t *testing.T) {
	plan, err := buildLintPlan(context.Background(), scopeFixture(t), "changed", "", false)
	want := []moduleScope{{Directory: ".", Packages: []string{"./..."}}}
	if err != nil || plan.Full || !reflect.DeepEqual(plan.Modules, want) {
		t.Fatalf("clean plan=%+v err=%v", plan, err)
	}
}

func TestLocalLintPlanStagedScopeDoesNotIncludeUnstagedPackages(t *testing.T) {
	root := scopeFixture(t)
	writeScopeFile(t, root, "base/base.go", "package base\nconst Value = 2\n")
	scopeGit(t, root, "add", "base/base.go")
	writeScopeFile(t, root, "unrelated/new.go", "package unrelated\nconst New = 1\n")
	plan, err := buildLintPlan(context.Background(), root, "staged", "", false)
	want := []moduleScope{{Directory: ".", Packages: []string{"./base", "./consumer", "./testconsumer"}}}
	if err != nil || plan.Full || !reflect.DeepEqual(plan.Modules, want) {
		t.Fatalf("staged plan=%+v err=%v want %v", plan, err, want)
	}
}

func TestLocalLintPlanDoesNotHideGitFailure(t *testing.T) {
	if _, err := buildLintPlan(context.Background(), t.TempDir(), "changed", "", false); err == nil {
		t.Fatal("failed Git discovery must fail closed")
	}
}

func TestLocalLintPlanBaseScopeReadsCommittedBranchDiff(t *testing.T) {
	root := scopeFixture(t)
	scopeGit(t, root, "tag", "fixture-base")
	writeScopeFile(t, root, "base/base.go", "package base\nconst Value = 2\n")
	scopeGit(t, root, "-c", "user.name=Scope Test", "-c", "user.email=scope@example.invalid", "commit", "-qam", "change")
	writeScopeFile(t, root, "unrelated/new.go", "package unrelated\nconst New = 1\n")
	plan, err := buildLintPlan(context.Background(), root, "base", "fixture-base", false)
	want := []moduleScope{{Directory: ".", Packages: []string{"./base", "./consumer", "./testconsumer"}}}
	if err != nil || plan.Full || !reflect.DeepEqual(plan.Modules, want) {
		t.Fatalf("base plan=%+v err=%v want %v", plan, err, want)
	}
	if _, err := buildLintPlan(context.Background(), root, "base", "", false); err == nil {
		t.Fatal("base mode without a commit must fail closed")
	}
}

func TestLocalLintPlanDirectScopeSkipsConsumersAndSharedFallback(t *testing.T) {
	root := scopeFixture(t)
	writeScopeFile(t, root, "base/base.go", "package base\nconst Value = 2\n")
	writeScopeFile(t, root, "Makefile", "all:\n")
	scopeGit(t, root, "add", "base/base.go", "Makefile")
	plan, err := buildLintPlan(context.Background(), root, "staged", "", true)
	want := []moduleScope{{Directory: ".", Packages: []string{"./base"}}}
	if err != nil || plan.Full || !reflect.DeepEqual(plan.Modules, want) {
		t.Fatalf("direct plan=%+v err=%v want %v", plan, err, want)
	}
}
