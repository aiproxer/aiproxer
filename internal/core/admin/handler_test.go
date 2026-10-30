package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/admin"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/diag"
)

func TestDiagnosticsHandler_healthEndpoint(t *testing.T) {
	t.Parallel()

	h := admin.DiagnosticsHandler(nil)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("expected ok, got %q", body["status"])
	}
}

func TestDiagnosticsHandler_attemptsEndpoint_notRegistered(t *testing.T) {
	t.Parallel()

	h := admin.DiagnosticsHandler(nil)

	req := httptest.NewRequest(http.MethodGet, "/attempts", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestDiagnosticsHandler_attemptsEndpoint_constructionFailed(t *testing.T) {
	t.Parallel()

	var store *b2bua.MemoryStore
	var loader diag.AttemptLoader = store
	h := admin.DiagnosticsHandler(loader)

	req := httptest.NewRequest(http.MethodGet, "/attempts?a_leg_id=test", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "diag:") || strings.Contains(body, "nil store") {
		t.Fatalf("internal error leaked to wire: %q", body)
	}
	if !strings.Contains(body, "attempts endpoint unavailable") {
		t.Fatalf("unexpected body: %q", body)
	}
}
