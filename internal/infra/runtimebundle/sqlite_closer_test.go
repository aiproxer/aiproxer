package runtimebundle_test

import (
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
)

func TestBuild_sqliteStoreRegistersCloser(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	dbpath := filepath.Join(tmp, "continuity.db")
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{
			InMemory:   false,
			Store:      "sqlite",
			SQLitePath: dbpath,
		},
	}
	ps, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	// Candidate ledger entries are generation-owned only; sqlite continuity lives on ProcessServices.
	// The second and third entries are the ledger-owned keep-warm generation
	// lifecycle: its prepare-phase start/stop plus its quiesce-phase stop so
	// retired generations release maintenance work at retirement (Req 6.5).
	if b.Ledger().Len() != 3 {
		t.Fatalf("expected 3 generation closers (upstream idle plus keep-warm prepare and quiesce lifecycle entries), got %d", b.Ledger().Len())
	}
	if ps.Closed() {
		t.Fatal("process must remain open while candidate is live")
	}
	if err := b.Close(); err != nil {
		t.Fatalf("candidate close: %v", err)
	}
	if ps.Closed() {
		t.Fatal("process must remain open after candidate close")
	}
}
