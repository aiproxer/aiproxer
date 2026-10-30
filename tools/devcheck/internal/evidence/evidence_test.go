package evidence

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
)

// Claim: a manifest for a clean revision names the exact commit it ran against
// and reports no dirty paths. Without this, a reviewer cannot tie a result to a
// revision at all.
func TestManifestRecordsCleanRevision(t *testing.T) {
	root := repoFixture(t)

	manifest := New(t.Context(), Input{Task: "test", Scope: Scope{Kind: "explicit", Module: "."}, Workdir: root})
	manifest.Finish(nil, "")

	want := fixtureGit(t, root, "rev-parse", "HEAD")
	if manifest.Revision.Head != want {
		t.Errorf("head = %q, want %q", manifest.Revision.Head, want)
	}
	if manifest.Revision.Dirty {
		t.Errorf("clean fixture reported dirty: %+v", manifest.Revision)
	}
	if manifest.Revision.DirtyDigest == "" {
		t.Error("dirty digest must be recorded even for a clean tree; it identifies the observed porcelain payload")
	}
	if manifest.Revision.Branch != "master" && manifest.Revision.Branch != "main" {
		t.Errorf("branch = %q, want the fixture default branch", manifest.Revision.Branch)
	}
	if manifest.Outcome != OutcomePassed {
		t.Errorf("outcome = %q, want %q", manifest.Outcome, OutcomePassed)
	}
}

// Claim: the dirty digest identifies the tree content that was tested, not the
// path list alone. Two different untracked payloads at the same path must not
// share a digest, and an identical payload must be reproducible.
func TestManifestDirtyDigestIdentifiesPayload(t *testing.T) {
	root := repoFixture(t)
	write(t, root, "notes.txt", "first")
	first := New(t.Context(), Input{Task: "test", Workdir: root}).Revision

	write(t, root, "notes.txt", "second")
	second := New(t.Context(), Input{Task: "test", Workdir: root}).Revision
	repeated := New(t.Context(), Input{Task: "test", Workdir: root}).Revision

	if first.DirtyDigest == second.DirtyDigest {
		t.Errorf("distinct payloads share digest %q", first.DirtyDigest)
	}
	if second.DirtyDigest != repeated.DirtyDigest {
		t.Errorf("identical payload produced digests %q and %q", second.DirtyDigest, repeated.DirtyDigest)
	}
	if !second.Dirty || second.DirtyPathCount != 1 {
		t.Errorf("one untracked file reported as dirty=%v count=%d", second.Dirty, second.DirtyPathCount)
	}
}

// Claim: the dirty Go file list is reported separately, so a reviewer can apply
// the repository's change-size budget to the same set the check saw.
func TestManifestReportsDirtyGoFiles(t *testing.T) {
	root := repoFixture(t)
	write(t, root, "dirty.go", "package dirty\n")
	write(t, root, "dirty_test.go", "package dirty\n")
	write(t, root, "notes.md", "notes\n")

	got := New(t.Context(), Input{Task: "test", Workdir: root}).Revision.DirtyGoFiles
	want := []string{"dirty.go", "dirty_test.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("dirty go files = %v, want %v", got, want)
	}
}

// Claim: a failed command makes the run a failure, and its test counters are
// preserved instead of being dropped with the failed step.
func TestManifestTotalsIncludeFailedStepCounters(t *testing.T) {
	manifest := New(t.Context(), Input{Task: "test", Workdir: t.TempDir()})
	manifest.Steps = append(manifest.Steps,
		Step{Label: "ok", Result: OutcomePassed, Tests: &Stats{Passed: 7, Cached: 2}},
		Step{Label: "bad", Result: OutcomeFailed, ExitCode: 2, Tests: &Stats{Passed: 1, Failed: 1, Skipped: 3}},
	)
	manifest.Finish(assertionError("command failed"), "")

	if manifest.Outcome != OutcomeFailed {
		t.Errorf("outcome = %q, want %q", manifest.Outcome, OutcomeFailed)
	}
	if !strings.Contains(manifest.FailureReason, "command failed") {
		t.Errorf("failure reason = %q, want the returned error", manifest.FailureReason)
	}
	want := Totals{
		CommandsRecorded:  2,
		CommandsCompleted: 1,
		CommandsFailed:    1,
		TestsPassed:       8,
		TestsCached:       2,
		TestsFailed:       1,
		TestsSkipped:      3,
	}
	got := manifest.Totals
	got.DurationMS = want.DurationMS
	if got != want {
		t.Errorf("totals = %+v, want %+v", got, want)
	}
}

// Claim: an infrastructure condition is reported as blocked, not failed. A
// missing toolchain says nothing about the code, and a reader who sees `failed`
// goes looking for a defect that is not there.
func TestManifestBlockedOutranksReturnedError(t *testing.T) {
	manifest := New(t.Context(), Input{Task: "lint", Workdir: t.TempDir()})
	manifest.Finish(assertionError("go: some unrelated failure"), "required tool golangci-lint is unavailable")

	if manifest.Outcome != OutcomeBlocked {
		t.Errorf("outcome = %q, want %q", manifest.Outcome, OutcomeBlocked)
	}
	if !strings.Contains(manifest.FailureReason, "golangci-lint") {
		t.Errorf("failure reason = %q, want the blocking condition", manifest.FailureReason)
	}
}

// Claim: a run that stops after its first step records only what it actually
// ran and is reported as failed. A manifest must never present a partial run as
// a complete, passing one.
func TestManifestRecordsInterruptedRun(t *testing.T) {
	manifest := New(t.Context(), Input{Task: "test", Workdir: t.TempDir()})
	manifest.Steps = append(manifest.Steps, Step{Label: "one", Result: OutcomePassed})
	manifest.Finish(assertionError("stopped before the second module"), "")

	if manifest.Outcome != OutcomeFailed {
		t.Errorf("outcome = %q, want %q", manifest.Outcome, OutcomeFailed)
	}
	if len(manifest.Steps) != 1 || manifest.Totals.CommandsRecorded != 1 {
		t.Errorf("steps = %+v, want only the executed step", manifest.Steps)
	}
	if !strings.Contains(manifest.FailureReason, "second module") {
		t.Errorf("failure reason = %q, want the returned error", manifest.FailureReason)
	}
}

// Claim: a manifest over more dirty files than the content-hashing bound still
// reports that it truncated, so two large dirty trees cannot be mistaken for
// each other on a prefix digest.
func TestManifestReportsTruncatedDirtyDigest(t *testing.T) {
	root := repoFixture(t)
	for i := range maxDirtyContentFiles + 5 {
		write(t, root, filepath.ToSlash(filepath.Join("bulk", string(rune('a'+i%26))+itoa(i)+".txt")), "body\n")
	}

	got := New(t.Context(), Input{Task: "test", Workdir: root}).Revision
	if !got.DirtyTruncated {
		t.Errorf("%d dirty files reported without truncation", got.DirtyPathCount)
	}
	if got.DirtyDigest == "" {
		t.Error("truncated manifest must still carry a digest")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}

// Claim: the written manifest is valid JSON that a consumer can read without
// guessing field names, and it is published atomically into a directory that
// does not exist yet.
func TestWriteProducesReadableManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "verification.json")
	manifest := New(t.Context(), Input{
		Task:    "test",
		Scope:   Scope{Kind: "explicit", Module: ".", Packages: []string{"./internal/qa"}},
		Workdir: t.TempDir(),
	})
	manifest.Steps = append(manifest.Steps, Step{Label: "go test", Result: OutcomePassed})
	manifest.Finish(nil, "")

	if err := manifest.Write(path); err != nil {
		t.Fatalf("Write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var decoded struct {
		SchemaVersion int `json:"schema_version"`
		Tool          string
		Scope         struct {
			Packages []string
		}
		Revision struct {
			Worktree string
		}
		Steps []struct {
			Result string
		}
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode manifest: %v\n%s", err, data)
	}
	if decoded.SchemaVersion != SchemaVersion || decoded.Tool != Tool {
		t.Errorf("schema_version=%d tool=%q, want %d and %q", decoded.SchemaVersion, decoded.Tool, SchemaVersion, Tool)
	}
	if strings.Join(decoded.Scope.Packages, ",") != "./internal/qa" {
		t.Errorf("scope packages = %v", decoded.Scope.Packages)
	}
	if decoded.Revision.Worktree == "" || len(decoded.Steps) != 1 || decoded.Steps[0].Result != OutcomePassed {
		t.Errorf("decoded = %+v", decoded)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want only the published manifest: %v", len(entries), entries)
	}
}

// Claim: git is probed with the repository-location variables cleared, so a
// manifest produced from a hook reports the requested worktree rather than the
// ambient repository.
func TestRevisionUsesRequestedWorktree(t *testing.T) {
	root := repoFixture(t)
	t.Setenv("GIT_DIR", filepath.Join(root, ".git"))

	got := New(t.Context(), Input{Task: "test", Workdir: root}).Revision
	if got.Head != fixtureGit(t, root, "rev-parse", "HEAD") {
		t.Errorf("head = %q, want the fixture commit", got.Head)
	}
	if got.Worktree != root {
		t.Errorf("worktree = %q, want %q", got.Worktree, root)
	}
}

func repoFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/evidence\n\ngo 1.26.0\n")
	write(t, root, "pkg/base.go", "package pkg\n")
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "."},
		{"-c", "user.name=Evidence Test", "-c", "user.email=evidence@example.invalid", "commit", "-qm", "fixture"},
	} {
		fixtureGit(t, root, args...)
	}
	return root
}

func fixtureGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = gitscope.Environ()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

type assertionError string

func (e assertionError) Error() string { return string(e) }
