//go:build unix

package acp_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	acp "github.com/aiproxer/aiproxer/connector-support/acp"
)

// TestKillProcessTree_UnixProcessGroup proves Setpgid + kill(-pid) removes
// parent and grandchild on unix runners (Linux CI today; macOS via
// .github/workflows/acp-process-tree.yml macos-latest — required before Task 6.3).
func TestKillProcessTree_UnixProcessGroup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	childPIDFile := filepath.Join(dir, "child.pid")
	proc, err := acp.OSProcessStarter{}.Start([]string{
		os.Args[0], "-test.run=TestHelperProcess_UnixTreeParent$", "--",
	}, "", append(
		os.Environ(),
		"ACP_WANT_HELPER=unix-tree-parent",
		"ACP_CHILD_PID_FILE="+childPIDFile,
	))
	if err != nil {
		t.Fatal(err)
	}
	waitFile(t, childPIDFile, 8*time.Second)
	grandchild := readPIDFile(t, childPIDFile)
	if err := proc.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	// Reap the direct child so the grandchild is reparented to init and reaped.
	// Without this, the helper stays as a zombie (kill(pid,0) succeeds for zombies)
	// and the grandchild cannot be collected by init.
	_ = proc.Wait()
	waitGone(t, proc.PID(), 8*time.Second)
	waitGone(t, grandchild, 8*time.Second)
}

// Run's cancellation contract must retain Start/Kill's descendant cleanup, not
// merely CommandContext's default direct-child kill (identity gate design 2.6).
func TestRun_CancellationKillsProcessTree(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("short-lived Run fails closed outside Linux/WSL")
	}
	dir := t.TempDir()
	childPIDFile := filepath.Join(dir, "child.pid")
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	done := make(chan struct{})
	var runErr error
	go func() {
		defer close(done)
		runErr = (acp.OSProcessStarter{}).Run(ctx, []string{
			os.Args[0], "-test.run=TestHelperProcess_UnixTreeParent$", "--",
		}, dir, []string{
			"PATH=" + os.Getenv("PATH"),
			"ACP_WANT_HELPER=unix-tree-parent",
			"ACP_CHILD_PID_FILE=" + childPIDFile,
		}, nil, nil)
	}()
	// Always join the test-owned caller even if setup/assertion fails.
	t.Cleanup(func() { cancel(); <-done })
	waitFile(t, childPIDFile, 8*time.Second)
	grandchild := readPIDFile(t, childPIDFile)
	cancel()
	<-done
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("Run cancellation: %v", runErr)
	}
	waitGone(t, grandchild, 8*time.Second)
}

// Task 2.6: successful probe completion owns descendants even when they do not
// retain output pipes. Omitting success cleanup leaves this child running.
func TestRun_SuccessKillsRedirectedProcessTree(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("short-lived Run fails closed outside Linux/WSL")
	}
	dir := t.TempDir()
	childPIDFile := filepath.Join(dir, "child.pid")
	err := (acp.OSProcessStarter{}).Run(t.Context(), []string{
		os.Args[0], "-test.run=TestHelperProcess_UnixTreeParent$", "--",
	}, dir, []string{
		"PATH=" + os.Getenv("PATH"),
		"ACP_WANT_HELPER=unix-tree-parent",
		"ACP_CHILD_PID_FILE=" + childPIDFile,
		"ACP_PARENT_EXIT=1",
	}, nil, nil)
	grandchild := readPIDFile(t, childPIDFile)
	gone := false
	t.Cleanup(func() {
		if !gone {
			_ = syscall.Kill(grandchild, syscall.SIGKILL)
		}
	})
	if err != nil {
		t.Fatalf("Run success: %v", err)
	}
	waitGone(t, grandchild, 8*time.Second)
	gone = true
}

// A deadline must also quiesce the redirected child before Run returns.
func TestRun_TimeoutKillsRedirectedProcessTree(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("short-lived Run fails closed outside Linux/WSL")
	}
	dir := t.TempDir()
	childPIDFile := filepath.Join(dir, "child.pid")
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err := (acp.OSProcessStarter{}).Run(ctx, []string{
		os.Args[0], "-test.run=TestHelperProcess_UnixTreeParent$", "--",
	}, dir, []string{
		"PATH=" + os.Getenv("PATH"),
		"ACP_WANT_HELPER=unix-tree-parent",
		"ACP_CHILD_PID_FILE=" + childPIDFile,
	}, nil, nil)
	grandchild := readPIDFile(t, childPIDFile)
	gone := false
	t.Cleanup(func() {
		if !gone {
			_ = syscall.Kill(grandchild, syscall.SIGKILL)
		}
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run deadline: %v", err)
	}
	waitGone(t, grandchild, 8*time.Second)
	gone = true
}

func TestHelperProcess_UnixTreeParent(t *testing.T) {
	t.Parallel()
	if os.Getenv("ACP_WANT_HELPER") != "unix-tree-parent" {
		return
	}
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	_ = os.WriteFile(os.Getenv("ACP_CHILD_PID_FILE"), []byte(strconv.Itoa(child.Process.Pid)), 0o600)
	if os.Getenv("ACP_PARENT_EXIT") == "1" {
		os.Exit(0)
	}
	_, _ = child.Process.Wait()
	os.Exit(0)
}

func waitFile(t *testing.T, path string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", path)
}

func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func waitGone(t *testing.T, pid int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive", pid)
}
