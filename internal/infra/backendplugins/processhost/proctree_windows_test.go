//go:build windows

package processhost_test

import (
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// stillActiveExitCode is the exit code a running Windows process reports.
const stillActiveExitCode = 259

// assertProcessExited proves the process is no longer running.
//
// A terminated process keeps its pid until every handle to it is closed, so
// the exit code is the proof: a running process reports STILL_ACTIVE.
func assertProcessExited(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		h, err := windows.OpenProcess(
			windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE,
			false,
			uint32(pid),
		)
		if err != nil {
			// The process is gone: its pid cannot be opened any more.
			return
		}
		var code uint32
		if err := windows.GetExitCodeProcess(h, &code); err != nil {
			_ = windows.CloseHandle(h)
			return
		}
		_ = windows.CloseHandle(h)
		if code != stillActiveExitCode {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d is still running", pid)
}
