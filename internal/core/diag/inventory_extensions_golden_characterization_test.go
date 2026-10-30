package diag_test

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

var updateGolden = flag.Bool("update-golden", false, "update golden test files in testdata")

// --- Stubs for Extension Inventory Characterization ---

type charStubSubmitHook struct {
	id  string
	ord int
}

func (h charStubSubmitHook) ID() string                      { return h.id }
func (h charStubSubmitHook) Order() int                      { return h.ord }
func (charStubSubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubSubmitHook) Handle(context.Context, *aipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, nil
}

type charStubRequestPartHook struct {
	id  string
	ord int
}

func (h charStubRequestPartHook) ID() string                      { return h.id }
func (h charStubRequestPartHook) Order() int                      { return h.ord }
func (charStubRequestPartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubRequestPartHook) HandleRequestParts(context.Context, *aipapi.Call, sdkhooks.PartMeta) error {
	return nil
}

type charStubResponsePartHook struct {
	id  string
	ord int
}

func (h charStubResponsePartHook) ID() string                      { return h.id }
func (h charStubResponsePartHook) Order() int                      { return h.ord }
func (charStubResponsePartHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubResponsePartHook) HandleEvent(context.Context, *aipapi.Event, sdkhooks.PartMeta) error {
	return nil
}

type charStubToolReactor struct {
	id  string
	ord int
}

func (r charStubToolReactor) ID() string { return r.id }
func (r charStubToolReactor) Order() int { return r.ord }
func (charStubToolReactor) HandleToolEvent(context.Context, aipapi.ToolEvent, sdkhooks.ToolMeta) (sdkhooks.ToolDecision, aipapi.ToolEvent, error) {
	return sdkhooks.ToolPass, aipapi.ToolEvent{}, nil
}

type charStubToolCatalogFilter struct {
	id  string
	ord int
}

func (f charStubToolCatalogFilter) ID() string                      { return f.id }
func (f charStubToolCatalogFilter) Order() int                      { return f.ord }
func (charStubToolCatalogFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubToolCatalogFilter) Handle(context.Context, *aipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	return nil
}

type charStubToolPolicy struct {
	id  string
	ord int
}

func (p charStubToolPolicy) ID() string                      { return p.id }
func (p charStubToolPolicy) Order() int                      { return p.ord }
func (charStubToolPolicy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubToolPolicy) Handle(context.Context, aipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionAllow, nil
}

type charStubToolFinalizer struct {
	id  string
	ord int
}

func (f charStubToolFinalizer) ID() string { return f.id }
func (f charStubToolFinalizer) Order() int { return f.ord }
func (charStubToolFinalizer) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass}, nil
}

type charStubRequestTransform struct {
	id  string
	ord int
}

func (t charStubRequestTransform) ID() string                      { return t.id }
func (t charStubRequestTransform) Order() int                      { return t.ord }
func (charStubRequestTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubRequestTransform) Handle(context.Context, *aipapi.Call, request.RequestMeta, request.Services) error {
	return nil
}

type charStubPreRequestHandler struct {
	id  string
	ord int
}

func (h charStubPreRequestHandler) ID() string                      { return h.id }
func (h charStubPreRequestHandler) Order() int                      { return h.ord }
func (charStubPreRequestHandler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubPreRequestHandler) Handle(context.Context, *aipapi.Call, prerequest.Meta, prerequest.Services) (prerequest.Decision, error) {
	return prerequest.Allow(), nil
}

type charStubRouteHintProvider struct {
	id  string
	ord int
}

func (p charStubRouteHintProvider) ID() string                      { return p.id }
func (p charStubRouteHintProvider) Order() int                      { return p.ord }
func (charStubRouteHintProvider) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubRouteHintProvider) Hint(context.Context, routehint.Input) (routehint.Result, error) {
	return routehint.Result{}, nil
}

type charStubSessionOpener struct {
	id string
}

func (o charStubSessionOpener) ID() string { return o.id }
func (charStubSessionOpener) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

type charStubWorkspaceResolver struct{}

func (charStubWorkspaceResolver) Resolve(context.Context) (workspace.WorkspaceView, error) {
	return workspace.WorkspaceView{}, nil
}

type charStubSecretGuard struct {
	id  string
	ord int
}

func (g charStubSecretGuard) ID() string                         { return g.id }
func (g charStubSecretGuard) Order() int                         { return g.ord }
func (charStubSecretGuard) FailureMode() secretguard.FailureMode { return secretguard.FailClosed }
func (charStubSecretGuard) Evaluate(context.Context, *aipapi.Call, secretguard.Meta, secretguard.Services) (secretguard.Decision, error) {
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

type charStubCompletionGate struct {
	id  string
	ord int
}

func (g charStubCompletionGate) ID() string                      { return g.id }
func (g charStubCompletionGate) Order() int                      { return g.ord }
func (charStubCompletionGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubCompletionGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.Outcome{}, nil
}

type charStubAttemptTransform struct {
	id  string
	ord int
}

func (t charStubAttemptTransform) ID() string                      { return t.id }
func (t charStubAttemptTransform) Order() int                      { return t.ord }
func (charStubAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (charStubAttemptTransform) HandleAttempt(context.Context, *aipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type charStubStreamObserverFactory struct {
	id  string
	ord int
}

func (f charStubStreamObserverFactory) ID() string                      { return f.id }
func (f charStubStreamObserverFactory) Order() int                      { return f.ord }
func (charStubStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (charStubStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

type charStubTrafficObserver struct{}

func (charStubTrafficObserver) OnObservation(context.Context, traffic.Observation) error { return nil }

type charStubUsageObserver struct{}

func (charStubUsageObserver) OnUsage(context.Context, usage.Event) error { return nil }

type charStubRawCaptureSink struct{}

func (charStubRawCaptureSink) WriteRaw(context.Context, traffic.Leg, traffic.CaptureMeta, []byte) error {
	return nil
}

type charStubTrafficRedactor struct {
	id string
}

func (r charStubTrafficRedactor) ID() string { return r.id }
func (charStubTrafficRedactor) Redact(context.Context, traffic.Leg, traffic.CaptureMeta, []byte) ([]byte, error) {
	return nil, nil
}

// populatedMultiFeatureRegistry implements FeatureRegistry for our characterization fixture.
type populatedMultiFeatureRegistry struct {
	bundles map[string]aipfeature.FeatureBundle
}

func (r *populatedMultiFeatureRegistry) BuildFeatureBundle(factoryKey string, _ yaml.Node) (aipfeature.FeatureBundle, error) {
	if b, ok := r.bundles[factoryKey]; ok {
		return b, nil
	}
	return aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1}, nil
}

// buildPopulatedMultiFeatureFixture constructs a deterministic, fully populated multi-feature
// configuration and extras that exercise:
// 1. Stage coalescing:
//   - StageToolEventReaction: ToolCallPolicies + ToolCallFinalizers + ToolReactors
//   - StageSessionOpen: SessionOpeners + WorkspaceResolvers
//   - StageTrafficObservation: TrafficObservers + UsageObservers + TrafficRedactors + RawCaptureSinks
//   - StageRequestWide: RequestTransforms + RequestPartHooks
//
// 2. Family-specific ordering:
//   - MaterializeSorted with Order and ID tie-breaking
//   - Slice order preservation (SessionOpeners, TrafficRedactors)
//   - Index-based labels (TrafficObservers, UsageObservers, RawCaptureSinks, WorkspaceResolvers)
//
// 3. Occupant labels across all plane prefixes.
// 4. Nil filtering across all slice-valued planes.
// 5. Privilege flags with distinct outcomes across features.
// 6. GenericPorts aggregation across features.
// 7. Dedicated SecretGuard metadata for "secrets-guard" factory kind.
// 8. Disabled feature skipping.
func buildPopulatedMultiFeatureFixture(t *testing.T) (*config.Config, *diag.InventoryExtras) {
	t.Helper()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{
				{ID: "fe-openai", Kind: "openai-responses", Enabled: true},
			},
			Backends: []config.PluginConfig{
				{ID: "be-anthropic", Kind: "anthropic", Enabled: true},
			},
			Features: []config.PluginConfig{
				{ID: "feat-alpha-sec-gate", Kind: "sec-gate", Enabled: true},
				{ID: "feat-bravo-tool-governance", Kind: "tool-governance", Enabled: true},
				{ID: "feat-charlie-traffic-metrics", Kind: "traffic-metrics", Enabled: true},
				{ID: "feat-delta-session-routing", Kind: "session-routing", Enabled: true},
				{ID: "feat-echo-stream-pipeline", Kind: "stream-pipeline", Enabled: true},
				{ID: "secrets-guard", Kind: "secrets-guard", Enabled: true},
				{ID: "feat-golf-disabled", Kind: "disabled-plugin", Enabled: false},
			},
		},
	}

	bundles := map[string]aipfeature.FeatureBundle{
		// Feature 1: Security Gateway (exercises SecretGuards, CompletionGates, RawCaptureSinks, RequestTransforms, all 3 true privileges)
		"sec-gate": func() aipfeature.FeatureBundle {
			cs := aipfeature.NewContributionSet()
			_ = aipfeature.Contribute(cs, aipfeature.PlaneSecretGuards, "sec-gate", []secretguard.Guard{
				charStubSecretGuard{id: "sg-z", ord: 20},
				charStubSecretGuard{id: "sg-a", ord: 10},
				nil, // nil filtering in secretguard.MaterializeSorted
				charStubSecretGuard{id: "sg-b", ord: 10},
			})
			_ = aipfeature.Contribute(cs, aipfeature.PlaneCompletionGates, "sec-gate", []completion.Gate{
				charStubCompletionGate{id: "cg-2", ord: 20},
				charStubCompletionGate{id: "cg-1", ord: 10},
			})
			_ = aipfeature.Contribute(cs, aipfeature.PlaneRawCaptureSinks, "sec-gate", []traffic.RawCaptureSink{
				charStubRawCaptureSink{},
				nil, // nil filtering in stageOccupancyFromBundle RawCaptureSinks
				charStubRawCaptureSink{},
			})
			_ = aipfeature.Contribute(cs, aipfeature.PlaneRequestTransforms, "sec-gate", []request.Transform{
				charStubRequestTransform{id: "rt-auth-inject", ord: 2},
				charStubRequestTransform{id: "rt-header-clean", ord: 1},
			})
			return aipfeature.BundleFromPlanes(cs.Freeze(), nil)
		}(),

		// Feature 2: Tool Governance (exercises StageToolEventReaction coalescing, StageToolCatalog, sorting, nil filtering)
		"tool-governance": func() aipfeature.FeatureBundle {
			cs := aipfeature.NewContributionSet()
			_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallPolicies, "tool-governance", []toolpolicy.Policy{
				charStubToolPolicy{id: "policy-deny-shell", ord: 20},
				nil, // nil filtering in toolpolicy.MaterializeSorted
				charStubToolPolicy{id: "policy-allow-read", ord: 10},
			})
			_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizers, "tool-governance", []toolcall.Finalizer{
				charStubToolFinalizer{id: "finalizer-clamp-args", ord: 10},
				nil, // nil filtering in toolcall.MaterializeSorted
				charStubToolFinalizer{id: "finalizer-validate-json", ord: 20},
			})
			_ = aipfeature.Contribute(cs, aipfeature.PlaneToolReactors, "tool-governance", []sdkhooks.ToolReactor{
				charStubToolReactor{id: "reactor-audit-log", ord: 30},
				charStubToolReactor{id: "reactor-early-abort", ord: 10},
			})
			_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCatalogFilters, "tool-governance", []toolcatalog.Filter{
				charStubToolCatalogFilter{id: "filter-private-tools", ord: 20},
				charStubToolCatalogFilter{id: "filter-admin-tools", ord: 10},
			})
			return aipfeature.BundleFromPlanes(cs.Freeze(), nil)
		}(),

		// Feature 3: Traffic Metrics (exercises StageTrafficObservation coalescing, index-based labels, slice order, all-false privileges)
		"traffic-metrics": func() aipfeature.FeatureBundle {
			cs := aipfeature.NewContributionSet()
			_ = aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, "traffic-metrics", []traffic.Observer{
				charStubTrafficObserver{},
				nil, // nil filtering
				charStubTrafficObserver{},
			})
			_ = aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, "traffic-metrics", []usage.Observer{
				charStubUsageObserver{},
				nil, // nil filtering
				charStubUsageObserver{},
			})
			_ = aipfeature.Contribute(cs, aipfeature.PlaneTrafficRedactors, "traffic-metrics", []traffic.Redactor{
				charStubTrafficRedactor{id: "redact-auth-token"},
				nil, // nil filtering
				charStubTrafficRedactor{id: "redact-ssn"},
			})
			return aipfeature.BundleFromPlanes(cs.Freeze(), nil)
		}(),

		// Feature 4: Session & Routing (exercises StageSessionOpen coalescing, PreRequest, RouteHinting)
		"session-routing": func() aipfeature.FeatureBundle {
			cs := aipfeature.NewContributionSet()
			_ = aipfeature.Contribute(cs, aipfeature.PlaneSessionOpeners, "session-routing", []session.Opener{
				charStubSessionOpener{id: "opener-user-session"},
				nil, // nil filtering
				charStubSessionOpener{id: "opener-tenant-session"},
			})
			_ = aipfeature.Contribute(cs, aipfeature.PlaneWorkspaceResolvers, "session-routing", []workspace.Resolver{
				charStubWorkspaceResolver{},
				nil, // nil filtering
				charStubWorkspaceResolver{},
			})
			_ = aipfeature.Contribute(cs, aipfeature.PlanePreRequestHandlers, "session-routing", []prerequest.Handler{
				charStubPreRequestHandler{id: "prereq-rate-limit", ord: 20},
				charStubPreRequestHandler{id: "prereq-auth-check", ord: 10},
			})
			_ = aipfeature.Contribute(cs, aipfeature.PlaneRouteHintProviders, "session-routing", []routehint.Provider{
				charStubRouteHintProvider{id: "hint-low-latency", ord: 20},
				nil, // nil filtering
				charStubRouteHintProvider{id: "hint-cost-tier", ord: 10},
			})
			return aipfeature.BundleFromPlanes(cs.Freeze(), nil)
		}(),

		// Feature 5: Stream Pipeline (exercises SubmitHooks, RequestPartHooks, ResponsePartHooks, AttemptTransforms, StreamObserverFactories, GenericPorts)
		"stream-pipeline": func() aipfeature.FeatureBundle {
			cs := aipfeature.NewContributionSet()
			_ = aipfeature.Contribute(cs, aipfeature.PlaneSubmitHooks, "stream-pipeline", []sdkhooks.SubmitHook{
				charStubSubmitHook{id: "submit-telemetry", ord: 20},
				charStubSubmitHook{id: "submit-preflight", ord: 10},
			})
			_ = aipfeature.Contribute(cs, aipfeature.PlaneRequestPartHooks, "stream-pipeline", []sdkhooks.RequestPartHook{
				charStubRequestPartHook{id: "req-part-metadata", ord: 10},
			})
			_ = aipfeature.Contribute(cs, aipfeature.PlaneResponsePartHooks, "stream-pipeline", []sdkhooks.ResponsePartHook{
				charStubResponsePartHook{id: "resp-part-mask", ord: 20},
				charStubResponsePartHook{id: "resp-part-tag", ord: 10},
			})
			_ = aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "stream-pipeline", []request.AttemptTransform{
				charStubAttemptTransform{id: "attempt-retry-backoff", ord: 20},
				charStubAttemptTransform{id: "attempt-circuit-breaker", ord: 10},
			})
			_ = aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "stream-pipeline", []response.StreamObserverFactory{
				charStubStreamObserverFactory{id: "stream-latency-tracker", ord: 20},
				charStubStreamObserverFactory{id: "stream-token-counter", ord: 10},
			})
			return aipfeature.BundleFromPlanes(cs.Freeze(), nil)
		}(),

		// Feature 6: Dedicated secrets-guard feature
		"secrets-guard": func() aipfeature.FeatureBundle {
			cs := aipfeature.NewContributionSet()
			_ = aipfeature.Contribute(cs, aipfeature.PlaneSecretGuards, "secrets-guard", []secretguard.Guard{
				charStubSecretGuard{id: "sg-core-pattern", ord: 10},
			})
			return aipfeature.BundleFromPlanes(cs.Freeze(), nil)
		}(),

		// Feature 7: Disabled feature
		"disabled-plugin": func() aipfeature.FeatureBundle {
			cs := aipfeature.NewContributionSet()
			_ = aipfeature.Contribute(cs, aipfeature.PlaneSubmitHooks, "disabled-plugin", []sdkhooks.SubmitHook{
				charStubSubmitHook{id: "disabled-submit", ord: 1},
			})
			return aipfeature.BundleFromPlanes(cs.Freeze(), nil)
		}(),
	}

	registry := &populatedMultiFeatureRegistry{bundles: bundles}
	extras := &diag.InventoryExtras{
		Reg:                          registry,
		Registrations:                config.RegistrationsFromConfig(cfg),
		SecretGuardCatalogEntryCount: 16,
		SecretGuardSourceCategories:  []string{"proxy_env", "operator_env", "request_credential"},
		SecretGuardAccessMode:        "strict_isolation",
		SecretGuardAction:            "block_and_audit",
	}
	merged, err := featurebundle.MergeFeatureSurfacesWithHost(registry, extras.Registrations, featurebundle.HostContributions{})
	require.NoError(t, err)
	// The compiled inventory read model (projection reduction plus the
	// secret-guard instance association) is computed by the featurehost
	// authority, exactly as the generation compile path does.
	extras.FeatureMetadata = featurehost.InventoryMetadataForConfig(cfg, featurehost.InventoryMetadataFromMergeSurface(merged), &diag.InventorySecretGuard{
		CatalogEntryCount: 16,
		SourceCategories:  []string{"proxy_env", "operator_env", "request_credential"},
		AccessMode:        "strict_isolation",
		Action:            "block_and_audit",
	})

	return cfg, extras
}

// stageOccupancyFromBundle reduces one compiled bundle's diagnostic
// projections through the featurehost inventory authority; diagnostics itself
// performs no feature-plane reduction.
func stageOccupancyFromBundle(b aipfeature.FeatureBundle) []diag.InventoryStageOccupancy {
	frozen, err := featurebundle.FreezeBundle(b, "feature")
	if err != nil {
		return []diag.InventoryStageOccupancy{}
	}
	metadata := featurehost.InventoryFeatureMetadataFromProjections("feature", aipfeature.ProjectDiagnostics(frozen))
	if metadata.StageOccupancy == nil {
		return []diag.InventoryStageOccupancy{}
	}
	return metadata.StageOccupancy
}

// Golden regeneration path:
// To regenerate the golden file when intentionally changing diagnostic snapshot formats:
//
//	go test ./internal/core/diag -run TestInventoryExtensions_PopulatedMultiFeatureGolden -update-golden
//
// Or set UPDATE_GOLDEN=1 in your environment when running tests.
func TestInventoryExtensions_PopulatedMultiFeatureGolden(t *testing.T) {
	t.Parallel()

	cfg, extras := buildPopulatedMultiFeatureFixture(t)
	snapshot, err := diag.InventorySnapshotForConfig(t.Context(), cfg, extras)
	require.NoError(t, err)

	gotBytes, err := json.MarshalIndent(snapshot, "", "  ")
	require.NoError(t, err)
	gotBytes = append(gotBytes, '\n')

	goldenPath := filepath.Join("testdata", "extension_inventory_populated_golden.json")

	if *updateGolden || os.Getenv("UPDATE_GOLDEN") == "1" {
		err := os.WriteFile(goldenPath, gotBytes, 0o600)
		require.NoError(t, err, "failed to update golden file")
	}

	wantBytes, err := os.ReadFile(goldenPath)
	require.NoError(t, err, "failed to read golden file; run with -update-golden to generate initial golden")

	require.Equal(t, string(wantBytes), string(gotBytes),
		"extension inventory golden mismatch; update golden only with intentional diagnostic contract changes")
}

// TestInventoryExtensions_StageCoalescingCharacterization explicitly asserts that coalesced stages
// correctly combine multiple planes into unified occupancy records with stable ordering and prefixes.
func TestInventoryExtensions_StageCoalescingCharacterization(t *testing.T) {
	t.Parallel()

	cfg, extras := buildPopulatedMultiFeatureFixture(t)
	snapshot, err := diag.InventorySnapshotForConfig(t.Context(), cfg, extras)
	require.NoError(t, err)

	featMap := make(map[string]diag.InventoryFeatureExtensions)
	for _, f := range snapshot.Extensions.Features {
		featMap[f.InstanceID] = f
	}

	// 1. Tool Event Reaction coalescing (ToolCallPolicies + ToolCallFinalizers + ToolReactors)
	toolFeat := featMap["feat-bravo-tool-governance"]
	var toolReaction *diag.InventoryStageOccupancy
	for i := range toolFeat.StageOccupancy {
		if toolFeat.StageOccupancy[i].StageID == extensions.StageToolEventReaction {
			toolReaction = &toolFeat.StageOccupancy[i]
			break
		}
	}
	require.NotNil(t, toolReaction, "missing StageToolEventReaction occupancy")
	require.Equal(t, 6, toolReaction.Count)
	require.Equal(t, []string{
		"tool_policy:policy-allow-read",
		"tool_policy:policy-deny-shell",
		"tool_finalizer:finalizer-clamp-args",
		"tool_finalizer:finalizer-validate-json",
		"reactor-early-abort",
		"reactor-audit-log",
	}, toolReaction.HandlerIDs)

	// 2. Session Open coalescing (SessionOpeners + WorkspaceResolvers)
	sessFeat := featMap["feat-delta-session-routing"]
	var sessOpen *diag.InventoryStageOccupancy
	for i := range sessFeat.StageOccupancy {
		if sessFeat.StageOccupancy[i].StageID == extensions.StageSessionOpen {
			sessOpen = &sessFeat.StageOccupancy[i]
			break
		}
	}
	require.NotNil(t, sessOpen, "missing StageSessionOpen occupancy")
	require.Equal(t, 4, sessOpen.Count)
	require.Equal(t, []string{
		"opener:opener-user-session",
		"opener:opener-tenant-session",
		"workspace_resolver:0",
		"workspace_resolver:2",
	}, sessOpen.HandlerIDs)

	// 3. Traffic Observation coalescing (TrafficObservers + UsageObservers + TrafficRedactors)
	trafFeat := featMap["feat-charlie-traffic-metrics"]
	var trafOcc *diag.InventoryStageOccupancy
	for i := range trafFeat.StageOccupancy {
		if trafFeat.StageOccupancy[i].StageID == aipfeature.StageIDTrafficObservation {
			trafOcc = &trafFeat.StageOccupancy[i]
			break
		}
	}
	require.NotNil(t, trafOcc, "missing StageTrafficObservation occupancy")
	require.Equal(t, 6, trafOcc.Count)
	require.Equal(t, []string{
		"traffic_observer:0",
		"traffic_observer:2",
		"usage_observer:0",
		"usage_observer:2",
		"traffic_redactor:redact-auth-token",
		"traffic_redactor:redact-ssn",
	}, trafOcc.HandlerIDs)
}

// TestInventoryExtensions_PrivilegeMatrixCharacterization pins the privilege projection rules:
// - AuxiliaryRequests = len(RequestTransforms) > 0 || len(PreRequestHandlers) > 0 || len(ToolCatalogFilters) > 0 || len(CompletionGates) > 0 || len(AttemptTransforms) > 0
// - CompletionGate = len(CompletionGates) > 0
// - RawCapture = len(RawCaptureSinks) > 0
// - AuthProvider = false
func TestInventoryExtensions_PrivilegeMatrixCharacterization(t *testing.T) {
	t.Parallel()

	cfg, extras := buildPopulatedMultiFeatureFixture(t)
	snapshot, err := diag.InventorySnapshotForConfig(t.Context(), cfg, extras)
	require.NoError(t, err)

	featMap := make(map[string]diag.InventoryFeatureExtensions)
	for _, f := range snapshot.Extensions.Features {
		featMap[f.InstanceID] = f
	}

	// feat-alpha-sec-gate: all 3 true
	f1 := featMap["feat-alpha-sec-gate"]
	require.True(t, f1.Privileges.RawCapture)
	require.True(t, f1.Privileges.AuxiliaryRequests)
	require.True(t, f1.Privileges.CompletionGate)
	require.False(t, f1.Privileges.AuthProvider)

	// feat-bravo-tool-governance: only AuxiliaryRequests
	f2 := featMap["feat-bravo-tool-governance"]
	require.False(t, f2.Privileges.RawCapture)
	require.True(t, f2.Privileges.AuxiliaryRequests)
	require.False(t, f2.Privileges.CompletionGate)
	require.False(t, f2.Privileges.AuthProvider)

	// feat-charlie-traffic-metrics: all false
	f3 := featMap["feat-charlie-traffic-metrics"]
	require.False(t, f3.Privileges.RawCapture)
	require.False(t, f3.Privileges.AuxiliaryRequests)
	require.False(t, f3.Privileges.CompletionGate)
	require.False(t, f3.Privileges.AuthProvider)

	// feat-delta-session-routing: only AuxiliaryRequests (PreRequestHandlers)
	f4 := featMap["feat-delta-session-routing"]
	require.False(t, f4.Privileges.RawCapture)
	require.True(t, f4.Privileges.AuxiliaryRequests)
	require.False(t, f4.Privileges.CompletionGate)
	require.False(t, f4.Privileges.AuthProvider)

	// feat-echo-stream-pipeline: only AuxiliaryRequests (AttemptTransforms)
	f5 := featMap["feat-echo-stream-pipeline"]
	require.False(t, f5.Privileges.RawCapture)
	require.True(t, f5.Privileges.AuxiliaryRequests)
	require.False(t, f5.Privileges.CompletionGate)
	require.False(t, f5.Privileges.AuthProvider)

	// secrets-guard: all false
	f6 := featMap["secrets-guard"]
	require.False(t, f6.Privileges.RawCapture)
	require.False(t, f6.Privileges.AuxiliaryRequests)
	require.False(t, f6.Privileges.CompletionGate)
	require.False(t, f6.Privileges.AuthProvider)

	// feat-golf-disabled: all false
	f7 := featMap["feat-golf-disabled"]
	require.False(t, f7.Privileges.RawCapture)
	require.False(t, f7.Privileges.AuxiliaryRequests)
	require.False(t, f7.Privileges.CompletionGate)
	require.False(t, f7.Privileges.AuthProvider)
	require.Empty(t, f7.StageOccupancy)
}

// TestInventoryExtensions_GenericPortsAggregationCharacterization pins aggregate generic port counts.
func TestInventoryExtensions_GenericPortsAggregationCharacterization(t *testing.T) {
	t.Parallel()

	cfg, extras := buildPopulatedMultiFeatureFixture(t)
	snapshot, err := diag.InventorySnapshotForConfig(t.Context(), cfg, extras)
	require.NoError(t, err)

	ports := snapshot.Extensions.GenericPorts
	require.True(t, ports.AttemptTransformOccupied)
	require.Equal(t, 2, ports.AttemptTransformHandlers)
	require.True(t, ports.FinalStreamObservationOccupied)
	require.Equal(t, 2, ports.FinalStreamObservationHandlers)
}

// TestInventoryExtensions_OccupantLabelFormatCharacterization pins the exact operator-facing
// label format produced for every extension plane.
func TestInventoryExtensions_OccupantLabelFormatCharacterization(t *testing.T) {
	t.Parallel()

	cs := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cs, aipfeature.PlaneSubmitHooks, "test", []sdkhooks.SubmitHook{
		charStubSubmitHook{id: "hook-sub", ord: 1},
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCatalogFilters, "test", []toolcatalog.Filter{
		charStubToolCatalogFilter{id: "filter-cat", ord: 1},
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneRequestTransforms, "test", []request.Transform{
		charStubRequestTransform{id: "req-tf", ord: 1},
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlanePreRequestHandlers, "test", []prerequest.Handler{
		charStubPreRequestHandler{id: "pre-req", ord: 1},
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneRouteHintProviders, "test", []routehint.Provider{
		charStubRouteHintProvider{id: "hint-rt", ord: 1},
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneRequestPartHooks, "test", []sdkhooks.RequestPartHook{
		charStubRequestPartHook{id: "req-part", ord: 1},
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneResponsePartHooks, "test", []sdkhooks.ResponsePartHook{
		charStubResponsePartHook{id: "resp-part", ord: 1},
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallPolicies, "test", []toolpolicy.Policy{
		charStubToolPolicy{id: "tool-pol", ord: 1},
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizers, "test", []toolcall.Finalizer{
		charStubToolFinalizer{id: "tool-fin", ord: 1},
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneToolReactors, "test", []sdkhooks.ToolReactor{
		charStubToolReactor{id: "tool-reac", ord: 1},
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneSessionOpeners, "test", []session.Opener{
		charStubSessionOpener{id: "sess-op"},
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneWorkspaceResolvers, "test", []workspace.Resolver{
		charStubWorkspaceResolver{},
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneSecretGuards, "test", []secretguard.Guard{
		charStubSecretGuard{id: "sec-gd", ord: 1},
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneCompletionGates, "test", []completion.Gate{
		charStubCompletionGate{id: "comp-gt", ord: 1},
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "test", []request.AttemptTransform{
		charStubAttemptTransform{id: "att-tf", ord: 1},
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "test", []response.StreamObserverFactory{
		charStubStreamObserverFactory{id: "stm-obs", ord: 1},
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, "test", []traffic.Observer{
		charStubTrafficObserver{},
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, "test", []usage.Observer{
		charStubUsageObserver{},
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneRawCaptureSinks, "test", []traffic.RawCaptureSink{
		charStubRawCaptureSink{},
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneTrafficRedactors, "test", []traffic.Redactor{
		charStubTrafficRedactor{id: "traf-red"},
	})
	b := aipfeature.BundleFromPlanes(cs.Freeze(), nil)

	occ := stageOccupancyFromBundle(b)
	occMap := make(map[string]diag.InventoryStageOccupancy)
	for _, o := range occ {
		// Note: RequestTransforms and RequestPartHooks both map to StageRequestWide;
		// record all entries for validation.
		occMap[o.StageID+":"+o.HandlerIDs[0]] = o
	}

	// Verify all label formats
	require.Equal(t, []string{"hook-sub"}, occMap[aipfeature.StageIDSubmit+":hook-sub"].HandlerIDs)
	require.Equal(t, []string{"tool_catalog:filter-cat"}, occMap[extensions.StageToolCatalog+":tool_catalog:filter-cat"].HandlerIDs)
	require.Equal(t, []string{"request_transform:req-tf"}, occMap[aipfeature.StageIDRequestWide+":request_transform:req-tf"].HandlerIDs)
	require.Equal(t, []string{"request_part:req-part"}, occMap[aipfeature.StageIDRequestWide+":request_part:req-part"].HandlerIDs)
	require.Equal(t, []string{"pre_request:pre-req"}, occMap[aipfeature.StageIDPreRequest+":pre_request:pre-req"].HandlerIDs)
	require.Equal(t, []string{"route_hint:hint-rt"}, occMap[aipfeature.StageIDRouteHinting+":route_hint:hint-rt"].HandlerIDs)
	require.Equal(t, []string{"resp-part"}, occMap[aipfeature.StageIDStreamEventMutation+":resp-part"].HandlerIDs)
	require.Equal(t, []string{"tool_policy:tool-pol", "tool_finalizer:tool-fin", "tool-reac"}, occMap[extensions.StageToolEventReaction+":tool_policy:tool-pol"].HandlerIDs)
	require.Equal(t, []string{"opener:sess-op", "workspace_resolver:0"}, occMap[extensions.StageSessionOpen+":opener:sess-op"].HandlerIDs)
	require.Equal(t, []string{"secret_guard:sec-gd"}, occMap[aipfeature.StageIDSecretGuard+":secret_guard:sec-gd"].HandlerIDs)
	require.Equal(t, []string{"completion_gate:comp-gt"}, occMap[aipfeature.StageIDCompletionGating+":completion_gate:comp-gt"].HandlerIDs)
	require.Equal(t, []string{"attempt_transform:att-tf"}, occMap[extensions.StageCandidateAttemptTransform+":attempt_transform:att-tf"].HandlerIDs)
	require.Equal(t, []string{"stream_observer:stm-obs"}, occMap[extensions.StageFinalStreamObservation+":stream_observer:stm-obs"].HandlerIDs)
	require.Equal(t, []string{"traffic_observer:0", "usage_observer:0", "raw_capture:0", "traffic_redactor:traf-red"}, occMap[aipfeature.StageIDTrafficObservation+":traffic_observer:0"].HandlerIDs)
}

// TestInventoryExtensions_NilFilteringAllPlanesCharacterization pins nil exclusion behavior
// across all nil-filtering planes without panicking.
func TestInventoryExtensions_NilFilteringAllPlanesCharacterization(t *testing.T) {
	t.Parallel()

	cs := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cs, aipfeature.PlaneSecretGuards, "test", []secretguard.Guard{
		nil,
		charStubSecretGuard{id: "sg-valid", ord: 1},
		nil,
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallPolicies, "test", []toolpolicy.Policy{
		nil,
		charStubToolPolicy{id: "pol-valid", ord: 1},
		nil,
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizers, "test", []toolcall.Finalizer{
		nil,
		charStubToolFinalizer{id: "fin-valid", ord: 1},
		nil,
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneRouteHintProviders, "test", []routehint.Provider{
		nil,
		charStubRouteHintProvider{id: "hint-valid", ord: 1},
		nil,
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneSessionOpeners, "test", []session.Opener{
		nil,
		charStubSessionOpener{id: "opener-valid"},
		nil,
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneWorkspaceResolvers, "test", []workspace.Resolver{
		nil,
		charStubWorkspaceResolver{},
		nil,
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, "test", []traffic.Observer{
		nil,
		charStubTrafficObserver{},
		nil,
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, "test", []usage.Observer{
		nil,
		charStubUsageObserver{},
		nil,
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneRawCaptureSinks, "test", []traffic.RawCaptureSink{
		nil,
		charStubRawCaptureSink{},
		nil,
	})
	_ = aipfeature.Contribute(cs, aipfeature.PlaneTrafficRedactors, "test", []traffic.Redactor{
		nil,
		charStubTrafficRedactor{id: "redact-valid"},
		nil,
	})
	b := aipfeature.BundleFromPlanes(cs.Freeze(), nil)

	occ := stageOccupancyFromBundle(b)
	require.Len(t, occ, 5, "must contain exactly 5 stage occupancy rows")
	for _, o := range occ {
		switch o.StageID {
		case aipfeature.StageIDSecretGuard:
			require.Equal(t, []string{"secret_guard:sg-valid"}, o.HandlerIDs)
			require.Equal(t, 1, o.Count)
		case extensions.StageToolEventReaction:
			require.Equal(t, []string{"tool_policy:pol-valid", "tool_finalizer:fin-valid"}, o.HandlerIDs)
			require.Equal(t, 2, o.Count)
		case aipfeature.StageIDRouteHinting:
			require.Equal(t, []string{"route_hint:hint-valid"}, o.HandlerIDs)
			require.Equal(t, 1, o.Count)
		case extensions.StageSessionOpen:
			require.Equal(t, []string{"opener:opener-valid", "workspace_resolver:1"}, o.HandlerIDs)
			require.Equal(t, 2, o.Count)
		case aipfeature.StageIDTrafficObservation:
			require.Equal(t, []string{"traffic_observer:1", "usage_observer:1", "raw_capture:1", "traffic_redactor:redact-valid"}, o.HandlerIDs)
			require.Equal(t, 4, o.Count)
		}
	}

	// Verify attempt transforms and stream observer factories filter nils during materialization
	atOccs := aipfeature.PlaneAttemptTransforms.MaterializeOccupants([]request.AttemptTransform{
		nil,
		charStubAttemptTransform{id: "attempt-valid", ord: 1},
		nil,
	})
	require.Equal(t, []aipfeature.DiagnosticOccupant{{Label: "attempt_transform:attempt-valid"}}, atOccs)

	soOccs := aipfeature.PlaneStreamObserverFactories.MaterializeOccupants([]response.StreamObserverFactory{
		nil,
		charStubStreamObserverFactory{id: "stream-valid", ord: 1},
		nil,
	})
	require.Equal(t, []aipfeature.DiagnosticOccupant{{Label: "stream_observer:stream-valid"}}, soOccs)
}

// TestInventoryExtensions_FamilySpecificOrderingCharacterization pins ordering differences across planes:
// 1. Order() + ID() sort (hooks, policies, guards, transforms)
// 2. Original slice order preservation (SessionOpeners, TrafficRedactors)
// 3. Slice-index-based label generation (TrafficObservers, UsageObservers, RawCaptureSinks, WorkspaceResolvers)
func TestInventoryExtensions_FamilySpecificOrderingCharacterization(t *testing.T) {
	t.Parallel()

	// 1. Order + ID tie-breaking
	csSort := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(csSort, aipfeature.PlaneSecretGuards, "test", []secretguard.Guard{
		charStubSecretGuard{id: "sg-z", ord: 20},
		charStubSecretGuard{id: "sg-b", ord: 10},
		charStubSecretGuard{id: "sg-a", ord: 10},
	})
	_ = aipfeature.Contribute(csSort, aipfeature.PlaneSubmitHooks, "test", []sdkhooks.SubmitHook{
		charStubSubmitHook{id: "sub-z", ord: 20},
		charStubSubmitHook{id: "sub-b", ord: 10},
		charStubSubmitHook{id: "sub-a", ord: 10},
	})
	bSort := aipfeature.BundleFromPlanes(csSort.Freeze(), nil)
	occSort := stageOccupancyFromBundle(bSort)
	for _, o := range occSort {
		switch o.StageID {
		case aipfeature.StageIDSecretGuard:
			require.Equal(t, []string{"secret_guard:sg-a", "secret_guard:sg-b", "secret_guard:sg-z"}, o.HandlerIDs)
		case aipfeature.StageIDSubmit:
			require.Equal(t, []string{"sub-a", "sub-b", "sub-z"}, o.HandlerIDs)
		}
	}

	// 2. Slice order preservation
	csSlice := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(csSlice, aipfeature.PlaneSessionOpeners, "test", []session.Opener{
		charStubSessionOpener{id: "z-opener"},
		charStubSessionOpener{id: "a-opener"},
	})
	_ = aipfeature.Contribute(csSlice, aipfeature.PlaneTrafficRedactors, "test", []traffic.Redactor{
		charStubTrafficRedactor{id: "z-redactor"},
		charStubTrafficRedactor{id: "a-redactor"},
	})
	bSlice := aipfeature.BundleFromPlanes(csSlice.Freeze(), nil)
	occSlice := stageOccupancyFromBundle(bSlice)
	for _, o := range occSlice {
		switch o.StageID {
		case extensions.StageSessionOpen:
			require.Equal(t, []string{"opener:z-opener", "opener:a-opener"}, o.HandlerIDs)
		case aipfeature.StageIDTrafficObservation:
			require.Equal(t, []string{"traffic_redactor:z-redactor", "traffic_redactor:a-redactor"}, o.HandlerIDs)
		}
	}

	// 3. Index-based label generation with non-consecutive indices
	csIndex := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(csIndex, aipfeature.PlaneWorkspaceResolvers, "test", []workspace.Resolver{
		charStubWorkspaceResolver{},
		nil,
		charStubWorkspaceResolver{},
		charStubWorkspaceResolver{},
	})
	bIndex := aipfeature.BundleFromPlanes(csIndex.Freeze(), nil)
	occIndex := stageOccupancyFromBundle(bIndex)
	require.Len(t, occIndex, 1)
	require.Equal(t, []string{"workspace_resolver:0", "workspace_resolver:2", "workspace_resolver:3"}, occIndex[0].HandlerIDs)
}

type charStubCompactionObs struct{}

func (charStubCompactionObs) OnCompaction(context.Context, compaction.Event) error {
	return nil
}

type charStubCompactionPreserver struct {
	id string
}

func (p charStubCompactionPreserver) ID() string { return p.id }
func (charStubCompactionPreserver) BeforeRequest(context.Context, *aipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (charStubCompactionPreserver) RequestOpened(context.Context, aipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (charStubCompactionPreserver) BeforeResponseRelease(context.Context, *aipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

// TestInventoryExtensions_CompactionPlaneExactEquivalenceWithBase proves that contributing
// compaction observers and compaction preservers onto features does not alter the inventory
// stage occupancy, privilege flags, generic ports, or overall snapshot JSON bytes, matching base.
func TestInventoryExtensions_CompactionPlaneExactEquivalenceWithBase(t *testing.T) {
	t.Parallel()

	cfgBase, extrasBase := buildPopulatedMultiFeatureFixture(t)
	snapBase, err := diag.InventorySnapshotForConfig(t.Context(), cfgBase, extrasBase)
	require.NoError(t, err)

	bytesBase, err := json.MarshalIndent(snapBase, "", "  ")
	require.NoError(t, err)

	// Now add compaction observers and preservers to multiple features
	reg, ok := extrasBase.Reg.(*populatedMultiFeatureRegistry)
	require.True(t, ok)
	bAlpha := reg.bundles["sec-gate"]
	csAlpha := aipfeature.NewContributionSet()
	require.NoError(t, bAlpha.PlaneSet.ReplayTo(csAlpha, "sec-gate"))
	require.NoError(t, aipfeature.Contribute(csAlpha, aipfeature.PlaneCompactionObservers, "sec-gate", []compaction.Observer{charStubCompactionObs{}}))
	require.NoError(t, aipfeature.Contribute(csAlpha, aipfeature.PlaneCompactionPreservers, "sec-gate", []compaction.Preserver{charStubCompactionPreserver{id: "preserver-sec"}}))
	reg.bundles["sec-gate"] = aipfeature.BundleFromPlanes(csAlpha.Freeze(), bAlpha.Lifecycles)

	bBravo := reg.bundles["tool-governance"]
	csBravo := aipfeature.NewContributionSet()
	require.NoError(t, bBravo.PlaneSet.ReplayTo(csBravo, "tool-governance"))
	require.NoError(t, aipfeature.Contribute(csBravo, aipfeature.PlaneCompactionObservers, "tool-governance", []compaction.Observer{charStubCompactionObs{}, nil, charStubCompactionObs{}}))
	require.NoError(t, aipfeature.Contribute(csBravo, aipfeature.PlaneCompactionPreservers, "tool-governance", []compaction.Preserver{charStubCompactionPreserver{id: "preserver-tool"}}))
	reg.bundles["tool-governance"] = aipfeature.BundleFromPlanes(csBravo.Freeze(), bBravo.Lifecycles)

	snapWithCompaction, err := diag.InventorySnapshotForConfig(t.Context(), cfgBase, extrasBase)
	require.NoError(t, err)

	bytesWithCompaction, err := json.MarshalIndent(snapWithCompaction, "", "  ")
	require.NoError(t, err)

	// Exact byte-for-byte equivalence
	require.Equal(t, string(bytesBase), string(bytesWithCompaction),
		"adding compaction observers/preservers must produce byte-for-byte identical inventory snapshot as base")
}
