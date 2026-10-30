//go:build !linux

package main

import (
	"fmt"
	"os"
)

// isolationSupported reports whether this host can build a mount namespace with
// passwordless elevation. Only Linux provides the mount namespace plus the
// sudo/setpriv pair the lane relies on, so every other host is explicitly
// unsupported rather than silently degrading to a weaker PATH-only proof.
func isolationSupported() bool { return false }

// isolationUnavailableReason explains, in operator terms, why the lane cannot
// prove Node absence on this host.
func isolationUnavailableReason() string {
	return fmt.Sprintf(
		"Node isolation needs a Linux mount namespace with passwordless sudo and setpriv; " +
			"run the no-Node lane on Linux (the Ubuntu CI lane) instead of accepting a PATH-only proof")
}

// requireNonRootEUID rejects an administrative caller. Off Linux this is a
// formality kept so the control is exercised on every platform.
func requireNonRootEUID(euid int) error {
	if euid == 0 {
		return fmt.Errorf("node-independence must not run as root")
	}
	return nil
}

// isolate is unreachable on unsupported hosts.
func isolate(string, []string, []string) error {
	return fmt.Errorf("node independence isolation unsupported: %s", isolationUnavailableReason())
}

// runRootHelper is unreachable on unsupported hosts.
func runRootHelper(string, []string, string) error {
	return fmt.Errorf("node independence isolation unsupported: %s", isolationUnavailableReason())
}

// validatedMaskTargets is unreachable on unsupported hosts.
func validatedMaskTargets([]string) ([]string, error) {
	return nil, fmt.Errorf("node independence isolation unsupported: %s", isolationUnavailableReason())
}

// isEntryPoint has no execute bit to inspect off Linux, so existence is the
// closest equivalent. The lane itself only runs on Linux.
func isEntryPoint(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// deviceOf has no meaning off Linux; the sweep then relies on prunedPrefixes
// alone, which is sufficient because the lane itself is Linux-only.
func deviceOf(any) uint64 { return 0 }

// sameDevice always reports true off Linux so the portable probe keeps walking.
func sameDevice(uint64, string) bool { return true }

// unmask is unreachable on unsupported hosts.
func unmask([]string) {}
