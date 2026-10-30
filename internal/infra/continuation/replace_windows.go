//go:build windows

package continuation

import (
	"golang.org/x/sys/windows"
)

func replaceFile(from, to string) error {
	source, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	destination, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(source, destination, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// Windows does not expose a portable directory fsync through the Go API.
// MoveFileEx(...WRITE_THROUGH) provides the available rename durability.
func syncDirectory(string) error { return nil }
