package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestGuard_RejectsIndexMutationAndPreservesFailure(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if _, err := sourceGit(t.Context(), repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	file := filepath.Join(repo, "tracked")
	if err := os.WriteFile(file, []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "tracked")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath="+filepath.Join(repo, "no-hooks"), "commit", "-qm", "base")
	if err := guardCommand(t.Context(), repo, "", []string{"git", "status", "--porcelain"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := guardCommand(t.Context(), repo, "", []string{"git", "rev-parse", "--verify", "missing"}, io.Discard); err == nil {
		t.Fatal("child failure hidden")
	}
	if err := os.WriteFile(file, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// git add preserves final working bytes but changes the index. Both planes
	// must be bound, not just HEAD-to-worktree content.
	if err := guardCommand(t.Context(), repo, "", []string{"git", "add", "tracked"}, io.Discard); err == nil {
		t.Fatal("index mutation accepted")
	}
	index := filepath.Join(t.TempDir(), "alternate.index")
	if _, err := sourceGitWithIndex(t.Context(), repo, index, "read-tree", "HEAD"); err != nil {
		t.Fatal(err)
	}
	if err := guardCommand(t.Context(), repo, index, []string{"git", "add", "tracked"}, io.Discard); err == nil {
		t.Fatal("alternate index mutation accepted")
	}
}
