package featurebundle

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/localturn"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// --- Test helpers (minimal no-op implementations for each interface) ---

type testSubmitHook struct{ tag string }

func (h testSubmitHook) ID() string                      { return h.tag }
func (testSubmitHook) Order() int                        { return 0 }
func (testSubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testSubmitHook) Handle(_ context.Context, _ *aipapi.Call, _ *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, nil
}

type testRequestPartHook struct{ tag string }

func (h testRequestPartHook) ID() string                      { return h.tag }
func (testRequestPartHook) Order() int                        { return 0 }
func (testRequestPartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testRequestPartHook) HandleRequestParts(_ context.Context, _ *aipapi.Call, _ sdkhooks.PartMeta) error {
	return nil
}

type testResponsePartHook struct{ tag string }

func (h testResponsePartHook) ID() string                      { return h.tag }
func (testResponsePartHook) Order() int                        { return 0 }
func (testResponsePartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testResponsePartHook) HandleEvent(_ context.Context, _ *aipapi.Event, _ sdkhooks.PartMeta) error {
	return nil
}

type testToolReactor struct{ tag string }

func (h testToolReactor) ID() string { return h.tag }
func (testToolReactor) Order() int   { return 0 }
func (testToolReactor) HandleToolEvent(_ context.Context, _ aipapi.ToolEvent, _ sdkhooks.ToolMeta) (sdkhooks.ToolDecision, aipapi.ToolEvent, error) {
	return sdkhooks.ToolPass, aipapi.ToolEvent{}, nil
}

type testOpener struct{ tag string }

func (h testOpener) ID() string { return h.tag }
func (testOpener) Open(_ context.Context, _ session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

type testResolver struct{ tag string }

func (testResolver) Resolve(_ context.Context) (aipworkspace.WorkspaceView, error) {
	return aipworkspace.WorkspaceView{ProjectRoot: "/tmp"}, nil
}

type testCatalogFilter struct{ tag string }

func (h testCatalogFilter) ID() string                      { return h.tag }
func (testCatalogFilter) Order() int                        { return 0 }
func (testCatalogFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testCatalogFilter) Handle(_ context.Context, _ *aipapi.Call, _ toolcatalog.CatalogMeta, _ toolcatalog.Services) error {
	return nil
}

type testPolicy struct{ tag string }

func (h testPolicy) ID() string                      { return h.tag }
func (testPolicy) Order() int                        { return 0 }
func (testPolicy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testPolicy) Handle(_ context.Context, _ aipapi.ToolEvent, _ toolpolicy.Meta, _ toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionAllow, nil
}

type testFinalizer struct{ tag string }

func (h testFinalizer) ID() string { return h.tag }
func (testFinalizer) Order() int   { return 0 }
func (testFinalizer) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass}, nil
}

type testTransform struct{ tag string }

func (h testTransform) ID() string                      { return h.tag }
func (testTransform) Order() int                        { return 0 }
func (testTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testTransform) Handle(_ context.Context, _ *aipapi.Call, _ request.RequestMeta, _ request.Services) error {
	return nil
}

type testPreReq struct{ tag string }

func (h testPreReq) ID() string                      { return h.tag }
func (testPreReq) Order() int                        { return 0 }
func (testPreReq) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testPreReq) Handle(_ context.Context, _ *aipapi.Call, _ prerequest.Meta, _ prerequest.Services) (prerequest.Decision, error) {
	return prerequest.Allow(), nil
}

type testRouteHint struct{ tag string }

func (h testRouteHint) ID() string                      { return h.tag }
func (testRouteHint) Order() int                        { return 0 }
func (testRouteHint) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testRouteHint) Hint(_ context.Context, _ routehint.Input) (routehint.Result, error) {
	return routehint.Result{}, nil
}

type testCompGate struct{ tag string }

func (h testCompGate) ID() string                      { return h.tag }
func (testCompGate) Order() int                        { return 0 }
func (testCompGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testCompGate) Handle(_ context.Context, _ completion.Meta, _ completion.Buffered, _ completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

type testAttemptTransform struct{ tag string }

func (h testAttemptTransform) ID() string                      { return h.tag }
func (testAttemptTransform) Order() int                        { return 0 }
func (testAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (testAttemptTransform) HandleAttempt(context.Context, *aipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type testStreamObserverFactory struct{ tag string }

func (h testStreamObserverFactory) ID() string                      { return h.tag }
func (testStreamObserverFactory) Order() int                        { return 0 }
func (testStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (testStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

type testTrafficObs struct{ tag string }

func (testTrafficObs) OnObservation(_ context.Context, _ traffic.Observation) error { return nil }

type testUsageObs struct{ tag string }

func (testUsageObs) OnUsage(_ context.Context, _ usage.Event) error { return nil }

type testCompactionObs struct{ tag string }

func (testCompactionObs) OnCompaction(_ context.Context, _ compaction.Event) error { return nil }

type testCompactionPreserver struct{ tag string }

func (p testCompactionPreserver) ID() string { return p.tag }

func (testCompactionPreserver) BeforeRequest(context.Context, *aipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (testCompactionPreserver) RequestOpened(context.Context, aipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (testCompactionPreserver) BeforeResponseRelease(context.Context, *aipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

type testRawSink struct{ tag string }

func (testRawSink) WriteRaw(_ context.Context, _ traffic.Leg, _ traffic.CaptureMeta, _ []byte) error {
	return nil
}

type testRedactor struct{ tag string }

func (r testRedactor) ID() string { return r.tag }

func (testRedactor) Redact(_ context.Context, _ traffic.Leg, _ traffic.CaptureMeta, body []byte) ([]byte, error) {
	return body, nil
}

type testSecretGuard struct{ tag string }

type testTerminalDecisionProvider struct{ tag string }

func (p testTerminalDecisionProvider) ID() string { return p.tag }

func (testTerminalDecisionProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

type testLocalTurnHandler struct{ tag string }

func (h testLocalTurnHandler) ID() string                      { return h.tag }
func (testLocalTurnHandler) Order() int                        { return 0 }
func (testLocalTurnHandler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (testLocalTurnHandler) Match(context.Context, aipapi.Call, localturn.Meta) (localturn.MatchResult, error) {
	return localturn.MatchResult{}, nil
}

func (testLocalTurnHandler) Handle(context.Context, localturn.HandleInput) (localturn.Reply, error) {
	return localturn.Reply{}, nil
}

func (g testSecretGuard) ID() string                         { return g.tag }
func (testSecretGuard) Order() int                           { return 0 }
func (testSecretGuard) FailureMode() secretguard.FailureMode { return secretguard.FailClosed }
func (testSecretGuard) Evaluate(context.Context, *aipapi.Call, secretguard.Meta, secretguard.Services) (secretguard.Decision, error) {
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

type testLifecycle struct{ tag string }

func (testLifecycle) Start(context.Context) error     { return nil }
func (testLifecycle) Stop(context.Context) error      { return nil }
func (testLifecycle) SafeUnderCandidateOverlap() bool { return true }

func makeTestBundle(fn func(cs *aipfeature.ContributionSet)) aipfeature.FeatureBundle {
	cs := aipfeature.NewContributionSet()
	if fn != nil {
		fn(cs)
	}
	return aipfeature.BundleFromPlanes(cs.Freeze(), nil)
}

// --- Tests ---

func TestMergeBundlesGenerated_empty(t *testing.T) {
	t.Parallel()
	gen, err := MergeBundlesGenerated()
	require.NoError(t, err)
	if len(gen.Lifecycles) != 0 {
		t.Fatalf("MergeBundlesGenerated() with no args should have empty lifecycles: %+v", gen)
	}
}

func TestMergeBundlesGenerated_TerminalDecisionProviderZeroAndOne(t *testing.T) {
	t.Parallel()
	empty, err := MergeBundlesGenerated()
	if err != nil {
		t.Fatalf("empty merge error: %v", err)
	}
	if aipfeature.Get(empty.Frozen, aipfeature.PlaneTerminalDecisionProvider) != nil {
		t.Fatal("empty merge unexpectedly contributed provider")
	}

	provider := testTerminalDecisionProvider{tag: "provider.example"}
	merged, err := MergeBundlesGenerated(makeTestBundle(func(cs *aipfeature.ContributionSet) {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "provider.example", terminaldecision.Provider(provider))
	}))
	if err != nil {
		t.Fatalf("one-provider merge error: %v", err)
	}
	if aipfeature.Get(merged.Frozen, aipfeature.PlaneTerminalDecisionProvider) != provider {
		t.Fatalf("merged provider = %#v, want %#v", aipfeature.Get(merged.Frozen, aipfeature.PlaneTerminalDecisionProvider), provider)
	}
}

func TestMergeBundlesGenerated_TerminalDecisionProviderConflictFailsBeforePublication(t *testing.T) {
	t.Parallel()
	first := testTerminalDecisionProvider{tag: "provider.first"}
	second := testTerminalDecisionProvider{tag: "provider.second"}
	merged, err := MergeBundlesGenerated(
		makeTestBundle(func(cs *aipfeature.ContributionSet) {
			_ = aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "first", terminaldecision.Provider(first))
		}),
		makeTestBundle(func(cs *aipfeature.ContributionSet) {
			_ = aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "second", terminaldecision.Provider(second))
		}),
	)
	if err == nil {
		t.Fatal("duplicate providers were accepted")
	}
	if !errors.Is(err, aipfeature.ErrExclusiveConflict) {
		t.Fatalf("conflict error = %v, want ErrExclusiveConflict", err)
	}
	if !errors.Is(err, aipfeature.ErrTerminalDecisionProviderConflict) {
		t.Fatalf("conflict error = %v, want ErrTerminalDecisionProviderConflict", err)
	}
	if !strings.Contains(err.Error(), first.ID()) || !strings.Contains(err.Error(), second.ID()) {
		t.Fatalf("conflict error = %q, want both bounded provider identities", err)
	}
	if !merged.Frozen.IsZero() || len(merged.Lifecycles) != 0 {
		t.Fatalf("candidate was published after conflict: %#v", merged)
	}
}

func TestMergeBundlesGenerated_TerminalDecisionProviderRejectsInvalidProvider(t *testing.T) {
	t.Parallel()
	var typedNil *testTerminalDecisionProvider
	cs := aipfeature.NewContributionSet()
	err := aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "p", terminaldecision.Provider(typedNil))
	if !errors.Is(err, terminaldecision.ErrInvalidProvider) {
		t.Fatalf("typed-nil provider error = %v, want ErrInvalidProvider", err)
	}
}

func TestMergeBundlesGenerated_concatenatesLifecycles(t *testing.T) {
	t.Parallel()
	b1 := aipfeature.FeatureBundle{
		SchemaVersion: aipfeature.SchemaVersionV1,
		Lifecycles:    []aipplugin.Lifecycle{testLifecycle{tag: "l1"}},
	}
	b2 := aipfeature.FeatureBundle{
		SchemaVersion: aipfeature.SchemaVersionV1,
		Lifecycles:    []aipplugin.Lifecycle{testLifecycle{tag: "l2"}},
	}
	gen, err := MergeBundlesGenerated(b1, b2)
	require.NoError(t, err)
	require.Len(t, gen.Lifecycles, 2)
}

func TestMergeBundlesGenerated_ToolCallFinalizationMaxArgsBytesMin(t *testing.T) {
	t.Parallel()
	gen, err := MergeBundlesGenerated(
		makeTestBundle(func(cs *aipfeature.ContributionSet) {
			_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "p1", 4096)
		}),
		makeTestBundle(func(cs *aipfeature.ContributionSet) {
			_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "p2", 1024)
		}),
		makeTestBundle(nil),
	)
	if err != nil {
		t.Fatalf("MergeBundlesGenerated error: %v", err)
	}
	got := aipfeature.Get(gen.Frozen, aipfeature.PlaneToolCallFinalizationMaxArgsBytes)
	if got != 1024 {
		t.Fatalf("got %d want 1024 (min of positives)", got)
	}
}

func TestMergeBundlesGenerated_ToolCallFinalizationMaxArgsBytesIgnoresNonPositive(t *testing.T) {
	t.Parallel()
	// Merge keeps min of positives only; Validate rejects negatives on FeatureBundle.
	// Non-positive values are not contributions at the merge surface.
	gen, err := MergeBundlesGenerated(
		makeTestBundle(func(cs *aipfeature.ContributionSet) {
			_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "p1", 2048)
		}),
		makeTestBundle(nil),
	)
	if err != nil {
		t.Fatalf("MergeBundlesGenerated error: %v", err)
	}
	got := aipfeature.Get(gen.Frozen, aipfeature.PlaneToolCallFinalizationMaxArgsBytes)
	if got != 2048 {
		t.Fatalf("got %d want 2048 (zero ignored at merge)", got)
	}
}

func TestMergeBundlesGenerated_preservesBundleOrderAcrossSlices(t *testing.T) {
	t.Parallel()
	b1 := makeTestBundle(func(cs *aipfeature.ContributionSet) {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneLocalTurnHandlers, "b1", []localturn.Handler{testLocalTurnHandler{tag: "first"}})
		_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizers, "b1", []toolcall.Finalizer{testFinalizer{tag: "first"}})
	})
	b2 := makeTestBundle(func(cs *aipfeature.ContributionSet) {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneLocalTurnHandlers, "b2", []localturn.Handler{testLocalTurnHandler{tag: "second"}})
		_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizers, "b2", []toolcall.Finalizer{testFinalizer{tag: "second"}})
	})
	b3 := makeTestBundle(func(cs *aipfeature.ContributionSet) {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneLocalTurnHandlers, "b3", []localturn.Handler{testLocalTurnHandler{tag: "third"}})
		_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizers, "b3", []toolcall.Finalizer{testFinalizer{tag: "third"}})
	})
	gen, err := MergeBundlesGenerated(b1, b2, b3)
	if err != nil {
		t.Fatalf("MergeBundlesGenerated error: %v", err)
	}
	lts := aipfeature.Get(gen.Frozen, aipfeature.PlaneLocalTurnHandlers)
	if len(lts) != 3 {
		t.Fatalf("LocalTurnHandlers: got %d want 3", len(lts))
	}
	fins := aipfeature.Get(gen.Frozen, aipfeature.PlaneToolCallFinalizers)
	if len(fins) != 3 {
		t.Fatalf("ToolCallFinalizers: got %d want 3", len(fins))
	}
	f0, ok := fins[0].(testFinalizer)
	if !ok || f0.tag != "first" {
		t.Fatalf("first finalizer: %#v", fins[0])
	}
	f1, ok := fins[1].(testFinalizer)
	if !ok || f1.tag != "second" {
		t.Fatalf("second finalizer: %#v", fins[1])
	}
	f2, ok := fins[2].(testFinalizer)
	if !ok || f2.tag != "third" {
		t.Fatalf("third finalizer: %#v", fins[2])
	}
}

func TestMergeFeatureSurfacesWithHost_ThreeSourceOrdering(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	err := reg.RegisterFeature("test-feature", func(n yaml.Node) (aipfeature.FeatureBundle, error) {
		return makeTestBundle(func(cs *aipfeature.ContributionSet) {
			_ = aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, "feat", []traffic.Observer{testTrafficObs{tag: "feat-to"}})
			_ = aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, "feat", []usage.Observer{testUsageObs{tag: "feat-uo"}})
			_ = aipfeature.Contribute(cs, aipfeature.PlaneRawCaptureSinks, "feat", []traffic.RawCaptureSink{testRawSink{tag: "feat-raw"}})
			_ = aipfeature.Contribute(cs, aipfeature.PlaneTrafficRedactors, "feat", []traffic.Redactor{testRedactor{tag: "feat-red"}})
		}), nil
	})
	if err != nil {
		t.Fatal(err)
	}

	regs := []aipsdk.Registration{
		{
			Kind:        aipsdk.PluginKindFeature,
			ID:          "inst-feature",
			FactoryKind: "test-feature",
			Enabled:     true,
		},
	}

	host := HostContributions{
		TrafficObservers: []traffic.Observer{
			testTrafficObs{tag: "host-to"},
		},
		UsageObservers: []usage.Observer{
			testUsageObs{tag: "host-uo"},
		},
	}

	candExtra := makeTestBundle(func(cs *aipfeature.ContributionSet) {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, "cand", []traffic.Observer{testTrafficObs{tag: "cand-to"}})
		_ = aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, "cand", []usage.Observer{testUsageObs{tag: "cand-uo"}})
		_ = aipfeature.Contribute(cs, aipfeature.PlaneRawCaptureSinks, "cand", []traffic.RawCaptureSink{testRawSink{tag: "cand-raw"}})
		_ = aipfeature.Contribute(cs, aipfeature.PlaneTrafficRedactors, "cand", []traffic.Redactor{testRedactor{tag: "cand-red"}})
	})

	gen, err := MergeFeatureSurfacesWithHost(reg, regs, host, candExtra)
	if err != nil {
		t.Fatalf("MergeFeatureSurfacesWithHost error: %v", err)
	}

	to := aipfeature.Get(gen.Frozen, aipfeature.PlaneTrafficObservers)
	if len(to) != 3 {
		t.Fatalf("TrafficObservers len = %d, want 3", len(to))
	}
	to0, ok0 := to[0].(testTrafficObs)
	to1, ok1 := to[1].(testTrafficObs)
	to2, ok2 := to[2].(testTrafficObs)
	if !ok0 || !ok1 || !ok2 || to0.tag != "feat-to" || to1.tag != "host-to" || to2.tag != "cand-to" {
		t.Fatalf("TrafficObservers order = %#v, want [feat-to, host-to, cand-to]", to)
	}

	uo := aipfeature.Get(gen.Frozen, aipfeature.PlaneUsageObservers)
	if len(uo) != 3 {
		t.Fatalf("UsageObservers len = %d, want 3", len(uo))
	}
	uo0, ok0 := uo[0].(testUsageObs)
	uo1, ok1 := uo[1].(testUsageObs)
	uo2, ok2 := uo[2].(testUsageObs)
	if !ok0 || !ok1 || !ok2 || uo0.tag != "feat-uo" || uo1.tag != "host-uo" || uo2.tag != "cand-uo" {
		t.Fatalf("UsageObservers order = %#v, want [feat-uo, host-uo, cand-uo]", uo)
	}

	raw := aipfeature.Get(gen.Frozen, aipfeature.PlaneRawCaptureSinks)
	if len(raw) != 2 {
		t.Fatalf("RawCaptureSinks len = %d, want 2", len(raw))
	}
	raw0, ok0 := raw[0].(testRawSink)
	raw1, ok1 := raw[1].(testRawSink)
	if !ok0 || !ok1 || raw0.tag != "feat-raw" || raw1.tag != "cand-raw" {
		t.Fatalf("RawCaptureSinks order = %#v, want [feat-raw, cand-raw]", raw)
	}

	red := aipfeature.Get(gen.Frozen, aipfeature.PlaneTrafficRedactors)
	if len(red) != 2 {
		t.Fatalf("TrafficRedactors len = %d, want 2", len(red))
	}
	if red[0].ID() != "feat-red" || red[1].ID() != "cand-red" {
		t.Fatalf("TrafficRedactors order = %#v, want [feat-red, cand-red]", red)
	}
}

func TestGeneratedMergeSurface_BindAttemptTransforms_ReplaceByIdentity(t *testing.T) {
	t.Parallel()

	initBundle := makeTestBundle(func(cs *aipfeature.ContributionSet) {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "init", []request.AttemptTransform{
			testAttemptTransform{tag: "xform-1"},
			testAttemptTransform{tag: "reasoning-preservation-transform"},
			testAttemptTransform{tag: "xform-2"},
		})
	})

	gen, err := MergeBundlesGenerated(initBundle)
	if err != nil {
		t.Fatalf("MergeBundlesGenerated: %v", err)
	}

	xformsBefore := aipfeature.Get(gen.Frozen, aipfeature.PlaneAttemptTransforms)
	if len(xformsBefore) != 3 {
		t.Fatalf("xforms before len = %d, want 3", len(xformsBefore))
	}

	boundXform := testAttemptTransform{tag: "reasoning-preservation-transform"}
	updatedGen, err := gen.BindAttemptTransforms("reasoning-preservation", []request.AttemptTransform{boundXform})
	if err != nil {
		t.Fatalf("BindAttemptTransforms: %v", err)
	}

	xformsAfter := aipfeature.Get(updatedGen.Frozen, aipfeature.PlaneAttemptTransforms)
	if len(xformsAfter) != 3 {
		t.Fatalf("xforms after len = %d, want 3", len(xformsAfter))
	}
	if xformsAfter[0].ID() != "xform-1" || xformsAfter[1].ID() != "xform-2" || xformsAfter[2].ID() != "reasoning-preservation-transform" {
		t.Fatalf("unexpected replacement order: %v, %v, %v", xformsAfter[0].ID(), xformsAfter[1].ID(), xformsAfter[2].ID())
	}

	// Idempotent: rebinding doesn't duplicate
	reboundGen, err := updatedGen.BindAttemptTransforms("reasoning-preservation", []request.AttemptTransform{boundXform})
	if err != nil {
		t.Fatalf("rebound: %v", err)
	}
	xformsRebound := aipfeature.Get(reboundGen.Frozen, aipfeature.PlaneAttemptTransforms)
	if len(xformsRebound) != 3 {
		t.Fatalf("xforms rebound len = %d, want 3", len(xformsRebound))
	}
	if xformsRebound[0].ID() != "xform-1" || xformsRebound[1].ID() != "xform-2" || xformsRebound[2].ID() != "reasoning-preservation-transform" {
		t.Fatalf("unexpected rebound order: %v, %v, %v", xformsRebound[0].ID(), xformsRebound[1].ID(), xformsRebound[2].ID())
	}
}

func TestGeneratedMergeSurface_BindAttemptTransforms_NilReject(t *testing.T) {
	t.Parallel()

	initBundle := makeTestBundle(func(cs *aipfeature.ContributionSet) {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "init", []request.AttemptTransform{
			testAttemptTransform{tag: "xform-1"},
		})
	})

	gen, err := MergeBundlesGenerated(initBundle)
	if err != nil {
		t.Fatalf("MergeBundlesGenerated: %v", err)
	}

	// Rejects slice containing nil element
	_, err = gen.BindAttemptTransforms("reasoning-preservation", []request.AttemptTransform{nil})
	if err == nil {
		t.Fatalf("expected error for nil transform element, got nil")
	}
	if !strings.Contains(err.Error(), "must not be nil") {
		t.Fatalf("error = %v, want 'must not be nil'", err)
	}
}

func TestGeneratedMergeSurface_AllPlanesPreserved_AcrossBindOperations(t *testing.T) {
	t.Parallel()

	initBundle := makeTestBundle(func(cs *aipfeature.ContributionSet) {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneSubmitHooks, "init", []sdkhooks.SubmitHook{testSubmitHook{tag: "hook-1"}})
		_ = aipfeature.Contribute(cs, aipfeature.PlaneSessionOpeners, "init", []session.Opener{testOpener{tag: "opener-1"}})
		_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCatalogFilters, "init", []toolcatalog.Filter{testCatalogFilter{tag: "cat-1"}})
		_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "init", 1024)
		_ = aipfeature.Contribute(cs, aipfeature.PlaneRequestTransforms, "init", []request.Transform{testTransform{tag: "req-1"}})
		_ = aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "init", []request.AttemptTransform{testAttemptTransform{tag: "xform-1"}})
		_ = aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "init", []response.StreamObserverFactory{testStreamObserverFactory{tag: "obs-1"}})
		_ = aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, "init", []traffic.Observer{testTrafficObs{tag: "to-1"}})
		_ = aipfeature.Contribute(cs, aipfeature.PlaneCompactionPreservers, "init", []compaction.Preserver{testCompactionPreserver{tag: "cp-1"}})
		_ = aipfeature.Contribute(cs, aipfeature.PlaneSecretGuards, "init", []secretguard.Guard{testSecretGuard{tag: "guard-1"}})
		_ = aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "term-1", terminaldecision.Provider(testTerminalDecisionProvider{tag: "term-1"}))
	})

	gen, err := MergeBundlesGenerated(initBundle)
	if err != nil {
		t.Fatalf("MergeBundlesGenerated: %v", err)
	}

	// 1. Test binding when starting from Frozen only (set == nil)
	fromFrozenOnly := GeneratedMergeSurface{
		Frozen:     gen.Frozen,
		Lifecycles: gen.Lifecycles,
	}

	updated, err := fromFrozenOnly.BindAttemptTransforms("binder-1", []request.AttemptTransform{
		testAttemptTransform{tag: "xform-bound"},
	})
	if err != nil {
		t.Fatalf("BindAttemptTransforms on fromFrozenOnly: %v", err)
	}

	// Verify all planes are preserved
	assert.Len(t, aipfeature.Get(updated.Frozen, aipfeature.PlaneSubmitHooks), 1)
	assert.Len(t, aipfeature.Get(updated.Frozen, aipfeature.PlaneSessionOpeners), 1)
	assert.Len(t, aipfeature.Get(updated.Frozen, aipfeature.PlaneToolCatalogFilters), 1)
	assert.Equal(t, 1024, aipfeature.Get(updated.Frozen, aipfeature.PlaneToolCallFinalizationMaxArgsBytes))
	assert.Len(t, aipfeature.Get(updated.Frozen, aipfeature.PlaneRequestTransforms), 1)
	assert.Len(t, aipfeature.Get(updated.Frozen, aipfeature.PlaneStreamObserverFactories), 1)
	assert.Len(t, aipfeature.Get(updated.Frozen, aipfeature.PlaneTrafficObservers), 1)
	assert.Len(t, aipfeature.Get(updated.Frozen, aipfeature.PlaneCompactionPreservers), 1)
	assert.Len(t, aipfeature.Get(updated.Frozen, aipfeature.PlaneSecretGuards), 1)
	assert.NotNil(t, aipfeature.Get(updated.Frozen, aipfeature.PlaneTerminalDecisionProvider))

	xforms := aipfeature.Get(updated.Frozen, aipfeature.PlaneAttemptTransforms)
	assert.Len(t, xforms, 2)
}

func TestGeneratedMergeSurface_SecondOperationFailureTransaction(t *testing.T) {
	t.Parallel()

	initBundle := makeTestBundle(func(cs *aipfeature.ContributionSet) {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "init", []request.AttemptTransform{
			testAttemptTransform{tag: "xform-1"},
		})
		_ = aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "init", []response.StreamObserverFactory{
			testStreamObserverFactory{tag: "obs-1"},
		})
	})

	g0, err := MergeBundlesGenerated(initBundle)
	if err != nil {
		t.Fatalf("MergeBundlesGenerated: %v", err)
	}

	// Operation 1 succeeds
	g1, err := g0.BindAttemptTransforms("binder", []request.AttemptTransform{
		testAttemptTransform{tag: "xform-bound"},
	})
	if err != nil {
		t.Fatalf("BindAttemptTransforms failed: %v", err)
	}
	require.Len(t, aipfeature.Get(g1.Frozen, aipfeature.PlaneAttemptTransforms), 2)

	// Operation 2 fails (nil factory in slice under NilReject)
	g2, err := g1.BindStreamObserverFactories("binder", []response.StreamObserverFactory{nil})
	require.Error(t, err)
	assert.True(t, g2.Frozen.IsZero())

	// g1 MUST BE COMPLETELY UNCHANGED
	xformsG1 := aipfeature.Get(g1.Frozen, aipfeature.PlaneAttemptTransforms)
	require.Len(t, xformsG1, 2)
	assert.Equal(t, "xform-1", xformsG1[0].ID())
	assert.Equal(t, "xform-bound", xformsG1[1].ID())

	obsG1 := aipfeature.Get(g1.Frozen, aipfeature.PlaneStreamObserverFactories)
	require.Len(t, obsG1, 1)
	assert.Equal(t, "obs-1", obsG1[0].ID())

	// g0 MUST ALSO BE COMPLETELY UNCHANGED
	xformsG0 := aipfeature.Get(g0.Frozen, aipfeature.PlaneAttemptTransforms)
	require.Len(t, xformsG0, 1)
	assert.Equal(t, "xform-1", xformsG0[0].ID())
}

func TestGeneratedMergeSurface_BindCompactionPreservers(t *testing.T) {
	t.Parallel()

	initBundle := makeTestBundle(func(cs *aipfeature.ContributionSet) {
		_ = aipfeature.Contribute(cs, aipfeature.PlaneCompactionPreservers, "init", []compaction.Preserver{
			testCompactionPreserver{tag: "pres-1"},
			testCompactionPreserver{tag: "pres-replace"},
		})
	})

	gen, err := MergeBundlesGenerated(initBundle)
	if err != nil {
		t.Fatalf("MergeBundlesGenerated: %v", err)
	}

	updated, err := gen.BindCompactionPreservers("pres-binder", []compaction.Preserver{
		testCompactionPreserver{tag: "pres-replace"},
	})
	if err != nil {
		t.Fatalf("BindCompactionPreservers failed: %v", err)
	}

	pres := aipfeature.Get(updated.Frozen, aipfeature.PlaneCompactionPreservers)
	require.Len(t, pres, 2)
	assert.Equal(t, "pres-1", pres[0].ID())
	assert.Equal(t, "pres-replace", pres[1].ID())
}

func TestContributeBundle_PlaneSetReplay(t *testing.T) {
	t.Parallel()

	// 1. Nil destination exact error
	err := ContributeBundle(nil, "p", aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1})
	require.Error(t, err)
	assert.Equal(t, "featurebundle: nil ContributionSet", err.Error())

	// 2. Populated PlaneSet replay
	csSrc := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(csSrc, aipfeature.PlaneSubmitHooks, "feat", []sdkhooks.SubmitHook{testSubmitHook{tag: "hook-1"}}))
	require.NoError(t, aipfeature.Contribute(csSrc, aipfeature.PlaneToolReactors, "feat", []sdkhooks.ToolReactor{testToolReactor{tag: "tool-1"}}))
	require.NoError(t, aipfeature.Contribute(csSrc, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, "feat", 2048))
	require.NoError(t, aipfeature.Contribute(csSrc, aipfeature.PlaneTerminalDecisionProvider, "term-prov", terminaldecision.Provider(testTerminalDecisionProvider{tag: "term-prov"})))
	require.NoError(t, aipfeature.Contribute(csSrc, aipfeature.PlaneSessionOpeners, "feat", []session.Opener{}))

	bundle := aipfeature.BundleFromPlanes(csSrc.Freeze(), []aipplugin.Lifecycle{testLifecycle{tag: "life-1"}})

	csDst := aipfeature.NewContributionSet()
	require.NoError(t, ContributeBundle(csDst, "test-plugin", bundle))

	frozen := csDst.Freeze()
	sh := aipfeature.Get(frozen, aipfeature.PlaneSubmitHooks)
	require.Len(t, sh, 1)
	assert.Equal(t, "hook-1", sh[0].ID())

	tr := aipfeature.Get(frozen, aipfeature.PlaneToolReactors)
	require.Len(t, tr, 1)
	assert.Equal(t, "tool-1", tr[0].ID())

	capBytes := aipfeature.Get(frozen, aipfeature.PlaneToolCallFinalizationMaxArgsBytes)
	assert.Equal(t, 2048, capBytes)

	tp := aipfeature.Get(frozen, aipfeature.PlaneTerminalDecisionProvider)
	require.NotNil(t, tp)
	assert.Equal(t, "term-prov", tp.ID())

	so := aipfeature.Get(frozen, aipfeature.PlaneSessionOpeners)
	assert.NotNil(t, so)
	assert.Len(t, so, 0)

	// 3. MergeBundlesGenerated with new-style bundle
	gen, err := MergeBundlesGenerated(bundle)
	require.NoError(t, err)
	require.Len(t, gen.Lifecycles, 1)
	lc, ok := gen.Lifecycles[0].(testLifecycle)
	require.True(t, ok)
	assert.Equal(t, "life-1", lc.tag)

	genSH := aipfeature.Get(gen.Frozen, aipfeature.PlaneSubmitHooks)
	require.Len(t, genSH, 1)
	assert.Equal(t, "hook-1", genSH[0].ID())

	// 4. FreezeBundle with new-style bundle
	frozenDirect, err := FreezeBundle(bundle, "")
	require.NoError(t, err)
	fbSH := aipfeature.Get(frozenDirect, aipfeature.PlaneSubmitHooks)
	require.Len(t, fbSH, 1)
	assert.Equal(t, "hook-1", fbSH[0].ID())

	// 5. Atomic failure on exclusive conflict
	conflictDst := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(conflictDst, aipfeature.PlaneTerminalDecisionProvider, "other-term", terminaldecision.Provider(testTerminalDecisionProvider{tag: "other-term"})))
	require.NoError(t, aipfeature.Contribute(conflictDst, aipfeature.PlaneSubmitHooks, "init", []sdkhooks.SubmitHook{testSubmitHook{tag: "init-hook"}}))

	err = ContributeBundle(conflictDst, "test-plugin", bundle)
	require.Error(t, err)

	conflictFrozen := conflictDst.Freeze()
	cSH := aipfeature.Get(conflictFrozen, aipfeature.PlaneSubmitHooks)
	require.Len(t, cSH, 1)
	assert.Equal(t, "init-hook", cSH[0].ID())
	cTR := aipfeature.Get(conflictFrozen, aipfeature.PlaneToolReactors)
	assert.Len(t, cTR, 0)
}
