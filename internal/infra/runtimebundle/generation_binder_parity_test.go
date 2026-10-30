package runtimebundle

import (
	"context"
	"log/slog"
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	featurecompaction "github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/reasoninghost"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type dummyHandlerComposer struct{}

func (dummyHandlerComposer) Compose(context.Context, *config.Config, *slog.Logger, httpcontract.StandardHTTPInput) (http.Handler, error) {
	return http.NotFoundHandler(), nil
}

func TestCompileGeneration_BinderFailuresFailClosed(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	require.NoError(t, standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}))

	reasoningNode := reasoningYAMLForTypedNil(t, "egress-ref")
	var compactionNode yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("extractor:\n  enabled: true\n  route: inherit\n"), &compactionNode))

	t.Run("compaction continuity missing prerequisite fails CompileGeneration before publication", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			Routing:    config.RoutingConfig{MaxAttempts: 3},
			Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
			Server:     config.ServerConfig{MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096},
			Diagnostics: config.DiagnosticsConfig{
				Enabled:    true,
				HealthPath: "/healthz",
			},
			Plugins: config.PluginsConfig{
				Features: []config.PluginConfig{
					{ID: featurecompaction.ID, Enabled: true, Config: compactionNode},
				},
			},
		}
		require.NoError(t, config.Validate(cfg))

		ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
			Cfg:  cfg,
			Log:  slog.Default(),
			Opts: &BuildOptions{PluginRegistry: reg},
		})
		require.NoError(t, err)
		ps.StandardFeatures = nil
		t.Cleanup(func() { _ = ps.Close() })

		gen, err := CompileGeneration(context.Background(), GenerationCompileInput{
			Process: ps,
			Compose: dummyHandlerComposer{}.Compose,
		})
		require.Error(t, err)
		assert.Nil(t, gen)
		assert.Contains(t, err.Error(), "generation prerequisite")
	})

	t.Run("reasoning compression missing capability fails CompileGeneration before publication", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			Routing:    config.RoutingConfig{MaxAttempts: 3},
			Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
			Server:     config.ServerConfig{MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096},
			Diagnostics: config.DiagnosticsConfig{
				Enabled:    true,
				HealthPath: "/healthz",
			},
			Plugins: config.PluginsConfig{
				Features: []config.PluginConfig{
					{ID: reasoningpreservation.ID, Enabled: true, Config: reasoningNode},
				},
			},
		}
		require.NoError(t, config.Validate(cfg))

		prod := ProductionOptions{
			FeatureHostRegistrations: []featurehost.Registration{
				(&reasoninghost.Binding{
					EgressPolicies: map[string]reasoninghost.EgressPolicy{
						"egress-ref": charEgressPolicy{version: "v1"},
					},
					MatcherResolver: charMatcherResolver{},
				}).Registration(),
			},
		}

		ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
			Cfg:  cfg,
			Log:  slog.Default(),
			Opts: &BuildOptions{PluginRegistry: reg, Production: prod},
		})
		require.NoError(t, err)
		ps.BackgroundAux = nil
		t.Cleanup(func() { _ = ps.Close() })

		gen, err := CompileGeneration(context.Background(), GenerationCompileInput{
			Process: ps,
			Compose: dummyHandlerComposer{}.Compose,
		})
		require.Error(t, err)
		assert.Nil(t, gen)
		assert.Contains(t, err.Error(), "BackgroundAux")
	})

	t.Run("complete capabilities successfully binds both generation binders", func(t *testing.T) {
		t.Parallel()
		cfg := &config.Config{
			Routing:    config.RoutingConfig{MaxAttempts: 3},
			Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
			Server:     config.ServerConfig{MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096},
			Diagnostics: config.DiagnosticsConfig{
				Enabled:    true,
				HealthPath: "/healthz",
			},
			Plugins: config.PluginsConfig{
				Features: []config.PluginConfig{
					{ID: featurecompaction.ID, Enabled: true, Config: compactionNode},
					{ID: reasoningpreservation.ID, Enabled: true, Config: reasoningNode},
				},
			},
		}
		require.NoError(t, config.Validate(cfg))

		scheduler, err := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner {
			return fixedRunnerForTypedNil{id: "full-gen"}
		}, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 4, MaxResults: 10})
		require.NoError(t, err)
		t.Cleanup(func() { _ = scheduler.Close() })

		prod := ProductionOptions{
			FeatureHostRegistrations: []featurehost.Registration{
				(&reasoninghost.Binding{
					EgressPolicies: map[string]reasoninghost.EgressPolicy{
						"egress-ref": charEgressPolicy{version: "v1"},
					},
					MatcherResolver: charMatcherResolver{},
				}).Registration(),
			},
		}
		opts := &BuildOptions{PluginRegistry: reg, Production: prod}

		ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
			Cfg:           cfg,
			Log:           slog.Default(),
			Opts:          opts,
			BackgroundAux: scheduler,
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = ps.Close() })

		gen, err := CompileGeneration(context.Background(), GenerationCompileInput{
			Process: ps,
			Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
			},
		})
		require.NoError(t, err)
		require.NotNil(t, gen)
		t.Cleanup(func() { _ = gen.Close() })
	})
}
