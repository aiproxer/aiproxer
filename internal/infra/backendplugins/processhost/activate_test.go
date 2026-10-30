package processhost_test

import (
	"context"
	"errors"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func dialOK(context.Context, net.Conn, processhost.PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
	return nil
}

func TestLazy_NoLaunchUntilActivate(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{}
	_ = processhost.NewHost(processhost.Config{Launcher: launcher, Channel: &processhost.TestChannel{}})
	if launcher.Launches.Load() != 0 {
		t.Fatal("launch during construction")
	}
}

func TestLazy_FirstConfigureLaunchSingleflight(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 1001}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: &processhost.TestChannel{}})
	art := &trust.VerifiedArtifact{DigestHex: "aa", Strategy: trust.BindingProtectedStaging}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, err := h.Activate(context.Background(), processhost.ActivateRequest{
				InstanceID: "shared", Artifact: art, Model: processhost.ProcessModelPerInstance,
				DialAndConfigure: dialOK,
			})
			if err != nil {
				t.Errorf("%v", err)
			}
		})
	}
	wg.Wait()
	if launcher.Launches.Load() != 1 {
		t.Fatalf("launches=%d", launcher.Launches.Load())
	}
}

func TestProcessModel_PerInstanceSeparateLaunches(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 2002}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: &processhost.TestChannel{}})
	art := &trust.VerifiedArtifact{DigestHex: "bb"}
	for _, id := range []string{"a", "b"} {
		_, err := h.Activate(context.Background(), processhost.ActivateRequest{
			InstanceID: id, Artifact: art, Model: processhost.ProcessModelPerInstance,
			DialAndConfigure: dialOK,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if launcher.Launches.Load() != 2 {
		t.Fatalf("launches=%d", launcher.Launches.Load())
	}
}

func TestProcessModel_SharedRequiresDeclarations(t *testing.T) {
	t.Parallel()
	_, err := processhost.ProcessModelFromSharing(backendplugin.ProcessSharingSharedArtifact, processhost.SharingOptions{})
	if !errors.Is(err, processhost.ReasonProcessModelViolation) {
		t.Fatalf("%v", err)
	}
	_, err = processhost.ProcessModelFromSharing("", processhost.SharingOptions{})
	if !errors.Is(err, processhost.ReasonProcessModelViolation) {
		t.Fatalf("%v", err)
	}
	m, err := processhost.ProcessModelFromSharing(backendplugin.ProcessSharingSharedArtifact, processhost.SharingOptions{
		IsolationDeclared: true, ConcurrencyDeclared: true,
	})
	if err != nil || m != processhost.ProcessModelSharedArtifact {
		t.Fatalf("%v %v", m, err)
	}
}

func TestProcessModel_SharedArtifactOneLaunch(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 3003}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: &processhost.TestChannel{}})
	art := &trust.VerifiedArtifact{DigestHex: "cc"}
	for _, id := range []string{"a", "b"} {
		_, err := h.Activate(context.Background(), processhost.ActivateRequest{
			InstanceID: id, Artifact: art, Model: processhost.ProcessModelSharedArtifact,
			Sharing:          processhost.SharingOptions{IsolationDeclared: true, ConcurrencyDeclared: true},
			DialAndConfigure: dialOK,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if launcher.Launches.Load() != 1 {
		t.Fatalf("launches=%d", launcher.Launches.Load())
	}
}

func TestPeer_PassExactAcceptIdentity(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 4004}
	ch := &processhost.TestChannel{PeerUID: 77}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: ch})
	var saw processhost.PeerIdentity
	_, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID: "x", Artifact: &trust.VerifiedArtifact{DigestHex: "dd"},
		Model: processhost.ProcessModelPerInstance,
		DialAndConfigure: func(_ context.Context, _ net.Conn, peer processhost.PeerIdentity, gen uint64, _ backendplugin.SecretBundle, _ []byte) error {
			saw = peer
			if peer.PID != 4004 || peer.Generation != gen || peer.UID != 77 {
				t.Fatalf("peer=%+v gen=%d", peer, gen)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if saw.PID != 4004 {
		t.Fatal("missing peer")
	}
}

func TestPeer_UnauthorizedRejected(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 4004}
	ch := &processhost.TestChannel{PeerPID: 9999}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: ch})
	configured := atomic.Bool{}
	_, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID: "x", Artifact: &trust.VerifiedArtifact{DigestHex: "dd"},
		Model: processhost.ProcessModelPerInstance,
		DialAndConfigure: func(context.Context, net.Conn, processhost.PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
			configured.Store(true)
			return nil
		},
	})
	if !errors.Is(err, processhost.ReasonPeerRejected) || configured.Load() {
		t.Fatalf("%v configured=%v", err, configured.Load())
	}
}

func TestPeer_StaleGenerationRejected(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 5005}
	ch := &processhost.TestChannel{StaleGen: true}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: ch})
	_, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID: "x", Artifact: &trust.VerifiedArtifact{DigestHex: "ee"},
		Model: processhost.ProcessModelPerInstance, DialAndConfigure: dialOK,
	})
	if !errors.Is(err, processhost.ReasonStaleGeneration) {
		t.Fatalf("%v", err)
	}
}

func TestSecureChannel_EnvBootstrapRejected(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 1}
	h := processhost.NewHost(processhost.Config{
		Launcher: launcher, Channel: &processhost.TestChannel{},
		AllowEnv: []string{"PLUGIN_CLIENT_CERT"},
	})
	_, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID: "x", Artifact: &trust.VerifiedArtifact{DigestHex: "ff"},
		Model: processhost.ProcessModelPerInstance, DialAndConfigure: dialOK,
	})
	if !errors.Is(err, processhost.ReasonEnvBootstrapRejected) {
		t.Fatalf("%v", err)
	}
}

func TestSecureChannel_SecretsOnlyAfterPeer(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 6006}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: &processhost.TestChannel{}})
	sec := backendplugin.SecretBundle{Values: map[string][]byte{"k": []byte("v")}}
	_, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID: "x", Artifact: &trust.VerifiedArtifact{DigestHex: "gg"},
		Model: processhost.ProcessModelPerInstance, Secrets: sec, ConfigYAML: []byte("a: 1\n"),
		DialAndConfigure: func(_ context.Context, _ net.Conn, _ processhost.PeerIdentity, _ uint64, secrets backendplugin.SecretBundle, cfg []byte) error {
			if string(secrets.Values["k"]) != "v" || string(cfg) != "a: 1\n" {
				t.Fatal("secrets/config missing after peer")
			}
			if len(launcher.LastSpec.Env) > 0 {
				for _, e := range launcher.LastSpec.Env {
					if containsSecretish(e) {
						t.Fatalf("secret in launch env: %s", e)
					}
				}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range launcher.LastSpec.Env {
		if containsSecretish(e) {
			t.Fatalf("launch env leaked %s", e)
		}
	}
}

func containsSecretish(e string) bool {
	return len(e) >= 3 && (containsFold(e, "SECRET") || containsFold(e, "PLUGIN_CLIENT") || containsFold(e, "COOKIE"))
}

func containsFold(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexFold(s, sub) >= 0)
}

func indexFold(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			a, b := s[i+j], sub[j]
			if a >= 'a' && a <= 'z' {
				a -= 32
			}
			if b >= 'a' && b <= 'z' {
				b -= 32
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func TestSecureChannel_MinimalEnvDefault(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 1}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: &processhost.TestChannel{}})
	_, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID: "x", Artifact: &trust.VerifiedArtifact{DigestHex: "hh"},
		Model: processhost.ProcessModelPerInstance, DialAndConfigure: dialOK,
	})
	if err != nil {
		t.Fatal(err)
	}
	if launcher.LastSpec.Env == nil {
		t.Fatal("env must be non-nil empty")
	}
}

func TestRestart_LaterOperationNewGeneration(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 6006}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: &processhost.TestChannel{}})
	art := &trust.VerifiedArtifact{DigestHex: "ii"}
	res, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID: "x", Artifact: art, Model: processhost.ProcessModelPerInstance, DialAndConfigure: dialOK,
	})
	if err != nil {
		t.Fatal(err)
	}
	key := processhost.OwnershipKey(processhost.ProcessModelPerInstance, "ii", "x")
	if err := h.InvalidateGeneration(key); err != nil {
		t.Fatal(err)
	}
	res2, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID: "x", Artifact: art, Model: processhost.ProcessModelPerInstance, DialAndConfigure: dialOK,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Generation <= res.Generation || launcher.Launches.Load() != 2 {
		t.Fatalf("gen %d<=%d launches=%d", res2.Generation, res.Generation, launcher.Launches.Load())
	}
}

func TestPlatform_DarwinFailClosed(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("darwin fail-closed production profile")
	}
	h := processhost.NewHost(processhost.Config{})
	_, err := h.Activate(context.Background(), processhost.ActivateRequest{
		InstanceID: "x", Artifact: &trust.VerifiedArtifact{DigestHex: "jj", Strategy: trust.BindingProtectedStaging},
		Model: processhost.ProcessModelPerInstance, DialAndConfigure: dialOK,
	})
	if !errors.Is(err, processhost.ReasonUnsupportedBinding) && !errors.Is(err, processhost.ReasonUnsupportedChannel) {
		t.Fatalf("want unsupported binding/channel, got %v", err)
	}
}
