package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

func TestInventoryProvider_remoteSuccess(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.4","name":"GPT 5.4"},{"id":"glm-5.2"}]}`))
	}))
	t.Cleanup(srv.Close)

	p := NewInventoryProvider(InventoryProviderConfig{
		BaseURL:    srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
		Kind:       BackendZen,
	})

	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Source != modelinventory.SourceRemote {
		t.Fatalf("source = %q", snap.Source)
	}
	if len(snap.Models) != 2 {
		t.Fatalf("models = %+v", snap.Models)
	}
	if len(snap.Warnings) != 0 {
		t.Fatalf("warnings = %+v", snap.Warnings)
	}
}

func TestInventoryProvider_remoteFailureUsesFallback(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fail", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	p := NewInventoryProvider(InventoryProviderConfig{
		BaseURL:    srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
		Kind:       BackendGo,
		Fallback: []ModelEntry{
			{RawID: "kimi-k2.7-code", DisplayName: "Kimi K2.7 Code"},
		},
	})

	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Source != modelinventory.SourceStaticInline {
		t.Fatalf("source = %q", snap.Source)
	}
	if len(snap.Models) != 1 {
		t.Fatalf("models = %+v", snap.Models)
	}
	if snap.Models[0].NativeID != "kimi-k2.7-code" {
		t.Fatalf("model = %+v", snap.Models[0])
	}
	if len(snap.Warnings) == 0 || !strings.Contains(snap.Warnings[0], "remote model discovery failed") {
		t.Fatalf("warnings = %+v", snap.Warnings)
	}
}

func TestInventoryProvider_nilContext(t *testing.T) {
	t.Parallel()

	p := NewInventoryProvider(InventoryProviderConfig{Kind: BackendZen})
	_, err := p.LoadModels(nil) //nolint:staticcheck // verifies nil context guard.
	if err != modelinventory.ErrNilContext {
		t.Fatalf("err = %v", err)
	}
}

func TestInventoryProvider_noFallbackOnRemoteFailure(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fail", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	_, _, _, err := LoadModelEntries(context.Background(), ModelLoaderConfig{
		BaseURL:    srv.URL,
		APIKey:     "test-key",
		HTTPClient: srv.Client(),
	}, nil)
	if err == nil {
		t.Fatal("expected error without fallback")
	}
}
