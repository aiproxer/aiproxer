//go:build linux || darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
)

// allowUncertainFixture records whether the host leaves same-user processes
// uninspectable (restricted /proc). The fixture below then exercises the
// explicit opt-in path instead of skipping, while confirmed occupants and
// dirty state still refuse.
var allowUncertainFixture bool

func TestCleanup_DryRunPreservesThenApplyRemovesOnlySelectedFixture(t *testing.T) {
	container := filepath.Join(t.TempDir(), "container")
	repo := filepath.Join(container, "branches", "main")
	path := filepath.Join(container, "worktrees", "task")
	if _, uncertain, err := activeUsers(t.Context(), path); err != nil {
		if os.Getenv("AIP_REQUIRE_CLEANUP_INTEGRATION") == "1" {
			t.Fatalf("required cleanup integration cannot run: %v", err)
		}
		t.Skipf("host cannot certify active-user ownership: %v", err)
	} else if len(uncertain) > 0 {
		t.Logf("host leaves %d same-user process(es) uninspectable; exercising the explicit opt-in path", len(uncertain))
		allowUncertainFixture = true
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = gitscope.Environ()
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, data)
		}
		return strings.TrimSpace(string(data))
	}
	git(repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(repo, "add", ".")
	git(repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "base")
	git(repo, "worktree", "add", "-b", "fix/task", path, "main")
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("delivered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(path, "add", ".")
	git(path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "task")
	head := git(path, "rev-parse", "HEAD")
	git(repo, "merge", "--squash", "fix/task")
	git(repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "merge")
	merge := git(repo, "rev-parse", "HEAD")
	git(repo, "update-ref", "refs/remotes/origin/main", merge)
	git(repo, "update-ref", "refs/remotes/origin/fix/task", head)
	git(repo, "config", "branch.fix/task.remote", "origin")
	git(repo, "config", "branch.fix/task.merge", "refs/heads/fix/task")
	bin := filepath.Join(container, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' '{\"state\":\"MERGED\",\"headRefName\":\"fix/task\",\"headRefOid\":\"" + head + "\",\"mergeCommit\":{\"oid\":\"" + merge + "\"}}'\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Chdir(repo)
	if err := os.WriteFile(filepath.Join(path, "untracked.txt"), []byte("preserve me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanup(t.Context(), path, "fix/task", 42, true, false, allowUncertainFixture); err == nil {
		t.Fatal("discarded untracked work")
	}
	if err := os.Remove(filepath.Join(path, "untracked.txt")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(path)
	if err := cleanup(t.Context(), path, "fix/task", 42, true, false, allowUncertainFixture); err == nil {
		t.Fatal("removed active worktree")
	}
	t.Chdir(repo)
	if err := cleanup(t.Context(), path, "fix/task", 42, false, false, allowUncertainFixture); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		t.Fatal("dry run removed selected worktree")
	}
	if err := cleanup(t.Context(), path, "fix/task", 42, true, false, allowUncertainFixture); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("applied cleanup retained worktree")
	}
	git(repo, "show-ref", "--verify", "refs/remotes/origin/fix/task")
	if data := git(repo, "status", "--porcelain"); data != "" {
		t.Fatalf("cleanup changed receiver: %s", data)
	}
}
