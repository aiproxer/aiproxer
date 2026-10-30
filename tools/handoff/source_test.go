package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
)

func TestHandoff_SourceIdentityAndStaleEvidence(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = repo, gitscope.Environ()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	write("source.go", "package example\n")
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath="+filepath.Join(repo, "no-hooks"), "commit", "-qm", "base")
	snap := func() SourceState {
		t.Helper()
		state, err := snapshot(t.Context(), repo)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	clean := snap()
	if again := snap(); again != clean {
		t.Fatal("unchanged source identity is unstable")
	}
	write("source.go", "package example\nconst Value = 1\n")
	dirty := snap()
	git("add", "source.go")
	staged := snap()
	write("untracked.go", "package example\n")
	untracked := snap()
	if clean == dirty || dirty == staged || staged == untracked {
		t.Fatal("source identity failed to distinguish unstaged/index/untracked state")
	}
	artifactDir := t.TempDir()
	log := filepath.Join(artifactDir, "verify.log")
	if err := os.WriteFile(log, []byte("passed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	zero, no := 0, false
	// Hook-exported Git selectors must not redirect the recorded command away
	// from the repository whose source identity was captured.
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "nonexistent.git"))
	var captured bytes.Buffer
	if err := captureCommand(t.Context(), repo, filepath.Join(artifactDir, "captured.log"), "verification", []string{"git", "rev-parse", "HEAD"}, &captured, io.Discard); err != nil {
		t.Fatal(err)
	}
	var generated struct {
		Source  SourceState `json:"source"`
		Command Command     `json:"command"`
	}
	if err := json.Unmarshal(captured.Bytes(), &generated); err != nil || generated.Source != untracked || generated.Command.ExitCode == nil || *generated.Command.ExitCode != 0 {
		t.Fatalf("recorded wrong source/status: %+v, %v", generated, err)
	}
	captured.Reset()
	if err := captureCommand(t.Context(), repo, filepath.Join(artifactDir, "failed.log"), "red", []string{"git", "rev-parse", "--verify", "missing-ref"}, &captured, io.Discard); err == nil {
		t.Fatal("recorder hid command failure")
	}
	if err := json.Unmarshal(captured.Bytes(), &generated); err != nil || generated.Command.ExitCode == nil || *generated.Command.ExitCode == 0 {
		t.Fatalf("failed command evidence wrong: %+v, %v", generated, err)
	}
	result := Result{Version: 1, Role: "implementer", Task: "1.1", Status: "READY_FOR_REVIEW", Source: untracked, Behavioral: &no, Findings: []Finding{}, Commands: []Command{{Purpose: "verification", Argv: []string{"go", "test"}, ExitCode: &zero, Evidence: log}}}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(artifactDir, "result.json")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"-repo", repo, "-file", file}
	if err := runHandoff(context.Background(), args, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	write("untracked.go", "package example\nconst New = 2\n")
	if err := runHandoff(context.Background(), args, io.Discard, io.Discard); err == nil {
		t.Fatal("stale evidence accepted")
	}
	if err := runHandoff(context.Background(), append(args, "-current=false", "-task=record", "-index", filepath.Join(artifactDir, "execution.json")), io.Discard, io.Discard); err == nil {
		t.Fatal("historical artifact advanced execution index")
	}
	captured.Reset()
	if err := captureCommand(t.Context(), repo, filepath.Join(artifactDir, "changed.log"), "verification", []string{"git", "update-index", "--force-remove", "source.go"}, &captured, io.Discard); err == nil || captured.Len() != 0 {
		t.Fatal("recorder attributed check to source changed during execution")
	}
}
