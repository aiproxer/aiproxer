package pluginreg_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
	"gopkg.in/yaml.v3"
)

type noopOpen struct{}

func (noopOpen) ID() string { return "noop-open" }
func (noopOpen) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

type rootRes struct{}

func (rootRes) Resolve(context.Context) (aipworkspace.WorkspaceView, error) {
	return aipworkspace.WorkspaceView{ProjectRoot: "/tmp"}, nil
}

type noopCat struct{}

func (noopCat) ID() string                        { return "cat" }
func (noopCat) Order() int                        { return 0 }
func (noopCat) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (noopCat) Handle(context.Context, *aipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	return nil
}

type noopRtx struct{}

func (noopRtx) ID() string                        { return "rtx" }
func (noopRtx) Order() int                        { return 0 }
func (noopRtx) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (noopRtx) Handle(context.Context, *aipapi.Call, request.RequestMeta, request.Services) error {
	return nil
}

type noopRH struct{}

func (noopRH) ID() string                        { return "rh" }
func (noopRH) Order() int                        { return 0 }
func (noopRH) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (noopRH) Hint(context.Context, routehint.Input) (routehint.Result, error) {
	return routehint.Result{}, nil
}

type noopCompGate struct{}

func (noopCompGate) ID() string                        { return "cg" }
func (noopCompGate) Order() int                        { return 0 }
func (noopCompGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (noopCompGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

type noopTrafficObs struct{}

func (noopTrafficObs) OnObservation(context.Context, traffic.Observation) error { return nil }

type noopRawSink struct{}

func (noopRawSink) WriteRaw(context.Context, traffic.Leg, traffic.CaptureMeta, []byte) error {
	return nil
}

type noopTrafficRed struct{}

func (noopTrafficRed) ID() string { return "red" }

func (noopTrafficRed) Redact(context.Context, traffic.Leg, traffic.CaptureMeta, []byte) ([]byte, error) {
	return nil, nil
}

func TestMergeFeatureSurface_concatTraffic(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	fac := "fac-traffic-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := reg.RegisterFeature(fac, func(n yaml.Node) (feature.FeatureBundle, error) {
		_ = n
		return testkit.FeatureBundle(t, fac, func(cs *feature.ContributionSet) error {
			if err := feature.Contribute(cs, feature.PlaneTrafficObservers, fac, []traffic.Observer{noopTrafficObs{}}); err != nil {
				return err
			}
			if err := feature.Contribute(cs, feature.PlaneRawCaptureSinks, fac, []traffic.RawCaptureSink{noopRawSink{}}); err != nil {
				return err
			}
			return feature.Contribute(cs, feature.PlaneTrafficRedactors, fac, []traffic.Redactor{noopTrafficRed{}})
		}, nil), nil
	}); err != nil {
		t.Fatal(err)
	}
	var cfgNode yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &cfgNode); err != nil {
		t.Fatal(err)
	}
	gen, err := featurebundle.MergeFeatureSurfaceGenerated(reg, []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: "i1", FactoryKind: fac, Enabled: true, Config: aipsdk.ConfigPayload{Node: cfgNode}},
	})
	if err != nil {
		t.Fatal(err)
	}
	to := feature.Get(gen.Frozen, feature.PlaneTrafficObservers)
	rc := feature.Get(gen.Frozen, feature.PlaneRawCaptureSinks)
	tr := feature.Get(gen.Frozen, feature.PlaneTrafficRedactors)
	if len(to) != 1 || len(rc) != 1 || len(tr) != 1 {
		t.Fatalf("traffic merge obs=%d raw=%d red=%d", len(to), len(rc), len(tr))
	}
}

func TestMergeFeatureSurface_concatCompletionGates(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	fac := "fac-cg-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := reg.RegisterFeature(fac, func(n yaml.Node) (feature.FeatureBundle, error) {
		_ = n
		return testkit.FeatureBundle(t, fac, func(cs *feature.ContributionSet) error {
			return feature.Contribute(cs, feature.PlaneCompletionGates, fac, []completion.Gate{noopCompGate{}})
		}, nil), nil
	}); err != nil {
		t.Fatal(err)
	}
	var cfgNode yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &cfgNode); err != nil {
		t.Fatal(err)
	}
	gen, err := featurebundle.MergeFeatureSurfaceGenerated(reg, []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: "i1", FactoryKind: fac, Enabled: true, Config: aipsdk.ConfigPayload{Node: cfgNode}},
	})
	if err != nil {
		t.Fatal(err)
	}
	gates := feature.Get(gen.Frozen, feature.PlaneCompletionGates)
	if len(gates) != 1 {
		t.Fatalf("completion gates=%d", len(gates))
	}
}

func TestMergeFeatureSurface_concatOpenersAndResolvers(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	fac := "fac-ext-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := reg.RegisterFeature(fac, func(n yaml.Node) (feature.FeatureBundle, error) {
		_ = n
		return testkit.FeatureBundle(t, fac, func(cs *feature.ContributionSet) error {
			if err := feature.Contribute(cs, feature.PlaneSessionOpeners, fac, []session.Opener{noopOpen{}}); err != nil {
				return err
			}
			return feature.Contribute(cs, feature.PlaneWorkspaceResolvers, fac, []aipworkspace.Resolver{rootRes{}})
		}, nil), nil
	}); err != nil {
		t.Fatal(err)
	}
	var cfgNode yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &cfgNode); err != nil {
		t.Fatal(err)
	}
	gen, err := featurebundle.MergeFeatureSurfaceGenerated(reg, []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: "i1", FactoryKind: fac, Enabled: true, Config: aipsdk.ConfigPayload{Node: cfgNode}},
	})
	if err != nil {
		t.Fatal(err)
	}
	openers := feature.Get(gen.Frozen, feature.PlaneSessionOpeners)
	resolvers := feature.Get(gen.Frozen, feature.PlaneWorkspaceResolvers)
	if len(openers) != 1 || len(resolvers) != 1 {
		t.Fatalf("openers=%d resolvers=%d", len(openers), len(resolvers))
	}
}

func TestMergeFeatureSurface_concatCatalogAndTransforms(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	fac := "fac-cat-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := reg.RegisterFeature(fac, func(n yaml.Node) (feature.FeatureBundle, error) {
		_ = n
		return testkit.FeatureBundle(t, fac, func(cs *feature.ContributionSet) error {
			if err := feature.Contribute(cs, feature.PlaneToolCatalogFilters, fac, []toolcatalog.Filter{noopCat{}}); err != nil {
				return err
			}
			return feature.Contribute(cs, feature.PlaneRequestTransforms, fac, []request.Transform{noopRtx{}})
		}, nil), nil
	}); err != nil {
		t.Fatal(err)
	}
	var cfgNode yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &cfgNode); err != nil {
		t.Fatal(err)
	}
	gen, err := featurebundle.MergeFeatureSurfaceGenerated(reg, []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: "i1", FactoryKind: fac, Enabled: true, Config: aipsdk.ConfigPayload{Node: cfgNode}},
	})
	if err != nil {
		t.Fatal(err)
	}
	filters := feature.Get(gen.Frozen, feature.PlaneToolCatalogFilters)
	transforms := feature.Get(gen.Frozen, feature.PlaneRequestTransforms)
	if len(filters) != 1 || len(transforms) != 1 {
		t.Fatalf("catalog=%d transforms=%d", len(filters), len(transforms))
	}
}

func TestMergeFeatureSurface_concatRouteHints(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	fac := "fac-rh-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := reg.RegisterFeature(fac, func(n yaml.Node) (feature.FeatureBundle, error) {
		_ = n
		return testkit.FeatureBundle(t, fac, func(cs *feature.ContributionSet) error {
			return feature.Contribute(cs, feature.PlaneRouteHintProviders, fac, []routehint.Provider{noopRH{}})
		}, nil), nil
	}); err != nil {
		t.Fatal(err)
	}
	var cfgNode yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &cfgNode); err != nil {
		t.Fatal(err)
	}
	gen, err := featurebundle.MergeFeatureSurfaceGenerated(reg, []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: "i1", FactoryKind: fac, Enabled: true, Config: aipsdk.ConfigPayload{Node: cfgNode}},
	})
	if err != nil {
		t.Fatal(err)
	}
	routeHints := feature.Get(gen.Frozen, feature.PlaneRouteHintProviders)
	if len(routeHints) != 1 {
		t.Fatalf("route hints=%d", len(routeHints))
	}
}

type mergeTracePolicy struct {
	tag string
}

func (p mergeTracePolicy) ID() string                      { return p.tag }
func (mergeTracePolicy) Order() int                        { return 0 }
func (mergeTracePolicy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (mergeTracePolicy) Handle(context.Context, aipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionAllow, nil
}

type mergeTraceUsage struct {
	tag string
}

func (mergeTraceUsage) OnUsage(context.Context, usage.Event) error { return nil }

func TestMergeFeatureSurface_mergeToolCallPoliciesUsageObserversRegistrationOrderIgnoresDisabled(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	facPol := "fac-merge-pol-" + strings.ReplaceAll(t.Name(), "/", "-")
	facPanic := "fac-merge-panic-" + strings.ReplaceAll(t.Name(), "/", "-")
	facUsage := "fac-merge-use-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := reg.RegisterFeature(facPol, func(n yaml.Node) (feature.FeatureBundle, error) {
		_ = n
		return testkit.FeatureBundle(t, facPol, func(cs *feature.ContributionSet) error {
			if err := feature.Contribute(cs, feature.PlaneToolCallPolicies, facPol, []toolpolicy.Policy{mergeTracePolicy{tag: "policy-from-first"}}); err != nil {
				return err
			}
			return feature.Contribute(cs, feature.PlaneUsageObservers, facPol, []usage.Observer{mergeTraceUsage{tag: "usage-from-first"}})
		}, nil), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterFeature(facPanic, func(yaml.Node) (feature.FeatureBundle, error) {
		panic("disabled registration must not invoke factory")
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterFeature(facUsage, func(n yaml.Node) (feature.FeatureBundle, error) {
		_ = n
		return testkit.FeatureBundle(t, facUsage, func(cs *feature.ContributionSet) error {
			return feature.Contribute(cs, feature.PlaneUsageObservers, facUsage, []usage.Observer{mergeTraceUsage{tag: "usage-from-second"}})
		}, nil), nil
	}); err != nil {
		t.Fatal(err)
	}
	var cfgNode yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &cfgNode); err != nil {
		t.Fatal(err)
	}
	gen, err := featurebundle.MergeFeatureSurfaceGenerated(reg, []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: "en-a", FactoryKind: facPol, Enabled: true, Config: aipsdk.ConfigPayload{Node: cfgNode}},
		{Kind: aipsdk.PluginKindFeature, ID: "dis", FactoryKind: facPanic, Enabled: false, Config: aipsdk.ConfigPayload{Node: cfgNode}},
		{Kind: aipsdk.PluginKindFeature, ID: "en-b", FactoryKind: facUsage, Enabled: true, Config: aipsdk.ConfigPayload{Node: cfgNode}},
	})
	if err != nil {
		t.Fatal(err)
	}
	pols := feature.Get(gen.Frozen, feature.PlaneToolCallPolicies)
	if len(pols) != 1 || pols[0].ID() != "policy-from-first" {
		t.Fatalf("policies %+v", pols)
	}
	usageObs := feature.Get(gen.Frozen, feature.PlaneUsageObservers)
	if len(usageObs) != 2 {
		t.Fatalf("usage observers=%d", len(usageObs))
	}
	u0, ok := usageObs[0].(mergeTraceUsage)
	if !ok || u0.tag != "usage-from-first" {
		t.Fatalf("first usage observer %#v", usageObs[0])
	}
	u1, ok := usageObs[1].(mergeTraceUsage)
	if !ok || u1.tag != "usage-from-second" {
		t.Fatalf("second usage observer %#v", usageObs[1])
	}
}

func TestMergeFeatureSurface_ignoresExplicitOtherFactoryEvenWhenIDMatches(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterFeature("other-feature", func(yaml.Node) (feature.FeatureBundle, error) {
		return feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1}, nil
	}); err != nil {
		t.Fatal(err)
	}
	regs := []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: "secrets-guard", FactoryKind: "other-feature", Enabled: true, Config: aipsdk.ConfigPayload{Node: yaml.Node{}}},
	}
	gen, err := featurebundle.MergeFeatureSurfaceGenerated(reg, regs)
	if err != nil {
		t.Fatal(err)
	}
	guards := feature.Get(gen.Frozen, feature.PlaneSecretGuards)
	if len(guards) != 0 {
		t.Fatalf("secret guards should come only from the explicit factory, got %+v", guards)
	}
}
