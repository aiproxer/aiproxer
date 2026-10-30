package interleavedthinking_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestInterleavedThinking_BoundaryIsolation(t *testing.T) {
	t.Parallel()
	// Find root of the interleavedthinking package.
	// Current test is in internal/plugins/features/interleavedthinking.
	root := "."
	forbiddenSubstrings := []string{
		"internal/core",
		"internal/infra/runtimebundle",
		"internal/standardplugins/featurehost",
	}

	var checkedFiles int
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		// Skip this boundary test itself so string literals aren't flagged
		if strings.HasSuffix(d.Name(), "boundary_test.go") {
			return nil
		}

		checkedFiles++
		node, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", path, err)
		}

		for _, imp := range node.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			for _, forbidden := range forbiddenSubstrings {
				if strings.Contains(importPath, forbidden) {
					t.Errorf("file %s imports forbidden package %q (matches %q)", path, importPath, forbidden)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk failed: %v", err)
	}
	if checkedFiles == 0 {
		t.Fatalf("no go files found to check in %s", root)
	}
	t.Logf("verified %d go files in interleavedthinking tree for boundary isolation", checkedFiles)
}
