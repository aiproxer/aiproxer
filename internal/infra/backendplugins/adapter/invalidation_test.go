package adapter_test

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	testkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/host"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestInvalidation_BufconnTransportDeath(t *testing.T) {
	t.Parallel()
	lis := bufconn.Listen(1 << 20)
	offer := backendplugin.ProtocolOffer{
		Major: 1, Minor: 0, DisableTransportRetries: true,
		Features: []backendplugin.Feature{{Name: "count_tokens"}, {Name: "finalize_billing"}},
	}
	gs := grpc.NewServer()
	backendpluginv1.RegisterBackendPluginServer(gs, backendplugin.NewGRPCServer(offer, &testkit.FakeService{
		Mode: testkit.ModeBlockedCancel, SlowWait: time.Hour,
	}))
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(func() { _ = lis.Close() })

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.NewClient("passthrough:///buf-death", grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := backendpluginv1.NewBackendPluginClient(conn)
	neg, err := client.Negotiate(context.Background(), &backendpluginv1.NegotiateRequest{
		HostMajor: 1, HostMinor: 0, DisableTransportRetries: true,
		HostFeatures: []*backendpluginv1.Feature{{Name: "count_tokens"}, {Name: "finalize_billing"}},
	})
	if err != nil || !neg.GetCompatible() {
		t.Fatalf("negotiate: %+v %v", neg, err)
	}
	_, err = client.Configure(context.Background(), &backendpluginv1.ConfigureRequest{
		InstanceId: "death", FactoryKind: "fake", ConfigYaml: []byte("k: v\n"),
		NegotiationToken: neg.GetNegotiationToken(),
		RuntimePolicy:    &backendpluginv1.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	var invalidated atomic.Int64
	sess := host.NewSessionForTesting(client, conn, "death", backendplugin.Negotiation{})
	profile, err := sess.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	br := adapter.Build(sess, profile, adapter.Options{
		InstanceID:           "death",
		InvalidateGeneration: func() { invalidated.Add(1) },
	})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	go func() {
		time.Sleep(30 * time.Millisecond)
		gs.Stop()
	}()

	_, err = stream.Recv(context.Background())
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatal("expected transport death error")
	}
	var ce *adapter.ClassifiedError
	if !errors.As(err, &ce) || ce.Code != "transport" {
		t.Fatalf("want transport ClassifiedError, got %T %v", err, err)
	}
	if invalidated.Load() != 1 {
		t.Fatalf("InvalidateGeneration count=%d want 1", invalidated.Load())
	}
}

func TestInvalidation_ProtocolViolationDuplicateTerminal(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeDuplicateTerminal}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "dup-inv", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	var invalidated atomic.Int64
	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID:           "dup-inv",
		InvalidateGeneration: func() { invalidated.Add(1) },
	})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	_, err = stream.Recv(context.Background())
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatal("expected protocol failure")
	}
	var ce *adapter.ClassifiedError
	if !errors.As(err, &ce) || ce.Code != "protocol" {
		t.Fatalf("want protocol ClassifiedError, got %T %v", err, err)
	}
	if invalidated.Load() != 1 {
		t.Fatalf("InvalidateGeneration count=%d want 1", invalidated.Load())
	}
}

func TestInvalidation_MalformedFrameViaBufconn(t *testing.T) {
	t.Parallel()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	backendpluginv1.RegisterBackendPluginServer(gs, &malformedFrameServer{})
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(func() {
		gs.Stop()
		_ = lis.Close()
	})

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.NewClient("passthrough:///buf-mal", grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := backendpluginv1.NewBackendPluginClient(conn)

	var invalidated atomic.Int64
	sess := host.NewSessionForTesting(client, conn, "mal", backendplugin.Negotiation{})
	br := adapter.Build(sess, backendplugin.ResolvedProfile{
		Capabilities: backendplugin.CapabilitySummary{Streaming: true},
	}, adapter.Options{
		InstanceID:           "mal",
		InvalidateGeneration: func() { invalidated.Add(1) },
	})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	_, err = stream.Recv(context.Background())
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatal("expected protocol/malformed failure")
	}
	if invalidated.Load() != 1 {
		t.Fatalf("InvalidateGeneration count=%d want 1", invalidated.Load())
	}
}

func TestInvalidation_ContextCancelDoesNotInvalidate(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeBlockedCancel, SlowWait: time.Hour}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "cancel-inv", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	var invalidated atomic.Int64
	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID:           "cancel-inv",
		InvalidateGeneration: func() { invalidated.Add(1) },
	})
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := br.Backend.Open(ctx, testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	cancel()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for cancel")
		default:
		}
		_, err := stream.Recv(context.Background())
		if err != nil {
			break
		}
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if invalidated.Load() != 0 {
		t.Fatalf("cancel must not invalidate, got %d", invalidated.Load())
	}
}

func TestClassifyExecuteError_Kinds(t *testing.T) {
	t.Parallel()
	if k := adapter.ClassifyExecuteError(backendplugin.ErrMultipleTerminals, false); !k.InvalidatesGeneration() || k.Kind != adapter.ExecuteFailureProtocolViolation {
		t.Fatalf("%+v", k)
	}
	if k := adapter.ClassifyExecuteError(context.Canceled, false); k.InvalidatesGeneration() || k.Kind != adapter.ExecuteFailureCanceled {
		t.Fatalf("%+v", k)
	}
	if k := adapter.ClassifyExecuteError(adapter.TransportDeath(io.EOF), true); !k.InvalidatesGeneration() || !k.OutputCommitted {
		t.Fatalf("%+v", k)
	}
	if k := adapter.ClassifyExecuteError(backendplugin.ModeError{Code: "any"}, false); !k.InvalidatesGeneration() || k.Kind != adapter.ExecuteFailureTransportDeath {
		t.Fatalf("ModeError must classify as transport death without fake-code matching: %+v", k)
	}
}

type malformedFrameServer struct {
	backendpluginv1.UnimplementedBackendPluginServer
}

func (m *malformedFrameServer) Execute(stream backendpluginv1.BackendPlugin_ExecuteServer) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	if err := stream.Send(&backendpluginv1.ExecuteServerFrame{
		Kind: backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_ACCEPTED,
	}); err != nil {
		return err
	}
	return stream.Send(&backendpluginv1.ExecuteServerFrame{
		Kind:     backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_EVENT,
		Sequence: 1,
		// Missing event payload → invalid/malformed frame after decode.
	})
}

func TestStream_OversizeOpaqueToolDiagnostic(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		frame backendplugin.ServerFrame
	}{
		{
			name: "opaque",
			frame: backendplugin.ServerFrame{
				Kind: backendplugin.ServerFrameEvent, Sequence: 1,
				Event: &backendplugin.CanonicalEvent{
					Kind: backendplugin.EventReasoningOpaqueDelta, Opaque: []byte(strings.Repeat("x", 200)),
				},
			},
		},
		{
			name: "tool_args",
			frame: func() backendplugin.ServerFrame {
				d := strings.Repeat("y", 200)
				id := "t1"
				return backendplugin.ServerFrame{
					Kind: backendplugin.ServerFrameEvent, Sequence: 1,
					Event: &backendplugin.CanonicalEvent{Kind: backendplugin.EventToolCallArgsDelta, ToolCallID: &id, Delta: &d},
				}
			}(),
		},
		{
			name: "diagnostic",
			frame: backendplugin.ServerFrame{
				Kind: backendplugin.ServerFrameDiagnostic, Sequence: 1,
				Diagnostic: strings.Repeat("z", 200),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sess := &scriptedSession{frames: []backendplugin.ServerFrame{
				{Kind: backendplugin.ServerFrameAccepted},
				tc.frame,
			}}
			var invalidated atomic.Int64
			br := adapter.Build(sess, backendplugin.ResolvedProfile{
				Capabilities: backendplugin.CapabilitySummary{Streaming: true},
			}, adapter.Options{
				InstanceID: "osz-" + tc.name, MaxStreamFrame: 64,
				InvalidateGeneration: func() { invalidated.Add(1) },
			})
			stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = stream.Close() })
			_, err = stream.Recv(context.Background())
			if err == nil || errors.Is(err, io.EOF) {
				t.Fatal("expected oversize failure")
			}
			var ce *adapter.ClassifiedError
			if !errors.As(err, &ce) || ce.Code != "protocol" {
				t.Fatalf("want protocol, got %T %v", err, err)
			}
			if invalidated.Load() != 1 {
				t.Fatalf("invalidate=%d", invalidated.Load())
			}
		})
	}
}

type scriptedSession struct {
	frames []backendplugin.ServerFrame
}

func (s *scriptedSession) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{Capabilities: backendplugin.CapabilitySummary{Streaming: true}}, nil
}

func (s *scriptedSession) ListModels(context.Context, uint32) (backendplugin.ListModelsResponse, error) {
	return backendplugin.ListModelsResponse{}, nil
}
func (s *scriptedSession) Close(context.Context) error { return nil }
func (s *scriptedSession) Execute(stream backendplugin.ExecuteStream) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	for _, fr := range s.frames {
		if err := stream.Send(fr); err != nil {
			return err
		}
	}
	return stream.Send(backendplugin.ServerFrame{
		Kind: backendplugin.ServerFrameTerminal, Sequence: uint64(len(s.frames)),
		Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
	})
}
