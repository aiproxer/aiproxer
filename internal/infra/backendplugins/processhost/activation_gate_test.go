package processhost_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestActivate_SameInstanceConfigureOnceNoEarlyReturn(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 7001}
	entered := make(chan struct{})
	release := make(chan struct{})
	var configures atomic.Int64
	var completed atomic.Int64

	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: &processhost.TestChannel{}})
	art := &trust.VerifiedArtifact{DigestHex: "race-aa"}

	var wg sync.WaitGroup
	const n = 8
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := h.Activate(context.Background(), processhost.ActivateRequest{
				InstanceID: "same", Artifact: art, Model: processhost.ProcessModelPerInstance,
				Secrets: backendplugin.SecretBundle{Values: map[string][]byte{"k": []byte("v")}},
				DialAndConfigure: func(context.Context, net.Conn, processhost.PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
					if configures.Add(1) == 1 {
						close(entered)
						<-release
					}
					return nil
				},
			})
			errs[i] = err
			completed.Add(1)
		}(i)
	}

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("configure never entered")
	}
	time.Sleep(20 * time.Millisecond)
	if completed.Load() != 0 {
		t.Fatalf("early Activate return before configure finished: %d", completed.Load())
	}
	close(release)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	if configures.Load() != 1 {
		t.Fatalf("configures=%d want 1", configures.Load())
	}
	if launcher.Launches.Load() != 1 {
		t.Fatalf("launches=%d", launcher.Launches.Load())
	}
}

func TestActivate_SharedInstancesSerializeConfigure(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 7002}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: &processhost.TestChannel{}})
	art := &trust.VerifiedArtifact{DigestHex: "race-bb"}

	var depth atomic.Int64
	var maxDepth atomic.Int64
	var configures atomic.Int64
	releaseA := make(chan struct{})
	startedA := make(chan struct{})

	enter := func() {
		configures.Add(1)
		d := depth.Add(1)
		for {
			cur := maxDepth.Load()
			if d <= cur || maxDepth.CompareAndSwap(cur, d) {
				break
			}
		}
	}
	leave := func() { depth.Add(-1) }

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := h.Activate(context.Background(), processhost.ActivateRequest{
			InstanceID: "a", Artifact: art, Model: processhost.ProcessModelSharedArtifact,
			Sharing: processhost.SharingOptions{IsolationDeclared: true, ConcurrencyDeclared: true},
			DialAndConfigure: func(context.Context, net.Conn, processhost.PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
				enter()
				close(startedA)
				<-releaseA
				leave()
				return nil
			},
		})
		if err != nil {
			t.Errorf("a: %v", err)
		}
	}()
	select {
	case <-startedA:
	case <-time.After(3 * time.Second):
		t.Fatal("instance a configure not started")
	}
	go func() {
		defer wg.Done()
		_, err := h.Activate(context.Background(), processhost.ActivateRequest{
			InstanceID: "b", Artifact: art, Model: processhost.ProcessModelSharedArtifact,
			Sharing: processhost.SharingOptions{IsolationDeclared: true, ConcurrencyDeclared: true},
			DialAndConfigure: func(context.Context, net.Conn, processhost.PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
				enter()
				leave()
				return nil
			},
		})
		if err != nil {
			t.Errorf("b: %v", err)
		}
	}()
	time.Sleep(30 * time.Millisecond)
	if configures.Load() != 1 {
		t.Fatalf("expected only a configuring, got %d", configures.Load())
	}
	close(releaseA)
	wg.Wait()
	if configures.Load() != 2 {
		t.Fatalf("configures=%d want 2", configures.Load())
	}
	if maxDepth.Load() != 1 {
		t.Fatalf("concurrent DialAndConfigure depth=%d", maxDepth.Load())
	}
	if launcher.Launches.Load() != 1 {
		t.Fatalf("launches=%d", launcher.Launches.Load())
	}
}

func TestActivate_SecretsOnlyPostAuthOnce(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 7003}
	ch := &processhost.TestChannel{PeerPID: 9999} // unauthorized
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: ch})
	var configures atomic.Int64
	_, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID: "x", Artifact: &trust.VerifiedArtifact{DigestHex: "race-cc"},
		Model:   processhost.ProcessModelPerInstance,
		Secrets: backendplugin.SecretBundle{Values: map[string][]byte{"k": []byte("secret")}},
		DialAndConfigure: func(_ context.Context, _ net.Conn, _ processhost.PeerIdentity, _ uint64, secrets backendplugin.SecretBundle, _ []byte) error {
			configures.Add(1)
			if len(secrets.Values["k"]) > 0 {
				t.Error("secrets visible in configure after auth failure path")
			}
			return nil
		},
	})
	if !errors.Is(err, processhost.ReasonPeerRejected) && !errors.Is(err, processhost.ReasonPIDReuse) {
		t.Fatalf("want peer/pid rejection, got %v", err)
	}
	if configures.Load() != 0 {
		t.Fatalf("configure called %d times", configures.Load())
	}
}

func TestActivate_ConfigureFailureNoWaiterSuccess(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 7004}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: &processhost.TestChannel{}})
	art := &trust.VerifiedArtifact{DigestHex: "race-dd"}
	entered := make(chan struct{})
	release := make(chan struct{})

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range 4 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := h.Activate(context.Background(), processhost.ActivateRequest{
				InstanceID: "fail", Artifact: art, Model: processhost.ProcessModelPerInstance,
				DialAndConfigure: func(context.Context, net.Conn, processhost.PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
					select {
					case <-entered:
					default:
						close(entered)
					}
					<-release
					return errors.New("boom")
				},
			})
			errs[i] = err
		}(i)
	}
	<-entered
	close(release)
	wg.Wait()
	for i, err := range errs {
		if err == nil {
			t.Fatalf("caller %d saw success", i)
		}
	}
}

func TestPeer_PIDReuseRejected(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 8001, ContainFail: true}
	ch := &processhost.TestChannel{PeerPID: 8001}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: ch})
	configured := atomic.Bool{}
	_, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID: "x", Artifact: &trust.VerifiedArtifact{DigestHex: "pid-reuse"},
		Model: processhost.ProcessModelPerInstance,
		DialAndConfigure: func(context.Context, net.Conn, processhost.PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
			configured.Store(true)
			return nil
		},
	})
	if !errors.Is(err, processhost.ReasonPIDReuse) {
		t.Fatalf("want pid_reuse, got %v", err)
	}
	if configured.Load() {
		t.Fatal("configure must not run")
	}
}

func TestSecureChannel_CookiePlaintextFallbackRejected(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 1}
	h := processhost.NewHost(processhost.Config{
		Launcher: launcher,
		Channel:  processhost.CookiePlaintextChannel{},
	})
	configured := atomic.Bool{}
	_, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID: "x", Artifact: &trust.VerifiedArtifact{DigestHex: "cookie"},
		Model: processhost.ProcessModelPerInstance,
		DialAndConfigure: func(context.Context, net.Conn, processhost.PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
			configured.Store(true)
			return nil
		},
	})
	if !errors.Is(err, processhost.ReasonCookieAuthRejected) && !errors.Is(err, processhost.ReasonUnsupportedChannel) {
		t.Fatalf("want cookie/unsupported channel, got %v", err)
	}
	if configured.Load() || launcher.Launches.Load() != 0 {
		t.Fatalf("configured=%v launches=%d", configured.Load(), launcher.Launches.Load())
	}
}

func TestSecureChannel_UnsupportedChannelNoConfigure(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 1}
	h := processhost.NewHost(processhost.Config{
		Launcher: launcher,
		Channel:  processhost.UnsupportedChannel{},
	})
	configured := atomic.Bool{}
	_, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID: "x", Artifact: &trust.VerifiedArtifact{DigestHex: "unsup"},
		Model: processhost.ProcessModelPerInstance,
		DialAndConfigure: func(context.Context, net.Conn, processhost.PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
			configured.Store(true)
			return nil
		},
	})
	if !errors.Is(err, processhost.ReasonUnsupportedChannel) {
		t.Fatalf("want unsupported_channel, got %v", err)
	}
	if configured.Load() {
		t.Fatal("configure must not run")
	}
}
