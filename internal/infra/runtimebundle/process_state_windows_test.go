//go:build windows

package runtimebundle_test

import (
	"errors"
	"math"

	"golang.org/x/sys/windows"
)

func processAlive(pid int) bool {
	if pid <= 0 || uint64(pid) > math.MaxUint32 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			// Access denied indicates the process exists and is running under another security context.
			return true
		}
		return false
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	event, err := windows.WaitForSingleObject(handle, 0)
	if err == nil {
		switch event {
		case uint32(windows.WAIT_TIMEOUT):
			return true
		case windows.WAIT_OBJECT_0:
			return false
		}
	}

	var exitCode uint32
	if errExit := windows.GetExitCodeProcess(handle, &exitCode); errExit == nil {
		return exitCode == uint32(windows.STATUS_PENDING)
	}
	return false
}
