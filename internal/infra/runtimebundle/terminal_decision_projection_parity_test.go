package runtimebundle_test

import (
	"context"
	"log/slog"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/internal/testkit"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

type parityTestTerminalDecisionProvider struct {
	id string
}

func (p *parityTestTerminalDecisionProvider) ID() string { return p.id }

func (p *parityTestTerminalDecisionProvider) Decide(_ context.Context, _ terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{
		Kind:       terminaldecision.DecisionAllowStop,
		ReasonCode: "parity-stop",
	}, nil
}

func leaseTerminalDecisionProvider(l *runtimehost.Lease) terminaldecision.Provider {
	if l == nil || l.RequestPlane() == nil {
		return nil
	}
	if p, ok := l.RequestPlane().(interface {
		TerminalDecisionProvider() terminaldecision.Provider
	}); ok {
		return p.TerminalDecisionProvider()
	}
	return nil
}

func TestTerminalDecisionGenerationProjectionAndReloadPinning(t *testing.T) {
	t.Parallel()

	provA := &parityTestTerminalDecisionProvider{id: "provider-a"}
	provB := &parityTestTerminalDecisionProvider{id: "provider-b"}

	registry := pluginreg.NewRegistry()
	require.NoError(t, registry.RegisterFeature("feature-term-a", func(yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, "feature-term-a", func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "feature-term-a", terminaldecision.Provider(provA))
		}, nil), nil
	}))
	require.NoError(t, registry.RegisterFeature("feature-term-b", func(yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, "feature-term-b", func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "feature-term-b", terminaldecision.Provider(provB))
		}, nil), nil
	}))

	baseCfg := &config.Config{
		Routing:     config.RoutingConfig{MaxAttempts: 3},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server:      config.ServerConfig{MaxRequestBodyBytes: 65536, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 65536},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
	}
	require.NoError(t, config.Validate(baseCfg))

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  baseCfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: registry},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	m := runtimehost.NewManager(4, nil)
	t.Cleanup(func() { _ = m.ShutdownDetached(context.Background()) })

	// 1. Publish G1 with Provider A
	cfgG1 := *baseCfg
	cfgG1.Plugins.Features = []config.PluginConfig{{ID: "feature-term-a", Enabled: true}}
	g1Bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: &cfgG1,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	require.NoError(t, err)
	g1Gen := m.PrepareRequestPlane("g1", g1Bundle)
	require.NoError(t, m.Publish(g1Gen))

	// 2. Admit and retain leaseG1
	leaseG1, ok := m.Acquire()
	require.True(t, ok)
	t.Cleanup(leaseG1.Release)

	// 3. Assert projection through leaseG1.RequestPlane() is A
	assert.Equal(t, provA, leaseTerminalDecisionProvider(leaseG1))

	// 4. Publish G2 with Provider B
	cfgG2 := *baseCfg
	cfgG2.Plugins.Features = []config.PluginConfig{{ID: "feature-term-b", Enabled: true}}
	g2Bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: &cfgG2,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	require.NoError(t, err)
	g2Gen := m.PrepareRequestPlane("g2", g2Bundle)
	require.NoError(t, m.Publish(g2Gen))

	// 5. Admit and retain leaseG2
	leaseG2, ok := m.Acquire()
	require.True(t, ok)
	t.Cleanup(leaseG2.Release)

	// 6. Assert through leases: G1=A, G2=B
	assert.Equal(t, provA, leaseTerminalDecisionProvider(leaseG1))
	assert.Equal(t, provB, leaseTerminalDecisionProvider(leaseG2))

	// 7. Publish G3 with no provider (feature disabled)
	cfgG3 := *baseCfg
	cfgG3.Plugins.Features = []config.PluginConfig{{ID: "feature-term-a", Enabled: false}}
	g3Bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: &cfgG3,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	require.NoError(t, err)
	g3Gen := m.PrepareRequestPlane("g3", g3Bundle)
	require.NoError(t, m.Publish(g3Gen))

	// 8. Admit and retain leaseG3
	leaseG3, ok := m.Acquire()
	require.True(t, ok)
	t.Cleanup(leaseG3.Release)

	// 9. Assert through leases: G1=A, G2=B, G3=nil
	assert.Equal(t, provA, leaseTerminalDecisionProvider(leaseG1))
	assert.Equal(t, provB, leaseTerminalDecisionProvider(leaseG2))
	assert.Nil(t, leaseTerminalDecisionProvider(leaseG3))

	// 10. Attempt invalid G4 candidate (duplicate providers in candidate planes)
	candSrc := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(candSrc, aipfeature.PlaneTerminalDecisionProvider, "plugin-x", terminaldecision.Provider(provA)))
	candFrozen := candSrc.Freeze()

	_, err = runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: &cfgG1, // has feature-term-a (provA)
		CandidateOpts: &runtimebundle.BuildOptions{
			FeaturePlanes: candFrozen, // also has provA -> conflict
		},
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.NotFoundHandler(), nil
		},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, aipfeature.ErrExclusiveConflict)

	// 11. Assert active generation in manager is still G3
	assert.Equal(t, g3Gen, m.Active())

	// 12. Reassert through all retained leases: G1=A, G2=B, G3=nil
	assert.Equal(t, provA, leaseTerminalDecisionProvider(leaseG1))
	assert.Equal(t, provB, leaseTerminalDecisionProvider(leaseG2))
	assert.Nil(t, leaseTerminalDecisionProvider(leaseG3))
}
