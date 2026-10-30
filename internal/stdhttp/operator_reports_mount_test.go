package stdhttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	httpauth "github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

// Task 4.2 generic operator mounting. Operator routes live under registered
// [AdminMount] paths with the diagnostics secret enforced before any handler
// runs; a composition that registers no reader/mount serves no route. Frontend
// inference responses carry no operator economics fields.
//
// The real billing operator readers, their reader-absence behavior and their
// route shapes stay covered by internal/stdhttp/admin/billing (which keeps its
// tests until the package moves in task 6.2) and by runtimebundle's temp
// billing-admin-mount handoff tests.

func serveOperator(t *testing.T, mux http.Handler, secret, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if secret != "" {
		req.Header.Set("X-AIP-Diagnostics-Secret", secret)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestOperatorRoutesMountedAndProtected(t *testing.T) {
	t.Parallel()
	first := http.NewServeMux()
	first.HandleFunc("/reconciliations", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"page":{"items":[]}}`))
	})
	second := http.NewServeMux()
	second.HandleFunc("/adjustments", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"page":{"items":[]}}`))
	})
	mux := adminMountsMux(t, adminMountsConfig("operator-secret"),
		AdminMount{Path: "/admin/billing", Handler: first},
		AdminMount{Path: "/admin/other", Handler: second},
	)

	for _, path := range []string{
		"/admin/billing/reconciliations?store_id=test&tenant_id=t",
		"/admin/other/adjustments?store_id=test&account_id=acct",
	} {
		denied := serveOperator(t, mux, "", path)
		if denied.Code != http.StatusForbidden {
			t.Fatalf("path %s without secret status=%d want 403", path, denied.Code)
		}
		allowed := serveOperator(t, mux, "operator-secret", path)
		if allowed.Code != http.StatusOK {
			t.Fatalf("path %s with secret status=%d body=%q", path, allowed.Code, allowed.Body.String())
		}
	}
}

func TestOperatorImportAbsentInReportsOnlyComposition(t *testing.T) {
	t.Parallel()
	reports := http.NewServeMux()
	reports.HandleFunc("/account", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"page":{"items":[]}}`))
	})
	mux := adminMountsMux(t, adminMountsConfig("operator-secret"),
		AdminMount{Path: "/admin/billing", Handler: reports})

	req := httptest.NewRequest(http.MethodPost, "/admin/billing/statement-observations", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AIP-Diagnostics-Secret", "operator-secret")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unregistered import subpath status=%d want 404", rec.Code)
	}
}

func TestOperatorRoutesDisabledWithoutReaders(t *testing.T) {
	t.Parallel()
	// A composition that registers no mount for the reports path serves no
	// operator route there, while a registered neighbor keeps working.
	mux := adminMountsMux(t, adminMountsConfig("operator-secret"),
		AdminMount{Path: "/admin/other", Handler: &adminMountsProbe{body: "ok"}})

	rec := serveOperator(t, mux, "operator-secret", "/admin/billing/reconciliations?store_id=test&tenant_id=t")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 for a path with no registered mount", rec.Code)
	}

	existing := serveOperator(t, mux, "operator-secret", "/admin/other/account?account_id=acct&limit=10")
	if existing.Code != http.StatusOK {
		t.Fatalf("registered neighbor status=%d want 200", existing.Code)
	}
}

func TestFrontendResponsesContainNoOperatorEconomics(t *testing.T) {
	t.Parallel()
	reg := isolationRegistry(t)
	var seen sync.Map
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "ok", &seen)
	mux := http.NewServeMux()
	plugins := []config.PluginConfig{{ID: "openai-legacy", Enabled: true}}
	if err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux: mux,
		Frontends: HTTPFrontendInput{
			Executor: ex, DefaultRouteSelector: "stub:x", Plugins: plugins, Registry: reg,
		},
	}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"stub:x","messages":[{"role":"user","content":"ping"}]}`))
	r = r.WithContext(httpauth.WithPrincipal(r.Context(), execview.PrincipalView{ID: "isolation-test"}))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	forbidden := []string{
		"valuation", "discrepanc", "allowance", "adjustment", "statement_line",
		"selected_cost", "provider_cost", "gross_margin", "reconciliation", "billing",
	}
	var walk func(value any, path string)
	walk = func(value any, path string) {
		switch node := value.(type) {
		case map[string]any:
			for key, child := range node {
				lower := strings.ToLower(key)
				for _, word := range forbidden {
					if strings.Contains(lower, word) {
						t.Fatalf("frontend response field %s contains operator economics %q", path+"/"+key, word)
					}
				}
				walk(child, path+"/"+key)
			}
		case []any:
			for _, child := range node {
				walk(child, path+"/[]")
			}
		}
	}
	walk(out, "")
}
