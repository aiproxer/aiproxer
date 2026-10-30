package modeldiscover_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/modeldiscover"
)

func TestCompatibleNoAuth_OpenAIInventoryOmitsAuthorization(t *testing.T) {
	t.Parallel()
	var auth, xAPIKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		xAPIKey = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"m1"}]}`)
	}))
	t.Cleanup(srv.Close)

	p := modeldiscover.OpenAICompatibleModelsProvider{
		BaseURL:            srv.URL + "/v1",
		HTTPClient:         srv.Client(),
		CanonicalPrefix:    "local",
		CompatibleModeAuth: true,
	}
	if _, err := p.LoadModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if auth != "" || xAPIKey != "" {
		t.Fatalf("headers Authorization=%q x-api-key=%q", auth, xAPIKey)
	}
}

func TestCompatibleNoAuth_AnthropicInventoryOmitsAPIKey(t *testing.T) {
	t.Parallel()
	var auth, xAPIKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		xAPIKey = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"m1","display_name":"M1"}]}`)
	}))
	t.Cleanup(srv.Close)

	p := modeldiscover.AnthropicModelsProvider{
		BaseURL:            srv.URL,
		HTTPClient:         srv.Client(),
		CanonicalPrefix:    "local",
		CompatibleModeAuth: true,
	}
	if _, err := p.LoadModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if auth != "" || xAPIKey != "" {
		t.Fatalf("headers Authorization=%q x-api-key=%q", auth, xAPIKey)
	}
}
