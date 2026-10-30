package runtimebundle

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	publichost "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/host"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

// Successful dialing does not transfer session ownership until activation is
// admitted. A late rejection must dispose the actual gRPC client transport.
func TestDiscoveredActivation_RejectsAndDisposesLateSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := processhost.NewHost(processhost.Config{Launcher: &processhost.TestLauncher{}, Channel: &processhost.TestChannel{}})
		defer func() { _ = h.Close() }()
		conn, err := grpc.NewClient("passthrough:///unused", grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		session := publichost.NewSessionForTesting(backendpluginv1.NewBackendPluginClient(conn), conn, "owner", backendplugin.Negotiation{})
		entered, release := make(chan uint64, 1), make(chan struct{})
		dial := func(_ context.Context, req DialSessionRequest) (ExecuteSession, backendplugin.ResolvedProfile, error) {
			entered <- req.Generation
			<-release
			return session, backendplugin.ResolvedProfile{}, nil
		}
		finished := make(chan error, 1)
		go func() {
			_, _, err := buildDiscoveredPhysical(context.Background(), h, ValidatedExport{
				Artifact: &trust.VerifiedArtifact{DigestHex: "late"}, Model: processhost.ProcessModelPerInstance,
			}, inferencePhysicalInput(), dial, new(atomic.Uint64), nil)
			finished <- err
		}()
		if err := h.InvalidateProcessGeneration(<-entered); err != nil {
			t.Fatal(err)
		}
		close(release)
		if err := <-finished; !errors.Is(err, processhost.ReasonGenerationInvalidated) {
			t.Fatalf("late activation: %v", err)
		}
		if conn.GetState() != connectivity.Shutdown {
			t.Fatal("rejected activation leaked its gRPC client connection")
		}
	})
}
