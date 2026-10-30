//go:build !windows

package oauthcred

import "os"

// syncDirectory flushes a directory entry update (rename or remove) to stable
// storage. Without it the credential bytes are durable but the directory entry
// that names them may not survive a crash.
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
