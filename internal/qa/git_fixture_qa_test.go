package qa

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Workflow fragments are executed against Git fixtures by the scope tests.
type ciWorkflow struct {
	Jobs map[string]ciJobConfig `yaml:"jobs"`
}

type ciJobConfig struct {
	Steps []ciStepSpec `yaml:"steps"`
}

type ciStepSpec struct {
	ID  string `yaml:"id"`
	Run string `yaml:"run"`
}

// qaGitEnvPins are Git environment variables that bind a subprocess to a
// specific repository location, index, or object store. Classifier fixtures
// must not inherit them from the caller: a stray GIT_DIR or GIT_WORK_TREE lets
// a fixture commit, or the CI classifier that inspects the fixture, read and
// write the caller's repository instead of the fixture. This is the same
// contract the architecture fixtures enforce (internal/archtest).
var qaGitEnvPins = []string{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_CEILING_DIRECTORIES",
	"GIT_COMMON_DIR",
	"GIT_DIR",
	"GIT_DISCOVERY_ACROSS_FILESYSTEM",
	"GIT_GRAFT_FILE",
	"GIT_INDEX_FILE",
	"GIT_NAMESPACE",
	"GIT_OBJECT_DIRECTORY",
	"GIT_PREFIX",
	"GIT_SHALLOW_FILE",
	"GIT_WORK_TREE",
}

// qaGitMaintenancePins disable Git's detached background maintenance for every
// fixture repository.
//
// `git commit` runs `git maintenance run --auto` detached, so a maintenance
// child can outlive the fixture and keep writing .git/objects/pack while the
// testing package removes the fixture tree. That surfaces as a run failure that
// has nothing to do with the assertion under test:
//
//	--- FAIL: TestQAFastPreflight_MainPushUsesActualDiff
//	    testing.go:1464: TempDir RemoveAll cleanup: unlinkat \
//	      /tmp/TestQAFastPreflight_MainPushUsesActualDiff.../001/.git/objects/pack: \
//	      directory not empty
//
// The pins live in the fixture repository configuration rather than in a
// per-invocation `-c` flag so the classifier subprocesses, which run `git diff`
// inside the same fixture, inherit them too.
var qaGitMaintenancePins = []struct{ key, value string }{
	{"gc.auto", "0"},
	{"gc.autoDetach", "false"},
	{"maintenance.auto", "false"},
}

// qaGitEnv is the environment every fixture subprocess runs with:
// repository-local Git pins and Git configuration overrides are filtered out
// rather than overridden with empty values, because an empty GIT_DIR is still a
// pin while an absent one lets Git discover the fixture itself.
func qaGitEnv() []string {
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains(qaGitEnvPins, name) || strings.HasPrefix(name, "GIT_CONFIG") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// qaGitFixture is a throwaway Git repository that the main-push classifier
// fixtures classify real diffs against. Repository-local settings apply to the
// fixture only, so neither the caller worktree nor caller Git configuration can
// reach a fixture commit.
//
// Fixture Git work is deliberately serial: one fixture is mutated by successive
// commits, and callers that need independent repositories clone the immutable
// baseline instead of racing on it.
type qaGitFixture struct {
	root string
	env  []string
}

// newQAGitFixture initializes a fixture repository rooted in t.TempDir with
// background maintenance disabled and hooks isolated to an empty directory, so
// no global template or inherited hook can run during a fixture commit.
func newQAGitFixture(t *testing.T) qaGitFixture {
	t.Helper()
	fixture := qaGitFixture{root: t.TempDir(), env: qaGitEnv()}
	// The hooks directory lives outside the fixture tree so `git add .` cannot
	// commit it and change the diff the classifiers classify.
	hooks := t.TempDir()
	fixture.git(t, "init", "-q")
	fixture.git(t, "config", "core.hooksPath", hooks)
	for _, pin := range qaGitMaintenancePins {
		fixture.git(t, "config", pin.key, pin.value)
	}
	return fixture
}

// cloneInto copies an immutable fixture into a private directory. The copy
// carries the fixture repository configuration, so the maintenance pins and the
// isolated hooks path travel with every clone; a clone therefore needs no Git
// setup of its own.
func (f qaGitFixture) cloneInto(t *testing.T) qaGitFixture {
	t.Helper()
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(f.root)); err != nil {
		t.Fatalf("clone fixture: %v", err)
	}
	return qaGitFixture{root: root, env: f.env}
}

// git runs one joined Git subprocess against the fixture and returns stdout.
// Every invocation is waited for, so no Git child outlives the call.
func (f qaGitFixture) git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{
		"-C", f.root,
		"-c", "user.name=QA",
		"-c", "user.email=qa@example.com",
		"-c", "commit.gpgsign=false",
	}, args...)...)
	cmd.Env = f.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// write creates a fixture file, creating parent directories as needed.
func (f qaGitFixture) write(t *testing.T, name, text string) {
	t.Helper()
	path := filepath.Join(f.root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create dir for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("write fixture file %s: %v", name, err)
	}
}

// commandEnv returns the filtered fixture environment plus the classifier
// variables. Classifiers run `git diff` inside the fixture, so they must not
// receive the caller's repository pins either.
func (f qaGitFixture) commandEnv(extra ...string) []string {
	return append(slices.Clone(f.env), extra...)
}

// TestQAGitFixture_DisablesBackgroundMaintenance guards the fixture lifecycle
// contract. Dropping any pin lets a detached maintenance child write into the
// fixture after the test body returns, which fails the whole run during
// TempDir cleanup instead of at the assertion that covers the regression.
//
// The expected settings are restated here on purpose: asserting only against
// qaGitMaintenancePins would pass vacuously once that list is emptied.
func TestQAGitFixture_DisablesBackgroundMaintenance(t *testing.T) {
	t.Parallel()
	want := []struct{ key, value string }{
		{"gc.auto", "0"},
		{"gc.autoDetach", "false"},
		{"maintenance.auto", "false"},
	}
	if !slices.EqualFunc(qaGitMaintenancePins, want, func(got, expected struct{ key, value string }) bool {
		return got == expected
	}) {
		t.Fatalf("maintenance pins = %v, want %v", qaGitMaintenancePins, want)
	}
	fixture := newQAGitFixture(t)
	for _, expected := range want {
		if got := fixture.git(t, "config", "--get", expected.key); got != expected.value {
			t.Fatalf("fixture %s = %q, want %q", expected.key, got, expected.value)
		}
	}
}

// TestQAGitFixture_CloneInheritsRepositoryConfiguration covers the lane
// classifiers, which clone one immutable baseline instead of rebuilding a
// repository per lane. The clone must arrive with the maintenance pins and the
// isolated hooks path already in place, otherwise the clone reintroduces the
// background writer that made fixture cleanup fail.
func TestQAGitFixture_CloneInheritsRepositoryConfiguration(t *testing.T) {
	t.Parallel()
	baseline := newQAGitFixture(t)
	baseline.write(t, "docs/example.md", "base fixture\n")
	baseline.git(t, "add", ".")
	baseline.git(t, "commit", "-qm", "base")

	clone := baseline.cloneInto(t)
	clone.write(t, "docs/example.md", "clone fixture\n")
	clone.git(t, "commit", "-qam", "head")

	for _, pin := range []struct{ key, value string }{
		{"gc.auto", "0"},
		{"gc.autoDetach", "false"},
		{"maintenance.auto", "false"},
	} {
		if got := clone.git(t, "config", "--get", pin.key); got != pin.value {
			t.Fatalf("clone %s = %q, want %q", pin.key, got, pin.value)
		}
	}
	if got, baselineHooks := clone.git(t, "config", "--get", "core.hooksPath"), baseline.git(t, "config", "--get", "core.hooksPath"); got != baselineHooks {
		t.Fatalf("clone core.hooksPath = %q, want the baseline value %q", got, baselineHooks)
	}
	// A real diff must survive the copy; an empty clone would pass the
	// configuration assertions above while proving nothing.
	if got := clone.git(t, "rev-list", "--count", "HEAD"); got != "2" {
		t.Fatalf("clone commit count = %q, want 2", got)
	}
	if got := clone.git(t, "diff", "--name-only", "HEAD^", "HEAD"); got != "docs/example.md" {
		t.Fatalf("clone diff = %q, want docs/example.md", got)
	}
}

// TestQAGitFixture_IgnoresCallerRepositoryPins proves the caller's repository
// location cannot capture a fixture commit. Repository-local Git variables are
// filtered out of the fixture environment instead of being overridden with
// empty values, so pointing them at an unreachable location must not stop the
// fixture from committing into its own repository.
func TestQAGitFixture_IgnoresCallerRepositoryPins(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent-caller-repository")
	index := filepath.Join(absent, "caller-index")
	t.Setenv("GIT_DIR", filepath.Join(absent, ".git"))
	t.Setenv("GIT_WORK_TREE", absent)
	t.Setenv("GIT_INDEX_FILE", index)

	fixture := newQAGitFixture(t)
	fixture.write(t, "docs/example.md", "base fixture\n")
	fixture.git(t, "add", ".")
	fixture.git(t, "commit", "-qm", "base")

	if got := fixture.git(t, "rev-list", "--count", "HEAD"); got != "1" {
		t.Fatalf("fixture commit count = %q, want 1", got)
	}
	if got := fixture.git(t, "ls-files", "docs/example.md"); got != "docs/example.md" {
		t.Fatalf("fixture tracked files = %q, want docs/example.md", got)
	}
	if _, err := os.Stat(index); !os.IsNotExist(err) {
		t.Fatalf("caller index %s was written by a fixture commit (stat error %v)", index, err)
	}
}

// TestQAGitFixture_IgnoresCallerHookConfiguration proves a fixture commit cannot
// execute a hook inherited from the caller's Git configuration. Fixture commits
// are throwaway, so an inherited hook is both a correctness hazard and a source
// of background writers that outlive the fixture.
//
// The fixture pins core.hooksPath to its own empty directory, which outranks the
// caller's global setting. A positive control confirms the caller's hook really
// is live in this environment, so the fixture assertion is not vacuous.
func TestQAGitFixture_IgnoresCallerHookConfiguration(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hooks := t.TempDir()
	// Git config values are escape-parsed, so a Windows path needs forward
	// slashes; otherwise the fixture would fail on config syntax instead of on
	// the inherited hook this guard is about.
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\ntouch \""+filepath.ToSlash(marker)+"\"\n"), 0o700); err != nil { //nolint:gosec // the fixture hook must be executable.
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "caller-gitconfig")
	if err := os.WriteFile(config, []byte("[core]\n\thooksPath = "+filepath.ToSlash(hooks)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", config)

	fixture := newQAGitFixture(t)
	fixture.write(t, "docs/example.md", "base fixture\n")
	fixture.git(t, "add", ".")
	fixture.git(t, "commit", "-qm", "base")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("caller hook ran during a fixture commit (stat error %v)", err)
	}

	// Positive control: a repository that does not pin core.hooksPath runs the
	// caller's hook, which is what makes the fixture assertion above meaningful.
	control := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=QA", "-c", "user.email=qa@example.com", "commit", "-q", "--allow-empty", "-m", "base"},
	} {
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", control}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("control git %v: %v\n%s", args, err, out)
		}
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("caller hook never ran, so the fixture assertion proves nothing (stat error %v)", err)
	}
}
