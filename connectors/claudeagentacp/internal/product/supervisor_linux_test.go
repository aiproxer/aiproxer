//go:build linux

package product

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"golang.org/x/sys/unix"
)

// The control peer is synthetic, but its detached process and the subreaper are
// real. A rejected, stuck, or prematurely acknowledged SDK control must never
// authorize a cleanup acknowledgement while that process is still alive.
func TestRuntimeSupervisor_ReapsDetachedChildrenDespiteStopControlFaults(t *testing.T) {
	for _, fault := range []string{"rejected", "pending", "early", "spoofed-marker", "enumeration", "pidfd", "reaper-timeout", "owner-exit"} {
		t.Run(fault, func(t *testing.T) {
			dir := t.TempDir()
			marker, pidFile, requested := filepath.Join(dir, "shutdown"), filepath.Join(dir, "pid"), filepath.Join(dir, "requested")
			script := filepath.Join(dir, "peer.sh")
			body := `setsid sh -c 'echo $$ > "$1"; exec sleep 300' sh "$1" &
read -r control
echo requested > "$2"
case "$3" in
pending) read -r never ;;
*) echo "$3" ;;
esac
wait
`
			if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			recoverFile := filepath.Join(dir, "recover")
			env := []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "AIP_SUPERVISOR_TEST_HELPER=1", "AIP_CLAUDE_ACP_SHUTDOWN_FILE=" + marker, "AIP_SUPERVISOR_OS_FAULT=" + fault, "AIP_SUPERVISOR_RECOVER=" + recoverFile}
			raw, err := (acp.OSProcessStarter{}).Start([]string{os.Args[0], "-test.run=^TestRuntimeSupervisor_Helper$", "--", "/bin/sh", script, pidFile, requested, fault}, dir, env)
			if err != nil {
				t.Fatal(err)
			}
			proc := wrapSupervisorProcess(raw, marker)
			root := filepath.Join(dir, "config")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			owned := &authProcess{Process: proc, root: root, shutdownFile: marker}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			timeoutDone := make(chan struct{})
			stop := context.AfterFunc(ctx, func() { defer close(timeoutDone); _ = raw.Kill() })
			defer func() {
				if !stop() {
					<-timeoutDone
				}
				cancel()
			}()
			done := false
			var waited chan error
			t.Cleanup(func() {
				if !done {
					_ = raw.Kill()
					if waited != nil {
						<-waited
					} else {
						_ = raw.Wait()
					}
				}
			})
			waitSupervisorFile(t, marker, "ready")
			waitSupervisorFile(t, pidFile, "")
			data, _ := os.ReadFile(pidFile)
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatal(err)
			}
			fd, err := unix.PidfdOpen(pid, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0); _ = unix.Close(fd) })
			if _, err := proc.Stdin().Write([]byte("stop\n")); err != nil {
				t.Fatal(err)
			}
			waitSupervisorFile(t, requested, "requested")
			if fault != "pending" {
				line, err := bufio.NewReader(proc.Stdout()).ReadString('\n')
				if err != nil || strings.TrimSpace(line) != fault {
					t.Fatalf("stop outcome %q: %v", line, err)
				}
			}
			if unix.PidfdSendSignal(fd, 0, nil, 0) != nil {
				t.Fatal("fault did not leave a live detached process")
			}
			if fault == "spoofed-marker" {
				if err := os.WriteFile(marker, []byte("stopped"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := owned.Kill(); err != nil {
					t.Fatal(err)
				}
			}
			if fault == "owner-exit" {
				_ = raw.Kill()
				if err := owned.Wait(); !errors.Is(err, acp.ErrCleanupIncomplete) {
					t.Errorf("unverified owner exit accepted: %v", err)
				}
				done = true
				if _, err := os.Stat(root); err != nil {
					t.Error("unverified cleanup deleted root", err)
				}
				return
			}
			if err := owned.Kill(); err != nil {
				t.Fatal(err)
			}
			waited = make(chan error, 1)
			go func() { waited <- owned.Wait() }()
			if fault == "enumeration" || fault == "pidfd" || fault == "reaper-timeout" {
				if file, ok := proc.Stderr().(*os.File); ok {
					_ = file.SetReadDeadline(time.Now().Add(2 * time.Second))
				}
				line, err := bufio.NewReader(proc.Stderr()).ReadString('\n')
				if err != nil || !strings.Contains(line, "cleanup incomplete") {
					t.Fatalf("missing cleanup-failure signal: %q %v", line, err)
				}
				if err := unix.Kill(raw.PID(), 0); err != nil {
					t.Fatal("supervisor abandoned ownership on reaper failure", err)
				}
				if err := unix.PidfdSendSignal(fd, 0, nil, 0); err != nil {
					t.Fatal("fault did not preserve live descendant", err)
				}
				if data, _ := os.ReadFile(marker); string(data) == "stopped" {
					t.Fatal("failure acknowledged as extinction")
				}
				if _, err := os.Stat(root); err != nil {
					t.Fatal("pending cleanup deleted root", err)
				}
				select {
				case err := <-waited:
					done = true
					t.Fatalf("cleanup returned while fault and descendant remain: %v", err)
				default:
				}
				if err := os.WriteFile(recoverFile, []byte("recover"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err = <-waited
			done = true
			if err != nil {
				t.Fatal(err)
			}
			if !errors.Is(unix.PidfdSendSignal(fd, 0, nil, 0), unix.ESRCH) {
				t.Fatal("detached child not terminated and reaped")
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatal("verified cleanup retained root")
			}
		})
	}
}

func TestRuntimeSupervisor_Helper(t *testing.T) {
	if os.Getenv("AIP_SUPERVISOR_TEST_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			ops := supervisorOps{children: runtimeChildren, kill: killRuntimeChild}
			fault := os.Getenv("AIP_SUPERVISOR_OS_FAULT")
			failing := func() bool { _, err := os.Stat(os.Getenv("AIP_SUPERVISOR_RECOVER")); return err != nil }
			preflight := true
			ops.children = func() ([]int, error) {
				if fault == "preflight" || (!preflight && fault == "enumeration" && failing()) {
					return nil, unix.EACCES
				}
				preflight = false
				return runtimeChildren()
			}
			ops.kill = func(pid int) error {
				if failing() {
					if fault == "pidfd" {
						return unix.EPERM
					}
					if fault == "reaper-timeout" {
						return nil
					} // signal succeeds but process remains alive
				}
				return killRuntimeChild(pid)
			}
			if runRuntimeSupervisor(os.Args[i+1:], ops, 20*time.Millisecond) != nil {
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	os.Exit(1)
}

func TestRuntimeSupervisor_PreflightFailureDoesNotLaunchRuntime(t *testing.T) {
	dir := t.TempDir()
	launched := filepath.Join(dir, "launched")
	proc, err := (acp.OSProcessStarter{}).Start([]string{os.Args[0], "-test.run=^TestRuntimeSupervisor_Helper$", "--", "/bin/sh", "-c", `touch "$1"`, "sh", launched}, dir, []string{"PATH=/usr/bin:/bin", "AIP_SUPERVISOR_TEST_HELPER=1", "AIP_SUPERVISOR_OS_FAULT=preflight", "AIP_CLAUDE_ACP_SHUTDOWN_FILE=" + filepath.Join(dir, "shutdown")})
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.Wait(); err == nil {
		t.Fatal("preflight error accepted")
	}
	if _, err := os.Stat(launched); !os.IsNotExist(err) {
		t.Fatal("runtime launched without child-inspection preflight")
	}
}

func waitSupervisorFile(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil && ((want == "" && len(data) > 0) || (want != "" && strings.TrimSpace(string(data)) == want)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("supervisor handshake deadline")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The supervisor's exit status separates a runtime that failed from a
// supervisor that could not confirm extinction. A runtime failure keeps the
// extinction proven, so it must not be reported as an unconfirmed cleanup.
func TestSupervisedProcess_SeparatesRuntimeFailureFromUnconfirmedCleanup(t *testing.T) {
	for _, tc := range []struct {
		name     string
		exitCode int
		want     error
	}{
		{"runtime failed", SupervisorRuntimeFailedExit, ErrRuntimeFailed},
		{"supervisor failed", 1, acp.ErrCleanupIncomplete},
		{"clean exit", 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			raw, err := (acp.OSProcessStarter{}).Start([]string{"/bin/sh", "-c", "exit " + strconv.Itoa(tc.exitCode)}, dir, []string{"PATH=/usr/bin:/bin"})
			if err != nil {
				t.Fatal(err)
			}
			proc := wrapSupervisorProcess(raw, filepath.Join(dir, "shutdown"))
			got := proc.Wait()
			if tc.want == nil {
				if got != nil {
					t.Fatalf("Wait() = %v, want nil", got)
				}
				return
			}
			if !errors.Is(got, tc.want) {
				t.Fatalf("Wait() = %v, want %v", got, tc.want)
			}
			if errors.Is(got, acp.ErrCleanupIncomplete) != errors.Is(tc.want, acp.ErrCleanupIncomplete) {
				t.Fatalf("Wait() = %v, cleanup classification wrong", got)
			}
		})
	}
}
