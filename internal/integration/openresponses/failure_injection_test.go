package openresponses

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/conformance"
)

// TestFailureInjection_ServerErrorFailoverToHealthyCandidate drives a full-path
// failover: a retryable primary origin failure is retried on a healthy second
// candidate and the terminal text arrives from the candidate origin.
func TestFailureInjection_ServerErrorFailoverToHealthyCandidate(t *testing.T) {
	t.Parallel()
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:   conformance.FrontendOpenResponses,
		Backend:    conformance.BackendOpenResponses,
		Transport:  conformance.TransportSSE,
		OriginFail: conformance.OriginFailServerError,
		Candidates: []conformance.Candidate{{Backend: conformance.BackendOpenResponses}},
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	res, err := d.Client.RoundTrip(context.Background(), "ping")
	if err != nil {
		t.Fatalf("failover round trip: %v", err)
	}
	if !strings.Contains(res.Text, conformance.HarnessFakeText) {
		t.Fatalf("failover text %q missing %q", res.Text, conformance.HarnessFakeText)
	}
	if d.RequestCount(conformance.BackendOpenResponses) < 1 {
		t.Fatal("primary failing origin was never attempted")
	}
	if cand := d.CandidateOrigin(0); cand == nil || cand.Count() < 1 {
		t.Fatal("healthy candidate origin was never reached")
	}
}

// TestFailureInjection_CredentialFailureSSEClassified proves an upstream 401 in
// the streaming path is classified (client-visible failure, no retry) with
// exactly one upstream attempt.
func TestFailureInjection_CredentialFailureSSEClassified(t *testing.T) {
	t.Parallel()
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:   conformance.FrontendOpenResponses,
		Backend:    conformance.BackendOpenResponses,
		Transport:  conformance.TransportSSE,
		OriginFail: conformance.OriginFailUnauthorized,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	if _, err := d.Client.RoundTrip(context.Background(), "ping"); err == nil {
		t.Fatal("expected credential failure to surface")
	}
	if got := d.RequestCount(conformance.BackendOpenResponses); got != 1 {
		t.Fatalf("credential failure caused %d upstream attempts, want exactly 1", got)
	}
}

// TestFailureInjection_BoundedCaptureOverflowDoesNotBlock verifies the bounded
// artifact capture reports overflow deterministically instead of unbounded growth.
func TestFailureInjection_BoundedCaptureOverflowDoesNotBlock(t *testing.T) {
	t.Parallel()
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:      conformance.FrontendOpenResponses,
		Backend:       conformance.BackendOpenResponses,
		Transport:     conformance.TransportJSON,
		ArtifactLimit: 1,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	for i := range 4 {
		if _, err := d.Client.RoundTrip(context.Background(), "ping"); err != nil {
			t.Fatalf("round trip %d: %v", i, err)
		}
	}
	origin := d.OriginFor(conformance.BackendOpenResponses)
	if got := len(origin.Capture()); got > 1 {
		t.Fatalf("capture length = %d, want <= 1", got)
	}
	if got := d.RequestCount(conformance.BackendOpenResponses); got != 4 {
		t.Fatalf("request count = %d, want 4", got)
	}
}
