//go:build !windows

package continuation

import "os"

func replaceFile(from, to string) error { return os.Rename(from, to) }

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
