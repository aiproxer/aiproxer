//go:build linux

package acp

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// errOwnerStillRunning models a lifecycle owner that never completes its join
// on its own: the join only finishes after the owner was asked to stop.
var errOwnerStillRunning = errors.New("acp: owner is still running")

// liveInstanceProcess is a lifecycle owner whose process instance is still
// alive. Joining it before a shutdown request models an owner that blocks
// retirement forever.
type liveInstanceProcess struct {
	cmd     *exec.Cmd
	stdin   *io.PipeWriter
	stdout  *io.PipeReader
	stderr  *io.PipeReader
	mu      sync.Mutex
	stopped bool
}

func newLiveInstanceProcess(t *testing.T, cmd *exec.Cmd) *liveInstanceProcess {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	t.Cleanup(func() {
		_ = inR.Close()
		_ = inW.Close()
		_ = outR.Close()
		_ = outW.Close()
		_ = errR.Close()
		_ = errW.Close()
	})
	return &liveInstanceProcess{cmd: cmd, stdin: inW, stdout: outR, stderr: errR}
}

func (p *liveInstanceProcess) PID() int              { return p.cmd.Process.Pid }
func (p *liveInstanceProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *liveInstanceProcess) Stdout() io.ReadCloser { return p.stdout }
func (p *liveInstanceProcess) Stderr() io.ReadCloser { return p.stderr }
func (p *liveInstanceProcess) WaitCleanup() error    { return p.Wait() }
func (p *liveInstanceProcess) WasStopped() bool      { p.mu.Lock(); defer p.mu.Unlock(); return p.stopped }

func (p *liveInstanceProcess) Wait() error {
	p.mu.Lock()
	stopped := p.stopped
	p.mu.Unlock()
	if !stopped {
		return errOwnerStillRunning
	}
	return p.cmd.Wait()
}

func (p *liveInstanceProcess) Kill() error {
	p.mu.Lock()
	p.stopped = true
	p.mu.Unlock()
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}

// A replaced or unlinked executable changes the executable key while the
// process instance — same PID, same start time — stays the same. Retirement
// must ask that live instance to stop before joining it, otherwise a healthy
// supervisor blocks the join and the pool never finishes closing.
func TestRuntimePool_IdentityMismatchStopsLiveInstanceBeforeJoining(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "acp-agent")
	source, err := os.ReadFile("/bin/sleep")
	if err != nil {
		t.Skipf("sleep binary unavailable: %v", err)
	}
	if err := os.WriteFile(binary, source, 0o755); err != nil {
		t.Fatalf("write agent binary: %v", err)
	}
	cmd := exec.Command(binary, "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start agent: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	proc := newLiveInstanceProcess(t, cmd)

	pool := NewRuntimePool(RuntimePoolConfig{})
	key := RuntimeKey{ClientSession: "replaced-executable"}
	pool.SetProcess(key, proc, nil, "session", "", "")

	// Unlinking the running executable changes /proc/<pid>/exe while the
	// process instance itself stays alive.
	if err := os.Remove(binary); err != nil {
		t.Fatalf("remove agent binary: %v", err)
	}

	if err := pool.KillRuntime(key); err != nil {
		t.Fatalf("KillRuntime error = %v", err)
	}
	if !proc.WasStopped() {
		t.Error("live owner was joined without a shutdown request")
	}
}
