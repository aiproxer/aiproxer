package stdhttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
)

func TestNewCatalogStatusHandler_methods(t *testing.T) {
	t.Parallel()
	h := NewCatalogStatusHandler(nil, modelcatalog.CatalogStatusHandlerConfig{UsageEnabled: false})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code %d", rr.Code)
	}
}

func TestNewCatalogStatusHandler_get_usageDisabled(t *testing.T) {
	t.Parallel()
	h := NewCatalogStatusHandler(nil, modelcatalog.CatalogStatusHandlerConfig{UsageEnabled: false})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/catalog-status", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code %d body=%s", rr.Code, rr.Body.String())
	}
	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("content-type %q", ct)
	}
	var env struct {
		Status       string `json:"status"`
		UsageEnabled bool   `json:"usage_enabled"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.UsageEnabled {
		t.Fatalf("usage_enabled: %+v", env)
	}
	if env.Status != string(modelcatalog.CatalogDiagDisabled) {
		t.Fatalf("status=%q want disabled", env.Status)
	}
}
