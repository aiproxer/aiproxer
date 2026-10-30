package runtimebundle_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type sideChannelLife struct {
	tag    string
	starts atomic.Int32
	stops  atomic.Int32
}

func (l *sideChannelLife) Start(context.Context) error     { l.starts.Add(1); return nil }
func (l *sideChannelLife) Stop(context.Context) error      { l.stops.Add(1); return nil }
func (l *sideChannelLife) SafeUnderCandidateOverlap() bool { return true }

type sideChannelHook struct{ tag string }

func (h sideChannelHook) ID() string                      { return h.tag }
func (sideChannelHook) Order() int                        { return 0 }
func (sideChannelHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (sideChannelHook) Handle(context.Context, *aipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, nil
}

type sideChannelProvider struct{ tag string }

func (p sideChannelProvider) ID() string { return p.tag }

func (sideChannelProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

func sideChannelYAMLNode(t *testing.T) yaml.Node {
	t.Helper()
	var n yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("{}"), &n))
	return n
}

// Pins the legacy lifecycle side channel at the BuildFeatureHooks seam:
// lifecycles from enabled features leave through the separate return slot in
// registration order (disabled registrations contribute nothing and their
// factories are never invoked), while hooks.Config carries only hook planes.
func TestBuildFeatureHooks_LifecycleSideChannelRegistrationOrderSkipsDisabled(t *testing.T) {
	t.Parallel()

	lifeA := &sideChannelLife{tag: "life-a"}
	lifeB1 := &sideChannelLife{tag: "life-b1"}
	lifeB2 := &sideChannelLife{tag: "life-b2"}

	reg := pluginreg.NewRegistry()
	facA := "sc-fac-a-" + t.Name()
	facDisabled := "sc-fac-disabled-" + t.Name()
	facB := "sc-fac-b-" + t.Name()
	require.NoError(t, reg.RegisterFeature(facA, func(yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, facA, func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneSubmitHooks, facA, []sdkhooks.SubmitHook{sideChannelHook{tag: "hook-a"}})
		}, []aipplugin.Lifecycle{lifeA}), nil
	}))
	require.NoError(t, reg.RegisterFeature(facDisabled, func(yaml.Node) (aipfeature.FeatureBundle, error) {
		panic("disabled registration must not invoke factory")
	}))
	require.NoError(t, reg.RegisterFeature(facB, func(yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, facB, func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneSubmitHooks, facB, []sdkhooks.SubmitHook{sideChannelHook{tag: "hook-b"}})
		}, []aipplugin.Lifecycle{lifeB1, lifeB2}), nil
	}))

	cfgNode := sideChannelYAMLNode(t)
	hookCfg, lifes, err := runtimebundle.BuildFeatureHooks(reg, []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: "inst-a", FactoryKind: facA, Enabled: true, Config: aipsdk.ConfigPayload{Node: cfgNode}},
		{Kind: aipsdk.PluginKindFeature, ID: "inst-dis", FactoryKind: facDisabled, Enabled: false, Config: aipsdk.ConfigPayload{Node: cfgNode}},
		{Kind: aipsdk.PluginKindFeature, ID: "inst-b", FactoryKind: facB, Enabled: true, Config: aipsdk.ConfigPayload{Node: cfgNode}},
	})
	require.NoError(t, err)

	require.Len(t, hookCfg.SubmitHooks, 2)
	require.Equal(t, "hook-a", hookCfg.SubmitHooks[0].ID())
	require.Equal(t, "hook-b", hookCfg.SubmitHooks[1].ID())
	require.Empty(t, hookCfg.RequestPartHooks)
	require.Empty(t, hookCfg.ResponsePartHooks)
	require.Empty(t, hookCfg.ToolReactors)

	require.Len(t, lifes, 3)
	require.Same(t, lifeA, lifes[0])
	require.Same(t, lifeB1, lifes[1])
	require.Same(t, lifeB2, lifes[2])
	for _, life := range lifes {
		sideLife, ok := life.(*sideChannelLife)
		require.True(t, ok)
		require.Zero(t, sideLife.starts.Load(), "BuildFeatureHooks must not start lifecycles")
	}
}

// Pins the merge failure path: a terminal-decision conflict surfaces the
// typed error and returns an empty hook config with nil lifecycles; the
// partially accumulated candidate (including its lifecycles) is discarded.
func TestBuildFeatureHooks_mergeFailureReturnsZeroConfigAndNilLifecycles(t *testing.T) {
	t.Parallel()

	doomed := &sideChannelLife{tag: "doomed"}
	reg := pluginreg.NewRegistry()
	facP1 := "sc-fac-p1-" + t.Name()
	facP2 := "sc-fac-p2-" + t.Name()
	require.NoError(t, reg.RegisterFeature(facP1, func(yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, "sc.p1", func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "sc.p1", terminaldecision.Provider(sideChannelProvider{tag: "sc.p1"}))
		}, []aipplugin.Lifecycle{doomed}), nil
	}))
	require.NoError(t, reg.RegisterFeature(facP2, func(yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, "sc.p2", func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "sc.p2", terminaldecision.Provider(sideChannelProvider{tag: "sc.p2"}))
		}, nil), nil
	}))

	cfgNode := sideChannelYAMLNode(t)
	hookCfg, lifes, err := runtimebundle.BuildFeatureHooks(reg, []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: "inst-p1", FactoryKind: facP1, Enabled: true, Config: aipsdk.ConfigPayload{Node: cfgNode}},
		{Kind: aipsdk.PluginKindFeature, ID: "inst-p2", FactoryKind: facP2, Enabled: true, Config: aipsdk.ConfigPayload{Node: cfgNode}},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, aipfeature.ErrExclusiveConflict)
	require.Contains(t, err.Error(), "sc.p1")
	require.Contains(t, err.Error(), "sc.p2")
	require.Equal(t, hooks.Config{}, hookCfg)
	require.Nil(t, lifes)
	require.Zero(t, doomed.starts.Load())
}

// Pins end-to-end transport through CompileGeneration: lifecycles merged from
// enabled feature registrations reach the candidate ledger and are started
// exactly once per published generation and stopped on close.
func TestCompileGeneration_TransportsMergedFeatureLifecyclesToGenerationRuntime(t *testing.T) {
	t.Parallel()

	lifeA := &sideChannelLife{tag: "e2e.a"}
	lifeB := &sideChannelLife{tag: "e2e.b"}

	reg := stdFactoryCatalog(t)
	facE2eA := "sc-fac-e2e-a-" + t.Name()
	facE2eB := "sc-fac-e2e-b-" + t.Name()
	require.NoError(t, reg.RegisterFeature(facE2eA, func(yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, facE2eA, func(cs *aipfeature.ContributionSet) error {
			return nil
		}, []aipplugin.Lifecycle{lifeA}), nil
	}))
	require.NoError(t, reg.RegisterFeature(facE2eB, func(yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, facE2eB, func(cs *aipfeature.ContributionSet) error {
			return nil
		}, []aipplugin.Lifecycle{lifeB}), nil
	}))

	cfg := processBaseConfig()
	require.NoError(t, config.Validate(cfg))
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	cand := stubCandidateConfig(t, "sc-e2e-backend", "sc-e2e-text", "sc-e2e-backend:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
	})
	cand.Plugins.Features = []config.PluginConfig{
		{Kind: facE2eA, ID: "sc-life-a", Enabled: true, Config: genYAMLNode(t, "{}")},
		{Kind: facE2eB, ID: "sc-life-b", Enabled: true, Config: genYAMLNode(t, "{}")},
	}
	require.NoError(t, config.Validate(cand))

	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	require.NoError(t, err)

	require.Equal(t, int32(1), lifeA.starts.Load(), "feature A lifecycle must start once via merged surface")
	require.Equal(t, int32(1), lifeB.starts.Load(), "feature B lifecycle must start once via merged surface")
	require.Equal(t, int32(0), lifeA.stops.Load())
	require.Equal(t, int32(0), lifeB.stops.Load())

	require.NoError(t, bundle.Close())
	require.Equal(t, int32(1), lifeA.starts.Load())
	require.Equal(t, int32(1), lifeB.starts.Load())
	require.Equal(t, int32(1), lifeA.stops.Load())
	require.Equal(t, int32(1), lifeB.stops.Load())

	require.False(t, ps.Closed(), "process services must survive generation close")
}
