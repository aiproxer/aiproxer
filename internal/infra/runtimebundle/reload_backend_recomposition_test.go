package runtimebundle_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

// Task 4.1: backend add / replace / remove / generic compatible / stub / rollback.

func TestReloadBackend_AddAbsentAtStartup(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t) // startup config has no enabled backends

	cand := stubCandidateConfig(t, "added-stub", "new-backend-text", "added-stub:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
	})
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration add: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	gb, ok := bundle.(*runtimebundle.GenerationBundle)
	if !ok {
		t.Fatal("expected *runtimebundle.GenerationBundle")
	}

	ids := map[string]bool{}
	for _, id := range gb.BackendIDs() {
		ids[id] = true
	}
	if !ids["added-stub"] {
		t.Fatalf("expected added-stub in backends, got %v", gb.BackendIDs())
	}
	body := postResponses(t, bundle.Handler(), "stub-default")
	if !strings.Contains(body, "new-backend-text") {
		t.Fatalf("new requests must hit added backend: %s", body)
	}
	if ps.Closed() {
		t.Fatal("process must remain open")
	}
}

func TestReloadBackend_ReplaceSameIDChangedConfig(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)

	cfgOld := stubCandidateConfig(t, "same-id", "old-text", "same-id:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
	})
	cfgNew := stubCandidateConfig(t, "same-id", "new-text", "same-id:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
	})

	oldBundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: cfgOld, Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("compile old: %v", err)
	}
	newBundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: cfgNew, Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("compile new: %v", err)
	}

	if oldBundle.ExecutorView() == newBundle.ExecutorView() {
		t.Fatal("same-ID replace must own distinct executor/instance views")
	}

	m := runtimehost.NewManager(4, nil)
	oldGen := m.PrepareRequestPlane("old", oldBundle)
	if err := m.Publish(oldGen); err != nil {
		t.Fatal(err)
	}
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire old")
	}
	pin, ok := lease.TransferPin(runtimehost.PinSSE)
	if !ok {
		t.Fatal("pin old stream")
	}
	lease.Release()

	newGen := m.PrepareRequestPlane("new", newBundle)
	if err := m.Publish(newGen); err != nil {
		t.Fatal(err)
	}

	// New requests use new instance.
	activeLease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire new")
	}
	bodyNew := postResponses(t, activeLease.Handler(), "stub-default")
	activeLease.Release()
	if !strings.Contains(bodyNew, "new-text") || strings.Contains(bodyNew, "old-text") {
		t.Fatalf("new generation body=%s", bodyNew)
	}

	// Old pinned stream retains old instance.
	bodyOld := postResponses(t, pin.Generation().Handler(), "stub-default")
	if !strings.Contains(bodyOld, "old-text") || strings.Contains(bodyOld, "new-text") {
		t.Fatalf("pinned old generation body=%s", bodyOld)
	}
	pin.Release()

	_ = oldBundle.Close()
	_ = newBundle.Close()
}

func TestReloadBackend_RemoveAndDisable(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)

	cfgBoth := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3, DefaultRoute: "keep:stub-default"},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096,
		},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
			Backends: []config.PluginConfig{
				{
					Kind: "local-stub", ID: "keep", Enabled: true,
					Config: genYAMLNode(t, `text: "keep-text"`+"\ninput_tokens: 1\noutput_tokens: 1\n"),
				},
				{
					Kind: "local-stub", ID: "drop", Enabled: true,
					Config: genYAMLNode(t, `text: "drop-text"`+"\ninput_tokens: 1\noutput_tokens: 1\n"),
				},
			},
		},
	}
	if err := config.Validate(cfgBoth); err != nil {
		t.Fatal(err)
	}
	oldBundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: cfgBoth, Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("compile both: %v", err)
	}

	cfgRemoved := stubCandidateConfig(t, "keep", "keep-text", "keep:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
	})
	// Explicit disable of drop row (removed from enabled set).
	cfgRemoved.Plugins.Backends = append(cfgRemoved.Plugins.Backends, config.PluginConfig{
		Kind: "local-stub", ID: "drop", Enabled: false,
		Config: genYAMLNode(t, `text: "drop-text"`+"\ninput_tokens: 1\noutput_tokens: 1\n"),
	})
	if err := config.Validate(cfgRemoved); err != nil {
		t.Fatal(err)
	}

	newBundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: cfgRemoved, Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("compile removed: %v", err)
	}

	m := runtimehost.NewManager(4, nil)
	oldGen := m.PrepareRequestPlane("old", oldBundle)
	if err := m.Publish(oldGen); err != nil {
		t.Fatal(err)
	}
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	pin, ok := lease.TransferPin(runtimehost.PinAsync)
	if !ok {
		t.Fatal("pin")
	}
	lease.Release()

	if err := m.Publish(m.PrepareRequestPlane("new", newBundle)); err != nil {
		t.Fatal(err)
	}

	newGB, ok := newBundle.(*runtimebundle.GenerationBundle)
	if !ok {
		t.Fatal("expected *runtimebundle.GenerationBundle")
	}
	newIDs := map[string]bool{}
	for _, id := range newGB.BackendIDs() {
		newIDs[id] = true
	}
	if newIDs["drop"] {
		t.Fatal("new generation must not expose disabled/removed backend")
	}
	if !newIDs["keep"] {
		t.Fatal("keep backend must remain")
	}

	// Retired generation still has drop for bound work.
	oldGB, ok := oldBundle.(*runtimebundle.GenerationBundle)
	if !ok {
		t.Fatal("expected *runtimebundle.GenerationBundle")
	}
	oldIDs := map[string]bool{}
	for _, id := range oldGB.BackendIDs() {
		oldIDs[id] = true
	}
	if !oldIDs["drop"] {
		t.Fatal("old generation must retain drop instance until drain")
	}
	if pin.Generation().RequestPlane() == nil {
		t.Fatal("pinned old plane missing")
	}
	pin.Release()
	_ = oldBundle.Close()
	_ = newBundle.Close()
}

func TestReloadBackend_GenericCompatibleKindsAdd(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)

	kinds := []struct {
		kind   string
		id     string
		prefix string
	}{
		{standardplugins.CustomOpenAILegacyCompatibleID, "gen-legacy", "reloadlegacy"},
		{standardplugins.CustomOpenAIResponsesCompatibleID, "gen-responses", "reloadresponses"},
		{standardplugins.CustomAnthropicCompatibleID, "gen-anthropic", "reloadanthropic"},
	}

	backends := make([]config.PluginConfig, 0, len(kinds)+1)
	backends = append(backends, config.PluginConfig{
		Kind: "local-stub", ID: "anchor", Enabled: true,
		Config: genYAMLNode(t, `text: "anchor"`+"\ninput_tokens: 1\noutput_tokens: 1\n"),
	})
	for _, k := range kinds {
		backends = append(backends, config.PluginConfig{
			Kind: k.kind, ID: k.id, Enabled: true,
			Config: genYAMLNode(t, fmt.Sprintf(`
backend_prefix: %s
base_url: http://127.0.0.1:9/v1
models:
  source: inline
  items:
    - canonical_id: %s/static-model
      native_id: static-model
`, k.prefix, k.prefix)),
		})
	}
	cand := &config.Config{
		Routing:     config.RoutingConfig{MaxAttempts: 3, DefaultRoute: "anchor:stub-default"},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096,
		},
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
			Backends:  backends,
		},
	}
	if err := config.Validate(cand); err != nil {
		t.Fatalf("validate: %v", err)
	}

	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: cand, Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("compile generic kinds: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	gb, ok := bundle.(*runtimebundle.GenerationBundle)
	if !ok {
		t.Fatal("expected *runtimebundle.GenerationBundle")
	}

	got := map[string]bool{}
	for _, id := range gb.BackendIDs() {
		got[id] = true
	}
	for _, k := range kinds {
		if !got[k.id] {
			t.Fatalf("missing generic kind instance %s (%s); have %v", k.id, k.kind, gb.BackendIDs())
		}
	}
}

func TestReloadBackend_CandidateFactoryFailureRollback(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var closes atomic.Int32
	if err := reg.RegisterBackend("probe-closeable", func(n yaml.Node, _ *http.Client, _ pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		var y struct {
			Fail bool `yaml:"fail"`
		}
		_ = config.DecodeYAMLNode(n, &y)
		if y.Fail {
			return execbackend.Backend{}, errors.New("injected factory failure")
		}
		return execbackend.Backend{
			BackendPrefixes: []string{"okprobe"},
			ModelInventory: modelinventory.StaticProvider{
				Source: modelinventory.SourceStaticBuiltin,
				Models: []modelinventory.Model{{CanonicalID: "okprobe/m", NativeID: "m", DisplayName: "m"}},
			},
			Close: func() error {
				closes.Add(1)
				return nil
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}

	cfgBase := processBaseConfig()
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfgBase,
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

	okCand := &config.Config{
		Routing:     config.RoutingConfig{MaxAttempts: 3, DefaultRoute: "ok:m"},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096,
		},
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
			Backends: []config.PluginConfig{{
				Kind: "probe-closeable", ID: "ok", Enabled: true, Config: genYAMLNode(t, "fail: false\n"),
			}},
		},
	}
	if err := config.Validate(okCand); err != nil {
		t.Fatal(err)
	}
	okBundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: okCand, Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("first compile: %v", err)
	}
	t.Cleanup(func() { _ = okBundle.Close() })

	badCand := &config.Config{
		Routing:     config.RoutingConfig{MaxAttempts: 3, DefaultRoute: "bad:m"},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096,
		},
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
			Backends: []config.PluginConfig{
				{Kind: "probe-closeable", ID: "ok2", Enabled: true, Config: genYAMLNode(t, "fail: false\n")},
				{Kind: "probe-closeable", ID: "bad", Enabled: true, Config: genYAMLNode(t, "fail: true\n")},
			},
		},
	}
	if err := config.Validate(badCand); err != nil {
		t.Fatal(err)
	}

	closesBefore := closes.Load()
	_, err = runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: badCand, Compose: stdhttp.ComposeStandardHTTP,
	})
	if err == nil {
		t.Fatal("expected factory failure")
	}
	if !strings.Contains(err.Error(), "injected factory failure") {
		t.Fatalf("err=%v", err)
	}
	// The successfully constructed "ok" instance in the failed candidate must roll back exactly once.
	if got := closes.Load() - closesBefore; got != 1 {
		t.Fatalf("candidate rollback closes=%d want 1", got)
	}
	if ps.Closed() {
		t.Fatal("process must not mutate/close on candidate failure")
	}
	// Active/old bundle still works.
	okGB, ok := okBundle.(*runtimebundle.GenerationBundle)
	if !ok {
		t.Fatal("expected *runtimebundle.GenerationBundle")
	}
	if len(okGB.BackendIDs()) == 0 {
		t.Fatal("active generation backends lost")
	}
}
