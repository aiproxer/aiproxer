//go:build windows

package processhost

import (
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsPipeConn_CloseRaceUnderIO(t *testing.T) {
	t.Parallel()
	name := `\\.\pipe\test-close-race-pipe`
	hServer, err := windows.CreateNamedPipe(
		windows.StringToUTF16Ptr(name),
		windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT,
		1, 4096, 4096, 0, nil,
	)
	if err != nil {
		t.Fatalf("CreateNamedPipe: %v", err)
	}

	hClient, err := windows.CreateFile(
		windows.StringToUTF16Ptr(name),
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OVERLAPPED, 0,
	)
	if err != nil {
		_ = windows.CloseHandle(hServer)
		t.Fatalf("CreateFile: %v", err)
	}

	serverConn := newPipeConn(hServer, name)
	clientConn := newPipeConn(hClient, name)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		buf := make([]byte, 256)
		for {
			_, err := serverConn.Read(buf)
			if err != nil {
				break
			}
		}
	}()

	go func() {
		defer wg.Done()
		data := []byte("hello world payload for pipe test")
		for {
			_, err := clientConn.Write(data)
			if err != nil {
				break
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)

	closeDone := make(chan struct{})
	go func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
		close(closeDone)
	}()

	select {
	case <-closeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Windows pipeConn.Close() deadlocked or hung under active I/O")
	}

	wg.Wait()
}

// TestWindowsPipeConn_DeadlineMethods documents that SetDeadline,
// SetReadDeadline, and SetWriteDeadline return a non-nil error because named
// pipe I/O is bounded by context cancellation / Close(), not by net.Conn
// deadline semantics. gRPC uses context-per-RPC cancellation so this is
// intentional. The test also asserts that Close() unblocks a stalled Read
// within a bounded window, which is the actual teardown mechanism.
func TestWindowsPipeConn_DeadlineMethods(t *testing.T) {
	t.Parallel()
	name := `\\.\pipe\test-deadline-methods-pipe`
	hServer, err := windows.CreateNamedPipe(
		windows.StringToUTF16Ptr(name),
		windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT,
		1, 4096, 4096, 0, nil,
	)
	if err != nil {
		t.Fatalf("CreateNamedPipe: %v", err)
	}

	hClient, err := windows.CreateFile(
		windows.StringToUTF16Ptr(name),
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OVERLAPPED, 0,
	)
	if err != nil {
		_ = windows.CloseHandle(hServer)
		t.Fatalf("CreateFile: %v", err)
	}

	conn := newPipeConn(hServer, name)
	clientConn := newPipeConn(hClient, name)
	defer func() { _ = clientConn.Close() }()

	// Deadline methods must return non-nil to signal unsupported (known limitation).
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err == nil {
		_ = conn.Close()
		t.Fatal("SetDeadline: expected non-nil error, got nil")
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err == nil {
		_ = conn.Close()
		t.Fatal("SetReadDeadline: expected non-nil error, got nil")
	}
	if err := conn.SetWriteDeadline(time.Now().Add(time.Second)); err == nil {
		_ = conn.Close()
		t.Fatal("SetWriteDeadline: expected non-nil error, got nil")
	}

	// Close() is the actual mechanism to interrupt stalled I/O.
	readDone := make(chan error, 1)
	go func() {
		buf := make([]byte, 256)
		_, err := conn.Read(buf)
		readDone <- err
	}()
	time.Sleep(20 * time.Millisecond)

	closeDone := make(chan struct{})
	go func() { _ = conn.Close(); close(closeDone) }()

	select {
	case <-closeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Close() did not complete within 3s while a Read was pending")
	}
	select {
	case err := <-readDone:
		if err == nil {
			t.Error("Read returned nil error after Close(), expected non-nil")
		}
	case <-time.After(time.Second):
		t.Error("pending Read did not unblock within 1s after Close()")
	}
}
