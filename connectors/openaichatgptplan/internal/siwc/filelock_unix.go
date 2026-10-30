//go:build unix

package siwc

import (
	"errors"
	"os"
	"syscall"
)

// The advisory lock on the platforms whose kernel provides flock(2).
//
// The lock is an exclusive, non-blocking flock on the file descriptor. The
// kernel owns it: the lock is released when the descriptor is closed, and
// the kernel closes the descriptors when the process dies. A process that
// holds the lock keeps it for as long as it likes - elapsed time is not
// evidence of anything.
//
// The lock belongs to the OPEN FILE DESCRIPTION, so two INDEPENDENT open
// file descriptions of the same file conflict - even inside one process.
// Descriptors that come from ONE description share the lock and do not
// conflict with each other.
//
// The package does NOT RELY on that property: the store's in-process
// admission is what serializes the writers of one registration inside one
// process, on every platform. The property is a fact about this platform,
// and the test of it is a fact about the platform the test runs on - not
// something the cross-process serialization is built on.

// lockState is the platform state of a held lock.
//
// flock(2) needs nothing beyond the descriptor.
type lockState struct{}

// tryLockFile takes the exclusive lock of the file without waiting.
//
// It reports whether the lock was taken. A failure that is not a conflict
// is an error: the caller must not treat an unusable lock as a free one.
func tryLockFile(file *os.File) (lockState, bool, error) {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		return lockState{}, true, nil
	case errors.Is(err, syscall.EWOULDBLOCK), errors.Is(err, syscall.EAGAIN):
		return lockState{}, false, nil
	default:
		return lockState{}, false, err
	}
}

// unlockFile releases the lock of the file.
func unlockFile(file *os.File, _ lockState) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
