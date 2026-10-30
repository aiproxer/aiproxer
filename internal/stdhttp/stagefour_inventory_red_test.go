package stdhttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
)

func TestMountedInventoryJSON_includesExtensionTruthBlock_RED(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{{ID: "submit-noop", Enabled: true}},
		},
	}
	ih, err := diag.InventoryHandler(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	const path = "/debug/inventory"
	mux.Handle(path, diag.WrapDiagnosticsProtect("", ih))

	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rr.Code, rr.Body.String())
	}
	var envelope struct {
		Extensions json.RawMessage `json:"extensions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Extensions) == 0 || string(envelope.Extensions) == "null" {
		t.Fatal("RED stage four: mounted inventory must include non-null extensions block (R14 / design section 14)")
	}
	var ext diag.InventoryExtensions
	if err := json.Unmarshal(envelope.Extensions, &ext); err != nil {
		t.Fatal(err)
	}
	if len(ext.LegalPipeline) != 17 || len(ext.Stages) != 17 {
		t.Fatalf("extensions contract: want 17 pipeline stages each, got pipeline=%d stages=%d", len(ext.LegalPipeline), len(ext.Stages))
	}
	for _, st := range ext.Stages {
		if strings.TrimSpace(st.ID) == "" || strings.TrimSpace(st.DefaultFailure) == "" {
			t.Fatalf("stage missing id or default_failure: %+v", st)
		}
	}
	secretGuard, sessionClassification, submitRequest := -1, -1, -1
	for i, id := range ext.LegalPipeline {
		switch id {
		case "secret_guard":
			secretGuard = i
		case "session_classification":
			sessionClassification = i
		case "submit_request":
			submitRequest = i
		}
	}
	if secretGuard < 0 || sessionClassification <= secretGuard || submitRequest <= sessionClassification {
		t.Fatalf("classification stage must follow secret_guard and precede submit_request, got %v", ext.LegalPipeline)
	}
	classificationStageCount := 0
	for _, st := range ext.Stages {
		if st.ID != "session_classification" {
			continue
		}
		classificationStageCount++
		if st.DefaultFailure != "fail_open" {
			t.Fatalf("session_classification default_failure: want fail_open, got %q", st.DefaultFailure)
		}
	}
	if classificationStageCount != 1 {
		t.Fatalf("session_classification stage rows: want 1, got %d", classificationStageCount)
	}
}
