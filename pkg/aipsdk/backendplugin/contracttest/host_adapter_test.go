package contracttest

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	testkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/host"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// TestRun_UsesRealHostAdapter proves the public entry point exercises the same
// GRPC host adapter used by executable connectors, rather than an optional seam.
//
//nolint:paralleltest // owns a fixed in-memory gRPC server lifecycle.
func TestRun_UsesRealHostAdapter(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	offer := backendplugin.ProtocolOffer{Major: 1, Minor: backendplugin.ProtocolMinorExactOpenResponsesFields, DisableTransportRetries: true, Features: []backendplugin.Feature{{Name: backendplugin.FeatureOrderedItems}}}
	backendpluginv1.RegisterBackendPluginServer(server, backendplugin.NewGRPCServer(offer, &testkit.FakeService{Mode: testkit.ModeValid}))
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(func() { server.Stop(); _ = lis.Close() })
	conn, err := lis.Dial()
	if err != nil {
		t.Fatal(err)
	}
	session, profile, err := adapter.DialConfiguredSession(context.Background(), conn, "contract", "fake", []byte("kind: fake\n"), backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	if profile.EvidenceSource == "" {
		t.Fatal("resolved profile lacks evidence source")
	}
	call := aipapi.Call{ID: "tck", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIResponses, TransportMode: aipapi.TransportModeStreaming}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}}}
	backend := adapter.Build(session, profile, adapter.Options{InstanceID: "contract", RoutePrefixes: []string{"fake"}}).Backend
	stream, err := backend.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Backend: "fake", Model: "fake-model"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	var text, usage bool
	for {
		ev, recvErr := stream.Recv(context.Background())
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		text = text || ev.Kind == aipapi.EventTextDelta
		usage = usage || ev.Kind == aipapi.EventUsageDelta
	}
	if !text || !usage {
		t.Fatalf("host adapter lost canonical evidence: text=%v usage=%v", text, usage)
	}
}

//nolint:paralleltest // owns a fixed in-memory gRPC server lifecycle.
func TestRun_Minor6SemanticExtensionCarrier(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	offer := backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorSemanticExtensions, DisableTransportRetries: true,
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureExactReasoningParts},
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureExactOpenResponsesFields},
			{Name: backendplugin.FeatureProxyOwnedSessionID},
			{Name: backendplugin.FeatureAccountingEvidence},
			{Name: backendplugin.FeatureSemanticExtensions},
		},
	}
	fakeSvc := &testkit.FakeService{
		Mode:          testkit.ModeValid,
		ProtocolMinor: backendplugin.ProtocolMinorSemanticExtensions,
		ExtraFeatures: []backendplugin.Feature{
			{Name: backendplugin.FeatureExactReasoningParts},
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureExactOpenResponsesFields},
			{Name: backendplugin.FeatureProxyOwnedSessionID},
			{Name: backendplugin.FeatureAccountingEvidence},
			{Name: backendplugin.FeatureSemanticExtensions},
		},
	}
	backendpluginv1.RegisterBackendPluginServer(server, backendplugin.NewGRPCServer(offer, fakeSvc))
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(func() { server.Stop(); _ = lis.Close() })
	conn, err := lis.Dial()
	if err != nil {
		t.Fatal(err)
	}
	session, profile, err := adapter.DialConfiguredSession(context.Background(), conn, "minor6-contract", "fake", []byte("kind: fake\n"), backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })

	call := aipapi.Call{
		ID: "tck-minor6", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIResponses, TransportMode: aipapi.TransportModeStreaming},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		SemanticExtensions: []aipapi.SemanticExtension{
			{Namespace: "aip", Type: "prompt_cache_key", Implementor: "proxy", Direction: "request", Presence: aipapi.SemanticExtensionValue, Data: []byte(`"cache-minor6"`)},
		},
	}
	backend := adapter.Build(session, profile, adapter.Options{InstanceID: "minor6-contract", RoutePrefixes: []string{"fake"}}).Backend
	stream, err := backend.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Backend: "fake", Model: "fake-model"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	for {
		_, recvErr := stream.Recv(context.Background())
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatal(recvErr)
		}
	}
	lastInv := fakeSvc.LastStartInvocation
	if lastInv == nil || len(lastInv.SemanticExtensions) != 1 || string(lastInv.SemanticExtensions[0].Data.Bytes()) != `"cache-minor6"` {
		t.Fatalf("minor-6 connector TCK carrier not transmitted: %#v", lastInv)
	}
}

//nolint:paralleltest // owns a fixed in-memory gRPC server lifecycle.
func TestRun_Minor6SemanticExtensionCarrier_NegotiationMismatch(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	offer := backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorAccountingEvidence, DisableTransportRetries: true,
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureAccountingEvidence},
		},
	}
	fakeSvc := &testkit.FakeService{
		Mode:          testkit.ModeValid,
		ProtocolMinor: backendplugin.ProtocolMinorAccountingEvidence,
	}
	backendpluginv1.RegisterBackendPluginServer(server, backendplugin.NewGRPCServer(offer, fakeSvc))
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(func() { server.Stop(); _ = lis.Close() })

	conn, err := lis.Dial()
	if err != nil {
		t.Fatal(err)
	}
	session, profile, err := adapter.DialConfiguredSession(context.Background(), conn, "minor6-mismatch", "fake", []byte("kind: fake\n"), backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })

	call := aipapi.Call{
		ID: "tck-minor6-mismatch", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIResponses, TransportMode: aipapi.TransportModeStreaming},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		SemanticExtensions: []aipapi.SemanticExtension{
			{Namespace: "aip", Type: "prompt_cache_key", Implementor: "proxy", Direction: "request", Presence: aipapi.SemanticExtensionValue, Data: []byte(`"cache-minor6"`)},
		},
	}
	backend := adapter.Build(session, profile, adapter.Options{InstanceID: "minor6-mismatch", RoutePrefixes: []string{"fake"}, Negotiation: session.Negotiation()}).Backend
	_, err = backend.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Backend: "fake", Model: "fake-model"}})
	if err == nil {
		t.Fatal("expected Open to fail due to semantic extension ABI negotiation mismatch on Minor 5")
	}
}

//nolint:paralleltest // owns a fixed in-memory gRPC server lifecycle.
func TestRun_Minor6SemanticExtensionCarrier_ErrorPropagation(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	offer := backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorSemanticExtensions, DisableTransportRetries: true,
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureExactReasoningParts},
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureExactOpenResponsesFields},
			{Name: backendplugin.FeatureProxyOwnedSessionID},
			{Name: backendplugin.FeatureAccountingEvidence},
			{Name: backendplugin.FeatureSemanticExtensions},
		},
	}
	fakeSvc := &testkit.FakeService{
		Mode:          testkit.ModeMalformedFrame,
		ProtocolMinor: backendplugin.ProtocolMinorSemanticExtensions,
		ExtraFeatures: []backendplugin.Feature{
			{Name: backendplugin.FeatureExactReasoningParts},
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureExactOpenResponsesFields},
			{Name: backendplugin.FeatureProxyOwnedSessionID},
			{Name: backendplugin.FeatureAccountingEvidence},
			{Name: backendplugin.FeatureSemanticExtensions},
		},
	}
	backendpluginv1.RegisterBackendPluginServer(server, backendplugin.NewGRPCServer(offer, fakeSvc))
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(func() { server.Stop(); _ = lis.Close() })

	conn, err := lis.Dial()
	if err != nil {
		t.Fatal(err)
	}
	session, profile, err := adapter.DialConfiguredSession(context.Background(), conn, "minor6-err", "fake", []byte("kind: fake\n"), backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })

	call := aipapi.Call{
		ID: "tck-minor6-err", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIResponses, TransportMode: aipapi.TransportModeStreaming},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		SemanticExtensions: []aipapi.SemanticExtension{
			{Namespace: "aip", Type: "prompt_cache_key", Implementor: "proxy", Direction: "request", Presence: aipapi.SemanticExtensionValue, Data: []byte(`"cache-minor6"`)},
		},
	}
	backend := adapter.Build(session, profile, adapter.Options{InstanceID: "minor6-err", RoutePrefixes: []string{"fake"}, Negotiation: session.Negotiation()}).Backend
	stream, err := backend.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Backend: "fake", Model: "fake-model"}})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	_, recvErr := stream.Recv(context.Background())
	if recvErr == nil {
		t.Fatal("expected error propagated from ModeFail fake service")
	}
}

//nolint:paralleltest // owns a fixed in-memory gRPC server lifecycle.
func TestRun_PostTerminalRecv_ReturnsEOF(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	offer := backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorAccountingEvidence, DisableTransportRetries: true,
		Features: []backendplugin.Feature{{Name: backendplugin.FeatureOrderedItems}},
	}
	fakeSvc := &testkit.FakeService{Mode: testkit.ModeValid}
	backendpluginv1.RegisterBackendPluginServer(server, backendplugin.NewGRPCServer(offer, fakeSvc))
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(func() { server.Stop(); _ = lis.Close() })

	conn, err := lis.Dial()
	if err != nil {
		t.Fatal(err)
	}
	session, profile, err := adapter.DialConfiguredSession(context.Background(), conn, "post-term", "fake", []byte("kind: fake\n"), backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })

	call := aipapi.Call{
		ID: "tck-post-term", Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenResponsesCreate, TransportMode: aipapi.TransportModeStreaming},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
	}
	backend := adapter.Build(session, profile, adapter.Options{InstanceID: "post-term", RoutePrefixes: []string{"fake"}, Negotiation: session.Negotiation()}).Backend
	stream, err := backend.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Backend: "fake", Model: "fake-model"}})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	for {
		_, recvErr := stream.Recv(context.Background())
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("unexpected Recv error before EOF: %v", recvErr)
		}
	}

	_, postErr := stream.Recv(context.Background())
	if !errors.Is(postErr, io.EOF) {
		t.Fatalf("post-terminal Recv() error = %v, want io.EOF", postErr)
	}
}

//nolint:paralleltest // owns a fixed in-memory gRPC server lifecycle.
func TestRun_TerminalScenariosInheritCorpusCoverage(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	offer := backendplugin.ProtocolOffer{Major: 1, Minor: backendplugin.ProtocolMinorCancellationHandshake, DisableTransportRetries: true, Features: []backendplugin.Feature{{Name: backendplugin.FeatureOrderedItems}}}
	fakeSvc := &testkit.FakeService{Mode: testkit.ModeValid}
	backendpluginv1.RegisterBackendPluginServer(server, backendplugin.NewGRPCServer(offer, fakeSvc))
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(func() { server.Stop(); _ = lis.Close() })
	conn, err := lis.Dial()
	if err != nil {
		t.Fatal(err)
	}
	result := Run(t, Config{
		PluginID:    "com.aiproxer.fake",
		Version:     "test",
		Timeout:     60 * time.Second,
		FactoryKind: "fake",
		ConfigYAML:  []byte("kind: fake\n"),
		Start: func(context.Context) (backendplugin.Service, func(), error) {
			return fakeSvc, nil, nil
		},
		StartHost: func(ctx context.Context, _ backendplugin.Service) (HostSession, func(), error) {
			sess, _, err := host.DialConfiguredSession(ctx, conn, "contract", "fake", []byte("kind: fake\n"), backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8})
			if err != nil {
				return nil, nil, err
			}
			return sess, func() {}, nil
		},
	})
	if err := result.Validate(); err != nil {
		t.Fatalf("reference certification invalid: %v", err)
	}
	byID := make(map[string]ScenarioResult, len(result.ScenarioResults))
	for _, r := range result.ScenarioResults {
		byID[r.ID] = r
	}
	for _, id := range []string{"terminal-clean-text", "terminal-clean-tool-call", "terminal-output-limit", "terminal-upstream-error", "terminal-usage-reported"} {
		r, ok := byID[id]
		if !ok {
			t.Fatalf("terminal scenario %q missing from certification", id)
		}
		if !r.Positive || !r.Executed || !r.Terminal || r.FramesValidated == 0 {
			t.Fatalf("terminal scenario %q lacks positive terminal evidence: %+v", id, r)
		}
	}
	// Ordering is structural: the runner records a failure for any frame
	// observed after the terminal frame, so usage/error flags on this
	// validated artifact imply observation before the terminal frame.
	if u := byID["terminal-usage-reported"]; !u.UsageObserved {
		t.Fatalf("terminal-usage-reported lacks usage evidence: %+v", u)
	}
	if e := byID["terminal-upstream-error"]; !e.ErrorObserved {
		t.Fatalf("terminal-upstream-error lacks error evidence: %+v", e)
	}
}

type cancellationMockManagedStream struct {
	mode         aipapi.CancelMode
	unblocked    chan struct{}
	cancelCalled atomic.Bool
	cancelCause  aipapi.CancelCause
	closeOnce    sync.Once
}

func (m *cancellationMockManagedStream) Recv(ctx context.Context) (aipapi.Event, error) {
	select {
	case <-m.unblocked:
		return aipapi.Event{}, context.Canceled
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	}
}

func (m *cancellationMockManagedStream) Close() error { return nil }

func (m *cancellationMockManagedStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	m.cancelCalled.Store(true)
	m.cancelCause = cause
	m.closeOnce.Do(func() { close(m.unblocked) })
	return aipapi.CancelResult{Mode: m.mode}
}

type cancellationHandshakeService struct {
	mode       aipapi.CancelMode
	lastStream *cancellationMockManagedStream
	mu         sync.Mutex
}

func (s *cancellationHandshakeService) Describe(ctx context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1,
		ProtocolMinor: backendplugin.ProtocolMinorCancellationHandshake,
		PluginID:      "com.aiproxer.test.cancel",
		Version:       "0.0.1",
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureCancellationHandshake},
		},
		Factories: []backendplugin.FactoryDescriptor{{
			Kind:           "cancel-test",
			CredentialMode: backendplugin.CredentialModeNone,
			AccessScope:    backendplugin.AccessScopeLocalOnly,
			ProcessSharing: backendplugin.ProcessSharingPerInstance,
			StaticCapabilities: backendplugin.CapabilitySummary{
				Streaming: true,
			},
			TransportCapabilities: backendplugin.TransportCapabilitySummary{
				Cancellation:        true,
				BidirectionalStream: true,
			},
		}},
	}, nil
}

func (s *cancellationHandshakeService) Configure(ctx context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	return &cancellationHandshakeInstance{service: s, mode: s.mode}, nil
}

type cancellationHandshakeInstance struct {
	service *cancellationHandshakeService
	mode    aipapi.CancelMode
}

func (inst *cancellationHandshakeInstance) Resolve(ctx context.Context, modelID *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{
		Capabilities:          backendplugin.CapabilitySummary{Streaming: true},
		TransportCapabilities: backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
	}, nil
}

func (inst *cancellationHandshakeInstance) ListModels(ctx context.Context, maxModels uint32) (backendplugin.ListModelsResponse, error) {
	return backendplugin.ListModelsResponse{}, nil
}

func (inst *cancellationHandshakeInstance) Close(ctx context.Context) error { return nil }

func (inst *cancellationHandshakeInstance) Execute(stream backendplugin.ExecuteStream) error {
	return backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
		ms := &cancellationMockManagedStream{
			mode:      inst.mode,
			unblocked: make(chan struct{}),
		}
		inst.service.mu.Lock()
		inst.service.lastStream = ms
		inst.service.mu.Unlock()
		return ms, nil
	})
}

//nolint:paralleltest // owns a fixed in-memory gRPC server lifecycle.
func TestRun_Minor8CancellationHandshake(t *testing.T) {
	modes := []aipapi.CancelMode{
		aipapi.CancelModeProvider,
		aipapi.CancelModeTransport,
		aipapi.CancelModeCloseOnly,
	}

	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			lis := bufconn.Listen(1 << 20)
			server := grpc.NewServer()
			offer := backendplugin.ProtocolOffer{
				Major: 1, Minor: backendplugin.ProtocolMinorCancellationHandshake, DisableTransportRetries: true,
				Features: []backendplugin.Feature{
					{Name: backendplugin.FeatureOrderedItems},
					{Name: backendplugin.FeatureCancellationHandshake},
				},
			}
			svc := &cancellationHandshakeService{mode: mode}
			backendpluginv1.RegisterBackendPluginServer(server, backendplugin.NewGRPCServer(offer, svc))
			go func() { _ = server.Serve(lis) }()
			t.Cleanup(func() { server.Stop(); _ = lis.Close() })

			gconn, err := grpc.NewClient("passthrough:///bufnet",
				grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
					return lis.Dial()
				}),
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = gconn.Close() })

			client := backendpluginv1.NewBackendPluginClient(gconn)
			negResp, err := client.Negotiate(context.Background(), &backendpluginv1.NegotiateRequest{
				HostMajor: 1,
				HostMinor: backendplugin.ProtocolMinorCancellationHandshake,
				HostFeatures: []*backendpluginv1.Feature{
					{Name: backendplugin.FeatureOrderedItems},
					{Name: backendplugin.FeatureCancellationHandshake},
				},
				DisableTransportRetries: true,
			})
			if err != nil {
				t.Fatalf("Negotiate failed: %v", err)
			}
			if !negResp.GetCompatible() || negResp.GetNegotiatedMinor() != backendplugin.ProtocolMinorCancellationHandshake {
				t.Fatalf("expected negotiated minor 8, got %+v", negResp)
			}

			_, err = client.Configure(context.Background(), &backendpluginv1.ConfigureRequest{
				InstanceId:       "minor8-cancel",
				FactoryKind:      "cancel-test",
				NegotiationToken: negResp.GetNegotiationToken(),
				RuntimePolicy: &backendpluginv1.RuntimePolicy{
					DisableTransportRetries: true,
				},
			})
			if err != nil {
				t.Fatalf("Configure failed: %v", err)
			}

			stream, err := client.Execute(context.Background())
			if err != nil {
				t.Fatalf("Execute failed: %v", err)
			}

			text := "hello"
			err = stream.Send(&backendpluginv1.ExecuteClientFrame{
				Kind:       backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_START,
				InstanceId: "minor8-cancel",
				Invocation: &backendpluginv1.Invocation{
					RequestId: "req-1", AttemptId: "att-1", ALegId: "aleg-1", BLegId: "bleg-1", CanonicalModelId: "cancel-model",
					Messages: []*backendpluginv1.Message{{
						Role: backendpluginv1.Role_ROLE_USER,
						Parts: []*backendpluginv1.Part{{
							Kind: backendpluginv1.PartKind_PART_KIND_TEXT, Text: &text,
						}},
					}},
				},
			})
			if err != nil {
				t.Fatalf("Send START failed: %v", err)
			}

			// Recv Accepted
			f1, err := stream.Recv()
			if err != nil || f1.GetKind() != backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_ACCEPTED {
				t.Fatalf("expected Accepted frame, got %+v, err: %v", f1, err)
			}

			// Send CANCEL
			err = stream.Send(&backendpluginv1.ExecuteClientFrame{
				Kind:         backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_CANCEL,
				InstanceId:   "minor8-cancel",
				CancelReason: backendpluginv1.CancelReason_CANCEL_REASON_CLIENT,
			})
			if err != nil {
				t.Fatalf("Send CANCEL failed: %v", err)
			}

			// Recv CancelOutcome
			f2, err := stream.Recv()
			if err != nil || f2.GetKind() != backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_CANCEL_OUTCOME {
				t.Fatalf("expected CancelOutcome frame, got %+v, err: %v", f2, err)
			}
			if f2.GetSequence() != 1 {
				t.Errorf("CancelOutcome sequence = %d, want 1", f2.GetSequence())
			}
			if f2.GetCancelOutcome() == nil || !f2.GetCancelOutcome().GetAcknowledged() {
				t.Errorf("expected Acknowledged CancelOutcome, got %+v", f2.GetCancelOutcome())
			}
			var wantMode backendpluginv1.CancelMode
			switch mode {
			case aipapi.CancelModeProvider:
				wantMode = backendpluginv1.CancelMode_CANCEL_MODE_PROVIDER
			case aipapi.CancelModeTransport:
				wantMode = backendpluginv1.CancelMode_CANCEL_MODE_TRANSPORT
			case aipapi.CancelModeCloseOnly:
				wantMode = backendpluginv1.CancelMode_CANCEL_MODE_CLOSE_ONLY
			}
			if f2.GetCancelOutcome().GetMode() != wantMode {
				t.Errorf("CancelOutcome Mode = %v, want %v", f2.GetCancelOutcome().GetMode(), wantMode)
			}

			// Recv Terminal
			f3, err := stream.Recv()
			if err != nil || f3.GetKind() != backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_TERMINAL {
				t.Fatalf("expected Terminal frame, got %+v, err: %v", f3, err)
			}
			if f3.GetSequence() != 2 {
				t.Errorf("Terminal sequence = %d, want 2", f3.GetSequence())
			}
			if f3.GetTerminal() == nil || f3.GetTerminal().GetStatus() != backendpluginv1.TerminalStatus_TERMINAL_STATUS_CANCELLED {
				t.Errorf("expected TerminalStatus_TERMINAL_STATUS_CANCELLED, got %+v", f3.GetTerminal())
			}

			svc.mu.Lock()
			lastStream := svc.lastStream
			svc.mu.Unlock()

			if lastStream == nil || !lastStream.cancelCalled.Load() {
				t.Fatal("expected upstream Cancel to be called via ForwardExecute")
			}
		})
	}
}
