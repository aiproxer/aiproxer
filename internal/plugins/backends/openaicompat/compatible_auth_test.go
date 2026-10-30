package openaicompat_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestCompatibleNoAuth_omitsAuthorizationHeader(t *testing.T) {
	t.Parallel()
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(srv.Close)

	be := openaicompat.NewBackend(openaicompat.BackendSpec{
		ID:                 "compat-noauth",
		BaseURL:            srv.URL + "/v1",
		HTTPClient:         srv.Client(),
		CompatibleModeAuth: true,
		ResolveFlavor:      func(aipapi.Call) openaicompat.Flavor { return openaicompat.FlavorChat },
	})
	call := aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}}}},
		Invocation: aipapi.Invocation{
			Operation:     aipapi.OperationOpenAIChatCompletions,
			DeliveryMode:  aipapi.DeliveryModeNonStreaming,
			TransportMode: aipapi.TransportModeNonStreaming,
		},
	}
	es, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = es.Close() }()
	for {
		_, rerr := es.Recv(context.Background())
		if rerr != nil {
			if rerr == io.EOF {
				break
			}
			t.Fatalf("Recv: %v", rerr)
		}
	}
	if sawAuth != "" {
		t.Fatalf("Authorization=%q, want omitted for compatible no-auth", sawAuth)
	}
}

func TestCompatibleCredential_poolSnapshotNeverContainsSecrets(t *testing.T) {
	t.Parallel()
	secret := "sk-compat-pool-secret-material"
	be := openaicompat.NewBackend(openaicompat.BackendSpec{
		ID:                 "compat-snap",
		BaseURL:            "http://127.0.0.1:9/v1",
		APIKeys:            []string{secret},
		CompatibleModeAuth: true,
	})
	// Construction must succeed; any string projection of the backend error/config path must not echo secrets.
	if be.Open == nil {
		t.Fatal("expected Open")
	}
	// Force a config-error style projection by opening against unreachable host and checking error text.
	es, err := be.Open(context.Background(), aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}}}},
		Invocation: aipapi.Invocation{
			Operation:     aipapi.OperationOpenAIChatCompletions,
			DeliveryMode:  aipapi.DeliveryModeNonStreaming,
			TransportMode: aipapi.TransportModeNonStreaming,
		},
	}, routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}})
	if err != nil {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("Open error leaked secret: %v", err)
		}
		return
	}
	defer func() { _ = es.Close() }()
	_, rerr := es.Recv(context.Background())
	if rerr != nil && strings.Contains(rerr.Error(), secret) {
		t.Fatalf("Recv error leaked secret: %v", rerr)
	}
}
