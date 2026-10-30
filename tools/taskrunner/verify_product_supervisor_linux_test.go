//go:build linux

package taskrunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestVerifyProductSupervisorKillsIsolatedProcessGroup exercises the POSIX
// session supervisor, not the controller: the supervised command spawns a
// descendant in its own process group and then hangs. GNU timeout would signal
// only the supervisor's group and leave that descendant alive. The supervisor
// must exit 124 at the shortened deadline, invalidate the reused evidence
// summary before starting, and kill the isolated descendant.
func TestVerifyProductSupervisorKillsIsolatedProcessGroup(t *testing.T) {
	helper := buildHelper(t)
	script, err := filepath.Abs("../../scripts/verify-product.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	evidence := filepath.Join(dir, "evidence")
	if err := os.MkdirAll(evidence, 0o755); err != nil {
		t.Fatal(err)
	}
	summaryPath := filepath.Join(evidence, "summary.json")
	if err := os.WriteFile(summaryPath, []byte(`{"verdict":"pass"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(dir, "grandchild.pid")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", script, "2", evidence,
		helper, "-mode=spawn-pipe-holder", "-detach", "-pid-file", pidFile)
	started := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start supervisor: %v", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	descendant := &escapedDescendant{}
	pid := awaitGrandchildPID(t, pidFile, descendant)
	t.Cleanup(func() { killGrandchild(t, pid) })

	select {
	case err := <-waited:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 124 {
			t.Fatalf("supervisor error = %v; want exit 124", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor did not return after its shortened deadline")
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("supervisor took %s; want the shortened 2s bound", elapsed)
	}
	if _, err := os.Stat(summaryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale summary stat = %v; want it removed before the command started", err)
	}
	waitProcessGone(t, pid)
}

// TestVerifyProductSupervisorFailsClosedWithoutSessionIdentity proves the
// handshake is mandatory: a slow inner Bash delays publishing the session id,
// and a separate-process-group descendant is already running. The supervisor
// must not proceed with an empty session id; it must terminate the whole tree.
func TestVerifyProductSupervisorFailsClosedWithoutSessionIdentity(t *testing.T) {
	helper := buildHelper(t)
	script, err := filepath.Abs("../../scripts/verify-product.sh")
	if err != nil {
		t.Fatal(err)
	}
	realBash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	shimDir := filepath.Join(dir, "shim")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(dir, "grandchild.pid")
	shim := fmt.Sprintf(`#!/bin/bash
"%s" -mode=spawn-pipe-holder -detach -pid-file "%s" &
sleep 5
exec /bin/bash "$@"
`, helper, pidFile)
	if err := os.WriteFile(filepath.Join(shimDir, "bash"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, realBash, script, "10", "", "true")
	cmd.Env = append(os.Environ(),
		"PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"AIP_PRODUCT_SMOKE_BOOTSTRAP_SECONDS=1",
	)
	started := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start supervisor: %v", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	descendant := &escapedDescendant{}
	pid := awaitGrandchildPID(t, pidFile, descendant)
	t.Cleanup(func() { killGrandchild(t, pid) })

	select {
	case err := <-waited:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
			t.Fatalf("supervisor error = %v; want exit 2 for an unacquired session identity", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor did not fail closed after the bootstrap deadline")
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("supervisor took %s; want the shortened 1s bootstrap bound", elapsed)
	}
	waitProcessGone(t, pid)
}

// TestVerifyProductSupervisorDoesNotDeleteInRepositorySummary proves the
// supervisor validates the evidence destination before invalidating a previous
// verdict. An in-repository path is the controller's error to report; deleting
// a repository file first would be a side effect on an invalid input.
func TestVerifyProductSupervisorDoesNotDeleteInRepositorySummary(t *testing.T) {
	repo := t.TempDir()
	scriptsDir := filepath.Join(repo, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "verify-product.sh"))
	if err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(scriptsDir, "verify-product.sh")
	if err := os.WriteFile(scriptPath, script, 0o755); err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(repo, "evidence")
	if err := os.MkdirAll(evidence, 0o755); err != nil {
		t.Fatal(err)
	}
	summary := filepath.Join(evidence, "summary.json")
	if err := os.WriteFile(summary, []byte(`{"verdict":"pass"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", scriptPath, "10", evidence, "true")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("supervisor with in-repository evidence: %v: %s", err, output)
	}
	if _, err := os.Stat(summary); err != nil {
		t.Fatalf("in-repository summary was modified: %v", err)
	}
}
