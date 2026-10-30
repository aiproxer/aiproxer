package snowflake_test

import (
	"context"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/connectors/snowflake/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/contracttest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/host"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

func bufconnHost(factory string, config []byte, secrets backendplugin.SecretBundle) func(context.Context, backendplugin.Service) (contracttest.HostSession, func(), error) {
	return bufconnHostWithOffer(factory, config, secrets, backendplugin.ProtocolOffer{
		Major:                   1,
		Minor:                   backendplugin.ProtocolMinorCancellationHandshake,
		DisableTransportRetries: true,
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureExactOpenResponsesFields},
			{Name: backendplugin.FeatureProxyOwnedSessionID},
			{Name: backendplugin.FeatureCancellationHandshake},
		},
	})
}

func bufconnHostWithOffer(factory string, config []byte, secrets backendplugin.SecretBundle, offer backendplugin.ProtocolOffer) func(context.Context, backendplugin.Service) (contracttest.HostSession, func(), error) {
	return func(ctx context.Context, svc backendplugin.Service) (contracttest.HostSession, func(), error) {
		lis := bufconn.Listen(1 << 20)
		server := grpc.NewServer()
		backendpluginv1.RegisterBackendPluginServer(server, backendplugin.NewGRPCServer(offer, svc))
		go func() { _ = server.Serve(lis) }()
		conn, err := lis.Dial()
		if err != nil {
			server.Stop()
			_ = lis.Close()
			return nil, nil, err
		}
		sess, _, err := host.DialConfiguredSession(ctx, conn, "contract", factory, config, secrets, backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8})
		if err != nil {
			server.Stop()
			_ = lis.Close()
			return nil, nil, err
		}
		return sess, func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = sess.Close(closeCtx)
			server.Stop()
			_ = lis.Close()
		}, nil
	}
}

func TestSupportedContractTCK(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{RequireBearer: false}))
	t.Cleanup(srv.Close)

	cfgYAML := []byte("account: testacc\napi_origin: " + srv.URL + "\n")
	secrets := backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte("test-pat")}}

	result := contracttest.Run(t, contracttest.Config{
		PluginID:    service.PluginID,
		Version:     "test",
		Timeout:     10 * time.Second,
		FactoryKind: service.FactoryKind,
		ConfigYAML:  cfgYAML,
		Secrets:     secrets,
		Start: func(context.Context) (backendplugin.Service, func(), error) {
			return service.New(), nil, nil
		},
		StartHost: bufconnHost(service.FactoryKind, cfgYAML, secrets),
	})
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	if !containsFeature(result.Negotiated.EnabledFeatures, backendplugin.FeatureCancellationHandshake) {
		t.Fatalf("negotiated handshake must be enabled for minor 8, got negotiated=%+v", result.Negotiated)
	}
	if result.Negotiated.NegotiatedMinor != backendplugin.ProtocolMinorCancellationHandshake {
		t.Fatalf("negotiated minor=%d want %d", result.Negotiated.NegotiatedMinor, backendplugin.ProtocolMinorCancellationHandshake)
	}
}

func TestSupportedContractTCK_LegacyFallback(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{RequireBearer: false}))
	t.Cleanup(srv.Close)

	cfgYAML := []byte("account: testacc\napi_origin: " + srv.URL + "\n")
	secrets := backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte("test-pat")}}

	legacyOffer := backendplugin.ProtocolOffer{
		Major:                   1,
		Minor:                   backendplugin.ProtocolMinorProxyOwnedSessionID,
		DisableTransportRetries: true,
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureExactOpenResponsesFields},
			{Name: backendplugin.FeatureProxyOwnedSessionID},
		},
	}
	result := contracttest.Run(t, contracttest.Config{
		PluginID:    service.PluginID + "-legacy",
		Version:     "test",
		Timeout:     10 * time.Second,
		FactoryKind: service.FactoryKind,
		ConfigYAML:  cfgYAML,
		Secrets:     secrets,
		Start: func(context.Context) (backendplugin.Service, func(), error) {
			return service.New(), nil, nil
		},
		StartHost: bufconnHostWithOffer(service.FactoryKind, cfgYAML, secrets, legacyOffer),
	})
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	if containsFeature(result.Negotiated.EnabledFeatures, backendplugin.FeatureCancellationHandshake) {
		t.Fatalf("legacy negotiation must NOT enable cancellation handshake, got %+v", result.Negotiated)
	}
	if result.Negotiated.Compatible && result.Negotiated.NegotiatedMinor >= backendplugin.ProtocolMinorCancellationHandshake {
		t.Fatalf("legacy negotiated minor=%d must be < %d", result.Negotiated.NegotiatedMinor, backendplugin.ProtocolMinorCancellationHandshake)
	}
}

func containsFeature(features []string, want string) bool {
	return slices.Contains(features, want)
}
