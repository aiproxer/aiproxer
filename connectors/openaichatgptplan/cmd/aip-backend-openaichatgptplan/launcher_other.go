//go:build !unix

package main

import (
	"fmt"
	"os/exec"
)

// The detached launch of the browser launcher.
//
// Windows has no zombie process state: the operating system reaps a process
// when it exits, and the process handle is all a parent holds. The launcher
// is therefore started directly and the handle is released at once.
//
// The browser the launcher opens is a different process, and this command
// never signals it.

// launchDetached starts the launcher detached from this command.
//
// The opener's executable is RESOLVED BEFORE the detachment, exactly as on
// the POSIX path: a launcher that does not exist is a failure the operator
// has to know about, and the failure names the missing program and never
// the target URL.
func launchDetached(target string) error {
	command := launcherCommand(target)
	if command.Err != nil {
		return fmt.Errorf("the browser opener %q is not available: %w", command.Path, command.Err)
	}
	if _, err := exec.LookPath(command.Path); err != nil {
		return fmt.Errorf("the browser opener %q is not available: %w", command.Path, err)
	}
	if err := command.Start(); err != nil {
		return err
	}
	// The handle is dropped: the command keeps no worker for a process it
	// does not own past the sign-in, and Windows reaps the process itself.
	_ = command.Process.Release()
	return nil
}
