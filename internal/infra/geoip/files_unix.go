//go:build !windows

package geoip

import "os"

func replaceFile(source, target string) error {
	return os.Rename(source, target)
}

func syncDirectory(directory string) error {
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
