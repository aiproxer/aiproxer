//go:build windows

package acp

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

func killProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	out, err := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").CombinedOutput()
	if err == nil {
		return nil
	}
	// taskkill writes its reason to stdout and exits non-zero; without it the
	// caller only sees a bare exit status and cannot tell an access denial from
	// a process that had already exited.
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return fmt.Errorf("acp: kill process tree %d: %w: %s", pid, err, msg)
	}
	return fmt.Errorf("acp: kill process tree %d: %w", pid, err)
}
