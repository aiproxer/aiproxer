package sagemaker_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/sagemaker/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aws/aws-sdk-go-v2/service/sagemakerruntime"
)

// F2 remediation: ListModels must expose only the configured endpoint and
// inference must collect the unary response (no provider event-stream
// buffering, no provider streaming advertised).

type f2StubRuntime struct {
	calls    int
	invokeFn func(context.Context, *sagemakerruntime.InvokeEndpointInput) (*sagemakerruntime.InvokeEndpointOutput, error)
}

func (f *f2StubRuntime) InvokeEndpoint(ctx context.Context, params *sagemakerruntime.InvokeEndpointInput, _ ...func(*sagemakerruntime.Options)) (*sagemakerruntime.InvokeEndpointOutput, error) {
	f.calls++
	if f.invokeFn != nil {
		return f.invokeFn(ctx, params)
	}
	return &sagemakerruntime.InvokeEndpointOutput{Body: []byte(`{"generated_text":"ok"}`)}, nil
}

func f2Configure(t *testing.T, r *f2StubRuntime, endpoint string) backendplugin.ConfiguredInstance {
	t.Helper()
	svc := service.New(service.WithStaticClients(r))
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "f2-inst",
		ConfigYAML:  []byte("region: us-east-1\nendpoint_name: " + endpoint + "\ninference_contract: hf-text-generation\n"),
		Secrets:     testStaticSecrets(),
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}
	return inst
}

// ListModels with no control plane in the loop still advertises exactly
// sagemaker/A without streaming.
func TestF2_ListModels_ExposesOnlyConfiguredEndpoint(t *testing.T) {
	t.Parallel()
	r := &f2StubRuntime{}
	inst := f2Configure(t, r, "ep-a")

	resp, err := inst.ListModels(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("expected exactly 1 model, got %d: %+v", len(resp.Models), resp.Models)
	}
	if resp.Models[0].CanonicalModelID != "sagemaker/ep-a" {
		t.Fatalf("CanonicalModelID=%q want %q", resp.Models[0].CanonicalModelID, "sagemaker/ep-a")
	}
	if resp.Models[0].NativeModelID != "ep-a" {
		t.Fatalf("NativeModelID=%q want %q", resp.Models[0].NativeModelID, "ep-a")
	}
	if !resp.Models[0].Capabilities.Streaming {
		t.Fatalf("provider streaming must be advertised in ListModels")
	}
}

// Execute(B) while configured for A fails closed with an explicit
// endpoint_name mismatch error and never invokes the runtime.
func TestF2_Execute_UnconfiguredEndpoint_FailsClosed(t *testing.T) {
	t.Parallel()
	r := &f2StubRuntime{}
	inst := f2Configure(t, r, "ep-a")

	stream := newTestExecuteStream(context.Background(), "sagemaker/ep-b", aipapi.OperationOpenAIChatCompletions, "hello", true, nil)
	err := inst.Execute(stream)
	if err == nil {
		t.Fatalf("expected Execute(sagemaker/ep-b) to fail closed while configured for ep-a")
	}
	if !strings.Contains(err.Error(), "does not match configured endpoint_name") {
		t.Fatalf("expected explicit endpoint_name mismatch error, got: %v", err)
	}
	if r.calls != 0 {
		t.Fatalf("unconfigured endpoint invoked runtime %d times; must fail before invoke", r.calls)
	}
}

// Provider streaming capability is advertised (InvokeEndpoint emits canonical events over a managed stream).
func TestF2_StreamingAdvertised(t *testing.T) {
	t.Parallel()
	svc := service.New(service.WithStaticClients(&f2StubRuntime{}))
	desc, err := svc.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe failed: %v", err)
	}
	if len(desc.Factories) != 1 {
		t.Fatalf("expected 1 factory, got %d", len(desc.Factories))
	}
	if !desc.Factories[0].StaticCapabilities.Streaming {
		t.Fatalf("Describe must advertise provider streaming")
	}

	r := &f2StubRuntime{}
	inst := f2Configure(t, r, "ep-a")
	profile, err := inst.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if !profile.Capabilities.Streaming {
		t.Fatalf("Resolve must advertise provider streaming")
	}
	listed, err := inst.ListModels(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if len(listed.Models) != 1 || !listed.Models[0].Capabilities.Streaming {
		t.Fatalf("ListModels must advertise exactly one streaming model, got %+v", listed.Models)
	}
}

// Streaming delivery requests are served via unary collect: the runtime
// InvokeEndpoint path succeeds and no event-stream API exists on the client.
func TestF2_StreamingDelivery_UsesUnaryCollect(t *testing.T) {
	t.Parallel()
	r := &f2StubRuntime{}
	inst := f2Configure(t, r, "ep-a")

	stream := newTestExecuteStream(context.Background(), "sagemaker/ep-a", aipapi.OperationOpenAIChatCompletions, "hello", false, nil)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("streaming-delivery Execute failed: %v", err)
	}
	if r.calls != 1 {
		t.Fatalf("expected exactly 1 unary runtime call, got %d", r.calls)
	}
	var sawDelta, sawTerminal bool
	for _, f := range stream.outbox {
		if f.Kind == backendplugin.ServerFrameEvent && f.Event != nil && f.Event.Kind == backendplugin.EventTextDelta && f.Event.Delta != nil && *f.Event.Delta == "ok" {
			sawDelta = true
		}
		if f.Kind == backendplugin.ServerFrameTerminal && f.Terminal != nil && f.Terminal.Status == backendplugin.TerminalSuccess {
			sawTerminal = true
		}
	}
	if !sawDelta {
		t.Fatalf("expected text delta \"ok\" via unary collect")
	}
	if !sawTerminal {
		t.Fatalf("expected terminal success")
	}
}

// f2MaxResponseBytes mirrors the connector's maxSageMakerResponseBytes bound
// (unary InvokeEndpoint bodies larger than this fail closed).
const f2MaxResponseBytes = 4 << 20

// f2ExactSizeDoc builds a valid hf-text-generation document of exactly size
// bytes: the padding lives in an ignored JSON field so the emitted text stays
// small while the raw body exercises the size bound precisely.
func f2ExactSizeDoc(t *testing.T, size int) []byte {
	t.Helper()
	const prefix = `{"generated_text":"ok","pad":"`
	const suffix = `"}`
	if size < len(prefix)+len(suffix) {
		t.Fatalf("size %d smaller than minimal document %d", size, len(prefix)+len(suffix))
	}
	doc := prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix
	if len(doc) != size {
		t.Fatalf("built document is %d bytes, want exactly %d", len(doc), size)
	}
	return []byte(doc)
}

// Unary responses at exactly the byte bound are accepted; anything larger
// fails closed on the explicit bound instead of growing an unbounded buffer.
func TestF2_UnaryResponseSizeBound(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		size      int
		wantDelta string // non-empty: Execute must succeed and emit this delta
		wantErr   string // non-empty: Execute must fail with this substring
	}{
		{name: "exact limit accepted", size: f2MaxResponseBytes, wantDelta: "ok"},
		{name: "limit plus one rejected", size: f2MaxResponseBytes + 1, wantErr: "exceeds limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := f2ExactSizeDoc(t, tc.size)
			r := &f2StubRuntime{
				invokeFn: func(context.Context, *sagemakerruntime.InvokeEndpointInput) (*sagemakerruntime.InvokeEndpointOutput, error) {
					return &sagemakerruntime.InvokeEndpointOutput{Body: body}, nil
				},
			}
			inst := f2Configure(t, r, "ep-a")

			stream := newTestExecuteStream(context.Background(), "sagemaker/ep-a", aipapi.OperationOpenAIChatCompletions, "hello", true, nil)
			err := inst.Execute(stream)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected %d-byte response to fail closed", tc.size)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected bounded exceeds-limit error, got: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected %d-byte response to be accepted, got: %v", tc.size, err)
			}
			var sawDelta, sawTerminal bool
			for _, f := range stream.outbox {
				if f.Kind == backendplugin.ServerFrameEvent && f.Event != nil && f.Event.Kind == backendplugin.EventTextDelta && f.Event.Delta != nil && *f.Event.Delta == tc.wantDelta {
					sawDelta = true
				}
				if f.Kind == backendplugin.ServerFrameTerminal && f.Terminal != nil && f.Terminal.Status == backendplugin.TerminalSuccess {
					sawTerminal = true
				}
			}
			if !sawDelta {
				t.Fatalf("expected text delta %q for exact-limit body", tc.wantDelta)
			}
			if !sawTerminal {
				t.Fatalf("expected terminal success for exact-limit body")
			}
		})
	}
}

// Candidate admission accepts both streaming and non-streaming delivery modes for SageMaker,
// and executing both through the connector delivers expected canonical events.
func TestF2_CandidateAdmission_StreamingAndNonStreaming(t *testing.T) {
	t.Parallel()
	r := &f2StubRuntime{}
	inst := f2Configure(t, r, "ep-a")

	profile, err := inst.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if !profile.Capabilities.Streaming {
		t.Fatalf("expected profile.Capabilities.Streaming=true")
	}
	if !profile.TransportCapabilities.BidirectionalStream {
		t.Fatalf("expected profile.TransportCapabilities.BidirectionalStream=true")
	}

	modes := []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming}
	transportCaps := aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Modes:     modes,
	})

	for _, tc := range []struct {
		name         string
		deliveryMode aipapi.DeliveryMode
		wantMode     aipapi.TransportMode
		nonStreaming bool
	}{
		{
			name:         "streaming",
			deliveryMode: aipapi.DeliveryModeStreaming,
			wantMode:     aipapi.TransportModeStreaming,
			nonStreaming: false,
		},
		{
			name:         "non_streaming",
			deliveryMode: aipapi.DeliveryModeNonStreaming,
			wantMode:     aipapi.TransportModeNonStreaming,
			nonStreaming: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := aipapi.Call{
				Invocation: aipapi.Invocation{
					Operation:    aipapi.OperationOpenAIChatCompletions,
					DeliveryMode: tc.deliveryMode,
				},
				Messages: []aipapi.Message{{
					Role:  aipapi.RoleUser,
					Parts: []aipapi.Part{aipapi.TextPart("admission test")},
				}},
			}
			admitRes := aipapi.AdmitCandidate(aipapi.CandidateAdmissionInput{
				Call:            call,
				Invocation:      call.Invocation,
				BackendCaps:     aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
				TransportCaps:   transportCaps,
				TransportPolicy: aipapi.TransportFallbackExact,
			})
			if admitRes.Kind != aipapi.NegotiationLossless {
				t.Fatalf("expected candidate admission lossless, got %v: %v", admitRes.Kind, admitRes.Transport.Err())
			}
			if admitRes.Transport.Selected != tc.wantMode {
				t.Fatalf("expected Selected=%s, got %s", tc.wantMode, admitRes.Transport.Selected)
			}

			stream := newTestExecuteStream(context.Background(), "sagemaker/ep-a", aipapi.OperationOpenAIChatCompletions, "admission test", tc.nonStreaming, nil)
			if err := inst.Execute(stream); err != nil {
				t.Fatalf("Execute failed for %s: %v", tc.name, err)
			}
			var sawDelta, sawTerminal bool
			for _, f := range stream.outbox {
				if f.Kind == backendplugin.ServerFrameEvent && f.Event != nil && f.Event.Kind == backendplugin.EventTextDelta && f.Event.Delta != nil && *f.Event.Delta == "ok" {
					sawDelta = true
				}
				if f.Kind == backendplugin.ServerFrameTerminal && f.Terminal != nil && f.Terminal.Status == backendplugin.TerminalSuccess {
					sawTerminal = true
				}
			}
			if !sawDelta {
				t.Fatalf("expected text delta for %s", tc.name)
			}
			if !sawTerminal {
				t.Fatalf("expected terminal success for %s", tc.name)
			}
		})
	}
}
