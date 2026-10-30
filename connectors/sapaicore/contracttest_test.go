package sapaicore_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/connectors/sapaicore/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/contracttest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/host"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

func newSAPAICoreTCKEmulator() http.Handler {
	compatEmu := openaicompat.NewEmulator(openaicompat.EmulatorConfig{RequireBearer: true})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth/token"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"access_token": "contract-bearer-tok", "token_type": "bearer", "expires_in": 3600}`))
			return

		case strings.HasSuffix(r.URL.Path, "/v2/lm/deployments"):
			if r.Header.Get("AI-Resource-Group") == "" {
				http.Error(w, "missing AI-Resource-Group", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"count": 1, "resources": [{"id": "contract-model", "status": "RUNNING"}]}`))
			return

		case strings.Contains(r.URL.Path, "/v2/inference/deployments/") && strings.HasSuffix(r.URL.Path, "/chat/completions"):
			if r.Header.Get("AI-Resource-Group") == "" {
				http.Error(w, "missing AI-Resource-Group", http.StatusBadRequest)
				return
			}
			compatEmu.ServeHTTP(w, r)
			return

		default:
			http.NotFound(w, r)
		}
	})
}

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
	srv := httptest.NewServer(newSAPAICoreTCKEmulator())
	t.Cleanup(srv.Close)

	cfgYAML := fmt.Appendf(nil, `resource_group: default
inference_contract: openai-chat
deployment_id: contract-model
api_origin: %s
oauth_origin: %s
`, srv.URL, srv.URL)
	secrets := backendplugin.SecretBundle{
		Values: map[string][]byte{
			"service_key": validServiceKeyJSON(srv.URL, srv.URL),
		},
	}

	result := contracttest.Run(t, contracttest.Config{
		PluginID:    service.PluginID,
		Version:     "test",
		Timeout:     10 * time.Second,
		FactoryKind: service.FactoryKind,
		ConfigYAML:  cfgYAML,
		Secrets:     secrets,
		Start: func(context.Context) (backendplugin.Service, func(), error) {
			return service.NewProduction(), nil, nil
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
	srv := httptest.NewServer(newSAPAICoreTCKEmulator())
	t.Cleanup(srv.Close)

	cfgYAML := fmt.Appendf(nil, `resource_group: default
inference_contract: openai-chat
deployment_id: contract-model
api_origin: %s
oauth_origin: %s
`, srv.URL, srv.URL)
	secrets := backendplugin.SecretBundle{
		Values: map[string][]byte{
			"service_key": validServiceKeyJSON(srv.URL, srv.URL),
		},
	}

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
		PluginID:    service.PluginID,
		Version:     "test",
		Timeout:     10 * time.Second,
		FactoryKind: service.FactoryKind,
		ConfigYAML:  cfgYAML,
		Secrets:     secrets,
		Start: func(context.Context) (backendplugin.Service, func(), error) {
			return service.NewProduction(), nil, nil
		},
		StartHost: bufconnHostWithOffer(service.FactoryKind, cfgYAML, secrets, legacyOffer),
	})
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	if containsFeature(result.Negotiated.EnabledFeatures, backendplugin.FeatureCancellationHandshake) {
		t.Fatalf("handshake must not be enabled when host omitted it: %+v", result.Negotiated)
	}
	if result.Negotiated.NegotiatedMinor != backendplugin.ProtocolMinorProxyOwnedSessionID {
		t.Fatalf("negotiated minor=%d want %d", result.Negotiated.NegotiatedMinor, backendplugin.ProtocolMinorProxyOwnedSessionID)
	}
}

func containsFeature(features []string, name string) bool {
	return slices.Contains(features, name)
}
