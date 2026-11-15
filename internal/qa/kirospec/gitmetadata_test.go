package kirospec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A public export without Git metadata cannot resolve export-ignore rules;
// private trees and real checkouts keep working.
func TestValidateGitMetadata(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		private bool
		git     string // "" or the .git entry kind: directory/file
		want    bool
	}{
		{"private tree without Git", true, "", false},
		{"public tree without Git", false, "", true},
		{"public checkout", false, "directory", false},
		{"public checkout worktree", false, "file", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if tc.private {
				if err := os.Mkdir(filepath.Join(root, "enterprise"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			switch tc.git {
			case "directory":
				if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: elsewhere\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := ValidateGitMetadata(root)
			if got := err != nil; got != tc.want {
				t.Fatalf("ValidateGitMetadata(%q) = %v, want error=%v", tc.name, err, tc.want)
			}
			if err != nil && !strings.Contains(err.Error(), "git init") {
				t.Fatalf("error %q does not name the fix", err)
			}
		})
	}
}
