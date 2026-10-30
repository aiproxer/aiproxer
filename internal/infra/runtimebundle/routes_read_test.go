package runtimebundle_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
)

func TestRoutesSnapshotFrom_matchesEffectiveDefaultRoute(t *testing.T) {
	t.Parallel()
	cfgPath := filepath.Join("..", "..", "..", "config", "config.yaml")
	cfg, err := config.LoadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := routing.ValidateModelAliasesConfig(cfg); err != nil {
		t.Fatal(err)
	}
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	raw := config.EffectiveDefaultRouteSelector(cfg, standardplugins.DefaultWireModel)
	ar, err := routing.NewAliasResolver(routing.ModelAliasRulesFromConfig(cfg))
	if err != nil {
		t.Fatal(err)
	}
	wantRoute := ar.Resolve(raw)
	snap, err := runtimebundle.RoutesSnapshotFrom(cfg, reg)
	if err != nil {
		t.Fatal(err)
	}
	if snap.EffectiveDefaultRoute != wantRoute {
		t.Fatalf("effective route: want %q got %q", wantRoute, snap.EffectiveDefaultRoute)
	}
}

func TestRoutesSnapshotFrom_stubVsLivePosture(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "stub1", Kind: "local-stub", Enabled: true},
			},
		},
	}
	reg := pluginreg.NewRegistry()
	snap, err := runtimebundle.RoutesSnapshotFrom(cfg, reg)
	if err != nil {
		t.Fatal(err)
	}
	if snap.CredentialPosture != "all_local_stub" {
		t.Fatalf("want all_local_stub, got %q", snap.CredentialPosture)
	}

	cfg2 := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "stub1", Kind: "local-stub", Enabled: true},
				{ID: "oai", Kind: "openai-responses", Enabled: true},
			},
		},
	}
	snap2, err := runtimebundle.RoutesSnapshotFrom(cfg2, reg)
	if err != nil {
		t.Fatal(err)
	}
	if snap2.CredentialPosture != "live_provider" {
		t.Fatalf("want live_provider, got %q", snap2.CredentialPosture)
	}
}

func TestRoutesSnapshotFrom_modelAliasesRoundTripJSON(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		ModelAliases: []config.ModelAliasConfig{
			{Pattern: `^foo$`, Replacement: "stub:bar"},
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b", Kind: "local-stub", Enabled: true}},
		},
	}
	reg := pluginreg.NewRegistry()
	snap, err := runtimebundle.RoutesSnapshotFrom(cfg, reg)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.ModelAliases) != 1 || snap.ModelAliases[0].Pattern != `^foo$` || snap.ModelAliases[0].Replacement != "stub:bar" {
		t.Fatalf("aliases: %+v", snap.ModelAliases)
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var decoded runtimebundle.RoutesSnapshot
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.ModelAliases) != 1 {
		t.Fatalf("json round trip: %+v", decoded)
	}
}

func TestRoutesSnapshotFrom_requiresRegistry(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b", Enabled: true}},
		},
	}
	_, err := runtimebundle.RoutesSnapshotFrom(cfg, nil)
	if err == nil {
		t.Fatal("expected error for nil registry")
	}
}

func TestRoutesSnapshotFrom_respectsDisabledBackend(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "off", Kind: "openai-responses", Enabled: false},
				{ID: "on", Kind: "local-stub", Enabled: true},
			},
		},
	}
	reg := pluginreg.NewRegistry()
	snap, err := runtimebundle.RoutesSnapshotFrom(cfg, reg)
	if err != nil {
		t.Fatal(err)
	}
	if snap.CredentialPosture != "all_local_stub" {
		t.Fatalf("disabled openai row must not force live_provider: got %q", snap.CredentialPosture)
	}
	if len(snap.Backends) != 2 {
		t.Fatalf("backends: %+v", snap.Backends)
	}
}
