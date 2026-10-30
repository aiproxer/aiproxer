package host_test

import (
	"context"
	"testing"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	fakebp "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/host"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

func TestSession_PromptCacheControllerUsesNegotiatedInstancePlane(t *testing.T) {
	t.Parallel()
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	service := &fakebp.FakeService{Mode: fakebp.ModeValid, PromptCache: true}
	backendpluginv1.RegisterBackendPluginServer(server, backendplugin.NewGRPCServer(backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorPromptCacheResidency, DisableTransportRetries: true,
		Features: []backendplugin.Feature{{Name: backendplugin.FeaturePromptCacheResidency}},
	}, service))
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(func() { server.Stop(); _ = lis.Close() })
	conn, err := lis.Dial()
	if err != nil {
		t.Fatal(err)
	}
	session, profile, err := host.DialConfiguredSession(context.Background(), conn, "prompt-cache", "fake", nil, backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	if !profile.PromptCacheProfile.RenewalSupported {
		t.Fatalf("profile=%+v", profile.PromptCacheProfile)
	}
	result, err := session.RenewPromptCache(context.Background(), promptcache.RenewRequest{Handle: promptcache.Handle("target"), OperationID: "op-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Result.Status != promptcache.Renewed || result.Result.Observation == nil || result.Result.Observation.BackendInstanceID != "prompt-cache" {
		t.Fatalf("result=%+v", result)
	}
	if result.Accounting == nil || result.Accounting.DedupeKey != "prompt-cache:op-1" {
		t.Fatalf("accounting=%+v", result.Accounting)
	}
	if err := session.ReleasePromptCache(context.Background(), promptcache.ReleaseRequest{Handle: promptcache.Handle("target")}); err != nil {
		t.Fatal(err)
	}
}
