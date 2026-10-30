//go:build linux

package acp

import (
	"fmt"
	"os/exec"
	"syscall"
)

func checkRunPlatform() error { return nil }

// waitRunExit observes exit without consuming it: the unreaped child reserves
// the PID until Run has killed its existing process group. CommandContext owns
// cancellation while this synchronous wait blocks; no extra watcher is needed.
func waitRunExit(cmd *exec.Cmd) error {
	for {
		// Linux permits a null siginfo pointer when only exit readiness matters.
		// P_PID (1) selects our direct child; WNOWAIT leaves cmd.Wait its sole reaper.
		_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, 1, uintptr(cmd.Process.Pid),
			0, syscall.WEXITED|syscall.WNOWAIT, 0, 0)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return fmt.Errorf("acp: wait for probe exit: %w", errno)
		}
		return nil
	}
}
