package processhost_test

import (
	"context"
	"errors"
	"net"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"go.uber.org/goleak"
)

//nolint:paralleltest // goleak VerifyNone conflicts with parallel tests
func TestCleanup_ConfigureFailureAfterStartReaps(t *testing.T) {
	defer goleak.VerifyNone(t)
	launcher := &processhost.TestLauncher{PID: 7100}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: &processhost.TestChannel{}})
	_, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID: "cfg-fail", Artifact: &trust.VerifiedArtifact{DigestHex: "cfg-fail"},
		Model: processhost.ProcessModelPerInstance,
		DialAndConfigure: func(context.Context, net.Conn, processhost.PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
			return errors.New("configure boom")
		},
	})
	if err == nil {
		t.Fatal("expected configure failure")
	}
	if launcher.Launches.Load() != 1 {
		t.Fatalf("launches=%d", launcher.Launches.Load())
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
}

//nolint:paralleltest // goleak VerifyNone conflicts with parallel tests
func TestCleanup_IdempotentBuildResultAndClose(t *testing.T) {
	defer goleak.VerifyNone(t)
	launcher := &processhost.TestLauncher{PID: 7007}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: &processhost.TestChannel{}})
	res, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID: "i", Artifact: &trust.VerifiedArtifact{DigestHex: "ii"},
		Model: processhost.ProcessModelPerInstance, DialAndConfigure: dialOK,
	})
	if err != nil {
		t.Fatal(err)
	}
	br := processhost.NewBuildResult(execbackend.Backend{}, res.Cleanup)
	if err := br.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if err := br.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCleanup_RejectNewWorkDuringShutdown(t *testing.T) {
	t.Parallel()
	h := processhost.NewHost(processhost.Config{
		Launcher: &processhost.TestLauncher{PID: 8008}, Channel: &processhost.TestChannel{},
	})
	_ = h.Close()
	_, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID: "i", Artifact: &trust.VerifiedArtifact{DigestHex: "jj"},
		Model: processhost.ProcessModelPerInstance, DialAndConfigure: dialOK,
	})
	if err != processhost.ReasonShuttingDown {
		t.Fatalf("%v", err)
	}
}

//nolint:paralleltest // goleak VerifyNone conflicts with parallel tests
func TestLeak_HostCloseReaps(t *testing.T) {
	defer goleak.VerifyNone(t)
	h := processhost.NewHost(processhost.Config{
		Launcher: &processhost.TestLauncher{PID: 9009}, Channel: &processhost.TestChannel{},
	})
	_, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID: "i", Artifact: &trust.VerifiedArtifact{DigestHex: "kk"},
		Model: processhost.ProcessModelPerInstance, DialAndConfigure: dialOK,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCleanup_GracefulThenHardAndExactlyOnceWait(t *testing.T) {
	t.Parallel()
	p := &processhost.TestLauncher{PID: 42}
	proc, err := p.Launch(context.Background(), processhost.LaunchSpec{Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.GracefulStop(10 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := proc.SignalKill(); err != nil {
		t.Fatal(err)
	}
	if err := proc.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := proc.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestCleanup_ReverseOrderBuildResults(t *testing.T) {
	t.Parallel()
	var order []int
	var cleanups []*processhost.BuildResult
	for i := range 3 {
		n := i
		cleanups = append(cleanups, processhost.NewBuildResult(execbackend.Backend{}, func() error {
			order = append(order, n)
			return nil
		}))
	}
	for _, cleanup := range slices.Backward(cleanups) {
		_ = cleanup.Cleanup()
	}
	if len(order) != 3 || order[0] != 2 || order[1] != 1 || order[2] != 0 {
		t.Fatalf("%v", order)
	}
}

func TestCleanup_ImmediateCloserRegistration(t *testing.T) {
	t.Parallel()
	var registered atomic.Int64
	register := func(c func() error) {
		registered.Add(1)
		_ = c
	}
	br := processhost.NewBuildResult(execbackend.Backend{}, func() error { return nil })
	register(br.Cleanup) // composition registers immediately after build
	if registered.Load() != 1 {
		t.Fatal("closer not registered")
	}
}

func TestCleanup_StopProcessExactlyOnce(t *testing.T) {
	t.Parallel()
	p := &processhost.TestLauncher{PID: 43}
	proc, err := p.Launch(context.Background(), processhost.LaunchSpec{Generation: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := processhost.StopProcess(proc, 5*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	var w processhost.ExactlyOnceWait
	if err := w.Wait(proc); err != nil {
		t.Fatal(err)
	}
	if err := w.Wait(proc); err != nil {
		t.Fatal(err)
	}
	switch runtime.GOOS {
	case "linux", "windows":
		if !processhost.DescendantCleanupSupported() {
			t.Fatalf("%s should advertise descendant/job cleanup", runtime.GOOS)
		}
	default:
		if processhost.DescendantCleanupSupported() {
			t.Fatal("unsupported profiles must not claim descendant cleanup")
		}
	}
}
