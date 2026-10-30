package runtimebundle_test

import (
	"math"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

func TestProcessAlive_CurrentProcess(t *testing.T) {
	t.Parallel()
	pid := os.Getpid()
	if !processAlive(pid) {
		t.Fatalf("expected current process PID %d to be alive", pid)
	}
}

func TestProcessAlive_InvalidPID(t *testing.T) {
	t.Parallel()
	for _, pid := range []int{0, -1, -999} {
		if processAlive(pid) {
			t.Errorf("expected invalid PID %d to not be alive", pid)
		}
	}
}

func TestProcessAlive_BoundaryPID(t *testing.T) {
	t.Parallel()
	for _, pid := range []int{0, -1, -999, math.MinInt} {
		if processAlive(pid) {
			t.Errorf("expected non-positive PID %d to not be alive", pid)
		}
	}
	if strconv.IntSize == 64 {
		for _, pid := range []int{math.MaxInt, int(uint64(math.MaxUint32) + 1), int(uint64(math.MaxUint32) + 100)} {
			if processAlive(pid) {
				t.Errorf("expected out-of-range PID %d to not be alive", pid)
			}
		}
	}
}

func TestProcessAlive_ChildProcessLifecycle(t *testing.T) {
	t.Parallel()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcessExit$", "-test.v")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start: %v", err)
	}
	pid := cmd.Process.Pid
	if !processAlive(pid) {
		t.Fatalf("expected started child process PID %d to be alive", pid)
	}

	if err := cmd.Wait(); err != nil {
		t.Fatalf("cmd.Wait: %v", err)
	}

	// Bounded wait for process handle state to reflect termination
	deadline := time.Now().Add(3 * time.Second)
	alive := true
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			alive = false
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if alive {
		t.Fatalf("expected exited child process PID %d to not be alive", pid)
	}
}

func TestHelperProcessExit(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	time.Sleep(50 * time.Millisecond)
	os.Exit(0)
}
