//go:build linux

package product

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"golang.org/x/sys/unix"
)

// RunRuntimeSupervisor is an internal executable mode, one per runtime. Linux
// adopts orphaned SDK children here even when they use detached process groups.
// No other runtime shares this subreaper or its child-reaping authority.
func RunRuntimeSupervisor(args []string) error {
	return runRuntimeSupervisor(args, supervisorOps{children: runtimeChildren, kill: killRuntimeChild}, time.Second)
}

type supervisorOps struct {
	children func() ([]int, error)
	kill     func(int) error
}

func runRuntimeSupervisor(args []string, ops supervisorOps, window time.Duration) error {
	failed := errors.New("claudeagentacp: runtime supervisor failed")
	marker := os.Getenv("AIP_CLAUDE_ACP_SHUTDOWN_FILE")
	if len(args) == 0 || !filepath.IsAbs(args[0]) || !filepath.IsAbs(marker) {
		return failed
	}
	if unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) != nil {
		return failed
	}
	// Read every actual task children file before launching tool-capable code.
	// Glob hides filesystem errors, so it is deliberately not used here.
	if _, err := ops.children(); err != nil {
		return failed
	}
	fd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		return failed
	}
	err = unix.PidfdSendSignal(fd, 0, nil, 0)
	_ = unix.Close(fd)
	if err != nil {
		return failed
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if os.WriteFile(marker, []byte("ready"), 0o600) != nil {
		return failed
	}
	cmd := exec.Command(args[0], args[1:]...)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "AIP_CLAUDE_ACP_SHUTDOWN_FILE=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if cmd.Start() != nil {
		return failed
	}
	killed := make(chan struct{})
	cancelKill := context.AfterFunc(ctx, func() { defer close(killed); _ = cmd.Process.Kill() })
	childErr := cmd.Wait()
	if !cancelKill() {
		<-killed
	}
	for reapRuntimeChildren(ops, window) != nil {
		// Losing this owner would reparent detached children outside our
		// authority. Retain ownership and retry; never acknowledge or exit on
		// enumeration/signalling errors or timeout. The parent reports a
		// pending retirement and retains the configuration root throughout.
		_, _ = io.WriteString(os.Stderr, "claudeagentacp: descendant cleanup incomplete; retaining supervisor\n")
		time.Sleep(100 * time.Millisecond)
	}
	// Only OS-confirmed extinction of this supervisor's child tree acknowledges
	// cleanup. SDK acknowledgements and task registry changes are not proof.
	// The file is advisory only. A zero exit status, observed through the
	// parent's own process handle, is the sole cleanup completion authority.
	_ = os.WriteFile(marker, []byte("stopped"), 0o600)
	if childErr != nil && ctx.Err() == nil {
		// The runtime exited on its own and unsuccessfully. The tree is
		// extinct, so this is a runtime failure and not an unconfirmed
		// cleanup; the parent must be able to tell the two apart.
		return ErrRuntimeFailed
	}
	return nil
}

func reapRuntimeChildren(ops supervisorOps, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		for {
			pid, err := unix.Wait4(-1, nil, unix.WNOHANG, nil)
			if errors.Is(err, unix.ECHILD) {
				return nil
			}
			if err != nil && !errors.Is(err, unix.EINTR) {
				return err
			}
			if pid <= 0 {
				break
			}
		}
		children, err := ops.children()
		if err != nil {
			return err
		}
		for _, pid := range children {
			if err := ops.kill(pid); err != nil && !errors.Is(err, unix.ESRCH) {
				return err
			}
		}
		if time.Now().After(deadline) {
			return errors.New("claudeagentacp: runtime descendants did not terminate")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func runtimeChildren() ([]int, error) {
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return nil, err
	}
	var children []int
	read := false
	for _, task := range tasks {
		data, err := os.ReadFile(filepath.Join("/proc/self/task", task.Name(), "children"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		} // task exited during inspection
		if err != nil {
			return nil, err
		}
		read = true
		for value := range strings.FieldsSeq(string(data)) {
			pid, err := strconv.Atoi(value)
			if err != nil {
				return nil, err
			}
			children = append(children, pid)
		}
	}
	if !read {
		return nil, errors.New("claudeagentacp: child inspection unavailable")
	}
	return children, nil
}

func killRuntimeChild(pid int) error {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return err
	}
	end := strings.LastIndexByte(string(stat), ')')
	fields := strings.Fields(string(stat)[end+1:])
	if end < 0 || len(fields) < 2 || fields[1] != strconv.Itoa(os.Getpid()) {
		return errors.New("claudeagentacp: descendant parentage unverified")
	}
	return unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
}

type supervisedProcess struct {
	acp.Process
	input   *supervisorInput
	wait    sync.Once
	waitErr error
}

func (p *supervisedProcess) Stdin() io.WriteCloser { return p.input }
func (p *supervisedProcess) Kill() error           { return p.input.Close() }
func (p *supervisedProcess) Wait() error {
	p.wait.Do(func() {
		timer := time.AfterFunc(2*time.Second, func() { slog.Error("claudeagentacp: cleanup incomplete; retaining runtime owner and state") })
		defer timer.Stop()
		if err := p.Process.Wait(); err != nil {
			if isRuntimeFailedExit(err) {
				// The supervisor confirmed extinction and reported that the
				// runtime itself failed. The tree is gone, so this is a runtime
				// failure and not an unconfirmed cleanup.
				p.waitErr = ErrRuntimeFailed
				slog.Error("claudeagentacp: supervised runtime exited unsuccessfully", "error", err)
			} else {
				p.waitErr = acp.ErrCleanupIncomplete
				slog.Error("claudeagentacp: supervisor exit did not confirm extinction; retaining state", "error", err)
			}
		}
		if p.input.process != nil {
			_ = p.input.process.Release()
		}
	})
	return p.waitErr
}

// isRuntimeFailedExit reports whether the supervisor exited with the runtime
// failure code. The supervisor uses a distinct code for a runtime that exited
// unsuccessfully, so the parent can separate that outcome from a supervisor
// that could not confirm extinction.
func isRuntimeFailedExit(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	return exitErr.ExitCode() == SupervisorRuntimeFailedExit
}

type supervisorInput struct {
	io.WriteCloser
	process *os.Process // bound at spawn, never resolve a numeric PID during cleanup
	once    sync.Once
	err     error
}

func (w *supervisorInput) Close() error {
	w.once.Do(func() {
		if w.process != nil {
			w.err = w.process.Signal(syscall.SIGTERM)
			if errors.Is(w.err, os.ErrProcessDone) {
				w.err = nil
			}
		}
		_ = w.WriteCloser.Close()
	})
	return w.err
}

func wrapSupervisorProcess(proc acp.Process, _ string) acp.Process {
	if proc.PID() <= 0 {
		return proc
	} // in-memory peers have no OS lifecycle
	process, _ := os.FindProcess(proc.PID())
	return &supervisedProcess{Process: proc, input: &supervisorInput{WriteCloser: proc.Stdin(), process: process}}
}
