//go:build unix

package main

import (
	"fmt"
	"os/exec"
)

// The detached launch of the browser launcher.
//
// The launcher is started by a shell that backgrounds it and exits at once.
// The launcher is therefore orphaned: its parent is the shell, the shell's
// parent is this command, and the shell's exit reparents the launcher to
// the init process. The operating system reaps the launcher when it exits,
// and this command is never the parent of a process it would have to wait
// for.
//
// The shell exits as soon as it has started the background job, so the
// wait below is bounded by the shell's startup and not by the browser's
// lifetime.
//
// The browser the launcher opens is a different process, and this command
// never signals it.

// launchDetached starts the launcher detached from this command.
//
// The launcher is passed as positional arguments, never interpolated into
// the script, so a URL can never become shell syntax.
//
// The shell is addressed by its absolute path: the command must be able to
// open a browser even when the operator's PATH does not name a shell.
//
// The opener's executable is RESOLVED BEFORE the detachment. A launcher
// that does not exist is a failure the operator has to know about, and a
// backgrounded job that cannot exec reports nothing at all: the shell
// schedules it and exits successfully, so the command would learn that the
// shell exited and would never learn that no browser opened the URL.
//
// The failure names the missing program and never the target URL: the URL
// carries the attempt's state, its nonce and its PKCE challenge, and it is
// a credential-bearing value.
func launchDetached(target string) error {
	launcher := launcherCommand(target)
	if launcher.Err != nil {
		return fmt.Errorf("the browser opener %q is not available: %w", launcher.Path, launcher.Err)
	}
	if _, err := exec.LookPath(launcher.Path); err != nil {
		return fmt.Errorf("the browser opener %q is not available: %w", launcher.Path, err)
	}
	script := `exec "$0" "$@" </dev/null >/dev/null 2>&1 &`
	arguments := append([]string{"-c", script, launcher.Path}, launcher.Args[1:]...)
	command := exec.Command("/bin/sh", arguments...)
	if err := command.Start(); err != nil {
		return err
	}
	// The shell returns as soon as it has started the background job.
	return command.Wait()
}
