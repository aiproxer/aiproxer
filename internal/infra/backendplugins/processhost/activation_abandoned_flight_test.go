package processhost

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
)

// A caller's cancellation ends only its own wait: the shared launch keeps
// running for its peers and can finish after every caller is gone. The
// generation it publishes then has no owning activation, so the launch itself
// must settle it. Without that settlement the child process, listener and RPC
// connection stay alive with nobody leasing them until shutdown.
func TestActivate_AbandonedSharedFlightReapsUnownedGeneration(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		reaped := make(chan struct{})
		var once sync.Once
		launcher := &TestLauncher{
			OnLaunch: func(LaunchSpec) { close(entered); <-release },
			OnReap:   func() { once.Do(func() { close(reaped) }) },
		}
		h := NewHost(Config{Launcher: launcher, Channel: &TestChannel{}})
		defer func() { _ = h.Close() }()
		req := ownershipRequest(ProcessModelSharedArtifact)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		finished := make(chan error, 1)
		go func() {
			_, err := h.Activate(ctx, req)
			finished <- err
		}()
		<-entered

		// The only caller withdraws while the launch it started is still
		// running. Its cancellation is not the launch's cancellation.
		cancel()
		if err := <-finished; !errors.Is(err, context.Canceled) {
			t.Fatalf("activation err = %v, want context.Canceled", err)
		}
		select {
		case <-reaped:
			t.Fatal("generation reaped before the launch published it")
		default:
		}

		// The abandoned launch now publishes a generation no activation owns.
		close(release)
		synctest.Wait()
		select {
		case <-reaped:
		default:
			t.Fatal("abandoned flight published a generation with no owning activation")
		}
	})
}
