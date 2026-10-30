//go:build linux

package processhost

import (
	"errors"
	"io"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"
)

// gateReader blocks the exec stdin copier until release is closed. Holding the
// copier keeps Cmd.Wait inside awaitGoroutines, so the first Wait caller cannot
// finish the reap until the test releases it.
type gateReader struct{ release <-chan struct{} }

func (r gateReader) Read([]byte) (int, error) {
	<-r.release
	return 0, io.EOF
}

// TestLinuxProcWait_ConcurrentCallersShareOneReap proves linuxProc.Wait is
// exactly-once for concurrent callers, matching windowsProc. Two callers are
// parked on a gate, so one of them owns the single Cmd.Wait while the child is
// still unreaped; the other must block for that same reap result instead of
// observing the not-yet-written wait error (pre-fix: nil) or issuing a second
// Cmd.Wait ("exec: Wait was already called").
//
// GOMAXPROCS(1) serializes the two callers so the pre-fix failure is the
// deterministic "second caller returns the unset error" schedule rather than a
// double-reap race. The bounded observe window below only shortens the post-fix
// path; every collected result is still compared, so it cannot mask a
// mismatch.
func TestLinuxProcWait_ConcurrentCallersShareOneReap(t *testing.T) {
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)

	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()

	cmd := exec.Command("/bin/sh", "-c", "exit 7")
	// Mirror PlatformLauncher: the child leads its own process group so
	// SignalKill targets only the child, never the test's own group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = gateReader{release: release}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	p := &linuxProc{cmd: cmd, gen: 1, pgid: cmd.Process.Pid}
	defer func() { _ = p.Close() }()

	gate := make(chan struct{})
	results := make(chan error, 2)
	var waiters sync.WaitGroup
	waiters.Add(2)
	for range 2 {
		go func() {
			defer waiters.Done()
			<-gate
			results <- p.Wait()
		}()
	}
	close(gate)

	var got []error
	select {
	case err := <-results:
		got = append(got, err)
	case <-time.After(250 * time.Millisecond):
	}
	unblock()
	waiters.Wait()
	close(results)
	for err := range results {
		got = append(got, err)
	}

	if len(got) != 2 {
		t.Fatalf("collected %d wait results, want 2", len(got))
	}
	for i, err := range got {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
			t.Errorf("wait result %d = %v, want exit status 7", i, err)
		}
	}
	if got[0] != got[1] {
		t.Errorf("concurrent Wait results differ: %v vs %v", got[0], got[1])
	}
}
