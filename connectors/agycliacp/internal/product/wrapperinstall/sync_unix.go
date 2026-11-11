//go:build !windows

package wrapperinstall

import "os"

// syncDirectory flushes a directory entry update (rename) to stable storage.
// Without it the executable bytes are durable but the cache entry that names
// them may not survive a crash.
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
