//go:build linux

package processhost_test

import (
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// assertProcessExited proves the process is gone.
//
// Only ESRCH proves the pid is gone: any other error, EPERM for example,
// leaves the question open and fails the test instead of passing it.
func assertProcessExited(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		err := unix.Kill(pid, 0)
		switch {
		case err == nil:
			// The pid still exists.
		case errors.Is(err, unix.ESRCH):
			return
		default:
			t.Fatalf("probing pid %d failed: %v", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d is still running", pid)
}
