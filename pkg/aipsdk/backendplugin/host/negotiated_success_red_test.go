package host_test

import (
	"context"
	"io"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/host"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

func TestRED_NegotiatedNormalSuccessReachesTerminalWithoutClientCancel(t *testing.T) {
	t.Parallel()
	// Plugin service that uses ForwardExecute with negotiated handshake; upstream succeeds quickly.
	svc := &forwardingRedService{}
	lis := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	backendpluginv1.RegisterBackendPluginServer(grpcServer, backendplugin.NewGRPCServer(backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorCancellationHandshake,
		DisableTransportRetries: true,
		Features:                []backendplugin.Feature{{Name: backendplugin.FeatureCancellationHandshake}},
	}, svc))
	go func() { _ = grpcServer.Serve(lis) }()
	t.Cleanup(func() { grpcServer.Stop(); _ = lis.Close() })

	conn, err := lis.Dial()
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	sess, _, err := host.DialConfiguredSession(context.Background(), conn, "red", "fake", nil, backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true})
	if err != nil {
		_ = conn.Close()
		t.Fatalf("DialConfiguredSession: %v", err)
	}

	// Client stream: sends START then blocks on Recv without ever sending Cancel/CloseInput.
	// It implements OptionalExecuteStreamCloser so host's pump can be closed, but server's
	// grpcExecuteStream before the fix has no Closer, so ForwardExecute will deadlock after terminal.
	stream := &redClientStream{
		ctx: context.Background(),
		frames: []backendplugin.ClientFrame{
			{Kind: backendplugin.ClientFrameStart, InstanceID: "red", Invocation: validInvocation()},
		},
	}

	done := make(chan error, 1)
	go func() { done <- sess.Execute(stream) }()

	select {
	case err := <-done:
		if err != nil {
			_ = sess.Close(context.Background())
			_ = conn.Close()
			grpcServer.Stop()
			_ = lis.Close()
			t.Fatalf("Session.Execute error: %v", err)
		}
		// Check that client actually saw terminal.
		found := false
		for _, f := range stream.out {
			if f.Kind == backendplugin.ServerFrameTerminal && f.Terminal != nil && f.Terminal.Status == backendplugin.TerminalSuccess {
				found = true
			}
		}
		if !found {
			_ = sess.Close(context.Background())
			_ = conn.Close()
			grpcServer.Stop()
			_ = lis.Close()
			t.Fatalf("client did not receive terminal success, out=%v", stream.out)
		}
		_ = sess.Close(context.Background())
		_ = conn.Close()
		grpcServer.Stop()
		_ = lis.Close()
	case <-time.After(3 * time.Second):
		// Dump goroutines for diagnosis.
		buf := make([]byte, 2<<20)
		n := runtime.Stack(buf, true)
		s := string(buf[:n])
		t.Logf("goroutine dump:\n%s", s)
		if !strings.Contains(s, "forwardActiveExecute") && !strings.Contains(s, "grpcExecuteStream") {
			t.Logf("no forwardActiveExecute found in dump")
		}
		grpcServer.Stop()
		_ = conn.Close()
		_ = lis.Close()
		t.Fatal("RED: Session.Execute deadlocked on negotiated normal success - server control reader blocked waiting for client Recv EOF (terminal/handler circular wait)")
	}
}

type forwardingRedService struct{}

func (s *forwardingRedService) Describe(_ context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1, ProtocolMinor: backendplugin.ProtocolMinorCancellationHandshake,
		PluginID: "red-fake", Version: "v1",
		Features:  []backendplugin.Feature{{Name: backendplugin.FeatureCancellationHandshake}},
		Factories: []backendplugin.FactoryDescriptor{{Kind: "fake", StaticCapabilities: backendplugin.CapabilitySummary{Streaming: true}}},
	}, nil
}

func (s *forwardingRedService) Configure(_ context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	return &forwardingRedInstance{}, nil
}

type forwardingRedInstance struct{}

func (f *forwardingRedInstance) Resolve(_ context.Context, _ *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{EvidenceSource: "red-fake", Capabilities: backendplugin.CapabilitySummary{Streaming: true}}, nil
}

func (f *forwardingRedInstance) ListModels(_ context.Context, _ uint32) (backendplugin.ListModelsResponse, error) {
	return backendplugin.ListModelsResponse{}, nil
}

func (f *forwardingRedInstance) Execute(stream backendplugin.ExecuteStream) error {
	return backendplugin.ForwardExecute(stream, func(_ context.Context, _ backendplugin.Invocation, _ aipapi.Call) (aipapi.ManagedEventStream, error) {
		return &redUpstream{events: []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "red-hi"}}}, nil
	})
}
func (f *forwardingRedInstance) Close(_ context.Context) error { return nil }

type redUpstream struct {
	events []aipapi.Event
	idx    int
}

func (m *redUpstream) Recv(_ context.Context) (aipapi.Event, error) {
	if m.idx >= len(m.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := m.events[m.idx]
	m.idx++
	return ev, nil
}
func (m *redUpstream) Close() error { return nil }
func (m *redUpstream) Cancel(_ context.Context, _ aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

type redClientStream struct {
	ctx     context.Context
	frames  []backendplugin.ClientFrame
	pos     int
	out     []backendplugin.ServerFrame
	closeCh chan struct{}
}

func (s *redClientStream) ensureCloseCh() {
	if s.closeCh == nil {
		s.closeCh = make(chan struct{})
	}
}

func (s *redClientStream) Context() context.Context { return s.ctx }
func (s *redClientStream) Recv() (backendplugin.ClientFrame, error) {
	s.ensureCloseCh()
	if s.pos >= len(s.frames) {
		select {
		case <-s.closeCh:
			return backendplugin.ClientFrame{}, io.EOF
		case <-s.ctx.Done():
			return backendplugin.ClientFrame{}, s.ctx.Err()
		}
	}
	f := s.frames[s.pos]
	s.pos++
	return f, nil
}

func (s *redClientStream) Send(frame backendplugin.ServerFrame) error {
	s.out = append(s.out, frame)
	return nil
}

func (s *redClientStream) Close() error {
	s.ensureCloseCh()
	select {
	case <-s.closeCh:
	default:
		close(s.closeCh)
	}
	return nil
}

var _ net.Conn = nil // satisfy import

func ensureRedImports() {
	_ = backendplugin.ProtocolMinorCancellationHandshake
}
