//go:build !linux

package bridge

import (
	"errors"
	"os"
	"os/exec"
	"time"
)

// Windows/macOS certification is deferred. The fallback kills only the direct
// child; the connector still refuses to run outside its certified Linux profile.
func configureProcessGroup(*exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	err := cmd.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

// waitProcessGroupGone has no group to observe outside the certified Linux
// profile; it only honors the bounded wait contract.
func waitProcessGroupGone(_ *exec.Cmd, _ time.Duration) {}
