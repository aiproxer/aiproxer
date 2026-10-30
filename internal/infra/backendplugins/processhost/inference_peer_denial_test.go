package processhost_test

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// TestActivate_LostPeerDeniesSecretsBeforeConfigure proves requirement 2.3's
// transport half for every peer-loss shape: an explicitly rejected peer, a
// stale generation, or a dead/uncontained child fails activation before
// DialAndConfigure runs, so zero secret bytes cross the transport. The child
// launch spec recorded on the failure path additionally carries no secret
// values: secrets travel only in the private Configure channel after peer
// success, never in launch environment.
func TestActivate_LostPeerDeniesSecretsBeforeConfigure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		launcher *processhost.TestLauncher
		channel  *processhost.TestChannel
	}{
		{"explicitly rejected peer", &processhost.TestLauncher{PID: 4242}, &processhost.TestChannel{RejectPeer: true}},
		{"stale generation", &processhost.TestLauncher{PID: 4242}, &processhost.TestChannel{StaleGen: true}},
		{"uncontained child pid", &processhost.TestLauncher{PID: 4242, ContainFail: true}, &processhost.TestChannel{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			host := processhost.NewHost(processhost.Config{Launcher: tc.launcher, Channel: tc.channel})
			t.Cleanup(func() { _ = host.Close() })
			var dials atomic.Int64
			_, err := host.Activate(context.Background(), processhost.ActivateRequest{
				InstanceID: "or-1",
				Artifact:   &trust.VerifiedArtifact{DigestHex: "inference-artifact"},
				Model:      processhost.ProcessModelPerInstance,
				Secrets:    backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("or-key-one")}},
				DialAndConfigure: func(context.Context, net.Conn, processhost.PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
					dials.Add(1)
					return nil
				},
			})
			if err == nil {
				t.Fatal("lost peer must fail activation")
			}
			if got := dials.Load(); got != 0 {
				t.Fatalf("DialAndConfigure ran %d times without peer authentication", got)
			}
			for _, entry := range tc.launcher.LastSpec.Env {
				if strings.Contains(entry, "or-key-one") {
					t.Fatalf("secret value in child launch env on failure path: %q", entry)
				}
			}
		})
	}
}
