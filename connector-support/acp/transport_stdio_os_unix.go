//go:build !windows

package acp

import (
	"os/exec"
	"syscall"
)

func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	// Send SIGKILL to the negative PID to kill the process group.
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
