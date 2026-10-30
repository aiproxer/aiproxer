package processhost

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	backendplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// TestInvalidateGeneration_StaleReportKeepsCurrentGeneration proves the
// generation scope of an invalidation: the diagnostic budget is reported
// asynchronously, and a report that lands after a newer generation took the
// key must not tear that newer generation down.
func TestInvalidateGeneration_StaleReportKeepsCurrentGeneration(t *testing.T) {
	t.Parallel()
	launcher := &TestLauncher{PID: 9801}
	h := NewHost(Config{Launcher: launcher, Channel: &TestChannel{}})
	res, err := h.Activate(context.Background(), ActivateRequest{
		InstanceID: "stale-report",
		Artifact:   &trust.VerifiedArtifact{DigestHex: "stale-report"},
		Model:      ProcessModelPerInstance,
		DialAndConfigure: func(context.Context, net.Conn, PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	key := OwnershipKey(ProcessModelPerInstance, "stale-report", "stale-report")

	// A report for another generation is stale and must not be applied.
	if err := h.invalidateGeneration(key, res.Generation+1, ReasonConnectorOutputLimitExceeded); err != nil {
		t.Fatal(err)
	}
	again, err := h.Activate(context.Background(), ActivateRequest{
		InstanceID: "stale-report",
		Artifact:   &trust.VerifiedArtifact{DigestHex: "stale-report"},
		Model:      ProcessModelPerInstance,
		DialAndConfigure: func(context.Context, net.Conn, PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.Generation != res.Generation || launcher.Launches.Load() != 1 {
		t.Fatalf("stale report dropped the live generation: gen %d launches %d",
			again.Generation, launcher.Launches.Load())
	}

	// A report for the live generation applies, and it reports the budget
	// reason the generation was terminated for.
	if err := h.invalidateGeneration(key, res.Generation, ReasonConnectorOutputLimitExceeded); !errors.Is(err, ReasonConnectorOutputLimitExceeded) {
		t.Fatalf("live report: %v", err)
	}
	fresh, err := h.Activate(context.Background(), ActivateRequest{
		InstanceID: "stale-report",
		Artifact:   &trust.VerifiedArtifact{DigestHex: "stale-report"},
		Model:      ProcessModelPerInstance,
		DialAndConfigure: func(context.Context, net.Conn, PeerIdentity, uint64, backendplugin.SecretBundle, []byte) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Generation == res.Generation || launcher.Launches.Load() != 2 {
		t.Fatalf("live report did not drop the generation: gen %d launches %d",
			fresh.Generation, launcher.Launches.Load())
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
}
