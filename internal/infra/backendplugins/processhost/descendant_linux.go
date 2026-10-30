//go:build linux

package processhost

// Linux Launch uses a process group; SignalKill/GracefulStop target the group.
// This is not a full descendant-tree claim across arbitrary reparents.
func descendantCleanupSupported() bool { return true }
