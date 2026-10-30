//go:build !windows && !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package taskrunner

import (
	"os/exec"
)

type otherProcess struct{ cmd *exec.Cmd }

func newPlatformProcessAdapter(cmd *exec.Cmd, _ bool) (processAdapter, error) {
	return &otherProcess{cmd: cmd}, nil
}
func (p *otherProcess) start() error               { return p.cmd.Start() }
func (p *otherProcess) startupCleanupError() error { return nil }
func (p *otherProcess) accounting() (ProcessAccounting, error) {
	return ProcessAccounting{Supported: false}, nil
}

func (p *otherProcess) kill() error {
	if p.cmd.Process != nil {
		return p.cmd.Process.Kill()
	}
	return nil
}

// killGroup is a no-op: this platform has neither a process group nor a job
// primitive the runner can address after the leader is reaped.
func (p *otherProcess) killGroup() error { return nil }

func (p *otherProcess) close() error { return nil }
