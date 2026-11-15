package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateReportsOneGitlessExportError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	errs := validate(root)
	if len(errs) != 1 || !strings.Contains(errs[0], "export-ignore rules need a Git checkout") {
		t.Fatalf("validate(gitless export) = %v, want one Git metadata error", errs)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, finding := range validate(root) {
		if strings.Contains(finding, "export-ignore rules need a Git checkout") {
			t.Fatalf("checkout still reports the Git metadata error: %s", finding)
		}
	}
}
