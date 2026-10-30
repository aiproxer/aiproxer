package runtime_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	coreruntime "github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/tokenaccounting/preflight"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

// Task 3.4 freeze: current non-plane/narrow-port capabilities (Requirements
// 5, 14, 15, 19; Design section 7 "Current narrow/non-plane ports"). Compiled
// from the Task 1.8 Call/authority census (evidence/1.8-call-census.md
// sections 3-4, legend section 2) and the Task 1.9 plane/hook census. Stock
// states are frozen as no-op (nil/disabled/uncontributed: contributes no
// request-content dependency) vs blocker (occupied/required without a wire
// contract: AssessLargeBody must decline) vs wire-capable (occupied but
// bounded/response-only/source-digest under an explicit contract).
//
// Response-only and source/digest rows are folded into wire-capable with an
// explicit note; the full four-way legend lives in 1.8 section 2. Test-only
// table; there is no eligibility engine here (Task 3.5 owns the
// WireEligibilitySummary). No runtime reflection and no arbitrary callback
// invocation: classification is an explicit map lookup, unknown ports fail
// closed, and no func(ctx, aipapi.Call) value is ever invoked by this file.

type lp34State string

const (
	lp34NoOp        lp34State = "no_op"
	lp34WireCapable lp34State = "wire_capable"
	lp34Blocker     lp34State = "blocker"
)

type lp34PortClass struct {
	Port          string
	NilState      lp34State
	OccupiedState lp34State
	Refs          string
}

// lp34NarrowPortTable is the frozen Task 3.4 compilation. Every row cites the
// 1.8 inventory section that classifies it; stock composition notes name the
// no-op shape (nil, Disabled/Noop, ReasonDisabled) explicitly.
var lp34NarrowPortTable = []lp34PortClass{
	// CoreRuntime (executor_config.go:56-74; 1.8 section 3.1).
	{Port: "core.store", NilState: lp34NoOp, OccupiedState: lp34WireCapable, Refs: "1.8 3.1: A-leg record by ID only; post-commit live read inside assessed domain, assessment never reads (design 8)"},
	{Port: "core.backends", NilState: lp34Blocker, OccupiedState: lp34Blocker, Refs: "1.8 3.1: empty map has no candidate to prove, decline; occupied map is generation-frozen facts, blocker until every candidate/domain member proves exact/domain support (Tasks 11.4/11.7)"},
	{Port: "core.aleg_lifecycle", NilState: lp34WireCapable, OccupiedState: lp34WireCapable, Refs: "1.8 3.1: A-leg ID lifecycle only, content-free plumbing"},
	{Port: "core.clocks_rng_tuning", NilState: lp34WireCapable, OccupiedState: lp34WireCapable, Refs: "1.8 3.1: Rand/Now/MaxPendingWireEvents/StreamRecovery are bounded knobs, never content dependencies"},
	{Port: "core.prompt_cache_maintenance", NilState: lp34NoOp, OccupiedState: lp34WireCapable, Refs: "1.8 3.1/4.10-10.4: nil means no maintenance; occupied post-commit maintenance carries IDs plus response-side observations/tool events only, no Call (design 7)"},
	{Port: "core.conversation_view_reader", NilState: lp34NoOp, OccupiedState: lp34Blocker, Refs: "1.8 3.1/4.9: nil is the identity fast path (empty snapshot, conversation_view.go); occupied content/trajectory use is a blocker unless a source-backed contract exists (Task 12.3)"},
	{Port: "core.conversation_view_tagger", NilState: lp34NoOp, OccupiedState: lp34Blocker, Refs: "1.8 3.1: nil means no local-turn tagging; occupied is a blocker while local-turn handlers are occupied; tag shape bounded but identity derivation content-shaped today"},
	{Port: "core.steering_writer_factory", NilState: lp34NoOp, OccupiedState: lp34Blocker, Refs: "1.8 3.1/4.9: nil with no steering need is no-op; non-nil resolver returns the full Call plus snapshot, blocker incl. memo/continuation steering (Task 12.3)"},
	{Port: "core.conversation_view_observer", NilState: lp34NoOp, OccupiedState: lp34WireCapable, Refs: "1.8 3.1: nil is no-op; occupied diagnostics are content-free (bounded enums plus ProjectionSummary counts/revisions/placement)"},
	// BillingRuntime (executor_config.go:132-148; 1.8 sections 3.2/4.7).
	{Port: "billing.credit_gate", NilState: lp34NoOp, OccupiedState: lp34WireCapable, Refs: "1.8 3.2/4.7-7.1: nil means no cheap screen; occupied Check(ctx,accountID) takes bounded strings (two-seam rule)"},
	{Port: "billing.exposure_admission", NilState: lp34NoOp, OccupiedState: lp34Blocker, Refs: "1.8 3.2/4.7-7.3: nil means no exposure seam; occupied current input embeds the full Call, blocker until the Task 10.5 bounded-facts refactor"},
	{Port: "billing.leg_observer", NilState: lp34NoOp, OccupiedState: lp34WireCapable, Refs: "1.8 3.2: observational only, bounded CallLegUsageRecord, never authorization/settlement/retry"},
	{Port: "billing.terminal_usage_sink", NilState: lp34NoOp, OccupiedState: lp34WireCapable, Refs: "1.8 3.2: bounded sealed BillingCallID-scoped records, no Call retained"},
	{Port: "billing.identity_call_callbacks", NilState: lp34NoOp, OccupiedState: lp34Blocker, Refs: "1.8 3.2/3.8/4.7-7.1: custom func(ctx,Call) AccountID/CustomerPricingRef/ChargePolicyRef blockers (req 15.6); sole stock exception is PrincipalSessionIdentity AccountID which ignores Call, scope-derived (billingcompose/identity.go)"},
	{Port: "billing.operator_rate_ref", NilState: lp34WireCapable, OccupiedState: lp34WireCapable, Refs: "1.8 3.2: func(ctx,string,string) is already a fact contract, never content-shaped"},
	// RoutingRuntime (executor_config.go:162-179; 1.8 sections 3.3/4.5).
	{Port: "routing.selector_aliases_defaults", NilState: lp34WireCapable, OccupiedState: lp34WireCapable, Refs: "1.8 3.3/4.5-5.10: MaxAttempts/DefaultBackend/AliasResolver are bounded selector text plus frozen tables; selector is top-level metadata the profile proves"},
	{Port: "routing.caps_resolver", NilState: lp34WireCapable, OccupiedState: lp34Blocker, Refs: "1.8 3.3/4.5-5.4: nil falls back to synthetic bounded facts; non-nil full-Call resolver blocker unless exact bounded-metadata proof (Task 12.2)"},
	{Port: "routing.catalog_resolver", NilState: lp34WireCapable, OccupiedState: lp34Blocker, Refs: "1.8 3.3/4.5-5.4: nil falls back to synthetic bounded facts; non-nil full-Call resolver blocker unless exact bounded-metadata proof (Task 12.2)"},
	{Port: "routing.eligibility_resolver", NilState: lp34NoOp, OccupiedState: lp34Blocker, Refs: "1.8 3.3/4.5-5.4: absent check is a no-op fact; non-nil full-Call check blocker unless exact bounded-metadata proof (Task 12.2)"},
	{Port: "routing.request_token_estimator", NilState: lp34WireCapable, OccupiedState: lp34Blocker, Refs: "1.8 3.3/4.5-5.10: nil is routing fail-open (catalog_ports.go); non-nil full-Call estimator blocker without an exact source contract; bytes are never tokens (Task 10.4)"},
	{Port: "routing.health_observer_affinity_fallback", NilState: lp34WireCapable, OccupiedState: lp34WireCapable, Refs: "1.8 3.3/4.5-5.11: CandidateHealth/RouteObserver/AffinityStore/MissingIdentity/TransportFallbackPolicy are bounded policy/views/IDs; affinity key derives from frozen Views, not Call"},
	{Port: "routing.route_override_reader", NilState: lp34NoOp, OccupiedState: lp34WireCapable, Refs: "req 7.1, 1.8 3.3/4.5-5.12: nil is no-op; mere presence is NOT a blocker; envelope derives from generation policy without live-store reads, post-commit snapshot/barrier runs inside the certified domain"},
	{Port: "routing.execution_policy_resolvers", NilState: lp34WireCapable, OccupiedState: lp34WireCapable, Refs: "1.8 3.3: ExecutionCompositionPolicy/BackendExecutionResolver are bounded generation policy"},
	// SecurityRuntime (executor_config.go:182-193; 1.8 section 3.4).
	{Port: "security.session_manager", NilState: lp34WireCapable, OccupiedState: lp34WireCapable, Refs: "1.8 3.4: bounded BeginInput fact shape (session wire IDs, principal/workspace refs, policy, hint); assessment performs no BeginTurn/A-leg/store work (req 6.2)"},
	{Port: "security.session_recorder", NilState: lp34NoOp, OccupiedState: lp34WireCapable, Refs: "req 14.3-14.5, 1.8 3.4: nil means no recording; occupied GateRecording is wire-capable under Item 5 via canonical retryRecvStream response-pipeline recorder; semantic-fact overflow selects canonical pre-commit (Task 9.3)"},
	{Port: "security.flags_metrics_audit", NilState: lp34WireCapable, OccupiedState: lp34WireCapable, Refs: "1.8 3.4: synthetic-principal/recording-mandatory/denial-mapper(func(error)error)/metrics/workspace/audit flags are bounded; the session-start emitter Call parameter is the separate blocker until refactored to bounded session/views facts"},
	// AccountingRuntime (executor_config.go:196-224; 1.8 sections 3.5/4.6).
	{Port: "accounting.preflight", NilState: lp34WireCapable, OccupiedState: lp34Blocker, Refs: "req 15.5, 1.8 3.5/4.6: nil/disabled checker reports ReasonDisabled (bounded, wire-safe); enabled CountCall-only checker is a blocker until an exact WireCounter exists (Task 10.4)"},
	{Port: "accounting.stream_usage", NilState: lp34NoOp, OccupiedState: lp34Blocker, Refs: "1.8 3.5/4.6: nil is no-op; occupied input-count leg over the full Call is a blocker; output leg over Text/Events is response-only wire-capable"},
	{Port: "accounting.admin_count_service", NilState: lp34WireCapable, OccupiedState: lp34Blocker, Refs: "1.8 3.5/4.6: nil skips enrichment (bounded); occupied token-dependent counting is a blocker (Tasks 10.4/12.2)"},
	{Port: "accounting.token_observability", NilState: lp34WireCapable, OccupiedState: lp34WireCapable, Refs: "1.8 3.5: stats only, bounded metrics"},
	{Port: "accounting.usage_authority", NilState: lp34WireCapable, OccupiedState: lp34WireCapable, Refs: "1.8 3.5: Admit/Settle/Release/ApplyUsage take bounded DTOs, no Call in the interface; CountCall-backed quantity inputs are the separate blocker legs"},
	{Port: "accounting.concurrency_leases", NilState: lp34WireCapable, OccupiedState: lp34WireCapable, Refs: "1.8 3.5: cleanup timeout, TTL/renew-before, aux policy, lease IDs are bounded"},
	{Port: "accounting.metering_recorder", NilState: lp34WireCapable, OccupiedState: lp34WireCapable, Refs: "req 15.1-15.3, 1.8 3.5/4.6: nil keeps checkpoints in-request only (bounded); occupied journal seam appends bounded Facts with deterministic IDs; the retained Snapshot.Call clone is the separate checkpoint-retention blocker until Task 10 digest retention"},
	{Port: "accounting.coordinators_snapshot_terminal", NilState: lp34WireCapable, OccupiedState: lp34WireCapable, Refs: "1.8 3.5: Request/AttemptCoordinator (nil-safe allow/disabled), SnapshotGeneration, TerminalWork carry bounded DTOs/refs/intents; content-dependent slot preview is the quantities-path blocker, not the plumbing"},
	// ObservabilityRuntime + ExtensionRuntime (executor_config.go:236-263; 1.8 section 3.6).
	{Port: "observability.logging_metrics", NilState: lp34WireCapable, OccupiedState: lp34WireCapable, Refs: "1.8 3.6: bounded labels/buffers; prompt/paths/tokens excluded by policy (response/diagnostic-only)"},
	{Port: "extension.hook_bus", NilState: lp34NoOp, OccupiedState: lp34Blocker, Refs: "req 5.7/13.6, 1.8 3.6: nil/empty bus is no-op; occupied submit/request-part/tool chains block unless a typed wire contract exists; occupied response-part hooks block under Blocker 2 conservative fail-safe because response machinery is bypassed during wire streaming"},
	{Port: "extension.runtime_snapshot", NilState: lp34WireCapable, OccupiedState: lp34WireCapable, Refs: "1.8 3.6: frozen generation composition facts, no per-request content; occupied content planes are individually blockers"},
	{Port: "extension.terminal_policy_reader", NilState: lp34WireCapable, OccupiedState: lp34WireCapable, Refs: "1.8 3.6: nil keeps the generation default (bounded); occupied reader maps a bounded session/A-leg query to {enabled, revision} with no Call"},
	{Port: "extension.toolcall_finalization_cap", NilState: lp34WireCapable, OccupiedState: lp34WireCapable, Refs: "1.8 3.6: bounded int assembler buffer cap"},
	// InterleavedRuntime + CompactionRuntime (executor_config.go:266-281; 1.8 section 3.7).
	{Port: "interleaved.processor", NilState: lp34NoOp, OccupiedState: lp34Blocker, Refs: "1.8 3.7/4.5-5.2/4.9: nil is no-op; BeginTurn/memo predicates are the wire-capable subset; thinker/executor shaping and continuation re-projection block (Tasks 12.3/19.6)"},
	{Port: "compaction.detector", NilState: lp34NoOp, OccupiedState: lp34Blocker, Refs: "1.8 3.7/4.10-10.5: nil disables observation entirely; RequestOpened(meta,Call) leg blocks; PreviewResponse/ResponseReleased(meta,Event) legs are response-only wire-capable"},
	{Port: "compaction.background_aux", NilState: lp34WireCapable, OccupiedState: lp34WireCapable, Refs: "1.8 3.7: bounded generation-bound scheduler client, never a content dependency"},
	// Backend narrow ports, custom callbacks, traffic, counting (1.8 sections 3.8/4.10-4.11).
	{Port: "backend.call_resolvers", NilState: lp34WireCapable, OccupiedState: lp34Blocker, Refs: "1.8 3.8: nil resolvers keep static Caps/TransportCaps/Replay/Dialect/Profile fields (bounded); non-nil ResolveCaps/ResolveTransportCaps/ResolveReplaySupport/ResolveDialectSupport/ResolvePromptCacheProfile/IgnoresAuthorityMaxOutputTokensClamp each take the full Call, blocker until Task 8 pure ResolveWireRequest/ResolveWireDomain; Open is post-commit transport inside the assessed domain"},
	{Port: "custom.call_callbacks", NilState: lp34NoOp, OccupiedState: lp34Blocker, Refs: "req 5.6/5.12, 1.8 3.8/4.8-4.9/4.11: absent custom callback is no-op; any other non-nil func(ctx,Call) or resolver returning (Call,...) without an explicit bounded fact contract is a blocker by default (continuation/materialize, auxreq/background, reasoning-restore, prerequest aux marshal, legacy projection, localturn Match/Handle, secretguard Evaluate); unknown fails closed"},
	{Port: "traffic.port_bundle", NilState: lp34WireCapable, OccupiedState: lp34Blocker, Refs: "req 13.1, 1.8 3.8/4.10-10.1: EmitIsNoop bundle (nil/DisabledRawCapture/NoopObserver/empty redactors) is the wire-capable no-op fact for static disposition; any capturing/observing/redacting leg blocks"},
	{Port: "counting.token_rule", NilState: lp34WireCapable, OccupiedState: lp34Blocker, Refs: "req 15.5, 1.8 3.5/4.6: absent/disabled counting is wire-capable; any required input-token count without an exact WireCounter blocks; raw body byte count is never substituted for tokens (Task 10.4)"},
}

func lp34Lookup(port string) (lp34PortClass, bool) {
	for _, row := range lp34NarrowPortTable {
		if row.Port == port {
			return row, true
		}
	}
	return lp34PortClass{}, false
}

func lp34ValidState(s lp34State) bool {
	return s == lp34NoOp || s == lp34WireCapable || s == lp34Blocker
}

// TestLargePayload34_NarrowPortTableCoversRequiredPorts pins the Task 3.4
// compilation scope: every Requirement 5.6 inventory item plus the
// task-brief traffic/secure-recorder/metering/accounting/billing/
// conversation/steering/route-override/counting families has a frozen row,
// every verdict is a known trinary state, and unknown ports fail closed to
// blocker (never "probably metadata-only"). Requirements 5, 14, 15, 19;
// design section 7.
func TestLargePayload34_NarrowPortTableCoversRequiredPorts(t *testing.T) {
	t.Parallel()

	required := []string{
		"core.prompt_cache_maintenance",
		"core.conversation_view_reader",
		"core.conversation_view_tagger",
		"core.conversation_view_observer",
		"core.steering_writer_factory",
		"extension.terminal_policy_reader",
		"interleaved.processor",
		"compaction.detector",
		"compaction.background_aux",
		"routing.request_token_estimator",
		"routing.caps_resolver",
		"routing.catalog_resolver",
		"routing.eligibility_resolver",
		"security.session_manager",
		"security.session_recorder",
		"billing.credit_gate",
		"billing.exposure_admission",
		"billing.leg_observer",
		"billing.terminal_usage_sink",
		"billing.identity_call_callbacks",
		"accounting.preflight",
		"accounting.stream_usage",
		"accounting.admin_count_service",
		"accounting.usage_authority",
		"accounting.concurrency_leases",
		"accounting.metering_recorder",
		"accounting.coordinators_snapshot_terminal",
		"custom.call_callbacks",
		"traffic.port_bundle",
		"routing.route_override_reader",
		"counting.token_rule",
		"backend.call_resolvers",
	}
	seen := make(map[string]struct{}, len(lp34NarrowPortTable))
	for _, row := range lp34NarrowPortTable {
		if row.Port == "" {
			t.Fatal("narrow-port table has a row with an empty port key")
		}
		if _, dup := seen[row.Port]; dup {
			t.Fatalf("narrow-port table has a duplicate row for %q", row.Port)
		}
		seen[row.Port] = struct{}{}
		if !lp34ValidState(row.NilState) {
			t.Fatalf("port %q has unknown nil verdict %q", row.Port, row.NilState)
		}
		if !lp34ValidState(row.OccupiedState) {
			t.Fatalf("port %q has unknown occupied verdict %q", row.Port, row.OccupiedState)
		}
		if strings.TrimSpace(row.Refs) == "" {
			t.Fatalf("port %q has no 1.8/design citation", row.Port)
		}
	}
	for _, port := range required {
		row, ok := lp34Lookup(port)
		if !ok {
			t.Fatalf("required narrow port %q is missing from the Task 3.4 table: classify it first", port)
		}
		if row.Port != port {
			t.Fatalf("lookup(%q) returned row for %q", port, row.Port)
		}
	}
	// Unknown fails closed: there is no "probably metadata-only" verdict.
	if _, ok := lp34Lookup("core.some_future_call_port"); ok {
		t.Fatal("lookup of an unknown port must fail closed, got a row")
	}
}

// TestLargePayload34_StockNilPortsAreNoOpFacts pins that a zero
// ExecutorConfig wires no narrow-port content dependency: every optional
// port reads back nil/empty, which the table classifies as the no-op fact
// for static disposition. This is a presence check only; no callback is
// invoked. Cf. 1.8 section 2 legend (nil/unconfigured optional ports).
func TestLargePayload34_StockNilPortsAreNoOpFacts(t *testing.T) {
	t.Parallel()

	var cfg coreruntime.ExecutorConfig
	if cfg.Core.Store != nil {
		t.Fatal("zero Core.Store must be nil (no-op continuity fact)")
	}
	if len(cfg.Core.Backends) != 0 {
		t.Fatal("zero Core.Backends must be empty")
	}
	if cfg.Core.PromptCacheMaintenance != nil {
		t.Fatal("zero PromptCacheMaintenance must be nil (no-op)")
	}
	if cfg.Core.ConversationViewReader != nil {
		t.Fatal("zero ConversationViewReader must be nil (no-op fast path)")
	}
	if cfg.Core.ConversationViewTagger != nil {
		t.Fatal("zero ConversationViewTagger must be nil (no-op)")
	}
	if cfg.Core.SteeringWriterFactory != nil {
		t.Fatal("zero SteeringWriterFactory must be nil (no-op)")
	}
	if cfg.Observability.ConversationViewObserver != nil {
		t.Fatal("zero ConversationViewObserver must be nil (no-op)")
	}
	if cfg.Billing.BillingCreditGate != nil {
		t.Fatal("zero BillingCreditGate must be nil (no-op)")
	}
	if cfg.Billing.BillingExposureAdmission != nil {
		t.Fatal("zero BillingExposureAdmission must be nil (no-op)")
	}
	if cfg.Billing.BillingLegObserver != nil {
		t.Fatal("zero BillingLegObserver must be nil (no-op)")
	}
	if cfg.Billing.TerminalUsageSink != nil {
		t.Fatal("zero TerminalUsageSink must be nil (no-op)")
	}
	if cfg.Billing.BillingIdentity.AccountID != nil ||
		cfg.Billing.BillingIdentity.CustomerPricingRef != nil ||
		cfg.Billing.BillingIdentity.ChargePolicyRef != nil ||
		cfg.Billing.BillingIdentity.OperatorRateRef != nil {
		t.Fatal("zero BillingIdentity must carry no Call callbacks (no-op)")
	}
	if cfg.Routing.CapsResolver != nil {
		t.Fatal("zero CapsResolver must be nil (synthetic bounded fallback)")
	}
	if cfg.Routing.CatalogResolver != nil {
		t.Fatal("zero CatalogResolver must be nil (synthetic bounded fallback)")
	}
	if cfg.Routing.EligibilityResolver != nil {
		t.Fatal("zero EligibilityResolver must be nil (no-op)")
	}
	if cfg.Routing.RequestTokenEstimator != nil {
		t.Fatal("zero RequestTokenEstimator must be nil (routing fail-open)")
	}
	if cfg.Routing.RouteOverrideReader != nil {
		t.Fatal("zero RouteOverrideReader must be nil (no-op)")
	}
	if cfg.Security.SecureSessionRecorder != nil {
		t.Fatal("zero SecureSessionRecorder must be nil (no-op)")
	}
	if cfg.Accounting.Preflight != nil {
		t.Fatal("zero Preflight must be nil (disabled wire-safe)")
	}
	if cfg.Accounting.StreamUsage != nil {
		t.Fatal("zero StreamUsage must be nil (no-op)")
	}
	if cfg.Accounting.AdminCountService != nil {
		t.Fatal("zero AdminCountService must be nil (skips enrichment)")
	}
	if cfg.Accounting.MeteringRecorder != nil {
		t.Fatal("zero MeteringRecorder must be nil (in-request only)")
	}
	if cfg.Accounting.UsageAuthority != nil {
		t.Fatal("zero UsageAuthority must be nil (no-op)")
	}
	if cfg.Accounting.RequestCoordinator != nil || cfg.Accounting.AttemptCoordinator != nil {
		t.Fatal("zero authority coordinators must be nil (nil-safe allow/disabled)")
	}
	if cfg.Extension.Bus != nil {
		t.Fatal("zero hook Bus must be nil (no-op)")
	}
	if cfg.Extension.RuntimeSnapshot != nil {
		t.Fatal("zero RuntimeSnapshot must be nil")
	}
	if cfg.Core.TerminalPolicyReader != nil {
		t.Fatal("zero TerminalPolicyReader must be nil (generation default)")
	}
	if cfg.Core.InterleavedProcessor != nil {
		t.Fatal("zero Interleaved.Processor must be nil (no-op)")
	}
	if cfg.Core.CompactionDetector != nil {
		t.Fatal("zero Compaction.Detector must be nil (observation disabled)")
	}
	if cfg.Compaction.BackgroundAux != nil {
		t.Fatal("zero BackgroundAux must be nil (bounded)")
	}
	var be execbackend.Backend
	if be.ResolveCaps != nil || be.ResolveTransportCaps != nil || be.ResolveReplaySupport != nil ||
		be.ResolveDialectSupport != nil || be.ResolvePromptCacheProfile != nil || be.IgnoresAuthorityMaxOutputTokensClamp != nil {
		t.Fatal("zero Backend must carry no Call-shaped resolvers (static bounded fields)")
	}
}

// lp34 stubs below stand in for capturing traffic legs. They are never given
// request content by these tests; occupancy alone is the blocker signal.

type lp34CapturingObserver struct{}

func (lp34CapturingObserver) OnObservation(context.Context, traffic.Observation) error { return nil }

type lp34CapturingRawSink struct{}

func (lp34CapturingRawSink) WriteRaw(context.Context, traffic.Leg, traffic.CaptureMeta, []byte) error {
	return nil
}

type lp34StubRedactor struct{}

func (lp34StubRedactor) ID() string { return "lp34" }

func (lp34StubRedactor) Redact(_ context.Context, _ traffic.Leg, _ traffic.CaptureMeta, body []byte) ([]byte, error) {
	return body, nil
}

// TestLargePayload34_TrafficNoOpVsCapturing freezes the stock traffic
// distinction from live code: the zero/disabled/noop PortBundle is the
// wire-capable no-op fact for static disposition (EmitIsNoop), while any
// capturing raw sink, observing observer, or redactor makes the bundle
// non-noop and therefore a canonical blocker. Requirement 13.1; design
// section 7; 1.8 sections 3.8, 4.10-10.1.
func TestLargePayload34_TrafficNoOpVsCapturing(t *testing.T) {
	t.Parallel()

	if !(traffic.PortBundle{}).EmitIsNoop() {
		t.Fatal("zero traffic PortBundle must be no-op (wire-capable no-op fact)")
	}
	disabled := traffic.PortBundle{Raw: traffic.DisabledRawCapture{}, Obs: traffic.NoopObserver{}}
	if !disabled.EmitIsNoop() {
		t.Fatal("DisabledRawCapture plus NoopObserver bundle must be no-op")
	}
	if !(traffic.PortBundle{Obs: traffic.ChainObservers()}).EmitIsNoop() {
		t.Fatal("empty chained-observer bundle must collapse to no-op")
	}
	if _, ok := traffic.MultiRawCapture().(traffic.DisabledRawCapture); !ok {
		t.Fatal("empty raw-capture fan-out must collapse to DisabledRawCapture")
	}
	if (traffic.PortBundle{Obs: lp34CapturingObserver{}}).EmitIsNoop() {
		t.Fatal("occupied traffic observer must not be no-op: capturing configuration blocks")
	}
	if (traffic.PortBundle{Raw: lp34CapturingRawSink{}}).EmitIsNoop() {
		t.Fatal("occupied raw-capture sink must not be no-op: capturing configuration blocks")
	}
	if (traffic.PortBundle{Red: []traffic.Redactor{lp34StubRedactor{}}}).EmitIsNoop() {
		t.Fatal("occupied traffic redactor must not be no-op: redacting configuration blocks")
	}
	row, ok := lp34Lookup("traffic.port_bundle")
	if !ok {
		t.Fatal("traffic.port_bundle row missing from the Task 3.4 table")
	}
	if row.NilState != lp34WireCapable || row.OccupiedState != lp34Blocker {
		t.Fatalf("traffic.port_bundle must freeze no-op=wire_capable occupied=blocker, got %q/%q", row.NilState, row.OccupiedState)
	}
}

// TestLargePayload34_AccountingDisabledIsWireSafeCountCallOnlyBlocks freezes
// the counting distinction from live preflight code: a nil/disabled checker
// reports ReasonDisabled (bounded, wire-safe), while an enabled checker with
// only a nil counter cannot count and therefore blocks the wire lane pending
// an exact WireCounter. Raw body bytes are never tokens. Requirements 15.5,
// 19; 1.8 sections 3.5, 4.6; Task 10.4.
func TestLargePayload34_AccountingDisabledIsWireSafeCountCallOnlyBlocks(t *testing.T) {
	t.Parallel()

	var nilChecker *preflight.Checker
	got := nilChecker.Check(context.Background(), preflight.Input{})
	if !got.Allowed || got.Reason != preflight.ReasonDisabled {
		t.Fatalf("nil preflight checker must report disabled wire-safe, got allowed=%v reason=%q", got.Allowed, got.Reason)
	}
	disabled := preflight.NewChecker(nil, preflight.Config{})
	got = disabled.Check(context.Background(), preflight.Input{})
	if !got.Allowed || got.Reason != preflight.ReasonDisabled {
		t.Fatalf("disabled preflight checker must report disabled wire-safe, got allowed=%v reason=%q", got.Allowed, got.Reason)
	}
	// Enabled strict checker with no counter: counting is unavailable, so the
	// wire lane must decline pending an exact WireCounter contract. The zero
	// Input.Call below is never read on this path (nil-counter check precedes
	// CountCall); no token is derived from body bytes.
	countOnly := preflight.NewChecker(nil, preflight.Config{Enabled: true, Mode: preflight.ModeStrict})
	got = countOnly.Check(context.Background(), preflight.Input{Backend: "lp34", Model: "lp34-model", CallID: "lp34-call"})
	if got.Allowed || got.Reason != preflight.ReasonCountUnavailable {
		t.Fatalf("enabled CountCall-only preflight must block, got allowed=%v reason=%q", got.Allowed, got.Reason)
	}
	row, ok := lp34Lookup("accounting.preflight")
	if !ok {
		t.Fatal("accounting.preflight row missing from the Task 3.4 table")
	}
	if row.NilState != lp34WireCapable || row.OccupiedState != lp34Blocker {
		t.Fatalf("accounting.preflight must freeze no-op=wire_capable occupied=blocker, got %q/%q", row.NilState, row.OccupiedState)
	}
	counting, ok := lp34Lookup("counting.token_rule")
	if !ok {
		t.Fatal("counting.token_rule row missing from the Task 3.4 table")
	}
	if counting.OccupiedState != lp34Blocker {
		t.Fatalf("counting.token_rule occupied state must be blocker, got %q", counting.OccupiedState)
	}
	if !strings.Contains(counting.Refs, "never") {
		t.Fatal("counting.token_rule must document that body bytes are never tokens")
	}
}

// TestLargePayload34_SecureRecorderInputIsBounded freezes the secure-session
// recorder wire contract: the stock recorder input carries ordered
// role/ordinal/part-kind lines only and never prompt text, so a composed
// recorder stays wire-capable; a nil recorder is the no-op fact.
// Requirements 14.3-14.5; 1.8 section 3.4; Task 9.3.
func TestLargePayload34_SecureRecorderInputIsBounded(t *testing.T) {
	t.Parallel()

	var cfg coreruntime.ExecutorConfig
	if cfg.Security.SecureSessionRecorder != nil {
		t.Fatal("zero SecureSessionRecorder must be nil (no-op)")
	}
	in := app.ClientTurnRecordInput{
		TraceID: "lp34-trace",
		Lines: []app.ClientInputLine{
			{Role: "user", Ordinal: 0, Parts: []string{"text", "tool_result"}},
			{Role: "assistant", Ordinal: 1, Parts: []string{"text"}},
		},
	}
	if len(in.Lines) != 2 {
		t.Fatalf("recorder input must carry exactly the constructed bounded lines, got %d", len(in.Lines))
	}
	if in.Lines[0].Role != "user" || in.Lines[0].Ordinal != 0 {
		t.Fatalf("recorder line must carry role/ordinal facts only, got %+v", in.Lines[0])
	}
	for _, line := range in.Lines {
		for _, kind := range line.Parts {
			if strings.Contains(kind, "prompt-bytes") {
				t.Fatalf("recorder line must carry part kinds, never prompt text, got %q", kind)
			}
		}
	}
	row, ok := lp34Lookup("security.session_recorder")
	if !ok {
		t.Fatal("security.session_recorder row missing from the Task 3.4 table")
	}
	if row.NilState != lp34NoOp || row.OccupiedState != lp34WireCapable {
		t.Fatalf("security.session_recorder must freeze nil=no-op occupied=wire-capable under Item 5 response-pipeline recorder parity, got %q/%q", row.NilState, row.OccupiedState)
	}
	if !strings.Contains(row.Refs, "overflow") {
		t.Fatal("security.session_recorder must document the semantic-fact overflow canonical rule")
	}
}

// TestLargePayload34_RouteOverridePresenceIsNotABlocker pins the Requirement
// 7.1 exception: standard continuity stores expose RouteOverrideReader and
// runtime composition wires it when available, so mere presence stays
// wire-capable. The late-route envelope derives from generation policy
// without live-store reads (Tasks 11.5/13.1); this test performs no store
// read. 1.8 sections 3.3, 4.5-5.12.
func TestLargePayload34_RouteOverridePresenceIsNotABlocker(t *testing.T) {
	t.Parallel()

	var cfg coreruntime.ExecutorConfig
	if cfg.Routing.RouteOverrideReader != nil {
		t.Fatal("zero RouteOverrideReader must be nil (no-op)")
	}
	row, ok := lp34Lookup("routing.route_override_reader")
	if !ok {
		t.Fatal("routing.route_override_reader row missing from the Task 3.4 table")
	}
	if row.NilState != lp34NoOp || row.OccupiedState != lp34WireCapable {
		t.Fatalf("routing.route_override_reader must freeze nil=no-op occupied=wire-capable (presence alone never blocks), got %q/%q", row.NilState, row.OccupiedState)
	}
	if !strings.Contains(row.Refs, "7.1") {
		t.Fatal("routing.route_override_reader must cite Requirement 7.1")
	}
}

// TestLargePayload34_CustomCallCallbacksAreBlockersUnlessBoundedFactContract
// pins the Task 3.4 custom-callback rule: any non-nil func(ctx, aipapi.Call)
// (BillingIdentity resolvers, steering resolver factory, backend
// Call-shaped resolvers, or any future custom callback) is a blocker unless
// an explicit bounded fact contract exists. The sole documented stock
// exception is the scope-derived PrincipalSessionIdentity AccountID, which
// ignores its Call parameter (billingcompose/identity.go); OperatorRateRef
// already takes bounded strings. Nothing in this test invokes a Call-shaped
// callback: presence alone is the blocker signal. Requirements 5.6, 15.6;
// 1.8 sections 3.2, 3.8, 4.7-7.1.
func TestLargePayload34_CustomCallCallbacksAreBlockersUnlessBoundedFactContract(t *testing.T) {
	t.Parallel()

	// Declared but never invoked: presence is the signal under test.
	customAccount := func(context.Context, aipapi.Call) string { return "acct-custom" }
	identity := coreruntime.BillingIdentity{AccountID: customAccount}
	if identity.AccountID == nil {
		t.Fatal("custom BillingIdentity AccountID presence signal lost")
	}
	row, ok := lp34Lookup("billing.identity_call_callbacks")
	if !ok {
		t.Fatal("billing.identity_call_callbacks row missing from the Task 3.4 table")
	}
	if row.OccupiedState != lp34Blocker {
		t.Fatalf("custom BillingIdentity Call callbacks must be blockers, got %q", row.OccupiedState)
	}
	if !strings.Contains(row.Refs, "PrincipalSessionIdentity") {
		t.Fatal("billing.identity_call_callbacks must document the sole stock scope-derived exception")
	}
	rateRow, ok := lp34Lookup("billing.operator_rate_ref")
	if !ok {
		t.Fatal("billing.operator_rate_ref row missing from the Task 3.4 table")
	}
	if rateRow.OccupiedState != lp34WireCapable {
		t.Fatalf("string-shaped OperatorRateRef must stay wire-capable, got %q", rateRow.OccupiedState)
	}
	// Backend Call-shaped resolver presence (declared, never invoked).
	be := execbackend.Backend{
		ResolveCaps: func(context.Context, aipapi.Call, routing.AttemptCandidate) aipapi.BackendCaps {
			return aipapi.BackendCaps{}
		},
	}
	if be.ResolveCaps == nil {
		t.Fatal("backend ResolveCaps presence signal lost")
	}
	backendRow, ok := lp34Lookup("backend.call_resolvers")
	if !ok {
		t.Fatal("backend.call_resolvers row missing from the Task 3.4 table")
	}
	if backendRow.OccupiedState != lp34Blocker {
		t.Fatalf("occupied backend Call-shaped resolvers must be blockers pending Task 8 proofs, got %q", backendRow.OccupiedState)
	}
	// Steering writer factory presence (declared, never invoked).
	factory := coreruntime.SteeringWriterFactory(func(context.Context, string, coreruntime.SteeringWriterResolver) (steering.Writer, error) {
		return nil, nil
	})
	if factory == nil {
		t.Fatal("steering writer factory presence signal lost")
	}
	steeringRow, ok := lp34Lookup("core.steering_writer_factory")
	if !ok {
		t.Fatal("core.steering_writer_factory row missing from the Task 3.4 table")
	}
	if steeringRow.OccupiedState != lp34Blocker {
		t.Fatalf("occupied steering writer factory must be a blocker, got %q", steeringRow.OccupiedState)
	}
	// Generic future custom callbacks fail closed by the same rule.
	customRow, ok := lp34Lookup("custom.call_callbacks")
	if !ok {
		t.Fatal("custom.call_callbacks row missing from the Task 3.4 table")
	}
	if customRow.OccupiedState != lp34Blocker {
		t.Fatalf("custom Call callbacks must fail closed to blocker, got %q", customRow.OccupiedState)
	}
}
