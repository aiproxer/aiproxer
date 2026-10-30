package extensions

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/localturn"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/state"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

type snapCtxKey struct{}

// SecretGuardPlane is the frozen secret-guard composition bound into a request snapshot.
type SecretGuardPlane struct {
	Guards             []secretguard.Guard
	MatcherResolver    secretguard.MatcherResolver
	Capability         any
	DecisionObserver   secretguard.Observer
	AuditFailurePolicy secretguard.AuditFailurePolicy
	AccessMode         string
	ConfigVersion      string
}

// RequestRuntimeSnapshot is a per-build binding of hook chains and service facades published
// onto each request context (design §15B, task 4.2). Many request goroutines may read the same
// pointer without synchronization; callers must treat it as frozen after construction: do not
// replace fields, mutate the embedded [*hooks.Bus], or swap facade implementations. Config reload
// or rebinding must publish a new snapshot (new [RequestRuntimeSnapshot] value and new executor
// wiring from [github.com/aiproxer/aiproxer/internal/infra/runtimebundle.Build]).
type RequestRuntimeSnapshot struct {
	hookBus          *hooks.Bus
	state            state.Store
	aux              auxiliary.Client
	obs              traffic.Observer
	usageObs         usage.Observer
	raw              traffic.RawCaptureSink
	ws               workspace.Resolver
	secretGuardPlane SecretGuardPlane
	policyObserver   policydecision.Observer
	timeoutBudget    TimeoutBudgetSource
	timeoutGuard     *ProviderTimeoutGuard
	featurePlanes    aipfeature.FrozenPlaneSet
	gen              int64
}

// SnapshotOptions configures optional facades; zero value uses disabled placeholders.
type SnapshotOptions struct {
	State            state.Store
	Aux              auxiliary.Client
	TrafficObserver  traffic.Observer
	UsageObserver    usage.Observer
	RawCapture       traffic.RawCaptureSink
	Workspace        workspace.Resolver
	SecretGuardPlane SecretGuardPlane
	// PolicyObserver receives normalized policy decision evidence. Nil defaults to a
	// disabled no-op observer so deployments without policy evidence keep current request
	// outcomes (requirements 7.6, 10.5).
	PolicyObserver policydecision.Observer
	// TimeoutBudgetSource is the frozen per-request source of decision-provider evaluation
	// budgets. Nil defaults to [DefaultTimeoutBudgetSource] (zero budget for every
	// stage/provider) so legacy extension behavior is unchanged (requirements 6.3, 10.5).
	TimeoutBudgetSource TimeoutBudgetSource
	FeaturePlanes       aipfeature.FrozenPlaneSet
	Generation          int64
}

// NewRequestRuntimeSnapshot captures bus and facades for the lifetime of the returned value.
// bus must be non-nil (or replaced with [hooks.New] empty bus). The same [*hooks.Bus] must not
// be mutated after this call if the snapshot is shared across concurrent requests.
func NewRequestRuntimeSnapshot(bus *hooks.Bus, opts SnapshotOptions) *RequestRuntimeSnapshot {
	if bus == nil {
		bus = hooks.New(hooks.Config{})
	}
	st := opts.State
	if st == nil {
		st = state.DisabledStore{}
	}
	ax := opts.Aux
	if ax == nil {
		ax = auxiliary.DisabledClient{}
	}
	ob := opts.TrafficObserver
	if ob == nil {
		ob = traffic.NoopObserver{}
	}
	uob := opts.UsageObserver
	if uob == nil {
		uob = usage.NoopObserver{}
	}
	raw := opts.RawCapture
	if raw == nil {
		raw = traffic.DisabledRawCapture{}
	}
	ws := opts.Workspace
	if ws == nil {
		ws = workspace.DisabledResolver{}
	}
	plane := opts.SecretGuardPlane
	plane.Guards = nil
	if secretguard.IsNilObserver(plane.DecisionObserver) {
		plane.DecisionObserver = nil
	}
	if plane.AuditFailurePolicy == "" {
		plane.AuditFailurePolicy = secretguard.AuditFailClosed
	}
	polObs := opts.PolicyObserver
	if polObs == nil {
		polObs = policydecision.NoopObserver{}
	}
	budget := opts.TimeoutBudgetSource
	if budget == nil {
		budget = DefaultTimeoutBudgetSource{}
	}

	// Materialize request planes once at snapshot construction via generated freeze:
	featurePlanes := aipfeature.FreezeRequestPlanes(opts.FeaturePlanes)

	return &RequestRuntimeSnapshot{
		hookBus:          bus,
		state:            st,
		aux:              ax,
		obs:              ob,
		usageObs:         uob,
		raw:              raw,
		ws:               ws,
		secretGuardPlane: plane,
		policyObserver:   polObs,
		timeoutBudget:    budget,
		timeoutGuard:     NewProviderTimeoutGuard(),
		featurePlanes:    featurePlanes,
		gen:              opts.Generation,
	}
}

// HookBus returns the hook bus bound at snapshot construction (brownfield compatibility).
func (s *RequestRuntimeSnapshot) HookBus() *hooks.Bus {
	if s == nil {
		return nil
	}
	return s.hookBus
}

// State returns the plugin state facade for this snapshot.
func (s *RequestRuntimeSnapshot) State() state.Store {
	if s == nil {
		return nil
	}
	return s.state
}

// Aux returns the auxiliary request client for this snapshot.
func (s *RequestRuntimeSnapshot) Aux() auxiliary.Client {
	if s == nil {
		return nil
	}
	return s.aux
}

// TrafficObserver returns the structured traffic observer for this snapshot.
func (s *RequestRuntimeSnapshot) TrafficObserver() traffic.Observer {
	if s == nil {
		return nil
	}
	return s.obs
}

// UsageObserver returns the usage observer for this snapshot.
func (s *RequestRuntimeSnapshot) UsageObserver() usage.Observer {
	if s == nil {
		return nil
	}
	return s.usageObs
}

// RawCapture returns the privileged raw capture sink for this snapshot.
func (s *RequestRuntimeSnapshot) RawCapture() traffic.RawCaptureSink {
	if s == nil {
		return nil
	}
	return s.raw
}

// Workspace returns the workspace resolver for this snapshot.
func (s *RequestRuntimeSnapshot) Workspace() workspace.Resolver {
	if s == nil {
		return nil
	}
	return s.ws
}

// SessionOpeners returns a defensive copy of the frozen session-open stage handlers (may be empty).
// Mutating the returned slice does not affect the snapshot.
func (s *RequestRuntimeSnapshot) SessionOpeners() []session.Opener {
	return aipfeature.Get(s.featurePlaneSet(), aipfeature.PlaneSessionOpeners)
}

// ToolCatalogFilters returns a defensive copy of frozen catalog filters (may be empty).
func (s *RequestRuntimeSnapshot) ToolCatalogFilters() []toolcatalog.Filter {
	return aipfeature.Get(s.featurePlaneSet(), aipfeature.PlaneToolCatalogFilters)
}

// ToolCallPolicies returns a defensive copy of frozen tool-call policies (may be empty).
// Mutating the returned slice does not affect the snapshot.
func (s *RequestRuntimeSnapshot) ToolCallPolicies() []toolpolicy.Policy {
	return aipfeature.Get(s.featurePlaneSet(), aipfeature.PlaneToolCallPolicies)
}

// ToolCallPoliciesExecution returns the frozen tool-call policy slice in execution order
// (the same ordering as [toolpolicy.MaterializeSorted]). The returned slice must not be
// mutated; it is the snapshot's internal backing store. Prefer [RequestRuntimeSnapshot.ToolCallPolicies]
// for a defensive copy; this accessor exists for the runtime executor hot path.
func (s *RequestRuntimeSnapshot) ToolCallPoliciesExecution() []toolpolicy.Policy {
	return aipfeature.RequestExecution(s.featurePlaneSet()).ToolCallPolicies()
}

func (s *RequestRuntimeSnapshot) ToolCallFinalizers() []toolcall.Finalizer {
	return aipfeature.Get(s.featurePlaneSet(), aipfeature.PlaneToolCallFinalizers)
}

func (s *RequestRuntimeSnapshot) ToolCallFinalizersExecution() []toolcall.Finalizer {
	return aipfeature.RequestExecution(s.featurePlaneSet()).ToolCallFinalizers()
}

// RequestTransforms returns a defensive copy of frozen request-wide transforms (may be empty).
func (s *RequestRuntimeSnapshot) RequestTransforms() []request.Transform {
	return aipfeature.Get(s.featurePlaneSet(), aipfeature.PlaneRequestTransforms)
}

// PreRequestHandlers returns a defensive copy of frozen pre-request admission handlers (may be empty).
func (s *RequestRuntimeSnapshot) PreRequestHandlers() []prerequest.Handler {
	return aipfeature.Get(s.featurePlaneSet(), aipfeature.PlanePreRequestHandlers)
}

// RouteHintProviders returns a defensive copy of frozen route hint providers (may be empty).
func (s *RequestRuntimeSnapshot) RouteHintProviders() []routehint.Provider {
	return aipfeature.Get(s.featurePlaneSet(), aipfeature.PlaneRouteHintProviders)
}

// CompletionGates returns a defensive copy of frozen completion gates (may be empty).
func (s *RequestRuntimeSnapshot) CompletionGates() []completion.Gate {
	return aipfeature.Get(s.featurePlaneSet(), aipfeature.PlaneCompletionGates)
}

// AttemptTransforms returns a defensive copy of frozen candidate attempt transforms (may be empty).
func (s *RequestRuntimeSnapshot) AttemptTransforms() []request.AttemptTransform {
	return aipfeature.Get(s.featurePlaneSet(), aipfeature.PlaneAttemptTransforms)
}

// StreamObserverFactories returns a defensive copy of frozen stream observer factories (may be empty).
func (s *RequestRuntimeSnapshot) StreamObserverFactories() []response.StreamObserverFactory {
	return aipfeature.Get(s.featurePlaneSet(), aipfeature.PlaneStreamObserverFactories)
}

// TrafficRedactors returns a defensive copy of frozen redactors for the traffic pipeline (may be empty).
func (s *RequestRuntimeSnapshot) TrafficRedactors() []traffic.Redactor {
	return aipfeature.Get(s.featurePlaneSet(), aipfeature.PlaneTrafficRedactors)
}

// CompactionObservers returns a defensive copy of the frozen compaction
// observer slice (may be empty). Mutating the returned slice does not affect
// the snapshot; the snapshot's internal backing slice must never be mutated.
func (s *RequestRuntimeSnapshot) CompactionObservers() []compaction.Observer {
	return aipfeature.Get(s.featurePlaneSet(), aipfeature.PlaneCompactionObservers)
}

// CompactionPreservers returns a defensive copy of the frozen content-bearing
// preservation callback slice. Mutating the returned slice does not affect the
// snapshot.
func (s *RequestRuntimeSnapshot) CompactionPreservers() []compaction.Preserver {
	return aipfeature.Get(s.featurePlaneSet(), aipfeature.PlaneCompactionPreservers)
}

// SecretGuards returns a defensive copy of the frozen secret guard slice.
func (s *RequestRuntimeSnapshot) SecretGuards() []secretguard.Guard {
	return aipfeature.Get(s.featurePlaneSet(), aipfeature.PlaneSecretGuards)
}

// SecretGuardsExecution returns the frozen secret guard slice without cloning.
func (s *RequestRuntimeSnapshot) SecretGuardsExecution() []secretguard.Guard {
	return aipfeature.RequestExecution(s.featurePlaneSet()).SecretGuards()
}

// SecretGuardPlane returns a defensive copy of the SecretGuardPlane configuration
// (guards slice cloned). Prefer [RequestRuntimeSnapshot.SecretGuardExecutionPlane]
// for the runtime executor hot path.
func (s *RequestRuntimeSnapshot) SecretGuardPlane() SecretGuardPlane {
	if s == nil {
		return SecretGuardPlane{}
	}
	plane := s.secretGuardPlane
	plane.Guards = s.SecretGuards()
	return plane
}

// SecretGuardExecutionPlane returns the frozen secret-guard plane without cloning the
// guard slice. MatcherResolver, DecisionObserver, and policy/config fields are safe to
// read; Guards is the snapshot's internal backing store in MaterializeSorted order and
// must not be mutated.
func (s *RequestRuntimeSnapshot) SecretGuardExecutionPlane() SecretGuardPlane {
	if s == nil {
		return SecretGuardPlane{}
	}
	plane := s.secretGuardPlane
	plane.Guards = s.SecretGuardsExecution()
	return plane
}

// Generation is an opaque build stamp (e.g. config reload generation in a future spec).
func (s *RequestRuntimeSnapshot) Generation() int64 {
	if s == nil {
		return 0
	}
	return s.gen
}

// PolicyObserver returns the frozen policy decision observer bound at snapshot construction
// (requirements 7.6, 10.5). A nil snapshot returns the disabled no-op default so callers can
// always invoke the returned observer without nil checks. The returned observer is an
// interface value; it is treated as frozen for the lifetime of the snapshot.
func (s *RequestRuntimeSnapshot) PolicyObserver() policydecision.Observer {
	if s == nil || s.policyObserver == nil {
		return policydecision.NoopObserver{}
	}
	return s.policyObserver
}

// TimeoutBudgetSource returns the frozen per-request decision-provider timeout budget source
// bound at snapshot construction (requirements 6.3, 10.5). A nil snapshot returns the default
// zero-budget source so callers can always invoke TimeoutFor without nil checks.
func (s *RequestRuntimeSnapshot) TimeoutBudgetSource() TimeoutBudgetSource {
	if s == nil || s.timeoutBudget == nil {
		return DefaultTimeoutBudgetSource{}
	}
	return s.timeoutBudget
}

// ProviderTimeoutGuard returns the snapshot-scoped guard used to contain
// uncooperative bounded providers. It is shared across requests for this immutable
// snapshot so one stuck stage/provider cannot accumulate one goroutine per request.
func (s *RequestRuntimeSnapshot) ProviderTimeoutGuard() *ProviderTimeoutGuard {
	if s == nil {
		return nil
	}
	return s.timeoutGuard
}

// LocalTurnHandlers returns a defensive copy of frozen local-turn handlers (may be empty).
// Mutating the returned slice does not affect the snapshot.
func (s *RequestRuntimeSnapshot) LocalTurnHandlers() []localturn.Handler {
	return aipfeature.Get(s.featurePlaneSet(), aipfeature.PlaneLocalTurnHandlers)
}

// LocalTurnHandlersExecution returns the frozen local-turn handler slice in execution order
// (sorted by Order then ID). The returned slice must not be mutated.
func (s *RequestRuntimeSnapshot) LocalTurnHandlersExecution() []localturn.Handler {
	return aipfeature.RequestExecution(s.featurePlaneSet()).LocalTurnHandlers()
}

func (s *RequestRuntimeSnapshot) featurePlaneSet() aipfeature.FrozenPlaneSet {
	if s == nil {
		return aipfeature.FrozenPlaneSet{}
	}
	return s.featurePlanes
}

// TerminalDecisionProvider returns the generation provider captured by this
// immutable request snapshot, or nil when no provider is active.
func (s *RequestRuntimeSnapshot) TerminalDecisionProvider() terminaldecision.Provider {
	return aipfeature.Get(s.featurePlaneSet(), aipfeature.PlaneTerminalDecisionProvider)
}

// TerminalDecisionProviderIdentity returns the frozen identity of the generation provider
// captured by this immutable request snapshot, if present.
func (s *RequestRuntimeSnapshot) TerminalDecisionProviderIdentity() (string, bool) {
	return aipfeature.FrozenIdentity(s.featurePlaneSet(), aipfeature.PlaneTerminalDecisionProvider)
}

// SessionClassifier returns the exclusive session-classifier plane bound at
// snapshot construction, or nil when the published generation contributes no
// classifier. The returned classifier is an interface value treated as frozen
// for the lifetime of the snapshot; generic runtime consumes it only through
// [RunSessionClassificationStage].
func (s *RequestRuntimeSnapshot) SessionClassifier() sessionclassification.Classifier {
	return aipfeature.Get(s.featurePlaneSet(), aipfeature.PlaneSessionClassifier)
}

// ControlToolProvider returns the generation proxy-owned model control tool
// provider captured by this immutable request snapshot, or nil when the
// generation admits no control tool. An absent provider requires zero provider
// method calls: callers must branch on the nil result rather than invoking it.
func (s *RequestRuntimeSnapshot) ControlToolProvider() controltool.Provider {
	return aipfeature.Get(s.featurePlaneSet(), aipfeature.PlaneControlToolProvider)
}

// ControlToolProviderIdentity returns the frozen identity of the generation
// control-tool provider captured by this immutable request snapshot, if present.
// It reads validated cached identity metadata and never invokes a live provider
// method, so a request path can pin the generation and honor plugin suppression
// without resolving a spec. An occupied plane without a frozen identity fails
// closed: a generation that cannot name its control-tool owner has no provable
// provenance.
func (s *RequestRuntimeSnapshot) ControlToolProviderIdentity() (string, bool) {
	return aipfeature.FrozenIdentity(s.featurePlaneSet(), aipfeature.PlaneControlToolProvider)
}

// WithRequestRuntimeSnapshot attaches snap to ctx for the remainder of the request lifetime.
// snap must remain valid and unchanged for the lifetime of ctx (see [RequestRuntimeSnapshot]).
func WithRequestRuntimeSnapshot(ctx context.Context, snap *RequestRuntimeSnapshot) context.Context {
	if ctx == nil || snap == nil {
		return ctx
	}
	return context.WithValue(ctx, snapCtxKey{}, snap)
}

// RequestRuntimeSnapshotFromContext returns the snapshot from [WithRequestRuntimeSnapshot], if any.
func RequestRuntimeSnapshotFromContext(ctx context.Context) *RequestRuntimeSnapshot {
	if ctx == nil {
		return nil
	}
	raw := ctx.Value(snapCtxKey{})
	s, ok := raw.(*RequestRuntimeSnapshot)
	if !ok {
		return nil
	}
	return s
}
