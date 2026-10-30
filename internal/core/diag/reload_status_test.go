package diag_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

func TestReloadDiagnostics_GenerationCorrelationProtected(t *testing.T) {
	t.Parallel()
	src := &staticReloadStatusSource{status: sdkreload.Status{
		ActiveGeneration:    7,
		RetainedGenerations: 2,
		LastSuccess: sdkreload.Result{
			Category:         sdkreload.ResultPublished,
			AttemptID:        3,
			ActiveGeneration: 7,
		},
		LastFailure: sdkreload.Result{
			Category:       sdkreload.ResultInvalid,
			AttemptID:      4,
			ReasonCategory: "load",
		},
		LastResult: sdkreload.Result{
			Category:       sdkreload.ResultInvalid,
			AttemptID:      4,
			ReasonCategory: "load",
		},
		SourceIntegrity: "ok",
		ModelGeneration: "model-gen-9",
		History: []sdkreload.HistoryEntry{{
			AttemptID:           4,
			Trigger:             sdkreload.TriggerAPI,
			Stage:               "load",
			Category:            sdkreload.ResultInvalid,
			ActiveGeneration:    7,
			CandidateGeneration: 0,
			RestartFieldCount:   0,
			ReasonCategory:      "load",
		}},
	}}
	h, err := diag.ReloadStatusHandler(src)
	if err != nil {
		t.Fatal(err)
	}
	protected := diag.WrapDiagnosticsProtect("diag-secret-12chars", h)

	req := httptest.NewRequest(http.MethodGet, "/diagnostics/reload", nil)
	rr := httptest.NewRecorder()
	protected.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("unprotected status=%d want 403", rr.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/diagnostics/reload", nil)
	req2.Header.Set(diag.HeaderDiagnosticsSecret, "diag-secret-12chars")
	rr2 := httptest.NewRecorder()
	protected.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr2.Code, rr2.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr2.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["active_generation"] != float64(7) {
		t.Fatalf("active_generation=%v", body["active_generation"])
	}
	if body["model_generation"] != "model-gen-9" {
		t.Fatalf("model_generation=%v", body["model_generation"])
	}
	raw := rr2.Body.String()
	for _, bad := range []string{"sk-", "password=", "postgres://", "api_key="} {
		if strings.Contains(strings.ToLower(raw), bad) {
			t.Fatalf("diagnostics leaked %q: %s", bad, raw)
		}
	}
}

type staticReloadStatusSource struct {
	status sdkreload.Status
}

func (s *staticReloadStatusSource) ReloadStatus() sdkreload.Status {
	return s.status
}

func TestReloadDiagnostics_CanonicalStatusDefensiveAndPathFree(t *testing.T) {
	t.Parallel()
	fields := []string{"sk-live-secret", "password=hunter2", "/etc/aip/secret.yaml"}
	hist := []sdkreload.HistoryEntry{{
		AttemptID: 1,
		Trigger:   sdkreload.TriggerAPI,
		Stage:     "publish",
		Category:  sdkreload.ResultPublished,
		SafeActor: "actor",
	}}
	cur := sdkreload.Result{
		Category:      sdkreload.ResultBusy,
		RestartFields: []string{"server.address"},
	}
	st := sdkreload.Status{
		ActiveGeneration: 3,
		CurrentAttempt:   &cur,
		LastResult:       cur,
		LastSuccess: sdkreload.Result{
			Category:         sdkreload.ResultPublished,
			ActiveGeneration: 3,
		},
		LastFailure: sdkreload.Result{
			Category:       sdkreload.ResultInvalid,
			ReasonCategory: "load",
		},
		SourceIntegrity: "ok",
		History:         hist,
		Busy:            true,
	}
	src := &staticReloadStatusSource{status: st.Clone()}
	h, err := diag.ReloadStatusHandler(src)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/diagnostics/reload", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	raw := rr.Body.String()
	for _, bad := range fields {
		if strings.Contains(raw, bad) {
			t.Fatalf("diagnostics leaked %q: %s", bad, raw)
		}
	}
	if strings.Contains(raw, "FixedSourcePath") || strings.Contains(raw, "/etc/") {
		t.Fatalf("diagnostics must stay path-free: %s", raw)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"last_success", "last_failure", "last_result"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("missing bounded field %q", key)
		}
	}
	// Source seam must accept canonical Status directly (compile/type identity).
	var _ diag.ReloadStatusSource = src
	if reflect.TypeOf(src.ReloadStatus()).PkgPath() != "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload" {
		t.Fatalf("ReloadStatus must surface canonical pkg path, got %s", reflect.TypeOf(src.ReloadStatus()).PkgPath())
	}
}
