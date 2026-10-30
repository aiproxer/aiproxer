package runtimebundle

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
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
	sdksg "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
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
)

// --- Minimal Typed-Nil Stubs for Parity Characterization ---

type charStubTerminalProvider struct {
	id    string
	panic bool
}

func (p *charStubTerminalProvider) ID() string {
	if p == nil {
		return ""
	}
	if p.panic {
		panic("boom-terminal-provider-id")
	}
	return p.id
}

func (p *charStubTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "stop"}, nil
}

type charStubLocalTurnHandler struct {
	id  string
	ord int
}

func (h *charStubLocalTurnHandler) ID() string {
	if h == nil {
		return ""
	}
	return h.id
}

func (h *charStubLocalTurnHandler) Order() int {
	if h == nil {
		return 0
	}
	return h.ord
}
func (h *charStubLocalTurnHandler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (h *charStubLocalTurnHandler) Match(context.Context, aipapi.Call, localturn.Meta) (localturn.MatchResult, error) {
	return localturn.MatchResult{}, nil
}

func (h *charStubLocalTurnHandler) Handle(context.Context, localturn.HandleInput) (localturn.Reply, error) {
	return localturn.Reply{}, nil
}

type charStubSubmitHook struct{ tag string }

func (h charStubSubmitHook) ID() string                      { return h.tag }
func (charStubSubmitHook) Order() int                        { return 0 }
func (charStubSubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubSubmitHook) Handle(context.Context, *aipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, nil
}

type charStubRequestPartHook struct{ tag string }

func (h charStubRequestPartHook) ID() string                      { return h.tag }
func (charStubRequestPartHook) Order() int                        { return 0 }
func (charStubRequestPartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubRequestPartHook) HandleRequestParts(context.Context, *aipapi.Call, sdkhooks.PartMeta) error {
	return nil
}

type charStubResponsePartHook struct{ tag string }

func (h charStubResponsePartHook) ID() string                      { return h.tag }
func (charStubResponsePartHook) Order() int                        { return 0 }
func (charStubResponsePartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubResponsePartHook) HandleEvent(context.Context, *aipapi.Event, sdkhooks.PartMeta) error {
	return nil
}

type charStubToolReactor struct{ tag string }

func (h charStubToolReactor) ID() string { return h.tag }
func (charStubToolReactor) Order() int   { return 0 }
func (charStubToolReactor) HandleToolEvent(context.Context, aipapi.ToolEvent, sdkhooks.ToolMeta) (sdkhooks.ToolDecision, aipapi.ToolEvent, error) {
	return sdkhooks.ToolPass, aipapi.ToolEvent{}, nil
}

type charStubLifecycle struct{ tag string }

func (charStubLifecycle) Start(context.Context) error { return nil }
func (charStubLifecycle) Stop(context.Context) error  { return nil }

type charStubOpener struct{ tag string }

func (h charStubOpener) ID() string { return h.tag }
func (charStubOpener) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

type charStubResolver struct{ tag string }

func (charStubResolver) Resolve(context.Context) (aipworkspace.WorkspaceView, error) {
	return aipworkspace.WorkspaceView{ProjectRoot: "/tmp"}, nil
}

type charStubCatalogFilter struct{ tag string }

func (h charStubCatalogFilter) ID() string                      { return h.tag }
func (charStubCatalogFilter) Order() int                        { return 0 }
func (charStubCatalogFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubCatalogFilter) Handle(context.Context, *aipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	return nil
}

type charStubPolicy struct{ tag string }

func (h *charStubPolicy) ID() string {
	if h == nil {
		return ""
	}
	return h.tag
}
func (h *charStubPolicy) Order() int                        { return 0 }
func (h *charStubPolicy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (h *charStubPolicy) Handle(context.Context, aipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionAllow, nil
}

type charStubFinalizer struct{ tag string }

func (h *charStubFinalizer) ID() string {
	if h == nil {
		return ""
	}
	return h.tag
}
func (h *charStubFinalizer) Order() int { return 0 }
func (h *charStubFinalizer) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass}, nil
}

type charStubTransform struct{ tag string }

func (h *charStubTransform) ID() string {
	if h == nil {
		return ""
	}
	return h.tag
}
func (h *charStubTransform) Order() int                        { return 0 }
func (h *charStubTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (h *charStubTransform) Handle(context.Context, *aipapi.Call, request.RequestMeta, request.Services) error {
	return nil
}

type charStubPreReq struct{ tag string }

func (h *charStubPreReq) ID() string {
	if h == nil {
		return ""
	}
	return h.tag
}
func (h *charStubPreReq) Order() int                        { return 0 }
func (h *charStubPreReq) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (h *charStubPreReq) Handle(context.Context, *aipapi.Call, prerequest.Meta, prerequest.Services) (prerequest.Decision, error) {
	return prerequest.Allow(), nil
}

type charStubRouteHint struct{ tag string }

func (h charStubRouteHint) ID() string                      { return h.tag }
func (charStubRouteHint) Order() int                        { return 0 }
func (charStubRouteHint) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubRouteHint) Hint(context.Context, routehint.Input) (routehint.Result, error) {
	return routehint.Result{}, nil
}

type charStubCompGate struct{ tag string }

func (h *charStubCompGate) ID() string {
	if h == nil {
		return ""
	}
	return h.tag
}
func (h *charStubCompGate) Order() int                        { return 0 }
func (h *charStubCompGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubCompGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

type charStubAttemptTransform struct{ tag string }

func (h charStubAttemptTransform) ID() string                      { return h.tag }
func (charStubAttemptTransform) Order() int                        { return 0 }
func (charStubAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (charStubAttemptTransform) HandleAttempt(context.Context, *aipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type charStubStreamObserverFactory struct{ tag string }

func (h charStubStreamObserverFactory) ID() string                      { return h.tag }
func (charStubStreamObserverFactory) Order() int                        { return 0 }
func (charStubStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

type charStubTrafficObs struct{ tag string }

func (charStubTrafficObs) OnObservation(context.Context, traffic.Observation) error { return nil }

type charStubUsageObs struct{ tag string }

func (charStubUsageObs) OnUsage(context.Context, usage.Event) error { return nil }

type charStubRawSink struct{ tag string }

func (charStubRawSink) WriteRaw(context.Context, traffic.Leg, traffic.CaptureMeta, []byte) error {
	return nil
}

type charStubRedactor struct{ tag string }

func (h *charStubRedactor) ID() string {
	if h == nil {
		return ""
	}
	return h.tag
}

func (h *charStubRedactor) Redact(context.Context, traffic.Leg, traffic.CaptureMeta, []byte) ([]byte, error) {
	return nil, nil
}

type charStubCompactionObs struct{ tag string }

func (charStubCompactionObs) OnCompaction(context.Context, compaction.Event) error { return nil }

type charStubCompactionPreserver struct{ tag string }

func (p charStubCompactionPreserver) ID() string { return p.tag }
func (charStubCompactionPreserver) BeforeRequest(context.Context, *aipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (charStubCompactionPreserver) RequestOpened(context.Context, aipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (charStubCompactionPreserver) BeforeResponseRelease(context.Context, *aipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

type charStubSGGuard struct {
	id  string
	ord int
}

func (g *charStubSGGuard) ID() string {
	if g == nil {
		return ""
	}
	return g.id
}

func (g *charStubSGGuard) Order() int {
	if g == nil {
		return 0
	}
	return g.ord
}
func (g *charStubSGGuard) FailureMode() sdksg.FailureMode { return sdksg.FailClosed }
func (g *charStubSGGuard) Evaluate(context.Context, *aipapi.Call, sdksg.Meta, sdksg.Services) (sdksg.Decision, error) {
	return sdksg.Decision{Outcome: sdksg.OutcomePass}, nil
}

// --- Acceptance Criteria 3: Terminal-Decision Typed-Nil & Fail-Before-Mutate ---

// TestTerminalDecision_TypedNilFailBeforeMutateAndCompileGeneration pins Requirement 1.2, 1.4, 4.2:
// - FeatureBundle.Validate() returns ErrInvalidProvider on typed-nil provider.
// - MergeBundlesGenerated / ContributeBundle returns ErrInvalidProvider on typed-nil provider.
// - Merge fails BEFORE any candidate mutation: fields on GeneratedMergeSurface remain identical.
// - Merge validates candidate and incoming provider identity before checking conflict.
// - CompileGeneration rejects typed-nil provider fail-closed.
func TestTerminalDecision_TypedNilFailBeforeMutateAndCompileGeneration(t *testing.T) {
	t.Parallel()

	var typedNilProvider terminaldecision.Provider = (*charStubTerminalProvider)(nil)

	t.Run("feature_bundle_validate_rejects_typed_nil", func(t *testing.T) {
		t.Parallel()
		cs := aipfeature.NewContributionSet()
		err := aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "feat", typedNilProvider)
		require.Error(t, err)
		assert.True(t, errors.Is(err, terminaldecision.ErrInvalidProvider))
		assert.Contains(t, err.Error(), "terminaldecision: invalid provider")
	})

	t.Run("merge_bundles_generated_rejects_typed_nil_and_fails_before_mutate", func(t *testing.T) {
		t.Parallel()
		cs := aipfeature.NewContributionSet()
		err := aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "feat", typedNilProvider)
		require.Error(t, err)
		assert.True(t, errors.Is(err, terminaldecision.ErrInvalidProvider))
	})

	t.Run("incoming_typed_nil_fails_identity_before_conflict_check", func(t *testing.T) {
		t.Parallel()
		cs := aipfeature.NewContributionSet()
		require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "valid-active", terminaldecision.Provider(&charStubTerminalProvider{id: "valid-active"})))
		err := aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "feat", typedNilProvider)
		require.Error(t, err)
		assert.True(t, errors.Is(err, terminaldecision.ErrInvalidProvider))
		assert.False(t, errors.Is(err, aipfeature.ErrExclusiveConflict))
	})

	t.Run("compile_generation_rejects_typed_nil_provider", func(t *testing.T) {
		t.Parallel()
		reg := pluginreg.NewRegistry()
		require.NoError(t, reg.RegisterFeature("term-feat-nil", func(yaml.Node) (aipfeature.FeatureBundle, error) {
			cs := aipfeature.NewContributionSet()
			if err := aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "term-feat-nil", typedNilProvider); err != nil {
				return aipfeature.FeatureBundle{}, err
			}
			return aipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
		}))
		cfg := &config.Config{
			Routing:     config.RoutingConfig{MaxAttempts: 3},
			Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
			Server:      config.ServerConfig{MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096},
			Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
			Plugins: config.PluginsConfig{
				Features: []config.PluginConfig{
					{ID: "term-feat-nil", Enabled: true},
				},
			},
		}
		require.NoError(t, config.Validate(cfg))

		ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
			Cfg:  cfg,
			Log:  slog.Default(),
			Opts: &BuildOptions{PluginRegistry: reg},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = ps.Close() })

		gen, err := CompileGeneration(context.Background(), GenerationCompileInput{
			Process: ps,
			Compose: func(context.Context, *config.Config, *slog.Logger, httpcontract.StandardHTTPInput) (http.Handler, error) {
				return http.NotFoundHandler(), nil
			},
		})
		require.Error(t, err)
		assert.Nil(t, gen)
		assert.True(t, errors.Is(err, terminaldecision.ErrInvalidProvider))
	})
}

// --- Acceptance Criteria 4: Nil Policy for Ordered Interface-Valued Planes ---

// TestPlaneParity_OrderedInterfacePlanesNilPolicyCensus characterizes the exact nil policy
// across all 24 ordered interface-valued planes:
// 1. FeatureBundle.Validate():
//   - Exactly 4 planes reject nil interface elements (AttemptTransforms, StreamObserverFactories, CompactionPreservers, LocalTurnHandlers).
//   - The remaining 20 planes accept slices with nil interface elements without error.
//
// 2. MergeBundlesGenerated():
//   - All 24 planes concatenate slices verbatim, preserving nil interface elements and relative slice positions.
//
// 3. RequestRuntimeSnapshot:
//   - MaterializeSorted filters nil on: SecretGuards (filters untyped/typed nil), LocalTurnHandlers (filters untyped/typed nil), ToolCallFinalizers (filters untyped nil), TrafficRedactors (filters untyped nil).
//   - toolpolicy.MaterializeSorted sorts non-nils and preserves nils at the end.
//   - Cloned planes preserve nil elements (SessionOpeners, ToolCatalogFilters, RouteHintProviders, CompactionObservers, CompactionPreservers, RequestTransforms, CompletionGates).
//   - Strict planes panic if nil passed: AttemptTransforms, StreamObserverFactories.
func TestPlaneParity_OrderedInterfacePlanesNilPolicyCensus(t *testing.T) {
	t.Parallel()

	t.Run("feature_bundle_validation_nil_policy_census", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name       string
			contribute func(*aipfeature.ContributionSet) error
			wantError  bool
			errSubstr  string
		}{
			{"SubmitHooks_accepts_nil", func(cs *aipfeature.ContributionSet) error {
				return aipfeature.Contribute(cs, aipfeature.PlaneSubmitHooks, "p", []sdkhooks.SubmitHook{nil})
			}, false, ""},
			{"RequestPartHooks_accepts_nil", func(cs *aipfeature.ContributionSet) error {
				return aipfeature.Contribute(cs, aipfeature.PlaneRequestPartHooks, "p", []sdkhooks.RequestPartHook{nil})
			}, false, ""},
			{"ResponsePartHooks_accepts_nil", func(cs *aipfeature.ContributionSet) error {
				return aipfeature.Contribute(cs, aipfeature.PlaneResponsePartHooks, "p", []sdkhooks.ResponsePartHook{nil})
			}, false, ""},
			{"ToolReactors_accepts_nil", func(cs *aipfeature.ContributionSet) error {
				return aipfeature.Contribute(cs, aipfeature.PlaneToolReactors, "p", []sdkhooks.ToolReactor{nil})
			}, false, ""},
			{"SessionOpeners_accepts_nil", func(cs *aipfeature.ContributionSet) error {
				return aipfeature.Contribute(cs, aipfeature.PlaneSessionOpeners, "p", []session.Opener{nil})
			}, false, ""},
			{"WorkspaceResolvers_accepts_nil", func(cs *aipfeature.ContributionSet) error {
				return aipfeature.Contribute(cs, aipfeature.PlaneWorkspaceResolvers, "p", []aipworkspace.Resolver{nil})
			}, false, ""},
			{"ToolCatalogFilters_accepts_nil", func(cs *aipfeature.ContributionSet) error {
				return aipfeature.Contribute(cs, aipfeature.PlaneToolCatalogFilters, "p", []toolcatalog.Filter{nil})
			}, false, ""},
			{"ToolCallPolicies_accepts_nil", func(cs *aipfeature.ContributionSet) error {
				return aipfeature.Contribute(cs, aipfeature.PlaneToolCallPolicies, "p", []toolpolicy.Policy{nil})
			}, false, ""},
			{"ToolCallFinalizers_accepts_nil", func(cs *aipfeature.ContributionSet) error {
				return aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizers, "p", []toolcall.Finalizer{nil})
			}, false, ""},
			{"RequestTransforms_accepts_nil", func(cs *aipfeature.ContributionSet) error {
				return aipfeature.Contribute(cs, aipfeature.PlaneRequestTransforms, "p", []request.Transform{nil})
			}, false, ""},
			{"PreRequestHandlers_accepts_nil", func(cs *aipfeature.ContributionSet) error {
				return aipfeature.Contribute(cs, aipfeature.PlanePreRequestHandlers, "p", []prerequest.Handler{nil})
			}, false, ""},
			{"RouteHintProviders_accepts_nil", func(cs *aipfeature.ContributionSet) error {
				return aipfeature.Contribute(cs, aipfeature.PlaneRouteHintProviders, "p", []routehint.Provider{nil})
			}, false, ""},
			{"CompletionGates_accepts_nil", func(cs *aipfeature.ContributionSet) error {
				return aipfeature.Contribute(cs, aipfeature.PlaneCompletionGates, "p", []completion.Gate{nil})
			}, false, ""},
			{"TrafficObservers_accepts_nil", func(cs *aipfeature.ContributionSet) error {
				return aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, "p", []traffic.Observer{nil})
			}, false, ""},
			{"UsageObservers_accepts_nil", func(cs *aipfeature.ContributionSet) error {
				return aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, "p", []usage.Observer{nil})
			}, false, ""},
			{"RawCaptureSinks_accepts_nil", func(cs *aipfeature.ContributionSet) error {
				return aipfeature.Contribute(cs, aipfeature.PlaneRawCaptureSinks, "p", []traffic.RawCaptureSink{nil})
			}, false, ""},
			{"TrafficRedactors_accepts_nil", func(cs *aipfeature.ContributionSet) error {
				return aipfeature.Contribute(cs, aipfeature.PlaneTrafficRedactors, "p", []traffic.Redactor{nil})
			}, false, ""},
			{"CompactionObservers_accepts_nil", func(cs *aipfeature.ContributionSet) error {
				return aipfeature.Contribute(cs, aipfeature.PlaneCompactionObservers, "p", []compaction.Observer{nil})
			}, false, ""},
			{"SecretGuards_accepts_nil", func(cs *aipfeature.ContributionSet) error {
				return aipfeature.Contribute(cs, aipfeature.PlaneSecretGuards, "p", []sdksg.Guard{nil})
			}, false, ""},

			// Strictly rejecting planes:
			{"AttemptTransforms_rejects_nil", func(cs *aipfeature.ContributionSet) error {
				return aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "p", []request.AttemptTransform{nil})
			}, true, "AttemptTransforms[0] must not be nil"},
			{"StreamObserverFactories_rejects_nil", func(cs *aipfeature.ContributionSet) error {
				return aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "p", []response.StreamObserverFactory{nil})
			}, true, "StreamObserverFactories[0] must not be nil"},
			{"CompactionPreservers_rejects_nil", func(cs *aipfeature.ContributionSet) error {
				return aipfeature.Contribute(cs, aipfeature.PlaneCompactionPreservers, "p", []compaction.Preserver{nil})
			}, true, "CompactionPreservers[0] must not be nil"},
			{"LocalTurnHandlers_rejects_nil", func(cs *aipfeature.ContributionSet) error {
				return aipfeature.Contribute(cs, aipfeature.PlaneLocalTurnHandlers, "p", []localturn.Handler{nil})
			}, true, "LocalTurnHandlers[0] must not be nil"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				cs := aipfeature.NewContributionSet()
				err := tt.contribute(cs)
				if tt.wantError {
					require.Error(t, err)
					assert.Contains(t, err.Error(), tt.errSubstr)
				} else {
					require.NoError(t, err)
					bundle := aipfeature.BundleFromPlanes(cs.Freeze(), nil)
					require.NoError(t, bundle.Validate())
				}
			})
		}
	})

	t.Run("merge_bundles_generated_preserves_nil_elements_verbatim", func(t *testing.T) {
		t.Parallel()
		b := aipfeature.FeatureBundle{
			SchemaVersion: aipfeature.SchemaVersionV1,
			Lifecycles:    []aipplugin.Lifecycle{nil, charStubLifecycle{tag: "l1"}, nil},
		}

		gen, err := featurebundle.MergeBundlesGenerated(b)
		require.NoError(t, err)
		require.Len(t, gen.Lifecycles, 3)
		assert.Nil(t, gen.Lifecycles[0])
		assert.NotNil(t, gen.Lifecycles[1])
		assert.Nil(t, gen.Lifecycles[2])
	})

	t.Run("secret_guards_plane_and_generated_storage_preserves_literal_and_typed_nil_verbatim", func(t *testing.T) {
		t.Parallel()
		var typedNilSGGuard *charStubSGGuard
		cs := aipfeature.NewContributionSet()
		err := aipfeature.Contribute(cs, aipfeature.PlaneSecretGuards, "test-plugin", []sdksg.Guard{
			nil,
			typedNilSGGuard,
			&charStubSGGuard{id: "sg1", ord: 1},
			nil,
		})
		require.NoError(t, err)

		frozen := cs.Freeze()
		guards := aipfeature.Get(frozen, aipfeature.PlaneSecretGuards)
		require.Len(t, guards, 4)
		assert.Nil(t, guards[0])
		assert.True(t, guards[1] != nil, "boxed typed-nil must not equal untyped nil interface")
		assert.True(t, sdksg.IsNilGuard(guards[1]), "IsNilGuard must report true for typed nil")
		assert.NotNil(t, guards[2])
		assert.Equal(t, "sg1", guards[2].ID())
		assert.Nil(t, guards[3])

		// Build runtime options with FeaturePlanes
		opts := &BuildOptions{
			FeaturePlanes: frozen,
		}
		res, err := testBuildSecretGuardRuntime(&config.Config{}, slog.Default(), opts, nil)
		require.NoError(t, err)
		require.NotNil(t, res)
		// Nil-element preservation is pinned at the frozen level above; the
		// engine plane itself carries no guards (they travel on planes).
		require.Empty(t, res.Plane.Guards)

		// Runtime snapshot materialization filters both untyped nil and typed nil
		snap := extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{FeaturePlanes: frozen, SecretGuardPlane: res.Plane})
		execGuards := snap.SecretGuardExecutionPlane().Guards
		require.Len(t, execGuards, 1)
		assert.Equal(t, "sg1", execGuards[0].ID())

		// Diagnostics materialization also filters both untyped nil and typed nil
		occupants := aipfeature.PlaneSecretGuards.Diagnostics.Materialize(guards)
		require.Len(t, occupants, 1)
		assert.Equal(t, "secret_guard:sg1", occupants[0].Label)
	})
}

// TestPlaneParity_SnapshotMaterializationNilAndTypedNilFiltering tests the behavior
// of NewRequestRuntimeSnapshot on planes with nil and typed-nil elements:
// - SecretGuards: MaterializeSorted filters untyped nil and typed nil guards.
// - LocalTurnHandlers: MaterializeSorted filters untyped nil and typed nil handlers.
// - ToolCallFinalizers: MaterializeSorted filters nil.
// - TrafficRedactors: MaterializeSortedRedactors filters nil.
// - ToolCallPolicies: MaterializeSorted preserves nil (sorted to end).
// - Cloned planes (SessionOpeners, ToolCatalogFilters, RouteHintProviders, CompactionObservers, CompactionPreservers, RequestTransforms, CompletionGates) preserve nil elements.
// - Strict planes panic on nil: AttemptTransforms, StreamObserverFactories.
func TestPlaneParity_SnapshotMaterializationNilAndTypedNilFiltering(t *testing.T) {
	t.Parallel()

	var typedNilSGGuard *charStubSGGuard
	var typedNilLTHandler *charStubLocalTurnHandler
	var typedNilFinalizer *charStubFinalizer
	var typedNilRedactor *charStubRedactor

	// 1. Reflection-based filtering planes: RequestMaterializer filters BOTH untyped nil and typed nil
	sgMaterialized := aipfeature.PlaneSecretGuards.RequestMaterializer([]sdksg.Guard{
		nil,
		typedNilSGGuard,
		&charStubSGGuard{id: "sg-valid-2", ord: 20},
		&charStubSGGuard{id: "sg-valid-1", ord: 10},
		nil,
	})
	require.Len(t, sgMaterialized, 2)
	assert.Equal(t, "sg-valid-1", sgMaterialized[0].ID())
	assert.Equal(t, "sg-valid-2", sgMaterialized[1].ID())

	ltMaterialized := aipfeature.PlaneLocalTurnHandlers.RequestMaterializer([]localturn.Handler{
		nil,
		typedNilLTHandler,
		&charStubLocalTurnHandler{id: "lt-valid-2", ord: 20},
		&charStubLocalTurnHandler{id: "lt-valid-1", ord: 10},
	})
	require.Len(t, ltMaterialized, 2)
	assert.Equal(t, "lt-valid-1", ltMaterialized[0].ID())
	assert.Equal(t, "lt-valid-2", ltMaterialized[1].ID())

	cset := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cset, aipfeature.PlaneSecretGuards, "test", []sdksg.Guard{
		&charStubSGGuard{id: "sg-valid-2", ord: 20},
		&charStubSGGuard{id: "sg-valid-1", ord: 10},
	})
	_ = aipfeature.Contribute(cset, aipfeature.PlaneLocalTurnHandlers, "test", []localturn.Handler{
		&charStubLocalTurnHandler{id: "lt-valid-2", ord: 20},
		&charStubLocalTurnHandler{id: "lt-valid-1", ord: 10},
	})
	_ = aipfeature.Contribute(cset, aipfeature.PlaneToolCallFinalizers, "test", []toolcall.Finalizer{
		nil,
		typedNilFinalizer,
		&charStubFinalizer{tag: "tf-valid"},
	})
	_ = aipfeature.Contribute(cset, aipfeature.PlaneTrafficRedactors, "test", []traffic.Redactor{
		nil,
		typedNilRedactor,
		&charStubRedactor{tag: "tr-valid"},
	})
	_ = aipfeature.Contribute(cset, aipfeature.PlaneToolCallPolicies, "test", []toolpolicy.Policy{
		&charStubPolicy{tag: "tp-valid"},
		nil,
	})
	_ = aipfeature.Contribute(cset, aipfeature.PlanePreRequestHandlers, "test", []prerequest.Handler{
		&charStubPreReq{tag: "pr-valid-2"},
		&charStubPreReq{tag: "pr-valid-1"},
	})
	_ = aipfeature.Contribute(cset, aipfeature.PlaneRequestTransforms, "test", []request.Transform{
		nil,
		&charStubTransform{tag: "rt-valid"},
	})
	_ = aipfeature.Contribute(cset, aipfeature.PlaneCompletionGates, "test", []completion.Gate{
		nil,
		&charStubCompGate{tag: "cg-valid"},
	})
	_ = aipfeature.Contribute(cset, aipfeature.PlaneSessionOpeners, "test", []session.Opener{charStubOpener{tag: "so-1"}, nil})
	_ = aipfeature.Contribute(cset, aipfeature.PlaneToolCatalogFilters, "test", []toolcatalog.Filter{nil, charStubCatalogFilter{tag: "cf-1"}})
	_ = aipfeature.Contribute(cset, aipfeature.PlaneRouteHintProviders, "test", []routehint.Provider{charStubRouteHint{tag: "rh-1"}, nil})

	frozen := cset.Freeze()

	snap := extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{
		FeaturePlanes: frozen,
	})

	sgExec := snap.SecretGuardExecutionPlane().Guards
	require.Len(t, sgExec, 2)
	assert.Equal(t, "sg-valid-1", sgExec[0].ID())
	assert.Equal(t, "sg-valid-2", sgExec[1].ID())

	ltExec := snap.LocalTurnHandlers()
	require.Len(t, ltExec, 2)
	assert.Equal(t, "lt-valid-1", ltExec[0].ID())
	assert.Equal(t, "lt-valid-2", ltExec[1].ID())

	// 2. Interface nil filtering planes: filters untyped nil (typed nil with nil-safe methods retained)
	tfExec := snap.ToolCallFinalizers()
	require.Len(t, tfExec, 2)
	assert.Nil(t, tfExec[0])
	assert.Equal(t, "tf-valid", tfExec[1].ID())

	trExec := snap.TrafficRedactors()
	require.Len(t, trExec, 2)
	assert.Nil(t, trExec[0])
	assert.Equal(t, "tr-valid", trExec[1].ID())

	// 2. toolpolicy MaterializeSorted preserves nil at the end
	tpExec := snap.ToolCallPolicies()
	require.Len(t, tpExec, 2)
	assert.Equal(t, "tp-valid", tpExec[0].ID())
	assert.Nil(t, tpExec[1])

	// 3. Direct clone planes: nil elements preserved verbatim
	rtExec := snap.RequestTransforms()
	require.Len(t, rtExec, 2)
	assert.Nil(t, rtExec[0])
	assert.NotNil(t, rtExec[1])

	cgExec := snap.CompletionGates()
	require.Len(t, cgExec, 2)
	assert.Nil(t, cgExec[0])
	assert.NotNil(t, cgExec[1])

	soSnap := snap.SessionOpeners()
	require.Len(t, soSnap, 2)
	assert.NotNil(t, soSnap[0])
	assert.Nil(t, soSnap[1])

	cfSnap := snap.ToolCatalogFilters()
	require.Len(t, cfSnap, 2)
	assert.Nil(t, cfSnap[0])
	assert.NotNil(t, cfSnap[1])

	rhSnap := snap.RouteHintProviders()
	require.Len(t, rhSnap, 2)
	assert.NotNil(t, rhSnap[0])
	assert.Nil(t, rhSnap[1])

	// 4. Strict planes panic on nil
	assert.Panics(t, func() {
		aipfeature.PlaneAttemptTransforms.RequestMaterializer([]request.AttemptTransform{nil})
	})
	assert.Panics(t, func() {
		aipfeature.PlaneStreamObserverFactories.RequestMaterializer([]response.StreamObserverFactory{nil})
	})
}

// TestPlaneParity_FailBeforeMutateOnInvalidInterfaceValues verifies that an invalid
// contribution (panicking provider, invalid identity, etc.) fails BEFORE modifying
// any field of the merged GeneratedMergeSurface.
func TestPlaneParity_FailBeforeMutateOnInvalidInterfaceValues(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		provider terminaldecision.Provider
		wantErr  string
	}{
		{
			name:     "typed_nil",
			provider: (*charStubTerminalProvider)(nil),
			wantErr:  "terminaldecision: invalid provider",
		},
		{
			name:     "empty_id",
			provider: &charStubTerminalProvider{id: ""},
			wantErr:  "provider identity is required",
		},
		{
			name:     "whitespace_id",
			provider: &charStubTerminalProvider{id: "   "},
			wantErr:  "provider identity is required",
		},
		{
			name:     "panicking_id",
			provider: &charStubTerminalProvider{panic: true},
			wantErr:  "provider identity unavailable",
		},
		{
			name:     "invalid_utf8_in_id",
			provider: &charStubTerminalProvider{id: "\xff\xfe\xfd"},
			wantErr:  "provider identity is not valid UTF-8",
		},
		{
			name:     "exceeds_max_bytes_in_id",
			provider: &charStubTerminalProvider{id: strings.Repeat("a", terminaldecision.MaxProviderIDBytes+1)},
			wantErr:  "provider identity exceeds 128 bytes",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cs := aipfeature.NewContributionSet()
			err := aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, "test", tc.provider)
			if err == nil {
				b := aipfeature.BundleFromPlanes(cs.Freeze(), []aipplugin.Lifecycle{charStubLifecycle{tag: "incoming-lifecycle"}})
				_, err = featurebundle.MergeBundlesGenerated(b)
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
