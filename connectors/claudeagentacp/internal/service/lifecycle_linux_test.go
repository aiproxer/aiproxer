//go:build linux

package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/connectors/claudeagentacp/internal/product"
	"golang.org/x/sys/unix"
)

// Requirement 5.4: run the existing ACP peer in a real process with a child,
// through the assembled engine and the production OS tree owner, not a mocked
// closer. Close must reap the direct process and leave no surviving child/root,
// even when its caller context has already expired.
func TestService_CloseKillsOwnedProcessTreeAndRoot(t *testing.T) {
	// Verify identity-bound observation/cleanup before launching any resources.
	operation := "pidfd_open"
	fd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err == nil {
		operation = "pidfd_send_signal"
		err = unix.PidfdSendSignal(fd, 0, nil, 0)
		if closeErr := unix.Close(fd); closeErr != nil {
			t.Fatalf("close preflight pidfd: %v", closeErr)
		}
	}
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) {
		t.Skipf("identity-bound process-tree verification unavailable: %s unsupported: %v", operation, err)
	}
	if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
		t.Skipf("identity-bound process-tree verification unavailable: %s permission denied: %v", operation, err)
	}
	if err != nil {
		t.Fatalf("%s preflight: %v", operation, err)
	}
	svc, _, _, _ := serviceFixture(t, "api_key")
	starter := &treeStarter{pidFile: filepath.Join(t.TempDir(), "child.pid")}
	svc.deps.starter = starter
	inst := configureFixture(t, svc, "api_key")
	stream, err := inst.eng.Open(t.Context(), turnCall())
	if err != nil {
		t.Fatal(err)
	}
	consume(t, stream)
	bytes, err := os.ReadFile(starter.pidFile)
	if err != nil {
		t.Fatal(err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(string(bytes)))
	if err != nil {
		t.Fatal(err)
	}
	// Bind failure cleanup while the peer still owns its live child. A pidfd
	// never targets a later process that reuses this numeric PID.
	childFD, err := unix.PidfdOpen(child, 0)
	if err != nil {
		t.Fatal(err)
	}
	reaped := false
	t.Cleanup(func() {
		if !reaped {
			_ = unix.PidfdSendSignal(childFD, unix.SIGKILL, nil, 0)
		}
		_ = unix.Close(childFD)
	})
	if unix.PidfdSendSignal(childFD, 0, nil, 0) != nil {
		t.Fatal("child never lived")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := inst.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(syscall.Kill(starter.proc.PID(), 0), syscall.ESRCH) {
		t.Fatal("direct process not reaped")
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for !errors.Is(unix.PidfdSendSignal(childFD, 0, nil, 0), syscall.ESRCH) {
		select {
		case <-deadline.C:
			t.Fatal("owned child not reaped")
		case <-tick.C:
		}
	}
	reaped = true
	requireRootGone(t, starter.env)
	if _, err := os.Stat(inst.eng.stateDir); !os.IsNotExist(err) {
		t.Fatal("instance state survived")
	}
}

// This substitutes only the executable peer; auth/root preparation, process
// group setup, ACP protocol and pool ownership remain the production path.
type treeStarter struct {
	pidFile string
	proc    acp.Process
	env     []string
}

func (s *treeStarter) Start(_ []string, cwd string, env []string) (acp.Process, error) {
	s.env = append([]string(nil), env...)
	proc, err := (acp.OSProcessStarter{}).Start([]string{os.Args[0], "-test.run=^TestServiceRuntimeSupervisor$", "--", os.Args[0], "-test.run=^TestServiceTreePeer$"}, cwd, append(append([]string(nil), env...), "AIP_TEST_TREE_PID="+s.pidFile))
	s.proc = proc
	return proc, err
}

func TestServiceRuntimeSupervisor(t *testing.T) {
	if os.Getenv("AIP_TEST_TREE_PID") == "" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			if product.RunRuntimeSupervisor(os.Args[i+1:]) != nil {
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	os.Exit(1)
}

func TestServiceTreePeer(t *testing.T) {
	path := os.Getenv("AIP_TEST_TREE_PID")
	if path == "" {
		return
	}
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		os.Exit(3)
	}
	peer := &adapterProcess{input: os.Stdin, output: os.Stdout, done: make(chan struct{})}
	peer.serve("api_key", false, false, false, false)
	os.Exit(0)
}
