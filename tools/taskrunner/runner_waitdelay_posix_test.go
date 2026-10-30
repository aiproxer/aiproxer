//go:build !windows

package taskrunner

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// waitDelayTestSlack absorbs process teardown and scheduling noise in the
// bounded-wait assertions. The behavior under test is a missing bound, so a
// generous slack still separates a bounded return from an unbounded one.
const waitDelayTestSlack = 3 * time.Second

// waitDelayStartupBudget is far larger than any teardown bound so fixture
// startup cannot race the request deadline: the tests arm the deadline
// explicitly once the escaped descendant is known.
const waitDelayStartupBudget = 30 * time.Second

// escapedDescendant tracks the detached pipe-holding grandchild. The pid is
// recorded as soon as it parses, so teardown can be registered before any
// fallible readiness step runs.
type escapedDescendant struct {
	mu  sync.Mutex
	pid int
}

func (d *escapedDescendant) record(pid int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pid = pid
}

func (d *escapedDescendant) recordedPID() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pid
}

// startPipeHolderRun starts Run against the helper that spawns a pipe-holding
// descendant. Cancellation, the run join and descendant teardown are registered
// before any fallible readiness step, so a startup failure cannot leave the run
// goroutine or an escaped process behind.
func startPipeHolderRun(t *testing.T, argv []string) (<-chan Result, *escapedDescendant, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	descendant := &escapedDescendant{}
	done := make(chan Result, 1)
	finished := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		if pid := descendant.recordedPID(); pid != 0 {
			killGrandchild(t, pid)
		}
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Errorf("Run did not return within the teardown bound")
		}
	})
	go func() {
		defer close(finished)
		done <- Run(ctx, Request{
			Argv:      argv,
			Timeout:   waitDelayStartupBudget,
			Output:    Stream,
			StreamOut: &bytes.Buffer{},
			StreamErr: &bytes.Buffer{},
		})
	}()
	return done, descendant, cancel
}

// TestRunner_WaitDelayEscapedPipeHolder proves that a descendant that left the
// runner's process group and inherited the streamed output pipe cannot make Run
// block past the deadline plus the process wait delay. The descendant survives
// the group kill, so only the runner's own wait bound ends the run.
//
//nolint:paralleltest // serial: spawns and observes real descendants holding stream pipes
func TestRunner_WaitDelayEscapedPipeHolder(t *testing.T) {
	helper := buildHelper(t)
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	done, descendant, cancel := startPipeHolderRun(t, []string{
		helper, "-mode=spawn-pipe-holder", "-detach", "-pid-file", pidFile,
	})

	pid := awaitGrandchildPID(t, pidFile, descendant)
	cancel()
	armedAt := time.Now()

	bound := processWaitDelay + waitDelayTestSlack
	select {
	case result := <-done:
		if result.Kind != DeadlineExceeded {
			t.Fatalf("Kind = %q (err %v), want %q: the caller deadline remains the primary cause", result.Kind, result.Err, DeadlineExceeded)
		}
		if !result.Cleanup.Attempted {
			t.Fatal("Cleanup.Attempted = false, want true")
		}
		if !errors.Is(result.Cleanup.Err, exec.ErrWaitDelay) {
			t.Fatalf("Cleanup.Err = %v, want an error wrapping exec.ErrWaitDelay", result.Cleanup.Err)
		}
		if elapsed := time.Since(armedAt); elapsed > bound {
			t.Fatalf("Run returned %s after the deadline was armed, want at most %s (wait delay %s + slack %s)",
				elapsed, bound, processWaitDelay, waitDelayTestSlack)
		}
		// The descendant escaped the process group, so the group kill cannot
		// reach it and it still holds the pipe while Run returns. That leak is
		// exactly what the wait bound has to contain.
		if err := syscall.Kill(pid, 0); err != nil {
			t.Fatalf("descendant %d is not alive after Run returned: %v", pid, err)
		}
	case <-time.After(bound):
		t.Fatalf("Run did not return within %s after the deadline was armed (wait delay %s + slack %s): a descendant holding the stream pipe still blocks Wait",
			bound, processWaitDelay, waitDelayTestSlack)
	}
}

// TestRunner_WaitDelayCleanExitWithLeakedPipe proves that a command that exits
// successfully while a same-process descendant keeps the streamed output pipe
// open is reported as CleanupFailure with exec.ErrWaitDelay instead of hanging.
//
//nolint:paralleltest // serial: spawns and observes real descendants holding stream pipes
func TestRunner_WaitDelayCleanExitWithLeakedPipe(t *testing.T) {
	helper := buildHelper(t)
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	done, descendant, _ := startPipeHolderRun(t, []string{
		helper, "-mode=spawn-pipe-holder", "-exit-after-spawn", "-pid-file", pidFile,
	})

	_ = awaitGrandchildPID(t, pidFile, descendant)

	bound := processWaitDelay + waitDelayTestSlack
	select {
	case result := <-done:
		if result.Kind != CleanupFailure {
			t.Fatalf("Kind = %q (err %v), want %q", result.Kind, result.Err, CleanupFailure)
		}
		if result.ExitCode != 0 {
			t.Fatalf("ExitCode = %d, want 0: the command itself exited cleanly", result.ExitCode)
		}
		if !result.Cleanup.Attempted {
			t.Fatal("Cleanup.Attempted = false, want true")
		}
		if !errors.Is(result.Cleanup.Err, exec.ErrWaitDelay) {
			t.Fatalf("Cleanup.Err = %v, want an error wrapping exec.ErrWaitDelay", result.Cleanup.Err)
		}
	case <-time.After(bound):
		t.Fatalf("Run did not return within %s after the command exited: the inherited stream pipe still blocks Wait", bound)
	}
}

// awaitGrandchildPID waits for the helper's pid publication and records the pid
// for teardown. An empty or partial file is retried rather than treated as
// fatal: a transiently incomplete publication is re-readable, and failing on it
// would abort before the escaped descendant is known.
func awaitGrandchildPID(t *testing.T, path string, descendant *escapedDescendant) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil && pid > 0 {
				descendant.record(pid)
				return pid
			}
		} else if !os.IsNotExist(err) {
			t.Fatalf("read grandchild pid file %q: %v", path, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild pid file %q never held a complete pid", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func killGrandchild(t *testing.T, pid int) {
	t.Helper()
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Errorf("kill descendant %d: %v", pid, err)
		return
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("descendant %d survived SIGKILL", pid)
}
