package stdhttp

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/ledgerstore"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	cpadmin "github.com/aiproxer/aiproxer/internal/stdhttp/admin/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

func controlPlaneMountConfig(queryEnabled bool) *config.Config {
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    config.RoutingConfig{DefaultRoute: "stub:model"},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: config.DiagnosticsConfig{
			Enabled:      true,
			HealthPath:   "/healthz",
			SharedSecret: "secretsecret",
		},
		Plugins: config.PluginsConfig{},
	}
	cfg.ControlPlane.Enabled = true
	cfg.ControlPlane.Store = "memory"
	cfg.ControlPlane.RecordingPolicy = "best_effort"
	cfg.ControlPlane.Query.Enabled = queryEnabled
	cfg.ControlPlane.Query.PathPrefix = "/cp"
	return cfg
}

func controlPlaneHTTPInput(t *testing.T, cfg *config.Config) StandardHTTPInput {
	t.Helper()
	store, err := ledgerstore.NewMemoryStore(ledgerstore.MemoryConfig{StoreID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	queries := controlplane.NewQueryService(store, status, controlplane.QueryServiceConfig{
		Enabled:         cfg.ControlPlane.Query.Enabled,
		DefaultPageSize: cfg.ControlPlane.Query.DefaultPageSize,
		MaxPageSize:     cfg.ControlPlane.Query.MaxPageSize,
	})
	ex := runtime.TestExecutor()
	reg := pluginreg.NewRegistry()
	return StandardHTTPInput{
		Core:      HTTPCoreInput{Executor: ex},
		Frontends: frontendInputForTest(cfg, ex, reg),
		Operations: HTTPOperationsInput{
			ControlPlaneQueries: cpadmin.AdaptControlPlaneQueries(queries),
		},
	}
}

func TestControlPlaneQuery_MountedWhenEnabledAndProtected(t *testing.T) {
	t.Parallel()
	cfg := controlPlaneMountConfig(true)
	in := controlPlaneHTTPInput(t, cfg)
	app, err := runtime.New(runtime.Options{Config: cfg, Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	startTestApp(ctx, t, app)
	h, err := ComposeStandardHTTP(ctx, cfg, slog.Default(), in)
	if err != nil {
		t.Fatal(err)
	}

	// Without shared secret -> 403 (protected).
	missing := httptest.NewRecorder()
	h.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/cp/status", nil))
	if missing.Code != http.StatusForbidden {
		t.Fatalf("missing secret: got %d, want 403", missing.Code)
	}

	// With shared secret -> 200 and ready status.
	allowed := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/cp/status", nil)
	req.Header.Set("X-AIP-Diagnostics-Secret", "secretsecret")
	h.ServeHTTP(allowed, req)
	if allowed.Code != http.StatusOK {
		t.Fatalf("allowed status: got %d, want 200 body=%s", allowed.Code, allowed.Body.String())
	}
}

func TestControlPlaneQuery_NotMountedWhenDisabled(t *testing.T) {
	t.Parallel()
	cfg := controlPlaneMountConfig(false)
	in := controlPlaneHTTPInput(t, cfg)
	app, err := runtime.New(runtime.Options{Config: cfg, Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	startTestApp(ctx, t, app)
	h, err := ComposeStandardHTTP(ctx, cfg, slog.Default(), in)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/cp/status", nil)
	req.Header.Set("X-AIP-Diagnostics-Secret", "secretsecret")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("disabled: got %d, want 404", rr.Code)
	}
}
