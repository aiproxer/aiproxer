package stdhttp

import (
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

// Task 4.2: the economics health route lives under the runtimebundle-provided
// temporary billing admin mount at the configured reports path. These tests
// retain the generic mount-level protection and path assertions; the real
// reader/handler/HealthSink -> actual host process registry proof lives in
// runtimebundle's protected health-request handoff test, and the billing
// package keeps its own reader-absence route test until the package moves.

func TestOperatorHealthMountedAndProtected(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Diagnostics: config.DiagnosticsConfig{SharedSecret: "health-secret"}}
	health := http.NewServeMux()
	health.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"queues":[{"queue":"customer","pending":3}]}`))
	})
	mux := adminMountsMux(t, cfg, AdminMount{Path: "/admin/billing", Handler: health})

	denied := serveOperator(t, mux, "", "/admin/billing/health")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("health without secret status=%d want 403", denied.Code)
	}
	allowed := serveOperator(t, mux, "health-secret", "/admin/billing/health")
	if allowed.Code != http.StatusOK {
		t.Fatalf("health with secret status=%d body=%q", allowed.Code, allowed.Body.String())
	}
}

func TestOperatorHealthDisabledWithoutReader(t *testing.T) {
	t.Parallel()
	// A mount that serves no health route never fabricates one: the same
	// reports path keeps 404 for an unregistered subpath even with the secret.
	accounts := http.NewServeMux()
	accounts.HandleFunc("/account", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux := adminMountsMux(t, adminMountsConfig("health-secret"), AdminMount{Path: "/admin/billing", Handler: accounts})

	rec := serveOperator(t, mux, "health-secret", "/admin/billing/health")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("health without a route status=%d want 404 disabled", rec.Code)
	}
	ok := serveOperator(t, mux, "health-secret", "/admin/billing/account?account_id=acct&limit=10")
	if ok.Code != http.StatusOK {
		t.Fatalf("registered account route status=%d want 200", ok.Code)
	}
}
