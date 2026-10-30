//go:build windows

package processhost

import (
	"errors"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// errNoChildHandles is returned when the capture has no child handles to
// hand to the connector.
var errNoChildHandles = errors.New("processhost: no child handles")

// windowsChildCapture owns the host-side capture of a launched connector's
// standard streams.
//
// The connector writes into host-owned pipes and the host drains them under
// the diagnostic budget, so a connector that floods the host is charged and
// terminated instead of running unbounded.
//
// The host-side handles are not inheritable. Go's os.Pipe marks both ends of
// a pipe inheritable, so an unrelated process created with
// bInheritHandles=TRUE and no handle list would inherit them: it could read
// the connector's output, and holding the write end would delay the pipe's
// end of file for the whole life of the connector. Only the duplicated
// handles handed to the child are inheritable, and only for the duration of
// the process creation.
//
// The parent releases its copies of the child's stream handles once the child
// exists, so the host observes the end of the connector's output when the
// connector exits.
type windowsChildCapture struct {
	// stdout and stderr are the read ends the host drains.
	stdout *os.File
	stderr *os.File
	// stdoutW and stderrW are the write ends the parent holds until the
	// child exists.
	stdoutW *os.File
	stderrW *os.File
	// stdin is the child's standard input: the null device, so the
	// connector reads end of file instead of the host's console.
	stdin *os.File

	// childStdin, childStdout and childStderr are the inheritable
	// duplicates handed to the child. They exist only between the
	// duplication and the process creation.
	childStdin  windows.Handle
	childStdout windows.Handle
	childStderr windows.Handle

	// attrHandles is the backing storage of the inherited-handle list
	// passed to UpdateProcThreadAttribute. The Windows API retains the
	// pointer without copying it, so the slice must outlive the attribute
	// list: it is stored here, not in a local that returns before
	// CreateProcess runs.
	attrHandles []windows.Handle
}

// newWindowsChildCapture creates the host-owned pipes for one connector.
//
// The pipes are created non-inheritable from the outset: an unrelated
// process created with bInheritHandles=TRUE and no handle list must never
// inherit the host's copy of the connector's output. Only the duplicated
// handles handed to the child are inheritable, and only for the duration of
// the process creation.
func newWindowsChildCapture() (*windowsChildCapture, error) {
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		return nil, err
	}
	stdoutR, stdoutW, err := newNonInheritablePipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	stderrR, stderrW, err := newNonInheritablePipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return nil, err
	}
	c := &windowsChildCapture{
		stdout:  stdoutR,
		stderr:  stderrR,
		stdoutW: stdoutW,
		stderrW: stderrW,
		stdin:   stdin,
	}
	return c, nil
}

// newNonInheritablePipe creates a connected pair of files whose handles are
// not inheritable.
//
// Go's os.Pipe marks both ends inheritable, which leaves a window between the
// creation and the clearing of the inherit flag during which an unrelated
// process could inherit the handle. Creating the pipe with a nil security
// descriptor makes both handles private from the start; only explicit
// duplicates cross the process boundary.
func newNonInheritablePipe() (*os.File, *os.File, error) {
	var r, w windows.Handle
	// A nil descriptor creates non-inheritable handles.
	if err := windows.CreatePipe(&r, &w, nil, 0); err != nil {
		return nil, nil, err
	}
	rf := os.NewFile(uintptr(r), "|0")
	if rf == nil {
		_ = windows.CloseHandle(r)
		_ = windows.CloseHandle(w)
		return nil, nil, errNoChildHandles
	}
	wf := os.NewFile(uintptr(w), "|1")
	if wf == nil {
		_ = rf.Close()
		_ = windows.CloseHandle(w)
		return nil, nil, errNoChildHandles
	}
	return rf, wf, nil
}

// prepareChildHandles duplicates the stream handles into inheritable handles
// the child receives.
//
// The duplicates are the only inheritable handles this launch owns, and they
// exist only until the process creation returns. A concurrent process created
// in that window with bInheritHandles=TRUE and no handle list could still
// inherit them; the window is the single CreateProcess call.
func (c *windowsChildCapture) prepareChildHandles() error {
	for _, pair := range []struct {
		src windows.Handle
		dst *windows.Handle
	}{
		{windows.Handle(c.stdin.Fd()), &c.childStdin},
		{windows.Handle(c.stdoutW.Fd()), &c.childStdout},
		{windows.Handle(c.stderrW.Fd()), &c.childStderr},
	} {
		dup, err := duplicateInheritable(pair.src)
		if err != nil {
			return err
		}
		*pair.dst = dup
	}
	return nil
}

// inheritedHandles returns the handles the child inherits. Only these handles
// cross the process boundary.
func (c *windowsChildCapture) inheritedHandles() []windows.Handle {
	return []windows.Handle{c.childStdin, c.childStdout, c.childStderr}
}

// releaseChildEnds releases the parent's copies of the child's stream
// handles. The child keeps its own copies, so the host observes the end of
// the connector's output when the connector exits.
func (c *windowsChildCapture) releaseChildEnds() {
	for _, h := range []windows.Handle{c.childStdin, c.childStdout, c.childStderr} {
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
	}
	c.childStdin, c.childStdout, c.childStderr = 0, 0, 0
	if c.stdin != nil {
		_ = c.stdin.Close()
		c.stdin = nil
	}
	if c.stdoutW != nil {
		_ = c.stdoutW.Close()
		c.stdoutW = nil
	}
	if c.stderrW != nil {
		_ = c.stderrW.Close()
		c.stderrW = nil
	}
}

// close releases every handle the capture owns. It is the failure path: a
// launched connector keeps the read ends until the host drains them.
func (c *windowsChildCapture) close() {
	c.releaseChildEnds()
	if c.stdout != nil {
		_ = c.stdout.Close()
		c.stdout = nil
	}
	if c.stderr != nil {
		_ = c.stderr.Close()
		c.stderr = nil
	}
}

// duplicateInheritable duplicates a handle into an inheritable handle. The
// duplicate is what the child receives; the original stays private.
func duplicateInheritable(src windows.Handle) (windows.Handle, error) {
	var dup windows.Handle
	if err := windows.DuplicateHandle(
		windows.CurrentProcess(),
		src,
		windows.CurrentProcess(),
		&dup,
		0,
		true,
		windows.DUPLICATE_SAME_ACCESS,
	); err != nil {
		return 0, err
	}
	return dup, nil
}

// childStartupInfo builds the startup information that routes the child's
// standard streams into the captured pipes and restricts inheritance to them.
//
// The inherited-handle list is stored on the capture: UpdateProcThreadAttribute
// retains the pointer without copying the array, so a local slice that
// returns before CreateProcess runs would leave the attribute list pointing
// at memory the garbage collector may reclaim. The stored slice outlives the
// attribute list, and the cleanup releases the list before dropping it.
func (c *windowsChildCapture) childStartupInfo() (*windows.StartupInfoEx, func(), error) {
	attrList, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, nil, err
	}
	c.attrHandles = c.inheritedHandles()
	if len(c.attrHandles) == 0 {
		attrList.Delete()
		c.attrHandles = nil
		return nil, nil, errNoChildHandles
	}
	if err := attrList.Update(
		windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST,
		unsafe.Pointer(&c.attrHandles[0]),
		uintptr(len(c.attrHandles))*unsafe.Sizeof(c.attrHandles[0]),
	); err != nil {
		attrList.Delete()
		c.attrHandles = nil
		return nil, nil, err
	}
	si := &windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb:        uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
			Flags:     windows.STARTF_USESTDHANDLES,
			StdInput:  c.childStdin,
			StdOutput: c.childStdout,
			StdErr:    c.childStderr,
		},
		ProcThreadAttributeList: attrList.List(),
	}
	return si, func() {
		attrList.Delete()
		// The attribute list is gone, so the backing storage may be
		// released. Keep it alive through the Delete call itself.
		runtime.KeepAlive(c.attrHandles)
		c.attrHandles = nil
	}, nil
}
