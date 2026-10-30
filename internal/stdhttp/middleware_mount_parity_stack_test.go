package stdhttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	coreconfig "github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

// TestStandardMiddlewareMountParity_StackOrderObservables freezes the load-bearing
// outer→inner middleware order via observable response behavior (Server header
// wins outermost; inner panic becomes safe 500). Used while broad Built mount
// inputs are replaced (tasks 3.1–3.5).
func TestStandardMiddlewareMountParity_StackOrderObservables(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Server", "inner-must-lose")
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/panic", func(http.ResponseWriter, *http.Request) {
		panic("parity-stack-panic-leak")
	})
	cfg := &coreconfig.Config{
		Logging: coreconfig.LoggingConfig{AccessLog: false},
		Identity: identity.Config{
			Downstream: identity.DownstreamPolicy{
				Server: identity.FieldPolicy{Mode: identity.ModeCustom, Value: "OuterParityGW"},
			},
		},
	}
	h := stackHTTPHandler(stackHTTPInput{
		Cfg: cfg, Log: testkit.DiscardLogger(), Security: HTTPSecurityInput{},
		TraceGen: diag.NewTraceIDGenerator(), Inner: mux,
	})

	ok := httptest.NewRecorder()
	h.ServeHTTP(ok, httptest.NewRequest(http.MethodGet, "/ok", nil))
	if ok.Code != http.StatusNoContent {
		t.Fatalf("ok status=%d", ok.Code)
	}
	if got := ok.Header().Get("Server"); got != "OuterParityGW" {
		t.Fatalf("Server=%q want OuterParityGW (DownstreamServerMiddleware outermost)", got)
	}

	pr := httptest.NewRecorder()
	h.ServeHTTP(pr, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if pr.Code != http.StatusInternalServerError {
		t.Fatalf("panic status=%d want 500", pr.Code)
	}
	body := pr.Body.String()
	if strings.Contains(body, "parity-stack-panic-leak") {
		t.Fatalf("body leaked panic: %q", body)
	}
	if got := pr.Header().Get("Server"); got != "OuterParityGW" {
		t.Fatalf("recovered panic Server=%q want OuterParityGW", got)
	}
}
