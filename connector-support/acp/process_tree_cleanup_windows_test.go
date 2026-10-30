//go:build windows

package acp_test

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	acp "github.com/aiproxer/aiproxer/connector-support/acp"
)

// TestKillProcessTree_WindowsDescendants proves CREATE_NEW_PROCESS_GROUP +
// taskkill /T cleanup: parent helper spawns a long-lived grandchild; Kill removes
// both. Job Objects remain plugin-host native cleanup, not ACP support.
//
// The helper must still be running when Kill is called. taskkill terminates
// descendants before the root, so a helper that exited on its own as soon as its
// grandchild died would race this test's own teardown: taskkill then reports
// "There is no running instance of the task" for the already-exited root and
// exits non-zero (255) even though the whole tree is gone. The pre-Kill liveness
// waits below keep that requirement observable instead of leaving it to timing.
func TestKillProcessTree_WindowsDescendants(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	childPIDFile := filepath.Join(dir, "child.pid")
	proc, err := acp.OSProcessStarter{}.Start([]string{
		os.Args[0], "-test.run=TestHelperProcess_WindowsTreeParent$", "--",
	}, "", append(
		os.Environ(),
		"ACP_WANT_HELPER=windows-tree-parent",
		"ACP_CHILD_PID_FILE="+childPIDFile,
	))
	if err != nil {
		t.Fatal(err)
	}
	waitFile(t, childPIDFile, 8*time.Second)
	grandchild := readPIDFile(t, childPIDFile)
	waitAlive(t, proc.PID(), 8*time.Second)
	waitAlive(t, grandchild, 8*time.Second)
	if err := proc.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	waitGone(t, proc.PID(), 8*time.Second)
	waitGone(t, grandchild, 8*time.Second)
}

func TestHelperProcess_WindowsTreeParent(t *testing.T) {
	t.Parallel()
	if os.Getenv("ACP_WANT_HELPER") != "windows-tree-parent" {
		return
	}
	child := exec.Command("cmd", "/C", "ping -n 60 127.0.0.1 >nul")
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	_ = os.WriteFile(os.Getenv("ACP_CHILD_PID_FILE"), []byte(strconv.Itoa(child.Process.Pid)), 0o600)
	// Block until the owning test kills this helper or exits (which closes the
	// stdin pipe). Exiting as soon as the grandchild dies would race the
	// taskkill /T teardown this test asserts.
	_, _ = io.Copy(io.Discard, os.Stdin)
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

func waitAlive(t *testing.T, pid int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if processAliveWindows(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d not running before Kill", pid)
}

func waitGone(t *testing.T, pid int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !processAliveWindows(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive", pid)
}

func processAliveWindows(pid int) bool {
	cmd := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), strconv.Itoa(pid))
}
