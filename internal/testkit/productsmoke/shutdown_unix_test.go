//go:build productsmoke && unix

package productsmoke

import (
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

const (
	// shutdownExitBound bounds the wait for the documented graceful-shutdown
	// signal to end the process.
	shutdownExitBound = 10 * time.Second
	// shutdownPortBound bounds the wait for the listening port to be released
	// after the process has exited.
	shutdownPortBound = 5 * time.Second
	// shutdownDialProbeBound bounds one pre-signal connection attempt that
	// proves the port was live before the exit.
	shutdownDialProbeBound = 2 * time.Second
)

// TestProduct_Shutdown covers the documented POSIX shutdown signal. It is
// POSIX-only in Required; the Windows required list excludes it.
func TestProduct_Shutdown(t *testing.T) {
	t.Run("graceful", testGracefulShutdown)
}

// testGracefulShutdown proves SIGTERM ends the process with exit code 0 within
// the bound and releases the listening port. Ordinary cleanup deliberately does
// not assert either; this is the only journey that does.
func testGracefulShutdown(t *testing.T) {
	t.Parallel()
	canary := newCanary(t)
	h := startSUT(t, sutConfig{Canary: canary})
	failures := failureContext{sut: h, provider: h.ReadyProvider(), wire: h.LastReadyWire()}

	// Prove the data-plane port is live before the signal, so "released" later
	// is an observed change rather than a port that never accepted anything.
	conn, err := net.DialTimeout("tcp", h.addr, shutdownDialProbeBound)
	if err != nil {
		failf(t, failures, "the data-plane port %s was not accepting connections before the shutdown signal: %v", h.addr, err)
	}
	_ = conn.Close()

	started := time.Now()
	if err := h.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		failf(t, failures, "send SIGTERM to the proxy process: %v", err)
	}
	select {
	case <-h.done:
	case <-time.After(shutdownExitBound):
		failf(t, failures, "the proxy did not exit within %s of SIGTERM, the documented graceful-shutdown signal", shutdownExitBound)
	}
	elapsed := time.Since(started)
	if h.exitErr != nil {
		code := "unknown"
		if exitErr, ok := errors.AsType[*exec.ExitError](h.exitErr); ok {
			code = strconv.Itoa(exitErr.ExitCode())
		}
		failf(t, failures, "the proxy exited non-zero after SIGTERM (exit code %s after %s): %v",
			code, elapsed.Round(time.Millisecond), h.exitErr)
	}
	if err := waitAddrReleased(h.addr, shutdownPortBound); err != nil {
		failf(t, failures, "after the graceful exit: %v", err)
	}

	summary := fmt.Sprintf("signal=SIGTERM exit_code=0 elapsed_ms=%d port=%s released=true\n", elapsed.Milliseconds(), h.addr)
	h.evidence.write("shutdown.txt", summary)
	t.Logf("graceful shutdown passed: exit 0 after %s, port %s released; evidence %s",
		elapsed.Round(time.Millisecond), h.addr, h.evidence.dir)
}

// waitAddrReleased proves addr's listening port is free: no client can connect
// to it and a new listener can bind it. Both conditions are required, so a
// socket that accepts but no longer serves cannot pass as released.
func waitAddrReleased(addr string, bound time.Duration) error {
	deadline := time.Now().Add(bound)
	var last error
	for {
		conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
		if err != nil {
			listener, listenErr := net.Listen("tcp", addr)
			if listenErr == nil {
				_ = listener.Close()
				return nil
			}
			last = listenErr
		} else {
			_ = conn.Close()
			last = fmt.Errorf("the proxy still accepts connections on %s", addr)
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("the listening port %s was not released within %s: %v", addr, bound, last)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
