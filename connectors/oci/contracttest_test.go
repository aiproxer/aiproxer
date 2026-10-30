package oci_test

import (
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
	"github.com/aiproxer/aiproxer/connectors/oci/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/contracttest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/host"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

func newOCIEmulator() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/20231130/actions/chat") {
			body, _ := io.ReadAll(r.Body)
			isStream := strings.Contains(string(body), `"isStream":true`) || strings.Contains(string(body), `"isStream": true`)

			if isStream {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				flusher, ok := w.(http.Flusher)
				if ok {
					flusher.Flush()
				}
				chunk := `data: {"chatResponse": {"apiFormat": "GENERIC", "choices": [{"index": 0, "message": {"role": "ASSISTANT", "content": [{"type": "TEXT", "text": "hello"}]}}]}}` + "\n\n"
				_, _ = w.Write([]byte(chunk))
				if ok {
					flusher.Flush()
				}
				_, _ = w.Write([]byte("data: [DONE]\n\n"))
				if ok {
					flusher.Flush()
				}
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"chatResponse": {"apiFormat": "GENERIC", "choices": [{"index": 0, "message": {"role": "ASSISTANT", "content": [{"type": "TEXT", "text": "hello"}]}}]}}`))
			return
		}

		if strings.HasSuffix(r.URL.Path, "/20231130/models") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"modelCollection": {"items": [{"id": "contract-model", "displayName": "contract-model"}]}}`))
			return
		}

		http.NotFound(w, r)
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
	srv := httptest.NewServer(newOCIEmulator())
	t.Cleanup(srv.Close)

	secrets, tenancy, user, fp := testKeyAndSecrets()
	cfgYAML := fmt.Appendf(nil, "region: us-chicago-1\ncompartment_id: ocid1.compartment.oc1..test\ntenancy_ocid: %s\nuser_ocid: %s\nfingerprint: %s\napi_origin: %s\nmodel_id: contract-model\n", tenancy, user, fp, srv.URL)

	result := contracttest.Run(t, contracttest.Config{
		PluginID:    service.PluginID,
		Version:     "test",
		Timeout:     10 * time.Second,
		FactoryKind: service.FactoryKind,
		ConfigYAML:  cfgYAML,
		Secrets:     secrets,
		Start: func(context.Context) (backendplugin.Service, func(), error) {
			return service.New(service.WithRequestSignerFactory(service.DefaultRequestSignerFactory)), nil, nil
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
	srv := httptest.NewServer(newOCIEmulator())
	t.Cleanup(srv.Close)

	secrets, tenancy, user, fp := testKeyAndSecrets()
	cfgYAML := fmt.Appendf(nil, "region: us-chicago-1\ncompartment_id: ocid1.compartment.oc1..test\ntenancy_ocid: %s\nuser_ocid: %s\nfingerprint: %s\napi_origin: %s\nmodel_id: contract-model\n", tenancy, user, fp, srv.URL)

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
			return service.New(service.WithRequestSignerFactory(service.DefaultRequestSignerFactory)), nil, nil
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
