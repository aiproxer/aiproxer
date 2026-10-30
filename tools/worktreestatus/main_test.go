package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParsePorcelain(t *testing.T) {
	t.Parallel()
	out := "worktree /repo/main\n" +
		"HEAD abcdef0123456789\n" +
		"branch refs/heads/main\n" +
		"\n" +
		"worktree /repo/wt-feat\n" +
		"HEAD 1234567890abcdef\n" +
		"branch refs/heads/feat/thing\n" +
		"locked\n" +
		"\n" +
		"worktree /repo/wt-detached\n" +
		"HEAD 1234567890abcdef\n" +
		"detached\n" +
		"\n" +
		"worktree /repo/wt-bare\n" +
		"bare\n"
	got := parsePorcelain(out)
	if len(got) != 4 {
		t.Fatalf("parsed %d worktrees, want 4: %+v", len(got), got)
	}
	if got[0].branch != "main" || got[0].head != "abcdef0123456789" {
		t.Errorf("main entry = %+v", got[0])
	}
	if got[1].branch != "feat/thing [locked]" {
		t.Errorf("locked entry branch = %q", got[1].branch)
	}
	if got[2].branch != "(detached)" {
		t.Errorf("detached entry branch = %q", got[2].branch)
	}
	if got[3].branch != "(bare)" {
		t.Errorf("bare entry branch = %q", got[3].branch)
	}
}

// testGit runs git with repo-local identity so the test never depends on or
// mutates the developer's global configuration.
func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.name=status-test", "-c", "user.email=status-test@example.invalid", "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, data)
	}
	return string(data)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStatusTempRepo(t *testing.T) {
	root := t.TempDir()
	testGit(t, root, "init", "-b", "main", ".")
	writeFile(t, filepath.Join(root, "a.txt"), "a\n")
	testGit(t, root, "add", "a.txt")
	testGit(t, root, "commit", "-m", "base")

	wt := filepath.Join(root, "wt-feat")
	testGit(t, root, "worktree", "add", wt, "-b", "feat/one")
	writeFile(t, filepath.Join(wt, "b.txt"), "b\n")
	testGit(t, wt, "add", "b.txt")
	testGit(t, wt, "commit", "-m", "one ahead")
	writeFile(t, filepath.Join(wt, "dirty.txt"), "untracked\n")

	detached := filepath.Join(root, "wt-detached")
	testGit(t, root, "worktree", "add", "--detach", detached, "main")

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var buf strings.Builder
	if err := statusIn(ctx, root, "main", &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"feat/one", "1/0", "dirty(1)", "(detached)", "3 worktree(s); ahead/behind against main"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
}

func TestStatusMissingBase(t *testing.T) {
	root := t.TempDir()
	testGit(t, root, "init", "-b", "main", ".")
	writeFile(t, filepath.Join(root, "a.txt"), "a\n")
	testGit(t, root, "add", "a.txt")
	testGit(t, root, "commit", "-m", "base")

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var buf strings.Builder
	if err := statusIn(ctx, root, "origin/main", &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "base ref origin/main not found locally") {
		t.Errorf("status output missing absent-base note:\n%s", out)
	}
}
