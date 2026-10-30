//go:build unix

package main

import (
	"os/signal"
	"syscall"
)

// ignoreBrokenPipeSignal keeps a write to a closed pipe from ending the
// fixture. Unix reports a broken pipe on stdout and stderr as SIGPIPE, and
// the Go runtime turns that signal into a fatal error.
func ignoreBrokenPipeSignal() {
	signal.Ignore(syscall.SIGPIPE)
}
