package runtimehost_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
)

func TestGenerationDispatcher_AdminPathUsesLeasedGenerationHandler(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	d := runtimehost.NewGenerationDispatcher(m)

	old := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "old-admin")
	})
	publishPlane(t, m, "old", old)

	req := httptest.NewRequest(http.MethodPut, "/admin/routing-overrides/a_1", nil)
	rr1 := httptest.NewRecorder()
	d.ServeHTTP(rr1, req)
	if rr1.Body.String() != "old-admin" {
		t.Fatalf("before publish body=%q", rr1.Body.String())
	}

	newH := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "new-admin")
	})
	publishPlane(t, m, "new", newH)

	rr2 := httptest.NewRecorder()
	d.ServeHTTP(rr2, httptest.NewRequest(http.MethodPut, "/admin/routing-overrides/a_1", nil))
	if rr2.Body.String() != "new-admin" {
		t.Fatalf("after publish body=%q want new-admin (dispatcher must rebind generation-specific admin handler)", rr2.Body.String())
	}
}
