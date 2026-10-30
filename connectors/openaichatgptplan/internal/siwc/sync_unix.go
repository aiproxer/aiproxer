//go:build !windows

package siwc

import "os"

// syncDirectory flushes a directory entry update (the publication of a
// record) to stable storage. Without it the record bytes are durable but
// the directory entry that names them may not survive a crash.
//
// The mechanism needs two things from the filesystem, and both are stated
// here because their absence is an explicit failure rather than a silent
// weakening. The directory must be openable for reading, so an ancestor
// that grants traversal but not reading - mode 0111, for example - makes
// the confirmation fail with a permission error, and the filesystem must
// support flushing a directory at all.
func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
