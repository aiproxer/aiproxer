package runtime_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// refusingHistory is a Responses tool history the adjacency quirk must refuse:
// output cA would have to cross assistant call cB, a model-authored item. It
// carries no reasoning parts, so capability negotiation cannot reject it
// first; the refusal below comes from the quirk alone.
func refusingHistory() *aipapi.Call {
	call := pdBaseCall("refuse:m|ok:m")
	call.Tools = []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{"type":"object"}`)}}
	call.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}
	call.Invocation = aipapi.Invocation{
		Operation:     aipapi.OperationOpenAIResponses,
		DeliveryMode:  aipapi.DeliveryModeNonStreaming,
		TransportMode: aipapi.TransportModeNonStreaming,
	}
	call.Messages = []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("start")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "cA", ToolName: "w", Content: []byte(`{"type":"function_call","call_id":"cA","name":"w","arguments":"{}"}`)}}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("trapped")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "cB", ToolName: "w", Content: []byte(`{"type":"function_call","call_id":"cB","name":"w","arguments":"{}"}`)}}},
		{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "cA", Content: []byte(`"ok-a"`)}}},
		{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "cB", Content: []byte(`"ok-b"`)}}},
	}
	return call
}

// refusingBackend is the real compatible backend with the adjacency quirk on.
// Refusal happens before any network I/O, so the base URL is never dialed.
func refusingBackend(opens *atomic.Int32) execbackend.Backend {
	be := openaicompat.NewBackend(openaicompat.BackendSpec{
		ID:            "refuse",
		BaseURL:       "http://127.0.0.1:1",
		APIKey:        "sk-test",
		Flavor:        openaicompat.FlavorResponses,
		ResolveFlavor: func(aipapi.Call) openaicompat.Flavor { return openaicompat.FlavorResponses },
		Quirks:        openaicompat.CompatibleQuirks{ToolOutputAdjacency: true},
	})
	inner := be.Open
	be.Open = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		opens.Add(1)
		return inner(ctx, call, cand)
	}
	return be
}

func okStubBackend(opened *atomic.Int32) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
		TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
			Operation: aipapi.OperationOpenAIResponses,
			Modes:     []aipapi.TransportMode{aipapi.TransportModeNonStreaming},
		}),
		ResolveCaps: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) aipapi.BackendCaps {
			return aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
		},
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			opened.Add(1)
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: "fallback-ok"},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}
}

func drainText(t *testing.T, stream aipapi.EventStream) string {
	t.Helper()
	defer func() { _ = stream.Close() }()
	var b strings.Builder
	for {
		ev, err := stream.Recv(context.Background())
		if err != nil {
			if errors.Is(err, io.EOF) {
				return b.String()
			}
			t.Fatalf("recv: %v", err)
		}
		if ev.Kind == aipapi.EventTextDelta {
			b.WriteString(ev.Delta)
		}
	}
}

// Claim: a refused adjacency rewrite fails over to the next candidate.
// The first backend refuses locally (recoverable pre-output); the route
// succeeds on the second candidate.
// Source: track 3 of the adjacency-refusal execution handoff.
func TestAdjacencyRefusal_FailsOverToNextCandidate(t *testing.T) {
	t.Parallel()
	var refuseOpens, okOpens atomic.Int32
	backends := map[string]execbackend.Backend{
		"refuse": refusingBackend(&refuseOpens),
		"ok":     okStubBackend(&okOpens),
	}
	ex := algSecureExecutor(t, backends, nil, extensions.SnapshotOptions{})
	stream, err := ex.Execute(principalCtx("adjacency-failover"), refusingHistory())
	if err != nil {
		t.Fatalf("failover must succeed on the second candidate: %v", err)
	}
	if got := drainText(t, stream); got != "fallback-ok" {
		t.Fatalf("second candidate text = %q, want %q", got, "fallback-ok")
	}
	if refuseOpens.Load() != 1 {
		t.Fatalf("refusing candidate opens = %d, want 1 (attempted, then swallowed)", refuseOpens.Load())
	}
	if okOpens.Load() != 1 {
		t.Fatalf("second candidate opens = %d, want 1", okOpens.Load())
	}
}

// Claim: with a single candidate, the exhausted route surfaces the refusal's
// capability root (which frontends map to 502 backend_unavailable) and never
// a 400-class client reject. This is the end-to-end counterpart of
// TestSwallowedCapability_SingleCandidateKeepsCapabilityReject, driven by the
// real quirk refusal instead of a synthetic producer.
// Source: #1136 makes the refusal recoverable pre-output so the core swallows
// it; #1140 preserves the swallowed capability reject as the terminal error.
// History: before #1140 a swallowed Open failure was dropped, so the route
// surfaced the generic planning error (502 backend_error) without
// ErrCapabilityReject; this test pinned that finding then and pins the fixed
// contract now.
func TestAdjacencyRefusal_SingleCandidateKeepsCapabilityReject(t *testing.T) {
	t.Parallel()
	var refuseOpens atomic.Int32
	backends := map[string]execbackend.Backend{"refuse": refusingBackend(&refuseOpens)}
	ex := algSecureExecutor(t, backends, nil, extensions.SnapshotOptions{})
	call := refusingHistory()
	call.Route = aipapi.RouteIntent{Selector: "refuse:m"}
	_, err := ex.Execute(principalCtx("adjacency-single"), call)
	if err == nil {
		t.Fatal("single refused candidate must surface an error")
	}
	if refuseOpens.Load() != 1 {
		t.Fatalf("refusing candidate opens = %d, want 1", refuseOpens.Load())
	}
	if !errors.Is(err, aipapi.ErrCapabilityReject) {
		t.Fatalf("surfaced error must match ErrCapabilityReject (the 502 predicate), got %T: %v", err, err)
	}
	if aipapi.IsReject(err) {
		t.Fatalf("surfaced error must not be a 400-class client reject: %v", err)
	}
}
