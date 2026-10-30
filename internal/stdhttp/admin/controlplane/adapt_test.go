package controlplane

import (
	"net/http"
	"net/http/httptest"
	"testing"

	concurrencyapp "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/controlplane"
)

// The accounting-authority typed-nil projection moved with its adapter to
// runtimebundle (task 4.2): OSS stdhttp no longer names the concrete
// usage-authority service. These tests retain the typed-nil contracts of the
// adapters that stay in this package.

func TestAdaptConcurrencyAuthorityQueries_typedNilIsNil(t *testing.T) {
	t.Parallel()
	if AdaptConcurrencyAuthorityQueries(nil) != nil {
		t.Fatal("nil *concurrencyapp.Service must adapt to nil ConcurrencyAuthorityQueries")
	}
	var typedNil *concurrencyapp.Service
	got := AdaptConcurrencyAuthorityQueries(typedNil)
	if got != nil {
		t.Fatal("typed-nil *concurrencyapp.Service must adapt to nil ConcurrencyAuthorityQueries")
	}
	concrete := &concurrencyapp.Service{}
	if AdaptConcurrencyAuthorityQueries(concrete) == nil {
		t.Fatal("non-nil *concurrencyapp.Service must adapt to non-nil interface")
	}
}

func TestAdaptConcurrencyAuthorityQueries_typedNilLeavesCapacityDisabled(t *testing.T) {
	t.Parallel()
	var typedNil *concurrencyapp.Service
	h := NewConcurrencyAuthorityHandler(ConcurrencyOptions{
		Service: AdaptConcurrencyAuthorityQueries(typedNil),
	})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/leases/capacity", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 disabled; body=%s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/leases/status", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("leases/status without provider/svc: status=%d want 404; body=%s", rr.Code, rr.Body.String())
	}
}

func TestAdaptControlPlaneQueries_typedNilIsNil(t *testing.T) {
	t.Parallel()
	if AdaptControlPlaneQueries(nil) != nil {
		t.Fatal("nil *QueryService must adapt to nil Queries")
	}
	var typedNil *controlplane.QueryService
	if AdaptControlPlaneQueries(typedNil) != nil {
		t.Fatal("typed-nil *QueryService must adapt to nil Queries")
	}
	if AdaptControlPlaneQueries(&controlplane.QueryService{}) == nil {
		t.Fatal("non-nil *QueryService must adapt to non-nil interface")
	}
}

func TestAdaptReadinessReport_typedNilIsNil(t *testing.T) {
	t.Parallel()
	if AdaptReadinessReport(nil) != nil {
		t.Fatal("nil *ReadinessReportService must adapt to nil reader")
	}
	var typedNil *controlplane.ReadinessReportService
	if AdaptReadinessReport(typedNil) != nil {
		t.Fatal("typed-nil *ReadinessReportService must adapt to nil reader")
	}
	if AdaptReadinessReport(&controlplane.ReadinessReportService{}) == nil {
		t.Fatal("non-nil *ReadinessReportService must adapt to non-nil interface")
	}
}
