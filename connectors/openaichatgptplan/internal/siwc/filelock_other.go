//go:build !unix && !windows

package siwc

import (
	"errors"
	"os"
)

// The advisory lock on the platforms that have none.
//
// The connector runs on the platforms whose kernels provide an advisory
// lock, and a platform that provides none FAILS CLOSED: the lock reports
// that it is unavailable instead of pretending to hold one. A connector
// that cannot serialize its rotations must not run them.
//
// The failure is explicit, and the caller treats it as the lock it could
// not take.

// lockState is the platform state of a held lock.
type lockState struct{}

// errNoAdvisoryLock reports that this platform has no advisory lock.
var errNoAdvisoryLock = errors.New(
	"siwc: this platform provides no advisory lock; the refresh cannot be serialized",
)

// tryLockFile always fails.
func tryLockFile(_ *os.File) (lockState, bool, error) {
	return lockState{}, false, errNoAdvisoryLock
}

// unlockFile does nothing.
func unlockFile(_ *os.File, _ lockState) error { return nil }
