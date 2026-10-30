package runtimebundle_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/configreload"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/localstub"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/internal/testkit/localstubreg"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

// Task 4.2: discovered factory activation, overlap, no-rescan/no-install, shared-process reject.

func discoveredFactoryCatalog(t *testing.T) *pluginreg.Registry {
	t.Helper()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	if err := localstubreg.RegisterInProcess(reg); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterDiscoveredBackend("discovered-host-stub", func(n yaml.Node, _ *http.Client, _ pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		// Deterministic hosted/local stub shape via already-discovered kind.
		be, err := localstub.NewFromYAML(n)
		if err != nil {
			return execbackend.Backend{}, err
		}
		be.BackendPrefixes = []string{"disc"}
		return be, nil
	}, pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialNone}, pluginreg.BackendReloadPolicy{
		AllowsCandidateOverlap: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterDiscoveredBackend("shared-process-exclusive", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			BackendPrefixes: []string{"shared-process-exclusive"},
			ModelInventory: modelinventory.StaticProvider{
				Source: modelinventory.SourceStaticBuiltin,
				Models: []modelinventory.Model{{
					CanonicalID: "shared-process-exclusive/m",
					NativeID:    "m",
					DisplayName: "Exclusive",
				}},
			},
		}, nil
	}, pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialNone}, pluginreg.BackendReloadPolicy{
		AllowsCandidateOverlap: false,
	}); err != nil {
		t.Fatal(err)
	}
	// Freeze happens in NewProcessServices; leave unfrozen for explicit Freeze tests.
	return reg
}

func TestReloadDiscovered_ActivateAlreadyDiscoveredKind(t *testing.T) {
	t.Parallel()
	reg := discoveredFactoryCatalog(t)
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

	// Startup had no discovered-host-stub row; candidate activates the already discovered kind.
	cand := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3, DefaultRoute: "disc:stub-default"},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096,
		},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
			Backends: []config.PluginConfig{{
				Kind: "discovered-host-stub", ID: "disc", Enabled: true,
				Config: genYAMLNode(t, `text: "from-discovered"`+"\ninput_tokens: 1\noutput_tokens: 1\n"),
			}},
		},
	}
	if err := config.Validate(cand); err != nil {
		t.Fatal(err)
	}
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: cand, Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("activate discovered: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	gb, ok := bundle.(*runtimebundle.GenerationBundle)
	if !ok {
		t.Fatal("expected *runtimebundle.GenerationBundle")
	}
	if !containsID(gb.BackendIDs(), "disc") {
		t.Fatalf("want disc, got %v", gb.BackendIDs())
	}
}

func TestReloadOverlap_OldAndNewInstanceHandles(t *testing.T) {
	t.Parallel()
	reg := discoveredFactoryCatalog(t)
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

	cfgA := &config.Config{
		Routing:     config.RoutingConfig{MaxAttempts: 3, DefaultRoute: "inst:stub-default"},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096,
		},
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
			Backends: []config.PluginConfig{{
				Kind: "local-stub", ID: "inst", Enabled: true,
				Config: genYAMLNode(t, `text: "overlap-A"`+"\ninput_tokens: 1\noutput_tokens: 1\n"),
			}},
		},
	}
	cfgB := &config.Config{
		Routing:     config.RoutingConfig{MaxAttempts: 3, DefaultRoute: "inst:stub-default"},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096,
		},
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
			Backends: []config.PluginConfig{{
				Kind: "local-stub", ID: "inst", Enabled: true,
				Config: genYAMLNode(t, `text: "overlap-B"`+"\ninput_tokens: 1\noutput_tokens: 1\n"),
			}},
		},
	}
	if err := config.Validate(cfgA); err != nil {
		t.Fatal(err)
	}
	if err := config.Validate(cfgB); err != nil {
		t.Fatal(err)
	}

	a, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: cfgA, Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: cfgB, Compose: stdhttp.ComposeStandardHTTP,
		LiveFactoryKinds: map[string]int{"local-stub": 1},
	})
	if err != nil {
		t.Fatalf("overlap-capable kind must compile beside live instance: %v", err)
	}

	m := runtimehost.NewManager(4, nil)
	if err := m.Publish(m.PrepareRequestPlane("a", a)); err != nil {
		t.Fatal(err)
	}
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	pin, ok := lease.TransferPin(runtimehost.PinSSE)
	if !ok {
		t.Fatal("pin")
	}
	lease.Release()
	if err := m.Publish(m.PrepareRequestPlane("b", b)); err != nil {
		t.Fatal(err)
	}

	bodyA := postResponses(t, pin.Generation().Handler(), "stub-default")
	bodyB := postResponses(t, m.Active().Handler(), "stub-default")
	if !strings.Contains(bodyA, "overlap-A") || strings.Contains(bodyA, "overlap-B") {
		t.Fatalf("old handle body=%s", bodyA)
	}
	if !strings.Contains(bodyB, "overlap-B") || strings.Contains(bodyB, "overlap-A") {
		t.Fatalf("new handle body=%s", bodyB)
	}
	pin.Release()
	_ = a.Close()
	_ = b.Close()
}

func TestReloadDiscovered_SharedProcessNoOverlap_RestartRequired(t *testing.T) {
	t.Parallel()
	reg := discoveredFactoryCatalog(t)
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

	cand := &config.Config{
		Routing:     config.RoutingConfig{MaxAttempts: 3, DefaultRoute: "excl:m"},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096,
		},
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
			Backends: []config.PluginConfig{{
				Kind: "shared-process-exclusive", ID: "excl", Enabled: true,
				Config: genYAMLNode(t, "{}\n"),
			}},
		},
	}
	if err := config.Validate(cand); err != nil {
		t.Fatal(err)
	}

	// First activation while no live instance is allowed.
	first, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: cand, Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("first exclusive compile: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })

	// Candidate that would overlap a live shared-process exclusive kind must fail before publication.
	_, err = runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:          ps,
		Candidate:        cand,
		Compose:          stdhttp.ComposeStandardHTTP,
		LiveFactoryKinds: map[string]int{"shared-process-exclusive": 1},
	})
	if err == nil {
		t.Fatal("expected restart-required for shared-process exclusive overlap")
	}
	var rr *configreload.RestartRequiredError
	if !errors.As(err, &rr) {
		t.Fatalf("want RestartRequiredError, got %T %v", err, err)
	}
	if rr.TotalBlocked < 1 {
		t.Fatalf("blocked=%d", rr.TotalBlocked)
	}
	found := slices.Contains(rr.RestartRequiredFields, "plugins.backends")
	if !found {
		t.Fatalf("want plugins.backends in fields, got %v", rr.RestartRequiredFields)
	}
	if !errors.Is(err, runtimebundle.ErrUnsafeLifecycleOverlap) {
		t.Fatalf("want ErrUnsafeLifecycleOverlap wrap, got %v", err)
	}
	if ps.Closed() {
		t.Fatal("process must stay unchanged")
	}
}

func TestReloadDiscovered_ProcessCatalogFrozenAtStartup(t *testing.T) {
	t.Parallel()
	reg := discoveredFactoryCatalog(t)
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

	if !reg.DiscoveryFrozen() {
		t.Fatal("factory discovery/trust catalog must be frozen after process construction")
	}
	if ps.FactoryCatalog != reg {
		t.Fatal("candidates must reuse process FactoryCatalog identity")
	}
}

func containsID(ids []string, want string) bool {
	return slices.Contains(ids, want)
}
