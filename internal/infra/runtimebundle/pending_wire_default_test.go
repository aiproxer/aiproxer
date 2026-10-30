package runtimebundle_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
)

func TestBuild_zeroMaxPendingWireEventsRemainsUnlimited(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Server:     config.ServerConfig{}, // MaxPendingWireEvents == 0
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	_, built := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if got := built.Executor().MaxPendingWireEvents; got != 0 {
		t.Fatalf("Executor.MaxPendingWireEvents = %d, want 0 (unlimited)", got)
	}
}
