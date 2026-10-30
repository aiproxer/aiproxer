package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Claim: release-declared executable companions are copied beside the binary,
// and neither unsafe paths nor links can introduce undeclared runtime bytes.
// Source: task 3.3 deterministic sidecar deployment. Mutant: omit copy or containment.
func TestExecutableCompanions_AdjacentAndContained(t *testing.T) {
	t.Parallel()
	root, bin := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "extension"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "extension", "bootstrap.mjs"), []byte("project-authored"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, source, target string
		fail                 bool
	}{
		{"adjacent", "extension/bootstrap.mjs", "adapter-extension/bootstrap.mjs", false},
		{"source escape", "../outside", "adapter-extension/bootstrap.mjs", true},
		{"target escape", "extension/bootstrap.mjs", "../bootstrap.mjs", true},
		{"missing", "extension/missing.mjs", "adapter-extension/missing.mjs", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dest := filepath.Join(bin, tc.name)
			err := copyExecutableCompanions(root, dest, map[string]string{tc.source: tc.target})
			if (err != nil) != tc.fail {
				t.Fatalf("copy error = %v, want failure %v", err, tc.fail)
			}
			if !tc.fail {
				data, err := os.ReadFile(filepath.Join(dest, tc.target))
				if err != nil || string(data) != "project-authored" {
					t.Fatalf("deployed companion = %q, %v", data, err)
				}
			}
		})
	}
	if err := os.Symlink(filepath.Join(root, "extension", "bootstrap.mjs"), filepath.Join(root, "linked.mjs")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := copyExecutableCompanions(root, bin, map[string]string{"linked.mjs": "linked.mjs"}); err == nil {
		t.Fatal("symlink companion accepted")
	}
}
