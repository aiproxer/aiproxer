package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
	"github.com/aiproxer/aiproxer/tools/devcheck/internal/evidence"
)

// Claim: a recorded step's log file holds the command's own output. A manifest
// that names a log nobody wrote is worse than no log, because it invites the
// reader to trust evidence that does not exist.
func TestRecorderTeesCommandOutputToLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	recorder := newEvidenceRecorder(t.Context(), path, "build", evidence.Scope{Kind: "explicit", Module: "."}, t.TempDir())
	var streamed strings.Builder

	step := recorder.start("module-a", []string{"sh", "-c", "printf streamed"}, t.TempDir(), &streamed)
	cmd := exec.Command("sh", "-c", "printf streamed")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("probe command: %v", err)
	}
	if _, err := step.output.Write(output); err != nil {
		t.Fatalf("write to step output: %v", err)
	}
	recorder.finishRun(step, nil, nil)
	if err := recorder.finish(nil); err != nil {
		t.Fatalf("finish: %v", err)
	}

	if streamed.String() != "streamed" {
		t.Errorf("caller stream = %q, want the command output to pass through unchanged", streamed.String())
	}
	manifest := readManifest(t, path)
	if len(manifest.Steps) != 1 {
		t.Fatalf("steps = %+v, want one recorded step", manifest.Steps)
	}
	logged, err := os.ReadFile(manifest.Steps[0].LogPath)
	if err != nil {
		t.Fatalf("recorded log %q: %v", manifest.Steps[0].LogPath, err)
	}
	if string(logged) != "streamed" {
		t.Errorf("log = %q, want the command output", logged)
	}
	if manifest.Steps[0].Result != evidence.OutcomePassed || manifest.Steps[0].ExitCode != 0 {
		t.Errorf("step = %+v, want a passed step with exit code 0", manifest.Steps[0])
	}
}

// Claim: a failing command records a non-zero exit code and makes the manifest
// a failure, so a red run cannot be filed as evidence of anything.
func TestRecorderRecordsFailedCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	recorder := newEvidenceRecorder(t.Context(), path, "build", evidence.Scope{Kind: "explicit", Module: "."}, t.TempDir())

	step := recorder.start("go-build", []string{"sh", "-c", "exit 3"}, t.TempDir(), os.Stdout)
	runErr := exec.Command("sh", "-c", "exit 3").Run()
	recorder.finishRun(step, &evidence.Stats{Passed: 4, Failed: 2}, runErr)
	if err := recorder.finish(runErr); err != nil {
		t.Fatalf("finish: %v", err)
	}

	manifest := readManifest(t, path)
	if manifest.Outcome != evidence.OutcomeFailed {
		t.Errorf("outcome = %q, want %q", manifest.Outcome, evidence.OutcomeFailed)
	}
	step0 := manifest.Steps[0]
	if step0.Result != evidence.OutcomeFailed || step0.ExitCode != 3 {
		t.Errorf("step = %+v, want a failed step with exit code 3", step0)
	}
	if manifest.Totals.TestsPassed != 4 || manifest.Totals.TestsFailed != 2 {
		t.Errorf("totals = %+v, want the failed step's counters preserved", manifest.Totals)
	}
}

// Claim: a blocked condition is reported as blocked even when a command also
// failed. The blocker is the actionable fact; the command error is downstream.
func TestRecorderBlockOutranksCommandFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	recorder := newEvidenceRecorder(t.Context(), path, "lint", evidence.Scope{Kind: "explicit", Module: "."}, t.TempDir())
	recorder.block("required tool golangci-lint is unavailable")
	recorder.block("a later condition that adds nothing")

	runErr := exec.Command("sh", "-c", "exit 1").Run()
	if err := recorder.finish(runErr); err != nil {
		t.Fatalf("finish: %v", err)
	}

	manifest := readManifest(t, path)
	if manifest.Outcome != evidence.OutcomeBlocked {
		t.Errorf("outcome = %q, want %q", manifest.Outcome, evidence.OutcomeBlocked)
	}
	if !strings.Contains(manifest.FailureReason, "golangci-lint") {
		t.Errorf("failure reason = %q, want the first blocking condition", manifest.FailureReason)
	}
}

// Claim: without -evidence the recorder is absent and command output is
// untouched. Recording must be strictly opt-in.
func TestRecorderAbsentWithoutPath(t *testing.T) {
	recorder := newEvidenceRecorder(t.Context(), "", "test", evidence.Scope{Kind: "explicit"}, t.TempDir())
	if recorder != nil {
		t.Fatalf("recorder = %+v, want none", recorder)
	}
	var sink strings.Builder
	step := recorder.start("label", []string{"true"}, t.TempDir(), &sink)
	if _, err := step.output.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if sink.String() != "x" {
		t.Errorf("sink = %q, want the command output unchanged", sink.String())
	}
	if step.logPath != "" {
		t.Errorf("log path = %q, want none recorded", step.logPath)
	}
	recorder.block("ignored")
	recorder.finishRun(step, nil, nil)
	if err := recorder.finish(nil); err != nil {
		t.Fatalf("finish with no manifest: %v", err)
	}
}

// Claim: a non-zero exit code is only reported for a real process failure. A
// command that never started must not look like it exited 0.
func TestExitCodeReportsUnstartedCommand(t *testing.T) {
	if got := exitCode(nil); got != 0 {
		t.Errorf("exitCode(nil) = %d, want 0", got)
	}
	if got := exitCode(errors.New("spawn failed")); got != -1 {
		t.Errorf("exitCode(plain error) = %d, want -1", got)
	}
}

// Claim: a label with separators cannot escape the evidence directory.
func TestSanitizeLogLabelProducesSingleFileName(t *testing.T) {
	for _, tc := range []struct {
		label string
		want  string
	}{
		{label: "module-a", want: "module-a"},
		{label: "./connectors/x [tags=precommit]", want: "connectors-x--tags-precommit"},
		{label: "///", want: "command"},
	} {
		if got := sanitizeLogLabel(tc.label); got != tc.want {
			t.Errorf("sanitizeLogLabel(%q) = %q, want %q", tc.label, got, tc.want)
		}
		if strings.ContainsAny(sanitizeLogLabel(tc.label), `/\`) {
			t.Errorf("sanitizeLogLabel(%q) kept a path separator", tc.label)
		}
	}
}

func readManifest(t *testing.T, path string) *evidence.Manifest {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	manifest := &evidence.Manifest{}
	if err := json.Unmarshal(data, manifest); err != nil {
		t.Fatalf("decode manifest: %v\n%s", err, data)
	}
	return manifest
}

func TestRecorder_MissingLogsCannotReturnSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	recorder := newEvidenceRecorder(t.Context(), path, "test", evidence.Scope{Kind: "explicit"}, t.TempDir())
	recorder.block("cannot retain full command logs")
	if err := recorder.finish(nil); err == nil {
		t.Fatal("missing log evidence returned success")
	}
	manifest := readManifest(t, path)
	if manifest.Outcome != evidence.OutcomeBlocked || manifest.FailureReason != "cannot retain full command logs" {
		t.Fatalf("lost evidence blocker: %+v", manifest)
	}
}

// Claim: a step whose log file cannot be opened still runs and streams its
// output to the caller, but the run is blocked with the log named as the
// reason. A step that cannot retain its log is not durable evidence, so it can
// never be reported as a passed verification.
func TestRecorder_UnopenableStepLogBlocksVerdict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	recorder := newEvidenceRecorder(t.Context(), path, "build", evidence.Scope{Kind: "explicit", Module: "."}, dir)
	// Establish the isolated run directory, then obstruct its next step log.
	primer := recorder.start("prepare", []string{"true"}, dir, &strings.Builder{})
	recorder.finishRun(primer, nil, nil)
	unopenable := filepath.Join(recorder.dir, "step-02-module-a.log")
	if err := os.MkdirAll(unopenable, 0o755); err != nil {
		t.Fatal(err)
	}
	var streamed strings.Builder
	step := recorder.start("module-a", []string{"go", "build", "./..."}, dir, &streamed)
	if _, err := step.output.Write([]byte("streamed")); err != nil {
		t.Fatalf("write to step output: %v", err)
	}
	if step.logPath != "" {
		t.Fatalf("step log path = %q, want none recorded for an unopenable log", step.logPath)
	}
	recorder.finishRun(step, nil, nil)
	err := recorder.finish(nil)
	if err == nil {
		t.Fatal("a run whose step log was never written returned success")
	}
	if streamed.String() != "streamed" {
		t.Errorf("caller stream = %q, want the command output to pass through unchanged", streamed.String())
	}
	manifest := readManifest(t, path)
	if manifest.Outcome != evidence.OutcomeBlocked || !strings.Contains(manifest.FailureReason, "open step log") {
		t.Fatalf("unopenable step log did not block the run: %+v", manifest)
	}
	if len(manifest.Steps) != 2 || manifest.Steps[1].Result != evidence.OutcomePassed || manifest.Steps[1].LogPath != "" {
		t.Fatalf("steps = %+v, want blocked log retention for the second command", manifest.Steps)
	}
}

// Concurrent worktrees may put differently named manifests in one scratch directory.
func TestRecorder_SharedDirectoryRetainsEachRunsLog(t *testing.T) {
	dir := t.TempDir()
	first := newEvidenceRecorder(t.Context(), filepath.Join(dir, "first.json"), "test", evidence.Scope{}, ".")
	second := newEvidenceRecorder(t.Context(), filepath.Join(dir, "second.json"), "test", evidence.Scope{}, ".")
	a := first.start("same-step", []string{"true"}, ".", &strings.Builder{})
	b := second.start("same-step", []string{"true"}, ".", &strings.Builder{})
	for _, item := range []struct {
		run  *commandRun
		text string
	}{{a, "first output"}, {b, "second output"}} {
		if _, err := io.WriteString(item.run.output, item.text); err != nil {
			t.Fatal(err)
		}
	}
	first.finishRun(a, nil, nil)
	second.finishRun(b, nil, nil)
	if err := first.finish(nil); err != nil {
		t.Fatal(err)
	}
	if err := second.finish(nil); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"first.json": "first output", "second.json": "second output"} {
		manifest := readManifest(t, filepath.Join(dir, name))
		got, err := os.ReadFile(manifest.Steps[0].LogPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("%s log=%q, want %q", name, got, want)
		}
	}
}

// A successful command does not certify inputs changed while it ran.
func TestRecorder_SourceMutationInvalidatesSuccessfulCommand(t *testing.T) {
	for _, mutation := range []string{"working", "index", "head", "untracked"} {
		t.Run(mutation, func(t *testing.T) {
			repo := t.TempDir()
			git := func(args ...string) {
				cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
				cmd.Env = gitscope.Environ()
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			git("init", "-q")
			git("config", "user.name", "fixture")
			git("config", "user.email", "fixture@example.invalid")
			write := func(name, text string) {
				if err := os.WriteFile(filepath.Join(repo, name), []byte(text), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write("source.go", "package before\n")
			git("add", "source.go")
			git("-c", "core.hooksPath=/dev/null", "commit", "-qm", "baseline")
			if mutation == "index" {
				write("source.go", "package changed\n")
			}
			path := filepath.Join(t.TempDir(), "manifest.json")
			recorder := newEvidenceRecorder(t.Context(), path, "test", evidence.Scope{}, repo)
			step := recorder.start("successful-command", []string{"true"}, repo, &strings.Builder{})
			switch mutation {
			case "working":
				write("source.go", "package changed\n")
			case "index":
				git("add", "source.go")
			case "head":
				git("-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-qm", "new head")
			case "untracked":
				write("new.go", "package added\n")
			}
			recorder.finishRun(step, nil, nil)
			if err := recorder.finish(nil); err == nil {
				t.Fatal("mutated inputs certified as passing")
			}
			manifest := readManifest(t, path)
			if manifest.Outcome == evidence.OutcomePassed || !strings.Contains(manifest.FailureReason, "source changed") {
				t.Fatalf("mutation verdict lost: %+v", manifest)
			}
		})
	}
}
