package modeldiscover_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/modeldiscover"
)

func TestCatalogAwareOpenAICompatibleModelsProvider_mapsCatalogMatches(t *testing.T) {
	t.Parallel()

	modelsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gemma3:4b"},{"id":"gpt-oss:120b"},{"id":"unknown-local"}]}`))
	}))
	t.Cleanup(modelsSrv.Close)

	catalogSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"openai":{"id":"openai","models":[{"id":"gpt-oss:120b"}]},"google":{"id":"google","models":[{"id":"gemma3:4b"}]}}`))
	}))
	t.Cleanup(catalogSrv.Close)

	p := modeldiscover.CatalogAwareOpenAICompatibleModelsProvider{
		BaseURL:         modelsSrv.URL + "/v1",
		APIKey:          "lmstudio",
		HTTPClient:      modelsSrv.Client(),
		CanonicalPrefix: "lmstudio",
		Catalog: modeldiscover.CatalogConfig{
			URL: catalogSrv.URL,
		},
	}
	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("LoadModels() error = %v", err)
	}
	want := map[string]string{
		"gemma3:4b":     "google/gemma3:4b",
		"gpt-oss:120b":  "openai/gpt-oss:120b",
		"unknown-local": "lmstudio/unknown-local",
	}
	for _, model := range snap.Models {
		if got, ok := want[model.NativeID]; !ok || model.CanonicalID != got {
			t.Fatalf("native %q = %+v, want canonical %q", model.NativeID, model, got)
		}
		delete(want, model.NativeID)
	}
	if len(want) != 0 {
		t.Fatalf("missing models: %+v", want)
	}
}

func TestCatalogAwareOpenAICompatibleModelsProvider_ambiguousCatalogMatchFallsBack(t *testing.T) {
	t.Parallel()

	modelsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"shared-name"}]}`))
	}))
	t.Cleanup(modelsSrv.Close)

	catalogSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"openai":{"id":"openai","models":[{"id":"shared-name"}]},"google":{"id":"google","models":[{"id":"shared-name"}]}}`))
	}))
	t.Cleanup(catalogSrv.Close)

	p := modeldiscover.CatalogAwareOpenAICompatibleModelsProvider{
		BaseURL:         modelsSrv.URL + "/v1",
		APIKey:          "lmstudio",
		HTTPClient:      modelsSrv.Client(),
		CanonicalPrefix: "lmstudio",
		Catalog: modeldiscover.CatalogConfig{
			URL: catalogSrv.URL,
		},
	}
	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Models) != 1 {
		t.Fatalf("models = %+v", snap.Models)
	}
	if snap.Models[0].CanonicalID != "lmstudio/shared-name" {
		t.Fatalf("model = %+v", snap.Models[0])
	}
}

func TestCatalogAwareOpenAICompatibleModelsProvider_catalogFailureWarnsAndFallsBack(t *testing.T) {
	t.Parallel()

	modelsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"local-model"}]}`))
	}))
	t.Cleanup(modelsSrv.Close)

	catalogSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(catalogSrv.Close)

	p := modeldiscover.CatalogAwareOpenAICompatibleModelsProvider{
		BaseURL:         modelsSrv.URL + "/v1",
		APIKey:          "lmstudio",
		HTTPClient:      modelsSrv.Client(),
		CanonicalPrefix: "lmstudio",
		Catalog: modeldiscover.CatalogConfig{
			URL: catalogSrv.URL,
		},
	}
	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Models) != 1 || snap.Models[0].CanonicalID != "lmstudio/local-model" {
		t.Fatalf("models = %+v", snap.Models)
	}
	if len(snap.Warnings) != 1 || !strings.Contains(snap.Warnings[0], "models.dev catalog lookup failed") {
		t.Fatalf("warnings = %+v", snap.Warnings)
	}
}

func TestCatalogAwareOpenAICompatibleModelsProvider_cloudSuffixDoesNotMatchCatalog(t *testing.T) {
	t.Parallel()

	modelsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"gemma3:4b-cloud"}]}`))
	}))
	t.Cleanup(modelsSrv.Close)

	catalogSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"google":{"id":"google","models":[{"id":"gemma3:4b"}]}}`))
	}))
	t.Cleanup(catalogSrv.Close)

	p := modeldiscover.CatalogAwareOpenAICompatibleModelsProvider{
		BaseURL:         modelsSrv.URL + "/v1",
		APIKey:          "lmstudio",
		HTTPClient:      modelsSrv.Client(),
		CanonicalPrefix: "lmstudio",
		Catalog: modeldiscover.CatalogConfig{
			URL: catalogSrv.URL,
		},
	}
	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Models) != 1 {
		t.Fatalf("models = %+v", snap.Models)
	}
	if snap.Models[0].NativeID != "gemma3:4b-cloud" {
		t.Fatalf("native = %q", snap.Models[0].NativeID)
	}
	if snap.Models[0].CanonicalID != "lmstudio/gemma3:4b-cloud" {
		t.Fatalf("canonical = %q, want lmstudio/gemma3:4b-cloud", snap.Models[0].CanonicalID)
	}
}

func TestCatalogAwareOpenAICompatibleModelsProvider_catalogDisabledUsesPrefixFallback(t *testing.T) {
	t.Parallel()

	disabled := false
	modelsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o"}]}`))
	}))
	t.Cleanup(modelsSrv.Close)

	p := modeldiscover.CatalogAwareOpenAICompatibleModelsProvider{
		BaseURL:         modelsSrv.URL + "/v1",
		APIKey:          "lmstudio",
		HTTPClient:      modelsSrv.Client(),
		CanonicalPrefix: "lmstudio",
		Catalog: modeldiscover.CatalogConfig{
			Enabled: &disabled,
			URL:     "http://127.0.0.1:9/catalog",
		},
	}
	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Models) != 1 || snap.Models[0].CanonicalID != "lmstudio/gpt-4o" {
		t.Fatalf("models = %+v", snap.Models)
	}
}

func TestCatalogAwareOpenAICompatibleModelsProvider_preservesNativeVendorIDFallback(t *testing.T) {
	t.Parallel()

	modelsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"liquid/lfm2-350m"}]}`))
	}))
	t.Cleanup(modelsSrv.Close)

	p := modeldiscover.CatalogAwareOpenAICompatibleModelsProvider{
		BaseURL:           modelsSrv.URL + "/v1",
		APIKey:            "lmstudio",
		HTTPClient:        modelsSrv.Client(),
		CanonicalPrefix:   "lmstudio",
		PreserveVendorIDs: true,
		Catalog: modeldiscover.CatalogConfig{
			URL: "http://127.0.0.1:9/catalog",
		},
	}
	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Models) != 1 || snap.Models[0].CanonicalID != "liquid/lfm2-350m" {
		t.Fatalf("models = %+v", snap.Models)
	}
}
