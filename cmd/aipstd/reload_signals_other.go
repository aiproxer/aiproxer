//go:build !unix

package main

import (
	"os"
	"syscall"
)

// ShutdownSignals are graceful-shutdown signals for NotifyContext (req 11.1-11.2).
func ShutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

// ReloadSignals is empty on platforms without SIGHUP (req 1.8, 11.8).
func ReloadSignals() []os.Signal { return nil }

// PlatformReloadMode reports API-only reload on platforms without SIGHUP (req 1.8, 11.8).
func PlatformReloadMode() string { return "api-only" }

// SignalsOverlap is always false when reload signals are empty (req 11.1).
func SignalsOverlap() bool { return false }
