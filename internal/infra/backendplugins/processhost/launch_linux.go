//go:build linux

package processhost

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"golang.org/x/sys/unix"
)

// Launch duplicates the held verified descriptor and executes it through a
// child ExtraFiles slot via /proc/self/fd/%d. The executable FD is appended
// after channel ExtraFiles so os/exec's FD-3+ remapping keeps channelChildFD
// stable and the exec path resolves to the verified bytes (not the channel).
// Shell is never used. Env must already be allowlisted by the supervisor.
func (PlatformLauncher) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	if spec.Artifact == nil || spec.Artifact.OpenFile() == nil {
		return nil, ReasonArtifactRequired
	}
	if spec.Artifact.Strategy != trust.BindingDescriptor {
		return nil, ReasonUnsupportedBinding
	}
	src := spec.Artifact.OpenFile()
	dupFD, err := unix.Dup(int(src.Fd()))
	if err != nil {
		return nil, fmt.Errorf("%w: dup: %v", ReasonLaunchFailed, err)
	}
	unix.CloseOnExec(dupFD)
	held := os.NewFile(uintptr(dupFD), "aip-bp-launch")
	if held == nil {
		_ = unix.Close(dupFD)
		return nil, fmt.Errorf("%w: newfile", ReasonLaunchFailed)
	}
	extras := append([]*os.File{}, spec.ExtraFiles...)
	// os/exec places ExtraFiles[i] at child FD 3+i. Append the executable last
	// so channel ExtraFiles[0] remains FD 3 (channelChildFD) while the exec
	// path targets the post-remap child FD for the held descriptor.
	execChildFD := 3 + len(extras)
	extras = append(extras, held)
	fdPath := "/proc/self/fd/" + strconv.Itoa(execChildFD)
	cmd := exec.CommandContext(ctx, fdPath)
	cmd.Dir = spec.WorkDir
	cmd.Env = append([]string{}, spec.Env...)
	cmd.ExtraFiles = extras
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = held.Close()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = held.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = held.Close()
		return nil, fmt.Errorf("%w: %v", ReasonLaunchFailed, err)
	}
	return &linuxProc{
		cmd: cmd, gen: spec.Generation, stdout: stdout, stderr: stderr,
		pgid: cmd.Process.Pid, held: held,
	}, nil
}

type linuxProc struct {
	cmd    *exec.Cmd
	gen    uint64
	stdout io.ReadCloser
	stderr io.ReadCloser
	pgid   int
	held   *os.File

	waitOnce sync.Once
	waitErr  error

	mu     sync.Mutex
	closed bool
}

func (p *linuxProc) PID() int                 { return p.cmd.Process.Pid }
func (p *linuxProc) Generation() uint64       { return p.gen }
func (p *linuxProc) Stdout() io.ReadCloser    { return p.stdout }
func (p *linuxProc) Stderr() io.ReadCloser    { return p.stderr }
func (p *linuxProc) ContainsPID(pid int) bool { return pid == p.PID() }
func (p *linuxProc) SignalKill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return unix.Kill(-p.pgid, unix.SIGKILL)
}

func (p *linuxProc) GracefulStop(timeout time.Duration) error {
	if p.cmd.Process == nil {
		return nil
	}
	_ = unix.Kill(-p.pgid, unix.SIGTERM)
	done := make(chan struct{})
	go func() {
		_ = p.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return p.waitErr
	case <-timer.C:
		_ = p.SignalKill()
		<-done
		return p.waitErr
	}
}

// Wait reaps the child exactly once; concurrent and repeated callers share the
// same result, matching windowsProc.
func (p *linuxProc) Wait() error {
	p.waitOnce.Do(func() {
		p.waitErr = p.cmd.Wait()
	})
	return p.waitErr
}

// Close is idempotent and safe against concurrent Wait callers: the reap is
// exactly-once and the held descriptor is released once.
func (p *linuxProc) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	_ = p.SignalKill()
	err := p.Wait()
	if p.held != nil {
		_ = p.held.Close()
		p.held = nil
	}
	return err
}
