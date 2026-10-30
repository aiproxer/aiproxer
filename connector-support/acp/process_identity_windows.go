//go:build windows

package acp

import (
	"syscall"
	"time"
	"unsafe"
)

var (
	modKernel32                   = syscall.NewLazyDLL("kernel32.dll")
	procOpenProcess               = modKernel32.NewProc("OpenProcess")
	procGetProcessTimes           = modKernel32.NewProc("GetProcessTimes")
	procQueryFullProcessImageName = modKernel32.NewProc("QueryFullProcessImageNameW")
	procCloseHandle               = modKernel32.NewProc("CloseHandle")
)

const (
	processQueryInformation = 0x0400
	processVMRead           = 0x0010
)

// platformProcessStartTime returns the process creation time on Windows using
// GetProcessTimes. Best-effort: returns zero on error.
func platformProcessStartTime(pid int) time.Time {
	if pid <= 0 {
		return time.Time{}
	}
	handle, _, _ := procOpenProcess.Call(
		uintptr(processQueryInformation),
		uintptr(0),
		uintptr(pid),
	)
	if handle == 0 {
		return time.Time{}
	}
	defer func() { _, _, _ = procCloseHandle.Call(handle) }()

	var creationTime, exitTime, kernelTime, userTime syscall.Filetime
	ret, _, _ := procGetProcessTimes.Call(
		handle,
		uintptr(unsafe.Pointer(&creationTime)),
		uintptr(unsafe.Pointer(&exitTime)),
		uintptr(unsafe.Pointer(&kernelTime)),
		uintptr(unsafe.Pointer(&userTime)),
	)
	if ret == 0 {
		return time.Time{}
	}
	// Filetime is 100-nanosecond intervals since January 1, 1601.
	// Convert to time.Time via syscall.Filetime.Nanoseconds() → Unix epoch.
	return time.Unix(0, creationTime.Nanoseconds())
}

// platformProcessExePath returns the executable path for pid on Windows using
// QueryFullProcessImageName.
func platformProcessExePath(pid int) string {
	if pid <= 0 {
		return ""
	}
	handle, _, _ := procOpenProcess.Call(
		uintptr(processQueryInformation|processVMRead),
		uintptr(0),
		uintptr(pid),
	)
	if handle == 0 {
		return ""
	}
	defer func() { _, _, _ = procCloseHandle.Call(handle) }()

	var buf [1024]uint16
	size := uint32(len(buf))
	ret, _, _ := procQueryFullProcessImageName.Call(
		handle,
		uintptr(0),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if ret == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:size])
}
