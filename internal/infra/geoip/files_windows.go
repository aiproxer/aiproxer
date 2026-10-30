//go:build windows

package geoip

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func replaceFile(source, target string) error {
	sourcePtr, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return fmt.Errorf("encode source path: %w", err)
	}
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return fmt.Errorf("encode target path: %w", err)
	}
	return windows.MoveFileEx(
		sourcePtr,
		targetPtr,
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	)
}

// MoveFileEx with MOVEFILE_WRITE_THROUGH supplies the metadata durability
// primitive for the same-directory manifest replacement on Windows.
func syncDirectory(string) error { return nil }
