package processhost

import (
	"context"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

type cancellationLauncher struct {
	started  chan struct{}
	canceled chan struct{}
	release  chan struct{}
	context  chan context.Context
}

type lateFlightLauncher struct {
	count                    atomic.Int64
	started, release, reaped chan struct{}
}

func (l *lateFlightLauncher) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	first := l.count.Add(1) == 1
	if first {
		close(l.started)
		<-l.release // deliberately ignores cancellation
	}
	launcher := &TestLauncher{}
	if first {
		launcher.OnReap = func() { close(l.reaped) }
	}
	return launcher.Launch(ctx, spec)
}

type uncancelableAcceptChannel struct{ base TestChannel }

func (c *uncancelableAcceptChannel) Listen(ctx context.Context, gen uint64) (Listener, []*os.File, error) {
	lis, files, err := c.base.Listen(ctx, gen)
	return uncancelableAcceptListener{lis}, files, err
}

type uncancelableAcceptListener struct{ Listener }

func (l uncancelableAcceptListener) Accept(context.Context) (net.Conn, PeerIdentity, error) {
	return l.Listener.Accept(context.Background())
}

func TestActivate_AbandonedLateSuccessCannotReplaceNewFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := &lateFlightLauncher{started: make(chan struct{}), release: make(chan struct{}), reaped: make(chan struct{})}
		h := NewHost(Config{Launcher: l, Channel: &uncancelableAcceptChannel{base: TestChannel{PeerPID: 4242}}})
		defer func() { _ = h.Close() }()
		req := ownershipRequest(ProcessModelSharedArtifact)
		ctx, cancel := context.WithCancel(context.Background())
		first := make(chan error, 1)
		go func() { _, err := h.Activate(ctx, req); first <- err }()
		<-l.started
		cancel()
		if err := <-first; !errors.Is(err, context.Canceled) {
			t.Fatalf("first activation: %v", err)
		}
		req.InstanceID = "replacement"
		replacement := activateOwner(t, h, req)
		close(l.release)
		synctest.Wait()
		select {
		case <-l.reaped:
		default:
			t.Fatal("late abandoned process was not reaped")
		}
		again := activateOwner(t, h, req)
		if again.Generation != replacement.Generation {
			t.Fatal("abandoned flight displaced replacement")
		}
		if l.count.Load() != 2 {
			t.Fatalf("launches = %d, want 2", l.count.Load())
		}
		if err := replacement.Cleanup(); err != nil {
			t.Fatal(err)
		}
	})
}

func (l *cancellationLauncher) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	l.context <- ctx
	close(l.started)
	select {
	case <-ctx.Done():
		close(l.canceled)
		return nil, ctx.Err()
	case <-l.release:
		return (&TestLauncher{}).Launch(ctx, spec)
	}
}

func TestActivate_LastWithdrawalCancelsLaunchButNotSurvivor(t *testing.T) {
	for _, survivor := range []bool{false, true} {
		name := "last withdrawal"
		if survivor {
			name = "survivor"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				l := &cancellationLauncher{started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}), context: make(chan context.Context, 1)}
				h := NewHost(Config{Launcher: l, Channel: &TestChannel{}})
				defer func() { _ = h.Close() }()
				req := ownershipRequest(ProcessModelSharedArtifact)
				ctx, cancel := context.WithCancel(context.Background())
				first := make(chan error, 1)
				go func() { _, err := h.Activate(ctx, req); first <- err }()
				<-l.started
				launchCtx := <-l.context
				second := make(chan ActivateResult, 1)
				secondErr := make(chan error, 1)
				if survivor {
					req.InstanceID = "survivor"
					go func() { res, err := h.Activate(context.Background(), req); second <- res; secondErr <- err }()
					synctest.Wait()
				}
				cancel()
				if err := <-first; !errors.Is(err, context.Canceled) {
					t.Fatalf("first activation: %v", err)
				}
				synctest.Wait()
				if survivor {
					select {
					case <-l.canceled:
						t.Fatal("canceled a surviving owner's launch")
					default:
					}
					close(l.release)
					res := <-second
					if err := <-secondErr; err != nil {
						t.Fatal(err)
					}
					select {
					case <-launchCtx.Done():
						t.Fatal("publication canceled the running generation")
					default:
					}
					if err := res.Cleanup(); err != nil {
						t.Fatal(err)
					}
				} else {
					select {
					case <-l.canceled:
					default:
						t.Fatal("last withdrawal did not cancel launch")
					}
				}
			})
		})
	}
}

func TestActivate_LastWithdrawalCancelsPeerAccept(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		launcher := &TestLauncher{}
		h := NewHost(Config{Launcher: launcher, Channel: waitingPeerChannel{entered: entered}})
		defer func() { _ = h.Close() }()
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan error, 1)
		go func() { _, err := h.Activate(ctx, ownershipRequest(ProcessModelPerInstance)); finished <- err }()
		<-entered
		cancel()
		if err := <-finished; !errors.Is(err, context.Canceled) {
			t.Fatalf("activation: %v", err)
		}
		synctest.Wait()
		proc, ok := launcher.LastProcess.(*testProc)
		if !ok {
			t.Fatalf("unexpected process %T", launcher.LastProcess)
		}
		if !proc.Killed() || !proc.Waited() {
			t.Fatal("withdrawn peer accept left the connector alive")
		}
	})
}

func TestActivate_AllSharedCallersWithdraw(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := &cancellationLauncher{started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}), context: make(chan context.Context, 1)}
		h := NewHost(Config{Launcher: l, Channel: &TestChannel{}})
		defer func() { _ = h.Close() }()
		req := ownershipRequest(ProcessModelSharedArtifact)
		firstCtx, cancelFirst := context.WithCancel(context.Background())
		secondCtx, cancelSecond := context.WithCancel(context.Background())
		defer cancelFirst()
		defer cancelSecond()
		first, second := make(chan error, 1), make(chan error, 1)
		go func() { _, err := h.Activate(firstCtx, req); first <- err }()
		<-l.started
		req.InstanceID = "second"
		go func() { _, err := h.Activate(secondCtx, req); second <- err }()
		synctest.Wait()
		cancelFirst()
		if err := <-first; !errors.Is(err, context.Canceled) {
			t.Fatalf("first activation: %v", err)
		}
		synctest.Wait()
		select {
		case <-l.canceled:
			t.Fatal("launch canceled while second owner was waiting")
		default:
		}
		cancelSecond()
		if err := <-second; !errors.Is(err, context.Canceled) {
			t.Fatalf("second activation: %v", err)
		}
		synctest.Wait()
		select {
		case <-l.canceled:
		default:
			t.Fatal("launch remained blocked after both owners withdrew")
		}
	})
}
