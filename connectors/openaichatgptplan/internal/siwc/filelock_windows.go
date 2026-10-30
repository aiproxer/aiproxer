//go:build windows

package siwc

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// The advisory lock on Windows.
//
// The kernel provides no flock(2), and the Windows equivalent is a byte
// range lock: LockFileEx takes an exclusive lock over a range of the
// file, and LOCKFILE_FAIL_IMMEDIATELY makes the attempt fail at once
// instead of waiting.
//
// A Windows byte-range lock is owned by the PROCESS, not by the handle.
// Two properties follow, and the package relies on the second one:
//
//   - the lock is released when the PROCESS ends, and when the process
//     closes a handle to the file, so a connector the host stops does not
//     block its registration forever; and
//   - a second descriptor of the same file INSIDE the same process is
//     NOT a second holder of the lock: the locking process may open the
//     file a second time, and the second handle simply cannot reach the
//     locked region until that region is unlocked.
//
// The release-on-close property is the reason the package keeps ONE
// descriptor of the lock file open: closing a file that carries the
// process's outstanding locks makes the operating system release them, so
// closing a second descriptor would release the lock of the first.
//
// The property is therefore enforced by the store's in-process ADMISSION
// and not by the lock itself: the admission serializes the writers of one
// registration inside one process, and it keeps ONE descriptor of the lock
// file open in this store at a time.
//
// The admission is PER STORE, and the argument above assumes ONE store per
// process: two stores over the same directory in one process keep two
// descriptors open and defeat the property. A process-wide registry is NOT
// an option here.
//
// The lock is taken over the first byte of the file, and the same range is
// released. The range is the same in every holder, so two PROCESSES
// conflict.

// lockState is the platform state of a held lock.
//
// The OVERLAPPED structure carries the range that was locked, and the
// release must name the same range.
type lockState struct {
	overlapped windows.Overlapped
}

// tryLockFile takes the exclusive lock of the file without waiting.
//
// It reports whether the lock was taken. A failure that is not a conflict
// is an error: the caller must not treat an unusable lock as a free one.
func tryLockFile(file *os.File) (lockState, bool, error) {
	state := lockState{}
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&state.overlapped,
	)
	switch {
	case err == nil:
		return state, true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return lockState{}, false, nil
	default:
		return lockState{}, false, err
	}
}

// unlockFile releases the lock of the file.
func unlockFile(file *os.File, state lockState) error {
	return windows.UnlockFileEx(
		windows.Handle(file.Fd()),
		0,
		1,
		0,
		&state.overlapped,
	)
}
