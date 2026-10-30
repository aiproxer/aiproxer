package openresponses

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/conformance"
)

// TestCellSelect_OpenResponsesFrontendAcrossRealBackends drives the OpenResponses
// frontend against every constructible real backend through the single generic
// cell selector. This is the "any authoritative FE×BE cell without bespoke
// pairwise wiring" proof for the OpenResponses frontend row.
//
// ACP is asserted separately as a fail-closed cell: the OpenResponses frontend
// produces item-authority calls and the ACP v1 prompt-turn subset consumes
// projected message authority; the item→ACP projector semantics land in Phase
// 8.3. Every other constructible backend round-trips today.
func TestCellSelect_OpenResponsesFrontendAcrossRealBackends(t *testing.T) {
	t.Parallel()
	constructible := []string{
		conformance.BackendOpenAIResponses,
		conformance.BackendOpenAILegacy,
		conformance.BackendAnthropic,
		conformance.BackendGemini,
		conformance.BackendBedrock,
		conformance.BackendOpenResponses,
	}
	for _, backend := range constructible {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			d := conformance.Deploy(t, conformance.DeploymentSpec{
				Frontend:  conformance.FrontendOpenResponses,
				Backend:   backend,
				Transport: conformance.TransportJSON,
			})
			if d == nil {
				t.Fatalf("Deploy(openresponses, %q) failed", backend)
			}
			defer func() { _ = d.Close() }()

			res, err := d.Client.RoundTrip(context.Background(), "ping")
			if err != nil {
				t.Fatalf("openresponses frontend -> %s: %v", backend, err)
			}
			if strings.TrimSpace(res.Text) == "" {
				t.Fatalf("openresponses frontend -> %s returned empty text", backend)
			}
			if res.Status != "completed" {
				t.Fatalf("openresponses frontend -> %s status = %q, want completed", backend, res.Status)
			}
			if got := d.RequestCount(backend); got != 1 {
				t.Fatalf("origin %s request count = %d, want 1", backend, got)
			}
		})
	}
}

// TestCellSelect_OpenResponsesFrontendToACPSubsetRoundTrips proves the Phase 8.3
// OpenResponses→ACP cell: item-authority calls are projected to the ACP v1
// prompt-turn subset (text blocks) and round-trip through the real executor and
// the reference ACP origin. The ACP plugin performs its own JSON-RPC handshake
// before the prompt turn, so the origin observes multiple requests.
func TestCellSelect_OpenResponsesFrontendToACPSubsetRoundTrips(t *testing.T) {
	t.Parallel()
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:  conformance.FrontendOpenResponses,
		Backend:   conformance.BackendACP,
		Transport: conformance.TransportJSON,
	})
	if d == nil {
		t.Fatal("Deploy(openresponses, acp) failed")
	}
	defer func() { _ = d.Close() }()
	res, err := d.Client.RoundTrip(context.Background(), "ping")
	if err != nil {
		t.Fatalf("OpenResponses→ACP round trip: %v", err)
	}
	if res.Status != "completed" {
		t.Fatalf("OpenResponses→ACP status = %q, want completed", res.Status)
	}
	if !strings.Contains(res.Text, "ok") {
		t.Fatalf("OpenResponses→ACP text = %q, want ok", res.Text)
	}
	if got := d.RequestCount(conformance.BackendACP); got < 1 {
		t.Fatalf("OpenResponses→ACP request count = %d, want >= 1 (handshake+prompt)", got)
	}
}

// TestCellSelect_ExistingFrontendsAcrossOpenResponsesBackend drives the existing
// frontend families through the generic cell selector against the real
// OpenResponses backend contract fake. Every bundled frontend family round-trips
// the backend column: anthropic and gemini produce empty operations, while the
// explicit legacy operation frontends (OpenAI Responses, OpenAI Chat Completions)
// are bridged through the legacy→ordered-items projector (Phase 8.4).
func TestCellSelect_ExistingFrontendsAcrossOpenResponsesBackend(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		frontend string
		path     string
		body     string
	}{
		{
			name:     "anthropic",
			frontend: conformance.FrontendAnthropic,
			path:     "/v1/messages",
			body:     `{"model":"claude-3-5-haiku-20241022","max_tokens":32,"messages":[{"role":"user","content":"ping"}]}`,
		},
		{
			name:     "openai-responses",
			frontend: conformance.FrontendOpenAIResponses,
			path:     "/v1/responses",
			body:     `{"model":"gpt-4o-mini","input":"ping"}`,
		},
		{
			name:     "openai-legacy",
			frontend: conformance.FrontendOpenAILegacy,
			path:     "/v1/chat/completions",
			body:     `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"ping"}]}`,
		},
		{
			name:     "gemini",
			frontend: conformance.FrontendGemini,
			path:     "/v1beta/models/gemini-2.0-flash:generateContent",
			body:     `{"contents":[{"role":"user","parts":[{"text":"ping"}]}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := conformance.Deploy(t, conformance.DeploymentSpec{
				Frontend:  tc.frontend,
				Backend:   conformance.BackendOpenResponses,
				Transport: conformance.TransportJSON,
			})
			if d == nil {
				t.Fatalf("Deploy(%q, openresponses) failed", tc.frontend)
			}
			defer func() { _ = d.Close() }()

			status, err := d.RawFrontendPost(context.Background(), tc.path, tc.body)
			if err != nil {
				t.Fatalf("raw post through %s: %v", tc.frontend, err)
			}
			if status != http.StatusOK {
				t.Fatalf("%s frontend -> openresponses backend status = %d, want 200 (backend column cell must be green)", tc.frontend, status)
			}
			if got := d.RequestCount(conformance.BackendOpenResponses); got != 1 {
				t.Fatalf("%s frontend caused %d upstream requests, want exactly 1", tc.frontend, got)
			}
		})
	}
}

// TestCellSelect_StreamingSSEAcrossRealBackends proves the generic streaming
// client transport works across a representative real-backend set.
func TestCellSelect_StreamingSSEAcrossRealBackends(t *testing.T) {
	t.Parallel()
	for _, backend := range []string{
		conformance.BackendOpenAIResponses,
		conformance.BackendAnthropic,
		conformance.BackendOpenResponses,
	} {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			d := conformance.Deploy(t, conformance.DeploymentSpec{
				Frontend:  conformance.FrontendOpenResponses,
				Backend:   backend,
				Transport: conformance.TransportSSE,
			})
			if d == nil {
				t.Fatalf("Deploy(openresponses, %q, sse) failed", backend)
			}
			defer func() { _ = d.Close() }()

			res, err := d.Client.RoundTrip(context.Background(), "ping")
			if err != nil {
				t.Fatalf("SSE openresponses -> %s: %v", backend, err)
			}
			if strings.TrimSpace(res.Text) == "" {
				t.Fatalf("SSE openresponses -> %s returned empty text", backend)
			}
			if len(res.Events) == 0 || res.Events[len(res.Events)-1] != "response.completed" {
				t.Fatalf("SSE openresponses -> %s missing terminal event: %v", backend, res.Events)
			}
		})
	}
}
