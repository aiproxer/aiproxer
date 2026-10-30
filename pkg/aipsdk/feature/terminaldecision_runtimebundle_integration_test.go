package feature_test

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

type integrationTDProvider struct {
	id     string
	calls  atomic.Int64
	decide func(context.Context, terminaldecision.Input) (terminaldecision.Decision, error)
}

func (p *integrationTDProvider) ID() string { return p.id }

func (p *integrationTDProvider) Decide(ctx context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	p.calls.Add(1)
	if p.decide != nil {
		return p.decide(ctx, in)
	}
	return terminaldecision.Decision{
		Kind:       terminaldecision.DecisionAllowStop,
		ReasonCode: "integration-stop",
	}, nil
}

func (p *integrationTDProvider) CallCount() int64 {
	return p.calls.Load()
}

func TestTerminalDecisionMapBackedCandidate_CompileGenerationSuccessAndIsolation(t *testing.T) {
	t.Parallel()

	candProvider := &integrationTDProvider{id: "cand-term-provider"}

	csCand := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(csCand, feature.PlaneTerminalDecisionProvider, "cand-plugin", terminaldecision.Provider(candProvider)))
	candidateFrozen := csCand.Freeze()

	reg := pluginreg.NewRegistry()

	cfg := &config.Config{
		Routing:     config.RoutingConfig{MaxAttempts: 3},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server:      config.ServerConfig{MaxRequestBodyBytes: 65536, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 65536},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
	}
	require.NoError(t, config.Validate(cfg))

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  slog.Default(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	generation, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		CandidateOpts: &runtimebundle.BuildOptions{
			FeaturePlanes: candidateFrozen,
		},
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.NotFoundHandler(), nil
		},
	})
	require.NoError(t, err)
	require.NotNil(t, generation)
	t.Cleanup(func() { _ = generation.Close() })

	assert.Equal(t, int64(0), candProvider.CallCount())

	gb, ok := generation.(*runtimebundle.GenerationBundle)
	require.True(t, ok)
	termProv := gb.TerminalDecisionProvider()
	require.NotNil(t, termProv)
	assert.Equal(t, "cand-term-provider", termProv.ID())
}

func TestTerminalDecisionMapBackedCandidate_CompileGenerationConflictWithBase(t *testing.T) {
	t.Parallel()

	baseProvider := &integrationTDProvider{id: "base-term-provider"}
	candProvider := &integrationTDProvider{id: "cand-term-provider"}

	reg := pluginreg.NewRegistry()
	require.NoError(t, reg.RegisterFeature("feature-term-base", func(n yaml.Node) (feature.FeatureBundle, error) {
		cs := feature.NewContributionSet()
		_ = feature.Contribute(cs, feature.PlaneTerminalDecisionProvider, "feature-term-base", terminaldecision.Provider(baseProvider))
		return feature.BundleFromPlanes(cs.Freeze(), nil), nil
	}))

	cfg := &config.Config{
		Routing:     config.RoutingConfig{MaxAttempts: 3},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server:      config.ServerConfig{MaxRequestBodyBytes: 65536, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 65536},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{
				{ID: "feature-term-base", Enabled: true},
			},
		},
	}
	require.NoError(t, config.Validate(cfg))

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  slog.Default(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	genBaseline, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.NotFoundHandler(), nil
		},
	})
	require.NoError(t, err)
	require.NotNil(t, genBaseline)
	t.Cleanup(func() { _ = genBaseline.Close() })

	gbBase, ok := genBaseline.(*runtimebundle.GenerationBundle)
	require.True(t, ok)
	assert.Equal(t, "base-term-provider", gbBase.TerminalDecisionProvider().ID())

	csCand := feature.NewContributionSet()
	require.NoError(t, feature.Contribute(csCand, feature.PlaneTerminalDecisionProvider, "cand-plugin", terminaldecision.Provider(candProvider)))
	candFrozen := csCand.Freeze()

	_, err = runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		CandidateOpts: &runtimebundle.BuildOptions{
			FeaturePlanes: candFrozen,
		},
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.NotFoundHandler(), nil
		},
	})

	require.Error(t, err)
	var attributed *feature.AttributedError
	require.ErrorAs(t, err, &attributed)
	assert.Equal(t, "candidate", attributed.PluginID)
	assert.Equal(t, feature.PlaneTerminalDecisionProvider.ID, attributed.PlaneID)
	require.ErrorIs(t, err, feature.ErrExclusiveConflict)
	assert.Contains(t, err.Error(), `"base-term-provider" and "cand-term-provider"`)

	// Prior generation remains usable
	assert.Equal(t, int64(0), baseProvider.CallCount())
	assert.Equal(t, int64(0), candProvider.CallCount())
	assert.Equal(t, "base-term-provider", gbBase.TerminalDecisionProvider().ID())
}

func TestTerminalDecisionMalformedMapCandidate_CompileGenerationAttributed(t *testing.T) {
	t.Parallel()

	baseProvider := &integrationTDProvider{id: "base-term-provider"}

	reg := pluginreg.NewRegistry()
	require.NoError(t, reg.RegisterFeature("feature-term-base", func(n yaml.Node) (feature.FeatureBundle, error) {
		cs := feature.NewContributionSet()
		_ = feature.Contribute(cs, feature.PlaneTerminalDecisionProvider, "feature-term-base", terminaldecision.Provider(baseProvider))
		return feature.BundleFromPlanes(cs.Freeze(), nil), nil
	}))

	cfg := &config.Config{
		Routing:     config.RoutingConfig{MaxAttempts: 3},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server:      config.ServerConfig{MaxRequestBodyBytes: 65536, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 65536},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{
				{ID: "feature-term-base", Enabled: true},
			},
		},
	}
	require.NoError(t, config.Validate(cfg))

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  slog.Default(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	genBaseline, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.NotFoundHandler(), nil
		},
	})
	require.NoError(t, err)
	require.NotNil(t, genBaseline)
	t.Cleanup(func() { _ = genBaseline.Close() })

	candProvider := &integrationTDProvider{id: "cand-term-provider"}
	malformedFrozen := feature.NewMalformedGeneratedFrozenTerminalDecisionMissingIdentityForTest(candProvider)

	_, err = runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		CandidateOpts: &runtimebundle.BuildOptions{
			FeaturePlanes: malformedFrozen,
		},
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.NotFoundHandler(), nil
		},
	})

	require.Error(t, err)
	var attributed *feature.AttributedError
	require.ErrorAs(t, err, &attributed)
	assert.Equal(t, "candidate", attributed.PluginID)
	assert.Equal(t, feature.PlaneTerminalDecisionProvider.ID, attributed.PlaneID)
	require.ErrorIs(t, err, feature.ErrInvalidContribution)

	// Baseline remains usable
	gb, ok := genBaseline.(*runtimebundle.GenerationBundle)
	require.True(t, ok)
	require.NotNil(t, gb.TerminalDecisionProvider())
	assert.Equal(t, "base-term-provider", gb.TerminalDecisionProvider().ID())
	assert.Equal(t, int64(0), baseProvider.CallCount())
}
