package diag_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
)

func TestInventoryJSON_includesExtensionTruthBlock_RED(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{{ID: "submit-noop", Enabled: true}},
		},
	}
	h, err := diag.InventoryHandler(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	var envelope struct {
		Extensions json.RawMessage `json:"extensions"`
	}
	if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Extensions) == 0 || string(envelope.Extensions) == "null" {
		t.Fatal("RED stage four: inventory JSON must include non-null extensions block (R14 / design section 14)")
	}
}
