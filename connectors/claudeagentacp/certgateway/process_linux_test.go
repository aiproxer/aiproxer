//go:build linux

package certgateway

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connectors/claudeagentacp/internal/product"
)

// A detached writer is ready before its parent exits, but cannot write until
// released. Certification must reap it before returning, not race TempDir cleanup.
func TestCertGateway_ProcessOwnerReapsDetachedWriter(t *testing.T) {
	for _, mode := range []string{"parent", "failed-parent"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			gate := filepath.Join(dir, "gate")
			if err := syscall.Mkfifo(gate, 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCertGateway_DetachedWriterHelper$", "--", mode, dir)
			out, err := certificationOutput(cmd, dir)
			if (err != nil) != (mode == "failed-parent") {
				t.Fatalf("fixture failed: %v: %s", err, out)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "pid"))
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			proc, err := os.FindProcess(pid)
			if err != nil {
				return // already extinct
			}
			defer func() { _ = proc.Release() }()
			if err := proc.Signal(syscall.Signal(0)); err == nil {
				_ = proc.Kill() // fixture-owned PID, bound by FindProcess
				t.Fatal("certification returned with a detached writer still alive")
			}
			if _, err := os.Stat(filepath.Join(dir, "late-write")); !os.IsNotExist(err) {
				t.Fatal("detached writer escaped the barrier")
			}
		})
	}
}

func TestCertGateway_DetachedWriterHelper(t *testing.T) {
	i := 0
	for ; i < len(os.Args) && os.Args[i] != "--"; i++ {
	}
	if i+2 >= len(os.Args) {
		return
	}
	mode, dir := os.Args[i+1], os.Args[i+2]
	if mode == "child" {
		ready := os.NewFile(3, "ready")
		_, _ = ready.Write([]byte{1})
		_ = ready.Close()
		gate, err := os.Open(filepath.Join(dir, "gate"))
		if err != nil {
			os.Exit(2)
		}
		_, _ = io.ReadAll(gate)
		_ = gate.Close()
		_ = os.WriteFile(filepath.Join(dir, "late-write"), []byte("escaped"), 0o600)
		os.Exit(0)
	}
	r, w, err := os.Pipe()
	if err != nil {
		os.Exit(2)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCertGateway_DetachedWriterHelper$", "--", "child", dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.ExtraFiles = []*os.File{w}
	if cmd.Start() != nil {
		os.Exit(2)
	}
	_ = w.Close()
	_ = os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
	var ready [1]byte
	if _, err := io.ReadFull(r, ready[:]); err != nil {
		os.Exit(2)
	}
	if mode == "failed-parent" {
		os.Exit(3)
	}
	os.Exit(0)
}

// The helper is a separate subreaper; never make the Go test runner a shared
// reaping authority for concurrent tests or other connector instances.
func TestCertGateway_ProcessOwner(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) {
			if err := product.RunRuntimeSupervisor(os.Args[i+1:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
}

func certificationOutput(cmd *exec.Cmd, workdir string) ([]byte, error) {
	args := append([]string{"-test.run=^TestCertGateway_ProcessOwner$", "--"}, cmd.Args...)
	cmd.Path, cmd.Args = os.Args[0], append([]string{os.Args[0]}, args...)
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	// Keep the supervisor control outside the untrusted runtime's config root.
	for i, v := range slices.Backward(cmd.Env) {
		if strings.HasPrefix(v, "AIP_CLAUDE_ACP_SHUTDOWN_FILE=") {
			cmd.Env = append(cmd.Env[:i], cmd.Env[i+1:]...)
		}
	}
	cmd.Env = append(cmd.Env, "AIP_CLAUDE_ACP_SHUTDOWN_FILE="+filepath.Join(workdir, "supervisor-state"))
	// CommandContext's default SIGKILL would kill the reaper before cleanup.
	// Give its existing signal path the bounded WaitDelay window instead.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	return cmd.CombinedOutput()
}
