package modeldiscover_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/modeldiscover"
)

func TestOpenAICompatibleModelsProvider_LoadModels(t *testing.T) {
	t.Parallel()

	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-4o"},{"id":"openai/gpt-4o-mini"}]}`))
	}))
	defer srv.Close()

	p := modeldiscover.OpenAICompatibleModelsProvider{
		BaseURL:           srv.URL + "/v1",
		APIKeys:           []string{"secret"},
		CanonicalPrefix:   "openai",
		PreserveVendorIDs: true,
		HTTPClient:        srv.Client(),
	}
	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("LoadModels() error = %v", err)
	}
	if auth != "Bearer secret" {
		t.Fatalf("Authorization = %q", auth)
	}
	if len(snap.Models) != 2 {
		t.Fatalf("models len = %d", len(snap.Models))
	}
	if snap.Models[0].CanonicalID != "openai/gpt-4o" || snap.Models[0].NativeID != "gpt-4o" {
		t.Fatalf("model[0] = %+v", snap.Models[0])
	}
	if snap.Models[1].CanonicalID != "openai/gpt-4o-mini" || snap.Models[1].NativeID != "openai/gpt-4o-mini" {
		t.Fatalf("model[1] = %+v", snap.Models[1])
	}
}

func TestAnthropicModelsProvider_LoadModels(t *testing.T) {
	t.Parallel()

	var apiKey, version string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey = r.Header.Get("x-api-key")
		version = r.Header.Get("anthropic-version")
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet-4-5","display_name":"Claude Sonnet 4.5"}]}`))
	}))
	defer srv.Close()

	p := modeldiscover.AnthropicModelsProvider{
		BaseURL:    srv.URL,
		APIKeys:    []string{"anthropic-secret"},
		HTTPClient: srv.Client(),
	}
	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("LoadModels() error = %v", err)
	}
	if apiKey != "anthropic-secret" {
		t.Fatalf("x-api-key = %q", apiKey)
	}
	if version == "" {
		t.Fatal("anthropic-version header is empty")
	}
	if len(snap.Models) != 1 {
		t.Fatalf("models len = %d", len(snap.Models))
	}
	if got := snap.Models[0]; got.CanonicalID != "anthropic/claude-sonnet-4-5" || got.NativeID != "claude-sonnet-4-5" || got.DisplayName != "Claude Sonnet 4.5" {
		t.Fatalf("model = %+v", got)
	}
}

func TestGeminiModelsProvider_LoadModelsUsesAPIKeyHeader(t *testing.T) {
	t.Parallel()

	var key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("x-goog-api-key")
		if r.URL.Path != "/v1beta/models" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-2.5-pro","displayName":"Gemini 2.5 Pro"}]}`))
	}))
	defer srv.Close()

	p := modeldiscover.GeminiModelsProvider{
		BaseURL:    srv.URL,
		APIKeys:    []string{"gemini secret/with spaces"},
		HTTPClient: srv.Client(),
	}
	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("LoadModels() error = %v", err)
	}
	if key != "gemini secret/with spaces" {
		t.Fatalf("x-goog-api-key header = %q", key)
	}
	if len(snap.Models) != 1 {
		t.Fatalf("models len = %d", len(snap.Models))
	}
	if got := snap.Models[0]; got.CanonicalID != "google/gemini-2.5-pro" || got.NativeID != "gemini-2.5-pro" || got.DisplayName != "Gemini 2.5 Pro" {
		t.Fatalf("model = %+v", got)
	}
}
