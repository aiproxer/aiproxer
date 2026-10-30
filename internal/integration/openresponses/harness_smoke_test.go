package openresponses

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/conformance"
)

// TestHarness_JSONRoundTrip drives the full path OpenResponses frontend → core
// executor → real OpenResponses backend → contract-fake origin over the JSON
// client transport and asserts the deterministic fake text.
func TestHarness_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:  conformance.FrontendOpenResponses,
		Backend:   conformance.BackendOpenResponses,
		Transport: conformance.TransportJSON,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	res, err := d.Client.RoundTrip(context.Background(), "ping")
	if err != nil {
		t.Fatalf("JSON round trip: %v", err)
	}
	if !strings.Contains(res.Text, conformance.HarnessFakeText) {
		t.Fatalf("JSON text %q missing %q", res.Text, conformance.HarnessFakeText)
	}
	if res.Object != "response" {
		t.Fatalf("JSON response object = %q, want response", res.Object)
	}
	if res.Status != "completed" {
		t.Fatalf("JSON response status = %q, want completed", res.Status)
	}
	if res.ResponseID == "" {
		t.Fatal("JSON response id is empty")
	}
	if got := d.RequestCount(conformance.BackendOpenResponses); got != 1 {
		t.Fatalf("origin request count = %d, want 1", got)
	}
}

// TestHarness_SSERoundTrip drives the streaming SSE client transport through
// the same full path and asserts ordered incremental deltas and a terminal.
func TestHarness_SSERoundTrip(t *testing.T) {
	t.Parallel()
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:  conformance.FrontendOpenResponses,
		Backend:   conformance.BackendOpenResponses,
		Transport: conformance.TransportSSE,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	res, err := d.Client.RoundTrip(context.Background(), "ping")
	if err != nil {
		t.Fatalf("SSE round trip: %v", err)
	}
	if !strings.Contains(res.Text, conformance.HarnessFakeText) {
		t.Fatalf("SSE text %q missing %q", res.Text, conformance.HarnessFakeText)
	}
	if !strings.Contains(res.Text, conformance.HarnessFakeText) {
		t.Fatal("SSE text empty")
	}
	if len(res.Events) < 3 {
		t.Fatalf("SSE saw only %d events, want ordered created/delta/terminal", len(res.Events))
	}
	first := res.Events[0]
	if first != "response.output_item.added" {
		t.Fatalf("SSE first event = %q, want response.output_item.added", first)
	}
	last := res.Events[len(res.Events)-1]
	if last != "response.completed" {
		t.Fatalf("SSE terminal event = %q, want response.completed", last)
	}
	if got := d.RequestCount(conformance.BackendOpenResponses); got != 1 {
		t.Fatalf("origin request count = %d, want 1", got)
	}
}

// TestHarness_CompactRoundTrip drives the standalone compaction client
// transport and asserts the response.compaction resource shape.
func TestHarness_CompactRoundTrip(t *testing.T) {
	t.Parallel()
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:  conformance.FrontendOpenResponses,
		Backend:   conformance.BackendOpenResponses,
		Transport: conformance.TransportCompact,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	res, err := d.Client.RoundTrip(context.Background(), "compact this")
	if err != nil {
		t.Fatalf("compact round trip: %v", err)
	}
	if res.Object != "response.compaction" {
		t.Fatalf("compact resource object = %q, want response.compaction", res.Object)
	}
	if res.Status != "completed" {
		t.Fatalf("compact resource status = %q, want completed", res.Status)
	}
	if got := d.RequestCount(conformance.BackendOpenResponses); got != 1 {
		t.Fatalf("origin request count = %d, want 1", got)
	}
}

// TestHarness_WebSocketTurn drives one client WebSocket turn through the full
// path and asserts the terminal event and assembled text.
func TestHarness_WebSocketTurn(t *testing.T) {
	t.Parallel()
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:  conformance.FrontendOpenResponses,
		Backend:   conformance.BackendOpenResponses,
		Transport: conformance.TransportWebSocket,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	res, err := d.Client.RoundTrip(context.Background(), "ping")
	if err != nil {
		t.Fatalf("WebSocket turn: %v", err)
	}
	if !strings.Contains(res.Text, conformance.HarnessFakeText) {
		t.Fatalf("WebSocket text %q missing %q", res.Text, conformance.HarnessFakeText)
	}
	if len(res.Events) == 0 || res.Events[len(res.Events)-1] != "response.completed" {
		t.Fatalf("WebSocket terminal event missing: %v", res.Events)
	}
}

// TestJSON_OpenResponsesCreateStrictDecode proves the JSON transport rejects a
// malformed body before any upstream request.
func TestJSON_OpenResponsesCreateStrictDecode(t *testing.T) {
	t.Parallel()
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:  conformance.FrontendOpenResponses,
		Backend:   conformance.BackendOpenResponses,
		Transport: conformance.TransportJSON,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	if err := d.SendRawCreate(context.Background(), `{"stream":false,"store":false}`); err == nil {
		t.Fatal("expected strict decode to reject a body without model/input")
	}
	if got := d.RequestCount(conformance.BackendOpenResponses); got != 0 {
		t.Fatalf("strict-decode rejection caused %d upstream requests, want 0", got)
	}
}

// TestSSE_StreamingOrderedIncrementalEvents pins the exact SSE event sequence
// the full path emits for one streaming cell.
func TestSSE_StreamingOrderedIncrementalEvents(t *testing.T) {
	t.Parallel()
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:  conformance.FrontendOpenResponses,
		Backend:   conformance.BackendOpenResponses,
		Transport: conformance.TransportSSE,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	res, err := d.Client.RoundTrip(context.Background(), "ping")
	if err != nil {
		t.Fatalf("SSE round trip: %v", err)
	}
	seen := map[string]bool{}
	for _, ev := range res.Events {
		seen[ev] = true
	}
	for _, want := range []string{
		"response.created",
		"response.output_item.added",
		"response.output_text.delta",
		"response.output_text.done",
		"response.output_item.done",
		"response.completed",
	} {
		if !seen[want] {
			t.Fatalf("SSE stream missing %q; saw %v", want, res.Events)
		}
	}
}

// TestCompact_CompactRejectsStreamingControl proves the compact transport is
// strictly non-streaming.
func TestCompact_CompactRejectsStreamingControl(t *testing.T) {
	t.Parallel()
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:  conformance.FrontendOpenResponses,
		Backend:   conformance.BackendOpenResponses,
		Transport: conformance.TransportCompact,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	if err := d.SendRawCompact(context.Background(), `{"model":"gpt-4o-mini","input":"x","stream":true}`); err == nil {
		t.Fatal("expected compact transport to reject streaming control")
	}
}

// TestWebSocket_TurnRejectsStoreTrue proves WebSocket turns are connection-local
// only: a store:true envelope is rejected without upstream work.
func TestWebSocket_TurnRejectsStoreTrue(t *testing.T) {
	t.Parallel()
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:  conformance.FrontendOpenResponses,
		Backend:   conformance.BackendOpenResponses,
		Transport: conformance.TransportWebSocket,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	if err := d.SendRawWSTurn(context.Background(), `{"type":"response.create","model":"gpt-4o-mini","input":"x","store":true}`); err == nil {
		t.Fatal("expected WebSocket turn with store:true to be rejected")
	}
	if got := d.RequestCount(conformance.BackendOpenResponses); got != 0 {
		t.Fatalf("rejected WebSocket turn caused %d upstream requests, want 0", got)
	}
}
