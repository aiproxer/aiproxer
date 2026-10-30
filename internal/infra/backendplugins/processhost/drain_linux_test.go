//go:build linux

package processhost_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	inframanifest "github.com/aiproxer/aiproxer/internal/infra/backendplugins/manifest"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"go.uber.org/goleak"
	"golang.org/x/sys/unix"
)

// TestHost_NativeLaunchShutdownReleasesDrain proves the real Linux launcher
// path end to end: a connector launched from verified bytes is served, and
// the shutdown terminates the child, joins the drain workers and reaps the
// process without waiting on the pipe.
//
//nolint:paralleltest // goleak VerifyNone conflicts with parallel tests
func TestHost_NativeLaunchShutdownReleasesDrain(t *testing.T) {
	defer goleak.VerifyNone(t)

	artifact := verifyStagedExecutable(t, stageExecFixture(t))
	h := processhost.NewHost(processhost.Config{
		Launcher: processhost.NewPlatformLauncher(),
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = h.Close() })

	res, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID:       "native-shutdown",
		Artifact:         artifact,
		Model:            processhost.ProcessModelPerInstance,
		DialAndConfigure: dialOK,
	})
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if res.PID <= 0 {
		t.Fatal("expected a live child pid")
	}

	done := make(chan error, 1)
	go func() { done <- h.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("close: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("host shutdown did not complete")
	}
	waitProcessGone(t, res.PID, 10*time.Second)
}

// verifyStagedExecutable verifies a staged discovery root and returns the
// verified artifact.
func verifyStagedExecutable(t *testing.T, root string) *trust.VerifiedArtifact {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "plugin.backendplugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := inframanifest.ParseStrictBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	res := trust.Verify(root, m, trust.VerifyOptions{})
	if res.Reason != trust.ReasonOK || res.Artifact == nil {
		t.Fatalf("verify: %+v", res)
	}
	t.Cleanup(func() { _ = res.Artifact.Close() })
	return res.Artifact
}

// waitProcessGone waits until the pid no longer exists, which proves the host
// terminated and reaped the child. Only ESRCH proves the pid is gone; any
// other error, EPERM for example, leaves the question open and fails the
// test instead of passing it.
func waitProcessGone(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		err := unix.Kill(pid, 0)
		switch {
		case err == nil:
			// The pid still exists.
		case errors.Is(err, unix.ESRCH):
			return
		default:
			t.Fatalf("probing pid %d failed: %v", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d was not terminated and reaped", pid)
}
