package stdhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	coreconfig "github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

// rejectAllAuthProvider always rejects so protected business handlers must not run.
type rejectAllAuthProvider struct{}

func (rejectAllAuthProvider) Authenticate(context.Context, http.ResponseWriter, *http.Request) (httpauth.AuthenticationResult, error) {
	return httpauth.AuthenticationResult{
		Type:       httpauth.TypeReject,
		HTTPStatus: http.StatusUnauthorized,
		Body:       []byte(`{"error":"auth"}`),
	}, nil
}

// TestMountContract_StackAuthBeforeInnerHandler freezes auth-before-business-logic
// on the full stackHTTPHandler composition (Task 3.1 characterization gap fill).
// Bundled-frontend auth suites cover Middleware alone; this locks the mount stack
// order where transport auth wraps the route mux innermost.
func TestMountContract_StackAuthBeforeInnerHandler(t *testing.T) {
	t.Parallel()
	var innerHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/protected", func(http.ResponseWriter, *http.Request) {
		innerHits.Add(1)
		t.Error("inner business handler must not run when auth rejects")
	})
	cfg := &coreconfig.Config{Logging: coreconfig.LoggingConfig{AccessLog: false}}
	h := stackHTTPHandler(stackHTTPInput{
		Cfg: cfg, Log: testkit.DiscardLogger(),
		Security: HTTPSecurityInput{
			HTTPAuthProviders: []httpauth.Provider{rejectAllAuthProvider{}},
		},
		TraceGen: diag.NewTraceIDGenerator(), Inner: mux,
	})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/protected", strings.NewReader(`{}`)))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rr.Code)
	}
	if innerHits.Load() != 0 {
		t.Fatalf("inner hits=%d want 0 (auth must run before protected handler)", innerHits.Load())
	}
}

// TestMountContract_NilOptionalCapabilitiesSkipMounts freezes nil/disabled optional
// mount behavior for metrics and control-plane query surfaces used during Built→group
// migration (Task 3.1).
func TestMountContract_NilOptionalCapabilitiesSkipMounts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := &coreconfig.Config{
		Observability: coreconfig.ObservabilityConfig{
			Metrics: coreconfig.MetricsConfig{Enabled: false, Path: "/metrics"},
		},
		Diagnostics: coreconfig.DiagnosticsConfig{Enabled: false},
	}
	mux := http.NewServeMux()
	httpProm, err := mountMetrics(mountMetricsInput{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: testkit.DiscardLogger(),
		Operations: HTTPOperationsInput{},
	})
	if err != nil {
		t.Fatalf("mountMetrics disabled: %v", err)
	}
	if httpProm != nil {
		t.Fatal("disabled metrics must return nil HTTPProm")
	}
	// Disabled metrics path must not be registered.
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("disabled metrics route status=%d want 404", rr.Code)
	}

	mountControlPlaneQuery(controlPlaneQueryMount{
		LogCtx: ctx, Mux: mux, Cfg: cfg, Log: testkit.DiscardLogger(),
		Operations: HTTPOperationsInput{}, // nil queries → no mount
	})
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/cp/status", nil))
	if rr2.Code != http.StatusNotFound {
		t.Fatalf("nil control-plane queries status=%d want 404", rr2.Code)
	}
}
