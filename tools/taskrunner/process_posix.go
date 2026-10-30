//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package taskrunner

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

const (
	killGroupConfirmTimeout = 2 * time.Second
	killGroupConfirmPoll    = 10 * time.Millisecond
)

type posixProcess struct{ cmd *exec.Cmd }

func newPlatformProcessAdapter(cmd *exec.Cmd, _ bool) (processAdapter, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &posixProcess{cmd: cmd}, nil
}

func (p *posixProcess) start() error               { return p.cmd.Start() }
func (p *posixProcess) startupCleanupError() error { return nil }
func (p *posixProcess) accounting() (ProcessAccounting, error) {
	return ProcessAccounting{Supported: false}, nil
}

func (p *posixProcess) kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
}

// killGroup kills any members left in the command's process group after
// cmd.Wait has reaped the leader. The group ID stays valid for the leader's
// remaining descendants, so the negative pid still addresses them. ESRCH means
// the group is already empty, which is success: this call exists to make the
// group empty, and the leader is already gone.
//
// Confirmation looks for live members, not merely an addressable group. A
// killed but unreaped descendant remains a group member as a zombie; the
// process-group existence probe alone cannot distinguish it from a process that
// survived SIGKILL and would report a false cleanup failure.
func (p *posixProcess) killGroup() error {
	if p.cmd.Process == nil {
		return nil
	}
	pid := p.cmd.Process.Pid
	killErr := syscall.Kill(-pid, syscall.SIGKILL)
	if errors.Is(killErr, syscall.ESRCH) {
		return nil
	}
	if killErr != nil {
		if alive, probeErr := processGroupHasLiveMembers(pid); probeErr == nil && !alive {
			return nil
		}
		return fmt.Errorf("kill process group %d: %w", pid, killErr)
	}
	// Confirm the delivery took effect so a member that survives SIGKILL is
	// reported instead of silently outliving the command.
	deadline := time.Now().Add(killGroupConfirmTimeout)
	var probeErr error
	for {
		alive, err := processGroupHasLiveMembers(pid)
		switch {
		case err != nil:
			probeErr = err
			// A failed inspection is not evidence of survival. Fall back to
			// group existence so a transient /proc read failure cannot turn a
			// successful kill into a false failure.
			if errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH) {
				return nil
			}
		case !alive:
			return nil
		}
		if time.Now().After(deadline) {
			if probeErr != nil {
				return fmt.Errorf("process group %d still has live members %s after SIGKILL: inspect group: %w", pid, killGroupConfirmTimeout, probeErr)
			}
			return fmt.Errorf("process group %d still has live members %s after SIGKILL", pid, killGroupConfirmTimeout)
		}
		time.Sleep(killGroupConfirmPoll)
	}
}

func (p *posixProcess) close() error { return nil }
