//go:build windows

package processhost

// Windows production launch assigns children to a Job with KILL_ON_JOB_CLOSE.
func descendantCleanupSupported() bool { return true }
