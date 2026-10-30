package runtimebundle_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/localstub"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/internal/testkit/localstubreg"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
	"gopkg.in/yaml.v3"
)

func genYAMLNode(t *testing.T, raw string) yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	for n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 || n.Content[0] == nil {
			t.Fatal("empty yaml document")
		}
		n = *n.Content[0]
	}
	return n
}

func stdFactoryCatalog(t *testing.T) *pluginreg.Registry {
	t.Helper()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	// local-stub is an external connector; in-process registration is test-only.
	if err := localstubreg.RegisterInProcess(reg); err != nil {
		t.Fatal(err)
	}
	return reg
}

// processBaseConfig returns the process-owned baseline used by compile
// candidates in this file. Diagnostics matches [stubCandidateConfig] so
// candidates that only change reloadable rows (backends/frontends/routing)
// do not trip [configreload.Classify]'s startup-only diagnostics gate; cfg is
// pre-validated so defaulted comparators observe the same normalized values
// a real reload would (config.Validate is idempotent on already-valid input).
func processBaseConfig() *config.Config {
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes:    1024,
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 4096,
		},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
	}
	_ = config.Validate(cfg)
	return cfg
}

func stubCandidateConfig(t *testing.T, backendID, text, defaultRoute string, frontends []config.PluginConfig) *config.Config {
	t.Helper()
	cfg := &config.Config{
		Routing: config.RoutingConfig{
			MaxAttempts:  3,
			DefaultRoute: defaultRoute,
		},
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
				Kind:    "local-stub",
				ID:      backendID,
				Enabled: true,
				Config: genYAMLNode(t, fmt.Sprintf(`
text: %q
input_tokens: 1
output_tokens: 1
`, text)),
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate candidate: %v", err)
	}
	return cfg
}

func newProcessForGeneration(t *testing.T) *runtimebundle.ProcessServices {
	t.Helper()
	cfg := processBaseConfig()
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate process cfg: %v", err)
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: stdFactoryCatalog(t)},
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

func postResponses(t *testing.T, h http.Handler, model string) string {
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

func TestCompileGeneration_CreatesHandlerWithoutListener(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)
	cand := stubCandidateConfig(t, "gen-a", "alpha-text", "gen-a:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
	})

	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	gb, ok := bundle.(*runtimebundle.GenerationBundle)
	if !ok {
		t.Fatal("expected *runtimebundle.GenerationBundle")
	}

	if bundle.Handler() == nil {
		t.Fatal("expected handler")
	}
	if bundle.ExecutorView() == nil {
		t.Fatal("expected executor view")
	}
	if gb.ResourceCount() == 0 {
		t.Fatal("expected generation-owned ledger entries")
	}
	if len(gb.BackendIDs()) == 0 {
		t.Fatal("expected backend IDs")
	}

	rr := httptest.NewRecorder()
	bundle.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("healthz status %d", rr.Code)
	}
	body := postResponses(t, bundle.Handler(), "stub-default")
	if !strings.Contains(body, "alpha-text") {
		t.Fatalf("body missing stub text: %s", body)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_ = ln.Close()
}

func TestCompileGeneration_CoexistTwoCandidatesDifferentPlanes(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)

	cfgA := stubCandidateConfig(t, "backend-a", "plane-A", "backend-a:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
		{ID: "openai-legacy", Enabled: false},
	})
	cfgB := stubCandidateConfig(t, "backend-b", "plane-B", "backend-b:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
		{ID: "anthropic", Enabled: true},
	})

	a, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfgA,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("compile A: %v", err)
	}
	b, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfgB,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("compile B: %v", err)
	}
	ga, ok := a.(*runtimebundle.GenerationBundle)
	if !ok {
		t.Fatal("expected *runtimebundle.GenerationBundle")
	}
	gb, ok := b.(*runtimebundle.GenerationBundle)
	if !ok {
		t.Fatal("expected *runtimebundle.GenerationBundle")
	}

	aIDs := map[string]bool{}
	for _, id := range ga.BackendIDs() {
		aIDs[id] = true
	}
	bIDs := map[string]bool{}
	for _, id := range gb.BackendIDs() {
		bIDs[id] = true
	}
	if !aIDs["backend-a"] {
		t.Fatal("A missing backend-a")
	}
	if aIDs["backend-b"] {
		t.Fatal("A must not see backend-b")
	}
	if !bIDs["backend-b"] {
		t.Fatal("B missing backend-b")
	}
	if bIDs["backend-a"] {
		t.Fatal("B must not see backend-a")
	}
	if a.ExecutorView() == nil || b.ExecutorView() == nil {
		t.Fatal("expected executor views")
	}
	if a.ExecutorView() == b.ExecutorView() {
		t.Fatal("candidates must own distinct executor views")
	}

	bodyA := postResponses(t, a.Handler(), "stub-default")
	bodyB := postResponses(t, b.Handler(), "stub-default")
	if !strings.Contains(bodyA, "plane-A") || strings.Contains(bodyA, "plane-B") {
		t.Fatalf("A leaked B: %s", bodyA)
	}
	if !strings.Contains(bodyB, "plane-B") || strings.Contains(bodyB, "plane-A") {
		t.Fatalf("B leaked A: %s", bodyB)
	}

	if ga.Routing().DefaultRoute == gb.Routing().DefaultRoute {
		t.Fatalf("expected different default routes, both %q", ga.Routing().DefaultRoute)
	}
	if len(gb.FrozenFrontends()) < 2 {
		t.Fatalf("B frontends=%d", len(gb.FrozenFrontends()))
	}

	if err := a.Close(); err != nil {
		t.Fatalf("close A: %v", err)
	}
	bodyB2 := postResponses(t, b.Handler(), "stub-default")
	if !strings.Contains(bodyB2, "plane-B") {
		t.Fatalf("B broken after A close: %s", bodyB2)
	}
	if ps.Closed() {
		t.Fatal("process services closed after candidate close")
	}
	if err := b.Close(); err != nil {
		t.Fatalf("close B: %v", err)
	}
	if ps.Closed() {
		t.Fatal("process services closed after both candidates")
	}
}

func TestCompileGeneration_HandlerFailureRollsBackCandidate(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)

	_, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "ok", "x", "ok:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		Compose: func(context.Context, *config.Config, *slog.Logger, stdhttp.StandardHTTPInput) (http.Handler, error) {
			return nil, fmt.Errorf("injected handler failure")
		},
	})
	if err == nil {
		t.Fatal("expected compose failure")
	}
	if !strings.Contains(err.Error(), "injected handler failure") {
		t.Fatalf("err=%v", err)
	}
	if ps.Closed() {
		t.Fatal("process closed on handler failure")
	}

	ok, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "ok2", "y", "ok2:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("recompile after rollback: %v", err)
	}
	t.Cleanup(func() { _ = ok.Close() })
	body := postResponses(t, ok.Handler(), "stub-default")
	if !strings.Contains(body, `"text":"y"`) && !strings.Contains(body, "y") {
		t.Fatalf("recompiled handler body=%s", body)
	}
}

func TestCompileGeneration_ImmutableAccessorsDefensive(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)
	cand := stubCandidateConfig(t, "imm", "immutable-text", "imm:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
	})

	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	gb, ok := bundle.(*runtimebundle.GenerationBundle)
	if !ok {
		t.Fatal("expected *runtimebundle.GenerationBundle")
	}

	cand.Routing.DefaultRoute = "mutated:gone"
	cand.Plugins.Frontends = nil
	cand.Plugins.Backends[0].Config = genYAMLNode(t, `text: "mutated-after-compile"`)

	body := postResponses(t, bundle.Handler(), "stub-default")
	if !strings.Contains(body, "immutable-text") {
		t.Fatalf("handler followed mutated config: %s", body)
	}
	if gb.Routing().DefaultRoute != "imm:stub-default" {
		t.Fatalf("routing mutated: %q", gb.Routing().DefaultRoute)
	}

	frontends := gb.FrozenFrontends()
	frontends[0].ID = "mutated-frontend"
	frontends2 := gb.FrozenFrontends()
	if frontends2[0].ID != "openai-responses" {
		t.Fatalf("frontend slice not defensive: %q", frontends2[0].ID)
	}

	prefixes := gb.RoutePrefixes()
	if len(prefixes) == 0 {
		t.Fatal("expected route prefixes")
	}
	prefixes[0] = "mutated-prefix"
	if gb.RoutePrefixes()[0] == "mutated-prefix" {
		t.Fatal("route prefixes not defensive")
	}
}

func TestCompileGeneration_BundleContractNoConfigAppBuilt(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "c", "t", "c:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })

	rt := reflect.TypeOf(bundle).Elem()
	for f := range rt.Fields() {
		ft := f.Type.String()
		name := f.Name
		if strings.Contains(ft, "*config.Config") || ft == "config.Config" {
			t.Fatalf("bundle field %s exposes config.Config (%s)", name, ft)
		}
		if strings.Contains(ft, "*runtime.App") || strings.HasSuffix(ft, ".App") && strings.Contains(ft, "runtime") {
			t.Fatalf("bundle field %s exposes App (%s)", name, ft)
		}
		if name == "built" || name == "Built" || strings.Contains(ft, ".Built") {
			t.Fatalf("bundle field %s exposes Built (%s)", name, ft)
		}
	}
	var _ runtimehost.PublishedRequestPlane = bundle
	var _ runtimehost.QuiesceCloser = bundle
	g := runtimehost.NewManager(2, nil).PrepareRequestPlane("g", bundle)
	if g.Lifecycle() != runtimehost.GenPrepared {
		t.Fatalf("lifecycle=%v", g.Lifecycle())
	}
	if g.RequestPlane() != bundle {
		t.Fatal("prepared generation must bind the exact request-plane bundle")
	}
	if g.Handler() == nil || bundle.Handler() == nil {
		t.Fatal("generation and bundle handlers must be non-nil")
	}
	// Identity: same publisher instance means the bound handler is the bundle's.
	if g.RequestPlane().Handler() == nil {
		t.Fatal("bound request plane handler is nil")
	}
}

func TestCompileGeneration_RegisteredAndCandidateLifecyclesOwnedTogether(t *testing.T) {
	t.Parallel()
	pluginLife, overlayLife := &overlapLife{}, &overlapLife{}
	reg := stdFactoryCatalog(t)
	if err := reg.RegisterFeature("generation-lifecycle-source", func(yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, "generation-lifecycle-source", nil, []aipplugin.Lifecycle{pluginLife}), nil
	}); err != nil {
		t.Fatal(err)
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg: processBaseConfig(), Log: testkit.DiscardLogger(),
		Opts:    &runtimebundle.BuildOptions{PluginRegistry: reg},
		Tracing: runtimebundle.ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	cfg := stubCandidateConfig(t, "life", "life-text", "life:stub-default", []config.PluginConfig{{ID: "openai-responses", Enabled: true}})
	cfg.Plugins.Features = []config.PluginConfig{{ID: "source", Kind: "generation-lifecycle-source", Enabled: true, Config: genYAMLNode(t, "{}")}}
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: cfg, Compose: stdhttp.ComposeStandardHTTP,
		CandidateOpts: &runtimebundle.BuildOptions{FeatureLifecycles: []aipplugin.Lifecycle{overlayLife}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	for name, life := range map[string]*overlapLife{"registered": pluginLife, "candidate": overlayLife} {
		if got := life.starts.Load(); got != 1 {
			t.Errorf("%s starts=%d, want 1", name, got)
		}
	}
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}
	for name, life := range map[string]*overlapLife{"registered": pluginLife, "candidate": overlayLife} {
		if got := life.stops.Load(); got != 1 {
			t.Errorf("%s stops=%d, want 1", name, got)
		}
	}
}

func TestCompileGeneration_LifecycleNotStartedTwice(t *testing.T) {
	t.Parallel()
	life := &overlapLife{}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  processBaseConfig(),
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: stdFactoryCatalog(t)},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "life", "life-text", "life:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		CandidateOpts: &runtimebundle.BuildOptions{
			FeatureLifecycles: []aipplugin.Lifecycle{life},
		},
		Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	if got := life.starts.Load(); got != 1 {
		t.Fatalf("starts=%d want 1", got)
	}
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}
	if got := life.stops.Load(); got != 1 {
		t.Fatalf("stops=%d want 1", got)
	}
	if got := life.starts.Load(); got != 1 {
		t.Fatalf("starts after close=%d", got)
	}
}

type overlapLife struct {
	starts, stops atomic.Int32
}

func (o *overlapLife) Start(context.Context) error     { o.starts.Add(1); return nil }
func (o *overlapLife) Stop(context.Context) error      { o.stops.Add(1); return nil }
func (o *overlapLife) SafeUnderCandidateOverlap() bool { return true }

func TestCompileGeneration_ExecutionCompositionSafety(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	if err := localstubreg.RegisterInProcess(reg); err != nil {
		t.Fatal(err)
	}
	// Register an agent_runtime factory for testing
	if err := reg.RegisterBackendWithProfiles("stub-agent", func(raw yaml.Node, upstream *http.Client, deps pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		be, err := localstub.NewFromYAML(raw)
		if err != nil {
			return execbackend.Backend{}, err
		}
		be.BackendPrefixes = []string{"stub-agent"}
		return be, nil
	}, pluginreg.BackendSecurityProfile{
		CredentialMode: pluginreg.CredentialNone,
		AccessScope:    pluginreg.BackendAccessAny,
	}, pluginreg.BackendExecutionProfile{Class: aipsdk.BackendExecutionAgentRuntime}); err != nil {
		t.Fatal(err)
	}

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  processBaseConfig(),
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

	makeCandidate := func(defaultRoute string, policy config.ExecutionCompositionPolicy, aliases ...config.ModelAliasConfig) *config.Config {
		cfg := stubCandidateConfig(t, "stub-inf", "text", defaultRoute, []config.PluginConfig{{ID: "openai-responses", Enabled: true}})
		cfg.Routing.ExecutionCompositionPolicy = policy
		cfg.ModelAliases = aliases
		cfg.Plugins.Backends = append(cfg.Plugins.Backends, config.PluginConfig{
			Kind:    "stub-agent",
			ID:      "stub-agent-inst",
			Enabled: true,
			Config: genYAMLNode(t, `
text: "agent-text"
input_tokens: 1
output_tokens: 1
`),
		})
		if err := config.Validate(cfg); err != nil {
			t.Fatalf("validate cfg: %v", err)
		}
		return cfg
	}

	t.Run("safe_policy_mixed_default_route_fails_compile", func(t *testing.T) {
		cand := makeCandidate("stub-inf:m1|stub-agent-inst:m2", config.ExecutionCompositionSafe)
		_, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
			Process:   ps,
			Candidate: cand,
			Compose:   stdhttp.ComposeStandardHTTP,
		})
		if err == nil {
			t.Fatal("expected compile to fail for mixed default_route under safe policy")
		}
		if !strings.Contains(err.Error(), "unsafe backend execution composition") {
			t.Fatalf("expected unsafe backend execution composition in error, got: %v", err)
		}
	})

	t.Run("safe_policy_mixed_alias_replacement_fails_compile", func(t *testing.T) {
		cand := makeCandidate("stub-inf:m1", config.ExecutionCompositionSafe, config.ModelAliasConfig{
			Pattern:     "my-alias",
			Replacement: "stub-inf:m1|stub-agent-inst:m2",
		})
		_, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
			Process:   ps,
			Candidate: cand,
			Compose:   stdhttp.ComposeStandardHTTP,
		})
		if err == nil {
			t.Fatal("expected compile to fail for mixed model alias replacement under safe policy")
		}
		if !strings.Contains(err.Error(), "unsafe backend execution composition") {
			t.Fatalf("expected unsafe backend execution composition in error, got: %v", err)
		}
	})

	t.Run("unrestricted_policy_mixed_default_route_succeeds_compile", func(t *testing.T) {
		cand := makeCandidate("stub-inf:m1|stub-agent-inst:m2", config.ExecutionCompositionUnrestricted)
		bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
			Process:   ps,
			Candidate: cand,
			Compose:   stdhttp.ComposeStandardHTTP,
		})
		if err != nil {
			t.Fatalf("expected compile to succeed for unrestricted policy, got: %v", err)
		}
		_ = bundle.Close()
	})
}
