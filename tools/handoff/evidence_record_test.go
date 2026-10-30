package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
)

func TestRecordedEvidence_RejectsChangedCommandSourceAndLog(t *testing.T) {
	repo, dir := t.TempDir(), t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "core.hooksPath=" + dir, "commit", "--allow-empty", "-qm", "base"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = gitscope.Environ()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v: %s", args, err, out)
		}
	}
	var output bytes.Buffer
	log := filepath.Join(dir, "check.log")
	if err := captureCommand(t.Context(), repo, log, "verification", []string{"git", "rev-parse", "HEAD"}, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	var record struct {
		Source  SourceState `json:"source"`
		Command Command     `json:"command"`
	}
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	var summary, diagnostics bytes.Buffer
	if err := runHandoff(t.Context(), []string{"-task=run", "-repo=" + repo, "-output=summary", "-log=" + filepath.Join(dir, "summary.log"), "--", "git", "rev-parse", "HEAD"}, &summary, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if diagnostics.Len() != 0 || !bytes.Contains(summary.Bytes(), []byte(`"exit_code":0`)) {
		t.Fatalf("summary lost status or streamed output: %s / %s", &summary, &diagnostics)
	}
	no := false
	result := Result{Version: 1, Role: "implementer", Task: "evidence", Status: "READY_FOR_REVIEW", Source: record.Source, Behavioral: &no, Commands: []Command{record.Command}, Findings: []Finding{}}
	if err := result.Validate(dir); err != nil {
		t.Fatal(err)
	}
	relative := result
	relative.Commands = append([]Command(nil), result.Commands...)
	relative.Commands[0].Evidence = filepath.Base(log)
	relative.Commands[0].Record = filepath.Base(record.Command.Record)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeDir, err := filepath.Rel(cwd, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := relative.Validate(relativeDir); err != nil {
		t.Fatalf("relative evidence rejected: %v", err)
	}
	for _, change := range []string{"argv", "exit", "source", "missing-record", "log"} {
		t.Run(change, func(t *testing.T) {
			bad := result
			bad.Commands = append([]Command(nil), result.Commands...)
			switch change {
			case "argv":
				bad.Commands[0].Argv = []string{"make", "test"}
			case "exit":
				code := 7
				bad.Commands[0].ExitCode = &code
			case "source":
				bad.Source.Fingerprint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			case "missing-record":
				bad.Commands[0].Record = filepath.Join(dir, "missing.record.json")
			case "log":
				if err := os.WriteFile(log, []byte("replacement"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := bad.Validate(dir); err == nil {
				t.Fatal("changed evidence accepted")
			}
		})
	}
}

func TestGoRED_RequiresTestFailureNotBuildFailure(t *testing.T) {
	for _, tc := range []struct {
		log  string
		want bool
	}{
		{"# example\nundefined: missing\nFAIL example [build failed]\n", false},
		{"--- FAIL: TestTerminal (0.00s)\nFAIL example\n", true},
		{`{"Action":"fail","Package":"example"}` + "\n", false},
		{`{"Action":"fail","Package":"example","Test":"TestTerminal"}` + "\n", true},
	} {
		if got := goTestAssertionFailure([]byte(tc.log)); got != tc.want {
			t.Fatalf("log %q: got %v, want %v", tc.log, got, tc.want)
		}
	}
}

func TestAssembleResult_UsesReceiptsAndPreservesArtifactOnFailure(t *testing.T) {
	dir := t.TempDir()
	state := SourceState{Head: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Fingerprint: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	no, zero := false, 0
	log := filepath.Join(dir, "verify.log")
	data := []byte("passed\n")
	if err := os.WriteFile(log, data, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	record := commandRecord{Source: state, Command: Command{Purpose: "verification", Argv: []string{"go", "test", "./tools/handoff"}, ExitCode: &zero, Evidence: log}, LogSHA256: hex.EncodeToString(digest[:])}
	receipt := log + ".record.json"
	if err := writeJSONAtomic(receipt, record); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, "result.json")
	template := Result{Version: 1, Role: "implementer", Task: "assemble", Status: "READY_FOR_REVIEW", Behavioral: &no, Findings: []Finding{}}
	if err := writeJSONAtomic(name, template); err != nil {
		t.Fatal(err)
	}
	if err := assembleResult(name, state, []string{receipt}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var assembled Result
	if err := json.Unmarshal(before, &assembled); err != nil {
		t.Fatal(err)
	}
	if assembled.Source != state || len(assembled.Commands) != 1 || !bytes.Contains(before, []byte("./tools/handoff")) {
		t.Fatalf("assembly lost recorded argv or source: %s", before)
	}
	// A nonzero compiler exit cannot satisfy behavioral readiness; a recorded
	// test failure can. Verify the acceptance path, not just the log parser.
	redLog := filepath.Join(dir, "red.log")
	one, yes := 1, true
	redCommand := Command{Purpose: "red", Argv: []string{"go", "test", "./example"}, ExitCode: &one, Evidence: redLog, Record: redLog + ".record.json"}
	for _, tc := range []struct {
		output   string
		accepted bool
	}{
		{"FAIL example [build failed]\n", false},
		{"--- FAIL: TestTerminal (0.00s)\nFAIL example\n", true},
	} {
		data := []byte(tc.output)
		if err := os.WriteFile(redLog, data, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		if err := writeJSONAtomic(redCommand.Record, commandRecord{Source: state, Command: redCommand, LogSHA256: hex.EncodeToString(digest[:])}); err != nil {
			t.Fatal(err)
		}
		probe := assembled
		probe.Behavioral = &yes
		probe.Commands = append(append([]Command(nil), assembled.Commands...), redCommand)
		if err := probe.Validate(dir); (err == nil) != tc.accepted {
			t.Fatalf("RED %q: %v", tc.output, err)
		}
	}
	if err := os.WriteFile(log, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := assembleResult(name, state, []string{receipt}); err == nil {
		t.Fatal("tampered log accepted")
	}
	after, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed assembly overwrote result")
	}
}

// newRecorderRepo returns a committed repository with one tracked file so a
// recorded command can change staged or working state while the recorder runs.
func newRecorderRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = repo, gitscope.Environ()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "tracked"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "tracked")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath="+filepath.Join(repo, "no-hooks"), "commit", "-qm", "base")
	return repo
}

func TestCaptureCommand_RecaptureInvalidatesPreviousReceipt(t *testing.T) {
	repo, dir := newRecorderRepo(t), t.TempDir()
	log := filepath.Join(dir, "check.log")
	var output bytes.Buffer
	if err := captureCommand(t.Context(), repo, log, "verification", []string{"git", "rev-parse", "HEAD"}, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	receipt := log + ".record.json"
	if _, err := os.Stat(receipt); err != nil {
		t.Fatalf("first capture recorded no receipt: %v", err)
	}
	// The recapture invalidates the old receipt before overwriting the log, then
	// fails before a replacement can be written because the index changes.
	if err := captureCommand(t.Context(), repo, log, "verification", []string{"git", "update-index", "--force-remove", "tracked"}, &output, io.Discard); err == nil {
		t.Fatal("source change during recapture accepted")
	}
	if _, err := os.Stat(receipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale receipt survived a failed recapture: %v", err)
	}
}

func TestCaptureCommand_RejectsUnknownEvidencePurpose(t *testing.T) {
	repo, dir := newRecorderRepo(t), t.TempDir()
	log := filepath.Join(dir, "check.log")
	var output bytes.Buffer
	if err := captureCommand(t.Context(), repo, log, "pretend", []string{"git", "rev-parse", "HEAD"}, &output, io.Discard); err == nil {
		t.Fatal("unknown evidence purpose accepted")
	}
	if output.Len() != 0 {
		t.Fatalf("rejected capture emitted a record: %s", output.Bytes())
	}
	if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected capture wrote evidence: %v", err)
	}
}

func TestAssembleResult_RejectsReceiptOutsideEvidenceLog(t *testing.T) {
	dir := t.TempDir()
	state := SourceState{Head: strings.Repeat("a", 40), Fingerprint: strings.Repeat("b", 64)}
	zero := 0
	log := filepath.Join(dir, "verify.log")
	data := []byte("passed\n")
	if err := os.WriteFile(log, data, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	record := commandRecord{Source: state, Command: Command{Purpose: "verification", Argv: []string{"go", "test", "./tools/handoff"}, ExitCode: &zero, Evidence: log}, LogSHA256: hex.EncodeToString(digest[:])}
	elsewhere := filepath.Join(dir, "moved.record.json")
	if err := writeJSONAtomic(elsewhere, record); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, "result.json")
	no := false
	if err := writeJSONAtomic(name, Result{Version: 1, Role: "implementer", Task: "assemble", Status: "READY_FOR_REVIEW", Behavioral: &no, Findings: []Finding{}}); err != nil {
		t.Fatal(err)
	}
	if err := assembleResult(name, state, []string{elsewhere}); err == nil {
		t.Fatal("receipt detached from its evidence log accepted")
	}
}
