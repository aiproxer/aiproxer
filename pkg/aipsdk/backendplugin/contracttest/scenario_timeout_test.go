package contracttest

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/host"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

// A stuck first scenario must fail the certification fast instead of wedging
// the corpus until the go test timeout fires. The stall mirrors the CI
// incident: one scenario outlived the run budget while teardown waited
// without a bound.
func TestCertify_StuckScenarioFailsFast(t *testing.T) {
	t.Parallel()
	svc := &stallOnceService{}
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	backendpluginv1.RegisterBackendPluginServer(server, backendplugin.NewGRPCServer(backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorCancellationHandshake, DisableTransportRetries: true,
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureExactOpenResponsesFields},
			{Name: backendplugin.FeatureProxyOwnedSessionID},
			{Name: backendplugin.FeatureCancellationHandshake},
		},
	}, svc))
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(func() { server.Stop(); _ = lis.Close() })

	conn, err := lis.Dial()
	if err != nil {
		t.Fatal(err)
	}
	// The instance ID must be "contract": the scenario loop addresses every
	// Execute at that fixed instance.
	ctx := context.Background()
	sess, _, err := host.DialConfiguredSession(ctx, conn, "contract", "fake", nil, backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	start := time.Now()
	_, err = certify(t, Config{PluginID: "stall-once", Version: "test"}, ctx, sess)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("certify succeeded with a stuck scenario")
	}
	if !strings.Contains(err.Error(), "text-baseline") {
		t.Fatalf("certify error = %v, want the stuck text-baseline scenario recorded", err)
	}
	// One stalled scenario at the 5s floor plus a fast corpus and bounded
	// teardown: far below any go test timeout, far above a healthy run.
	if elapsed > 60*time.Second {
		t.Fatalf("certify took %v with a stuck scenario, want fail-fast", elapsed)
	}
	if got := svc.executions.Load(); got < 2 {
		t.Fatalf("executions = %d, want the corpus to continue past the stuck scenario", got)
	}
}

// stallOnceService stalls its first Execute until the stream context expires,
// then answers every later stream with an immediate terminal frame.
type stallOnceService struct {
	executions atomic.Int64
}

func (s *stallOnceService) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1, ProtocolMinor: backendplugin.ProtocolMinorCancellationHandshake,
		PluginID: "stall-once", Version: "test",
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureExactOpenResponsesFields},
			{Name: backendplugin.FeatureProxyOwnedSessionID},
			{Name: backendplugin.FeatureCancellationHandshake},
		},
		Factories: []backendplugin.FactoryDescriptor{{Kind: "fake"}},
	}, nil
}

func (s *stallOnceService) Configure(context.Context, backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	return (*stallOnceInstance)(s), nil
}

type stallOnceInstance stallOnceService

func (s *stallOnceInstance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{}, nil
}

func (s *stallOnceInstance) ListModels(context.Context, uint32) (backendplugin.ListModelsResponse, error) {
	return backendplugin.ListModelsResponse{}, nil
}

func (s *stallOnceInstance) Close(context.Context) error { return nil }

func (s *stallOnceInstance) Execute(stream backendplugin.ExecuteStream) error {
	if (*stallOnceService)(s).executions.Add(1) == 1 {
		<-stream.Context().Done()
		return stream.Context().Err()
	}
	for {
		frame, err := stream.Recv()
		if err != nil {
			return err
		}
		if frame.Kind == backendplugin.ClientFrameStart {
			return stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameTerminal, Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess}})
		}
	}
}
