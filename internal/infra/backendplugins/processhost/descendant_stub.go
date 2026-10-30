//go:build !linux && !windows

package processhost

// Darwin and other profiles do not claim native process-tree cleanup.
func descendantCleanupSupported() bool { return false }
