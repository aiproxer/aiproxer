//go:build unix

package main

import (
	"os"
	"syscall"
)

// ShutdownSignals are graceful-shutdown signals for NotifyContext (req 11.1-11.2).
func ShutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

// ReloadSignals are explicit reload triggers, registered separately from shutdown (req 11.1).
func ReloadSignals() []os.Signal {
	return []os.Signal{syscall.SIGHUP}
}

// PlatformReloadMode reports the supported reload trigger surfaces on this OS (req 1.8, 11.8).
func PlatformReloadMode() string { return "sighup+api" }

// SignalsOverlap reports whether reload and shutdown signal sets intersect (req 11.1).
func SignalsOverlap() bool {
	shut := map[os.Signal]struct{}{}
	for _, s := range ShutdownSignals() {
		shut[s] = struct{}{}
	}
	for _, s := range ReloadSignals() {
		if _, ok := shut[s]; ok {
			return true
		}
	}
	return false
}
