package stdhttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/internal/testkit/localstubreg"
)

func TestComposeStandardHTTP_routingOverrideAdminNotOnFrontendPaths(t *testing.T) {
	t.Parallel()
	const secret = "override-secret-12"
	cfg := stubPlaneConfig(t, "ovfe", "ok", "ovfe:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
	})
	cfg.Diagnostics.SharedSecret = secret
	cfg.Routing.OverrideAdmin.Enabled = true
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	ps := processFromCfg(t, cfg)
	gen, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() { _ = gen.Close() })

	rrFE := httptest.NewRecorder()
	gen.Handler().ServeHTTP(rrFE, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rrFE.Code == http.StatusNotImplemented {
		t.Fatal("frontend /v1 path must not hit the 501-era admin stub")
	}

	adminPath := "/admin/routing-overrides/a_missing"
	rrAnon := httptest.NewRecorder()
	gen.Handler().ServeHTTP(rrAnon, httptest.NewRequest(http.MethodGet, adminPath, nil))
	if rrAnon.Code != http.StatusForbidden {
		t.Fatalf("unauthenticated admin status=%d want 403 (must not be a frontend protocol response)", rrAnon.Code)
	}

	req := httptest.NewRequest(http.MethodGet, adminPath, nil)
	req.Header.Set(diag.HeaderDiagnosticsSecret, secret)
	rrAuth := httptest.NewRecorder()
	gen.Handler().ServeHTTP(rrAuth, req)
	if rrAuth.Code != http.StatusNotFound {
		t.Fatalf("authenticated admin unknown a-leg status=%d want 404", rrAuth.Code)
	}
	if strings.Contains(strings.ToLower(rrAuth.Body.String()), "chat.completion") {
		t.Fatalf("admin path leaked frontend protocol body: %s", rrAuth.Body.String())
	}

	nested := httptest.NewRecorder()
	gen.Handler().ServeHTTP(nested, httptest.NewRequest(http.MethodGet, "/v1/admin/routing-overrides/a_missing", nil))
	if nested.Code == http.StatusForbidden {
		t.Fatal("frontend-prefixed path must not reach the protected override admin wrapper")
	}
}

func TestComposeStandardHTTP_routingOverrideAdminRequiresAccessAuth(t *testing.T) {
	t.Parallel()
	const apiKey = "test-local-api-key-16"
	cfg := stubPlaneConfig(t, "ovauth", "ok", "ovauth:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
	})
	cfg.Server.Address = "127.0.0.1:0"
	cfg.Server.AuthMode = config.AuthModeExternal
	cfg.Diagnostics.SharedSecret = ""
	cfg.Routing.OverrideAdmin.Enabled = true
	cfg.Access = config.AccessConfig{Mode: "multi_user"}
	cfg.Auth = config.AuthConfig{
		Handler:       "local_api_key",
		RequiredLevel: "api_key",
		LocalAPIKeys: []config.AuthLocalAPIKeyRecord{
			{KeyID: "k1", PrincipalID: "api-user-1", Key: apiKey},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	ps := processFromCfg(t, cfg)
	leg, err := ps.Continuity.CreateALeg(context.Background(), "ov-access-auth")
	if err != nil {
		t.Fatal(err)
	}
	gen, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() { _ = gen.Close() })

	other := httptest.NewRecorder()
	gen.Handler().ServeHTTP(other, httptest.NewRequest(http.MethodGet, "/no-such-route", nil))

	body, err := json.Marshal(map[string]string{"selector": "ovauth:stub-default"})
	if err != nil {
		t.Fatal(err)
	}
	unauth := httptest.NewRequest(http.MethodPut, "/admin/routing-overrides/"+leg.ALegID, bytes.NewReader(body))
	unauth.Header.Set("Content-Type", "application/json")
	denied := httptest.NewRecorder()
	gen.Handler().ServeHTTP(denied, unauth)
	if denied.Code != other.Code {
		t.Fatalf("override admin bypassed access-auth: admin=%d other=%d adminBody=%s", denied.Code, other.Code, denied.Body.String())
	}
	if denied.Code == http.StatusOK {
		t.Fatal("unauthenticated PUT must not mutate override state")
	}

	authed := httptest.NewRequest(http.MethodPut, "/admin/routing-overrides/"+leg.ALegID, bytes.NewReader(body))
	authed.Header.Set("Content-Type", "application/json")
	authed.Header.Set("Authorization", "Bearer "+apiKey)
	allowed := httptest.NewRecorder()
	gen.Handler().ServeHTTP(allowed, authed)
	if allowed.Code != http.StatusOK {
		t.Fatalf("loopback PUT with API key status=%d body=%s", allowed.Code, allowed.Body.String())
	}
}

func TestComposeStandardHTTP_overrideAdminRejectsUnknownBackend(t *testing.T) {
	t.Parallel()
	const secret = "override-secret-12"
	cfg := stubPlaneConfig(t, "ovknown", "ok", "ovknown:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
	})
	cfg.Diagnostics.SharedSecret = secret
	cfg.Routing.OverrideAdmin.Enabled = true
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	ps := processFromCfg(t, cfg)
	leg, err := ps.Continuity.CreateALeg(context.Background(), "ov-unknown-backend")
	if err != nil {
		t.Fatal(err)
	}
	gen, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() { _ = gen.Close() })

	body, err := json.Marshal(map[string]string{"selector": "typo-backend:model"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/admin/routing-overrides/"+leg.ALegID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(diag.HeaderDiagnosticsSecret, secret)
	rr := httptest.NewRecorder()
	gen.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown backend PUT status=%d body=%s want 400", rr.Code, rr.Body.String())
	}

	got, err := ps.RouteOverrideStore.Snapshot(context.Background(), leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Active {
		t.Fatalf("unknown backend must not persist: %+v", got)
	}
}

func TestComposeStandardHTTP_disabledOverrideAdminDoesNotClearPersistedState(t *testing.T) {
	t.Parallel()
	ps := newStdProcess(t)
	if ps.RouteOverrideStore == nil || ps.Continuity == nil {
		t.Fatal("process must expose override store and continuity")
	}
	ctx := context.Background()
	leg, err := ps.Continuity.CreateALeg(ctx, "stdhttp-ov-disabled")
	if err != nil {
		t.Fatal(err)
	}
	st, err := ps.RouteOverrideStore.Replace(ctx, leg.ALegID, "ovfe:stub-default", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Active || st.Revision != 1 {
		t.Fatalf("persisted override: %+v", st)
	}

	cfg := stubPlaneConfig(t, "ovoff", "ok", "ovoff:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
	})
	cfg.Routing.OverrideAdmin.Enabled = false
	gen, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() { _ = gen.Close() })

	rr := httptest.NewRecorder()
	gen.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/admin/routing-overrides/"+leg.ALegID, nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("disabled admin HTTP status=%d want 404", rr.Code)
	}

	got, err := ps.RouteOverrideStore.Snapshot(ctx, leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Active || got.Revision != st.Revision || got.Selector != st.Selector {
		t.Fatalf("disabling admin HTTP must not clear persisted override: got %+v want %+v", got, st)
	}
	if _, ok := routeoverride.AsReader(ps.RouteOverrideStore); !ok {
		t.Fatal("runtime reader capability must remain after admin HTTP is disabled")
	}
}

func TestComposeStandardHTTP_overrideMutationAuditOmitsRawSelector(t *testing.T) {
	t.Parallel()
	const (
		secret   = "override-secret-12"
		selector = "ovaudit:SECRETSEL"
	)
	var logBuf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg := stubPlaneConfig(t, "ovaudit", "ok", "ovaudit:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
	})
	cfg.Diagnostics.SharedSecret = secret
	cfg.Routing.OverrideAdmin.Enabled = true
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	ps := processFromCfgLog(t, cfg, log)
	leg, err := ps.Continuity.CreateALeg(context.Background(), "stdhttp-ov-audit")
	if err != nil {
		t.Fatal(err)
	}
	gen, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() { _ = gen.Close() })

	body, err := json.Marshal(map[string]string{"selector": selector})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/admin/routing-overrides/"+leg.ALegID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(diag.HeaderDiagnosticsSecret, secret)
	rr := httptest.NewRecorder()
	gen.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), selector) {
		t.Fatalf("protected PUT must return raw selector: %s", rr.Body.String())
	}

	logs := logBuf.String()
	foundAudit := false
	for line := range strings.SplitSeq(logs, "\n") {
		if !strings.Contains(line, diag.RouteOverrideMutationLogMsg) {
			continue
		}
		foundAudit = true
		if strings.Contains(line, selector) || strings.Contains(line, "SECRETSEL") {
			t.Fatalf("raw selector leaked into mutation log: %s", line)
		}
		if strings.Contains(line, leg.ALegID) {
			t.Fatalf("raw A-leg leaked into mutation log: %s", line)
		}
	}
	if !foundAudit {
		t.Fatalf("expected mutation audit log, got %s", logs)
	}
}

func processFromCfg(t *testing.T, cfg *config.Config) *runtimebundle.ProcessServices {
	t.Helper()
	return processFromCfgLog(t, cfg, testkit.DiscardLogger())
}

func processFromCfgLog(t *testing.T, cfg *config.Config, log *slog.Logger) *runtimebundle.ProcessServices {
	t.Helper()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	if err := localstubreg.RegisterInProcess(reg); err != nil {
		t.Fatal(err)
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  log,
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	return ps
}
