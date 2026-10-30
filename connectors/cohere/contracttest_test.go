package cohere_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/connectors/cohere/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/contracttest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/host"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

func newCohereTCKEmulator() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}

		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"models":[{"name":"contract-model","endpoints":["chat"]}]}`))
			return

		case "/v2/chat":
			body, _ := io.ReadAll(r.Body)
			isStream := bytes.Contains(body, []byte(`"stream":true`))

			if isStream {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				flusher, ok := w.(http.Flusher)
				if ok {
					flusher.Flush()
				}
				chunks := []string{
					`data: {"type":"message-start","id":"msg-contract","delta":{"message":{"role":"assistant"}}}` + "\n\n",
					`data: {"type":"content-start","index":0,"delta":{"message":{"content":{"type":"text","text":""}}}}` + "\n\n",
					`data: {"type":"content-delta","index":0,"delta":{"message":{"content":{"text":"hello"}}}}` + "\n\n",
					`data: {"type":"content-end","index":0}` + "\n\n",
					`data: {"type":"message-end","id":"msg-contract","delta":{"finish_reason":"COMPLETE"}}` + "\n\n",
				}
				for _, c := range chunks {
					_, _ = w.Write([]byte(c))
					if ok {
						flusher.Flush()
					}
				}
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"id": "msg-contract",
				"finish_reason": "COMPLETE",
				"message": {
					"role": "assistant",
					"content": [{"type":"text","text":"hello"}]
				}
			}`))
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
	srv := httptest.NewServer(newCohereTCKEmulator())
	t.Cleanup(srv.Close)

	cfgYAML := fmt.Appendf(nil, "api_origin: %s\nmodel: contract-model\n", srv.URL)
	secrets := backendplugin.SecretBundle{
		Values: map[string][]byte{"api_key": []byte("contract-bearer-tok")},
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
	srv := httptest.NewServer(newCohereTCKEmulator())
	t.Cleanup(srv.Close)

	cfgYAML := fmt.Appendf(nil, "api_origin: %s\nmodel: contract-model\n", srv.URL)
	secrets := backendplugin.SecretBundle{
		Values: map[string][]byte{"api_key": []byte("contract-bearer-tok")},
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
