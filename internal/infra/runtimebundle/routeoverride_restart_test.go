package runtimebundle_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

func TestProcessServices_sqliteReopenRetainsActiveAndClearedOverride(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "cont-ov-restart.db")
	cfg := routeOverrideBaseConfig()
	cfg.Continuity = config.ContinuityConfig{
		InMemory:   false,
		Store:      "sqlite",
		SQLitePath: path,
	}
	ctx := context.Background()
	ps1 := mustRouteOverrideProcess(t, cfg)
	store1 := ps1.RouteOverrideStore
	if store1 == nil {
		t.Fatal("sqlite process must expose override store")
	}
	leg, err := ps1.Continuity.CreateALeg(ctx, "ov-sqlite-reopen")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_003_000, 0).UTC()
	active, err := store1.Replace(ctx, leg.ALegID, "openai:gpt-4", now)
	if err != nil {
		t.Fatal(err)
	}
	cleared, err := store1.Clear(ctx, leg.ALegID, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Active || cleared.Revision != active.Revision+1 {
		t.Fatalf("cleared state: %+v after %+v", cleared, active)
	}
	if err := ps1.Close(); err != nil {
		t.Fatalf("close first process: %v", err)
	}

	ps2 := mustRouteOverrideProcess(t, cfg)
	t.Cleanup(func() { _ = ps2.Close() })
	got, err := ps2.RouteOverrideStore.Snapshot(ctx, leg.ALegID)
	if err != nil {
		t.Fatalf("reopen Snapshot: %v", err)
	}
	if got.Active || got.Selector != "" || got.Revision != cleared.Revision || got.ALegID != leg.ALegID {
		t.Fatalf("sqlite reopen must retain cleared revision: got %+v want %+v", got, cleared)
	}
}

func TestProcessServices_memoryRebuildDoesNotRetainOverride(t *testing.T) {
	t.Parallel()
	cfg := routeOverrideBaseConfig()
	ctx := context.Background()
	ps1 := mustRouteOverrideProcess(t, cfg)
	leg, err := ps1.Continuity.CreateALeg(ctx, "ov-mem-rebuild")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ps1.RouteOverrideStore.Replace(ctx, leg.ALegID, "openai:gpt-4", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := ps1.Close(); err != nil {
		t.Fatalf("close first process: %v", err)
	}

	ps2 := mustRouteOverrideProcess(t, cfg)
	t.Cleanup(func() { _ = ps2.Close() })
	if _, err := ps2.RouteOverrideStore.Snapshot(ctx, leg.ALegID); !errors.Is(err, routeoverride.ErrNotFound) {
		t.Fatalf("memory process rebuild must not retain override: %v", err)
	}
}

func mustRouteOverrideProcess(t *testing.T, cfg *config.Config) *runtimebundle.ProcessServices {
	t.Helper()
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: generationRegistry(t)},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	return ps
}
