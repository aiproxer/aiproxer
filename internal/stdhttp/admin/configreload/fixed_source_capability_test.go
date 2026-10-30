package configreload_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	mgmtreload "github.com/aiproxer/aiproxer/internal/stdhttp/admin/configreload"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

func TestStatusDTO_FixedSourcePathFromCoordinatorCapability(t *testing.T) {
	t.Parallel()
	coord := newFakeCoordinator("/fixed/startup/config.yaml", nil)
	h, err := mgmtreload.NewHandler(mgmtreload.Options{
		AuthMode:    mgmtreload.AuthModeBearer,
		BearerToken: "test-management-secret",
	}, coord)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, mgmtreload.StatusPath, nil)
	req.Header.Set("Authorization", "Bearer test-management-secret")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var dto mgmtreload.StatusDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.FixedSourcePath != "/fixed/startup/config.yaml" {
		t.Fatalf("fixed_source_path=%q", dto.FixedSourcePath)
	}
	st := coord.Status()
	raw, _ := json.Marshal(st)
	if jsonHasKey(raw, "FixedSourcePath") || jsonHasKey(raw, "fixed_source_path") {
		t.Fatalf("canonical status JSON must not carry path fields: %s", raw)
	}
	_ = sdkreload.Status{}
}

func jsonHasKey(raw []byte, key string) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}
