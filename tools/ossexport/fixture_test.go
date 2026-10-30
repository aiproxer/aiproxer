package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fixture is a synthetic Git repository that stands in for the AIProxer tree:
// an open-source Go module, an Enterprise tree, and export-ignore rules that
// keep both out of the published archive.
type fixture struct {
	t     *testing.T
	root  string
	files map[string]string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, root: t.TempDir(), files: map[string]string{}}
	f.git("init", "-q")
	f.git("config", "user.email", "fixture@example.invalid")
	f.git("config", "user.name", "Fixture")
	f.git("config", "commit.gpgsign", "false")
	f.file("go.mod", "module example.com/fixture\n\ngo 1.26\n")
	f.file("main.go", "package main\n\nfunc main() {}\n")
	f.file("main_test.go", "package main\n\nimport \"testing\"\n\nfunc TestFixtureMain(t *testing.T) {}\n")
	f.file(".gitattributes", "/enterprise/** export-ignore\n/docs/secret.md export-ignore\n")
	f.file("enterprise/internal/secret/secret.go",
		"package secret\n\n// Value reports an Enterprise-only value.\nfunc Value() int { return 1 }\n")
	f.file("docs/secret.md", "# Secret\n\nEnterprise-only documentation.\n")
	f.file("docs/guide.md", "# Guide\n\nPublic documentation.\n")
	f.file("docs/readme.md", "# Fixture\n\nSee the [guide](guide.md).\n")
	return f
}

// file stages a file for the next commit.
func (f *fixture) file(rel, content string) {
	f.t.Helper()
	f.files[rel] = content
}

// git runs a Git command inside the fixture repository.
func (f *fixture) git(args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = f.root
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes every staged file, commits them, and returns the commit id.
func (f *fixture) commit(message string) string {
	f.t.Helper()
	for rel, content := range f.files {
		path := filepath.Join(f.root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			f.t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			f.t.Fatalf("write %s: %v", rel, err)
		}
	}
	f.files = map[string]string{}
	f.git("add", "-A")
	f.git("commit", "-q", "-m", message)
	return f.git("rev-parse", "HEAD")
}

// export runs the tool against the fixture and returns the result.
func (f *fixture) export(rev, out string) (result, error) {
	f.t.Helper()
	return export(f.t.Context(), options{repo: f.root, rev: rev, out: out})
}

// archiveFiles returns the sorted file names inside a tar archive.
func archiveFiles(t *testing.T, archive string) []string {
	t.Helper()
	names := readArchiveNames(t, archive)
	return names
}
