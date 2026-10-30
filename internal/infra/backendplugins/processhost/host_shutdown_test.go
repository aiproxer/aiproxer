package processhost

import (
	"context"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// TestClose_ReapsActivationAdmittedBeforeShutdown pins the terminal host
// boundary: an activation admitted before Close must not publish its slot
// after Close has reaped the slots snapshot, or the child process, listener,
// and RPC connection leak past shutdown.
func TestClose_ReapsActivationAdmittedBeforeShutdown(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		launched := make(chan struct{})
		release := make(chan struct{})
		launcher := &TestLauncher{
			PID: 7711,
			OnLaunch: func(LaunchSpec) {
				close(launched)
				<-release
			},
		}
		h := NewHost(Config{Launcher: launcher, Channel: &TestChannel{}})

		activateErr := make(chan error, 1)
		go func() {
			_, err := h.Activate(context.Background(), ActivateRequest{
				InstanceID: "shutdown-race",
				Artifact:   &trust.VerifiedArtifact{DigestHex: "shutdown-race"},
				Model:      ProcessModelPerInstance,
				DialAndConfigure: func(context.Context, net.Conn, PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
					return nil
				},
			})
			activateErr <- err
		}()

		select {
		case <-launched:
		case <-time.After(5 * time.Second):
			t.Fatal("launcher never entered Launch")
		}
		// Close starts shutdown while the launch is still in flight, so the
		// subsequent slot publication must observe the closed host.
		closed := make(chan error, 1)
		go func() { closed <- h.Close(); close(closed) }()
		synctest.Wait()
		select {
		case <-closed:
			t.Error("Close returned before its admitted launch could be reaped")
		default:
		}
		close(release)
		if err := <-closed; err != nil {
			t.Fatalf("close host: %v", err)
		}

		select {
		case err := <-activateErr:
			if err == nil {
				t.Fatal("activation admitted before shutdown returned success after Close")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("activation did not return after Close released the launch")
		}
		proc, ok := launcher.LastProcess.(*testProc)
		if !ok || proc == nil {
			t.Fatalf("launcher recorded process %T", launcher.LastProcess)
		}
		if !proc.Killed() {
			t.Fatal("process launched during shutdown was never reaped by the host")
		}
	})
}
