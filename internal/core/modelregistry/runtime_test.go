package modelregistry_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

func TestRuntime_StartEnumeratesBeforeServingCache(t *testing.T) {
	t.Parallel()

	cache := &fakeModelRegistryCache{load: modelregistry.Snapshot{
		Generation:  "cached-generation",
		RefreshedAt: time.Unix(100, 0).UTC(),
		Models: []modelregistry.BackendModel{{
			CanonicalID: "openai/gpt-cached",
			NativeID:    "gpt-cached",
			BackendID:   "remote-backend",
			Kind:        "openai-responses",
			Source:      modelinventory.SourceRemote,
			LoadedAt:    time.Unix(100, 0).UTC(),
		}},
	}}
	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-live",
		NativeID:    "gpt-live",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "remote-backend",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
		Cache: cache,
		Now:   func() time.Time { return time.Unix(200, 0).UTC() },
	})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Discovery runs at startup even with a valid cache: the cache is a
	// fallback, not a substitute for enumeration.
	if provider.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.Calls())
	}
	if _, ok := rt.Lookup("openai/gpt-cached"); ok {
		t.Fatal("stale cached model must not shadow fresh enumeration")
	}
	got, ok := rt.Lookup("openai/gpt-live")
	if !ok || len(got) != 1 || got[0].BackendID != "remote-backend" {
		t.Fatalf("Lookup live = %+v, %v", got, ok)
	}
	if cache.saves != 1 {
		t.Fatalf("cache saves = %d, want 1 (fresh snapshot persisted)", cache.saves)
	}
	diag := rt.Diagnostics()
	if len(diag.BackendDiscoveries) != 1 {
		t.Fatalf("BackendDiscoveries len = %d", len(diag.BackendDiscoveries))
	}
	if diag.BackendDiscoveries[0].Status != modelinventory.DiscoveryStatusOK {
		t.Fatalf("startup Status = %q, want ok", diag.BackendDiscoveries[0].Status)
	}
}

func TestRuntime_StartFallsBackToCacheOnTransientFailure(t *testing.T) {
	t.Parallel()

	cache := &fakeModelRegistryCache{load: modelregistry.Snapshot{
		Generation:  "cached-generation",
		RefreshedAt: time.Unix(100, 0).UTC(),
		Models: []modelregistry.BackendModel{{
			CanonicalID: "openai/gpt-cached",
			NativeID:    "gpt-cached",
			BackendID:   "remote-backend",
			Kind:        "openai-responses",
			Source:      modelinventory.SourceRemote,
			LoadedAt:    time.Unix(100, 0).UTC(),
		}},
	}}
	provider := &countingInventoryProvider{err: errors.New("upstream unavailable")}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "remote-backend",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
		Cache: cache,
		Now:   func() time.Time { return time.Unix(200, 0).UTC() },
	})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1 (enumeration attempted first)", provider.Calls())
	}
	got, ok := rt.Lookup("openai/gpt-cached")
	if !ok || len(got) != 1 || got[0].BackendID != "remote-backend" {
		t.Fatalf("Lookup cached fallback = %+v, %v", got, ok)
	}
	diag := rt.Diagnostics()
	if len(diag.BackendDiscoveries) != 1 {
		t.Fatalf("BackendDiscoveries len = %d", len(diag.BackendDiscoveries))
	}
	if diag.BackendDiscoveries[0].Status != modelinventory.DiscoveryStatusCached {
		t.Fatalf("fallback Status = %q, want cached", diag.BackendDiscoveries[0].Status)
	}
}

func TestRuntime_RefreshDoesNotAcceptUntilPublish(t *testing.T) {
	t.Parallel()

	provider := &acceptingInventoryProvider{countingInventoryProvider: countingInventoryProvider{
		models: []modelinventory.Model{{
			CanonicalID: "vendor/v1",
			NativeID:    "v1",
		}},
	}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "acp-solo",
			Kind:            "cursor-cli-acp",
			BackendPrefixes: []string{"cursor-cli-acp"},
			Provider:        provider,
		}},
		Cache: &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	acceptsAfterStart := provider.Accepts()
	if acceptsAfterStart < 1 {
		t.Fatal("Start/publish must AcceptInventory")
	}
	if got := provider.Accepted(); len(got) != 1 || got[0].NativeID != "v1" {
		t.Fatalf("after Start Accepted = %+v", got)
	}
	if len(rt.Discoveries()) != 1 {
		t.Fatalf("discoveries after Start = %+v", rt.Discoveries())
	}

	provider.SetModels([]modelinventory.Model{{
		CanonicalID: "vendor/v2",
		NativeID:    "v2",
	}})
	// Build alone (as used mid-refresh before publish) must not AcceptInventory
	// and must not be enough to change live discoveries (those commit in publish).
	built, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{{
		BackendID:       "acp-solo",
		Kind:            "cursor-cli-acp",
		BackendPrefixes: []string{"cursor-cli-acp"},
		Provider:        provider,
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(built.Registry.All()) != 1 || built.Registry.All()[0].NativeID != "v2" {
		t.Fatalf("Build registry = %+v", built.Registry.All())
	}
	if provider.Accepts() != acceptsAfterStart {
		t.Fatalf("Build AcceptInventory calls = %d, want still %d (no mid-refresh advance)", provider.Accepts(), acceptsAfterStart)
	}
	if got := provider.Accepted(); len(got) != 1 || got[0].NativeID != "v1" {
		t.Fatalf("allowlist during Build = %+v, want still v1 until publish", got)
	}

	rt.RunRefresh(context.Background())
	if provider.Accepts() <= acceptsAfterStart {
		t.Fatal("refresh publish must AcceptInventory")
	}
	if got := provider.Accepted(); len(got) != 2 {
		t.Fatalf("after refresh publish Accepted = %+v, want union v1+v2", got)
	}
	natives := map[string]bool{}
	for _, m := range provider.Accepted() {
		natives[m.NativeID] = true
	}
	if !natives["v1"] || !natives["v2"] {
		t.Fatalf("after refresh publish Accepted = %+v, want monotonic union", provider.Accepted())
	}
	diag := rt.Diagnostics()
	if diag.ModelCount != 1 {
		t.Fatalf("ModelCount = %d after publish", diag.ModelCount)
	}
	if got, ok := rt.Lookup("vendor/v2"); !ok || len(got) != 1 {
		t.Fatalf("Lookup after publish = %+v, %v", got, ok)
	}
}

func TestRuntime_PublishKeepsRegistryAndSnapshotAligned(t *testing.T) {
	t.Parallel()

	provider := &acceptingInventoryProvider{countingInventoryProvider: countingInventoryProvider{
		models: []modelinventory.Model{{
			CanonicalID: "vendor/a",
			NativeID:    "a",
		}},
	}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "solo",
			Kind:            "test",
			BackendPrefixes: []string{"test"},
			Provider:        provider,
		}},
		Cache: &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable},
		Now:   func() time.Time { return time.Unix(42, 0).UTC() },
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	diag := rt.Diagnostics()
	all := rt.All()
	if !diag.Active || diag.ModelCount != len(all) {
		t.Fatalf("diag ModelCount=%d All=%d Active=%v", diag.ModelCount, len(all), diag.Active)
	}
	if diag.Generation == "" {
		t.Fatal("expected generation")
	}
	if len(diag.BackendDiscoveries) != 1 || diag.BackendDiscoveries[0].ModelCount != 1 {
		t.Fatalf("discoveries = %+v", diag.BackendDiscoveries)
	}
}

func TestRuntime_StartCacheHitHydratesAcceptedInventoryAfterRefreshAttempt(t *testing.T) {
	t.Parallel()

	cache := &fakeModelRegistryCache{load: modelregistry.Snapshot{
		Generation:  "cached-generation",
		RefreshedAt: time.Unix(100, 0).UTC(),
		Models: []modelregistry.BackendModel{
			{
				CanonicalID: "openai/gpt-cached",
				NativeID:    "gpt-cached",
				DisplayName: "Cached",
				BackendID:   "acp-backend",
				Kind:        "cursor-cli-acp",
				Source:      modelinventory.SourceRemote,
				LoadedAt:    time.Unix(100, 0).UTC(),
			},
			{
				CanonicalID: "openai/gpt-other",
				NativeID:    "gpt-other",
				BackendID:   "other-backend",
				Kind:        "openai-responses",
				Source:      modelinventory.SourceRemote,
				LoadedAt:    time.Unix(100, 0).UTC(),
			},
		},
	}}
	provider := &acceptingInventoryProvider{countingInventoryProvider: countingInventoryProvider{err: errors.New("upstream unavailable")}}
	omitted := &acceptingInventoryProvider{countingInventoryProvider: countingInventoryProvider{err: errors.New("upstream unavailable")}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{
			{
				BackendID:       "acp-backend",
				Kind:            "cursor-cli-acp",
				BackendPrefixes: []string{"cursor-cli-acp"},
				Provider:        provider,
			},
			{
				BackendID:       "empty-backend",
				Kind:            "cursor-cli-acp",
				BackendPrefixes: []string{"cursor-cli-acp-empty"},
				Provider:        omitted,
			},
			{
				BackendID:       "other-backend",
				Kind:            "openai-responses",
				BackendPrefixes: []string{"openai-responses"},
				Provider:        &countingInventoryProvider{err: errors.New("upstream unavailable")},
			},
		},
		Cache: cache,
	})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Enumeration is attempted first; when every backend fails transiently the
	// valid cache is published as fallback.
	if provider.Calls() != 1 || omitted.Calls() != 1 {
		t.Fatalf("provider LoadModels calls = %d/%d, want 1/1", provider.Calls(), omitted.Calls())
	}
	got := provider.Accepted()
	if len(got) != 1 || got[0].CanonicalID != "openai/gpt-cached" || got[0].NativeID != "gpt-cached" {
		t.Fatalf("AcceptInventory = %+v, want cached model for acp-backend", got)
	}
	if omitted.Accepts() != 1 || len(omitted.Accepted()) != 0 {
		t.Fatalf("omitted backend accepts=%d models=%+v, want 1 clear", omitted.Accepts(), omitted.Accepted())
	}
}

func TestRuntime_StartIgnoresCacheWithUnconfiguredBackendID(t *testing.T) {
	t.Parallel()

	cache := &fakeModelRegistryCache{load: modelregistry.Snapshot{
		Generation: "stale-generation",
		Models: []modelregistry.BackendModel{{
			CanonicalID: "openai/gpt-stale",
			NativeID:    "gpt-stale",
			BackendID:   "removed-backend",
			Kind:        "openai-responses",
		}},
	}}
	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-remote",
		NativeID:    "gpt-remote",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "remote-backend",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
		Cache: cache,
	})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.Calls())
	}
	if _, ok := rt.Lookup("openai/gpt-stale"); ok {
		t.Fatal("stale cached backend should not be published")
	}
	if got, ok := rt.Lookup("openai/gpt-remote"); !ok || len(got) != 1 || got[0].BackendID != "remote-backend" {
		t.Fatalf("remote lookup = %+v, %v", got, ok)
	}
}

func TestRuntime_StartIgnoresCacheWithQualifiedCanonicalID(t *testing.T) {
	t.Parallel()

	cache := &fakeModelRegistryCache{load: modelregistry.Snapshot{
		Generation: "qualified-cache",
		Models: []modelregistry.BackendModel{{
			CanonicalID: "ollama:google/gemma4",
			NativeID:    "google/gemma4",
			BackendID:   "ollama-local",
			Kind:        "ollama",
		}},
	}}
	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "google/gemma4",
		NativeID:    "gemma4:latest",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "ollama-local",
			Kind:            "ollama",
			BackendPrefixes: []string{"ollama"},
			Provider:        provider,
		}},
		Cache: cache,
	})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.Calls())
	}
	if _, ok := rt.Lookup("ollama:google/gemma4"); ok {
		t.Fatal("qualified cached canonical id should not be published")
	}
	if got, ok := rt.Lookup("google/gemma4"); !ok || len(got) != 1 || got[0].NativeID != "gemma4:latest" {
		t.Fatalf("remote lookup = %+v, %v", got, ok)
	}
}

func TestRuntime_StartColdLoadsRemoteAndSavesCache(t *testing.T) {
	t.Parallel()

	cache := &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable}
	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-remote",
		NativeID:    "gpt-remote",
		DisplayName: "Remote",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "remote-backend",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
		Cache: cache,
		Now:   func() time.Time { return time.Unix(300, 0).UTC() },
	})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.Calls())
	}
	if cache.saves != 1 {
		t.Fatalf("cache saves = %d, want 1", cache.saves)
	}
	got, ok := rt.Lookup("openai/gpt-remote")
	if !ok || len(got) != 1 || got[0].BackendID != "remote-backend" {
		t.Fatalf("Lookup remote = %+v, %v", got, ok)
	}
}

func TestRuntime_RefreshFailureKeepsPriorSuccessfulRegistry(t *testing.T) {
	t.Parallel()

	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-before",
		NativeID:    "gpt-before",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "backend",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
		Cache: &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable},
		Now:   func() time.Time { return time.Unix(400, 0).UTC() },
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	provider.SetError(errors.New("upstream unavailable"))
	provider.SetModels(nil)
	rt.RunRefresh(context.Background())

	if got, ok := rt.Lookup("openai/gpt-before"); !ok || len(got) != 1 {
		t.Fatalf("prior lookup = %+v, %v", got, ok)
	}
	if _, ok := rt.Lookup("openai/gpt-after"); ok {
		t.Fatal("unexpected new model after failed refresh")
	}
	if rt.LastRefreshFailure() != modelregistry.RefreshFailureFetch {
		t.Fatalf("LastRefreshFailure = %q", rt.LastRefreshFailure())
	}
}

func TestRuntime_StartInvalidCacheFallsBackToRemote(t *testing.T) {
	t.Parallel()

	cache := &fakeModelRegistryCache{load: modelregistry.Snapshot{
		Generation: "invalid",
		Models: []modelregistry.BackendModel{{
			CanonicalID: "not-canonical",
			NativeID:    "x",
			BackendID:   "cached",
			Kind:        "test",
		}},
	}}
	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-remote",
		NativeID:    "gpt-remote",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "remote-backend",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
		Cache: cache,
	})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.Calls())
	}
	if got, ok := rt.Lookup("openai/gpt-remote"); !ok || len(got) != 1 {
		t.Fatalf("remote lookup = %+v, %v", got, ok)
	}
	if rt.LastCacheFailure() != modelregistry.RefreshFailureNone {
		t.Fatalf("LastCacheFailure = %q, want none", rt.LastCacheFailure())
	}
	diag := rt.Diagnostics()
	if diag.LastCacheErrorCategory != modelregistry.RefreshFailureNone {
		t.Fatalf("Diagnostics LastCacheErrorCategory = %q, want none", diag.LastCacheErrorCategory)
	}
	if diag.LastRefreshErrorCategory != modelregistry.RefreshFailureNone {
		t.Fatalf("Diagnostics LastRefreshErrorCategory = %q, want none", diag.LastRefreshErrorCategory)
	}
}

func TestRuntime_StartCacheLoadErrorFallsBackToRemoteAndReportsCacheFailure(t *testing.T) {
	t.Parallel()

	cache := &fakeModelRegistryCache{loadErr: errors.New("permission denied")}
	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-remote",
		NativeID:    "gpt-remote",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "remote-backend",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
		Cache: cache,
	})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.Calls())
	}
	if got, ok := rt.Lookup("openai/gpt-remote"); !ok || len(got) != 1 {
		t.Fatalf("remote lookup = %+v, %v", got, ok)
	}
	if rt.LastCacheFailure() != modelregistry.RefreshFailureNone {
		t.Fatalf("LastCacheFailure = %q, want none", rt.LastCacheFailure())
	}
}

func TestRuntime_SuccessfulRefreshClearsPriorCacheFailure(t *testing.T) {
	t.Parallel()

	cache := &fakeModelRegistryCache{loadErr: errors.New("permission denied")}
	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-remote",
		NativeID:    "gpt-remote",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "remote-backend",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
		Cache: cache,
	})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rt.LastCacheFailure() != modelregistry.RefreshFailureNone {
		t.Fatalf("LastCacheFailure after fallback = %q, want none", rt.LastCacheFailure())
	}

	cache.loadErr = nil
	rt.RunRefresh(context.Background())

	if rt.LastCacheFailure() != modelregistry.RefreshFailureNone {
		t.Fatalf("LastCacheFailure after successful refresh = %q, want none", rt.LastCacheFailure())
	}
	diag := rt.Diagnostics()
	if diag.LastCacheErrorCategory != modelregistry.RefreshFailureNone {
		t.Fatalf("Diagnostics LastCacheErrorCategory = %q, want none", diag.LastCacheErrorCategory)
	}
}

func TestRuntime_CacheSaveFailurePublishesRefreshedRegistry(t *testing.T) {
	t.Parallel()

	cache := &fakeModelRegistryCache{
		loadErr: modelregistry.ErrSnapshotUnavailable,
	}
	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-before",
		NativeID:    "gpt-before",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "backend",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
		Cache: cache,
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	cache.saveErr = errors.New("disk full")
	provider.SetModels([]modelinventory.Model{{
		CanonicalID: "openai/gpt-after",
		NativeID:    "gpt-after",
	}})
	rt.RunRefresh(context.Background())

	if got, ok := rt.Lookup("openai/gpt-after"); !ok || len(got) != 1 {
		t.Fatalf("refreshed lookup = %+v, %v", got, ok)
	}
	if rt.LastRefreshFailure() != modelregistry.RefreshFailureNone {
		t.Fatalf("LastRefreshFailure = %q", rt.LastRefreshFailure())
	}
	if rt.LastCacheFailure() != modelregistry.RefreshFailureCache {
		t.Fatalf("LastCacheFailure = %q", rt.LastCacheFailure())
	}
}

func TestRuntime_StartColdPublishesRemoteWhenCacheSaveFails(t *testing.T) {
	t.Parallel()

	cache := &fakeModelRegistryCache{
		loadErr: modelregistry.ErrSnapshotUnavailable,
		saveErr: errors.New("disk full"),
	}
	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-remote",
		NativeID:    "gpt-remote",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "backend",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
		Cache: cache,
	})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, ok := rt.Lookup("openai/gpt-remote"); !ok || len(got) != 1 {
		t.Fatalf("remote lookup = %+v, %v", got, ok)
	}
	if rt.LastRefreshFailure() != modelregistry.RefreshFailureNone {
		t.Fatalf("LastRefreshFailure = %q", rt.LastRefreshFailure())
	}
	if rt.LastCacheFailure() != modelregistry.RefreshFailureCache {
		t.Fatalf("LastCacheFailure = %q", rt.LastCacheFailure())
	}
}

func TestRuntime_DiagnosticsReportsActiveRegistryAndFailure(t *testing.T) {
	t.Parallel()

	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-before",
		NativeID:    "gpt-before",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "backend",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
		Cache: &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable},
		Now:   func() time.Time { return time.Unix(700, 0).UTC() },
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	provider.SetError(errors.New("upstream unavailable"))
	rt.RunRefresh(context.Background())

	diag := rt.Diagnostics()
	if !diag.Active {
		t.Fatal("expected active diagnostics")
	}
	if diag.ModelCount != 1 {
		t.Fatalf("ModelCount = %d, want 1", diag.ModelCount)
	}
	if diag.BackendModelCounts["backend"] != 1 {
		t.Fatalf("BackendModelCounts = %+v", diag.BackendModelCounts)
	}
	if diag.LastRefreshErrorCategory != modelregistry.RefreshFailureFetch {
		t.Fatalf("LastRefreshErrorCategory = %q", diag.LastRefreshErrorCategory)
	}
	if len(diag.BackendDiscoveries) != 1 {
		t.Fatalf("BackendDiscoveries len = %d", len(diag.BackendDiscoveries))
	}
	if diag.BackendDiscoveries[0].Status != modelinventory.DiscoveryStatusUnavailable {
		t.Fatalf("discovery Status = %q", diag.BackendDiscoveries[0].Status)
	}
	if diag.BackendDiscoveries[0].ErrorCode != modelinventory.ErrorCodeUnavailable {
		t.Fatalf("discovery ErrorCode = %q", diag.BackendDiscoveries[0].ErrorCode)
	}
}

func TestRuntime_DiagnosticsDefensiveCopy(t *testing.T) {
	t.Parallel()

	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "backend",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider: modelinventory.StaticProvider{Models: []modelinventory.Model{
				{CanonicalID: "openai/gpt-4o", NativeID: "gpt-4o"},
			}},
		}},
		Cache: &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	diag := rt.Diagnostics()
	if len(diag.BackendDiscoveries) != 1 {
		t.Fatalf("discoveries len = %d", len(diag.BackendDiscoveries))
	}
	diag.BackendDiscoveries[0].BackendID = "mutated"
	diag.BackendDiscoveries[0].ErrorCode = "leaked"
	diag.BackendModelCounts["backend"] = 99

	diag2 := rt.Diagnostics()
	if diag2.BackendDiscoveries[0].BackendID != "backend" {
		t.Fatalf("BackendID mutated through diagnostics copy: %q", diag2.BackendDiscoveries[0].BackendID)
	}
	if diag2.BackendDiscoveries[0].ErrorCode != "" {
		t.Fatalf("ErrorCode mutated through diagnostics copy: %q", diag2.BackendDiscoveries[0].ErrorCode)
	}
	if diag2.BackendModelCounts["backend"] != 1 {
		t.Fatalf("BackendModelCounts mutated: %+v", diag2.BackendModelCounts)
	}
}

func TestRuntime_RefreshFailureRetainsLastGoodAndRecordsDiscoveries(t *testing.T) {
	t.Parallel()

	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-before",
		NativeID:    "gpt-before",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "backend",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
		Cache: &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	provider.SetError(errors.New("api key=sekrit"))
	provider.SetModels(nil)
	rt.RunRefresh(context.Background())

	if got, ok := rt.Lookup("openai/gpt-before"); !ok || len(got) != 1 {
		t.Fatalf("prior lookup = %+v, %v", got, ok)
	}
	diag := rt.Diagnostics()
	if diag.LastRefreshErrorCategory != modelregistry.RefreshFailureFetch {
		t.Fatalf("LastRefreshErrorCategory = %q", diag.LastRefreshErrorCategory)
	}
	if len(diag.BackendDiscoveries) != 1 || diag.BackendDiscoveries[0].Status != modelinventory.DiscoveryStatusUnavailable {
		t.Fatalf("discoveries = %+v", diag.BackendDiscoveries)
	}
	if strings.Contains(diag.BackendDiscoveries[0].ErrorCode, "sekrit") {
		t.Fatalf("ErrorCode leaked raw text: %q", diag.BackendDiscoveries[0].ErrorCode)
	}
}

func TestRuntime_InvalidInventoryRefreshOmitsBackendKeepsSibling(t *testing.T) {
	t.Parallel()

	good := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "vendor/good",
		NativeID:    "good",
	}}}
	bad := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "vendor/bad-before",
		NativeID:    "bad-before",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{
			{
				BackendID:       "good",
				Kind:            "test",
				BackendPrefixes: []string{"test-good"},
				Provider:        good,
			},
			{
				BackendID:       "bad",
				Kind:            "test",
				BackendPrefixes: []string{"test-bad"},
				Provider:        bad,
			},
		},
		Cache: &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := rt.All(); len(got) != 2 {
		t.Fatalf("initial All() len = %d, want 2", len(got))
	}

	bad.SetModels([]modelinventory.Model{
		{CanonicalID: "vendor/still-valid-first", NativeID: "still-valid-first"},
		{CanonicalID: "", NativeID: "secret-native"},
	})
	good.SetModels([]modelinventory.Model{{
		CanonicalID: "vendor/good-refreshed",
		NativeID:    "good-refreshed",
	}})
	rt.RunRefresh(context.Background())

	got := rt.All()
	if len(got) != 1 || got[0].BackendID != "good" || got[0].CanonicalID != "vendor/good-refreshed" {
		t.Fatalf("after refresh All() = %+v, want only refreshed good", got)
	}
	if _, ok := rt.Lookup("vendor/still-valid-first"); ok {
		t.Fatal("partial rows from invalid backend must not publish")
	}
	diag := rt.Diagnostics()
	byID := map[string]modelregistry.BackendDiscovery{}
	for _, d := range diag.BackendDiscoveries {
		byID[d.BackendID] = d
	}
	if byID["good"].Status != modelinventory.DiscoveryStatusOK || byID["good"].ModelCount != 1 {
		t.Fatalf("good discovery = %+v", byID["good"])
	}
	if byID["bad"].Status != modelinventory.DiscoveryStatusUnavailable || byID["bad"].ErrorCode != modelinventory.ErrorCodeInvalidInventory {
		t.Fatalf("bad discovery = %+v", byID["bad"])
	}
	if strings.Contains(byID["bad"].ErrorCode, "secret") {
		t.Fatalf("ErrorCode leaked raw text: %q", byID["bad"].ErrorCode)
	}
	if diag.LastRefreshErrorCategory != modelregistry.RefreshFailureNone {
		t.Fatalf("LastRefreshErrorCategory = %q, want none when sibling remains usable", diag.LastRefreshErrorCategory)
	}
}

func TestRuntime_InvalidInventoryRefreshRetainsLastGoodWhenNoUsableRows(t *testing.T) {
	t.Parallel()

	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "vendor/before",
		NativeID:    "before",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "solo",
			Kind:            "test",
			BackendPrefixes: []string{"test"},
			Provider:        provider,
		}},
		Cache: &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	provider.SetModels([]modelinventory.Model{{CanonicalID: "broken", NativeID: "x"}})
	rt.RunRefresh(context.Background())

	if got, ok := rt.Lookup("vendor/before"); !ok || len(got) != 1 {
		t.Fatalf("prior lookup = %+v, %v", got, ok)
	}
	diag := rt.Diagnostics()
	if diag.LastRefreshErrorCategory != modelregistry.RefreshFailureFetch {
		t.Fatalf("LastRefreshErrorCategory = %q", diag.LastRefreshErrorCategory)
	}
	if len(diag.BackendDiscoveries) != 1 || diag.BackendDiscoveries[0].ErrorCode != modelinventory.ErrorCodeInvalidInventory {
		t.Fatalf("discoveries = %+v", diag.BackendDiscoveries)
	}
}

// Regression: when refresh retains the last-good registry after an invalid
// Build, allowlists must stay (or be re-synced) to that snapshot so Open cannot
// diverge from GET /v1/models.
func TestRuntime_RetainLastGoodResyncsAcceptedInventoryAfterInvalidRefresh(t *testing.T) {
	t.Parallel()

	provider := &trackingAcceptInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "vendor/before",
		NativeID:    "before",
		DisplayName: "Before",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "acp-solo",
			Kind:            "cursor-cli-acp",
			BackendPrefixes: []string{"cursor-cli-acp"},
			Provider:        provider,
		}},
		Cache: &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := provider.Accepted(); len(got) != 1 || got[0].CanonicalID != "vendor/before" {
		t.Fatalf("AcceptInventory after Start = %+v, want vendor/before", got)
	}

	provider.models = []modelinventory.Model{{CanonicalID: "broken", NativeID: "x"}}
	rt.RunRefresh(context.Background())

	if got, ok := rt.Lookup("vendor/before"); !ok || len(got) != 1 {
		t.Fatalf("retained Lookup = %+v, %v", got, ok)
	}
	got := provider.Accepted()
	if len(got) != 1 || got[0].CanonicalID != "vendor/before" || got[0].NativeID != "before" {
		t.Fatalf("AcceptInventory after retain = %+v, want last-good resync", got)
	}
	diag := rt.Diagnostics()
	if diag.LastRefreshErrorCategory != modelregistry.RefreshFailureFetch {
		t.Fatalf("LastRefreshErrorCategory = %q", diag.LastRefreshErrorCategory)
	}
}

func TestRuntime_AuthoritativeEmptyRefreshPublishesEmpty(t *testing.T) {
	t.Parallel()

	provider := &trackingAcceptInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "vendor/before",
		NativeID:    "before",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "acp-solo",
			Kind:            "cursor-cli-acp",
			BackendPrefixes: []string{"cursor-cli-acp"},
			Provider:        provider,
		}},
		Cache: &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Successful empty enumeration is authoritative: the empty registry is
	// published instead of resurrecting the prior snapshot.
	provider.models = nil
	rt.RunRefresh(context.Background())

	if _, ok := rt.Lookup("vendor/before"); ok {
		t.Fatal("removed model must not be resurrected after authoritative empty refresh")
	}
	if got := rt.All(); len(got) != 0 {
		t.Fatalf("All() = %+v, want empty", got)
	}
	// Provider gates stay monotonic: acceptance retains the last-good native
	// even though advertisement dropped it.
	got := provider.Accepted()
	if len(got) != 1 || got[0].CanonicalID != "vendor/before" || got[0].NativeID != "before" {
		t.Fatalf("AcceptInventory after empty publish = %+v, want last-good retained", got)
	}
	diag := rt.Diagnostics()
	if diag.LastRefreshErrorCategory != modelregistry.RefreshFailureNone {
		t.Fatalf("LastRefreshErrorCategory = %q, want none for authoritative empty", diag.LastRefreshErrorCategory)
	}
	if len(diag.BackendDiscoveries) != 1 || diag.BackendDiscoveries[0].Status != modelinventory.DiscoveryStatusEmpty {
		t.Fatalf("discoveries = %+v, want single empty status", diag.BackendDiscoveries)
	}
}

func TestRuntime_StartAllUnavailablePublishesEmptyRegistry(t *testing.T) {
	t.Parallel()

	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "broken",
			Kind:            "test",
			BackendPrefixes: []string{"test"},
			Provider:        modelinventory.ErrorProvider{Err: errors.New("construction failed")},
		}},
		Cache: &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got := rt.All(); len(got) != 0 {
		t.Fatalf("All() len = %d, want 0", len(got))
	}
	diag := rt.Diagnostics()
	if !diag.Active {
		t.Fatal("expected active empty registry")
	}
	if len(diag.BackendDiscoveries) != 1 || diag.BackendDiscoveries[0].Status != modelinventory.DiscoveryStatusUnavailable {
		t.Fatalf("discoveries = %+v", diag.BackendDiscoveries)
	}
}

func TestRuntime_StartCanceledContextDoesNotPublishEmptySuccess(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "slow",
			Kind:            "test",
			BackendPrefixes: []string{"test"},
			Provider: cancelAwareInventoryProvider{
				started: started,
				models:  []modelinventory.Model{{CanonicalID: "vendor/a", NativeID: "a"}},
			},
		}},
		Cache: &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable},
	})
	go func() {
		<-started
		cancel()
	}()

	err := rt.Start(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want context.Canceled", err)
	}
	if rt.ActiveRegistry() != nil {
		t.Fatal("canceled Start must not publish an active registry")
	}
}

func TestRuntime_CacheStartupMarksMissingBackendModelsAsEmpty(t *testing.T) {
	t.Parallel()

	cache := &fakeModelRegistryCache{load: modelregistry.Snapshot{
		Generation: "partial-cache",
		Models: []modelregistry.BackendModel{{
			CanonicalID: "openai/gpt-cached",
			NativeID:    "gpt-cached",
			BackendID:   "present",
			Kind:        "openai-responses",
			Source:      modelinventory.SourceRemote,
		}},
	}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{
			{
				BackendID:       "present",
				Kind:            "openai-responses",
				BackendPrefixes: []string{"openai-responses"},
				Provider:        &countingInventoryProvider{err: errors.New("must not fetch")},
			},
			{
				BackendID:       "absent",
				Kind:            "openai-responses",
				BackendPrefixes: []string{"openai-responses"},
				Provider:        &countingInventoryProvider{err: errors.New("must not fetch")},
			},
		},
		Cache: cache,
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	diag := rt.Diagnostics()
	byID := map[string]modelregistry.BackendDiscovery{}
	for _, d := range diag.BackendDiscoveries {
		byID[d.BackendID] = d
	}
	if byID["present"].Status != modelinventory.DiscoveryStatusCached || byID["present"].ModelCount != 1 {
		t.Fatalf("present = %+v", byID["present"])
	}
	if byID["absent"].Status != modelinventory.DiscoveryStatusEmpty || byID["absent"].ModelCount != 0 {
		t.Fatalf("absent = %+v, want empty status", byID["absent"])
	}
	if byID["absent"].ErrorCode != modelinventory.ErrorCodeEmpty {
		t.Fatalf("absent ErrorCode = %q, want %q", byID["absent"].ErrorCode, modelinventory.ErrorCodeEmpty)
	}
}

func TestRuntime_ConcurrentLookupDuringRefresh(t *testing.T) {
	t.Parallel()

	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-initial",
		NativeID:    "gpt-initial",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "backend",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
		Cache: &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 200 {
				_, _ = rt.Lookup("openai/gpt-initial")
				_ = rt.All()
			}
		})
	}
	for range 20 {
		rt.RunRefresh(context.Background())
	}
	wg.Wait()
}

type fakeModelRegistryCache struct {
	load    modelregistry.Snapshot
	loadErr error
	saveErr error
	saves   int
	saved   modelregistry.Snapshot
}

func (c *fakeModelRegistryCache) Load(context.Context) (modelregistry.Snapshot, error) {
	if c.loadErr != nil {
		return modelregistry.Snapshot{}, c.loadErr
	}
	return c.load, nil
}

func (c *fakeModelRegistryCache) Save(_ context.Context, snap modelregistry.Snapshot) error {
	if c.saveErr != nil {
		return c.saveErr
	}
	c.saves++
	c.saved = snap
	return nil
}

type countingInventoryProvider struct {
	mu     sync.Mutex
	calls  int
	err    error
	models []modelinventory.Model
}

func (p *countingInventoryProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	if ctx == nil {
		return modelinventory.Snapshot{}, modelinventory.ErrNilContext
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.err != nil {
		return modelinventory.Snapshot{}, p.err
	}
	return modelinventory.Snapshot{
		Source:   modelinventory.SourceRemote,
		LoadedAt: time.Unix(500, 0).UTC(),
		Models:   p.models,
	}, nil
}

func (p *countingInventoryProvider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *countingInventoryProvider) SetModels(models []modelinventory.Model) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.models = models
}

func (p *countingInventoryProvider) SetError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

// acceptingInventoryProvider tracks AcceptInventory calls for allowlist hydration.
type acceptingInventoryProvider struct {
	countingInventoryProvider
	mu       sync.Mutex
	accepted []modelinventory.Model
	accepts  int
}

func (p *acceptingInventoryProvider) AcceptInventory(models []modelinventory.Model) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.accepts++
	p.accepted = append([]modelinventory.Model(nil), models...)
	if p.accepted == nil {
		p.accepted = []modelinventory.Model{}
	}
}

func (p *acceptingInventoryProvider) Accepted() []modelinventory.Model {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]modelinventory.Model(nil), p.accepted...)
}

func (p *acceptingInventoryProvider) Accepts() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.accepts
}

func TestRuntime_RunRefreshSkipsWhenInFlight(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	release := make(chan struct{})
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	provider := &blockingInventoryProvider{
		entered:     entered,
		release:     release,
		inFlight:    &inFlight,
		maxInFlight: &maxInFlight,
		models: []modelinventory.Model{{
			CanonicalID: "openai/gpt-block",
			NativeID:    "gpt-block",
		}},
	}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "backend",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
		Cache: &fakeModelRegistryCache{load: modelregistry.Snapshot{
			Generation:  "seed",
			RefreshedAt: time.Unix(1, 0).UTC(),
			Models: []modelregistry.BackendModel{{
				CanonicalID: "openai/gpt-block",
				NativeID:    "gpt-block",
				BackendID:   "backend",
				Kind:        "openai-responses",
			}},
		}},
	})
	// Startup enumerates first and holds the refresh guard, so concurrent
	// refreshes skip instead of stampeding the blocked provider.
	startErr := make(chan error, 1)
	go func() { startErr <- rt.Start(context.Background()) }()
	<-entered
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { rt.RunRefresh(context.Background()) })
	}
	// Give skipped callers a moment to hit the guard.
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if err := <-startErr; err != nil {
		t.Fatalf("Start: %v", err)
	}
	if maxInFlight.Load() != 1 {
		t.Fatalf("max concurrent LoadModels = %d, want 1", maxInFlight.Load())
	}
}

func TestRuntime_IdenticalRefreshSkipsAllowlistRebuild(t *testing.T) {
	t.Parallel()

	provider := &acceptingInventoryProvider{}
	provider.models = []modelinventory.Model{{
		CanonicalID: "openai/gpt-stable",
		NativeID:    "gpt-stable",
	}}
	var nowMu sync.Mutex
	now := time.Unix(2000, 0).UTC()
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "backend",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
		Now: func() time.Time {
			nowMu.Lock()
			defer nowMu.Unlock()
			return now
		},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	gen1 := rt.Diagnostics().Generation
	accepts := provider.Accepts()
	if accepts < 1 {
		t.Fatal("expected AcceptInventory on Start")
	}
	nowMu.Lock()
	now = now.Add(time.Hour)
	nowMu.Unlock()
	rt.RunRefresh(context.Background())
	gen2 := rt.Diagnostics().Generation
	if gen2 != gen1 {
		t.Fatalf("generation changed on identical catalog: %q -> %q", gen1, gen2)
	}
	if provider.Accepts() != accepts {
		t.Fatalf("AcceptInventory calls = %d, want %d (skip rebuild)", provider.Accepts(), accepts)
	}
}

type blockingInventoryProvider struct {
	entered     chan struct{}
	release     chan struct{}
	inFlight    *atomic.Int32
	maxInFlight *atomic.Int32
	models      []modelinventory.Model
	onceEnter   sync.Once
}

func (p *blockingInventoryProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	if ctx == nil {
		return modelinventory.Snapshot{}, modelinventory.ErrNilContext
	}
	n := p.inFlight.Add(1)
	for {
		cur := p.maxInFlight.Load()
		if n <= cur || p.maxInFlight.CompareAndSwap(cur, n) {
			break
		}
	}
	defer p.inFlight.Add(-1)
	p.onceEnter.Do(func() { close(p.entered) })
	select {
	case <-p.release:
	case <-ctx.Done():
		return modelinventory.Snapshot{}, ctx.Err()
	}
	return modelinventory.Snapshot{
		Source: modelinventory.SourceRemote,
		Models: p.models,
	}, nil
}

func TestRuntime_PartialTransientFailureMergesRetainedRows(t *testing.T) {
	t.Parallel()

	good := &acceptingInventoryProvider{countingInventoryProvider: countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "vendor/good",
		NativeID:    "good",
	}}}}
	bad := &acceptingInventoryProvider{countingInventoryProvider: countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "vendor/bad",
		NativeID:    "bad",
	}}}}
	cache := &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{
			{
				BackendID:       "good",
				Kind:            "test",
				BackendPrefixes: []string{"test-good"},
				Provider:        good,
			},
			{
				BackendID:       "bad",
				Kind:            "test",
				BackendPrefixes: []string{"test-bad"},
				Provider:        bad,
			},
		},
		Cache: cache,
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cache.saves != 1 {
		t.Fatalf("cache saves after start = %d, want 1", cache.saves)
	}

	good.SetModels([]modelinventory.Model{{
		CanonicalID: "vendor/good-refreshed",
		NativeID:    "good-refreshed",
	}})
	bad.SetError(errors.New("upstream unavailable"))
	bad.SetModels(nil)
	rt.RunRefresh(context.Background())

	// Fresh rows advertise; transiently failed backend retains prior rows.
	if got, ok := rt.Lookup("vendor/good-refreshed"); !ok || len(got) != 1 {
		t.Fatalf("refreshed lookup = %+v, %v", got, ok)
	}
	if got, ok := rt.Lookup("vendor/bad"); !ok || len(got) != 1 {
		t.Fatalf("retained lookup = %+v, %v", got, ok)
	}
	if rt.LastRefreshFailure() != modelregistry.RefreshFailureNone {
		t.Fatalf("LastRefreshFailure = %q, want none when merged registry is usable", rt.LastRefreshFailure())
	}
	if cache.saves != 1 {
		t.Fatalf("cache saves after merged refresh = %d, want still 1 (stale rows not persisted)", cache.saves)
	}
	diag := rt.Diagnostics()
	byID := map[string]modelregistry.BackendDiscovery{}
	for _, d := range diag.BackendDiscoveries {
		byID[d.BackendID] = d
	}
	if byID["good"].Status != modelinventory.DiscoveryStatusOK || byID["good"].ModelCount != 1 {
		t.Fatalf("good discovery = %+v", byID["good"])
	}
	if byID["bad"].Status != modelinventory.DiscoveryStatusUnavailable {
		t.Fatalf("bad discovery = %+v, want unavailable", byID["bad"])
	}
	// Acceptance stays monotonic across the merged publish.
	natives := map[string]bool{}
	for _, m := range bad.Accepted() {
		natives[m.NativeID] = true
	}
	if !natives["bad"] {
		t.Fatalf("bad Accepted = %+v, want retained native", bad.Accepted())
	}
}

func TestRuntime_SuccessfulRemovalPropagates(t *testing.T) {
	t.Parallel()

	provider := &countingInventoryProvider{models: []modelinventory.Model{
		{CanonicalID: "vendor/keep", NativeID: "keep"},
		{CanonicalID: "vendor/drop", NativeID: "drop"},
	}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "solo",
			Kind:            "test",
			BackendPrefixes: []string{"test"},
			Provider:        provider,
		}},
		Cache: &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	provider.SetModels([]modelinventory.Model{{
		CanonicalID: "vendor/keep",
		NativeID:    "keep",
	}})
	rt.RunRefresh(context.Background())

	if _, ok := rt.Lookup("vendor/drop"); ok {
		t.Fatal("removed model still advertised after successful refresh")
	}
	if got, ok := rt.Lookup("vendor/keep"); !ok || len(got) != 1 {
		t.Fatalf("kept lookup = %+v, %v", got, ok)
	}
	if rt.LastRefreshFailure() != modelregistry.RefreshFailureNone {
		t.Fatalf("LastRefreshFailure = %q", rt.LastRefreshFailure())
	}
}
