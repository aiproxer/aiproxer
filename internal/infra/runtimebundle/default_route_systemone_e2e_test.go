package runtimebundle_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// TestDefaultRoute_SystemOneRequestUsesTheDerivedDefaultRoute proves the
// SystemOne frontend routes a client that named no route to the derived default
// route, and the backend receives the model id it published.
//
// The frontend resolves its route from the body model, so the route's origin
// is established where the route is chosen. The request therefore keeps the
// generation's frozen model binding, and the backend receives the native model
// id rather than the canonical one.
func TestDefaultRoute_SystemOneRequestUsesTheDerivedDefaultRoute(t *testing.T) {
	t.Parallel()

	var (
		decisionCalls atomic.Int32
		lastWireModel atomic.Pointer[string]
	)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			body, _ := io.ReadAll(r.Body)
			var payload struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal(body, &payload); err == nil {
				text := payload.Model
				lastWireModel.Store(&text)
			}
		}
		decisionCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"jev-latest","answers":{"q":{"type":"noul","noul":0.8}},"usage":{}}`)
	}))
	defer upstream.Close()

	cfg := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
routing:
  max_attempts: 1
continuity:
  in_memory: true
  store: memory
logging:
  level: error
  format: text
diagnostics:
  enabled: false
plugins:
  frontends:
    - id: systemone
      enabled: true
      config:
        max_questions: 16
  backends:
    - kind: custom-systemone-compatible
      id: decision-backend
      enabled: true
      config:
        backend_prefix: decision
        base_url: %q
        models:
          source: inline
          items:
            - canonical_id: vendor/decision-model
              native_id: jev-latest
`, upstream.URL)

	cfgPath := filepath.Join(t.TempDir(), "systemone-default-route-e2e.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	// The operator read model: the route the operator is told about.
	snap, err := runtimebundle.InspectRoutes(t.Context(), runtimebundle.InspectInput{
		ConfigPath: cfgPath,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	})
	if err != nil {
		t.Fatalf("InspectRoutes: %v", err)
	}
	const wantRoute = "decision-backend:vendor/decision-model"
	if snap.EffectiveDefaultRoute != wantRoute {
		t.Fatalf("advertised default route = %q want %q", snap.EffectiveDefaultRoute, wantRoute)
	}
	if snap.EffectiveDefaultRouteSource != "discovered_inventory" {
		t.Fatalf("advertised default route source = %q", snap.EffectiveDefaultRouteSource)
	}

	// A client model with no route prefix falls back to the default route.
	req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(
		`{"model":"jev","state":"x","questions":{"q":{"type":"noul"}}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	host.HTTPHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if decisionCalls.Load() == 0 {
		t.Fatal("the derived default route served no request")
	}
	model := lastWireModel.Load()
	if model == nil {
		t.Fatal("the backend received no request body")
	}
	if *model != "jev-latest" {
		t.Fatalf("upstream model = %q want the model id the backend published", *model)
	}
}
