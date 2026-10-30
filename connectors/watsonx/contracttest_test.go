package watsonx_test

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
	"github.com/aiproxer/aiproxer/connectors/watsonx/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/contracttest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/host"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

func newWatsonxEmulator() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/identity/token"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"access_token": "contract-bearer-tok", "token_type": "Bearer", "expires_in": 3600}`))
			return

		case strings.HasSuffix(r.URL.Path, "/ml/v1/text/chat_stream"):
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, ok := w.(http.Flusher)
			if ok {
				flusher.Flush()
			}
			chunk := `data: {"id":"chat-1","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}` + "\n\n"
			_, _ = w.Write([]byte(chunk))
			if ok {
				flusher.Flush()
			}
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			if ok {
				flusher.Flush()
			}
			return

		case strings.HasSuffix(r.URL.Path, "/ml/v1/text/chat"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"chat-1","model_id":"contract-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"}}]}`))
			return

		case strings.HasSuffix(r.URL.Path, "/ml/v1/foundation_model_specs"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"total_count":1,"resources":[{"model_id":"contract-model","label":"contract-model","functions":[{"id":"text_chat"}],"lifecycle":[{"id":"available"}]}]}`))
			return

		case strings.HasSuffix(r.URL.Path, "/ml/v4/deployments"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"total_results":0,"resources":[]}`))
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
	srv := httptest.NewServer(newWatsonxEmulator())
	t.Cleanup(srv.Close)

	cfgYAML := fmt.Sprintf("region: us-south\nproject_id: contract-proj\napi_origin: %s\niam_origin: %s\nmodel_id: contract-model\n", srv.URL, srv.URL)
	secrets := backendplugin.SecretBundle{
		Values: map[string][]byte{
			"api_key": []byte("contract-api-key"),
		},
	}

	result := contracttest.Run(t, contracttest.Config{
		PluginID:    service.PluginID,
		Version:     "test",
		Timeout:     10 * time.Second,
		FactoryKind: service.FactoryKind,
		ConfigYAML:  []byte(cfgYAML),
		Secrets:     secrets,
		Start: func(context.Context) (backendplugin.Service, func(), error) {
			return service.NewProduction(), nil, nil
		},
		StartHost: bufconnHost(service.FactoryKind, []byte(cfgYAML), secrets),
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
	srv := httptest.NewServer(newWatsonxEmulator())
	t.Cleanup(srv.Close)

	cfgYAML := fmt.Sprintf("region: us-south\nproject_id: contract-proj\napi_origin: %s\niam_origin: %s\nmodel_id: contract-model\n", srv.URL, srv.URL)
	secrets := backendplugin.SecretBundle{
		Values: map[string][]byte{
			"api_key": []byte("contract-api-key"),
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
		PluginID:    service.PluginID + "-legacy",
		Version:     "test",
		Timeout:     10 * time.Second,
		FactoryKind: service.FactoryKind,
		ConfigYAML:  []byte(cfgYAML),
		Secrets:     secrets,
		Start: func(context.Context) (backendplugin.Service, func(), error) {
			return service.NewProduction(), nil, nil
		},
		StartHost: bufconnHostWithOffer(service.FactoryKind, []byte(cfgYAML), secrets, legacyOffer),
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
