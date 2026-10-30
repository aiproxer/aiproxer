package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/aiproxer/aiproxer/internal/core/affinity"
	"github.com/aiproxer/aiproxer/internal/core/auth"
	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/capabilities"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/policy"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
	"github.com/aiproxer/aiproxer/internal/core/streamrecovery"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingobs "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/observability"
	accountingpreflight "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/preflight"
	accountingstream "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/streamusage"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// MonetaryAuthority names the validated money authority behind a complete
// monetary host. The set is closed and content-free: an authority names a
// money root, never a customer, an account or a price.
type MonetaryAuthority string

const (
	// MonetaryAuthorityExternalBinding is a validated external billing binding
	// (pkg/aipsdk/billing.Binding). The binding adapter publishes it, and the
	// adapter exists only for a binding that passed the binding validation:
	// a valid identity and version, a valid scope, and non-nil credit screen,
	// quoter, admission, terminal and lifecycle ports.
	MonetaryAuthorityExternalBinding MonetaryAuthority = "external_binding"
	// MonetaryAuthorityStockRoot is the internal reference money root: the
	// durable billing store, the atomic exposure admission, the cheap credit
	// screen, the terminal usage sink, the rating and provider-cost resolvers
	// and the post-usage settlement workers.
	MonetaryAuthorityStockRoot MonetaryAuthority = "stock_money_root"
)

// TerminalHandoff states that this host's terminal usage sink holds the
// obligation durably before it acknowledges the terminal append. The set is
// closed and content-free.
type TerminalHandoff string

const (
	// TerminalHandoffNone is the default: nothing states that the terminal
	// handoff is durable, so a customer-funded operation is refused.
	TerminalHandoffNone TerminalHandoff = ""
	// TerminalHandoffDurable states that the terminal usage sink commits the
	// obligation durably before it acknowledges the append, so a completed
	// obligation survives a process restart and a central delivery outage.
	TerminalHandoffDurable TerminalHandoff = "durable"
)

// MonetaryHost is the trusted capability that a complete monetary authority
// stands behind a billing seam bundle.
//
// It is never inferred from the presence of the billing ports: four wired
// pointers are not a monetary host, because a stock no-op, a partial default
// or a typed-nil port can satisfy a pointer check. The host composition
// publishes the capability only where it validated a complete monetary
// authority, so a customer-funded leg can rely on a real quote, rating,
// lifecycle and durable terminal authority behind the account it charges.
//
// The capability carries no money shape: no price, no rating, no balance, no
// ledger and no account.
type MonetaryHost struct {
	// Authority names the validated money authority. The zero value is not a
	// monetary host.
	Authority MonetaryAuthority
	// BindingID is the validated binding's stable identity. It is empty for
	// the internal reference money root, which has no external binding.
	BindingID string
	// TerminalHandoff states that the terminal usage sink holds the
	// obligation durably before it acknowledges the terminal append. A
	// customer-funded leg needs it: without it the customer charge is lost
	// with the process.
	TerminalHandoff TerminalHandoff
}

// BillingIdentity is the composition-root identity bundle shared by billing
// exposure admission and terminal call-closure stamping so those seams cannot drift.
type BillingIdentity struct {
	AccountID func(context.Context, aipapi.Call) string
	// MonetaryHost is the trusted capability of the complete monetary
	// authority behind this identity bundle. The host composition publishes it
	// only where it validated a complete monetary binding, so an executor can
	// tell a real monetary host from a wired set of no-op ports.
	MonetaryHost MonetaryHost
	// StoreID resolves the trusted durable billing/metering store identity for a
	// request. It is captured with the terminal billing facts and is intentionally
	// absent from public runtime options.
	StoreID            func(context.Context) string
	CustomerPricingRef func(context.Context, aipapi.Call) usageevidence.VersionRef
	ChargePolicyRef    func(context.Context, aipapi.Call) usageevidence.VersionRef
	OperatorRateRef    func(context.Context, string, string) usageevidence.VersionRef

	// WireBounded indicates whether this identity bundle is backed by stock/bounded
	// facts (such as PrincipalSessionIdentity) rather than custom Call-inspecting callbacks.
	// Custom callbacks without an explicit bounded contract remain wire blockers (Requirements 15.6, 19).
	WireBounded bool

	// WireAccountID optionally resolves an account identity directly from bounded scope
	// without constructing or inspecting a aipapi.Call.
	WireAccountID func(context.Context, scope.PrincipalScopeView) string

	// WireCustomerPricingRef optionally resolves the customer pricing snapshot ref from bounded facts.
	WireCustomerPricingRef func(context.Context) usageevidence.VersionRef

	// WireChargePolicyRef optionally resolves the charge policy snapshot ref from bounded facts.
	WireChargePolicyRef func(context.Context) usageevidence.VersionRef
}

// HasCustomCallCallbacks reports whether BillingIdentity has Call-shaped callbacks
// that are NOT certified as wire-bounded (Requirements 15.6, 19).
func (b BillingIdentity) HasCustomCallCallbacks() bool {
	if b.WireBounded {
		return false
	}
	return b.AccountID != nil || b.CustomerPricingRef != nil || b.ChargePolicyRef != nil
}

// EmptyVisibleTurnRetryPolicy is the generation-pinned resolution of the
// typed `empty_visible_turn_retry` config section (spec
// empty-visible-turn-retry, requirements 1.1 and 1.3). The zero value is the
// disabled default; the composition root writes the generation's value and the
// runtime captures it per turn, so a reload cannot change an in-flight turn.
type EmptyVisibleTurnRetryPolicy struct {
	// Enabled turns the corrective retry on for turns admitted under the
	// generation that carries this value.
	Enabled bool
}

// CoreRuntime carries continuity store, backends, lifecycle coordination, and clocks.
type CoreRuntime struct {
	CorePorts
	Store                b2bua.Store
	Backends             map[string]execbackend.Backend
	ALegLifecycle        *leglifecycle.Coordinator
	Rand                 routing.Rng
	Now                  func() time.Time
	MaxPendingWireEvents int
	StreamRecovery       streamrecovery.Config
	// EmptyVisibleTurnRetry is the generation-pinned opt-in gate for the
	// empty-visible-turn corrective retry. See EmptyVisibleTurnRetryPolicy.
	EmptyVisibleTurnRetry EmptyVisibleTurnRetryPolicy
	// ConversationViewTagger is the optional narrow tagger port for local-turn
	// tag-before-release.
	ConversationViewTagger ConversationViewTagger
	// SteeringWriterFactory constructs an authoritative steering writer for a given A-leg.
	SteeringWriterFactory SteeringWriterFactory
	// LargeBodyAssessor evaluates frontend proof for candidate fast-path execution.
	LargeBodyAssessor largebody.LargeBodyAssessor
	// LargeBodyGenerationID is the optional generation identity for largebody execution validation.
	LargeBodyGenerationID string
	// LargeBodyCandidateDomainGeneration is the optional generation identity for late-route proof validation.
	LargeBodyCandidateDomainGeneration string
	// BoundRegistry is the generation's frozen model-registry publication.
	// A request that carries its own bound view uses that one; a request the
	// generation starts itself — an auxiliary background request — has no
	// request-bound view and uses the generation's.
	BoundRegistry modelregistry.BoundView
}

// ConversationViewObserver is optional narrow diagnostics for bounded conversation-view
// projection/anchor/steering metrics. Nil is no-op. Labels are bounded enums only (placement, operation, policy, stage).
type ConversationViewObserver interface {
	OnProjection(stage string, summary conversationprojection.ProjectionSummary)
	OnProjectionFailure(stage string)
	OnAnchorFallback(stage string, policy conversationprojection.AnchorMissingPolicy)
	OnAnchorFailure(policy conversationprojection.AnchorMissingPolicy)
}

// TagRequest is one element of a TagNeverBackend batch.
type TagRequest struct {
	Identity conversationprojection.MessageIdentity `json:"identity"`
	Reason   conversationprojection.ReasonCode      `json:"reason"`
}

func (r TagRequest) Validate() error {
	if err := r.Identity.Validate(); err != nil {
		return fmt.Errorf("%w: %v", conversationprojection.ErrInvalidMessageIdentity, err)
	}
	s := string(r.Reason)
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("%w: reason code is required", conversationprojection.ErrInvalidReasonCode)
	}
	if len(s) > 64 {
		return fmt.Errorf("%w: reason code exceeds 64 bytes", conversationprojection.ErrInvalidReasonCode)
	}
	for _, ch := range s {
		if ch > unicode.MaxASCII {
			return fmt.Errorf("%w: reason code must be ascii", conversationprojection.ErrInvalidReasonCode)
		}
		if ch != '_' && ch != '-' && ch != '.' && (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') {
			return fmt.Errorf("%w: invalid character %q in reason code", conversationprojection.ErrInvalidReasonCode, ch)
		}
	}
	return nil
}

// TagResult is returned from a successful TagNeverBackend call.
type TagResult struct {
	StateRevision uint64                       `json:"state_revision"`
	Tags          []conversationprojection.Tag `json:"tags"`
}

// ConversationViewTagger is the narrow tagger interface consumed by runtime.
type ConversationViewTagger interface {
	TagNeverBackend(ctx context.Context, aLegID string, tags []TagRequest) (TagResult, error)
}

// SteeringWriterResolver resolves the current call and snapshot for anchor calculation.
type SteeringWriterResolver func(ctx context.Context) (aipapi.Call, conversationprojection.Snapshot, error)

// SteeringWriterFactory constructs an authoritative steering writer for a given A-leg.
type SteeringWriterFactory func(ctx context.Context, aLegID string, resolver SteeringWriterResolver) (steering.Writer, error)

// BillingRuntime carries the runtime seams for two-stage exposure admission and
// durable terminal usage. Token quota and usage-authority stay on AccountingRuntime.
type BillingRuntime struct {
	// BillingCreditGate is the cheap settled-credit screen. Authoritative
	// billing requires it; when wired, runtime invokes it after identity
	// preparation and before route expansion.
	BillingCreditGate BillingCreditGate
	// BillingExposureAdmission is the authoritative operational-exposure seam.
	// When set, it replaces hold admission for this executor generation.
	BillingExposureAdmission BillingExposureAdmission
	// BillingLegObserver receives one recorded current call-leg usage record at terminal ownership.
	// It is observational only and never participates in authorization,
	// settlement, retry, cancellation, or client-visible output.
	BillingLegObserver BillingLegObserver
	TerminalUsageSink  billingport.TerminalUsageSink
	// BillingIdentity is the composition identity bundle for exposure admission
	// and terminal call-closure stamping. It contains no hold/authorization identity.
	BillingIdentity BillingIdentity
}

// hasTerminalSink reports whether the process-local terminal sink is wired.
func (e *Executor) hasTerminalSink() bool {
	return e != nil && e.TerminalUsageSink != nil
}

// hasTerminalCallSink reports whether the terminal sink can persist call closure.
// The leg and call records share one authoritative sink.
func (e *Executor) hasTerminalCallSink() bool {
	return e.hasTerminalSink()
}

// RoutingRuntime carries selector parsing, planning, negotiation, and affinity policy.
type RoutingRuntime struct {
	MaxAttempts             int
	DefaultBackend          string
	SelectorAliases         *routing.AliasResolver
	CapsResolver            capabilities.Resolver
	CatalogResolver         CatalogResolver
	EligibilityResolver     EligibilityResolver
	RequestTokenEstimator   RequestTokenEstimator
	CandidateHealth         policy.CandidateHealth
	RouteObserver           aipsdk.RouteObserver
	AffinityStore           affinity.Store
	AffinityMissingIdentity affinity.MissingIdentityPolicy
	TransportFallbackPolicy aipapi.TransportFallbackPolicy
	RouteOverrideReader     routeoverride.Reader
	// AttemptPolicy decides what an attempt candidate does when it
	// cannot replay the dialect of client-sent reasoning items: log_skip
	// (strip those parts for that attempt, the standard-distribution
	// default) or reject (today's hard refusal for strict operators).
	// Empty resolves to log_skip via clientReasoningPolicy.
	AttemptPolicy aipapi.AttemptPolicy

	ExecutionCompositionPolicy config.ExecutionCompositionPolicy
	BackendExecutionResolver   routing.BackendExecutionResolver
}

// SecurityRuntime carries secure-session gates, auth events, and session audit policy.
type SecurityRuntime struct {
	SecureSession                           *app.Manager
	SyntheticLocalPrincipal                 bool
	SecureSessionRecorder                   app.GateRecording
	SecureSessionRecordingMandatory         bool
	SessionDenialMapper                     func(error) error
	SecureSessionMetrics                    SecureSessionMetrics
	SecureSessionRequireWorkspaceID         bool
	SecureSessionWorkspaceResolveFailClosed bool
	AuthEvents                              *auth.EventDispatcher
	SessionAuditPolicy                      auth.SessionAuditPolicy
}

// AccountingRuntime carries token-accounting admission, usage reconstruction, and ledger hooks.
type AccountingRuntime struct {
	Preflight                    *accountingpreflight.Checker
	StreamUsage                  *accountingstream.Reconstructor
	TokenAccountingObservability *accountingobs.Stats
	AdminCountService            *accountingapp.Service
	UsageAuthority               UsageAuthorityService
	UsageAuthorityCleanupTimeout time.Duration
	// ConcurrencyProvider is the optional Phase 8 logical-request lease authority.
	ConcurrencyProvider authority.ConcurrencyProvider
	// ConcurrencyLeaseTTL / ConcurrencyRenewBefore are defaults used by heartbeat
	// when the admit decision omits rule-level values.
	ConcurrencyLeaseTTL    time.Duration
	ConcurrencyRenewBefore time.Duration
	// ConcurrencyAuxiliaryLeasePolicy is inherit (default) or acquire_own (10.10).
	ConcurrencyAuxiliaryLeasePolicy string
	// MeteringRecorder is the optional Phase 3 metering journal port. Nil means
	// checkpoints are retained in-request only (no durable append until Phase 5).
	MeteringRecorder metering.Recorder
	// MeteringObservationSink is the optional V2 evidence journal port. It is
	// deliberately separate from MeteringRecorder because pre-terminal economic
	// checkpoints persist immutable observations only; they never rate or mutate
	// money from a receive callback. Runtime checkpoint flushing requires the
	// sink to also implement metering.AtomicObservationSink; a plain
	// ObservationSink is retained for source compatibility but is not retried or
	// invoked by that path.
	MeteringObservationSink metering.ObservationSink
	// RequestCoordinator admits customer/logical-request authority once per request (Phase 6).
	RequestCoordinator *authoritycoord.RequestCoordinator
	// AttemptCoordinator admits operator/attempt authority per B-leg (Phase 6).
	AttemptCoordinator *authoritycoord.AttemptCoordinator
	// SnapshotGeneration is the atomic policy/rating generation publisher (Phase 9.3).
	// Admit binds Current() usage/concurrency/rating refs when present.
	SnapshotGeneration *snapshotgen.Publisher
	// TerminalWork accepts durable settle/release intents on post-output failures
	// (Phase 4.5; requirements 7.7, 8.3; design D9).
	TerminalWork *terminalworkapp.IntentService
}

// UsageAuthorityService is the runtime-owned boundary for accounting authority
// admission, settlement, release, advisory usage application, and bounded query access.
type UsageAuthorityService interface {
	Admit(ctx context.Context, in usagelimit.AdmissionInput) (usagelimit.AdmissionResult, error)
	Settle(ctx context.Context, in usagelimit.SettleInput) (usagelimit.SettleResult, error)
	Release(ctx context.Context, in usagelimit.ReleaseInput) (usagelimit.ReleaseResult, error)
	ApplyUsage(ctx context.Context, cmd usagelimit.ApplyUsageCommand) (usagelimit.ApplyUsageResult, error)
}

// ObservabilityRuntime carries structured logging, metrics, and diagnostics toggles.
type ObservabilityRuntime struct {
	Log                        *slog.Logger
	Metrics                    MetricsSink
	ExtensionMetrics           extensions.StageMetrics
	SecretGuardDecisionMetrics extensions.SecretGuardDecisionMetrics
	RouteTrace                 *diag.RouteTraceBuffer
	PolicyDiagnosticsEnabled   bool
	CompletionBufferLimits     completion.BufferLimits
	// SeamDigestsEnabled gates the content-free per-item digest feed for
	// the prompt-cache collapse diagnostic. Off by default: when false no
	// per-request hashing runs. When true the proxy hashes the model-bound
	// request that actually reaches the backend, per B-leg. Set from
	// observability.metrics.prompt_cache_collapse.seam_digests_enabled.
	SeamDigestsEnabled bool
	// SeamDigestKey is the process-stable HMAC key borrowed from the process
	// metrics bundle. Every generation executor copies the same process key,
	// so the same prompt yields the same digests across generation rebuilds.
	// It is never logged, persisted, or exposed: only the HMAC digests
	// travel on usage samples.
	SeamDigestKey SeamDigestKey
	// SystemPromptDigestEnabled gates the content-free diagnostic identity
	// feeds for the prompt-cache collapse detector: the provider-bound
	// instruction identity and the downstream switch identities (core
	// provider, optional adapter target, open credential, and usage provider
	// account). It is on whenever the diagnostic can consume them
	// (observability.metrics.enabled). The same gate also stamps the
	// request launch ordinal, so a published identity always has comparable
	// chronology. When false no identity hashing runs.
	SystemPromptDigestEnabled bool
	// SystemPromptDigestKey is the process-stable HMAC key borrowed from the
	// process metrics bundle. It protects the instruction-identity feed and,
	// with separate domain separation, the switch-identity feeds. It is the
	// same process key the seam-digest feed uses, copied into its own field
	// so each feed's key lifetime is explicit. It is never logged,
	// persisted, or exposed: only HMAC digests travel on usage samples.
	// A zero key disables the instruction and switch feeds rather than
	// publishing publicly reproducible fingerprints.
	SystemPromptDigestKey SeamDigestKey
	// SystemPromptLaunchSeq is the process-owned launch ordinal source shared
	// by every generation executor. The process composition injects the same
	// instance into each generation, so reloads and overlapping retirement do
	// not restart the chronology the collapse diagnostic compares. Nil means
	// no chronology is available; the diagnostic then stays conservative. A
	// direct-construction executor may leave it nil or set its own sequence.
	SystemPromptLaunchSeq *RequestLaunchSequence
	// ConversationViewObserver is optional narrow diagnostics for bounded conversation-view
	// projection/anchor/steering metrics. Nil is no-op. Labels are bounded enums only (placement, operation, policy, stage).
	ConversationViewObserver ConversationViewObserver
}

// ExtensionRuntime carries the hook bus and frozen per-build extension snapshot.
type ExtensionRuntime struct {
	Bus             *hooks.Bus
	RuntimeSnapshot *extensions.RequestRuntimeSnapshot

	// ToolCallFinalizationMaxArgsBytes is the assembler buffer cap from merged
	// feature bundles (0 means default at assembler construction).
	ToolCallFinalizationMaxArgsBytes int

	toolCallFinalizers []toolcall.Finalizer
}

// CompactionRuntime carries the generation-bound background scheduler client.
type CompactionRuntime struct {
	// BackgroundAux is the generation-bound process scheduler client. The
	// scheduler itself remains process-owned; this interface lets callbacks
	// submit work against the executor's frozen generation binding.
	BackgroundAux auxiliary.BackgroundClient
}

// ExecutorConfig groups executor dependencies for explicit construction at the
// composition root and in tests. Use [NewExecutor] to obtain a runnable executor.
type ExecutorConfig struct {
	Core          CoreRuntime
	Billing       BillingRuntime
	Routing       RoutingRuntime
	Security      SecurityRuntime
	Accounting    AccountingRuntime
	Observability ObservabilityRuntime
	Extension     ExtensionRuntime
	Compaction    CompactionRuntime
}

// NewExecutor constructs an [Executor] from grouped runtime configuration.
func NewExecutor(cfg ExecutorConfig) *Executor {
	if cfg.Routing.ExecutionCompositionPolicy == "" {
		cfg.Routing.ExecutionCompositionPolicy = config.ExecutionCompositionSafe
	}
	// Fail closed on a missing digest key: an enabled feed without the
	// process-stable HMAC secret would publish publicly reproducible
	// fingerprints, so the feed is refused instead of running keyless. The
	// request path re-checks the key for executors built outside this
	// constructor. Both content-free digest feeds share this rule.
	if cfg.Observability.SeamDigestsEnabled && cfg.Observability.SeamDigestKey.IsZero() {
		cfg.Observability.SeamDigestsEnabled = false
	}
	if cfg.Observability.SystemPromptDigestEnabled && cfg.Observability.SystemPromptDigestKey.IsZero() {
		cfg.Observability.SystemPromptDigestEnabled = false
	}
	// Keep direct-construction executors usable when the feed is armed. The
	// process composition injects its shared sequence before this constructor,
	// so a generation never reaches this fallback in production.
	if cfg.Observability.SystemPromptDigestEnabled && cfg.Observability.SystemPromptLaunchSeq == nil {
		cfg.Observability.SystemPromptLaunchSeq = NewRequestLaunchSequence()
	}
	if cfg.Routing.BackendExecutionResolver == nil && len(cfg.Core.Backends) > 0 {
		m := make(map[string]aipsdk.BackendExecutionClass, len(cfg.Core.Backends))
		for k := range cfg.Core.Backends {
			m[k] = aipsdk.BackendExecutionUnknown
		}
		cfg.Routing.BackendExecutionResolver = routing.BackendExecutionResolverFunc(func(id string) (aipsdk.BackendExecutionClass, bool) {
			c, ok := m[id]
			return c, ok
		})
	}
	return &Executor{
		CoreRuntime:          cfg.Core,
		BillingRuntime:       cfg.Billing,
		RoutingRuntime:       cfg.Routing,
		SecurityRuntime:      cfg.Security,
		AccountingRuntime:    cfg.Accounting,
		ObservabilityRuntime: cfg.Observability,
		ExtensionRuntime:     cfg.Extension,
		CompactionRuntime:    cfg.Compaction,
	}
}
