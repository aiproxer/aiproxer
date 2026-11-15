package qa

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
	"github.com/aiproxer/aiproxer/internal/testkit/opensource"
)

// trackedGoFiles lists the worktree's tracked Go files as sorted,
// repository-relative slash paths. Reading the index instead of walking the
// tree keeps scratch files out of repository checks, and an exported tree
// (fresh history, a subset of the files) measures exactly what it ships: an
// absent recorded path is a removal, never a failure. A published export
// carries no Git metadata at all, so the walk of that tree is its file list.
func trackedGoFiles(t *testing.T, root string) []string {
	t.Helper()

	files, err := listTrackedGoFiles(root)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return files
}

// listTrackedGoFiles returns the Go files of root from the Git index. Only a
// published export may fall back to a filesystem walk, and only because it
// carries no .git entry to read and no Enterprise tree the walk could reach:
// every other git failure (a broken checkout, a dirty index) stays an error.
func listTrackedGoFiles(root string) ([]string, error) {
	cmd := exec.Command("git", "-C", root, "ls-files", "-z")
	cmd.Env = gitscope.Environ()
	out, err := cmd.Output()
	if err != nil {
		if isPublishedExport(root) {
			return walkGoFiles(root)
		}
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	var files []string
	for _, name := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if strings.HasSuffix(name, ".go") {
			files = append(files, name)
		}
	}
	sort.Strings(files)
	return files, nil
}

// isPublishedExport reports whether root is the published tree: no .git entry,
// so the index is unreadable, and no Enterprise tree, so a walk measures only
// what the export ships.
func isPublishedExport(root string) bool {
	_, err := os.Lstat(filepath.Join(root, ".git"))
	return errors.Is(err, fs.ErrNotExist) && opensource.IsOpenSourceTree(root)
}

// walkGoFiles returns every Go file under root as sorted, root-relative slash
// paths. The export ships its whole tree, so the walk excludes nothing.
func walkGoFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	sort.Strings(files)
	return files, nil
}

// TestTrackedGoFiles_PublishedExportWalksGoFiles proves the fallback the public
// export needs: the extracted tree has no .git, so git ls-files cannot run, and
// the walk must still report the nested Go files while ignoring other files.
func TestTrackedGoFiles_PublishedExportWalksGoFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fixtures := []struct {
		path    string
		content string
	}{
		{path: "a.go", content: "package a\n"},
		{path: "sub/b.go", content: "package sub\n"},
		{path: "sub/notes.md", content: "# notes\n"},
		{path: "vendor/c.go", content: "package c\n"},
	}
	for _, fixture := range fixtures {
		path := filepath.Join(root, filepath.FromSlash(fixture.path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create fixture directory for %s: %v", fixture.path, err)
		}
		if err := os.WriteFile(path, []byte(fixture.content), 0o600); err != nil {
			t.Fatalf("write fixture %s: %v", fixture.path, err)
		}
	}

	got := trackedGoFiles(t, root)
	want := []string{"a.go", "sub/b.go", "vendor/c.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("published export files = %v, want %v", got, want)
	}
}

// TestTrackedGoFiles_PrivateTreeWithoutGitFailsClosed proves the fallback is
// reserved for the published export: a private tree that lost its Git metadata
// still fails instead of measuring the Enterprise tree by walking it.
func TestTrackedGoFiles_PrivateTreeWithoutGitFailsClosed(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "enterprise"), 0o755); err != nil {
		t.Fatalf("create Enterprise tree: %v", err)
	}

	_, err := listTrackedGoFiles(root)
	if err == nil {
		t.Fatal("a tree with an Enterprise directory must not fall back to a walk")
	}
	if !strings.Contains(err.Error(), "git ls-files") {
		t.Fatalf("error = %v, want the git ls-files failure", err)
	}
}
