//go:build !windows

package taskrunner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestProcessGroupKillOnExit_StopsGrandchildAfterNormalExit proves the opt-in
// group kill reaches a grandchild that survives a leader which exits normally.
// Without it, an inner `go test -timeout` panic kills the test binary before its
// t.Cleanup can stop the real proxy it started.
func TestProcessGroupKillOnExit_StopsGrandchildAfterNormalExit(t *testing.T) {
	helper := buildHelper(t)
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	result := Run(context.Background(), Request{
		Argv:            []string{helper, "-mode=spawn-grandchild", "-exit-after-spawn", "-pid-file", pidFile},
		Timeout:         10 * time.Second,
		Output:          Capture,
		KillGroupOnExit: true,
	})
	pid := readGrandchildPID(t, pidFile)
	if result.Kind != Success {
		t.Fatalf("kind = %q, err = %v; want success", result.Kind, result.Err)
	}
	if !result.Cleanup.Attempted {
		t.Fatal("cleanup was not attempted; want the opted-in group kill after the leader exited")
	}
	if result.Cleanup.Err != nil {
		t.Fatalf("cleanup error = %v; want the kill to succeed", result.Cleanup.Err)
	}
	waitProcessGone(t, pid)
}

// TestProcessGroupKillOnExit_DefaultKeepsGrandchild pins the default: without
// the option, a leader that exits normally does not stop its group descendants.
func TestProcessGroupKillOnExit_DefaultKeepsGrandchild(t *testing.T) {
	helper := buildHelper(t)
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	result := Run(context.Background(), Request{
		Argv:    []string{helper, "-mode=spawn-grandchild", "-exit-after-spawn", "-pid-file", pidFile},
		Timeout: 10 * time.Second,
		Output:  Capture,
	})
	pid := readGrandchildPID(t, pidFile)
	if result.Kind != Success {
		t.Fatalf("kind = %q, err = %v; want success", result.Kind, result.Err)
	}
	if result.Cleanup.Attempted {
		t.Fatal("cleanup was attempted; the default must leave the process group alone")
	}
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		waitProcessGone(t, pid)
	})
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("grandchild pid=%d check = %v; the default runner stopped it", pid, err)
	}
}

// TestProcessGroupKillOnExit_EmptyGroupIsNotACleanupFailure proves a leader
// whose group is already empty after Wait is not reported as a cleanup failure,
// on both the success and child-failure outcomes.
func TestProcessGroupKillOnExit_EmptyGroupIsNotACleanupFailure(t *testing.T) {
	helper := buildHelper(t)
	for _, tc := range []struct {
		name string
		args []string
		want Kind
	}{
		{name: "success", args: []string{"-mode=success"}, want: Success},
		{name: "child failure", args: []string{"-mode=fail", "-exit=7"}, want: ChildFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := Run(context.Background(), Request{
				Argv:            append([]string{helper}, tc.args...),
				Timeout:         10 * time.Second,
				Output:          Capture,
				KillGroupOnExit: true,
			})
			if result.Kind != tc.want {
				t.Fatalf("kind = %q, err = %v; want %q", result.Kind, result.Err, tc.want)
			}
			if !result.Cleanup.Attempted {
				t.Fatal("cleanup was not attempted")
			}
			if result.Cleanup.Err != nil {
				t.Fatalf("cleanup error = %v; an empty process group is success", result.Cleanup.Err)
			}
		})
	}
}

// TestProcessGroupKillOnExit_CaptureLeaderExitWithPipeHolder proves the opt-in
// group cleanup also runs when capture mode's leader exits successfully but a
// descendant keeps both output pipes open. Draining before cmd.Wait would never
// observe the leader's exit, so the request would consume its whole timeout and
// be classified DeadlineExceeded even though the leader succeeded.
func TestProcessGroupKillOnExit_CaptureLeaderExitWithPipeHolder(t *testing.T) {
	helper := buildHelper(t)
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	started := time.Now()
	result := Run(context.Background(), Request{
		Argv:            []string{helper, "-mode=spawn-pipe-holder", "-exit-after-spawn", "-pid-file", pidFile},
		Timeout:         5 * time.Second,
		Output:          Capture,
		KillGroupOnExit: true,
	})
	elapsed := time.Since(started)
	pid := readGrandchildPID(t, pidFile)
	if result.Kind == DeadlineExceeded {
		t.Fatalf("kind = %q after %s; a leader that exited successfully must not be reported as DeadlineExceeded because a descendant held the capture pipes", result.Kind, elapsed)
	}
	if result.Kind != CleanupFailure {
		t.Fatalf("kind = %q, err = %v; want %q: the leader exited successfully but the leaked pipe required WaitDelay cleanup", result.Kind, result.Err, CleanupFailure)
	}
	if !result.Cleanup.Attempted {
		t.Fatal("cleanup was not attempted")
	}
	if !errors.Is(result.Cleanup.Err, exec.ErrWaitDelay) {
		t.Fatalf("cleanup error = %v; want an error wrapping exec.ErrWaitDelay", result.Cleanup.Err)
	}
	if elapsed > processWaitDelay+3*time.Second {
		t.Fatalf("capture run took %s; want at most %s (wait delay %s + slack)", elapsed, processWaitDelay+3*time.Second, processWaitDelay)
	}
	waitProcessGone(t, pid)
}

func TestProcessGroupKillOnExit_PropagatesCleanupFailure(t *testing.T) {
	helper := buildHelper(t)
	cmd := exec.Command(helper, "-mode=success")
	cmd.WaitDelay = processWaitDelay
	adapter, err := newProcessAdapter(cmd, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.start(); err != nil {
		t.Fatal(err)
	}
	want := errors.New("process group still has members after SIGKILL")
	result := waitForProcess(
		context.Background(), cmd, failingKillGroup{processAdapter: adapter, err: want},
		Result{}, time.Now(), 10*time.Second, true,
	)
	if !result.Cleanup.Attempted {
		t.Fatal("cleanup was not attempted")
	}
	if !errors.Is(result.Cleanup.Err, want) {
		t.Fatalf("cleanup error = %v; want it to carry the surviving-group failure", result.Cleanup.Err)
	}
	if result.Kind != CleanupFailure {
		t.Fatalf("kind = %q; a successful leader with a failed group kill must not pass", result.Kind)
	}
}

// failingKillGroup lets the test prove cleanup failures reach Result without
// needing an unkillable process.
type failingKillGroup struct {
	processAdapter
	err error
}

func (a failingKillGroup) killGroup() error { return a.err }

func readGrandchildPID(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read grandchild pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse grandchild pid %q: %v", raw, err)
	}
	return pid
}

func waitProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process pid=%d is still alive", pid)
}
