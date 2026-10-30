//go:build windows

package processhost

import (
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"
)

type pipeAddr string

func (a pipeAddr) Network() string { return "pipe" }
func (a pipeAddr) String() string  { return string(a) }

type pipeConn struct {
	handle  windows.Handle
	name    string
	closed  atomic.Bool
	closeMu sync.Mutex
	wg      sync.WaitGroup
}

func newPipeConn(h windows.Handle, name string) *pipeConn {
	_ = windows.SetFileCompletionNotificationModes(h, windows.FILE_SKIP_COMPLETION_PORT_ON_SUCCESS|windows.FILE_SKIP_SET_EVENT_ON_HANDLE)
	return &pipeConn{handle: h, name: name}
}

func (c *pipeConn) asyncIO(prepare func(ov *windows.Overlapped) (uint32, error)) (int, error) {
	// Keep closeMu held until the overlapped operation has been submitted. If
	// Close wins between wg.Add and ReadFile/WriteFile, CancelIoEx could run
	// before Windows has registered the operation and Close would then wait
	// forever on an I/O that was never cancelled.
	c.closeMu.Lock()
	if c.closed.Load() {
		c.closeMu.Unlock()
		return 0, net.ErrClosed
	}
	c.wg.Add(1)
	defer c.wg.Done()

	hEvent, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		c.closeMu.Unlock()
		return 0, err
	}
	defer func() { _ = windows.CloseHandle(hEvent) }()
	ov := windows.Overlapped{HEvent: hEvent}
	n, err := prepare(&ov)
	c.closeMu.Unlock()
	if err == nil {
		if n == 0 {
			return 0, io.EOF
		}
		return int(n), nil
	}
	if err != windows.ERROR_IO_PENDING {
		if err == windows.ERROR_BROKEN_PIPE || err == windows.ERROR_NO_DATA {
			return 0, io.EOF
		}
		return 0, err
	}
	s, werr := windows.WaitForSingleObject(hEvent, windows.INFINITE)
	if werr != nil {
		_ = windows.CancelIoEx(c.handle, &ov)
		return 0, werr
	}
	if s != windows.WAIT_OBJECT_0 {
		_ = windows.CancelIoEx(c.handle, &ov)
		return 0, fmt.Errorf("wait status %d", s)
	}
	var done uint32
	if err := windows.GetOverlappedResult(c.handle, &ov, &done, false); err != nil {
		if err == windows.ERROR_BROKEN_PIPE || err == windows.ERROR_NO_DATA {
			return int(done), io.EOF
		}
		return int(done), err
	}
	if done == 0 {
		return 0, io.EOF
	}
	return int(done), nil
}

func (c *pipeConn) Read(b []byte) (int, error) {
	return c.asyncIO(func(ov *windows.Overlapped) (uint32, error) {
		var n uint32
		err := windows.ReadFile(c.handle, b, &n, ov)
		return n, err
	})
}

func (c *pipeConn) Write(b []byte) (int, error) {
	return c.asyncIO(func(ov *windows.Overlapped) (uint32, error) {
		var n uint32
		err := windows.WriteFile(c.handle, b, &n, ov)
		return n, err
	})
}

func (c *pipeConn) Close() error {
	c.closeMu.Lock()
	if c.closed.Swap(true) {
		c.closeMu.Unlock()
		return nil
	}
	_ = windows.CancelIoEx(c.handle, nil)
	c.closeMu.Unlock()

	c.wg.Wait()

	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	if c.handle == 0 {
		return nil
	}
	err := windows.CloseHandle(c.handle)
	c.handle = 0
	return err
}

func (c *pipeConn) LocalAddr() net.Addr           { return pipeAddr(c.name) }
func (c *pipeConn) RemoteAddr() net.Addr          { return pipeAddr(c.name) }
func (c *pipeConn) SetDeadline(t time.Time) error { return fmt.Errorf("pipe: deadlines unsupported") }

func (c *pipeConn) SetReadDeadline(t time.Time) error {
	return fmt.Errorf("pipe: deadlines unsupported")
}

func (c *pipeConn) SetWriteDeadline(t time.Time) error {
	return fmt.Errorf("pipe: deadlines unsupported")
}

func (c *pipeConn) Handle() windows.Handle { return c.handle }
