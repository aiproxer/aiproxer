package dbparity_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
)

func TestDiscoverMigrations_HappyPath(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	files := []string{
		"20260101000000_baseline.go",
		"20260201120000_add_columns.go",
		"20260301093000_create_indexes.go",
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(tmpDir, f), []byte("package test\n"), 0o600); err != nil {
			t.Fatalf("WriteFile %s: %v", f, err)
		}
	}

	discovered, err := dbparity.DiscoverMigrations(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverMigrations unexpected error: %v", err)
	}

	if len(discovered) != 3 {
		t.Fatalf("len(discovered) = %d; want 3", len(discovered))
	}

	expected := []struct {
		id       string
		name     string
		filename string
	}{
		{"20260101000000", "baseline", "20260101000000_baseline.go"},
		{"20260201120000", "add_columns", "20260201120000_add_columns.go"},
		{"20260301093000", "create_indexes", "20260301093000_create_indexes.go"},
	}

	for i, exp := range expected {
		if discovered[i].ID != exp.id {
			t.Errorf("discovered[%d].ID = %q; want %q", i, discovered[i].ID, exp.id)
		}
		if discovered[i].Name != exp.name {
			t.Errorf("discovered[%d].Name = %q; want %q", i, discovered[i].Name, exp.name)
		}
		if discovered[i].Filename != exp.filename {
			t.Errorf("discovered[%d].Filename = %q; want %q", i, discovered[i].Filename, exp.filename)
		}
		if !strings.HasSuffix(filepath.ToSlash(discovered[i].Path), exp.filename) {
			t.Errorf("discovered[%d].Path = %q; want suffix %q", i, discovered[i].Path, exp.filename)
		}
	}
}

func TestDiscoverMigrations_Exclusions(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	files := []string{
		"20260101000000_valid_migration.go",
		"20260101000000_valid_migration_test.go", // excluded: _test.go
		"20260826_short_timestamp.go",            // excluded: 8 digits
		"202608261234567_long_timestamp.go",      // excluded: 15 digits
		"2026082612345a_alpha_timestamp.go",      // excluded: non-digit in timestamp
		"not_a_migration.go",                     // excluded: no timestamp prefix
		"20260101000000.go",                      // excluded: no underscore + name
		"README.md",                              // excluded: non-Go file
		".gitignore",                             // excluded: dotfile
	}

	for _, f := range files {
		if err := os.WriteFile(filepath.Join(tmpDir, f), []byte("package test\n"), 0o600); err != nil {
			t.Fatalf("WriteFile %s: %v", f, err)
		}
	}

	// Also create a subdirectory that should be ignored
	subDir := filepath.Join(tmpDir, "nested")
	if err := os.MkdirAll(subDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "20260901000000_nested.go"), []byte("package test\n"), 0o600); err != nil {
		t.Fatalf("WriteFile nested: %v", err)
	}

	discovered, err := dbparity.DiscoverMigrations(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverMigrations unexpected error: %v", err)
	}

	if len(discovered) != 1 {
		t.Fatalf("len(discovered) = %d; want 1 (discovered: %+v)", len(discovered), discovered)
	}
	if discovered[0].ID != "20260101000000" {
		t.Errorf("discovered[0].ID = %q; want 20260101000000", discovered[0].ID)
	}
	if discovered[0].Filename != "20260101000000_valid_migration.go" {
		t.Errorf("discovered[0].Filename = %q; want 20260101000000_valid_migration.go", discovered[0].Filename)
	}
}

func TestDiscoverMigrations_EmptyRoot(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	discovered, err := dbparity.DiscoverMigrations(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverMigrations on empty dir error: %v", err)
	}
	if len(discovered) != 0 {
		t.Fatalf("len(discovered) = %d; want 0", len(discovered))
	}
}

func TestDiscoverMigrations_BlankPath(t *testing.T) {
	t.Parallel()

	_, err := dbparity.DiscoverMigrations("")
	if err == nil {
		t.Fatal("expected error for empty root path")
	}

	_, err = dbparity.DiscoverMigrations("   ")
	if err == nil {
		t.Fatal("expected error for whitespace root path")
	}
}

func TestDiscoverMigrations_NonExistentPath(t *testing.T) {
	t.Parallel()

	_, err := dbparity.DiscoverMigrations(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected error for non-existent root path")
	}
}

func TestDiscoverMigrations_NotADirectory(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "file.txt")
	if err := os.WriteFile(filePath, []byte("content"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := dbparity.DiscoverMigrations(filePath)
	if err == nil {
		t.Fatal("expected error when root path is a file")
	}
}

func TestDiscoverMigrations_WhitespaceRoot(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	migFile := "20260101000000_baseline.go"
	filePath := filepath.Join(tmpDir, migFile)
	if err := os.WriteFile(filePath, []byte("package test\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Test with leading and trailing whitespace
	whitespacePath := "  \t " + tmpDir + " \n \t "
	discovered, err := dbparity.DiscoverMigrations(whitespacePath)
	if err != nil {
		t.Fatalf("DiscoverMigrations with whitespace path error: %v", err)
	}

	if len(discovered) != 1 {
		t.Fatalf("len(discovered) = %d; want 1", len(discovered))
	}

	expectedPath := filepath.ToSlash(filePath)
	if discovered[0].Path != expectedPath {
		t.Errorf("discovered[0].Path = %q; want %q", discovered[0].Path, expectedPath)
	}

	// Verify that the discovered path can be read directly
	content, err := os.ReadFile(discovered[0].Path)
	if err != nil {
		t.Fatalf("ReadFile from discovered path %q failed: %v", discovered[0].Path, err)
	}
	if string(content) != "package test\n" {
		t.Errorf("content = %q; want %q", string(content), "package test\n")
	}
}

func TestDiscoverMigrations_DuplicateTimestampError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "20260101000000_first.go"), []byte("package test\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "20260101000000_second.go"), []byte("package test\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := dbparity.DiscoverMigrations(tmpDir)
	if err == nil {
		t.Fatal("expected error on duplicate migration timestamp ID, got nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "20260101000000") {
		t.Errorf("error %q should contain duplicate ID 20260101000000", errMsg)
	}
	if !strings.Contains(errMsg, "20260101000000_first.go") {
		t.Errorf("error %q should contain first filename 20260101000000_first.go", errMsg)
	}
	if !strings.Contains(errMsg, "20260101000000_second.go") {
		t.Errorf("error %q should contain second filename 20260101000000_second.go", errMsg)
	}
}

func TestDiscoverMigrations_OutOfOrderSorting(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	files := []string{
		"20260901000000_late.go",
		"20260101000000_early.go",
		"20260501000000_middle.go",
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(tmpDir, f), []byte("package test\n"), 0o600); err != nil {
			t.Fatalf("WriteFile %s: %v", f, err)
		}
	}

	discovered, err := dbparity.DiscoverMigrations(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverMigrations unexpected error: %v", err)
	}

	if len(discovered) != 3 {
		t.Fatalf("len(discovered) = %d; want 3", len(discovered))
	}

	expectedIDs := []string{"20260101000000", "20260501000000", "20260901000000"}
	for i, expID := range expectedIDs {
		if discovered[i].ID != expID {
			t.Errorf("discovered[%d].ID = %q; want %q", i, discovered[i].ID, expID)
		}
	}
}

func TestDiscoverMigrationIDs(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	files := []string{
		"20260301000000_gamma.go",
		"20260101000000_alpha.go",
		"20260201000000_beta.go",
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(tmpDir, f), []byte("package test\n"), 0o600); err != nil {
			t.Fatalf("WriteFile %s: %v", f, err)
		}
	}

	ids, err := dbparity.DiscoverMigrationIDs(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverMigrationIDs error: %v", err)
	}

	expected := []string{"20260101000000", "20260201000000", "20260301000000"}
	if len(ids) != len(expected) {
		t.Fatalf("len(ids) = %d; want %d", len(ids), len(expected))
	}
	for i, exp := range expected {
		if ids[i] != exp {
			t.Errorf("ids[%d] = %q; want %q", i, ids[i], exp)
		}
	}
}

func TestDiscoverComponentMigrations(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	dirA := filepath.Join(repoRoot, "internal", "storeA")
	dirB := filepath.Join(repoRoot, "internal", "storeB")
	if err := os.MkdirAll(dirA, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.MkdirAll(dirB, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dirA, "20260101000000_a.go"), []byte("package test\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "20260201000000_b.go"), []byte("package test\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	comp := dbparity.Component{
		ID: "multi-root-comp",
		MigrationRoots: []string{
			"internal/storeA",
			"internal/storeB",
		},
	}

	discovered, err := dbparity.DiscoverComponentMigrations(repoRoot, comp)
	if err != nil {
		t.Fatalf("DiscoverComponentMigrations error: %v", err)
	}
	if len(discovered) != 2 {
		t.Fatalf("len(discovered) = %d; want 2", len(discovered))
	}
	if discovered[0].ID != "20260101000000" || discovered[1].ID != "20260201000000" {
		t.Errorf("unexpected discovered IDs: %+v", discovered)
	}
	if discovered[0].Path != "internal/storeA/20260101000000_a.go" {
		t.Errorf("discovered[0].Path = %q; want %q", discovered[0].Path, "internal/storeA/20260101000000_a.go")
	}
	if discovered[1].Path != "internal/storeB/20260201000000_b.go" {
		t.Errorf("discovered[1].Path = %q; want %q", discovered[1].Path, "internal/storeB/20260201000000_b.go")
	}

	// Test duplicate ID across multiple roots returns error
	if err := os.WriteFile(filepath.Join(dirB, "20260101000000_duplicate.go"), []byte("package test\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err = dbparity.DiscoverComponentMigrations(repoRoot, comp)
	if err == nil {
		t.Fatal("expected error on duplicate migration ID across roots, got nil")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "multi-root-comp") {
		t.Errorf("error %q should contain component ID multi-root-comp", errMsg)
	}
	if !strings.Contains(errMsg, "20260101000000") {
		t.Errorf("error %q should contain duplicate ID 20260101000000", errMsg)
	}
	if !strings.Contains(errMsg, "internal/storeA/20260101000000_a.go") {
		t.Errorf("error %q should contain first path internal/storeA/20260101000000_a.go", errMsg)
	}
	if !strings.Contains(errMsg, "internal/storeB/20260101000000_duplicate.go") {
		t.Errorf("error %q should contain second path internal/storeB/20260101000000_duplicate.go", errMsg)
	}
}

func TestDiscoverComponentMigrations_WhitespaceRepoRoot(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	dirA := filepath.Join(repoRoot, "internal", "storeA")
	if err := os.MkdirAll(dirA, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirA, "20260101000000_a.go"), []byte("package test\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	comp := dbparity.Component{
		ID: "ws-comp",
		MigrationRoots: []string{
			"internal/storeA",
		},
	}

	whitespaceRoot := "  \t " + repoRoot + " \n "
	discovered, err := dbparity.DiscoverComponentMigrations(whitespaceRoot, comp)
	if err != nil {
		t.Fatalf("DiscoverComponentMigrations with whitespace repoRoot error: %v", err)
	}
	if len(discovered) != 1 {
		t.Fatalf("len(discovered) = %d; want 1", len(discovered))
	}
	if discovered[0].Path != "internal/storeA/20260101000000_a.go" {
		t.Errorf("discovered[0].Path = %q; want %q", discovered[0].Path, "internal/storeA/20260101000000_a.go")
	}
}

func TestAssertMigrationHistoryIDs(t *testing.T) {
	t.Parallel()

	discovered := []string{
		"20260101000000",
		"20260201000000",
		"20260301000000",
	}

	t.Run("all applied - success", func(t *testing.T) {
		t.Parallel()
		recorded := map[string]bool{
			"20260101000000": true,
			"20260201000000": true,
			"20260301000000": true,
			"20260401000000": true, // extra is okay
		}
		if err := dbparity.AssertMigrationHistoryIDs(discovered, recorded); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("missing single migration - error", func(t *testing.T) {
		t.Parallel()
		recorded := map[string]bool{
			"20260101000000": true,
			"20260301000000": true,
		}
		err := dbparity.AssertMigrationHistoryIDs(discovered, recorded)
		if err == nil {
			t.Fatal("expected error for missing migration")
		}
		if !strings.Contains(err.Error(), "20260201000000") {
			t.Errorf("error %q should mention missing ID 20260201000000", err.Error())
		}
	})

	t.Run("missing multiple migrations - sorted error", func(t *testing.T) {
		t.Parallel()
		recorded := map[string]bool{
			"20260201000000": true,
		}
		err := dbparity.AssertMigrationHistoryIDs(discovered, recorded)
		if err == nil {
			t.Fatal("expected error for missing migrations")
		}
		if !strings.Contains(err.Error(), "20260101000000") || !strings.Contains(err.Error(), "20260301000000") {
			t.Errorf("error %q should mention missing IDs", err.Error())
		}
	})

	t.Run("empty discovered - success", func(t *testing.T) {
		t.Parallel()
		if err := dbparity.AssertMigrationHistoryIDs(nil, map[string]bool{}); err != nil {
			t.Fatalf("unexpected error on nil discovered: %v", err)
		}
	})
}

func TestAssertMigrationHistory(t *testing.T) {
	t.Parallel()

	discovered := []string{"20260101000000", "20260201000000"}

	t.Run("all applied slice - success", func(t *testing.T) {
		t.Parallel()
		applied := []string{"20260101000000", "20260201000000"}
		if err := dbparity.AssertMigrationHistory(discovered, applied); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("missing applied slice - error", func(t *testing.T) {
		t.Parallel()
		applied := []string{"20260101000000"}
		err := dbparity.AssertMigrationHistory(discovered, applied)
		if err == nil {
			t.Fatal("expected error when applied history missing ID")
		}
		if !strings.Contains(err.Error(), "20260201000000") {
			t.Errorf("error %q should mention 20260201000000", err.Error())
		}
	})
}

func TestAssertMigrationFilesApplied(t *testing.T) {
	t.Parallel()

	files := []dbparity.MigrationFile{
		{ID: "20260101000000", Filename: "20260101000000_a.go"},
		{ID: "20260201000000", Filename: "20260201000000_b.go"},
	}

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		recorded := map[string]bool{
			"20260101000000": true,
			"20260201000000": true,
		}
		if err := dbparity.AssertMigrationFilesApplied(files, recorded); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("missing error", func(t *testing.T) {
		t.Parallel()
		recorded := map[string]bool{
			"20260101000000": true,
		}
		err := dbparity.AssertMigrationFilesApplied(files, recorded)
		if err == nil {
			t.Fatal("expected error for missing migration file in applied history")
		}
		if !strings.Contains(err.Error(), "20260201000000") {
			t.Errorf("error %q should mention missing 20260201000000", err.Error())
		}
	})
}
