package runtimebundle

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	featurecompaction "github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// --- Stubs for Compaction Parity Tests ---

type parityCompactionObs struct {
	id string
}

func (o parityCompactionObs) ID() string { return o.id }
func (parityCompactionObs) OnCompaction(context.Context, compaction.Event) error {
	return nil
}

type parityCompactionPreserver struct {
	id string
}

func (p parityCompactionPreserver) ID() string { return p.id }
func (parityCompactionPreserver) BeforeRequest(context.Context, *aipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (parityCompactionPreserver) RequestOpened(context.Context, aipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (parityCompactionPreserver) BeforeResponseRelease(context.Context, *aipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

type parityPanicPreserver struct{}

func (parityPanicPreserver) ID() string {
	panic("malicious preserver ID panic")
}

func (parityPanicPreserver) BeforeRequest(context.Context, *aipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (parityPanicPreserver) RequestOpened(context.Context, aipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (parityPanicPreserver) BeforeResponseRelease(context.Context, *aipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func TestCompactionProjection_ParityWithFrozenAndRegistrationOrder(t *testing.T) {
	t.Parallel()

	b1 := testkit.FeatureBundle(t, "b1", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneCompactionObservers, "b1", []compaction.Observer{
			parityCompactionObs{id: "obs-b1-1"},
			parityCompactionObs{id: "obs-b1-2"},
		}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneCompactionPreservers, "b1", []compaction.Preserver{
			parityCompactionPreserver{id: "pres-b1-1"},
		})
	}, nil)
	b2 := testkit.FeatureBundle(t, "b2", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneCompactionObservers, "b2", []compaction.Observer{
			parityCompactionObs{id: "obs-b2-1"},
		}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneCompactionPreservers, "b2", []compaction.Preserver{
			parityCompactionPreserver{id: "pres-b2-1"},
			parityCompactionPreserver{id: "pres-b2-2"},
		})
	}, nil)

	gen, err := featurebundle.MergeBundlesGenerated(b1, b2)
	require.NoError(t, err)

	// 1. Verify Frozen plane contents preserve registration order
	frozenObs := aipfeature.Get(gen.Frozen, aipfeature.PlaneCompactionObservers)
	require.Len(t, frozenObs, 3)
	obs0, ok := frozenObs[0].(parityCompactionObs)
	require.True(t, ok)
	assert.Equal(t, "obs-b1-1", obs0.id)
	obs1, ok := frozenObs[1].(parityCompactionObs)
	require.True(t, ok)
	assert.Equal(t, "obs-b1-2", obs1.id)
	obs2, ok := frozenObs[2].(parityCompactionObs)
	require.True(t, ok)
	assert.Equal(t, "obs-b2-1", obs2.id)

	frozenPres := aipfeature.Get(gen.Frozen, aipfeature.PlaneCompactionPreservers)
	require.Len(t, frozenPres, 3)
	assert.Equal(t, "pres-b1-1", frozenPres[0].ID())
	assert.Equal(t, "pres-b2-1", frozenPres[1].ID())
	assert.Equal(t, "pres-b2-2", frozenPres[2].ID())

	// 2. Verify SnapshotOptions materializes exact frozen order
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		FeaturePlanes: gen.Frozen,
	})

	snapObs := snap.CompactionObservers()
	require.Len(t, snapObs, 3)
	snapObs0, ok := snapObs[0].(parityCompactionObs)
	require.True(t, ok)
	assert.Equal(t, "obs-b1-1", snapObs0.id)
	snapObs1, ok := snapObs[1].(parityCompactionObs)
	require.True(t, ok)
	assert.Equal(t, "obs-b1-2", snapObs1.id)
	snapObs2, ok := snapObs[2].(parityCompactionObs)
	require.True(t, ok)
	assert.Equal(t, "obs-b2-1", snapObs2.id)

	snapPres := snap.CompactionPreservers()
	require.Len(t, snapPres, 3)
	assert.Equal(t, "pres-b1-1", snapPres[0].ID())
	assert.Equal(t, "pres-b2-1", snapPres[1].ID())
	assert.Equal(t, "pres-b2-2", snapPres[2].ID())
}

func TestCompactionProjection_NilAndEmptySlicePreservation(t *testing.T) {
	t.Parallel()

	t.Run("nil_uncontributed_slices", func(t *testing.T) {
		t.Parallel()
		var zeroFrozen aipfeature.FrozenPlaneSet
		assert.Nil(t, aipfeature.Get(zeroFrozen, aipfeature.PlaneCompactionObservers))
		assert.Nil(t, aipfeature.Get(zeroFrozen, aipfeature.PlaneCompactionPreservers))
	})

	t.Run("empty_contributions", func(t *testing.T) {
		t.Parallel()
		cs := aipfeature.NewContributionSet()
		err := aipfeature.Contribute(cs, aipfeature.PlaneCompactionObservers, "p1", []compaction.Observer{})
		require.NoError(t, err)
		err = aipfeature.Contribute(cs, aipfeature.PlaneCompactionPreservers, "p1", []compaction.Preserver{})
		require.NoError(t, err)

		frozen := cs.Freeze()
		obs := aipfeature.Get(frozen, aipfeature.PlaneCompactionObservers)
		pres := aipfeature.Get(frozen, aipfeature.PlaneCompactionPreservers)
		assert.NotNil(t, obs)
		assert.Empty(t, obs)
		assert.NotNil(t, pres)
		assert.Empty(t, pres)
	})

	t.Run("literal_and_boxed_typed_nil_elements_exact_historical_semantics", func(t *testing.T) {
		t.Parallel()

		// Literal nil preserver rejected by Validate (p == nil)
		cs1 := aipfeature.NewContributionSet()
		err1 := aipfeature.Contribute(cs1, aipfeature.PlaneCompactionPreservers, "p1", []compaction.Preserver{nil})
		require.Error(t, err1, "literal nil preserver must fail validation")
		assert.Contains(t, err1.Error(), "CompactionPreservers[0] must not be nil")

		// Boxed typed-nil preserver accepted by Validate (p != nil in Go interface)
		var typedNilPres *parityCompactionPreserver
		cs2 := aipfeature.NewContributionSet()
		err2 := aipfeature.Contribute(cs2, aipfeature.PlaneCompactionPreservers, "p1", []compaction.Preserver{typedNilPres})
		require.NoError(t, err2, "boxed typed nil preserver must be accepted")
		fPres := cs2.Freeze()
		require.Len(t, aipfeature.Get(fPres, aipfeature.PlaneCompactionPreservers), 1)

		// Literal nil observer accepted (observers have NilNotApplicable, no Validate)
		cs3 := aipfeature.NewContributionSet()
		err3 := aipfeature.Contribute(cs3, aipfeature.PlaneCompactionObservers, "p1", []compaction.Observer{nil})
		require.NoError(t, err3, "literal nil observer must be accepted")
		fObs1 := cs3.Freeze()
		require.Len(t, aipfeature.Get(fObs1, aipfeature.PlaneCompactionObservers), 1)

		// Boxed typed nil observer accepted
		var typedNilObs *parityCompactionObs
		cs4 := aipfeature.NewContributionSet()
		err4 := aipfeature.Contribute(cs4, aipfeature.PlaneCompactionObservers, "p1", []compaction.Observer{typedNilObs})
		require.NoError(t, err4, "boxed typed nil observer must be accepted")
		fObs2 := cs4.Freeze()
		require.Len(t, aipfeature.Get(fObs2, aipfeature.PlaneCompactionObservers), 1)
	})
}

func TestCompactionProjection_CandidateFeaturePlanesOverlay(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)
	require.NoError(t, reg.RegisterFeature("compaction-feat", func(n yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, "compaction-feat", func(cs *aipfeature.ContributionSet) error {
			if err := aipfeature.Contribute(cs, aipfeature.PlaneCompactionObservers, "compaction-feat", []compaction.Observer{
				parityCompactionObs{id: "base-obs"},
			}); err != nil {
				return err
			}
			return aipfeature.Contribute(cs, aipfeature.PlaneCompactionPreservers, "compaction-feat", []compaction.Preserver{
				parityCompactionPreserver{id: "base-pres"},
			})
		}, nil), nil
	}))

	cfg := obsTestProcessConfig()
	require.NoError(t, config.Validate(cfg))

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg: cfg,
		Log: testkit.DiscardLogger(),
		Opts: &BuildOptions{
			PluginRegistry: reg,
		},
		Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	cand := obsTestCandidateConfig(t, "compaction-feat")

	candBundle := testkit.FeatureBundle(t, "cand", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneCompactionObservers, "cand", []compaction.Observer{
			parityCompactionObs{id: "cand-obs"},
		}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneCompactionPreservers, "cand", []compaction.Preserver{
			parityCompactionPreserver{id: "cand-pres"},
		})
	}, nil)
	candGen, err := featurebundle.MergeBundlesGenerated(candBundle)
	require.NoError(t, err)

	genRuntime, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		CandidateOpts: &BuildOptions{
			FeaturePlanes: candGen.Frozen,
		},
		Compose: func(context.Context, *config.Config, *slog.Logger, httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.NotFoundHandler(), nil
		},
	})
	require.NoError(t, err)
	require.NotNil(t, genRuntime)
	t.Cleanup(func() { _ = genRuntime.Close() })

	bundle, ok := genRuntime.(*GenerationBundle)
	require.True(t, ok)
	exec := bundle.execution.executor
	require.NotNil(t, exec)
	require.NotNil(t, exec.RuntimeSnapshot)

	snap := exec.RuntimeSnapshot
	obs := snap.CompactionObservers()
	require.Len(t, obs, 2)
	baseObs, ok := obs[0].(parityCompactionObs)
	require.True(t, ok)
	assert.Equal(t, "base-obs", baseObs.id)
	candObs, ok := obs[1].(parityCompactionObs)
	require.True(t, ok)
	assert.Equal(t, "cand-obs", candObs.id)

	pres := snap.CompactionPreservers()
	require.Len(t, pres, 2)
	assert.Equal(t, "base-pres", pres[0].ID())
	assert.Equal(t, "cand-pres", pres[1].ID())
}

func TestCompactionProjection_ContinuityGenerationBinder_ReplaceByIdentity(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	require.NoError(t, standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}))

	var compactionNode yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("extractor:\n  enabled: true\n  route: inherit\n"), &compactionNode))

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

	opts := &BuildOptions{PluginRegistry: reg}

	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg:  cfg,
		Log:  slog.Default(),
		Opts: opts,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	// Add an extra preserver via candidate options to check replacement preserves other preservers
	candBundle := testkit.FeatureBundle(t, "cand", func(cs *aipfeature.ContributionSet) error {
		return aipfeature.Contribute(cs, aipfeature.PlaneCompactionPreservers, "cand", []compaction.Preserver{
			parityCompactionPreserver{id: "user-preserver-1"},
		})
	}, nil)
	candGen, err := featurebundle.MergeBundlesGenerated(candBundle)
	require.NoError(t, err)

	gen, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process: ps,
		CandidateOpts: &BuildOptions{
			FeaturePlanes: candGen.Frozen,
		},
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
		},
	})
	require.NoError(t, err)
	require.NotNil(t, gen)
	t.Cleanup(func() { _ = gen.Close() })

	bundle, ok := gen.(*GenerationBundle)
	require.True(t, ok)
	snap := bundle.execution.executor.RuntimeSnapshot
	pres := snap.CompactionPreservers()
	require.Len(t, pres, 2)
	assert.Equal(t, "user-preserver-1", pres[0].ID())
	assert.Equal(t, featurecompaction.ID, pres[1].ID())
}

func TestCompactionProjection_PanicSafeIdentityExtraction(t *testing.T) {
	t.Parallel()

	initBundle := testkit.FeatureBundle(t, "init", func(cs *aipfeature.ContributionSet) error {
		return aipfeature.Contribute(cs, aipfeature.PlaneCompactionPreservers, "init", []compaction.Preserver{
			parityCompactionPreserver{id: "pres-1"},
			parityPanicPreserver{},
		})
	}, nil)

	gen, err := featurebundle.MergeBundlesGenerated(initBundle)
	require.NoError(t, err)

	// Rebinding should safely ignore panicking preserver ID without crashing
	updated, err := gen.BindCompactionPreservers("pres-binder", []compaction.Preserver{
		parityCompactionPreserver{id: "pres-1"},
	})
	require.NoError(t, err)

	pres := aipfeature.Get(updated.Frozen, aipfeature.PlaneCompactionPreservers)
	require.Len(t, pres, 2)
}

func TestCompactionProjection_WholeBinderTransactionFailureRollback(t *testing.T) {
	t.Parallel()

	initBundle := testkit.FeatureBundle(t, "init", func(cs *aipfeature.ContributionSet) error {
		return aipfeature.Contribute(cs, aipfeature.PlaneCompactionPreservers, "init", []compaction.Preserver{
			parityCompactionPreserver{id: "pres-1"},
		})
	}, nil)

	g0, err := featurebundle.MergeBundlesGenerated(initBundle)
	require.NoError(t, err)

	// Operation 1 succeeds
	g1, err := g0.BindCompactionPreservers("binder-1", []compaction.Preserver{
		parityCompactionPreserver{id: "pres-bound"},
	})
	require.NoError(t, err)
	require.Len(t, aipfeature.Get(g1.Frozen, aipfeature.PlaneCompactionPreservers), 2)

	// Operation 2 fails with nil element
	var typedNil *parityCompactionPreserver
	g2, err := g1.BindCompactionPreservers("binder-2", []compaction.Preserver{typedNil})
	require.Error(t, err)
	assert.True(t, g2.Frozen.IsZero())

	// g1 MUST be completely unchanged
	presG1 := aipfeature.Get(g1.Frozen, aipfeature.PlaneCompactionPreservers)
	require.Len(t, presG1, 2)
	assert.Equal(t, "pres-1", presG1[0].ID())
	assert.Equal(t, "pres-bound", presG1[1].ID())
}

func TestCompactionProjection_DefensiveIsolationAndRaceSafety(t *testing.T) {
	t.Parallel()

	gen, err := featurebundle.MergeBundlesGenerated(testkit.FeatureBundle(t, "feat-1", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneCompactionObservers, "feat-1", []compaction.Observer{
			parityCompactionObs{id: "obs-1"},
		}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneCompactionPreservers, "feat-1", []compaction.Preserver{
			parityCompactionPreserver{id: "pres-1"},
		})
	}, nil))
	require.NoError(t, err)

	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		FeaturePlanes: gen.Frozen,
	})

	// Defensive copy check: modifying returned slice does not modify snapshot
	obs := snap.CompactionObservers()
	require.Len(t, obs, 1)
	obs[0] = parityCompactionObs{id: "mutated-obs"}

	obsAgain := snap.CompactionObservers()
	require.Len(t, obsAgain, 1)
	obsAgain0, ok := obsAgain[0].(parityCompactionObs)
	require.True(t, ok)
	assert.Equal(t, "obs-1", obsAgain0.id)

	pres := snap.CompactionPreservers()
	require.Len(t, pres, 1)
	pres[0] = parityCompactionPreserver{id: "mutated-pres"}

	presAgain := snap.CompactionPreservers()
	require.Len(t, presAgain, 1)
	assert.Equal(t, "pres-1", presAgain[0].ID())

	// Race safety check
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				_ = snap.CompactionObservers()
				_ = snap.CompactionPreservers()
			}
		})
	}
	wg.Wait()
}
