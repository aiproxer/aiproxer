package diag

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPprofHandler_nilWhenEmpty(t *testing.T) {
	t.Parallel()
	if PprofHandler("") != nil || PprofHandler("   ") != nil {
		t.Fatal("expected nil handler for empty prefix")
	}
}

func TestPprofHandler_index(t *testing.T) {
	t.Parallel()
	h := PprofHandler("/debug/pprof")
	if h == nil {
		t.Fatal("nil handler")
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "goroutine") && !strings.Contains(body, "heap") {
		t.Fatalf("expected pprof index markers in body (len=%d)", len(body))
	}
}
