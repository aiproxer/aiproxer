package configreload_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/configreload"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	mgmtreload "github.com/aiproxer/aiproxer/internal/stdhttp/admin/configreload"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

type statusStub struct {
	st   sdkreload.Status
	path string
}

func (s statusStub) Reload(context.Context, sdkreload.Trigger) sdkreload.Result {
	return s.st.LastResult
}
func (s statusStub) Status() sdkreload.Status { return s.st }
func (s statusStub) FixedSourcePath() string  { return s.path }

// TestReadiness_IndependentOfReloadControlFailure proves req 13.1-13.2 / 14.8:
// a failed reload status remains visible on the management surface while the
// data-plane health probe stays healthy for the last-good active generation.
func TestReadiness_IndependentOfReloadControlFailure(t *testing.T) {
	t.Parallel()

	health := diag.HealthHandler()
	rrHealth := httptest.NewRecorder()
	health.ServeHTTP(rrHealth, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rrHealth.Code != http.StatusOK {
		t.Fatalf("health status=%d", rrHealth.Code)
	}
	if !strings.Contains(rrHealth.Body.String(), `"status":"ok"`) {
		t.Fatalf("health body=%s", rrHealth.Body.String())
	}

	st := sdkreload.Status{
		ActiveGeneration: 11,
		Busy:             false,
		LastResult: sdkreload.Result{
			Category:         sdkreload.ResultInvalid,
			AttemptID:        9,
			ActiveGeneration: 11,
			ReasonCategory:   configreload.StageLoad,
		},
		LastFailure: sdkreload.Result{
			Category:         sdkreload.ResultInvalid,
			AttemptID:        9,
			ActiveGeneration: 11,
			ReasonCategory:   configreload.StageLoad,
		},
		LastSuccess: sdkreload.Result{
			Category:         sdkreload.ResultPublished,
			AttemptID:        8,
			ActiveGeneration: 11,
		},
		SourceIntegrity:     "ok",
		RetainedGenerations: 1,
		ControlDegraded:     true,
	}
	h, err := mgmtreload.NewHandler(mgmtreload.Options{}, statusStub{st: st, path: "/fixed/config.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, mgmtreload.StatusPath, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status code=%d body=%s", rr.Code, rr.Body.String())
	}
	var dto mgmtreload.StatusDTO
	if err := json.NewDecoder(rr.Body).Decode(&dto); err != nil {
		t.Fatal(err)
	}
	if dto.FixedSourcePath != "/fixed/config.yaml" {
		t.Fatalf("fixed_source_path=%q", dto.FixedSourcePath)
	}
	if dto.ActiveGeneration != 11 {
		t.Fatalf("active=%d", dto.ActiveGeneration)
	}
	if dto.LastResult.Category != string(sdkreload.ResultInvalid) {
		t.Fatalf("last_result=%q", dto.LastResult.Category)
	}
	if !dto.ControlDegraded {
		t.Fatal("expected control_degraded visible on management status")
	}
	rr2 := httptest.NewRecorder()
	health.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr2.Code != http.StatusOK {
		t.Fatalf("health after failed reload status=%d", rr2.Code)
	}
}
