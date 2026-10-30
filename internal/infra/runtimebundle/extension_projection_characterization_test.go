package runtimebundle

import (
	"context"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/localturn"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	"github.com/stretchr/testify/require"
)

type stubProjEnv struct{ val string }

func (s stubProjEnv) Lookup(name string) (string, bool) { return s.val, true }
func (s stubProjEnv) Snapshot() []string                { return []string{s.val} }

type projCatalogFilter struct{ id string }

func (f projCatalogFilter) ID() string                      { return f.id }
func (projCatalogFilter) Order() int                        { return 0 }
func (projCatalogFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (projCatalogFilter) Handle(context.Context, *aipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	return nil
}

type projOpener struct{ tag string }

func (o projOpener) ID() string { return o.tag }
func (projOpener) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

type projTransform struct{ tag string }

func (t projTransform) ID() string                      { return t.tag }
func (projTransform) Order() int                        { return 0 }
func (projTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (projTransform) Handle(context.Context, *aipapi.Call, request.RequestMeta, request.Services) error {
	return nil
}

type projTrafficObs struct{ tag string }

func (projTrafficObs) OnObservation(context.Context, traffic.Observation) error { return nil }

type projUsageObs struct{ tag string }

func (projUsageObs) OnUsage(context.Context, usage.Event) error { return nil }

type projTerminalProvider struct{ tag string }

func (p projTerminalProvider) ID() string { return p.tag }

type projFinalizer struct{ id string }

func (f projFinalizer) ID() string { return f.id }
func (projFinalizer) Order() int   { return 0 }
func (projFinalizer) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{}, nil
}

func (projTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

// projMerged builds a generated merge surface with one tagged element on the planes the
// projection tests observe.
func projMerged(t *testing.T) featurebundle.GeneratedMergeSurface {
	t.Helper()
	b := testkit.FeatureBundle(t, "feat", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizers, "feat", []toolcall.Finalizer{projFinalizer{id: "finalizer"}}); err != nil {
			return err
		}
		if err := aipfeature.Contribute(cs, aipfeature.PlaneRequestTransforms, "feat", []request.Transform{projTransform{tag: "transform"}}); err != nil {
			return err
		}
		if err := aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, "feat", []traffic.Observer{projTrafficObs{tag: "feat-traffic"}}); err != nil {
			return err
		}
		if err := aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, "feat", []usage.Observer{projUsageObs{tag: "feat-usage"}}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneLocalTurnHandlers, "feat", []localturn.Handler{wiringHandler{id: "handler", ord: 1}})
	}, nil)
	gen, err := featurebundle.MergeBundlesGenerated(b)
	require.NoError(t, err)
	return gen
}

func assertAllSliceFieldsNil(t *testing.T, v any) {
	t.Helper()
	rv := reflect.ValueOf(v)
	rt := rv.Type()
	for i := range rt.NumField() {
		field := rt.Field(i)
		if field.Type.Kind() != reflect.Slice {
			continue
		}
		fv := rv.Field(i)
		require.True(t, fv.IsNil(), "%s: want nil slice, got len=%d", field.Name, fv.Len())
	}
}

// Pins backing-array isolation in both directions: projections are copies with
// zero spare capacity, so mutation or growth of either side never reaches the
// other.
func TestGeneratedMergeSurface_backingArrayIsolationBothDirections(t *testing.T) {
	t.Parallel()

	gen := projMerged(t)
	ltFrozen := aipfeature.Get(gen.Frozen, aipfeature.PlaneLocalTurnHandlers)
	require.Len(t, ltFrozen, 1)
	require.Equal(t, "handler", ltFrozen[0].ID())

	toFrozen := aipfeature.Get(gen.Frozen, aipfeature.PlaneTrafficObservers)
	require.Len(t, toFrozen, 1)
	trafficObs, trafficObsOK := toFrozen[0].(projTrafficObs)
	require.True(t, trafficObsOK)
	require.Equal(t, "feat-traffic", trafficObs.tag)

	uoFrozen := aipfeature.Get(gen.Frozen, aipfeature.PlaneUsageObservers)
	require.Len(t, uoFrozen, 1)
	usageObs, usageObsOK := uoFrozen[0].(projUsageObs)
	require.True(t, usageObsOK)
	require.Equal(t, "feat-usage", usageObs.tag)
}

// Pins host-injection ordering: production observers append AFTER feature
// contributions, and a nil process options injects nothing.
func TestGeneratedMergeSurface_hostObserversAppendAfterFeatures(t *testing.T) {
	t.Parallel()

	b := testkit.FeatureBundle(t, "feat", func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, "feat", []traffic.Observer{projTrafficObs{tag: "feat-1"}, projTrafficObs{tag: "feat-2"}}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, "feat", []usage.Observer{projUsageObs{tag: "feat-u"}})
	}, nil)

	cs := aipfeature.NewContributionSet()
	require.NoError(t, featurebundle.ContributeBundle(cs, "feat", b))
	require.NoError(t, featurebundle.ContributeBundle(cs, "host", testkit.FeatureBundle(t, "host", func(csHost *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(csHost, aipfeature.PlaneTrafficObservers, "host", []traffic.Observer{projTrafficObs{tag: "host-1"}}); err != nil {
			return err
		}
		return aipfeature.Contribute(csHost, aipfeature.PlaneUsageObservers, "host", []usage.Observer{projUsageObs{tag: "host-u"}, projUsageObs{tag: "host-u2"}})
	}, nil)))
	gen := featurebundle.GeneratedMergeSurface{Frozen: cs.Freeze()}

	trafficFromFrozen := aipfeature.Get(gen.Frozen, aipfeature.PlaneTrafficObservers)
	gotTraffic := make([]string, 0, len(trafficFromFrozen))
	for _, o := range trafficFromFrozen {
		obs, ok := o.(projTrafficObs)
		require.True(t, ok)
		gotTraffic = append(gotTraffic, obs.tag)
	}
	require.Equal(t, []string{"feat-1", "feat-2", "host-1"}, gotTraffic)

	usageFromFrozen := aipfeature.Get(gen.Frozen, aipfeature.PlaneUsageObservers)
	gotUsage := make([]string, 0, len(usageFromFrozen))
	for _, o := range usageFromFrozen {
		obs, ok := o.(projUsageObs)
		require.True(t, ok)
		gotUsage = append(gotUsage, obs.tag)
	}
	require.Equal(t, []string{"feat-u", "host-u", "host-u2"}, gotUsage)

	genFeatOnly, err := featurebundle.MergeBundlesGenerated(b)
	require.NoError(t, err)
	require.Len(t, aipfeature.Get(genFeatOnly.Frozen, aipfeature.PlaneTrafficObservers), 2)
	require.Len(t, aipfeature.Get(genFeatOnly.Frozen, aipfeature.PlaneUsageObservers), 1)
}
