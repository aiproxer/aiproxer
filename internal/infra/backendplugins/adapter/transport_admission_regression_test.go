package adapter_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	testkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// TestAdapter_TransportNegotiation_StreamingConnector verifies that a connector declaring
// Capabilities.Streaming: true and TransportCapabilities.BidirectionalStream: true
// admits both streaming and non-streaming delivery modes, and executes both cleanly.
func TestAdapter_TransportNegotiation_StreamingConnector(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "streaming-inst", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := inst.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !profile.Capabilities.Streaming || !profile.TransportCapabilities.BidirectionalStream {
		t.Fatalf("expected profile with Streaming=true and BidirectionalStream=true, got %+v", profile)
	}

	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID: "streaming-inst", RoutePrefixes: []string{"fake"},
	})
	t.Cleanup(func() { _ = br.Cleanup() })
	be := br.Backend

	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "fake", Model: "fake-model"},
		Key:     "fake:fake-model",
	}

	// 1. Streaming delivery mode
	callStreaming := aipapi.Call{
		ID:      "req-stream",
		Session: aipapi.SessionRef{ALegID: "aleg-stream"},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeStreaming,
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello streaming")},
		}},
	}
	capsStream := be.ResolveTransportCaps(context.Background(), callStreaming, cand)
	if !capsStream.Supports(aipapi.OperationOpenAIChatCompletions, aipapi.TransportModeStreaming) {
		t.Fatal("expected transport caps to support streaming")
	}
	if !capsStream.Supports(aipapi.OperationOpenAIChatCompletions, aipapi.TransportModeNonStreaming) {
		t.Fatal("expected transport caps to support non-streaming")
	}
	admitStream := aipapi.AdmitCandidate(aipapi.CandidateAdmissionInput{
		Call:            callStreaming,
		Invocation:      callStreaming.Invocation,
		BackendCaps:     be.Caps,
		TransportCaps:   capsStream,
		TransportPolicy: aipapi.TransportFallbackExact,
	})
	if admitStream.Kind != aipapi.NegotiationLossless {
		t.Fatalf("expected streaming candidate admission to succeed, got %v: %v", admitStream.Kind, admitStream.Transport.Err())
	}
	if admitStream.Transport.Selected != aipapi.TransportModeStreaming {
		t.Fatalf("expected Selected=streaming, got %s", admitStream.Transport.Selected)
	}

	// Execute streaming call through host adapter
	stream, err := be.Open(context.Background(), callStreaming, cand)
	if err != nil {
		t.Fatalf("be.Open streaming failed: %v", err)
	}
	var sawDeltaStream bool
	drainCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		ev, err := stream.Recv(drainCtx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("stream.Recv error: %v", err)
		}
		if ev.Kind == aipapi.EventTextDelta && ev.Delta == "ok" {
			sawDeltaStream = true
		}
	}
	_ = stream.Close()
	if !sawDeltaStream {
		t.Fatal("streaming call did not receive expected text delta")
	}

	// 2. Non-streaming delivery mode
	callNonStreaming := aipapi.Call{
		ID:      "req-nonstream",
		Session: aipapi.SessionRef{ALegID: "aleg-nonstream"},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeNonStreaming,
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello non-streaming")},
		}},
	}
	capsNonStream := be.ResolveTransportCaps(context.Background(), callNonStreaming, cand)
	admitNonStream := aipapi.AdmitCandidate(aipapi.CandidateAdmissionInput{
		Call:            callNonStreaming,
		Invocation:      callNonStreaming.Invocation,
		BackendCaps:     be.Caps,
		TransportCaps:   capsNonStream,
		TransportPolicy: aipapi.TransportFallbackExact,
	})
	if admitNonStream.Kind != aipapi.NegotiationLossless {
		t.Fatalf("expected non-streaming candidate admission to succeed, got %v: %v", admitNonStream.Kind, admitNonStream.Transport.Err())
	}
	if admitNonStream.Transport.Selected != aipapi.TransportModeNonStreaming {
		t.Fatalf("expected Selected=non_streaming, got %s", admitNonStream.Transport.Selected)
	}

	// Execute non-streaming call through host adapter (collecting canonical stream)
	streamNS, err := be.Open(context.Background(), callNonStreaming, cand)
	if err != nil {
		t.Fatalf("be.Open non-streaming failed: %v", err)
	}
	var sawDeltaNS bool
	drainCtxNS, cancelNS := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelNS()
	for {
		ev, err := streamNS.Recv(drainCtxNS)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("streamNS.Recv error: %v", err)
		}
		if ev.Kind == aipapi.EventTextDelta && ev.Delta == "ok" {
			sawDeltaNS = true
		}
	}
	_ = streamNS.Close()
	if !sawDeltaNS {
		t.Fatal("non-streaming call did not receive expected text delta")
	}
}

// TestAdapter_TransportNegotiation_NonStreamingOnlyConnector verifies that when a connector declares
// Capabilities.Streaming: false and TransportCapabilities.BidirectionalStream: true:
// - Non-streaming delivery mode is admitted (TransportModeNonStreaming).
// - Streaming delivery mode is rejected by candidate admission (NegotiationReject / ErrTransportReject).
func TestAdapter_TransportNegotiation_NonStreamingOnlyConnector(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "nonstream-only", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Override profile to simulate a non-streaming-only connector over bidirectional gRPC stream
	nonStreamingProfile := backendplugin.ResolvedProfile{
		Capabilities: backendplugin.CapabilitySummary{
			Streaming: false,
		},
		TransportCapabilities: backendplugin.TransportCapabilitySummary{
			Cancellation:        true,
			BidirectionalStream: true,
		},
	}

	br := adapter.Build(inst, nonStreamingProfile, adapter.Options{
		InstanceID: "nonstream-only", RoutePrefixes: []string{"fake"},
	})
	t.Cleanup(func() { _ = br.Cleanup() })
	be := br.Backend

	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "fake", Model: "fake-model"},
		Key:     "fake:fake-model",
	}

	// 1. Non-streaming call MUST be admitted
	callNonStreaming := aipapi.Call{
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeNonStreaming,
		},
	}
	capsNS := be.ResolveTransportCaps(context.Background(), callNonStreaming, cand)
	if capsNS == nil {
		t.Fatal("expected non-nil transport caps for non-streaming connector")
	}
	if !capsNS.Supports(aipapi.OperationOpenAIChatCompletions, aipapi.TransportModeNonStreaming) {
		t.Fatal("transport caps must support TransportModeNonStreaming")
	}
	if capsNS.Supports(aipapi.OperationOpenAIChatCompletions, aipapi.TransportModeStreaming) {
		t.Fatal("transport caps must NOT support TransportModeStreaming when Capabilities.Streaming=false")
	}

	admitNS := aipapi.AdmitCandidate(aipapi.CandidateAdmissionInput{
		Call:            callNonStreaming,
		Invocation:      callNonStreaming.Invocation,
		BackendCaps:     be.Caps,
		TransportCaps:   capsNS,
		TransportPolicy: aipapi.TransportFallbackExact,
	})
	if admitNS.Kind != aipapi.NegotiationLossless {
		t.Fatalf("expected non-streaming candidate admission to succeed, got %v: %v", admitNS.Kind, admitNS.Transport.Err())
	}
	if admitNS.Transport.Selected != aipapi.TransportModeNonStreaming {
		t.Fatalf("expected Selected=non_streaming, got %s", admitNS.Transport.Selected)
	}

	// 2. Streaming call MUST be rejected with ErrTransportReject
	callStreaming := aipapi.Call{
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeStreaming,
		},
	}
	capsStream := be.ResolveTransportCaps(context.Background(), callStreaming, cand)
	admitStream := aipapi.AdmitCandidate(aipapi.CandidateAdmissionInput{
		Call:            callStreaming,
		Invocation:      callStreaming.Invocation,
		BackendCaps:     be.Caps,
		TransportCaps:   capsStream,
		TransportPolicy: aipapi.TransportFallbackExact,
	})
	if admitStream.Kind != aipapi.NegotiationReject {
		t.Fatalf("expected streaming candidate admission to be rejected, got %v", admitStream.Kind)
	}
	if !errors.Is(admitStream.Transport.Err(), aipapi.ErrTransportReject) {
		t.Fatalf("expected ErrTransportReject, got: %v", admitStream.Transport.Err())
	}
}
