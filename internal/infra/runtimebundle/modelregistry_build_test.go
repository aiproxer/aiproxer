package runtimebundle_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

func TestBuild_requiresModelInventoryForEnabledBackends(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("test-no-inventory", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{"test-no-inventory"},
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}

	_, _, err := processAndCandidateErr(t, modelRegistryTestConfig("test-no-inventory"), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if !errors.Is(err, modelregistry.ErrMissingProvider) {
		t.Fatalf("Build() error = %v, want ErrMissingProvider", err)
	}
}

func TestBuild_exposesModelRegistryForFastLookup(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("test-inventory", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{"test-inventory"},
			ModelInventory: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "openai/gpt-4o", NativeID: "gpt-4o"},
			}},
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}

	_, b := mustProcessAndCandidate(t, modelRegistryTestConfig("test-inventory"), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if b.ModelRegistry() == nil {
		t.Fatal("ModelRegistry is nil")
	}
	got, ok := b.ModelRegistry().Lookup("openai/gpt-4o")
	if !ok {
		t.Fatal("Lookup(openai/gpt-4o) ok = false")
	}
	if len(got) != 1 || got[0].BackendID != "test-backend" || got[0].NativeID != "gpt-4o" {
		t.Fatalf("Lookup(openai/gpt-4o) = %+v", got)
	}
}

func TestBuild_modelRegistryFallsBackToCacheWhenEnumerationFails(t *testing.T) {
	t.Parallel()

	cachePath := filepath.Join(t.TempDir(), "backend-models.json")
	writeModelRegistryCache(t, cachePath, modelregistry.Snapshot{
		Generation:  "cached",
		RefreshedAt: time.Unix(100, 0).UTC(),
		Models: []modelregistry.BackendModel{{
			CanonicalID: "openai/gpt-cached",
			NativeID:    "gpt-cached",
			BackendID:   "test-backend",
			Kind:        "test-inventory",
			Source:      modelinventory.SourceRemote,
			LoadedAt:    time.Unix(100, 0).UTC(),
		}},
	})
	provider := &runtimeBundleCountingInventory{err: errors.New("upstream unavailable")}
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("test-inventory", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{"test-inventory"},
			ModelInventory:  provider,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}

	cfg := modelRegistryTestConfig("test-inventory")
	cfg.ModelInventory.CachePath = cachePath
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	t.Cleanup(closeModelRegistryBuilt(t, b))

	if provider.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1 (enumeration attempted before cache fallback)", provider.Calls())
	}
	got, ok := b.ModelRegistry().Lookup("openai/gpt-cached")
	if !ok || len(got) != 1 || got[0].BackendID != "test-backend" {
		t.Fatalf("cached lookup = %+v, %v", got, ok)
	}
	if runtimebundle.CandidateModelRegistryRuntime(b) == nil {
		t.Fatal("ModelRegistryRuntime is nil")
	}
}

func TestBuild_modelRegistryColdStartSavesCache(t *testing.T) {
	t.Parallel()

	cachePath := filepath.Join(t.TempDir(), "backend-models.json")
	provider := &runtimeBundleCountingInventory{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-remote",
		NativeID:    "gpt-remote",
	}}}
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("test-inventory", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{"test-inventory"},
			ModelInventory:  provider,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}

	cfg := modelRegistryTestConfig("test-inventory")
	cfg.ModelInventory.CachePath = cachePath
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	t.Cleanup(closeModelRegistryBuilt(t, b))

	if provider.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.Calls())
	}
	saved := readModelRegistryCache(t, cachePath)
	if len(saved.Models) != 1 || saved.Models[0].CanonicalID != "openai/gpt-remote" {
		t.Fatalf("saved cache = %+v", saved)
	}
}

func TestBuild_modelRegistryColdStartInvalidInventoryOmitsBackend(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("test-inventory-good", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{"test-inventory-good"},
			ModelInventory: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "vendor/good", NativeID: "good"},
			}},
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterBackend("test-inventory-bad", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{"test-inventory-bad"},
			ModelInventory: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "vendor/first-ok", NativeID: "first-ok"},
				{CanonicalID: "", NativeID: "secret-native"},
			}},
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}

	cfg := modelRegistryTestConfig("test-inventory-good")
	cfg.ModelInventory.CachePath = filepath.Join(t.TempDir(), "missing.json")
	cfg.Plugins.Backends = append(cfg.Plugins.Backends, config.PluginConfig{
		Kind:    "test-inventory-bad",
		ID:      "test-backend-bad",
		Enabled: true,
	})
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	t.Cleanup(closeModelRegistryBuilt(t, b))
	if got := b.ModelRegistry().All(); len(got) != 1 || got[0].BackendID != "test-backend" {
		t.Fatalf("All() = %+v, want only good backend", got)
	}
	diag := runtimebundle.CandidateModelRegistryRuntime(b).Diagnostics()
	byID := map[string]modelregistry.BackendDiscovery{}
	for _, d := range diag.BackendDiscoveries {
		byID[d.BackendID] = d
	}
	if byID["test-backend"].Status != modelinventory.DiscoveryStatusOK {
		t.Fatalf("good discovery = %+v", byID["test-backend"])
	}
	if byID["test-backend-bad"].Status != modelinventory.DiscoveryStatusUnavailable {
		t.Fatalf("bad status = %q", byID["test-backend-bad"].Status)
	}
	if byID["test-backend-bad"].ErrorCode != modelinventory.ErrorCodeInvalidInventory {
		t.Fatalf("bad ErrorCode = %q", byID["test-backend-bad"].ErrorCode)
	}
	if byID["test-backend-bad"].ModelCount != 0 {
		t.Fatalf("bad ModelCount = %d", byID["test-backend-bad"].ModelCount)
	}
	if strings.Contains(byID["test-backend-bad"].ErrorCode, "secret") {
		t.Fatalf("ErrorCode leaked raw text: %q", byID["test-backend-bad"].ErrorCode)
	}
}

func TestBuild_modelRegistryColdStartAllUnavailablePublishesEmptyRegistry(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("test-inventory", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{"test-inventory"},
			ModelInventory:  modelinventory.ErrorProvider{Err: errors.New("remote unavailable")},
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}

	cfg := modelRegistryTestConfig("test-inventory")
	cfg.ModelInventory.CachePath = filepath.Join(t.TempDir(), "missing.json")
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	t.Cleanup(closeModelRegistryBuilt(t, b))
	if got := b.ModelRegistry().All(); len(got) != 0 {
		t.Fatalf("All() len = %d, want 0", len(got))
	}
	diag := runtimebundle.CandidateModelRegistryRuntime(b).Diagnostics()
	if len(diag.BackendDiscoveries) != 1 {
		t.Fatalf("BackendDiscoveries len = %d", len(diag.BackendDiscoveries))
	}
	if diag.BackendDiscoveries[0].Status != modelinventory.DiscoveryStatusUnavailable {
		t.Fatalf("Status = %q, want unavailable", diag.BackendDiscoveries[0].Status)
	}
	if diag.BackendDiscoveries[0].ErrorCode != modelinventory.ErrorCodeUnavailable {
		t.Fatalf("ErrorCode = %q", diag.BackendDiscoveries[0].ErrorCode)
	}
}

func TestBuild_modelRegistryStaticInventoryDoesNotStartRefreshCloser(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("test-inventory", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{"test-inventory"},
			ModelInventory:  testModelInventory(),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	_, b := mustProcessAndCandidate(t, modelRegistryTestConfig("test-inventory"), &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if b.Ledger().Len() != 3 {
		t.Fatalf("closers = %d, want 1 upstream-idle closer plus ledger-owned keep-warm lifecycle prepare and quiesce entries for disabled model catalog with static inventory", b.Ledger().Len())
	}
	closeRuntimeBuilt(t, b)
}

func TestBuild_modelRegistryErrorProviderWithCacheDoesNotStartRefreshCloser(t *testing.T) {
	t.Parallel()

	cachePath := filepath.Join(t.TempDir(), "backend-models.json")
	writeModelRegistryCache(t, cachePath, modelregistry.Snapshot{
		Generation:  "cached",
		RefreshedAt: time.Unix(100, 0).UTC(),
		Models: []modelregistry.BackendModel{{
			CanonicalID: "openai/gpt-cached",
			NativeID:    "gpt-cached",
			BackendID:   "test-backend",
			Kind:        "test-error-inventory",
			Source:      modelinventory.SourceRemote,
			LoadedAt:    time.Unix(100, 0).UTC(),
		}},
	})

	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("test-error-inventory", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{"test-error-inventory"},
			ModelInventory:  modelinventory.ErrorProvider{Err: errors.New("backend construction failed")},
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}

	cfg := modelRegistryTestConfig("test-error-inventory")
	cfg.ModelInventory.CachePath = cachePath
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if b.Ledger().Len() != 3 {
		t.Fatalf("closers = %d, want 1 upstream-idle closer plus ledger-owned keep-warm lifecycle prepare and quiesce entries for disabled model catalog with cached model registry", b.Ledger().Len())
	}
	defer closeRuntimeBuilt(t, b)
	if runtimebundle.CandidateModelRegistryRuntime(b) == nil {
		t.Fatal("ModelRegistryRuntime is nil")
	}
	if runtimebundle.CandidateModelRegistryRuntime(b).LastRefreshFailure() != modelregistry.RefreshFailureNone {
		t.Fatalf("LastRefreshFailure = %q, want none", runtimebundle.CandidateModelRegistryRuntime(b).LastRefreshFailure())
	}
	got, ok := b.ModelRegistry().Lookup("openai/gpt-cached")
	if !ok || len(got) != 1 || got[0].BackendID != "test-backend" {
		t.Fatalf("cached lookup = %+v, %v", got, ok)
	}
}

func TestBuild_modelRegistryFetchTimeoutAppliesPerBackend(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("test-inventory-a", delayedBackendFactory("test-inventory-a", "vendor/a", 25*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterBackend("test-inventory-b", delayedBackendFactory("test-inventory-b", "vendor/b", 25*time.Millisecond)); err != nil {
		t.Fatal(err)
	}

	cfg := modelRegistryTestConfig("test-inventory-a")
	cfg.ModelInventory.FetchTimeout = "250ms"
	cfg.Plugins.Backends = append(cfg.Plugins.Backends, config.PluginConfig{
		Kind:    "test-inventory-b",
		ID:      "test-backend-b",
		Enabled: true,
	})

	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	t.Cleanup(closeModelRegistryBuilt(t, b))
	if got := b.ModelRegistry().All(); len(got) != 2 {
		t.Fatalf("model registry count = %d, want 2", len(got))
	}
}

func modelRegistryTestConfig(kind string) *config.Config {
	return &config.Config{
		Routing: config.RoutingConfig{
			MaxAttempts:  3,
			DefaultRoute: "test-backend:test-model",
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind:    strings.TrimSpace(kind),
				ID:      "test-backend",
				Enabled: true,
			}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
}

func mustYAMLNode(t *testing.T, raw string) yaml.Node {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	return node
}

func writeModelRegistryCache(t *testing.T, path string, snap modelregistry.Snapshot) {
	t.Helper()
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readModelRegistryCache(t *testing.T, path string) modelregistry.Snapshot {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snap modelregistry.Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

func closeModelRegistryBuilt(t *testing.T, b *runtimebundle.CandidateHTTPCompile) func() {
	t.Helper()
	return func() {
		if b == nil {
			return
		}
		if err := b.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}
}

type runtimeBundleCountingInventory struct {
	mu     sync.Mutex
	calls  int
	err    error
	models []modelinventory.Model
}

func delayedBackendFactory(prefix, modelID string, delay time.Duration) pluginreg.BackendFactory {
	return func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{prefix},
			ModelInventory: delayedRuntimeBundleInventory{
				modelID: modelID,
				delay:   delay,
			},
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}
}

type delayedRuntimeBundleInventory struct {
	modelID string
	delay   time.Duration
}

func (p delayedRuntimeBundleInventory) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	select {
	case <-ctx.Done():
		return modelinventory.Snapshot{}, ctx.Err()
	case <-time.After(p.delay):
		return modelinventory.Snapshot{
			Source:   modelinventory.SourceRemote,
			LoadedAt: time.Unix(500, 0).UTC(),
			Models: []modelinventory.Model{{
				CanonicalID: p.modelID,
				NativeID:    strings.TrimPrefix(p.modelID, "vendor/"),
			}},
		}, nil
	}
}

func (p *runtimeBundleCountingInventory) LoadModels(context.Context) (modelinventory.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.err != nil {
		return modelinventory.Snapshot{}, p.err
	}
	return modelinventory.Snapshot{
		Source:   modelinventory.SourceRemote,
		LoadedAt: time.Unix(500, 0).UTC(),
		Models:   append([]modelinventory.Model(nil), p.models...),
	}, nil
}

func (p *runtimeBundleCountingInventory) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}
