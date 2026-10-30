package archtest

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoLocalGitEnvPins are Git environment variables that bind a subprocess to a
// specific repository location, index, or object store. Test Git fixtures must
// not inherit them from the caller: a stray GIT_INDEX_FILE or GIT_WORK_TREE lets
// a fixture commit, or a hook it triggers, mutate the caller's index.
var repoLocalGitEnvPins = map[string]bool{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	"GIT_CEILING_DIRECTORIES":          true,
	"GIT_COMMON_DIR":                   true,
	"GIT_DIR":                          true,
	"GIT_DISCOVERY_ACROSS_FILESYSTEM":  true,
	"GIT_GRAFT_FILE":                   true,
	"GIT_INDEX_FILE":                   true,
	"GIT_NAMESPACE":                    true,
	"GIT_OBJECT_DIRECTORY":             true,
	"GIT_PREFIX":                       true,
	"GIT_SHALLOW_FILE":                 true,
	"GIT_WORK_TREE":                    true,
}

// gitFixture is a throwaway Git repository rooted in t.TempDir. Architecture
// contract tests use it instead of the live repository so their assertions never
// depend on uncommitted or unrelated work in the caller's worktree.
type gitFixture struct {
	root string
	env  []string
}

// newGitFixture initializes an isolated repository. Repository-local Git
// settings apply to the fixture only: fixture identity, signing disabled, and an
// empty hooks directory, so no global template, inherited hooks, or caller
// repository setting can reach a fixture commit.
func newGitFixture(t *testing.T) gitFixture {
	t.Helper()
	root := t.TempDir()
	hooksDir := filepath.Join(root, "fixture-empty-hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("create fixture hooks dir: %v", err)
	}

	// Filter repository-local Git environment pins out of the subprocess
	// environment instead of overriding them with empty values: an empty GIT_DIR
	// is still a pin, while an absent one lets git discover the fixture itself.
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if repoLocalGitEnvPins[name] || strings.HasPrefix(name, "GIT_CONFIG") {
			continue
		}
		env = append(env, kv)
	}

	fixture := gitFixture{root: root, env: env}
	fixture.git(t, "init", "-q")
	fixture.git(t, "config", "user.email", "archtest-fixture@example.invalid")
	fixture.git(t, "config", "user.name", "archtest fixture")
	fixture.git(t, "config", "commit.gpgsign", "false")
	fixture.git(t, "config", "core.hooksPath", hooksDir)
	return fixture
}

// git runs one Git subprocess against the fixture and returns its stdout.
func (f gitFixture) git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", f.root}, args...)...)
	cmd.Env = f.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v failed: %v\nstdout: %s\nstderr: %s", args, err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// write creates a fixture file, creating parent directories as needed.
func (f gitFixture) write(t *testing.T, rel string, content string) {
	t.Helper()
	abs := filepath.Join(f.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("create dir for %s: %v", rel, err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture file %s: %v", rel, err)
	}
}

// remove deletes a fixture file from the fixture working tree.
func (f gitFixture) remove(t *testing.T, rel string) {
	t.Helper()
	if err := os.Remove(filepath.Join(f.root, filepath.FromSlash(rel))); err != nil {
		t.Fatalf("remove fixture file %s: %v", rel, err)
	}
}
