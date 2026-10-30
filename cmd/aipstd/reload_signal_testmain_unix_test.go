//go:build unix

package main

import (
	"os"
	"os/signal"
	"syscall"
	"testing"
)

func TestMain(m *testing.M) {
	// Keep the test process alive after adapters unregister SIGHUP. Notify still
	// delivers to registered channels while active; Ignore covers the gaps.
	signal.Ignore(syscall.SIGHUP)
	os.Exit(m.Run())
}
