package stdhttp_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/internal/testkit/localstubreg"
	"gopkg.in/yaml.v3"
)

// TestComposeStandardHTTP_RouteConflictRejects proves the canonical composer
// (invoked by CompileGeneration on the production path) preserves
// route-conflict rejection behavior (task 3.4–3.5, req 9.1-9.7).
func TestComposeStandardHTTP_RouteConflictRejects(t *testing.T) {
	t.Parallel()
	ps := newStdProcess(t)
	_, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubPlaneConfig(t, "rc", "x", "rc:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
			{ID: "responses-dup", Kind: "openai-responses", Enabled: true},
		}),
		Compose: stdhttp.ComposeStandardHTTP,
	})
	if err == nil {
		t.Fatal("expected route conflict")
	}
	if !errors.Is(err, stdhttp.ErrRouteConflict) {
		t.Fatalf("want ErrRouteConflict, got %v", err)
	}
}

func TestComposeStandardHTTP_ManagementRoutesNotMounted(t *testing.T) {
	t.Parallel()
	ps := newStdProcess(t)
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubPlaneConfig(t, "mgmt", "m", "mgmt:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })

	for _, path := range []string{"/admin/config/reload", "/admin/config/status"} {
		rr := httptest.NewRecorder()
		bundle.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s must stay outside swappable graph: %d", path, rr.Code)
		}
	}
}

func TestComposeStandardHTTP_NilInputsRejected(t *testing.T) {
	t.Parallel()
	ps := newStdProcess(t)
	cfg := stubPlaneConfig(t, "nil", "ok", "nil:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
	})
	cand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{Process: ps, Candidate: cfg})
	if err != nil {
		t.Fatalf("CompileCandidate: %v", err)
	}
	t.Cleanup(func() { _ = cand.Close() })
	in := stdhttp.StandardHTTPInput{
		Core:      stdhttp.HTTPCoreInput{Executor: cand.Executor()},
		Frontends: stdhttp.HTTPFrontendInput{Registry: cand.PluginRegistry(), Executor: cand.Executor()},
	}
	log := testkit.DiscardLogger()
	cases := []struct {
		name string
		ctx  context.Context
		cfg  *config.Config
		in   stdhttp.StandardHTTPInput
		want string
	}{
		{name: "nil_ctx", ctx: nil, cfg: cfg, in: in, want: "nil context"},
		{name: "nil_cfg", ctx: context.Background(), cfg: nil, in: in, want: "nil config"},
		{name: "nil_executor", ctx: context.Background(), cfg: cfg, in: stdhttp.StandardHTTPInput{Frontends: in.Frontends}, want: "nil executor"},
		{name: "nil_registry", ctx: context.Background(), cfg: cfg, in: stdhttp.StandardHTTPInput{Core: in.Core}, want: "nil plugin registry"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := stdhttp.ComposeStandardHTTP(tc.ctx, tc.cfg, log, tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want substring %q", err, tc.want)
			}
		})
	}
	_, err = stdhttp.ComposeStandardHTTP(context.Background(), cfg, nil, in)
	if err == nil || !strings.Contains(err.Error(), "nil logger") {
		t.Fatalf("nil logger: err=%v", err)
	}
}

func TestGenerationDispatcher_CoexistOldNewHandlers(t *testing.T) {
	t.Parallel()
	ps := newStdProcess(t)
	oldBundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: stubPlaneConfig(t, "old", "OLD", "old:stub-default", []config.PluginConfig{{ID: "openai-responses", Enabled: true}}),
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatal(err)
	}
	newBundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: stubPlaneConfig(t, "new", "NEW", "new:stub-default", []config.PluginConfig{{ID: "openai-responses", Enabled: true}}),
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatal(err)
	}

	mgr := runtimehost.NewManager(4, nil)
	disp := runtimehost.NewGenerationDispatcher(mgr)
	if err := mgr.Publish(mgr.PrepareRequestPlane("old", oldBundle)); err != nil {
		t.Fatal(err)
	}
	lease, ok := mgr.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	defer lease.Release()
	if err := mgr.Publish(mgr.PrepareRequestPlane("new", newBundle)); err != nil {
		t.Fatal(err)
	}

	bodyOld := postPlaneResponses(t, lease.Handler(), "stub-default")
	bodyNew := postPlaneResponses(t, disp, "stub-default")
	if !strings.Contains(bodyOld, `"text":"OLD"`) || strings.Contains(bodyOld, `"text":"NEW"`) {
		t.Fatalf("old=%s", bodyOld)
	}
	if !strings.Contains(bodyNew, `"text":"NEW"`) || strings.Contains(bodyNew, `"text":"OLD"`) {
		t.Fatalf("new=%s", bodyNew)
	}
}

func newStdProcess(t *testing.T) *runtimebundle.ProcessServices {
	t.Helper()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	if err := localstubreg.RegisterInProcess(reg); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing:     config.RoutingConfig{MaxAttempts: 3},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes:    1024,
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 4096,
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	return ps
}

func stubPlaneConfig(t *testing.T, backendID, text, defaultRoute string, frontends []config.PluginConfig) *config.Config {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal(fmt.Appendf(nil, "text: %q\ninput_tokens: 1\noutput_tokens: 1\n", text), &n); err != nil {
		t.Fatal(err)
	}
	for n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = *n.Content[0]
	}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3, DefaultRoute: defaultRoute},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes:    1024,
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 4096,
		},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins: config.PluginsConfig{
			Frontends: frontends,
			Backends: []config.PluginConfig{{
				Kind: "local-stub", ID: backendID, Enabled: true, Config: n,
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return cfg
}

func postPlaneResponses(t *testing.T, h http.Handler, model string) string {
	t.Helper()
	body := fmt.Appendf(nil, `{"model":%q,"stream":false,"input":[{"role":"user","content":"ping"}]}`, model)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rr.Code, rr.Body.String())
	}
	return rr.Body.String()
}
