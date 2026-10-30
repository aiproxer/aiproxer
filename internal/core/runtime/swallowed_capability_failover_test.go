package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// capabilityRefusingBackend fails Open with a capability-classified,
// recoverable pre-output error: the same contract the adjacency quirk's
// refusal carries, without depending on that backend's code.
func capabilityRefusingBackend(opens *atomic.Int32) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
			Operation: aipapi.OperationOpenAIChatCompletions,
			Modes:     []aipapi.TransportMode{aipapi.TransportModeNonStreaming},
		}),
		ResolveCaps: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) aipapi.BackendCaps {
			return aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
		},
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			opens.Add(1)
			return nil, aipapi.RecoverablePreOutputError(fmt.Errorf("probe: capability refused: %w", aipapi.ErrCapabilityReject))
		},
	}
}

func fallbackOKBackend(opens *atomic.Int32) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
			Operation: aipapi.OperationOpenAIChatCompletions,
			Modes:     []aipapi.TransportMode{aipapi.TransportModeNonStreaming},
		}),
		ResolveCaps: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) aipapi.BackendCaps {
			return aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
		},
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			opens.Add(1)
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: "fallback-ok"},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}
}

func nonStreamingChatCall(selector string) *aipapi.Call {
	call := pdBaseCall(selector)
	call.Invocation = aipapi.Invocation{
		Operation:     aipapi.OperationOpenAIChatCompletions,
		DeliveryMode:  aipapi.DeliveryModeNonStreaming,
		TransportMode: aipapi.TransportModeNonStreaming,
	}
	return call
}

func drainTextDeltas(t *testing.T, stream aipapi.EventStream) string {
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

// Claim: a capability-classified swallowed Open failure fails over to the
// next candidate instead of failing the request.
func TestSwallowedCapability_FailsOverToNextCandidate(t *testing.T) {
	t.Parallel()
	var refuseOpens, okOpens atomic.Int32
	backends := map[string]execbackend.Backend{
		"refuse": capabilityRefusingBackend(&refuseOpens),
		"ok":     fallbackOKBackend(&okOpens),
	}
	ex := algSecureExecutor(t, backends, nil, extensions.SnapshotOptions{})
	stream, err := ex.Execute(principalCtx("swallowed-capability-failover"), nonStreamingChatCall("refuse:m|ok:m"))
	if err != nil {
		t.Fatalf("failover must succeed on the second candidate: %v", err)
	}
	if got := drainTextDeltas(t, stream); got != "fallback-ok" {
		t.Fatalf("second candidate text = %q, want %q", got, "fallback-ok")
	}
	if refuseOpens.Load() != 1 {
		t.Fatalf("refusing candidate opens = %d, want 1 (attempted, then swallowed)", refuseOpens.Load())
	}
	if okOpens.Load() != 1 {
		t.Fatalf("second candidate opens = %d, want 1", okOpens.Load())
	}
}

// Claim: when every candidate's swallowed failure is capability-classified,
// the exhausted route surfaces the capability root (which frontends map to
// 502) instead of the generic planning error.
// Mutant: drop the SwallowedCapabilityReject recording -> NoEligibleCandidate.
func TestSwallowedCapability_SingleCandidateKeepsCapabilityReject(t *testing.T) {
	t.Parallel()
	var refuseOpens atomic.Int32
	backends := map[string]execbackend.Backend{"refuse": capabilityRefusingBackend(&refuseOpens)}
	ex := algSecureExecutor(t, backends, nil, extensions.SnapshotOptions{})
	_, err := ex.Execute(principalCtx("swallowed-capability-single"), nonStreamingChatCall("refuse:m"))
	if err == nil {
		t.Fatal("single refused candidate must surface an error")
	}
	if refuseOpens.Load() != 1 {
		t.Fatalf("refusing candidate opens = %d, want 1", refuseOpens.Load())
	}
	if !errors.Is(err, aipapi.ErrCapabilityReject) {
		t.Fatalf("surfaced error must match ErrCapabilityReject, got %T: %v", err, err)
	}
	if aipapi.IsReject(err) {
		t.Fatalf("surfaced error must not be a 400-class client reject: %v", err)
	}
}
