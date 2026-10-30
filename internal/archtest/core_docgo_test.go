package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCorePackagesHaveDocGo requires every internal/core/* directory that contains
// non-test .go files to also contain a doc.go with a package comment. This locks
// the core admission rule (arch review Phase 5 Task 5.3): new core packages must
// explain their boundary so the purpose is reviewable without reading all code.
// Directories with only subdirectories (no .go files at the top level) are skipped
// because they are parent directories, not Go packages.
func TestCorePackagesHaveDocGo(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	coreDir := filepath.Join(root, "internal", "core")
	entries, err := os.ReadDir(coreDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(coreDir, e.Name())
		hasNonTestGo := false
		subEntries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("failed to read %s: %v", dir, err)
		}
		for _, f := range subEntries {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".go") || strings.HasSuffix(f.Name(), "_test.go") {
				continue
			}
			hasNonTestGo = true
			break
		}
		if !hasNonTestGo {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			t.Parallel()
			docPath := filepath.Join(dir, "doc.go")
			if _, err := os.Stat(docPath); err != nil {
				t.Fatalf("internal/core/%s has non-test .go files but no doc.go", e.Name())
			}
		})
	}
}
