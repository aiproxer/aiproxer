//go:build windows

package processhost

import (
	"os"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procGetHandleInformation reads the inherit flag of a handle. The Go
// standard library and x/sys expose SetHandleInformation but not its reader,
// so the call is bound here.
var procGetHandleInformation = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetHandleInformation")

// TestWindowsChildCapture_ParentHandlesAreNotInheritable proves the
// host-side capture handles are not inheritable.
//
// Go's os.Pipe marks both ends of a pipe inheritable. The capture lives for
// the whole generation, so an inheritable host handle would leak into every
// unrelated process created with bInheritHandles=TRUE and no handle list:
// such a process could read the connector's output, and holding the write end
// would delay the pipe's end of file for the connector's lifetime.
//
// Only the duplicated handles handed to the child may be inheritable.
func TestWindowsChildCapture_ParentHandlesAreNotInheritable(t *testing.T) {
	capture, err := newWindowsChildCapture()
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	t.Cleanup(func() { capture.close() })

	for name, f := range map[string]*os.File{
		"stdin":        capture.stdin,
		"stdout":       capture.stdout,
		"stdout write": capture.stdoutW,
		"stderr":       capture.stderr,
		"stderr write": capture.stderrW,
	} {
		if f == nil {
			t.Fatalf("the capture has no %s handle", name)
		}
		if handleIsInheritable(t, windows.Handle(f.Fd())) {
			t.Fatalf("the host-side %s handle is inheritable", name)
		}
	}

	// The child duplicates are the only inheritable handles this launch
	// owns, and they exist only for the process creation.
	if err := capture.prepareChildHandles(); err != nil {
		t.Fatalf("child handles: %v", err)
	}
	for name, h := range map[string]windows.Handle{
		"child stdin":  capture.childStdin,
		"child stdout": capture.childStdout,
		"child stderr": capture.childStderr,
	} {
		if h == 0 {
			t.Fatalf("the capture has no %s handle", name)
		}
		if !handleIsInheritable(t, h) {
			t.Fatalf("the %s handle is not inheritable", name)
		}
	}
}

// handleIsInheritable reports whether the handle carries the inherit flag.
func handleIsInheritable(t *testing.T, h windows.Handle) bool {
	t.Helper()
	var flags uint32
	r, _, err := procGetHandleInformation.Call(uintptr(h), uintptr(unsafe.Pointer(&flags)))
	if r == 0 {
		t.Fatalf("GetHandleInformation(%v): %v", h, err)
	}
	return flags&windows.HANDLE_FLAG_INHERIT != 0
}

// TestWindowsChildCapture_AttrHandlesOutliveConstruction proves the
// inherited-handle list survives the garbage collector between attribute-list
// construction and native process creation.
//
// UpdateProcThreadAttribute retains the pointer without copying the array, so
// a local slice that returns before CreateProcess runs would leave the
// attribute list pointing at memory the collector may reclaim. (Go's collector
// is non-moving, so the failure mode is use-after-free: sporadic CreateProcess
// failures or corrupt inheritance under pressure, never a deterministic
// crash.) The capture therefore stores the backing array until the list is
// destroyed; this test drives collection and allocation pressure through the
// vulnerable interval and then verifies the stored array still aliases the
// handles the startup info names.
//
// The test is coupled to the retention mechanism by design: it names the
// stored field, so restoring the local-slice implementation fails the build
// instead of passing vacuously.
func TestWindowsChildCapture_AttrHandlesOutliveConstruction(t *testing.T) {
	capture, err := newWindowsChildCapture()
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	t.Cleanup(func() { capture.close() })

	if err := capture.prepareChildHandles(); err != nil {
		t.Fatalf("child handles: %v", err)
	}
	si, release, err := capture.childStartupInfo()
	if err != nil {
		t.Fatalf("startup info: %v", err)
	}
	if si.ProcThreadAttributeList == nil {
		t.Fatal("the startup info carries no attribute list")
	}

	// Collection and allocation pressure between construction and
	// consumption: without retained storage the list would point at
	// reclaimable memory here.
	for range 100 {
		_ = make([]byte, 1<<20)
		runtime.GC()
	}

	// The stored slice still aliases the child handles the startup info
	// names for the native call.
	if len(capture.attrHandles) != 3 {
		t.Fatalf("the capture retains %d handle-list entries, want 3", len(capture.attrHandles))
	}
	want := capture.inheritedHandles()
	for i := range want {
		if capture.attrHandles[i] != want[i] {
			t.Fatalf("handle-list entry %d is %v, want %v", i, capture.attrHandles[i], want[i])
		}
	}
	if si.StdInput != capture.childStdin || si.StdOutput != capture.childStdout || si.StdErr != capture.childStderr {
		t.Fatal("the startup info no longer names the retained child handles")
	}

	release()
	// The storage is released only after the list is destroyed.
	if capture.attrHandles != nil {
		t.Fatal("the handle-list storage was not released with the attribute list")
	}
}
