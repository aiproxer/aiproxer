package processhost_test

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

type cancelFlightProc struct {
	pid int
	gen uint64
}

func (p *cancelFlightProc) PID() int                         { return p.pid }
func (p *cancelFlightProc) Generation() uint64               { return p.gen }
func (p *cancelFlightProc) Wait() error                      { return nil }
func (p *cancelFlightProc) SignalKill() error                { return nil }
func (p *cancelFlightProc) GracefulStop(time.Duration) error { return nil }
func (p *cancelFlightProc) Close() error                     { return nil }
func (p *cancelFlightProc) ContainsPID(pid int) bool         { return pid == p.pid }
func (p *cancelFlightProc) Stdout() io.ReadCloser {
	return io.NopCloser(strings.NewReader(""))
}

func (p *cancelFlightProc) Stderr() io.ReadCloser {
	return io.NopCloser(strings.NewReader(""))
}

// blockingFlightLauncher blocks every launch until unblock is closed, or the
// launch context is done. It models a slow connector spawn that respects
// cancellation, so singleflight waiter/leader cancellation is observable.
type blockingFlightLauncher struct {
	started     chan struct{}
	unblock     chan struct{}
	startedOnce atomic.Bool
	launches    atomic.Int64
}

func (l *blockingFlightLauncher) Launch(ctx context.Context, spec processhost.LaunchSpec) (processhost.Process, error) {
	l.launches.Add(1)
	if l.startedOnce.CompareAndSwap(false, true) {
		close(l.started)
	}
	select {
	case <-l.unblock:
		return &cancelFlightProc{pid: 4242, gen: spec.Generation}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func cancelFlightDial(context.Context, net.Conn, processhost.PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
	return nil
}

func cancelFlightReq(artifact *trust.VerifiedArtifact, id string) processhost.ActivateRequest {
	return processhost.ActivateRequest{
		InstanceID:       id,
		Artifact:         artifact,
		Model:            processhost.ProcessModelSharedArtifact,
		Sharing:          processhost.SharingOptions{IsolationDeclared: true, ConcurrencyDeclared: true},
		DialAndConfigure: cancelFlightDial,
	}
}

// A waiter whose context expires while a peer launch is blocked must stop
// waiting instead of blocking until the peer launch completes.
func TestActivate_SharedFlightWaiterCancelUnblocks(t *testing.T) {
	t.Parallel()
	launcher := &blockingFlightLauncher{started: make(chan struct{}), unblock: make(chan struct{})}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: &processhost.TestChannel{}})
	t.Cleanup(func() { _ = h.Close() })
	art := &trust.VerifiedArtifact{DigestHex: "cancel-waiter-shared-001"}

	leaderDone := make(chan error, 1)
	go func() {
		_, err := h.Activate(context.Background(), cancelFlightReq(art, "leader"))
		leaderDone <- err
	}()

	select {
	case <-launcher.started:
	case <-time.After(2 * time.Second):
		t.Fatal("leader launch did not start")
	}

	waiterCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	waiterDone := make(chan error, 1)
	go func() {
		_, err := h.Activate(waiterCtx, cancelFlightReq(art, "waiter"))
		waiterDone <- err
	}()

	select {
	case err := <-waiterDone:
		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter err = %v, want context deadline/canceled", err)
		}
		select {
		case out := <-leaderDone:
			t.Fatalf("leader completed before waiter cancellation (err=%v)", out)
		default:
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter Activate did not return on context timeout while leader launch blocked")
	}

	close(launcher.unblock)
	select {
	case err := <-leaderDone:
		if err != nil {
			t.Fatalf("leader Activate: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("leader did not complete after unblock")
	}
	if got := launcher.launches.Load(); got != 1 {
		t.Fatalf("launches = %d, want 1 (shared flight)", got)
	}
}

// Cancelling the flight leader must not fail a waiter that is still waiting:
// the shared launch runs on a detached context, so only the leader observes
// its own cancellation.
func TestActivate_SharedFlightLeaderCancelDoesNotFailWaiter(t *testing.T) {
	t.Parallel()
	launcher := &blockingFlightLauncher{started: make(chan struct{}), unblock: make(chan struct{})}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: &processhost.TestChannel{}})
	t.Cleanup(func() { _ = h.Close() })
	art := &trust.VerifiedArtifact{DigestHex: "cancel-leader-shared-001"}

	leaderCtx, leaderCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer leaderCancel()
	leaderDone := make(chan error, 1)
	go func() {
		_, err := h.Activate(leaderCtx, cancelFlightReq(art, "leader"))
		leaderDone <- err
	}()

	select {
	case <-launcher.started:
	case <-time.After(2 * time.Second):
		t.Fatal("leader launch did not start")
	}

	waiterDone := make(chan error, 1)
	go func() {
		_, err := h.Activate(context.Background(), cancelFlightReq(art, "waiter"))
		waiterDone <- err
	}()

	select {
	case err := <-leaderDone:
		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			t.Fatalf("leader err = %v, want context deadline/canceled", err)
		}
		select {
		case werr := <-waiterDone:
			t.Fatalf("waiter completed before unblock (err=%v): leader cancellation poisoned the shared flight", werr)
		default:
		}
	case <-time.After(2 * time.Second):
		t.Fatal("leader Activate did not return on its context timeout")
	}

	close(launcher.unblock)
	select {
	case err := <-waiterDone:
		if err != nil {
			t.Fatalf("waiter Activate: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not complete after unblock")
	}
	if got := launcher.launches.Load(); got != 1 {
		t.Fatalf("launches = %d, want 1 (shared flight)", got)
	}
}
