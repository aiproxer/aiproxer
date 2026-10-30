package openresponses

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/conformance"
)

// TestHarness_ExistingFamilyClientRoundTrip proves the existing reference-family
// client hook drives a deployment through the repository independent reference
// clients (non-streaming and streaming) for an existing frontend×backend cell.
func TestHarness_ExistingFamilyClientRoundTrip(t *testing.T) {
	t.Parallel()
	for _, transport := range []conformance.ClientTransport{conformance.TransportJSON, conformance.TransportSSE} {
		t.Run(string(transport), func(t *testing.T) {
			t.Parallel()
			d := conformance.Deploy(t, conformance.DeploymentSpec{
				Frontend:  conformance.FrontendOpenAILegacy,
				Backend:   conformance.BackendOpenAILegacy,
				Transport: transport,
			})
			if d == nil {
				t.Fatal("Deploy failed")
			}
			defer func() { _ = d.Close() }()

			res, err := d.Client.RoundTrip(context.Background(), "ping")
			if err != nil {
				t.Fatalf("existing-family client round trip over %s: %v", transport, err)
			}
			if strings.TrimSpace(res.Text) == "" {
				t.Fatalf("existing-family client over %s returned empty text", transport)
			}
			if got := d.RequestCount(conformance.BackendOpenAILegacy); got != 1 {
				t.Fatalf("origin request count = %d, want 1", got)
			}
		})
	}
}
