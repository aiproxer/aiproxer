package runtimebundle_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/stretchr/testify/require"
)

func emptyVisibleTurnRetryBaseConfig() *config.Config {
	return &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
	}
}

// TestCompileGeneration_emptyVisibleTurnRetryGatePinnedPerGeneration proves the
// typed gate section is resolved into each generation's executor and stays
// pinned there (spec empty-visible-turn-retry, requirements 1.1 and 1.3;
// design "Generation-pinned configuration"): an explicitly enabled section
// carries an enabled executor gate, an omitted one carries a disabled gate,
// and building the later generation does not change the earlier one.
func TestCompileGeneration_emptyVisibleTurnRetryGatePinnedPerGeneration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	enabledCfg := emptyVisibleTurnRetryBaseConfig()
	enabledCfg.EmptyVisibleTurnRetry.Enabled = true

	ps, err := runtimebundle.NewProcessServices(ctx, runtimebundle.ProcessServicesInput{
		Cfg:  emptyVisibleTurnRetryBaseConfig(),
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: generationRegistry(t)},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	genOn, err := runtimebundle.CompileGeneration(ctx, runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: enabledCfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = genOn.Close() })
	onExec := runtimebundle.GenerationExecutorOf(genOn)
	require.NotNil(t, onExec, "enabled generation must expose an executor")
	require.True(t, onExec.EmptyVisibleTurnRetry.Enabled, "an explicitly enabled section must resolve to an enabled executor gate")

	genOff, err := runtimebundle.CompileGeneration(ctx, runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: emptyVisibleTurnRetryBaseConfig(),
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = genOff.Close() })
	offExec := runtimebundle.GenerationExecutorOf(genOff)
	require.NotNil(t, offExec, "disabled generation must expose an executor")
	require.False(t, offExec.EmptyVisibleTurnRetry.Enabled, "an omitted section must resolve to a disabled executor gate")
	require.True(t, onExec.EmptyVisibleTurnRetry.Enabled, "the earlier generation must keep its own gate after a later generation builds")
}
