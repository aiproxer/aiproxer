package gemini_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	backend "github.com/aiproxer/aiproxer/internal/plugins/backends/gemini"
)

// TestInventory_structuredCredentialsTakePrecedence proves inventory follows
// the same credential precedence as serving (NewPoolFromCredentials): when
// structured Credentials are set, stale legacy APIKey/APIKeys material is
// never attempted, so a 401 on the legacy key cannot stop discovery while
// the serving credential is healthy.
func TestInventory_structuredCredentialsTakePrecedence(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(r.Header.Get("x-goog-api-key"))
		mu.Lock()
		seen = append(seen, key)
		mu.Unlock()
		if key == "stale-legacy" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-2.0-flash","displayName":"Gemini 2.0 Flash"}]}`))
	}))
	t.Cleanup(srv.Close)

	be := backend.New(backend.Config{
		BaseURL: srv.URL,
		APIKey:  "stale-legacy",
		APIKeys: []string{"stale-legacy"},
		Credentials: []credpool.Credential{
			{ID: "c0", Secret: "structured-ok"},
		},
		HTTPClient: srv.Client(),
	})
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("LoadModels with healthy structured credential: %v", err)
	}
	if len(snap.Models) != 1 || snap.Models[0].NativeID != "gemini-2.0-flash" {
		t.Fatalf("models = %+v", snap.Models)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != "structured-ok" {
		t.Fatalf("inventory attempted keys %q; stale legacy material must never be attempted when structured credentials are set", seen)
	}
}
