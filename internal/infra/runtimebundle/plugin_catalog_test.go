package runtimebundle_test

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/catalog"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
)

func TestResolvePluginCatalog_InspectAndServeShareSnapshot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeE2EManifest(t, root, "a.backendplugin.json", "io.dup", "conflict-kind")
	writeE2EManifest(t, root, "b.backendplugin.json", "io.dup2", "conflict-kind")
	reg := pluginreg.NewRegistry()
	_ = standardplugins.InstallEssentialBackendsOn(reg, standardplugins.UpstreamAPIKeys{})
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{
				Enabled: true, Paths: []string{root}, DevelopmentMode: true,
			},
			Backends: []config.PluginConfig{{
				Kind: "conflict-kind", ID: "c1", Enabled: true,
			}},
		},
	}
	staging := t.TempDir()
	serveRes, err := runtimebundle.ResolvePluginCatalog(cfg, reg, staging)
	if err != nil {
		t.Fatal(err)
	}
	inspectRep, inspectErr := runtimebundle.InspectBackendPlugins(cfg, reg)
	if !errors.Is(inspectErr, serveRes.CatalogErr) {
		t.Fatalf("inspect err=%v serve CatalogErr=%v (must be identical)", inspectErr, serveRes.CatalogErr)
	}
	if serveRes.CatalogErr == nil {
		t.Fatal("enabled unresolved/conflict must fail closed")
	}
	type key struct {
		kind   string
		state  catalog.State
		reason catalog.Reason
	}
	fromSnap := map[key]int{}
	for _, e := range serveRes.Snapshot.Entries {
		fromSnap[key{e.ExportKind, e.State, e.Reason}]++
	}
	fromInspect := map[key]int{}
	for _, e := range inspectRep.Entries {
		if e.Source == "builtin" {
			continue
		}
		fromInspect[key{e.Kind, e.State, e.Reason}]++
	}
	for k, n := range fromSnap {
		if fromInspect[k] < n {
			t.Fatalf("inspect missing snapshot entry %+v (snap=%d inspect=%d); snap=%+v inspect=%+v",
				k, n, fromInspect[k], serveRes.Snapshot.Entries, inspectRep.Entries)
		}
	}
}
