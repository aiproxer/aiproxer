package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
)

func TestValidateDependabotIgnores(t *testing.T) {
	t.Parallel()
	const head = "version: 2\nupdates:\n  - package-ecosystem: gomod\n"
	cases := []struct {
		name, content string
		wantLines     []string
	}{
		{"linked", head + "    ignore:\n      # held: https://github.com/o/r/issues/1\n      - dependency-name: a\n        versions: [\">= 2\"]\n    groups: {}\n", nil},
		{"link above ignore key only", head + "    # https://github.com/o/r/issues/1\n    ignore:\n      - dependency-name: a\n", []string{":6:"}},
		{"second entry unlinked", head + "    ignore:\n      # https://github.com/o/r/issues/1\n      - dependency-name: a\n      # no issue\n      - dependency-name: b\n", []string{":8:"}},
		{"unlinked reordered keys", head + "    ignore:\n      - versions: [\">= 1\"]\n        dependency-name: a\n", []string{":5:"}},
		{"linked reordered keys", head + "    ignore:\n      # https://github.com/o/r/issues/2\n      - versions: [\">= 1\"]\n        dependency-name: a\n", nil},
		{"unlinked flow list", head + "    ignore: [{dependency-name: a}]\n", []string{":4:"}},
		{"malformed yaml", head + "    ignore: [\n", []string{"dependabot.yml:"}},
		{"outside ignore", head + "    allow:\n      - dependency-name: a\n", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, ".github"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".github", "dependabot.yml"), []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			errs := validateDependabotIgnores(root)
			if len(errs) != len(tc.wantLines) {
				t.Fatalf("findings %q, want %d", errs, len(tc.wantLines))
			}
			for i, want := range tc.wantLines {
				if !strings.Contains(errs[i], want) {
					t.Fatalf("finding %q does not name line %s", errs[i], want)
				}
			}
		})
	}
}

func TestDependabotOmissionOnlyAllowedForPublicExport(t *testing.T) {
	t.Parallel()
	for _, private := range []bool{false, true} {
		for _, ignored := range []bool{false, true} {
			t.Run(fmtCase(private, ignored), func(t *testing.T) {
				root := t.TempDir()
				cmd := exec.Command("git", "-C", root, "init", "-q", "--template="+t.TempDir())
				cmd.Env = gitscope.Environ()
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git init: %v: %s", err, out)
				}
				attrs := ""
				if ignored {
					attrs = ".github/dependabot.yml export-ignore\n"
				}
				if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte(attrs), 0o644); err != nil {
					t.Fatal(err)
				}
				if private {
					if err := os.Mkdir(filepath.Join(root, "enterprise"), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				tolerated := !private && ignored
				if findings := validateDependabotIgnores(root); (len(findings) == 0) != tolerated {
					t.Fatalf("findings %v, tolerated=%v", findings, tolerated)
				}
			})
		}
	}
}

func fmtCase(private, ignored bool) string {
	name := "public"
	if private {
		name = "private"
	}
	if ignored {
		return name + "/export-ignored"
	}
	return name + "/not-ignored"
}
