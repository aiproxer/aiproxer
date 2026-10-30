package featurebundle

import (
	"context"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
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
	"github.com/stretchr/testify/require"
)

// --- Characterization stubs (tag-carrying minimal implementations) ---

type charSubmitHook struct{ tag string }

func (h charSubmitHook) ID() string                      { return h.tag }
func (charSubmitHook) Order() int                        { return 0 }
func (charSubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charSubmitHook) Handle(context.Context, *aipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, nil
}

type charRequestPartHook struct{ tag string }

func (h charRequestPartHook) ID() string                      { return h.tag }
func (charRequestPartHook) Order() int                        { return 0 }
func (charRequestPartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charRequestPartHook) HandleRequestParts(context.Context, *aipapi.Call, sdkhooks.PartMeta) error {
	return nil
}

type charResponsePartHook struct{ tag string }

func (h charResponsePartHook) ID() string                      { return h.tag }
func (charResponsePartHook) Order() int                        { return 0 }
func (charResponsePartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charResponsePartHook) HandleEvent(context.Context, *aipapi.Event, sdkhooks.PartMeta) error {
	return nil
}

type charToolReactor struct{ tag string }

func (h charToolReactor) ID() string { return h.tag }
func (charToolReactor) Order() int   { return 0 }
func (charToolReactor) HandleToolEvent(context.Context, aipapi.ToolEvent, sdkhooks.ToolMeta) (sdkhooks.ToolDecision, aipapi.ToolEvent, error) {
	return sdkhooks.ToolPass, aipapi.ToolEvent{}, nil
}

type charLifecycle struct{ tag string }

func (charLifecycle) Start(context.Context) error { return nil }
func (charLifecycle) Stop(context.Context) error  { return nil }

type charOpener struct{ tag string }

func (h charOpener) ID() string { return h.tag }
func (charOpener) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

type charResolver struct{ tag string }

func (charResolver) Resolve(context.Context) (aipworkspace.WorkspaceView, error) {
	return aipworkspace.WorkspaceView{ProjectRoot: "/tmp"}, nil
}

type charCatalogFilter struct{ tag string }

func (h charCatalogFilter) ID() string                      { return h.tag }
func (charCatalogFilter) Order() int                        { return 0 }
func (charCatalogFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charCatalogFilter) Handle(context.Context, *aipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	return nil
}

type charPolicy struct{ tag string }

func (h charPolicy) ID() string                      { return h.tag }
func (charPolicy) Order() int                        { return 0 }
func (charPolicy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charPolicy) Handle(context.Context, aipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionAllow, nil
}

type charFinalizer struct{ tag string }

func (h charFinalizer) ID() string { return h.tag }
func (charFinalizer) Order() int   { return 0 }
func (charFinalizer) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass}, nil
}

type charTransform struct{ tag string }

func (h charTransform) ID() string                      { return h.tag }
func (charTransform) Order() int                        { return 0 }
func (charTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charTransform) Handle(context.Context, *aipapi.Call, request.RequestMeta, request.Services) error {
	return nil
}

type charPreReq struct{ tag string }

func (h charPreReq) ID() string                      { return h.tag }
func (charPreReq) Order() int                        { return 0 }
func (charPreReq) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charPreReq) Handle(context.Context, *aipapi.Call, prerequest.Meta, prerequest.Services) (prerequest.Decision, error) {
	return prerequest.Allow(), nil
}

type charRouteHint struct{ tag string }

func (h charRouteHint) ID() string                      { return h.tag }
func (charRouteHint) Order() int                        { return 0 }
func (charRouteHint) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charRouteHint) Hint(context.Context, routehint.Input) (routehint.Result, error) {
	return routehint.Result{}, nil
}

type charCompGate struct{ tag string }

func (h charCompGate) ID() string                      { return h.tag }
func (charCompGate) Order() int                        { return 0 }
func (charCompGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charCompGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

type charAttemptTransform struct{ tag string }

func (h charAttemptTransform) ID() string                      { return h.tag }
func (charAttemptTransform) Order() int                        { return 0 }
func (charAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (charAttemptTransform) HandleAttempt(context.Context, *aipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type charStreamObserverFactory struct{ tag string }

func (h charStreamObserverFactory) ID() string                      { return h.tag }
func (charStreamObserverFactory) Order() int                        { return 0 }
func (charStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

type charTrafficObs struct{ tag string }

func (charTrafficObs) OnObservation(context.Context, traffic.Observation) error { return nil }

type charUsageObs struct{ tag string }

func (charUsageObs) OnUsage(context.Context, usage.Event) error { return nil }

type charRawSink struct{ tag string }

func (charRawSink) WriteRaw(context.Context, traffic.Leg, traffic.CaptureMeta, []byte) error {
	return nil
}

type charRedactor struct{ tag string }

func (h charRedactor) ID() string { return h.tag }

func (charRedactor) Redact(context.Context, traffic.Leg, traffic.CaptureMeta, []byte) ([]byte, error) {
	return nil, nil
}

type charCompactionObs struct{ tag string }

func (charCompactionObs) OnCompaction(context.Context, compaction.Event) error { return nil }

type charCompactionPreserver struct{ tag string }

func (p charCompactionPreserver) ID() string { return p.tag }

func (charCompactionPreserver) BeforeRequest(context.Context, *aipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (charCompactionPreserver) RequestOpened(context.Context, aipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (charCompactionPreserver) BeforeResponseRelease(context.Context, *aipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

type charSecretGuard struct{ tag string }

func (g charSecretGuard) ID() string                         { return g.tag }
func (charSecretGuard) Order() int                           { return 0 }
func (charSecretGuard) FailureMode() secretguard.FailureMode { return secretguard.FailClosed }
func (charSecretGuard) Evaluate(context.Context, *aipapi.Call, secretguard.Meta, secretguard.Services) (secretguard.Decision, error) {
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

type charLocalTurnHandler struct{ tag string }

func (h charLocalTurnHandler) ID() string                      { return h.tag }
func (charLocalTurnHandler) Order() int                        { return 0 }
func (charLocalTurnHandler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (charLocalTurnHandler) Match(context.Context, aipapi.Call, localturn.Meta) (localturn.MatchResult, error) {
	return localturn.MatchResult{}, nil
}

func (charLocalTurnHandler) Handle(context.Context, localturn.HandleInput) (localturn.Reply, error) {
	return localturn.Reply{}, nil
}

type charTerminalProvider struct{ tag string }

func (p charTerminalProvider) ID() string { return p.tag }

func (charTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

// --- Helpers ---

func charTags[T any](xs []T, tag func(T) string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		out = append(out, tag(x))
	}
	return out
}

// charBundle contributes two tagged elements on every ordered plane so both
// bundle order and within-bundle order are observable after a merge.
func charBundle(prefix string) aipfeature.FeatureBundle {
	cs := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cs, aipfeature.PlaneSubmitHooks, prefix, []sdkhooks.SubmitHook{charSubmitHook{tag: prefix + "-a"}, charSubmitHook{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneRequestPartHooks, prefix, []sdkhooks.RequestPartHook{charRequestPartHook{tag: prefix + "-a"}, charRequestPartHook{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneResponsePartHooks, prefix, []sdkhooks.ResponsePartHook{charResponsePartHook{tag: prefix + "-a"}, charResponsePartHook{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneToolReactors, prefix, []sdkhooks.ToolReactor{charToolReactor{tag: prefix + "-a"}, charToolReactor{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneSessionOpeners, prefix, []session.Opener{charOpener{tag: prefix + "-a"}, charOpener{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneWorkspaceResolvers, prefix, []aipworkspace.Resolver{charResolver{tag: prefix + "-a"}, charResolver{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCatalogFilters, prefix, []toolcatalog.Filter{charCatalogFilter{tag: prefix + "-a"}, charCatalogFilter{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallPolicies, prefix, []toolpolicy.Policy{charPolicy{tag: prefix + "-a"}, charPolicy{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizers, prefix, []toolcall.Finalizer{charFinalizer{tag: prefix + "-a"}, charFinalizer{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneRequestTransforms, prefix, []request.Transform{charTransform{tag: prefix + "-a"}, charTransform{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlanePreRequestHandlers, prefix, []prerequest.Handler{charPreReq{tag: prefix + "-a"}, charPreReq{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneRouteHintProviders, prefix, []routehint.Provider{charRouteHint{tag: prefix + "-a"}, charRouteHint{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneCompletionGates, prefix, []completion.Gate{charCompGate{tag: prefix + "-a"}, charCompGate{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, prefix, []request.AttemptTransform{charAttemptTransform{tag: prefix + "-a"}, charAttemptTransform{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, prefix, []response.StreamObserverFactory{charStreamObserverFactory{tag: prefix + "-a"}, charStreamObserverFactory{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, prefix, []traffic.Observer{charTrafficObs{tag: prefix + "-a"}, charTrafficObs{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, prefix, []usage.Observer{charUsageObs{tag: prefix + "-a"}, charUsageObs{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneRawCaptureSinks, prefix, []traffic.RawCaptureSink{charRawSink{tag: prefix + "-a"}, charRawSink{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneTrafficRedactors, prefix, []traffic.Redactor{charRedactor{tag: prefix + "-a"}, charRedactor{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneCompactionObservers, prefix, []compaction.Observer{charCompactionObs{tag: prefix + "-a"}, charCompactionObs{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneCompactionPreservers, prefix, []compaction.Preserver{charCompactionPreserver{tag: prefix + "-a"}, charCompactionPreserver{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneSecretGuards, prefix, []secretguard.Guard{charSecretGuard{tag: prefix + "-a"}, charSecretGuard{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneLocalTurnHandlers, prefix, []localturn.Handler{charLocalTurnHandler{tag: prefix + "-a"}, charLocalTurnHandler{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, prefix, terminaldecision.Provider(charTerminalProvider{tag: prefix + "-provider"}))
	lifecycles := []aipplugin.Lifecycle{charLifecycle{tag: prefix + "-a"}, charLifecycle{tag: prefix + "-b"}}
	return aipfeature.BundleFromPlanes(cs.Freeze(), lifecycles)
}

// charOrderedBundle contributes on every ordered plane without touching the
// exclusive terminal-decision slot.
func charOrderedBundle(prefix string) aipfeature.FeatureBundle {
	cs := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cs, aipfeature.PlaneSubmitHooks, prefix, []sdkhooks.SubmitHook{charSubmitHook{tag: prefix + "-a"}, charSubmitHook{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneRequestPartHooks, prefix, []sdkhooks.RequestPartHook{charRequestPartHook{tag: prefix + "-a"}, charRequestPartHook{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneResponsePartHooks, prefix, []sdkhooks.ResponsePartHook{charResponsePartHook{tag: prefix + "-a"}, charResponsePartHook{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneToolReactors, prefix, []sdkhooks.ToolReactor{charToolReactor{tag: prefix + "-a"}, charToolReactor{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneSessionOpeners, prefix, []session.Opener{charOpener{tag: prefix + "-a"}, charOpener{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneWorkspaceResolvers, prefix, []aipworkspace.Resolver{charResolver{tag: prefix + "-a"}, charResolver{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCatalogFilters, prefix, []toolcatalog.Filter{charCatalogFilter{tag: prefix + "-a"}, charCatalogFilter{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallPolicies, prefix, []toolpolicy.Policy{charPolicy{tag: prefix + "-a"}, charPolicy{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizers, prefix, []toolcall.Finalizer{charFinalizer{tag: prefix + "-a"}, charFinalizer{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneRequestTransforms, prefix, []request.Transform{charTransform{tag: prefix + "-a"}, charTransform{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlanePreRequestHandlers, prefix, []prerequest.Handler{charPreReq{tag: prefix + "-a"}, charPreReq{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneRouteHintProviders, prefix, []routehint.Provider{charRouteHint{tag: prefix + "-a"}, charRouteHint{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneCompletionGates, prefix, []completion.Gate{charCompGate{tag: prefix + "-a"}, charCompGate{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, prefix, []request.AttemptTransform{charAttemptTransform{tag: prefix + "-a"}, charAttemptTransform{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, prefix, []response.StreamObserverFactory{charStreamObserverFactory{tag: prefix + "-a"}, charStreamObserverFactory{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, prefix, []traffic.Observer{charTrafficObs{tag: prefix + "-a"}, charTrafficObs{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, prefix, []usage.Observer{charUsageObs{tag: prefix + "-a"}, charUsageObs{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneRawCaptureSinks, prefix, []traffic.RawCaptureSink{charRawSink{tag: prefix + "-a"}, charRawSink{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneTrafficRedactors, prefix, []traffic.Redactor{charRedactor{tag: prefix + "-a"}, charRedactor{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneCompactionObservers, prefix, []compaction.Observer{charCompactionObs{tag: prefix + "-a"}, charCompactionObs{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneCompactionPreservers, prefix, []compaction.Preserver{charCompactionPreserver{tag: prefix + "-a"}, charCompactionPreserver{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneSecretGuards, prefix, []secretguard.Guard{charSecretGuard{tag: prefix + "-a"}, charSecretGuard{tag: prefix + "-b"}})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneLocalTurnHandlers, prefix, []localturn.Handler{charLocalTurnHandler{tag: prefix + "-a"}, charLocalTurnHandler{tag: prefix + "-b"}})
	lifecycles := []aipplugin.Lifecycle{charLifecycle{tag: prefix + "-a"}, charLifecycle{tag: prefix + "-b"}}
	return aipfeature.BundleFromPlanes(cs.Freeze(), lifecycles)
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

// --- Tests ---

// Pins requirement 1.1: contributions concatenate across every ordered plane in
// bundle (registration) order with within-bundle order preserved and no
// reordering; zero-length contributions are inert. Lifecycles ride the same
// ordered concatenation at the merge layer even though they leave through the
// side channel at transport time.
func TestMergeBundlesGenerated_orderedConcatenationAcrossAllPlanes(t *testing.T) {
	t.Parallel()

	emptyD := aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1}
	merged, err := MergeBundlesGenerated(charOrderedBundle("A"), charOrderedBundle("B"), emptyD, charOrderedBundle("C"))
	require.NoError(t, err)

	want := []string{"A-a", "A-b", "B-a", "B-b", "C-a", "C-b"}
	tests := []struct {
		name string
		got  func(GeneratedMergeSurface) []string
	}{
		{"Lifecycles", func(m GeneratedMergeSurface) []string {
			return charTags(m.Lifecycles, func(l aipplugin.Lifecycle) string {
				if lc, ok := l.(charLifecycle); ok {
					return lc.tag
				}
				return ""
			})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, want, tt.got(merged))
		})
	}

	// Exclusive slot: a later bundle's provider lands after ordered slice
	// contributions; the single contribution wins the one slot in GeneratedMergeSurface.
	provider := charTerminalProvider{tag: "C-provider"}
	csC := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(csC, aipfeature.PlaneTerminalDecisionProvider, "C", terminaldecision.Provider(provider)))
	finalGen, err := MergeBundlesGenerated(charOrderedBundle("A"), charOrderedBundle("B"),
		aipfeature.BundleFromPlanes(csC.Freeze(), nil))
	require.NoError(t, err)
	require.Equal(t, provider, aipfeature.Get(finalGen.Frozen, aipfeature.PlaneTerminalDecisionProvider))
}

func TestMergeBundlesGenerated_nilVsEmptySemantics(t *testing.T) {
	t.Parallel()

	t.Run("no_bundles_keeps_all_slice_fields_nil", func(t *testing.T) {
		t.Parallel()
		merged, err := MergeBundlesGenerated()
		require.NoError(t, err)
		require.Nil(t, merged.Lifecycles)
	})

	t.Run("zero_value_bundles_keep_all_slice_fields_nil", func(t *testing.T) {
		t.Parallel()
		merged, err := MergeBundlesGenerated(aipfeature.FeatureBundle{}, aipfeature.FeatureBundle{})
		require.NoError(t, err)
		require.Nil(t, merged.Lifecycles)
	})

	t.Run("explicitly_empty_bundle_slices_survive_as_non_nil", func(t *testing.T) {
		t.Parallel()
		bundle := aipfeature.FeatureBundle{
			SchemaVersion: aipfeature.SchemaVersionV1,
			Lifecycles:    []aipplugin.Lifecycle{},
		}
		merged, err := MergeBundlesGenerated(bundle)
		require.NoError(t, err)
		require.NotNil(t, merged.Lifecycles)
		require.Len(t, merged.Lifecycles, 0)
	})

	t.Run("populated_contributions_materialize_non_nil_slices", func(t *testing.T) {
		t.Parallel()
		merged, err := MergeBundlesGenerated(charBundle("A"))
		require.NoError(t, err)
		require.Len(t, merged.Lifecycles, 2)
		require.False(t, reflect.ValueOf(merged.Lifecycles).IsNil())
	})
}

// Pins validate-before-mutate: a rejected contribution leaves the accumulated
// candidate byte-for-byte unchanged, so lifecycles/hooks already merged are
// neither lost nor reordered by a failing merge replay.
func TestMergeBundlesGenerated_failureLeavesCandidateDiscarded(t *testing.T) {
	t.Parallel()

	gen, err := MergeBundlesGenerated(charBundle("A"), charBundle("B"))
	require.Error(t, err)
	require.ErrorIs(t, err, aipfeature.ErrExclusiveConflict)
	require.Contains(t, err.Error(), "A-provider")
	require.Contains(t, err.Error(), "B-provider")
	require.Equal(t, GeneratedMergeSurface{}, gen)
}

// Pins fail-closed candidate discard: when a later bundle conflicts, the whole
// accumulated candidate (including earlier lifecycles) is discarded rather
// than returned partially merged.
func TestMergeBundlesGenerated_conflictDiscardsAccumulatedCandidate(t *testing.T) {
	t.Parallel()

	merged, err := MergeBundlesGenerated(
		charBundle("A"),
		charBundle("B"),
	)
	require.Error(t, err)
	require.ErrorIs(t, err, aipfeature.ErrExclusiveConflict)
	require.Equal(t, GeneratedMergeSurface{}, merged)
}
