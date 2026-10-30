package diag_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/diag"
)

func TestWrapDiagnosticsProtect_noSecret_passesThrough(t *testing.T) {
	t.Parallel()
	called := false
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	wrapped := diag.WrapDiagnosticsProtect("", h)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !called {
		t.Fatalf("code=%d called=%v", rec.Code, called)
	}
}

func TestWrapDiagnosticsProtect_rejectsMissingHeader(t *testing.T) {
	t.Parallel()
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("next must not run") })
	wrapped := diag.WrapDiagnosticsProtect("supersecret12", h)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestWrapDiagnosticsProtect_acceptsMatchingHeader(t *testing.T) {
	t.Parallel()
	called := false
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	wrapped := diag.WrapDiagnosticsProtect("supersecret12", h)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(diag.HeaderDiagnosticsSecret, "supersecret12")
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !called {
		t.Fatalf("code=%d called=%v", rec.Code, called)
	}
}

func TestWrapDiagnosticsProtectHeaders_acceptsAlias(t *testing.T) {
	t.Parallel()
	called := false
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	wrapped := diag.WrapDiagnosticsProtectHeaders("supersecret12", []string{diag.HeaderDiagnosticsSecret, "X-Debug-Secret"}, h)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Debug-Secret", "supersecret12")
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !called {
		t.Fatalf("code=%d called=%v", rec.Code, called)
	}
}
