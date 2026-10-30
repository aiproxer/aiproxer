package host_test

import (
	"context"
	"errors"
	"io"
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

func TestRED_NegotiatedUpstreamErrorReachesTerminalWithoutClientCancel(t *testing.T) {
	t.Parallel()
	svc := &errorForwardingRedService{upstreamErr: errors.New("upstream boom sk-or-v1-SECRET-1234567890")}
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
	sess, _, err := host.DialConfiguredSession(context.Background(), conn, "red-err", "fake", nil, backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true})
	if err != nil {
		_ = conn.Close()
		t.Fatalf("DialConfiguredSession: %v", err)
	}
	stream := &redClientStream{
		ctx: context.Background(),
		frames: []backendplugin.ClientFrame{
			{Kind: backendplugin.ClientFrameStart, InstanceID: "red-err", Invocation: validInvocation()},
		},
	}
	done := make(chan error, 1)
	go func() { done <- sess.Execute(stream) }()
	select {
	case err := <-done:
		// Host may return nil (terminal already communicated) or transport_death / error wrapped.
		// The key is it returns promptly and terminal was delivered.
		_ = err
		found := false
		for _, f := range stream.out {
			if f.Kind == backendplugin.ServerFrameTerminal && f.Terminal != nil {
				if f.Terminal.Status == backendplugin.TerminalFailure && f.Terminal.Error != nil {
					found = true
					if f.Terminal.Error.Message != "upstream execution failed" {
						t.Fatalf("terminal error message = %q, want %q", f.Terminal.Error.Message, "upstream execution failed")
					}
					if f.Terminal.Error.Code != backendplugin.ErrorCodeInternal {
						t.Fatalf("terminal error code = %q, want %q", f.Terminal.Error.Code, backendplugin.ErrorCodeInternal)
					}
					if f.Terminal.Error.Retryable {
						t.Fatalf("terminal error must not be retryable")
					}
					if strings.Contains(f.Terminal.Error.Message, "sk-or-v1-SECRET") {
						t.Fatalf("terminal error leaked secret: %q", f.Terminal.Error.Message)
					}
				} else if f.Terminal.Status == backendplugin.TerminalFailure {
					t.Fatalf("terminal failure must carry constant safe error, got %+v", f.Terminal)
				}
			}
		}
		if !found {
			t.Fatalf("client did not receive terminal failure, out=%v err=%v", stream.out, err)
		}
		// Prompt completion: Execute must return promptly after terminal (no deadlock).
		// Presence of TerminalFailure above plus prompt return satisfies this.
		_ = sess.Close(context.Background())
		_ = conn.Close()
		grpcServer.Stop()
		_ = lis.Close()
	case <-time.After(3 * time.Second):
		buf := make([]byte, 2<<20)
		n := runtime.Stack(buf, true)
		s := string(buf[:n])
		t.Logf("goroutine dump:\n%s", s)
		grpcServer.Stop()
		_ = conn.Close()
		_ = lis.Close()
		t.Fatal("RED: Session.Execute deadlocked on negotiated upstream error - server control reader blocked waiting for host CloseSend (no terminal sent)")
	}
}

func TestRED_NegotiatedSendFailureDoesNotDeadlock(t *testing.T) {
	t.Parallel()
	// Upstream succeeds quickly but client disconnects (Send fails) - server should not deadlock waiting for control reader.
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
	sess, _, err := host.DialConfiguredSession(context.Background(), conn, "red-send-fail", "fake", nil, backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true})
	if err != nil {
		_ = conn.Close()
		t.Fatalf("DialConfiguredSession: %v", err)
	}
	// Client stream that fails Send after first event.
	stream := &failingSendRedClientStream{
		ctx: context.Background(),
		frames: []backendplugin.ClientFrame{
			{Kind: backendplugin.ClientFrameStart, InstanceID: "red-send-fail", Invocation: validInvocation()},
		},
	}
	done := make(chan error, 1)
	go func() { done <- sess.Execute(stream) }()
	select {
	case <-done:
		// Must complete even though Send failed.
		_ = sess.Close(context.Background())
		_ = conn.Close()
		grpcServer.Stop()
		_ = lis.Close()
	case <-time.After(3 * time.Second):
		buf := make([]byte, 2<<20)
		n := runtime.Stack(buf, true)
		s := string(buf[:n])
		t.Logf("goroutine dump:\n%s", s)
		grpcServer.Stop()
		_ = conn.Close()
		_ = lis.Close()
		t.Fatal("RED: Session.Execute deadlocked on send failure")
	}
}

type errorForwardingRedService struct {
	upstreamErr error
}

func (s *errorForwardingRedService) Describe(_ context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1, ProtocolMinor: backendplugin.ProtocolMinorCancellationHandshake,
		PluginID: "red-err-fake", Version: "v1",
		Features:  []backendplugin.Feature{{Name: backendplugin.FeatureCancellationHandshake}},
		Factories: []backendplugin.FactoryDescriptor{{Kind: "fake", StaticCapabilities: backendplugin.CapabilitySummary{Streaming: true}}},
	}, nil
}

func (s *errorForwardingRedService) Configure(_ context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	return &errorForwardingRedInstance{err: s.upstreamErr}, nil
}

type errorForwardingRedInstance struct {
	err error
}

func (f *errorForwardingRedInstance) Resolve(_ context.Context, _ *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{EvidenceSource: "red-err-fake", Capabilities: backendplugin.CapabilitySummary{Streaming: true}}, nil
}

func (f *errorForwardingRedInstance) ListModels(_ context.Context, _ uint32) (backendplugin.ListModelsResponse, error) {
	return backendplugin.ListModelsResponse{}, nil
}

func (f *errorForwardingRedInstance) Execute(stream backendplugin.ExecuteStream) error {
	return backendplugin.ForwardExecute(stream, func(_ context.Context, _ backendplugin.Invocation, _ aipapi.Call) (aipapi.ManagedEventStream, error) {
		return &errorRedUpstream{err: f.err}, nil
	})
}
func (f *errorForwardingRedInstance) Close(_ context.Context) error { return nil }

type errorRedUpstream struct {
	err    error
	called bool
}

func (m *errorRedUpstream) Recv(_ context.Context) (aipapi.Event, error) {
	if !m.called {
		m.called = true
		return aipapi.Event{}, m.err
	}
	return aipapi.Event{}, io.EOF
}
func (m *errorRedUpstream) Close() error { return nil }
func (m *errorRedUpstream) Cancel(_ context.Context, _ aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

type failingSendRedClientStream struct {
	ctx     context.Context
	frames  []backendplugin.ClientFrame
	pos     int
	out     []backendplugin.ServerFrame
	closeCh chan struct{}
	failed  bool
}

func (s *failingSendRedClientStream) ensureCloseCh() {
	if s.closeCh == nil {
		s.closeCh = make(chan struct{})
	}
}
func (s *failingSendRedClientStream) Context() context.Context { return s.ctx }
func (s *failingSendRedClientStream) Recv() (backendplugin.ClientFrame, error) {
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

func (s *failingSendRedClientStream) Send(frame backendplugin.ServerFrame) error {
	if !s.failed {
		s.failed = true
		return errors.New("client send failure")
	}
	s.out = append(s.out, frame)
	return nil
}

func (s *failingSendRedClientStream) Close() error {
	s.ensureCloseCh()
	select {
	case <-s.closeCh:
	default:
		close(s.closeCh)
	}
	return nil
}
