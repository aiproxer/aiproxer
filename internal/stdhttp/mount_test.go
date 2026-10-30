package stdhttp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	keepwarm "github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	adminkeepwarm "github.com/aiproxer/aiproxer/internal/stdhttp/admin/keepwarm"
	adminaccounting "github.com/aiproxer/aiproxer/internal/stdhttp/admin/tokenaccounting"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

func TestMountBundledFrontends_geminiDoesNotRegisterRoot(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	var healthCalled bool
	mux.HandleFunc("/healthz", func(http.ResponseWriter, *http.Request) {
		healthCalled = true
	})
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "ok", nil)
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	plugins := []config.PluginConfig{
		{ID: gemini.ID, Enabled: true},
	}
	if err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux: mux,
		Frontends: HTTPFrontendInput{
			Executor:             ex,
			DefaultRouteSelector: "stub:gemini-2.0-flash",
			Plugins:              plugins,
			MaxRequestBodyBytes:  0,
			Registry:             reg,
		},
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if !healthCalled {
		t.Fatal("expected /healthz to hit explicit handler, not Gemini")
	}
}

func TestKeepwarmAdminMountedWithDiagnosticsSecret(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Diagnostics: config.DiagnosticsConfig{Enabled: true, SharedSecret: "secretsecret"}}
	stub := &keepwarmMountPolicyStub{}
	mux := http.NewServeMux()
	mountKeepwarmAdmin(mountAccountingAdminInput{LogCtx: context.Background(), Mux: mux, Cfg: cfg, Log: slog.Default(), Operations: HTTPOperationsInput{KeepwarmAdminEnabled: true, KeepwarmAdmin: adminkeepwarm.Options{Enabled: true, Service: stub, ResolveALegID: func(context.Context, *http.Request) (string, error) { return "a-authority", nil }}}})
	missing := httptest.NewRecorder()
	mux.ServeHTTP(missing, httptest.NewRequest(http.MethodPost, "/admin/keepwarm/disable", strings.NewReader(`{}`)))
	if missing.Code != http.StatusForbidden {
		t.Fatalf("missing secret status=%d", missing.Code)
	}
	allowed := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/keepwarm/disable", strings.NewReader(`{}`))
	req.Header.Set(diag.HeaderDiagnosticsSecret, "secretsecret")
	mux.ServeHTTP(allowed, req)
	if allowed.Code != http.StatusOK || !stub.disabled {
		t.Fatalf("status=%d body=%s disabled=%v", allowed.Code, allowed.Body.String(), stub.disabled)
	}
}

// #3: the keep-warm admin surface must not mount at all when the diagnostics
// shared secret is unset; an empty secret otherwise leaves it unauthenticated.
func TestKeepwarmAdminNotMountedWithoutDiagnosticsSecret(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Diagnostics: config.DiagnosticsConfig{Enabled: true}}
	stub := &keepwarmMountPolicyStub{}
	mux := http.NewServeMux()
	mountKeepwarmAdmin(mountAccountingAdminInput{LogCtx: context.Background(), Mux: mux, Cfg: cfg, Log: slog.Default(), Operations: HTTPOperationsInput{KeepwarmAdminEnabled: true, KeepwarmAdmin: adminkeepwarm.Options{Enabled: true, Service: stub, ResolveALegID: func(context.Context, *http.Request) (string, error) { return "a-authority", nil }}}})
	req := httptest.NewRequest(http.MethodPost, "/admin/keepwarm/disable", strings.NewReader(`{}`))
	req.Header.Set(diag.HeaderDiagnosticsSecret, "anything")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("keep-warm admin mounted without secret: status=%d", rec.Code)
	}
}

type keepwarmMountPolicyStub struct{ disabled bool }

func (s *keepwarmMountPolicyStub) Disable(string) (keepwarm.SessionPolicy, error) {
	s.disabled = true
	return keepwarm.SessionPolicy{Disabled: true, Revision: 1}, nil
}
func (s *keepwarmMountPolicyStub) Clear(string) error { s.disabled = false; return nil }
func (s *keepwarmMountPolicyStub) Get(string) (keepwarm.SessionPolicy, bool) {
	return keepwarm.SessionPolicy{Disabled: s.disabled}, s.disabled
}

func TestTokenAccountingAdminMountedWithDiagnosticsSecret(t *testing.T) {
	t.Parallel()
	cfg := tokenAccountingAdminTestConfig(true)
	svc := accountingapp.NewService(accountingapp.ServiceConfig{Mode: accountingapp.ModeLocalOnly}, nil, fixedLocalCounter{})
	ex := runtime.TestExecutor()
	ex.AdminCountService = svc
	app := mustRuntimeApp(t, cfg)
	ctx := context.Background()
	startTestApp(ctx, t, app)
	reg := pluginreg.NewRegistry()
	in := StandardHTTPInput{
		Core:      HTTPCoreInput{Executor: ex},
		Frontends: frontendInputForTest(cfg, ex, reg),
		Operations: HTTPOperationsInput{
			TokenAccountingAdmin: adminaccounting.AdaptCountCallService(svc),
			Registrations:        httpcontract.CloneRegistrations(app.Registrations()),
		},
	}
	h, err := ComposeStandardHTTP(ctx, cfg, slog.Default(), in)
	if err != nil {
		t.Fatal(err)
	}

	missing := httptest.NewRecorder()
	h.ServeHTTP(missing, httptest.NewRequest(http.MethodPost, "/admin/token-count", strings.NewReader(tokenAccountingAdminBody())))
	if missing.Code != http.StatusForbidden {
		t.Fatalf("missing secret status %d body=%s", missing.Code, missing.Body.String())
	}

	allowed := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/token-count", strings.NewReader(tokenAccountingAdminBody()))
	req.Header.Set(diag.HeaderDiagnosticsSecret, "secretsecret")
	h.ServeHTTP(allowed, req)
	if allowed.Code != http.StatusOK {
		t.Fatalf("allowed status %d body=%s", allowed.Code, allowed.Body.String())
	}
	if !strings.Contains(allowed.Body.String(), `"client_visible"`) {
		t.Fatalf("response missing count body: %s", allowed.Body.String())
	}
	if strings.Contains(allowed.Body.String(), "secret prompt") {
		t.Fatalf("response leaked request content: %s", allowed.Body.String())
	}
}

func TestTokenAccountingAdminDisabledNotRegistered(t *testing.T) {
	t.Parallel()
	cfg := tokenAccountingAdminTestConfig(false)
	ex := runtime.TestExecutor()
	app := mustRuntimeApp(t, cfg)
	ctx := context.Background()
	startTestApp(ctx, t, app)
	reg := pluginreg.NewRegistry()
	in := StandardHTTPInput{
		Core:       HTTPCoreInput{Executor: ex},
		Frontends:  frontendInputForTest(cfg, ex, reg),
		Operations: HTTPOperationsInput{Registrations: httpcontract.CloneRegistrations(app.Registrations())},
	}
	h, err := ComposeStandardHTTP(ctx, cfg, slog.Default(), in)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/token-count", strings.NewReader(tokenAccountingAdminBody()))
	req.Header.Set(diag.HeaderDiagnosticsSecret, "secretsecret")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestTokenAccountingAdminMountedBodyLimitDoesNotEchoContent(t *testing.T) {
	t.Parallel()
	cfg := tokenAccountingAdminTestConfig(true)
	cfg.Accounting.Admin.MaxBodyBytes = 16
	svc := accountingapp.NewService(accountingapp.ServiceConfig{Mode: accountingapp.ModeLocalOnly}, nil, fixedLocalCounter{})
	ex := runtime.TestExecutor()
	ex.AdminCountService = svc
	app := mustRuntimeApp(t, cfg)
	ctx := context.Background()
	startTestApp(ctx, t, app)
	reg := pluginreg.NewRegistry()
	in := StandardHTTPInput{
		Core:      HTTPCoreInput{Executor: ex},
		Frontends: frontendInputForTest(cfg, ex, reg),
		Operations: HTTPOperationsInput{
			TokenAccountingAdmin: adminaccounting.AdaptCountCallService(svc),
			Registrations:        httpcontract.CloneRegistrations(app.Registrations()),
		},
	}
	h, err := ComposeStandardHTTP(ctx, cfg, slog.Default(), in)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/token-count", strings.NewReader(tokenAccountingAdminBody()))
	req.Header.Set(diag.HeaderDiagnosticsSecret, "secretsecret")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "secret prompt") || strings.Contains(rr.Body.String(), "Messages") {
		t.Fatalf("response leaked request content: %s", rr.Body.String())
	}
}

// TestTokenAccountingAdmin_explicitServiceUsed pins that the HTTP adapter mounts
// exactly the projected admin service and never consults the executor: the
// executor carries a failing count service that would surface as an error if
// the adapter reached into it.
func TestTokenAccountingAdmin_explicitServiceUsed(t *testing.T) {
	t.Parallel()
	cfg := tokenAccountingAdminTestConfig(true)
	explicit := accountingapp.NewService(accountingapp.ServiceConfig{Mode: accountingapp.ModeLocalOnly}, nil, fixedLocalCounter{})
	ignored := accountingapp.NewService(accountingapp.ServiceConfig{Mode: accountingapp.ModeLocalOnly}, nil, failingLocalCounter{})
	ex := runtime.TestExecutor()
	ex.AdminCountService = ignored
	app := mustRuntimeApp(t, cfg)
	ctx := context.Background()
	startTestApp(ctx, t, app)
	reg := pluginreg.NewRegistry()
	in := StandardHTTPInput{
		Core:      HTTPCoreInput{Executor: ex},
		Frontends: frontendInputForTest(cfg, ex, reg),
		Operations: HTTPOperationsInput{
			TokenAccountingAdmin: adminaccounting.AdaptCountCallService(explicit),
			Registrations:        httpcontract.CloneRegistrations(app.Registrations()),
		},
	}
	h, err := ComposeStandardHTTP(ctx, cfg, slog.Default(), in)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/token-count", strings.NewReader(tokenAccountingAdminBody()))
	req.Header.Set(diag.HeaderDiagnosticsSecret, "secretsecret")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("explicit service status %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"client_visible"`) {
		t.Fatalf("response missing count body: %s", rr.Body.String())
	}
}

func TestTokenAccountingAdmin_nilServiceReturnsUnavailable(t *testing.T) {
	t.Parallel()
	cfg := tokenAccountingAdminTestConfig(true)
	ex := runtime.TestExecutor()
	// No TokenAccountingAdmin and no AdminCountService.
	app := mustRuntimeApp(t, cfg)
	ctx := context.Background()
	startTestApp(ctx, t, app)
	reg := pluginreg.NewRegistry()
	in := StandardHTTPInput{
		Core:       HTTPCoreInput{Executor: ex},
		Frontends:  frontendInputForTest(cfg, ex, reg),
		Operations: HTTPOperationsInput{Registrations: httpcontract.CloneRegistrations(app.Registrations())},
	}
	h, err := ComposeStandardHTTP(ctx, cfg, slog.Default(), in)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/token-count", strings.NewReader(tokenAccountingAdminBody()))
	req.Header.Set(diag.HeaderDiagnosticsSecret, "secretsecret")
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "count_unavailable") {
		t.Fatalf("body=%s", rr.Body.String())
	}
}

type failingLocalCounter struct{}

func (failingLocalCounter) CountText(context.Context, accountingapp.CountTextInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{}, errors.New("fallback must not be used")
}

func (failingLocalCounter) CountCall(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{}, errors.New("fallback must not be used")
}

func (failingLocalCounter) CountOutput(context.Context, accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{}, errors.New("fallback must not be used")
}

func tokenAccountingAdminTestConfig(enabled bool) *config.Config {
	return &config.Config{
		Server:      config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:     config.RoutingConfig{DefaultRoute: "stub:model"},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz", SharedSecret: "secretsecret"},
		Accounting: config.AccountingConfig{
			Enabled: true,
			Mode:    "local_only",
			Admin:   config.AccountingAdminConfig{Enabled: enabled, Path: "/admin/token-count", MaxBodyBytes: 1024},
		},
		Plugins: config.PluginsConfig{},
	}
}

func mustRuntimeApp(t *testing.T, cfg *config.Config) *runtime.App {
	t.Helper()
	app, err := runtime.New(runtime.Options{Config: cfg, Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func tokenAccountingAdminBody() string {
	return `{"backend":"stub","model":"model","call":{"ID":"call-1","Messages":[{"Role":"user","Parts":[{"Kind":"text","Text":"secret prompt"}]}]}}`
}

type fixedLocalCounter struct{}

func (fixedLocalCounter) CountText(context.Context, accountingapp.CountTextInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{InputTokens: 1, TotalTokens: 1, Accounting: aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneClientVisible, Source: aipapi.UsageSourceLocalTokenizer, Authority: aipapi.UsageAuthorityEstimated}}, nil
}

func (fixedLocalCounter) CountCall(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{InputTokens: 7, TotalTokens: 7, Accounting: aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneClientVisible, Source: aipapi.UsageSourceLocalTokenizer, Authority: aipapi.UsageAuthorityEstimated}}, nil
}

func (fixedLocalCounter) CountOutput(context.Context, accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	return accountingapp.CountResult{OutputTokens: 3, TotalTokens: 3, Accounting: aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneClientVisible, Source: aipapi.UsageSourceLocalTokenizer, Authority: aipapi.UsageAuthorityEstimated}}, nil
}

func TestMountBundledFrontends_explicitRegistryMissingFrontend(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "ok", nil)
	err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux: mux,
		Frontends: HTTPFrontendInput{
			Executor:             ex,
			DefaultRouteSelector: "stub:x",
			Plugins:              []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
			MaxRequestBodyBytes:  0,
			Registry:             reg,
		},
	})
	if err == nil {
		t.Fatal("expected error when registry lacks frontend factories")
	}
	if !strings.Contains(err.Error(), `unknown frontend plugin "openai-responses"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestComposeStandardHTTP_diagnosticsHealthzMounted locks the diagnostics mount block in
// prepareStandardHandler: when Diagnostics.Enabled is true with a HealthPath, GET <health>
// returns 200 through the assembled standard handler.
func TestComposeStandardHTTP_diagnosticsHealthzMounted(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Server:      config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:     config.RoutingConfig{DefaultRoute: "stub:model"},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz", SharedSecret: "secretsecret"},
		Plugins:     config.PluginsConfig{},
	}
	ex := runtime.TestExecutor()
	app := mustRuntimeApp(t, cfg)
	ctx := context.Background()
	startTestApp(ctx, t, app)
	reg := pluginreg.NewRegistry()
	in := StandardHTTPInput{
		Core:       HTTPCoreInput{Executor: ex},
		Frontends:  frontendInputForTest(cfg, ex, reg),
		Operations: HTTPOperationsInput{Registrations: httpcontract.CloneRegistrations(app.Registrations())},
	}
	h, err := ComposeStandardHTTP(ctx, cfg, slog.Default(), in)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("healthz status %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestComposeStandardHTTP_openAIModelsAndModelRegistryDiagMounted(t *testing.T) {
	t.Parallel()
	const secret = "secretsecret"
	provider := &mutableInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-4o",
		NativeID:    "native-hidden",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "openai-a",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    config.RoutingConfig{DefaultRoute: "openai-responses:gpt-4o"},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: config.DiagnosticsConfig{
			Enabled:      true,
			HealthPath:   "/healthz",
			SharedSecret: secret,
		},
		ModelInventory: config.ModelInventoryConfig{
			DiagnosticsPath: "/debug/model-registry",
		},
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
		},
	}
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	app := mustRuntimeApp(t, cfg)
	ctx := context.Background()
	startTestApp(ctx, t, app)
	in := StandardHTTPInput{
		Core:      HTTPCoreInput{Executor: ex},
		Frontends: frontendInputForTest(cfg, ex, reg),
		Models:    HTTPModelInput{ModelRegistryRuntime: rt},
	}
	h, err := ComposeStandardHTTP(ctx, cfg, slog.Default(), in)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/v1/models status %d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "native-hidden") {
		t.Fatalf("native leak: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "openai-a:openai/gpt-4o") {
		t.Fatalf("missing pinned id: %s", rr.Body.String())
	}
	etag := rr.Header().Get("ETag")
	if etag == "" {
		t.Fatal("/v1/models missing ETag")
	}
	req304 := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req304.Header.Set("If-None-Match", etag)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req304)
	if rr.Code != http.StatusNotModified {
		t.Fatalf("/v1/models If-None-Match status %d want 304", rr.Code)
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/debug/model-registry", nil))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("unauthed model-registry status %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/debug/model-registry", nil)
	req.Header.Set(diag.HeaderDiagnosticsSecret, secret)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("authed model-registry status %d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "native-hidden") {
		t.Fatalf("native leak in diag: %s", rr.Body.String())
	}
}
