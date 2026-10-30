package archtest

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type mockArchtestFS struct {
	files         map[string][]byte
	dirs          map[string][]os.DirEntry
	readErr       error
	dirErr        error
	readDirCount  int
	readFileCount int
	onReadDir     func()
	onReadFile    func(rel string)
}

func (m *mockArchtestFS) ReadFile(rel string) ([]byte, error) {
	m.readFileCount++
	if m.onReadFile != nil {
		m.onReadFile(rel)
	}
	if m.readErr != nil {
		return nil, m.readErr
	}
	content, ok := m.files[filepath.ToSlash(rel)]
	if !ok {
		return nil, os.ErrNotExist
	}
	return content, nil
}

func (m *mockArchtestFS) WalkProductionGoFiles(fn func(rel string, src []byte) error) error {
	return nil
}

func (m *mockArchtestFS) WalkRootFiles(rootPath string, fn func(rel string, src []byte) error) error {
	return nil
}

func (m *mockArchtestFS) ReadDir(rel string) ([]os.DirEntry, error) {
	m.readDirCount++
	if m.onReadDir != nil {
		m.onReadDir()
	}
	if m.dirErr != nil {
		return nil, m.dirErr
	}
	return m.dirs[filepath.ToSlash(rel)], nil
}

func TestLoadTurnRecvASTFilesFromFS_Contract(t *testing.T) {
	t.Parallel()

	t.Run("filters_sorts_and_extracts_imports", func(t *testing.T) {
		t.Parallel()
		mockFS := &mockArchtestFS{
			dirs: map[string][]os.DirEntry{
				"internal/core/runtime": {
					gitDirEntry{name: "z_stream.go", isDir: false},
					gitDirEntry{name: "a_runtime.go", isDir: false},
					gitDirEntry{name: "ignored_test.go", isDir: false},
					gitDirEntry{name: "subpkg", isDir: true},
					gitDirEntry{name: "readme.txt", isDir: false},
				},
			},
			files: map[string][]byte{
				"internal/core/runtime/z_stream.go":  []byte("package runtime\n\nimport (\n\t\"context\"\n\trename \"net/http\"\n\t_ \"embed\"\n\t. \"fmt\"\n)\ntype Z struct{}\n"),
				"internal/core/runtime/a_runtime.go": []byte("package runtime\n\ntype A struct{}\n"),
			},
		}

		files, err := loadTurnRecvASTFilesFromFS(mockFS)
		if err != nil {
			t.Fatalf("loadTurnRecvASTFilesFromFS failed: %v", err)
		}
		if len(files) != 2 {
			t.Fatalf("expected 2 files, got %d", len(files))
		}
		if files[0].RelPath != "internal/core/runtime/a_runtime.go" {
			t.Errorf("files[0].RelPath = %q, want internal/core/runtime/a_runtime.go", files[0].RelPath)
		}
		if files[1].RelPath != "internal/core/runtime/z_stream.go" {
			t.Errorf("files[1].RelPath = %q, want internal/core/runtime/z_stream.go", files[1].RelPath)
		}
		if files[1].AST == nil || files[1].FSet == nil {
			t.Fatal("expected non-nil AST and FSet")
		}
		wantImports := map[string]string{
			"context": "context",
			"rename":  "net/http",
		}
		if !maps.Equal(files[1].Imports, wantImports) {
			t.Errorf("imports mismatch: got %v, want %v", files[1].Imports, wantImports)
		}
	})

	t.Run("read_dir_error", func(t *testing.T) {
		t.Parallel()
		errFS := &mockArchtestFS{
			dirErr: fmt.Errorf("simulated dir read error"),
		}
		if _, err := loadTurnRecvASTFilesFromFS(errFS); err == nil {
			t.Fatal("expected error on dir read failure, got nil")
		}
	})

	t.Run("read_file_error", func(t *testing.T) {
		t.Parallel()
		errReadFileFS := &mockArchtestFS{
			dirs: map[string][]os.DirEntry{
				"internal/core/runtime": {
					gitDirEntry{name: "file.go", isDir: false},
				},
			},
			readErr: fmt.Errorf("simulated file read error"),
		}
		if _, err := loadTurnRecvASTFilesFromFS(errReadFileFS); err == nil {
			t.Fatal("expected error on file read failure, got nil")
		}
	})

	t.Run("bad_syntax_error", func(t *testing.T) {
		t.Parallel()
		badSyntaxFS := &mockArchtestFS{
			dirs: map[string][]os.DirEntry{
				"internal/core/runtime": {
					gitDirEntry{name: "broken.go", isDir: false},
				},
			},
			files: map[string][]byte{
				"internal/core/runtime/broken.go": []byte("package runtime\n\ninvalid go code {{{"),
			},
		}
		if _, err := loadTurnRecvASTFilesFromFS(badSyntaxFS); err == nil {
			t.Fatal("expected parse error on broken syntax, got nil")
		}
	})

	t.Run("canceled_context_aborts_before_readdir", func(t *testing.T) {
		t.Parallel()
		trackingFS := &mockArchtestFS{
			dirs: map[string][]os.DirEntry{
				"internal/core/runtime": {
					gitDirEntry{name: "file.go", isDir: false},
				},
			},
			files: map[string][]byte{
				"internal/core/runtime/file.go": []byte("package runtime\n"),
			},
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := loadTurnRecvASTFilesFromFSContext(ctx, trackingFS)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
		if trackingFS.readDirCount != 0 {
			t.Errorf("readDirCount = %d, want 0", trackingFS.readDirCount)
		}
		if trackingFS.readFileCount != 0 {
			t.Errorf("readFileCount = %d, want 0", trackingFS.readFileCount)
		}
	})

	t.Run("canceled_context_aborts_after_readdir", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		trackingFS := &mockArchtestFS{
			dirs: map[string][]os.DirEntry{
				"internal/core/runtime": {
					gitDirEntry{name: "file.go", isDir: false},
				},
			},
			files: map[string][]byte{
				"internal/core/runtime/file.go": []byte("package runtime\n"),
			},
			onReadDir: func() {
				cancel()
			},
		}

		_, err := loadTurnRecvASTFilesFromFSContext(ctx, trackingFS)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
		if trackingFS.readDirCount != 1 {
			t.Errorf("readDirCount = %d, want 1", trackingFS.readDirCount)
		}
		if trackingFS.readFileCount != 0 {
			t.Errorf("readFileCount = %d, want 0", trackingFS.readFileCount)
		}
	})
}

func TestLoadTurnRecvASTFilesAtRef_Contract(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	// This contract is asserted against an isolated temporary Git fixture, never
	// against the live repository. Asserting HEAD/working-tree parity on the real
	// tree made an unrelated uncommitted production file (a dirty tree, before
	// its commit exists) fail a loader contract that has nothing to do with it.
	t.Run("committed_fixture_head_parity_and_dirty_tree_independence", func(t *testing.T) {
		t.Parallel()
		fixture := newGitFixture(t)

		// Committed runtime tree: two non-test sources with nontrivial import
		// aliases, plus entries the census must ignore (a _test.go file, a
		// non-Go file) and a subdirectory source proving the census stays
		// nonrecursive.
		fixture.write(t, "internal/core/runtime/a_first.go", `package runtime

import (
	"context"
	rename "net/http"
)

// First is committed fixture runtime source.
type First struct{}
`)
		fixture.write(t, "internal/core/runtime/z_last.go", `package runtime

import (
	. "fmt"
	_ "strings"
)

// Last is committed fixture runtime source with no collected imports.
type Last struct{}
`)
		fixture.write(t, "internal/core/runtime/zz_ignored_test.go", "package runtime\n")
		fixture.write(t, "internal/core/runtime/notes.txt", "ignored non-Go fixture entry\n")
		fixture.write(t, "internal/core/runtime/subpkg/nested.go", "package subpkg\n")
		fixture.git(t, "add",
			"internal/core/runtime/a_first.go",
			"internal/core/runtime/z_last.go",
			"internal/core/runtime/zz_ignored_test.go",
			"internal/core/runtime/notes.txt",
			"internal/core/runtime/subpkg/nested.go",
		)
		fixture.git(t, "commit", "-qm", "fixture: committed runtime tree")
		commitSHA := strings.TrimSpace(fixture.git(t, "rev-parse", "HEAD"))
		if commitSHA == "" {
			t.Fatal("fixture commit SHA is empty")
		}
		if status := strings.TrimSpace(fixture.git(t, "status", "--porcelain")); status != "" {
			t.Fatalf("fixture is not clean after commit: %s", status)
		}

		wantCommittedPaths := []string{
			"internal/core/runtime/a_first.go",
			"internal/core/runtime/z_last.go",
		}
		wantCommittedImports := map[string]map[string]string{
			"internal/core/runtime/a_first.go": {"context": "context", "rename": "net/http"},
			"internal/core/runtime/z_last.go":  {},
		}
		// Asserting each loader against the exact expected paths and imports is
		// strictly stronger than comparing the two loaders to each other, and it
		// also proves the ignored _test.go, non-Go, and subdirectory entries were
		// left out of both.
		relPathsOf := func(files []turnRecvASTFile) []string {
			paths := make([]string, 0, len(files))
			for _, file := range files {
				paths = append(paths, file.RelPath)
			}
			return paths
		}
		assertRuntimeTree := func(label string, files []turnRecvASTFile, wantPaths []string, wantImports map[string]map[string]string) {
			t.Helper()
			if len(files) == 0 {
				t.Fatalf("%s: expected non-empty runtime file census", label)
			}
			if gotPaths := relPathsOf(files); !slices.Equal(gotPaths, wantPaths) {
				t.Fatalf("%s paths = %v, want %v", label, gotPaths, wantPaths)
			}
			for _, file := range files {
				if !strings.HasPrefix(file.RelPath, "internal/core/runtime/") {
					t.Errorf("%s file %q does not have expected prefix", label, file.RelPath)
				}
				if !strings.HasSuffix(file.RelPath, ".go") || strings.HasSuffix(file.RelPath, "_test.go") {
					t.Errorf("%s file %q should be non-test go file", label, file.RelPath)
				}
				if file.AST == nil || file.FSet == nil {
					t.Errorf("%s file %q has nil AST or FSet", label, file.RelPath)
				}
				if file.Imports == nil {
					t.Errorf("%s file %q has nil Imports", label, file.RelPath)
				}
				want, ok := wantImports[file.RelPath]
				if !ok {
					t.Errorf("%s file %q has no expected import contract", label, file.RelPath)
					continue
				}
				if !maps.Equal(file.Imports, want) {
					t.Errorf("%s file %q imports = %v, want %v", label, file.RelPath, file.Imports, want)
				}
			}
		}

		headClean, err := loadTurnRecvASTFilesAtRefContext(t.Context(), fixture.root, "HEAD")
		if err != nil {
			t.Fatalf("loadTurnRecvASTFilesAtRefContext(fixture, HEAD) failed: %v", err)
		}
		assertRuntimeTree("HEAD(clean fixture)", headClean, wantCommittedPaths, wantCommittedImports)

		wtClean, err := loadTurnRecvASTFilesContext(t.Context(), fixture.root)
		if err != nil {
			t.Fatalf("loadTurnRecvASTFilesContext(fixture) failed: %v", err)
		}
		assertRuntimeTree("working tree(clean fixture)", wtClean, wantCommittedPaths, wantCommittedImports)

		// Dirty the fixture without committing: add a production source file,
		// delete a committed one, and change a committed import set, plus fresh
		// ignored entries.
		fixture.write(t, "internal/core/runtime/b_dirty.go", `package runtime

import (
	"errors"
	httpclient "net/http"
)

// Dirty is uncommitted fixture runtime source.
type Dirty struct{}
`)
		fixture.remove(t, "internal/core/runtime/z_last.go")
		fixture.write(t, "internal/core/runtime/a_first.go", `package runtime

import (
	"context"
	"errors"
	rename "net/http"
)

// First is the uncommitted variant of committed fixture runtime source.
type First struct{}
`)
		fixture.write(t, "internal/core/runtime/dirty_ignored_test.go", "package runtime\n")
		fixture.write(t, "internal/core/runtime/dirty_notes.txt", "ignored non-Go fixture entry\n")
		if status := strings.TrimSpace(fixture.git(t, "status", "--porcelain")); status == "" {
			t.Fatal("fixture is not dirty; reference/live independence assertions would be vacuous")
		}

		// Load the pinned commit by explicit SHA: a fresh git archive subprocess
		// over the dirty tree proves the reference view is the committed one, not
		// a cached result of the clean-phase call above.
		headDirty, err := loadTurnRecvASTFilesAtRefContext(t.Context(), fixture.root, commitSHA)
		if err != nil {
			t.Fatalf("loadTurnRecvASTFilesAtRefContext(fixture, %s) failed: %v", commitSHA, err)
		}
		assertRuntimeTree("HEAD(dirty fixture)", headDirty, wantCommittedPaths, wantCommittedImports)

		wantDirtyPaths := []string{
			"internal/core/runtime/a_first.go",
			"internal/core/runtime/b_dirty.go",
		}
		wantDirtyImports := map[string]map[string]string{
			"internal/core/runtime/a_first.go": {"context": "context", "errors": "errors", "rename": "net/http"},
			"internal/core/runtime/b_dirty.go": {"errors": "errors", "httpclient": "net/http"},
		}
		wtDirty, err := loadTurnRecvASTFilesContext(t.Context(), fixture.root)
		if err != nil {
			t.Fatalf("loadTurnRecvASTFilesContext(dirty fixture) failed: %v", err)
		}
		assertRuntimeTree("working tree(dirty fixture)", wtDirty, wantDirtyPaths, wantDirtyImports)

		// Explicit divergence evidence on top of the exact assertions: the
		// uncommitted file never reaches the reference view, the deleted file
		// stays there, and the edited import is visible only in the live view.
		headPaths := relPathsOf(headDirty)
		if slices.Contains(headPaths, "internal/core/runtime/b_dirty.go") {
			t.Errorf("HEAD view leaked uncommitted file: %v", headPaths)
		}
		if !slices.Contains(headPaths, "internal/core/runtime/z_last.go") {
			t.Errorf("HEAD view lost deleted committed file: %v", headPaths)
		}
		if _, ok := headDirty[0].Imports["errors"]; ok {
			t.Errorf("HEAD view leaked working-tree import edit: %v", headDirty[0].Imports)
		}
		wtPaths := relPathsOf(wtDirty)
		if slices.Contains(wtPaths, "internal/core/runtime/z_last.go") {
			t.Errorf("working tree view still reports deleted file: %v", wtPaths)
		}
		if _, ok := wtDirty[0].Imports["errors"]; !ok {
			t.Errorf("working tree view missed uncommitted import edit: %v", wtDirty[0].Imports)
		}
	})

	t.Run("invalid_ref_fails", func(t *testing.T) {
		t.Parallel()
		_, err := loadTurnRecvASTFilesAtRefContext(t.Context(), root, "invalid-ref-contract-test-nonexistent-404")
		if err == nil {
			t.Fatal("expected error for invalid git ref, got nil")
		}
	})

	t.Run("canceled_context_fails", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := loadTurnRecvASTFilesAtRefContext(ctx, root, "HEAD")
		if err == nil {
			t.Fatal("expected error on canceled context, got nil")
		}
		if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "canceled") {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	})
}

func TestLoadGitCommitFSContext_CancellationAndCacheRecovery(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	// Cancellation test: pre-canceled context must fail immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := loadGitCommitFSContext(ctx, root, "HEAD")
	if err == nil {
		t.Fatal("expected error with canceled context, got nil")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	// Cache recovery test: subsequent call with valid context must succeed (not poisoned)
	fs, err := loadGitCommitFSContext(t.Context(), root, "HEAD")
	if err != nil {
		t.Fatalf("subsequent loadGitCommitFSContext failed after cancellation: %v", err)
	}
	if len(fs.files) == 0 {
		t.Fatal("expected non-empty files in loaded gitCommitFS")
	}
}
