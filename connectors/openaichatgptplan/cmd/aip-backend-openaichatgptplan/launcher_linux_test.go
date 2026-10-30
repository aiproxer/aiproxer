//go:build linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The process ownership of the browser launcher, proven on the process
// table.
//
// The property is that this command never becomes the parent of the
// launcher: the launcher is started by a shell that backgrounds it and
// exits, so the operating system reparents the launcher to init and reaps
// it. A launcher that is this command's own child would stay a ZOMBIE child
// of a command that is still running the authorization.
//
// The proof is the process table, not a goroutine count: the probe reads
// /proc/<pid>/stat and looks at the state and the parent.

// TestOpenBrowser_LauncherIsNotAChildOfTheCommand proves that the launcher
// is not this command's child.
//
// A child of this command that has exited would be a zombie whose parent is
// this command's pid. The launcher is detached, so it is either gone - the
// operating system reaped it - or its parent is somebody else.
func TestOpenBrowser_LauncherIsNotAChildOfTheCommand(t *testing.T) {
	directory := t.TempDir()
	pidFile := filepath.Join(directory, "pid")
	script := "#!/bin/sh\nprintf '%s' \"$$\" > '" + pidFile + "'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(directory, "xdg-open"), []byte(script), 0o700); err != nil {
		t.Fatalf("write the controlled launcher: %v", err)
	}
	t.Setenv("PATH", directory)

	if err := openBrowser(context.Background(), "https://example.invalid/"); err != nil {
		t.Fatalf("openBrowser: %v", err)
	}
	pid := waitForPID(t, pidFile)

	// A wait on a process that is not this command's child fails with
	// ECHILD. That is the proof that the operating system owns the
	// launcher's lifetime, not this command.
	var status syscall.WaitStatus
	reaped, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
	if err == nil {
		t.Fatalf("the launcher is this command's child: wait4 reaped %d", reaped)
	}
	if err != syscall.ECHILD {
		t.Fatalf("wait4 on the launcher failed with %v, want ECHILD: the launcher is this command's child", err)
	}

	// The process table agrees: the launcher is either gone, or it is not
	// a zombie child of this command.
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		// The launcher is gone: the operating system reaped it.
		return
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 4 {
		return
	}
	if fields[2] == "Z" && fields[3] == strconv.Itoa(os.Getpid()) {
		t.Fatalf("the launcher is a zombie child of this command: %s", string(raw))
	}
}

// TestOpenBrowser_LongLivedBrowserSurvives proves that a browser that
// outlives the launcher is not killed.
//
// The controlled launcher starts a child that keeps running and then exits.
// The child is the user's browser session: it may outlive the login, and
// the command must never signal it.
//
// The child waits for a RELEASE file that the test writes, so the test
// decides when the child completes. The child therefore CANNOT finish
// before the test releases it, and the fixture can observe it while it is
// still alive: a child that finished at once would prove nothing.
func TestOpenBrowser_LongLivedBrowserSurvives(t *testing.T) {
	directory := t.TempDir()
	// The launcher's script runs with a PATH that holds only the controlled
	// launcher, so the sleep it uses is resolved DELIBERATELY, while the
	// real PATH is still in place, and the script names it by its absolute
	// path. An unqualified sleep would not be found at all: the shell
	// would report "sleep: not found" and would continue as if the delay
	// had happened.
	sleep := resolveExecutable(t, "sleep")
	started := filepath.Join(directory, "browser-started")
	alive := filepath.Join(directory, "browser-alive")
	release := filepath.Join(directory, "browser-release")
	pidFile := filepath.Join(directory, "browser-pid")
	script := "#!/bin/sh\n" +
		"( printf started > '" + started + "'; " +
		"while [ ! -f '" + release + "' ]; do " + sleep + " 1; done; " +
		"printf alive > '" + alive + "' ) &\n" +
		"printf '%s' \"$!\" > '" + pidFile + "'\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(directory, "xdg-open"), []byte(script), 0o700); err != nil {
		t.Fatalf("write the controlled launcher: %v", err)
	}
	t.Setenv("PATH", directory)

	if err := openBrowser(context.Background(), "https://example.invalid/"); err != nil {
		t.Fatalf("openBrowser: %v", err)
	}
	// The child started.
	waitForFile(t, started)
	pid := waitForPID(t, pidFile)
	// The child has NOT completed: the release file does not exist yet, so
	// the child cannot have reached the end of its script.
	if _, err := os.Stat(alive); err == nil {
		t.Fatal("the browser session completed before the test released it")
	}
	// The child is ALIVE across the boundary: the command has returned and
	// the child is still running. The process table is the proof, the same
	// way the ownership test above proves that the launcher is not this
	// command's child.
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Fatalf("the browser session is not alive across the launch: %v", err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 3 {
		t.Fatalf("the process table entry of the browser session is truncated: %s", string(raw))
	}
	if fields[2] == "Z" {
		t.Fatalf("the browser session is a zombie: %s", string(raw))
	}
	// The command did not kill it: the child completes on its own once the
	// test releases it.
	if err := os.WriteFile(release, []byte("release\n"), 0o600); err != nil {
		t.Fatalf("release the browser session: %v", err)
	}
	waitForFile(t, alive)
}

// waitForFile waits for a file to appear.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the file %s did not appear", filepath.Base(path))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForPID reads the launcher pid the controlled launcher wrote.
func waitForPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err == nil {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if convErr == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the controlled launcher did not report its pid")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
