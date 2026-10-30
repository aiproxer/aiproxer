package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestHTTPMetricsMiddleware_overrideAdminPathOmitsSelectorAndALegLabels(t *testing.T) {
	t.Parallel()
	const (
		aLeg = "SECRET_ALEG"
		sel  = "SECRETSEL"
	)
	reg := prometheus.NewRegistry()
	m := RegisterHTTPMetrics(reg, false)
	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPut, "/admin/routing-overrides/"+aLeg, strings.NewReader(`{"selector":"`+sel+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var dump strings.Builder
	sawRouteGroup := false
	for _, f := range families {
		dump.WriteString(f.String())
		for _, metric := range f.GetMetric() {
			for _, lp := range metric.GetLabel() {
				v := lp.GetValue()
				if strings.Contains(v, aLeg) || strings.Contains(v, sel) {
					t.Fatalf("metrics label %s=%q leaked A-leg or selector", lp.GetName(), v)
				}
				if lp.GetName() == "route_group" {
					sawRouteGroup = true
					if v != "/admin" {
						t.Fatalf("route_group=%q want /admin (coarse path, not A-leg)", v)
					}
				}
			}
		}
	}
	s := dump.String()
	if strings.Contains(s, aLeg) || strings.Contains(s, sel) {
		t.Fatalf("metrics exposition leaked A-leg or selector:\n%s", s)
	}
	if !sawRouteGroup {
		t.Fatal("expected route_group label")
	}
}
