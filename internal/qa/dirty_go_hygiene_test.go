package qa

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
)

func TestParsePorcelainGoPaths_SelectsGoOnly(t *testing.T) {
	t.Parallel()
	raw := strings.Join([]string{
		" M pkg/a.go",
		"?? cmd/new.go",
		"D  pkg/old.go",
		" M README.md",
		"?? docs/notes.md",
		"R  pkg/from.go",
		"pkg/to.go",
		"A  pkg/added.go",
	}, "\x00") + "\x00"
	got := parsePorcelainGoPaths([]byte(raw))
	want := []string{"pkg/a.go", "cmd/new.go", "pkg/old.go", "pkg/from.go", "pkg/to.go", "pkg/added.go"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("parsePorcelainGoPaths=%q, want %q", got, want)
	}
}

func TestParsePorcelainGoPaths_Dedups(t *testing.T) {
	t.Parallel()
	raw := " M pkg/a.go\x00M  pkg/a.go\x00"
	got := parsePorcelainGoPaths([]byte(raw))
	if len(got) != 1 || got[0] != "pkg/a.go" {
		t.Fatalf("dedup=%q, want [pkg/a.go]", got)
	}
}

func TestDirtyGoLimitExceeded(t *testing.T) {
	t.Parallel()
	if dirtyGoLimitExceeded(maxDirtyGoFiles) {
		t.Fatalf("%d files must still pass", maxDirtyGoFiles)
	}
	if !dirtyGoLimitExceeded(maxDirtyGoFiles + 1) {
		t.Fatalf("%d files must fail", maxDirtyGoFiles+1)
	}
	names := make([]string, 0, maxDirtyGoFiles+1)
	var raw []byte
	for i := range maxDirtyGoFiles + 1 {
		name := fmt.Sprintf("pkg/f%03d.go", i)
		names = append(names, name)
		raw = append(raw, []byte("?? "+name)...)
		raw = append(raw, 0)
	}
	got := parsePorcelainGoPaths(raw)
	if len(got) != len(names) {
		t.Fatalf("parsed %d paths, want %d", len(got), len(names))
	}
	if !dirtyGoLimitExceeded(len(got)) {
		t.Fatal("101 dirty Go files must exceed the hygiene limit")
	}
}

func TestRootHygiene_DirtyGoFiles(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	if mergeInProgress(root) {
		t.Skip("a merge is in progress; its dirty files are already-reviewed integration-branch " +
			"content and must not be charged to the unreviewed-change budget")
	}
	if largeChangeOverride() {
		t.Skipf("%s is set: the maintainer authorized a change above the budget", largeChangeOverrideEnv)
	}
	paths, err := listDirtyGoFiles(root)
	if err != nil {
		t.Fatalf("list dirty Go files: %v", err)
	}
	if dirtyGoLimitExceeded(len(paths)) {
		show := paths
		if len(show) > 20 {
			show = show[:20]
		}
		t.Fatalf("worktree has %d dirty *.go files (limit %d); split the change. first paths: %s",
			len(paths), maxDirtyGoFiles, strings.Join(show, ", "))
	}
}

func TestListDirtyGoFiles_TempRepoExceedsLimit(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	gitQA(t, dir, "init", "-q")
	gitQA(t, dir, "config", "core.autocrlf", "false")
	for i := range maxDirtyGoFiles + 1 {
		path := filepath.Join(dir, fmt.Sprintf("f%03d.go", i))
		if err := os.WriteFile(path, []byte("package p\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := listDirtyGoFiles(dir)
	if err != nil {
		t.Fatalf("listDirtyGoFiles: %v", err)
	}
	if !dirtyGoLimitExceeded(len(paths)) {
		t.Fatalf("untracked repo listed %d dirty Go files, want > %d (%v)", len(paths), maxDirtyGoFiles, paths)
	}
}

func gitQA(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitscope.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestLargeChangeOverride_FollowsTheChangeSizeOverrideEnv(t *testing.T) {
	for value, want := range map[string]bool{"": false, "0": false, "1": true, "true": true, "yes": true, "on": true} {
		t.Setenv(largeChangeOverrideEnv, value)
		if got := largeChangeOverride(); got != want {
			t.Errorf("%s=%q: largeChangeOverride() = %v, want %v", largeChangeOverrideEnv, value, got, want)
		}
	}
}
