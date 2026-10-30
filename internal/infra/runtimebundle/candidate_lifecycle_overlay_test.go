package runtimebundle_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/testkit"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type orderedProbeLifecycle struct {
	tag      string
	starts   atomic.Int32
	stops    atomic.Int32
	recorder *[]string
	mu       *sync.Mutex
}

func (l *orderedProbeLifecycle) Start(context.Context) error {
	l.starts.Add(1)
	if l.recorder != nil && l.mu != nil {
		l.mu.Lock()
		*l.recorder = append(*l.recorder, l.tag+":start")
		l.mu.Unlock()
	}
	return nil
}

func (l *orderedProbeLifecycle) Stop(context.Context) error {
	l.stops.Add(1)
	if l.recorder != nil && l.mu != nil {
		l.mu.Lock()
		*l.recorder = append(*l.recorder, l.tag+":stop")
		l.mu.Unlock()
	}
	return nil
}

func (l *orderedProbeLifecycle) SafeUnderCandidateOverlap() bool { return true }

func TestCompileCandidate_DirectCandidateLifecycleOwnershipAndOrder(t *testing.T) {
	t.Parallel()

	var events []string
	var mu sync.Mutex
	var factoryCalls atomic.Int32

	pluginLife := &orderedProbeLifecycle{tag: "plugin", recorder: &events, mu: &mu}
	overlayLife := &orderedProbeLifecycle{tag: "overlay", recorder: &events, mu: &mu}

	reg := pluginreg.NewRegistry()
	require.NoError(t, reg.RegisterFeature("cand-order-feat", func(yaml.Node) (aipfeature.FeatureBundle, error) {
		factoryCalls.Add(1)
		return testkit.FeatureBundle(t, "cand-order-feat", nil, []aipplugin.Lifecycle{pluginLife}), nil
	}))

	var empty yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("{}"), &empty))

	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{
				{Kind: "cand-order-feat", ID: "f1", Enabled: true, Config: empty},
			},
		},
	}
	require.NoError(t, config.Validate(cfg))

	opts := &runtimebundle.BuildOptions{PluginRegistry: reg}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: opts,
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	cand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
		CandidateOpts: &runtimebundle.BuildOptions{
			FeatureLifecycles: []aipplugin.Lifecycle{overlayLife},
		},
	})
	require.NoError(t, err)

	require.Equal(t, int32(1), factoryCalls.Load(), "feature factory must be called exactly once during candidate compile")
	require.Equal(t, int32(1), pluginLife.starts.Load())
	require.Equal(t, int32(1), overlayLife.starts.Load())
	require.Equal(t, int32(0), pluginLife.stops.Load())
	require.Equal(t, int32(0), overlayLife.stops.Load())

	mu.Lock()
	require.Equal(t, []string{"plugin:start", "overlay:start"}, events)
	mu.Unlock()

	require.NoError(t, cand.RollbackUnpublished())

	require.Equal(t, int32(1), factoryCalls.Load(), "feature factory must not be called during rollback/close")
	require.Equal(t, int32(1), pluginLife.stops.Load())
	require.Equal(t, int32(1), overlayLife.stops.Load())

	mu.Lock()
	require.Equal(t, []string{"plugin:start", "overlay:start", "overlay:stop", "plugin:stop"}, events)
	mu.Unlock()
}

func TestCompileCandidate_ProcessExistingLifecycleAndCandidateConfigNotSuppressed(t *testing.T) {
	t.Parallel()

	var events []string
	var mu sync.Mutex
	var factoryCalls atomic.Int32

	procLife := &orderedProbeLifecycle{tag: "proc", recorder: &events, mu: &mu}
	candPluginLife := &orderedProbeLifecycle{tag: "cand-plugin", recorder: &events, mu: &mu}

	reg := pluginreg.NewRegistry()
	require.NoError(t, reg.RegisterFeature("cand-new-feat", func(yaml.Node) (aipfeature.FeatureBundle, error) {
		factoryCalls.Add(1)
		return testkit.FeatureBundle(t, "cand-new-feat", nil, []aipplugin.Lifecycle{candPluginLife}), nil
	}))

	baseCfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	require.NoError(t, config.Validate(baseCfg))

	opts := &runtimebundle.BuildOptions{
		PluginRegistry:    reg,
		FeatureLifecycles: []aipplugin.Lifecycle{procLife},
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  baseCfg,
		Log:  testkit.DiscardLogger(),
		Opts: opts,
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	var empty yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("{}"), &empty))

	candCfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{
				{Kind: "cand-new-feat", ID: "f1", Enabled: true, Config: empty},
			},
		},
	}
	require.NoError(t, config.Validate(candCfg))

	cand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Bus:       hooks.New(hooks.Config{}),
		Candidate: candCfg,
	})
	require.NoError(t, err)

	require.Equal(t, int32(1), factoryCalls.Load())
	require.Equal(t, int32(1), candPluginLife.starts.Load())
	require.Equal(t, int32(1), procLife.starts.Load())

	mu.Lock()
	require.Equal(t, []string{"cand-plugin:start", "proc:start"}, events)
	mu.Unlock()

	require.NoError(t, cand.Close())

	require.Equal(t, int32(1), factoryCalls.Load())
	mu.Lock()
	require.Equal(t, []string{"cand-plugin:start", "proc:start", "proc:stop", "cand-plugin:stop"}, events)
	mu.Unlock()
}

func TestCompileCandidate_LifecycleFailureRollbackNoLeak(t *testing.T) {
	t.Parallel()

	var events []string
	var mu sync.Mutex
	var factoryCalls atomic.Int32

	pluginLife := &orderedProbeLifecycle{tag: "plugin", recorder: &events, mu: &mu}
	overlayLife := &orderedProbeLifecycle{tag: "overlay", recorder: &events, mu: &mu}

	reg := pluginreg.NewRegistry()
	require.NoError(t, reg.RegisterFeature("cand-leak-feat", func(yaml.Node) (aipfeature.FeatureBundle, error) {
		factoryCalls.Add(1)
		return testkit.FeatureBundle(t, "cand-leak-feat", nil, []aipplugin.Lifecycle{pluginLife}), nil
	}))

	var empty yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("{}"), &empty))

	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{
				{Kind: "cand-leak-feat", ID: "f1", Enabled: true, Config: empty},
			},
		},
	}
	require.NoError(t, config.Validate(cfg))

	opts := &runtimebundle.BuildOptions{PluginRegistry: reg}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: opts,
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	_, err = runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
		CandidateOpts: &runtimebundle.BuildOptions{
			FeatureLifecycles: []aipplugin.Lifecycle{overlayLife},
		},
		FaultInject: runtimebundle.CandidateFaultInject{
			After: "activate",
		},
	})
	require.ErrorIs(t, err, runtimebundle.ErrCandidateFaultInjected)

	require.Equal(t, int32(1), factoryCalls.Load())
	require.Equal(t, int32(1), pluginLife.starts.Load())
	require.Equal(t, int32(1), overlayLife.starts.Load())
	require.Equal(t, int32(1), pluginLife.stops.Load())
	require.Equal(t, int32(1), overlayLife.stops.Load())

	mu.Lock()
	require.Equal(t, []string{"plugin:start", "overlay:start", "overlay:stop", "plugin:stop"}, events)
	mu.Unlock()
}

func TestCompileCandidate_PrebuiltFeaturePlanesNoDuplicateLifecycles(t *testing.T) {
	t.Parallel()

	var events []string
	var mu sync.Mutex

	life1 := &orderedProbeLifecycle{tag: "prebuilt-1", recorder: &events, mu: &mu}
	life2 := &orderedProbeLifecycle{tag: "prebuilt-2", recorder: &events, mu: &mu}

	var factoryCalls atomic.Int32
	reg := pluginreg.NewRegistry()
	require.NoError(t, reg.RegisterFeature("cand-prebuilt-feat", func(yaml.Node) (aipfeature.FeatureBundle, error) {
		factoryCalls.Add(1)
		return testkit.FeatureBundle(t, "cand-prebuilt-feat", nil, []aipplugin.Lifecycle{life1, life2}), nil
	}))

	var empty yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("{}"), &empty))

	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{
				{Kind: "cand-prebuilt-feat", ID: "f1", Enabled: true, Config: empty},
			},
		},
	}
	require.NoError(t, config.Validate(cfg))

	opts := &runtimebundle.BuildOptions{PluginRegistry: reg}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: opts,
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	genMerged, err := featurebundle.MergeFeatureSurfacesWithHost(reg, config.RegistrationsFromConfig(cfg), featurebundle.HostContributions{})
	require.NoError(t, err)
	require.Equal(t, int32(1), factoryCalls.Load())

	// Reset factory call counter before CompileCandidate
	factoryCalls.Store(0)

	cand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
		CandidateOpts: &runtimebundle.BuildOptions{
			FeaturePlanes:           genMerged.Frozen,
			FeatureLifecycles:       genMerged.Lifecycles,
			ReplaceCandidateSurface: true,
		},
	})
	require.NoError(t, err)

	// Factory counter must remain 0 because prebuilt FeaturePlanes skips merge
	require.Equal(t, int32(0), factoryCalls.Load(), "feature factory must not be called when prebuilt FeaturePlanes is supplied")

	// Verify exact starts count and order
	require.Equal(t, int32(1), life1.starts.Load())
	require.Equal(t, int32(1), life2.starts.Load())
	require.Equal(t, int32(0), life1.stops.Load())
	require.Equal(t, int32(0), life2.stops.Load())

	mu.Lock()
	require.Equal(t, []string{"prebuilt-1:start", "prebuilt-2:start"}, events)
	mu.Unlock()

	require.NoError(t, cand.Close())

	// Verify exact stops count and reverse order
	require.Equal(t, int32(1), life1.stops.Load())
	require.Equal(t, int32(1), life2.stops.Load())

	mu.Lock()
	require.Equal(t, []string{"prebuilt-1:start", "prebuilt-2:start", "prebuilt-2:stop", "prebuilt-1:stop"}, events)
	mu.Unlock()
}

func TestCompileCandidate_PrebuiltFeaturePlanesRollbackStopsReverseOrder(t *testing.T) {
	t.Parallel()

	var events []string
	var mu sync.Mutex

	life1 := &orderedProbeLifecycle{tag: "prebuilt-1", recorder: &events, mu: &mu}
	life2 := &orderedProbeLifecycle{tag: "prebuilt-2", recorder: &events, mu: &mu}

	var factoryCalls atomic.Int32
	reg := pluginreg.NewRegistry()
	require.NoError(t, reg.RegisterFeature("cand-prebuilt-feat", func(yaml.Node) (aipfeature.FeatureBundle, error) {
		factoryCalls.Add(1)
		return testkit.FeatureBundle(t, "cand-prebuilt-feat", nil, []aipplugin.Lifecycle{life1, life2}), nil
	}))

	var empty yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("{}"), &empty))

	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{
				{Kind: "cand-prebuilt-feat", ID: "f1", Enabled: true, Config: empty},
			},
		},
	}
	require.NoError(t, config.Validate(cfg))

	opts := &runtimebundle.BuildOptions{PluginRegistry: reg}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: opts,
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	genMerged, err := featurebundle.MergeFeatureSurfacesWithHost(reg, config.RegistrationsFromConfig(cfg), featurebundle.HostContributions{})
	require.NoError(t, err)

	factoryCalls.Store(0)

	_, err = runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
		CandidateOpts: &runtimebundle.BuildOptions{
			FeaturePlanes:           genMerged.Frozen,
			FeatureLifecycles:       genMerged.Lifecycles,
			ReplaceCandidateSurface: true,
		},
		FaultInject: runtimebundle.CandidateFaultInject{
			After: "activate",
		},
	})
	require.ErrorIs(t, err, runtimebundle.ErrCandidateFaultInjected)

	require.Equal(t, int32(0), factoryCalls.Load())
	require.Equal(t, int32(1), life1.starts.Load())
	require.Equal(t, int32(1), life2.starts.Load())
	require.Equal(t, int32(1), life1.stops.Load())
	require.Equal(t, int32(1), life2.stops.Load())

	mu.Lock()
	require.Equal(t, []string{"prebuilt-1:start", "prebuilt-2:start", "prebuilt-2:stop", "prebuilt-1:stop"}, events)
	mu.Unlock()
}
