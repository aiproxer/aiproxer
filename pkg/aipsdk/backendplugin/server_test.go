package backendplugin_test

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type stubService struct {
	configureDelay time.Duration
	executeHold    time.Duration
	closeFails     int
	executeStarted chan struct{}
	releaseHold    chan struct{}
}

type closeCountingInstance struct {
	stubInstance
	remainingFails *int
}

func (s *stubService) Describe(ctx context.Context) (backendplugin.PluginDescriptor, error) {
	_ = ctx
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1, ProtocolMinor: 0, PluginID: "stub", Version: "0",
		Features: []backendplugin.Feature{{Name: "count_tokens"}},
		Factories: []backendplugin.FactoryDescriptor{{
			Kind: "stub", CredentialMode: backendplugin.CredentialModeNone,
			AccessScope: backendplugin.AccessScopeLocalOnly, ProcessSharing: backendplugin.ProcessSharingPerInstance,
			StaticCapabilities: backendplugin.CapabilitySummary{Streaming: true},
		}},
	}, nil
}

func (s *stubService) Configure(ctx context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	if s.configureDelay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(s.configureDelay):
		}
	}
	inst := &closeCountingInstance{
		stubInstance: stubInstance{
			hold: s.executeHold, id: req.InstanceID,
			executeStarted: s.executeStarted, releaseHold: s.releaseHold,
		},
		remainingFails: &s.closeFails,
	}
	return inst, nil
}

type stubInstance struct {
	hold           time.Duration
	id             string
	executeStarted chan struct{}
	releaseHold    chan struct{}
}

func (i *stubInstance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{Capabilities: backendplugin.CapabilitySummary{Streaming: true}}, nil
}

func (i *stubInstance) ListModels(context.Context, uint32) (backendplugin.ListModelsResponse, error) {
	return backendplugin.ListModelsResponse{}, nil
}

func (i *stubInstance) Execute(stream backendplugin.ExecuteStream) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	if err := stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted}); err != nil {
		return err
	}
	if i.executeStarted != nil {
		select {
		case <-i.executeStarted:
		default:
			close(i.executeStarted)
		}
	}
	if i.releaseHold != nil {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-i.releaseHold:
		}
	} else if i.hold > 0 {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-time.After(i.hold):
		}
	}
	return stream.Send(backendplugin.ServerFrame{
		Kind: backendplugin.ServerFrameTerminal, Sequence: 1,
		Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
	})
}
func (i *stubInstance) Close(context.Context) error { return nil }

func (i *closeCountingInstance) Close(ctx context.Context) error {
	if i.remainingFails != nil && *i.remainingFails > 0 {
		*i.remainingFails--
		return errors.New("close failed once")
	}
	return i.stubInstance.Close(ctx)
}

func testOffer() backendplugin.ProtocolOffer {
	return backendplugin.ProtocolOffer{
		Major: 1, Minor: 0, DisableTransportRetries: true,
		Features: []backendplugin.Feature{{Name: "count_tokens"}},
	}
}

func startBufServer(t *testing.T, svc backendplugin.Service) (backendpluginv1.BackendPluginClient, func()) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	backendpluginv1.RegisterBackendPluginServer(gs, backendplugin.NewGRPCServer(testOffer(), svc))
	go func() { _ = gs.Serve(lis) }()
	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.NewClient("passthrough:///buf", grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	return backendpluginv1.NewBackendPluginClient(conn), func() {
		_ = conn.Close()
		gs.Stop()
		_ = lis.Close()
	}
}

func negotiateOK(t *testing.T, c backendpluginv1.BackendPluginClient) string {
	t.Helper()
	resp, err := c.Negotiate(context.Background(), &backendpluginv1.NegotiateRequest{
		HostMajor: 1, HostMinor: 0, DisableTransportRetries: true,
		HostFeatures: []*backendpluginv1.Feature{{Name: "count_tokens"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GetCompatible() || resp.GetNegotiationToken() == "" {
		t.Fatalf("want compatible token, got %+v", resp)
	}
	return resp.GetNegotiationToken()
}

func TestGRPCServer_NegotiateConfigureExecuteClose(t *testing.T) {
	t.Parallel()
	client, stop := startBufServer(t, &stubService{})
	defer stop()
	token := negotiateOK(t, client)
	cfg, err := client.Configure(context.Background(), &backendpluginv1.ConfigureRequest{
		InstanceId: "i1", FactoryKind: "stub", ConfigYaml: []byte("k: v\n"),
		NegotiationToken: token,
		RuntimePolicy:    &backendpluginv1.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GetInstanceId() != "i1" {
		t.Fatalf("instance=%q", cfg.GetInstanceId())
	}
	stream, err := client.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	text := "hi"
	inv := &backendpluginv1.Invocation{
		RequestId: "r", AttemptId: "a", ALegId: "a", BLegId: "b", CanonicalModelId: "m",
		Messages: []*backendpluginv1.Message{{Role: backendpluginv1.Role_ROLE_USER, Parts: []*backendpluginv1.Part{
			{Kind: backendpluginv1.PartKind_PART_KIND_TEXT, Text: &text},
		}}},
	}
	if err := stream.Send(&backendpluginv1.ExecuteClientFrame{
		Kind: backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_START, InstanceId: "i1", Invocation: inv,
	}); err != nil {
		t.Fatal(err)
	}
	_ = stream.CloseSend()
	sawTerminal := false
	for {
		fr, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if fr.GetKind() == backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_TERMINAL {
			sawTerminal = true
		}
	}
	if !sawTerminal {
		t.Fatal("missing terminal")
	}
	if _, err := client.CloseInstance(context.Background(), &backendpluginv1.CloseInstanceRequest{InstanceId: "i1"}); err != nil {
		t.Fatal(err)
	}
}

func TestGRPCServer_ConfigureRequiresNegotiationToken(t *testing.T) {
	t.Parallel()
	client, stop := startBufServer(t, &stubService{})
	defer stop()
	_, err := client.Configure(context.Background(), &backendpluginv1.ConfigureRequest{
		InstanceId: "i1", FactoryKind: "stub",
		RuntimePolicy: &backendpluginv1.RuntimePolicy{DisableTransportRetries: true},
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code=%v err=%v", status.Code(err), err)
	}
}

func TestGRPCServer_OldMinorExactInvocationRejectedBeforeExecute(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	client, stop := startBufServer(t, &stubService{executeStarted: started})
	defer stop()
	token := negotiateOK(t, client)
	if _, err := client.Configure(context.Background(), &backendpluginv1.ConfigureRequest{
		InstanceId: "old", FactoryKind: "stub", NegotiationToken: token,
		RuntimePolicy: &backendpluginv1.RuntimePolicy{DisableTransportRetries: true},
	}); err != nil {
		t.Fatal(err)
	}
	dialect := string(aipapi.ReasoningDialectOpenAIResponsesItemV1)
	inv := &backendpluginv1.Invocation{
		RequestId: "r", AttemptId: "a", ALegId: "a", BLegId: "b", CanonicalModelId: "m",
		Messages: []*backendpluginv1.Message{{Role: backendpluginv1.Role_ROLE_ASSISTANT, Parts: []*backendpluginv1.Part{{
			Kind: backendpluginv1.PartKind_PART_KIND_REASONING, ReasoningDialect: &dialect,
			ReasoningSummary: &backendpluginv1.RawJSONValue{State: &backendpluginv1.RawJSONValue_Json{Json: []byte(`[]`)}},
		}}}},
	}
	stream, err := client.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&backendpluginv1.ExecuteClientFrame{Kind: backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_START, InstanceId: "old", Invocation: inv}); err != nil {
		t.Fatal(err)
	}
	_ = stream.CloseSend()
	_, err = stream.Recv()
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code=%v err=%v, want failed precondition", status.Code(err), err)
	}
	select {
	case <-started:
		t.Fatal("old-minor exact invocation reached plugin execution")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestGRPCServer_NegotiateIncompatibleDeterministic(t *testing.T) {
	t.Parallel()
	client, stop := startBufServer(t, &stubService{})
	defer stop()
	resp, err := client.Negotiate(context.Background(), &backendpluginv1.NegotiateRequest{
		HostMajor: 2, HostMinor: 0, DisableTransportRetries: true,
	})
	if err != nil {
		t.Fatalf("incompatible must be nil RPC error, got %v", err)
	}
	if resp.GetCompatible() || resp.GetNegotiationToken() != "" {
		t.Fatalf("want incompatible empty token, got %+v", resp)
	}
}

func TestGRPCServer_NegotiateMalformedInvalidArgument(t *testing.T) {
	t.Parallel()
	client, stop := startBufServer(t, &stubService{})
	defer stop()
	_, err := client.Negotiate(context.Background(), &backendpluginv1.NegotiateRequest{
		HostMajor: 1, HostMinor: 0, DisableTransportRetries: true,
		HostFeatures: []*backendpluginv1.Feature{{Name: ""}, {Name: "x"}, {Name: "x"}},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%v err=%v", status.Code(err), err)
	}
}

func TestGRPCServer_DuplicateConfigureAlreadyExists(t *testing.T) {
	t.Parallel()
	client, stop := startBufServer(t, &stubService{})
	defer stop()
	t1 := negotiateOK(t, client)
	t2 := negotiateOK(t, client)
	req := func(tok string) *backendpluginv1.ConfigureRequest {
		return &backendpluginv1.ConfigureRequest{
			InstanceId: "dup", FactoryKind: "stub", NegotiationToken: tok,
			RuntimePolicy: &backendpluginv1.RuntimePolicy{DisableTransportRetries: true},
		}
	}
	if _, err := client.Configure(context.Background(), req(t1)); err != nil {
		t.Fatal(err)
	}
	_, err := client.Configure(context.Background(), req(t2))
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("code=%v err=%v", status.Code(err), err)
	}
}

func TestGRPCServer_CloseWaitsForExecuteLease(t *testing.T) {
	t.Parallel()
	executeStarted := make(chan struct{})
	releaseHold := make(chan struct{})
	client, stop := startBufServer(t, &stubService{
		executeStarted: executeStarted,
		releaseHold:    releaseHold,
	})
	defer stop()
	token := negotiateOK(t, client)
	if _, err := client.Configure(context.Background(), &backendpluginv1.ConfigureRequest{
		InstanceId: "lease", FactoryKind: "stub", NegotiationToken: token,
		RuntimePolicy: &backendpluginv1.RuntimePolicy{DisableTransportRetries: true},
	}); err != nil {
		t.Fatal(err)
	}
	stream, err := client.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	text := "hi"
	inv := &backendpluginv1.Invocation{
		RequestId: "r", AttemptId: "a", ALegId: "a", BLegId: "b", CanonicalModelId: "m",
		Messages: []*backendpluginv1.Message{{Role: backendpluginv1.Role_ROLE_USER, Parts: []*backendpluginv1.Part{
			{Kind: backendpluginv1.PartKind_PART_KIND_TEXT, Text: &text},
		}}},
	}
	if err := stream.Send(&backendpluginv1.ExecuteClientFrame{
		Kind: backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_START, InstanceId: "lease", Invocation: inv,
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-executeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("execute never acquired lease/started hold")
	}

	closeDone := make(chan error, 1)
	go func() {
		_, err := client.CloseInstance(context.Background(), &backendpluginv1.CloseInstanceRequest{InstanceId: "lease"})
		closeDone <- err
	}()

	select {
	case err := <-closeDone:
		t.Fatalf("close returned before releaseHold: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseHold)
	recvDone := make(chan error, 1)
	go func() {
		for {
			_, err := stream.Recv()
			if err != nil {
				recvDone <- err
				return
			}
		}
	}()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close did not finish after releaseHold")
	}
	_ = stream.CloseSend()
	select {
	case <-recvDone:
	case <-time.After(5 * time.Second):
	}
}

func TestGRPCServer_ConcurrentConfigureSingleToken(t *testing.T) {
	t.Parallel()
	client, stop := startBufServer(t, &stubService{configureDelay: 40 * time.Millisecond})
	defer stop()
	token := negotiateOK(t, client)
	var okCount, failCount atomic.Int32
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		id := "a"
		if i == 1 {
			id = "b"
		}
		go func(instanceID string) {
			defer wg.Done()
			_, err := client.Configure(context.Background(), &backendpluginv1.ConfigureRequest{
				InstanceId: instanceID, FactoryKind: "stub", NegotiationToken: token,
				RuntimePolicy: &backendpluginv1.RuntimePolicy{DisableTransportRetries: true},
			})
			if err == nil {
				okCount.Add(1)
				return
			}
			if status.Code(err) != codes.FailedPrecondition {
				t.Errorf("code=%v err=%v", status.Code(err), err)
				return
			}
			failCount.Add(1)
		}(id)
	}
	wg.Wait()
	if okCount.Load() != 1 || failCount.Load() != 1 {
		t.Fatalf("ok=%d fail=%d want 1/1", okCount.Load(), failCount.Load())
	}
}

func TestGRPCServer_CloseRetryAfterFailure(t *testing.T) {
	t.Parallel()
	client, stop := startBufServer(t, &stubService{closeFails: 1})
	defer stop()
	token := negotiateOK(t, client)
	if _, err := client.Configure(context.Background(), &backendpluginv1.ConfigureRequest{
		InstanceId: "closeretry", FactoryKind: "stub", NegotiationToken: token,
		RuntimePolicy: &backendpluginv1.RuntimePolicy{DisableTransportRetries: true},
	}); err != nil {
		t.Fatal(err)
	}
	_, err := client.CloseInstance(context.Background(), &backendpluginv1.CloseInstanceRequest{InstanceId: "closeretry"})
	if err == nil {
		t.Fatal("expected first close to fail")
	}
	if _, err := client.CloseInstance(context.Background(), &backendpluginv1.CloseInstanceRequest{InstanceId: "closeretry"}); err != nil {
		t.Fatalf("retry close: %v", err)
	}
	_, err = client.CloseInstance(context.Background(), &backendpluginv1.CloseInstanceRequest{InstanceId: "closeretry"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("duplicate close code=%v err=%v", status.Code(err), err)
	}
}

func TestGRPCServer_HealthGuardedConcurrent(t *testing.T) {
	t.Parallel()
	srv := backendplugin.NewGRPCServer(testOffer(), &stubService{})
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = srv.Health(context.Background(), &backendpluginv1.HealthRequest{})
		}()
		go func() {
			defer wg.Done()
			_, _ = srv.GracefulShutdown(context.Background(), &backendpluginv1.GracefulShutdownRequest{})
		}()
	}
	wg.Wait()
	resp, err := srv.Health(context.Background(), &backendpluginv1.HealthRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetServing() {
		t.Fatal("expected serving=false after shutdown races")
	}
}
