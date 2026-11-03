//go:build linux

package bridge

import (
	"os/exec"
	"syscall"
	"time"
)

// configureProcessGroup makes the sidecar the leader of its own process group
// so the Go owner can terminate the sidecar and every SDK child it spawned.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	// Only signal a group this connector created. pid 1 is never ours and the
	// proxy's own group must never be a target.
	if pid <= 1 || pid == syscall.Getpgrp() {
		return nil
	}
	// Negative pid targets the whole sidecar-owned group, including the
	// Claude Code CLI child and any SDK descendants. ESRCH means the group is
	// already empty, which is the normal clean-shutdown outcome.
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}

// waitProcessGroupGone waits until the owned group has no members left, or the
// bound expires. A descendant reparented away at leader exit is still a group
// member until it is reaped, so the wait makes ownership discharge observable
// to a caller that checks immediately after Close.
func waitProcessGroupGone(cmd *exec.Cmd, timeout time.Duration) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if pid <= 1 || pid == syscall.Getpgrp() {
		return
	}
	deadline := time.Now().Add(timeout)
	for {
		if err := syscall.Kill(-pid, 0); err == syscall.ESRCH {
			return
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}
