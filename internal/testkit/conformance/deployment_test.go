package conformance

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	testkitopenresponses "github.com/aiproxer/aiproxer/internal/testkit/openresponses"
)

// The TestCellSelect_* tests in this file prove the reusable Phase 7 smoke
// harness: the generic DeploymentSpec cell selector can compose any
// authoritative FE×BE cell with contract-fake origins and injectable failure
// modes, deterministically, and fail closed for cells it cannot construct.
// These tests are SMOKE SCAFFOLDING ONLY — they are not Phase 8 compatibility
// evidence. Phase 8 replaces the contract-fake origins with the independent
// OpenResponses refbackend/refclient emulators and certifies each cell with
// official-wire scenarios (tasks 8.1–8.5).

// TestCellSelect_SmokeScaffold_HarnessAuthoritativeIDsAreDeterministic locks
// the smoke harness authoritative cell vocabulary: all real bundled frontends
// plus the OpenResponses family, and all constructible real backends plus the
// provider connector columns (openrouter, nvidia) that the base harness fails
// closed. Smoke-scaffold only; Phase 8 lands the connector columns.
func TestCellSelect_SmokeScaffold_HarnessAuthoritativeIDsAreDeterministic(t *testing.T) {
	t.Parallel()
	fe := HarnessFrontendIDs()
	be := HarnessBackendIDs()

	wantFE := []string{FrontendOpenAIResponses, FrontendOpenAILegacy, FrontendAnthropic, FrontendGemini, FrontendOpenResponses}
	wantBE := []string{
		BackendOpenAIResponses, BackendOpenAILegacy, BackendAnthropic, BackendGemini,
		BackendBedrock, BackendACP, BackendOpenRouter, BackendNVIDIA, BackendOpenResponses,
	}

	if !slices.Equal(fe, wantFE) {
		t.Fatalf("harness frontend IDs = %v, want %v", fe, wantFE)
	}
	if !slices.Equal(be, wantBE) {
		t.Fatalf("harness backend IDs = %v, want %v", be, wantBE)
	}
	// The harness vocabulary is a superset of the locked baseline matrix.
	for _, id := range BundledFrontendIDs() {
		if !slices.Contains(fe, id) {
			t.Fatalf("baseline frontend %q missing from harness IDs", id)
		}
	}
	for _, id := range BundledBackendIDs() {
		if !slices.Contains(be, id) {
			t.Fatalf("baseline backend %q missing from harness IDs", id)
		}
	}
}

// TestCellSelect_SmokeScaffold_DeployRejectsUnknownOrEmptyCells proves the
// generic smoke selector is fail-closed before any server/port is created for a
// cell it cannot mount. Smoke-scaffold only; Phase 8 extends the vocabulary
// with the independent emulators and connectors.
func TestCellSelect_SmokeScaffold_DeployRejectsUnknownOrEmptyCells(t *testing.T) {
	t.Parallel()
	cases := []DeploymentSpec{
		{Frontend: "", Backend: BackendOpenResponses},
		{Frontend: FrontendOpenResponses, Backend: ""},
		{Frontend: "nonsense-frontend", Backend: BackendOpenResponses},
		{Frontend: FrontendOpenResponses, Backend: "nonsense-backend"},
	}
	for _, tc := range cases {
		t.Run(tc.Frontend+"__"+tc.Backend, func(t *testing.T) {
			if err := tc.Validate(); err == nil {
				t.Fatalf("Validate accepted invalid cell %q x %q", tc.Frontend, tc.Backend)
			}
			if d := Deploy(t, tc); d != nil {
				t.Fatalf("Deploy accepted invalid cell %q x %q and started a server", tc.Frontend, tc.Backend)
			}
		})
	}
}

// TestCellSelect_SmokeScaffold_ProviderConnectorColumnsFailClosed verifies
// openrouter/nvidia cells resolve as not-yet-constructible with a clear reason
// and no deployed origin/port (base smoke harness has no provider connectors).
// Smoke-scaffold only; Phase 8.5 lands the connector columns.
func TestCellSelect_SmokeScaffold_ProviderConnectorColumnsFailClosed(t *testing.T) {
	t.Parallel()
	for _, backend := range []string{BackendOpenRouter, BackendNVIDIA} {
		t.Run(backend, func(t *testing.T) {
			spec := DeploymentSpec{Frontend: FrontendOpenResponses, Backend: backend}
			if err := spec.Validate(); err == nil {
				t.Fatal("provider connector column must fail validation in base harness")
			}
			if d := Deploy(t, spec); d != nil {
				t.Fatalf("Deploy(%q) started a deployment; connector columns must fail closed", backend)
			}
		})
	}
}

// TestCellSelect_SmokeScaffold_GenericSelectorMountsEveryConstructibleFrontend
// verifies the one generic smoke Deploy path mounts every real frontend family
// (existing 4 plus OpenResponses) through the same generic selector. The
// OpenResponses frontend round-trips the full path today; existing frontends
// against the OpenResponses backend are a Phase 8.4 backend-column concern, so
// this test pins the smoke mount proof (deployment composes, frontend origin
// serves) for every family. This is the "no bespoke pairwise wiring" smoke
// proof on the frontend axis — NOT Phase 8 compatibility evidence.
func TestCellSelect_SmokeScaffold_GenericSelectorMountsEveryConstructibleFrontend(t *testing.T) {
	t.Parallel()
	for _, frontend := range HarnessFrontendIDs() {
		t.Run(frontend, func(t *testing.T) {
			d := Deploy(t, DeploymentSpec{Frontend: frontend, Backend: BackendOpenResponses, Transport: TransportJSON})
			if d == nil {
				t.Fatalf("Deploy(%q, openresponses) failed", frontend)
			}
			defer func() { _ = d.Close() }()
			if d.Server == nil {
				t.Fatal("deployment has no frontend server")
			}
			if frontend == FrontendOpenResponses {
				res, err := d.Client.RoundTrip(context.Background(), "ping")
				if err != nil {
					t.Fatalf("round trip through OpenResponses frontend: %v", err)
				}
				if strings.TrimSpace(res.Text) == "" {
					t.Fatal("round trip through OpenResponses frontend returned empty text")
				}
			}
		})
	}
}

// TestHarness_DeployIsDeterministicAndClean proves the same cell deployed twice
// yields byte-identical client-visible text and that Close is idempotent and
// shuts the frontend origin down (connection refused after close).
func TestHarness_DeployIsDeterministicAndClean(t *testing.T) {
	t.Parallel()

	first := Deploy(t, DeploymentSpec{Frontend: FrontendOpenResponses, Backend: BackendOpenResponses, Transport: TransportJSON})
	if first == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = first.Close() }()

	second := Deploy(t, DeploymentSpec{Frontend: FrontendOpenResponses, Backend: BackendOpenResponses, Transport: TransportJSON})
	if second == nil {
		t.Fatal("Deploy failed")
	}

	got := mustHarnessRoundTrip(t, first.Client, "ping")
	if !strings.Contains(got, harnessFakeText) {
		t.Fatalf("round trip text %q missing %q", got, harnessFakeText)
	}
	want := got
	for range 2 {
		again := mustHarnessRoundTrip(t, second.Client, "ping")
		if again != want {
			t.Fatalf("non-deterministic harness output: first=%q later=%q", want, again)
		}
	}

	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("second Close must be idempotent: %v", err)
	}
	if resp, err := first.Server.Client().Get(first.Server.URL); err == nil {
		_ = resp.Body.Close()
		t.Fatal("frontend origin still serving after Close")
	}
}

// TestHarness_RequestCountersBoundedRedactedArtifacts proves the harness origin
// counts every upstream request, bounds and redacts its request-capture
// artifacts, and reports the exact count through the deployment.
func TestHarness_RequestCountersBoundedRedactedArtifacts(t *testing.T) {
	t.Parallel()
	d := Deploy(t, DeploymentSpec{
		Frontend:      FrontendOpenResponses,
		Backend:       BackendOpenResponses,
		Transport:     TransportJSON,
		ArtifactLimit: 2,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	origin := d.OriginFor(BackendOpenResponses)
	if origin == nil {
		t.Fatal("no primary origin")
	}
	for range 5 {
		mustHarnessRoundTrip(t, d.Client, "ping")
	}
	if got := origin.Count(); got != 5 {
		t.Fatalf("origin request count = %d, want 5", got)
	}
	if got := d.RequestCount(BackendOpenResponses); got != 5 {
		t.Fatalf("deployment request count = %d, want 5", got)
	}
	captured := origin.Capture()
	if len(captured) > 2 {
		t.Fatalf("capture length = %d, want bounded at ArtifactLimit=2", len(captured))
	}
	for _, obs := range captured {
		for key, values := range obs.Headers {
			for _, v := range values {
				if key == "Authorization" {
					if v != "[REDACTED]" {
						t.Fatalf("Authorization header not redacted: %q", v)
					}
				}
				if v == "sk-test" {
					t.Fatalf("credential material %q leaked in captured artifact header %q", v, key)
				}
			}
		}
	}
}

// TestFailureInjection_UnauthorizedOriginClassifiedNoRetry proves the harness
// credential/failure injection yields exactly one upstream attempt for a
// classified 401: no silent retry, no second request, and a client-visible error.
func TestFailureInjection_UnauthorizedOriginClassifiedNoRetry(t *testing.T) {
	t.Parallel()
	d := Deploy(t, DeploymentSpec{
		Frontend:   FrontendOpenResponses,
		Backend:    BackendOpenResponses,
		Transport:  TransportJSON,
		OriginFail: OriginFailUnauthorized,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	if _, err := d.Client.RoundTrip(context.Background(), "ping"); err == nil {
		t.Fatal("expected unauthorized round trip to fail")
	}
	if got := d.RequestCount(BackendOpenResponses); got != 1 {
		t.Fatalf("upstream request count = %d, want exactly 1 (no retry after classified failure)", got)
	}
}

// TestFailureInjection_MultipleCandidatesFailOver proves the generic harness
// composes multiple real candidate backends with independent injectable
// origins and that core fails over from a failing primary to a succeeding
// candidate without bespoke wiring.
func TestFailureInjection_MultipleCandidatesFailOver(t *testing.T) {
	t.Parallel()
	d := Deploy(t, DeploymentSpec{
		Frontend:  FrontendOpenResponses,
		Backend:   BackendOpenResponses,
		Transport: TransportJSON,
		// Primary origin fails with a retryable upstream error.
		OriginFail: OriginFailServerError,
		Candidates: []Candidate{{Backend: BackendOpenResponses, OriginFail: OriginFailNone}},
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	got := mustHarnessRoundTrip(t, d.Client, "ping")
	if !strings.Contains(got, harnessFakeText) {
		t.Fatalf("failover text %q missing %q", got, harnessFakeText)
	}
	if d.RequestCount(BackendOpenResponses) < 1 {
		t.Fatal("primary origin received no requests; failover chain not exercised")
	}
	cand := d.CandidateOrigin(0)
	if cand == nil {
		t.Fatal("no candidate origin")
	}
	if cand.Count() < 1 {
		t.Fatalf("candidate origin received %d requests, want >= 1", cand.Count())
	}
}

// TestFailureInjection_UnroutableModelZeroUpstreamRequests proves pre-network
// rejection through the full path: a model that resolves to no registered
// backend never reaches the reference origin.
func TestFailureInjection_UnroutableModelZeroUpstreamRequests(t *testing.T) {
	t.Parallel()
	d := Deploy(t, DeploymentSpec{Frontend: FrontendOpenResponses, Backend: BackendOpenResponses, Transport: TransportJSON})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	if err := d.RoundTripModel(context.Background(), "unroutable:model", "ping"); err == nil {
		t.Fatal("expected unroutable model to fail")
	}
	if got := d.RequestCount(BackendOpenResponses); got != 0 {
		t.Fatalf("unroutable model caused %d upstream requests, want 0", got)
	}
}

// TestHarness_VirtualClockDeterministicOriginTimestamps proves the harness
// virtual clock drives the reference-provider origin.
func TestHarness_VirtualClockDeterministicOriginTimestamps(t *testing.T) {
	t.Parallel()
	clock := testkitopenresponses.NewVirtualClock(time.Unix(1715620000, 0))
	d := Deploy(t, DeploymentSpec{
		Frontend:  FrontendOpenResponses,
		Backend:   BackendOpenResponses,
		Transport: TransportJSON,
		Clock:     clock,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()
	origin := d.OriginFor(BackendOpenResponses)
	if origin == nil {
		t.Fatal("no origin")
	}
	if origin.Clock() != clock {
		t.Fatal("origin did not receive the injected virtual clock")
	}
}

func mustHarnessRoundTrip(tb testing.TB, client ClientEntrypoint, prompt string) string {
	tb.Helper()
	res, err := client.RoundTrip(context.Background(), prompt)
	if err != nil {
		tb.Fatalf("round trip: %v", err)
	}
	return res.Text
}
