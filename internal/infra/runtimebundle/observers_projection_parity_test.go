package runtimebundle

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	coreruntime "github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/internal/testkit/localstubreg"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// --- Stub types for observer projection testing ---

type stubTrafficObs struct {
	id     string
	events *[]string
	mu     *sync.Mutex
}

func (o stubTrafficObs) OnObservation(ctx context.Context, obs traffic.Observation) error {
	if o.mu != nil && o.events != nil {
		o.mu.Lock()
		*o.events = append(*o.events, o.id)
		o.mu.Unlock()
	}
	return nil
}

type stubUsageObs struct {
	id     string
	events *[]string
	mu     *sync.Mutex
}

func (o stubUsageObs) OnUsage(ctx context.Context, ev usage.Event) error {
	if o.mu != nil && o.events != nil {
		o.mu.Lock()
		*o.events = append(*o.events, o.id)
		o.mu.Unlock()
	}
	return nil
}

type stubRawSink struct {
	id     string
	events *[]string
	mu     *sync.Mutex
}

func (s stubRawSink) WriteRaw(ctx context.Context, leg traffic.Leg, meta traffic.CaptureMeta, payload []byte) error {
	if s.mu != nil && s.events != nil {
		s.mu.Lock()
		*s.events = append(*s.events, s.id)
		s.mu.Unlock()
	}
	return nil
}

type stubRedactor struct {
	id     string
	events *[]string
	mu     *sync.Mutex
	prefix string
}

func (r stubRedactor) ID() string { return r.id }

func (r stubRedactor) Redact(ctx context.Context, leg traffic.Leg, meta traffic.CaptureMeta, payload []byte) ([]byte, error) {
	if r.mu != nil && r.events != nil {
		r.mu.Lock()
		*r.events = append(*r.events, r.id)
		o := r.prefix + ":" + string(payload)
		r.mu.Unlock()
		return []byte(o), nil
	}
	return payload, nil
}

type stubStreamObsFactory struct {
	id         string
	ord        int
	openCount  *int
	mu         *sync.Mutex
	createdObs *[]string
}

func (f stubStreamObsFactory) ID() string                        { return f.id }
func (f stubStreamObsFactory) Order() int                        { return f.ord }
func (f stubStreamObsFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (f stubStreamObsFactory) Open(ctx context.Context, meta response.StreamMeta, svc response.Services) (response.StreamObserver, error) {
	if f.mu != nil {
		f.mu.Lock()
		defer f.mu.Unlock()
	}
	if f.openCount != nil {
		*f.openCount++
	}
	if f.createdObs != nil {
		*f.createdObs = append(*f.createdObs, f.id)
	}
	return stubStreamObserver{id: f.id}, nil
}

type stubStreamObserver struct {
	id string
}

func (s stubStreamObserver) Observe(ctx context.Context, ev aipapi.Event) error {
	return nil
}

func (s stubStreamObserver) Finish(ctx context.Context, outcome response.StreamOutcome) error {
	return nil
}

func TestObserversProjection_ParityWithFrozenAndExpectedConfig(t *testing.T) {
	t.Parallel()

	b1 := testkit.FeatureBundle(t, "b1", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, "b1", []traffic.Observer{stubTrafficObs{id: "to-1"}}); err != nil {
			return err
		}
		if err := aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, "b1", []usage.Observer{stubUsageObs{id: "uo-1"}}); err != nil {
			return err
		}
		if err := aipfeature.Contribute(cs, aipfeature.PlaneRawCaptureSinks, "b1", []traffic.RawCaptureSink{stubRawSink{id: "raw-1"}}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneTrafficRedactors, "b1", []traffic.Redactor{stubRedactor{id: "red-1", prefix: "r1"}})
	}, nil)

	b2 := testkit.FeatureBundle(t, "b2", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, "b2", []traffic.Observer{stubTrafficObs{id: "to-2"}}); err != nil {
			return err
		}
		if err := aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, "b2", []usage.Observer{stubUsageObs{id: "uo-2"}}); err != nil {
			return err
		}
		if err := aipfeature.Contribute(cs, aipfeature.PlaneRawCaptureSinks, "b2", []traffic.RawCaptureSink{stubRawSink{id: "raw-2"}}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneTrafficRedactors, "b2", []traffic.Redactor{stubRedactor{id: "red-2", prefix: "r2"}})
	}, nil)

	gen, err := featurebundle.MergeBundlesGenerated(b1, b2)
	require.NoError(t, err)

	to := aipfeature.Get(gen.Frozen, aipfeature.PlaneTrafficObservers)
	uo := aipfeature.Get(gen.Frozen, aipfeature.PlaneUsageObservers)
	raw := aipfeature.Get(gen.Frozen, aipfeature.PlaneRawCaptureSinks)
	red := aipfeature.Get(gen.Frozen, aipfeature.PlaneTrafficRedactors)

	// Verify counts and identities
	require.Len(t, to, 2)
	to0, ok0 := to[0].(stubTrafficObs)
	to1, ok1 := to[1].(stubTrafficObs)
	require.True(t, ok0)
	require.True(t, ok1)
	assert.Equal(t, "to-1", to0.id)
	assert.Equal(t, "to-2", to1.id)

	require.Len(t, uo, 2)
	uo0, ok0 := uo[0].(stubUsageObs)
	uo1, ok1 := uo[1].(stubUsageObs)
	require.True(t, ok0)
	require.True(t, ok1)
	assert.Equal(t, "uo-1", uo0.id)
	assert.Equal(t, "uo-2", uo1.id)

	require.Len(t, raw, 2)
	raw0, ok0 := raw[0].(stubRawSink)
	raw1, ok1 := raw[1].(stubRawSink)
	require.True(t, ok0)
	require.True(t, ok1)
	assert.Equal(t, "raw-1", raw0.id)
	assert.Equal(t, "raw-2", raw1.id)

	require.Len(t, red, 2)
	assert.Equal(t, "red-1", red[0].ID())
	assert.Equal(t, "red-2", red[1].ID())
}

func TestObserversProjection_HostInjectionOrdering(t *testing.T) {
	t.Parallel()

	cs := aipfeature.NewContributionSet()
	require.NoError(t, featurebundle.ContributeBundle(cs, "feat-plugin", testkit.FeatureBundle(t, "feat-plugin", func(csFeat *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(csFeat, aipfeature.PlaneTrafficObservers, "feat-plugin", []traffic.Observer{
			stubTrafficObs{id: "feat-to-1"},
			stubTrafficObs{id: "feat-to-2"},
		}); err != nil {
			return err
		}
		return aipfeature.Contribute(csFeat, aipfeature.PlaneUsageObservers, "feat-plugin", []usage.Observer{
			stubUsageObs{id: "feat-uo-1"},
		})
	}, nil)))
	require.NoError(t, featurebundle.ContributeHost(cs, featurebundle.HostContributions{
		TrafficObservers: []traffic.Observer{
			stubTrafficObs{id: "host-to-1"},
			stubTrafficObs{id: "host-to-2"},
		},
		UsageObservers: []usage.Observer{
			stubUsageObs{id: "host-uo-1"},
		},
	}))

	gen := featurebundle.GeneratedMergeSurface{Frozen: cs.Freeze()}

	to := aipfeature.Get(gen.Frozen, aipfeature.PlaneTrafficObservers)
	require.Len(t, to, 4)
	to0, ok0 := to[0].(stubTrafficObs)
	to1, ok1 := to[1].(stubTrafficObs)
	to2, ok2 := to[2].(stubTrafficObs)
	to3, ok3 := to[3].(stubTrafficObs)
	require.True(t, ok0 && ok1 && ok2 && ok3)
	assert.Equal(t, "feat-to-1", to0.id)
	assert.Equal(t, "feat-to-2", to1.id)
	assert.Equal(t, "host-to-1", to2.id)
	assert.Equal(t, "host-to-2", to3.id)

	uo := aipfeature.Get(gen.Frozen, aipfeature.PlaneUsageObservers)
	require.Len(t, uo, 2)
	uo0, ok0 := uo[0].(stubUsageObs)
	uo1, ok1 := uo[1].(stubUsageObs)
	require.True(t, ok0 && ok1)
	assert.Equal(t, "feat-uo-1", uo0.id)
	assert.Equal(t, "host-uo-1", uo1.id)
}

func TestObserversProjection_ThreeSourceOrdering(t *testing.T) {
	t.Parallel()

	cs := aipfeature.NewContributionSet()
	require.NoError(t, featurebundle.ContributeBundle(cs, "feat-plugin", testkit.FeatureBundle(t, "feat-plugin", func(csFeat *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(csFeat, aipfeature.PlaneTrafficObservers, "feat-plugin", []traffic.Observer{
			stubTrafficObs{id: "feat-to-1"},
		}); err != nil {
			return err
		}
		return aipfeature.Contribute(csFeat, aipfeature.PlaneUsageObservers, "feat-plugin", []usage.Observer{
			stubUsageObs{id: "feat-uo-1"},
		})
	}, nil)))
	require.NoError(t, featurebundle.ContributeHost(cs, featurebundle.HostContributions{
		TrafficObservers: []traffic.Observer{
			stubTrafficObs{id: "host-to-1"},
		},
		UsageObservers: []usage.Observer{
			stubUsageObs{id: "host-uo-1"},
		},
	}))
	require.NoError(t, featurebundle.ContributeBundle(cs, "candidate-extra", testkit.FeatureBundle(t, "candidate-extra", func(csCand *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(csCand, aipfeature.PlaneTrafficObservers, "candidate-extra", []traffic.Observer{
			stubTrafficObs{id: "cand-to-1"},
		}); err != nil {
			return err
		}
		return aipfeature.Contribute(csCand, aipfeature.PlaneUsageObservers, "candidate-extra", []usage.Observer{
			stubUsageObs{id: "cand-uo-1"},
		})
	}, nil)))

	gen := featurebundle.GeneratedMergeSurface{Frozen: cs.Freeze()}

	to := aipfeature.Get(gen.Frozen, aipfeature.PlaneTrafficObservers)
	require.Len(t, to, 3)
	to0, ok0 := to[0].(stubTrafficObs)
	to1, ok1 := to[1].(stubTrafficObs)
	to2, ok2 := to[2].(stubTrafficObs)
	require.True(t, ok0 && ok1 && ok2)
	assert.Equal(t, "feat-to-1", to0.id)
	assert.Equal(t, "host-to-1", to1.id)
	assert.Equal(t, "cand-to-1", to2.id)

	uo := aipfeature.Get(gen.Frozen, aipfeature.PlaneUsageObservers)
	require.Len(t, uo, 3)
	uo0, ok0 := uo[0].(stubUsageObs)
	uo1, ok1 := uo[1].(stubUsageObs)
	uo2, ok2 := uo[2].(stubUsageObs)
	require.True(t, ok0 && ok1 && ok2)
	assert.Equal(t, "feat-uo-1", uo0.id)
	assert.Equal(t, "host-uo-1", uo1.id)
	assert.Equal(t, "cand-uo-1", uo2.id)
}

func TestObserversProjection_ExactNilAndEmptySemantics(t *testing.T) {
	t.Parallel()

	t.Run("zero_generated_surface_projects_nil_slices", func(t *testing.T) {
		t.Parallel()
		var gen featurebundle.GeneratedMergeSurface
		assert.Nil(t, aipfeature.Get(gen.Frozen, aipfeature.PlaneTrafficObservers))
		assert.Nil(t, aipfeature.Get(gen.Frozen, aipfeature.PlaneUsageObservers))
		assert.Nil(t, aipfeature.Get(gen.Frozen, aipfeature.PlaneRawCaptureSinks))
		assert.Nil(t, aipfeature.Get(gen.Frozen, aipfeature.PlaneTrafficRedactors))
	})

	t.Run("empty_explicit_slices_project_empty_slices", func(t *testing.T) {
		t.Parallel()
		gen, err := featurebundle.MergeBundlesGenerated(testkit.FeatureBundle(t, "empty", func(cs *aipfeature.ContributionSet) error {
			if err := aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, "empty", []traffic.Observer{}); err != nil {
				return err
			}
			if err := aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, "empty", []usage.Observer{}); err != nil {
				return err
			}
			if err := aipfeature.Contribute(cs, aipfeature.PlaneRawCaptureSinks, "empty", []traffic.RawCaptureSink{}); err != nil {
				return err
			}
			return aipfeature.Contribute(cs, aipfeature.PlaneTrafficRedactors, "empty", []traffic.Redactor{})
		}, nil))
		require.NoError(t, err)

		to := aipfeature.Get(gen.Frozen, aipfeature.PlaneTrafficObservers)
		assert.NotNil(t, to)
		assert.Empty(t, to)

		uo := aipfeature.Get(gen.Frozen, aipfeature.PlaneUsageObservers)
		assert.NotNil(t, uo)
		assert.Empty(t, uo)

		rcs := aipfeature.Get(gen.Frozen, aipfeature.PlaneRawCaptureSinks)
		assert.NotNil(t, rcs)
		assert.Empty(t, rcs)

		tr := aipfeature.Get(gen.Frozen, aipfeature.PlaneTrafficRedactors)
		assert.NotNil(t, tr)
		assert.Empty(t, tr)
	})
}

func TestObserversProjection_BackingArrayIsolation(t *testing.T) {
	t.Parallel()

	gen, err := featurebundle.MergeBundlesGenerated(testkit.FeatureBundle(t, "orig", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, "orig", []traffic.Observer{stubTrafficObs{id: "orig-to"}}); err != nil {
			return err
		}
		if err := aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, "orig", []usage.Observer{stubUsageObs{id: "orig-uo"}}); err != nil {
			return err
		}
		if err := aipfeature.Contribute(cs, aipfeature.PlaneRawCaptureSinks, "orig", []traffic.RawCaptureSink{stubRawSink{id: "orig-raw"}}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneTrafficRedactors, "orig", []traffic.Redactor{stubRedactor{id: "orig-red"}})
	}, nil))
	require.NoError(t, err)

	to := aipfeature.Get(gen.Frozen, aipfeature.PlaneTrafficObservers)
	uo := aipfeature.Get(gen.Frozen, aipfeature.PlaneUsageObservers)
	raw := aipfeature.Get(gen.Frozen, aipfeature.PlaneRawCaptureSinks)
	red := aipfeature.Get(gen.Frozen, aipfeature.PlaneTrafficRedactors)

	// Mutate retrieved slices
	to[0] = stubTrafficObs{id: "mutated-to"}
	uo[0] = stubUsageObs{id: "mutated-uo"}
	raw[0] = stubRawSink{id: "mutated-raw"}
	red[0] = stubRedactor{id: "mutated-red"}

	// Re-reading from Frozen must return untouched original values
	toFrozen := aipfeature.Get(gen.Frozen, aipfeature.PlaneTrafficObservers)
	require.Len(t, toFrozen, 1)
	to0, ok0 := toFrozen[0].(stubTrafficObs)
	require.True(t, ok0)
	assert.Equal(t, "orig-to", to0.id)

	uoFrozen := aipfeature.Get(gen.Frozen, aipfeature.PlaneUsageObservers)
	require.Len(t, uoFrozen, 1)
	uo0, ok0 := uoFrozen[0].(stubUsageObs)
	require.True(t, ok0)
	assert.Equal(t, "orig-uo", uo0.id)

	rawFrozen := aipfeature.Get(gen.Frozen, aipfeature.PlaneRawCaptureSinks)
	require.Len(t, rawFrozen, 1)
	raw0, ok0 := rawFrozen[0].(stubRawSink)
	require.True(t, ok0)
	assert.Equal(t, "orig-raw", raw0.id)

	redFrozen := aipfeature.Get(gen.Frozen, aipfeature.PlaneTrafficRedactors)
	require.Len(t, redFrozen, 1)
	assert.Equal(t, "orig-red", redFrozen[0].ID())
}

func TestObserversProjection_EndToEndSnapshotDispatch(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var trafficEvents, usageEvents, rawEvents, redactorEvents []string

	b1 := testkit.FeatureBundle(t, "b1", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, "b1", []traffic.Observer{stubTrafficObs{id: "to-1", events: &trafficEvents, mu: &mu}}); err != nil {
			return err
		}
		if err := aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, "b1", []usage.Observer{stubUsageObs{id: "uo-1", events: &usageEvents, mu: &mu}}); err != nil {
			return err
		}
		if err := aipfeature.Contribute(cs, aipfeature.PlaneRawCaptureSinks, "b1", []traffic.RawCaptureSink{stubRawSink{id: "raw-1", events: &rawEvents, mu: &mu}}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneTrafficRedactors, "b1", []traffic.Redactor{stubRedactor{id: "red-1", prefix: "r1", events: &redactorEvents, mu: &mu}})
	}, nil)

	b2 := testkit.FeatureBundle(t, "b2", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, "b2", []traffic.Observer{stubTrafficObs{id: "to-2", events: &trafficEvents, mu: &mu}}); err != nil {
			return err
		}
		if err := aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, "b2", []usage.Observer{stubUsageObs{id: "uo-2", events: &usageEvents, mu: &mu}}); err != nil {
			return err
		}
		if err := aipfeature.Contribute(cs, aipfeature.PlaneRawCaptureSinks, "b2", []traffic.RawCaptureSink{stubRawSink{id: "raw-2", events: &rawEvents, mu: &mu}}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneTrafficRedactors, "b2", []traffic.Redactor{stubRedactor{id: "red-2", prefix: "r2", events: &redactorEvents, mu: &mu}})
	}, nil)

	cs := aipfeature.NewContributionSet()
	require.NoError(t, featurebundle.ContributeBundle(cs, "b1", b1))
	require.NoError(t, featurebundle.ContributeBundle(cs, "b2", b2))
	require.NoError(t, featurebundle.ContributeHost(cs, featurebundle.HostContributions{
		TrafficObservers: []traffic.Observer{
			stubTrafficObs{id: "host-to", events: &trafficEvents, mu: &mu},
		},
		UsageObservers: []usage.Observer{
			stubUsageObs{id: "host-uo", events: &usageEvents, mu: &mu},
		},
	}))

	gen := featurebundle.GeneratedMergeSurface{Frozen: cs.Freeze()}

	bus := hooks.New(hooks.Config{})
	var err error
	snap := buildRuntimeSnapshot(bus, &config.Config{}, &BuildOptions{FeaturePlanes: gen.Frozen}, time.Now, nil, &controlPlaneRuntime{}, nil, extensions.SecretGuardPlane{}, nil)
	require.NotNil(t, snap)

	// 1. Dispatch TrafficObserver
	obs := snap.TrafficObserver()
	require.NotNil(t, obs)
	err = obs.OnObservation(context.Background(), traffic.Observation{})
	require.NoError(t, err)
	assert.Equal(t, []string{"to-1", "to-2", "host-to"}, trafficEvents)

	// 2. Dispatch UsageObserver
	uobs := snap.UsageObserver()
	require.NotNil(t, uobs)
	err = uobs.OnUsage(context.Background(), usage.Event{})
	require.NoError(t, err)
	assert.Equal(t, []string{"uo-1", "uo-2", "host-uo"}, usageEvents)

	// 3. Dispatch RawCapture
	raw := snap.RawCapture()
	require.NotNil(t, raw)
	err = raw.WriteRaw(context.Background(), traffic.LegCTP, traffic.CaptureMeta{}, []byte("test"))
	require.NoError(t, err)
	assert.Equal(t, []string{"raw-1", "raw-2"}, rawEvents)

	// 4. Dispatch TrafficRedactors
	redactors := snap.TrafficRedactors()
	require.Len(t, redactors, 2)
	assert.Equal(t, "red-1", redactors[0].ID())
	assert.Equal(t, "red-2", redactors[1].ID())

	// 5. TrafficPortBundle returns unified triple
	portBundle := snap.TrafficPortBundle()
	assert.NotNil(t, portBundle.Obs)
	assert.NotNil(t, portBundle.Raw)
	assert.Len(t, portBundle.Red, 2)
}

func TestObserversProjection_PluginRegistryLifecyclePreserved(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	toFacID := "test.to"
	uoFacID := "test.uo"

	err := reg.RegisterFeature(toFacID, func(n yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, toFacID, func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, toFacID, []traffic.Observer{
				stubTrafficObs{id: "reg-to"},
			})
		}, nil), nil
	})
	require.NoError(t, err)

	err = reg.RegisterFeature(uoFacID, func(n yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, uoFacID, func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, uoFacID, []usage.Observer{
				stubUsageObs{id: "reg-uo"},
			})
		}, nil), nil
	})
	require.NoError(t, err)

	var cfgNode yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte("{}"), &cfgNode))

	// Both plugins enabled
	gen, err := featurebundle.MergeFeatureSurfaceGenerated(reg, []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: "inst-to", FactoryKind: toFacID, Enabled: true, Config: aipsdk.ConfigPayload{Node: cfgNode}},
		{Kind: aipsdk.PluginKindFeature, ID: "inst-uo", FactoryKind: uoFacID, Enabled: true, Config: aipsdk.ConfigPayload{Node: cfgNode}},
	})
	require.NoError(t, err)

	to := aipfeature.Get(gen.Frozen, aipfeature.PlaneTrafficObservers)
	uo := aipfeature.Get(gen.Frozen, aipfeature.PlaneUsageObservers)
	require.Len(t, to, 1)
	require.Len(t, uo, 1)
	to0, ok0 := to[0].(stubTrafficObs)
	uo0, ok1 := uo[0].(stubUsageObs)
	require.True(t, ok0)
	require.True(t, ok1)
	assert.Equal(t, "reg-to", to0.id)
	assert.Equal(t, "reg-uo", uo0.id)

	// Disabled plugin contributes nothing
	genDisabled, err := featurebundle.MergeFeatureSurfaceGenerated(reg, []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: "inst-to", FactoryKind: toFacID, Enabled: true, Config: aipsdk.ConfigPayload{Node: cfgNode}},
		{Kind: aipsdk.PluginKindFeature, ID: "inst-uo", FactoryKind: uoFacID, Enabled: false, Config: aipsdk.ConfigPayload{Node: cfgNode}},
	})
	require.NoError(t, err)

	toDisabled := aipfeature.Get(genDisabled.Frozen, aipfeature.PlaneTrafficObservers)
	uoDisabled := aipfeature.Get(genDisabled.Frozen, aipfeature.PlaneUsageObservers)
	require.Len(t, toDisabled, 1)
	require.Empty(t, uoDisabled, "disabled usage plugin must not contribute to usage observers")
}

func obsTestProcessConfig() *config.Config {
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes:    1024,
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 4096,
		},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
	}
	_ = config.Validate(cfg)
	return cfg
}

func obsTestCandidateConfig(t *testing.T, featurePlugins ...string) *config.Config {
	t.Helper()
	var yamlNode yaml.Node
	_ = yaml.Unmarshal([]byte("text: ok\ninput_tokens: 1\noutput_tokens: 1\n"), &yamlNode)

	features := make([]config.PluginConfig, 0, len(featurePlugins))
	for _, f := range featurePlugins {
		features = append(features, config.PluginConfig{
			ID:      f,
			Enabled: true,
		})
	}

	cfg := &config.Config{
		Routing: config.RoutingConfig{
			MaxAttempts:  3,
			DefaultRoute: "stub-backend:default",
		},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes:    1024,
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 4096,
		},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins: config.PluginsConfig{
			Features:  features,
			Frontends: []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
			Backends: []config.PluginConfig{{
				Kind:    "local-stub",
				ID:      "stub-backend",
				Enabled: true,
				Config:  yamlNode,
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate candidate: %v", err)
	}
	return cfg
}

func obsTestFactoryCatalog(t *testing.T) *pluginreg.Registry {
	t.Helper()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	if err := localstubreg.RegisterInProcess(reg); err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestCompileGeneration_ObserversRealIntegration(t *testing.T) {
	t.Parallel()

	t.Run("feature_then_host_order", func(t *testing.T) {
		t.Parallel()
		var events []string
		var mu sync.Mutex

		reg := obsTestFactoryCatalog(t)
		err := reg.RegisterFeature("obs-feature", func(n yaml.Node) (aipfeature.FeatureBundle, error) {
			return testkit.FeatureBundle(t, "obs-feature", func(cs *aipfeature.ContributionSet) error {
				if err := aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, "obs-feature", []traffic.Observer{
					stubTrafficObs{id: "feat-to", events: &events, mu: &mu},
				}); err != nil {
					return err
				}
				return aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, "obs-feature", []usage.Observer{
					stubUsageObs{id: "feat-uo", events: &events, mu: &mu},
				})
			}, nil), nil
		})
		require.NoError(t, err)

		cfg := obsTestProcessConfig()
		require.NoError(t, config.Validate(cfg))

		ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
			Cfg: cfg,
			Log: testkit.DiscardLogger(),
			Opts: &BuildOptions{
				PluginRegistry: reg,
				Production: ProductionOptions{
					TrafficObservers: []traffic.Observer{
						stubTrafficObs{id: "host-to", events: &events, mu: &mu},
					},
					UsageObservers: []usage.Observer{
						stubUsageObs{id: "host-uo", events: &events, mu: &mu},
					},
				},
			},
			Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = ps.Close() })

		cand := obsTestCandidateConfig(t, "obs-feature")

		bundle, err := CompileGeneration(context.Background(), GenerationCompileInput{
			Process:   ps,
			Candidate: cand,
			Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
				obs := in.Frontends.TrafficPorts.Obs
				require.NotNil(t, obs)
				require.NoError(t, obs.OnObservation(ctx, traffic.Observation{Leg: traffic.LegCTP}))
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = bundle.Close() })

		require.Equal(t, []string{"feat-to", "host-to"}, events)
	})

	t.Run("feature_process_host_candidate_extension_three_source_order", func(t *testing.T) {
		t.Parallel()
		var trafficEvents []string
		var usageEvents []string
		var mu sync.Mutex

		reg := obsTestFactoryCatalog(t)
		require.NoError(t, reg.RegisterFeature("three-source-feature", func(n yaml.Node) (aipfeature.FeatureBundle, error) {
			return testkit.FeatureBundle(t, "three-source-feature", func(cs *aipfeature.ContributionSet) error {
				if err := aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, "three-source-feature", []traffic.Observer{
					stubTrafficObs{id: "feat-to", events: &trafficEvents, mu: &mu},
				}); err != nil {
					return err
				}
				return aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, "three-source-feature", []usage.Observer{
					stubUsageObs{id: "feat-uo", events: &usageEvents, mu: &mu},
				})
			}, nil), nil
		}))
		require.NoError(t, reg.RegisterFeature("three-source-cand", func(n yaml.Node) (aipfeature.FeatureBundle, error) {
			return testkit.FeatureBundle(t, "three-source-cand", func(cs *aipfeature.ContributionSet) error {
				if err := aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, "three-source-cand", []traffic.Observer{
					stubTrafficObs{id: "cand-to", events: &trafficEvents, mu: &mu},
				}); err != nil {
					return err
				}
				return aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, "three-source-cand", []usage.Observer{
					stubUsageObs{id: "cand-uo", events: &usageEvents, mu: &mu},
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
				Production: ProductionOptions{
					TrafficObservers: []traffic.Observer{
						stubTrafficObs{id: "host-to", events: &trafficEvents, mu: &mu},
					},
					UsageObservers: []usage.Observer{
						stubUsageObs{id: "host-uo", events: &usageEvents, mu: &mu},
					},
				},
			},
			Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = ps.Close() })

		cand := obsTestCandidateConfig(t, "three-source-feature")
		cand.Plugins.Features = append(cand.Plugins.Features, config.PluginConfig{
			ID: "three-source-cand", Enabled: true,
		})

		bundle, err := CompileGeneration(context.Background(), GenerationCompileInput{
			Process:   ps,
			Candidate: cand,
			Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
				obs := in.Frontends.TrafficPorts.Obs
				require.NotNil(t, obs)
				require.NoError(t, obs.OnObservation(ctx, traffic.Observation{Leg: traffic.LegCTP}))

				uobs := composerExecutor(t, in).RuntimeSnapshot.UsageObserver()
				require.NotNil(t, uobs)
				require.NoError(t, uobs.OnUsage(ctx, usage.Event{}))

				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = bundle.Close() })

		require.Equal(t, []string{"feat-to", "cand-to", "host-to"}, trafficEvents, "traffic observers must execute in feature -> candidate -> host order")
		require.Equal(t, []string{"feat-uo", "cand-uo", "host-uo"}, usageEvents, "usage observers must execute in feature -> candidate -> host order")
	})

	t.Run("candidate_production_options_ignored", func(t *testing.T) {
		t.Parallel()
		var events []string
		var mu sync.Mutex

		cfg := obsTestProcessConfig()
		require.NoError(t, config.Validate(cfg))

		ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
			Cfg: cfg,
			Log: testkit.DiscardLogger(),
			Opts: &BuildOptions{
				PluginRegistry: obsTestFactoryCatalog(t),
				Production: ProductionOptions{
					TrafficObservers: []traffic.Observer{
						stubTrafficObs{id: "process-host-to", events: &events, mu: &mu},
					},
				},
			},
			Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = ps.Close() })

		cand := obsTestCandidateConfig(t)

		bundle, err := CompileGeneration(context.Background(), GenerationCompileInput{
			Process:   ps,
			Candidate: cand,
			CandidateOpts: &BuildOptions{
				Production: ProductionOptions{
					TrafficObservers: []traffic.Observer{
						stubTrafficObs{id: "candidate-prod-to-SHOULD-BE-IGNORED", events: &events, mu: &mu},
					},
				},
			},
			Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
				obs := in.Frontends.TrafficPorts.Obs
				require.NotNil(t, obs)
				require.NoError(t, obs.OnObservation(ctx, traffic.Observation{Leg: traffic.LegCTP}))
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = bundle.Close() })

		require.Equal(t, []string{"process-host-to"}, events, "candidate Production options must be ignored")
	})

	t.Run("candidate_extensions_raw_and_redactor_preserved", func(t *testing.T) {
		t.Parallel()
		var rawEvents, redEvents []string
		var mu sync.Mutex

		reg := obsTestFactoryCatalog(t)
		require.NoError(t, reg.RegisterFeature("feature-raw-red", func(n yaml.Node) (aipfeature.FeatureBundle, error) {
			return testkit.FeatureBundle(t, "feature-raw-red", func(cs *aipfeature.ContributionSet) error {
				if err := aipfeature.Contribute(cs, aipfeature.PlaneRawCaptureSinks, "feature-raw-red", []traffic.RawCaptureSink{
					stubRawSink{id: "feat-raw", events: &rawEvents, mu: &mu},
				}); err != nil {
					return err
				}
				return aipfeature.Contribute(cs, aipfeature.PlaneTrafficRedactors, "feature-raw-red", []traffic.Redactor{
					stubRedactor{id: "1-feat-red", prefix: "feat", events: &redEvents, mu: &mu},
				})
			}, nil), nil
		}))
		require.NoError(t, reg.RegisterFeature("cand-raw-red", func(n yaml.Node) (aipfeature.FeatureBundle, error) {
			return testkit.FeatureBundle(t, "cand-raw-red", func(cs *aipfeature.ContributionSet) error {
				if err := aipfeature.Contribute(cs, aipfeature.PlaneRawCaptureSinks, "cand-raw-red", []traffic.RawCaptureSink{
					stubRawSink{id: "cand-raw", events: &rawEvents, mu: &mu},
				}); err != nil {
					return err
				}
				return aipfeature.Contribute(cs, aipfeature.PlaneTrafficRedactors, "cand-raw-red", []traffic.Redactor{
					stubRedactor{id: "2-cand-red", prefix: "cand", events: &redEvents, mu: &mu},
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

		cand := obsTestCandidateConfig(t, "feature-raw-red")
		cand.Plugins.Features = append(cand.Plugins.Features, config.PluginConfig{
			ID: "cand-raw-red", Enabled: true,
		})

		bundle, err := CompileGeneration(context.Background(), GenerationCompileInput{
			Process:   ps,
			Candidate: cand,
			Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
				raw := in.Frontends.TrafficPorts.Raw
				require.NotNil(t, raw)
				require.NoError(t, raw.WriteRaw(ctx, traffic.LegCTP, traffic.CaptureMeta{}, []byte("data")))

				reds := in.Frontends.TrafficPorts.Red
				require.Len(t, reds, 2)
				assert.Equal(t, "1-feat-red", reds[0].ID())
				assert.Equal(t, "2-cand-red", reds[1].ID())
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = bundle.Close() })

		require.Equal(t, []string{"feat-raw", "cand-raw"}, rawEvents)
	})

	t.Run("spare_capacity_inputs_isolated", func(t *testing.T) {
		t.Parallel()
		rawSlice := make([]traffic.Observer, 1, 10)
		rawSlice[0] = stubTrafficObs{id: "orig-to"}

		cfg := obsTestProcessConfig()
		require.NoError(t, config.Validate(cfg))

		ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
			Cfg: cfg,
			Log: testkit.DiscardLogger(),
			Opts: &BuildOptions{
				PluginRegistry: obsTestFactoryCatalog(t),
				Production: ProductionOptions{
					TrafficObservers: rawSlice,
				},
			},
			Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = ps.Close() })

		cand := obsTestCandidateConfig(t)

		bundle, err := CompileGeneration(context.Background(), GenerationCompileInput{
			Process:   ps,
			Candidate: cand,
			Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = bundle.Close() })

		require.Len(t, rawSlice, 1)
		to0, ok := rawSlice[0].(stubTrafficObs)
		require.True(t, ok)
		assert.Equal(t, "orig-to", to0.id)
	})

	t.Run("nil_and_empty_exact", func(t *testing.T) {
		t.Parallel()
		cfg := obsTestProcessConfig()
		require.NoError(t, config.Validate(cfg))

		ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
			Cfg: cfg,
			Log: testkit.DiscardLogger(),
			Opts: &BuildOptions{
				PluginRegistry: obsTestFactoryCatalog(t),
			},
			Tracing: ProcessTracing{Shutdown: func(context.Context) error { return nil }},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = ps.Close() })

		cand := obsTestCandidateConfig(t)

		bundle, err := CompileGeneration(context.Background(), GenerationCompileInput{
			Process:   ps,
			Candidate: cand,
			Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
				assert.Equal(t, traffic.NoopObserver{}, in.Frontends.TrafficPorts.Obs)
				assert.Equal(t, traffic.DisabledRawCapture{}, in.Frontends.TrafficPorts.Raw)
				assert.Nil(t, in.Frontends.TrafficPorts.Red)
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = bundle.Close() })
	})
}

func TestStreamObserverFactoriesProjection_ParityWithFrozenAndExpectedConfig(t *testing.T) {
	t.Parallel()

	b1 := testkit.FeatureBundle(t, "b1", func(cs *aipfeature.ContributionSet) error {
		return aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "b1", []response.StreamObserverFactory{
			stubStreamObsFactory{id: "so-1"},
		})
	}, nil)
	b2 := testkit.FeatureBundle(t, "b2", func(cs *aipfeature.ContributionSet) error {
		return aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "b2", []response.StreamObserverFactory{
			stubStreamObsFactory{id: "so-2"},
		})
	}, nil)

	gen, err := featurebundle.MergeBundlesGenerated(b1, b2)
	require.NoError(t, err)

	so := aipfeature.Get(gen.Frozen, aipfeature.PlaneStreamObserverFactories)
	require.Len(t, so, 2)
	assert.Equal(t, "so-1", so[0].ID())
	assert.Equal(t, "so-2", so[1].ID())
}

func TestStreamObserverFactoriesProjection_CandidateOverlayOrdering(t *testing.T) {
	t.Parallel()

	reg := obsTestFactoryCatalog(t)
	err := reg.RegisterFeature("feature-so", func(n yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, "feature-so", func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "feature-so", []response.StreamObserverFactory{
				stubStreamObsFactory{id: "1-feat-so-1", ord: 1},
				stubStreamObsFactory{id: "2-feat-so-2", ord: 2},
			})
		}, nil), nil
	})
	require.NoError(t, err)

	err = reg.RegisterFeature("feature-so-cand", func(n yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, "feature-so-cand", func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "feature-so-cand", []response.StreamObserverFactory{
				stubStreamObsFactory{id: "3-cand-so-1", ord: 3},
			})
		}, nil), nil
	})
	require.NoError(t, err)

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

	cand := obsTestCandidateConfig(t, "feature-so")
	cand.Plugins.Features = append(cand.Plugins.Features, config.PluginConfig{
		ID: "feature-so-cand", Enabled: true,
	})
	var snap *extensions.RequestRuntimeSnapshot

	bundle, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			snap = composerExecutor(t, in).RuntimeSnapshot
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Close() })

	require.NotNil(t, snap)

	soFactories := snap.StreamObserverFactories()
	require.Len(t, soFactories, 3)
	assert.Equal(t, "1-feat-so-1", soFactories[0].ID())
	assert.Equal(t, "2-feat-so-2", soFactories[1].ID())
	assert.Equal(t, "3-cand-so-1", soFactories[2].ID())
}

func TestStreamObserverFactoriesProjection_ThreeSourceOrdering(t *testing.T) {
	t.Parallel()

	cs := aipfeature.NewContributionSet()
	require.NoError(t, featurebundle.ContributeBundle(cs, "feat-plugin", testkit.FeatureBundle(t, "feat-plugin", func(csFeat *aipfeature.ContributionSet) error {
		return aipfeature.Contribute(csFeat, aipfeature.PlaneStreamObserverFactories, "feat-plugin", []response.StreamObserverFactory{
			stubStreamObsFactory{id: "feat-so-1"},
		})
	}, nil)))
	require.NoError(t, featurebundle.ContributeBundle(cs, "candidate-extra", testkit.FeatureBundle(t, "candidate-extra", func(csCand *aipfeature.ContributionSet) error {
		return aipfeature.Contribute(csCand, aipfeature.PlaneStreamObserverFactories, "candidate-extra", []response.StreamObserverFactory{
			stubStreamObsFactory{id: "cand-so-1"},
		})
	}, nil)))

	gen := featurebundle.GeneratedMergeSurface{Frozen: cs.Freeze()}

	so := aipfeature.Get(gen.Frozen, aipfeature.PlaneStreamObserverFactories)
	require.Len(t, so, 2)
	assert.Equal(t, "feat-so-1", so[0].ID())
	assert.Equal(t, "cand-so-1", so[1].ID())
}

func TestStreamObserverFactoriesProjection_LazyLifecycleAndInvocation(t *testing.T) {
	t.Parallel()

	var openCount int
	var mu sync.Mutex
	var createdObs []string

	factory := stubStreamObsFactory{
		id:         "lazy-so",
		openCount:  &openCount,
		mu:         &mu,
		createdObs: &createdObs,
	}

	reg := obsTestFactoryCatalog(t)
	err := reg.RegisterFeature("feature-lazy-so", func(n yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, "feature-lazy-so", func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "feature-lazy-so", []response.StreamObserverFactory{
				factory,
			})
		}, nil), nil
	})
	require.NoError(t, err)

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

	cand := obsTestCandidateConfig(t, "feature-lazy-so")
	var snap *extensions.RequestRuntimeSnapshot

	bundle, err := CompileGeneration(context.Background(), GenerationCompileInput{
		Process:   ps,
		Candidate: cand,
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			snap = composerExecutor(t, in).RuntimeSnapshot
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Close() })

	// Prove factories are NOT eagerly invoked or opened during compilation
	mu.Lock()
	require.Equal(t, 0, openCount, "factories must not be opened during CompileGeneration")
	mu.Unlock()

	require.NotNil(t, snap)

	// Accessing snapshot does not invoke Open
	factories := snap.StreamObserverFactories()
	require.Len(t, factories, 1)
	mu.Lock()
	require.Equal(t, 0, openCount, "factories must not be opened on Snapshot() accessor reads")
	mu.Unlock()

	// Open session 1
	sess1 := &extensions.FinalStreamObservationSession{Log: testkit.DiscardLogger()}
	err = sess1.Open(context.Background(), factories, response.StreamMeta{BLegID: "bleg-1"}, response.Services{})
	require.NoError(t, err)
	mu.Lock()
	require.Equal(t, 1, openCount, "factory Open should be invoked exactly once for session 1")
	mu.Unlock()

	// Open session 2 with same factory produces separate observer instance
	sess2 := &extensions.FinalStreamObservationSession{Log: testkit.DiscardLogger()}
	err = sess2.Open(context.Background(), factories, response.StreamMeta{BLegID: "bleg-2"}, response.Services{})
	require.NoError(t, err)
	mu.Lock()
	require.Equal(t, 2, openCount, "factory Open should be invoked for session 2")
	require.Equal(t, []string{"lazy-so", "lazy-so"}, createdObs)
	mu.Unlock()
}

func TestStreamObserverFactoriesProjection_BackingArrayIsolation(t *testing.T) {
	t.Parallel()

	gen, err := featurebundle.MergeBundlesGenerated(testkit.FeatureBundle(t, "orig", func(cs *aipfeature.ContributionSet) error {
		return aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "orig", []response.StreamObserverFactory{
			stubStreamObsFactory{id: "orig-so"},
		})
	}, nil))
	require.NoError(t, err)

	so := aipfeature.Get(gen.Frozen, aipfeature.PlaneStreamObserverFactories)
	require.Len(t, so, 1)

	// Mutate returned slice
	so[0] = stubStreamObsFactory{id: "mutated-so"}

	// Re-reading from Frozen should not reflect mutation
	frozenAgain := aipfeature.Get(gen.Frozen, aipfeature.PlaneStreamObserverFactories)
	require.Len(t, frozenAgain, 1)
	assert.Equal(t, "orig-so", frozenAgain[0].ID(), "FrozenPlaneSet backing array must be isolated from caller mutations")
}

func TestStreamObserverFactoriesProjection_ExactNilAndEmptySemantics(t *testing.T) {
	t.Parallel()

	t.Run("zero_generated_surface_projects_nil_slices", func(t *testing.T) {
		t.Parallel()
		var gen featurebundle.GeneratedMergeSurface
		assert.Nil(t, aipfeature.Get(gen.Frozen, aipfeature.PlaneStreamObserverFactories))
	})

	t.Run("empty_explicit_slices_project_empty_slices", func(t *testing.T) {
		t.Parallel()
		gen, err := featurebundle.MergeBundlesGenerated(testkit.FeatureBundle(t, "empty", func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "empty", []response.StreamObserverFactory{})
		}, nil))
		require.NoError(t, err)

		sof := aipfeature.Get(gen.Frozen, aipfeature.PlaneStreamObserverFactories)
		assert.NotNil(t, sof)
		assert.Empty(t, sof)
	})
}

func TestStreamObserverFactoriesProjection_TypedNilRejection(t *testing.T) {
	t.Parallel()

	cs := aipfeature.NewContributionSet()
	err := aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "test", []response.StreamObserverFactory{nil})
	require.Error(t, err, "Contribute must reject nil StreamObserverFactory")
}

// composerExecutor returns the concrete executor behind a composer input's
// narrow ExecutorView, failing the test when the view is not backed by the
// runtime executor.
func composerExecutor(t *testing.T, in httpcontract.StandardHTTPInput) *coreruntime.Executor {
	t.Helper()
	ex, ok := in.Core.Executor.(*coreruntime.Executor)
	if !ok || ex == nil {
		t.Fatalf("composer executor = %T, want concrete *runtime.Executor", in.Core.Executor)
	}
	return ex
}
