package largebody

import (
	"strings"
)

// DependencyClass categorizes a dependency's wire compatibility (Requirement 19.4).
type DependencyClass uint8

const (
	// DependencyClassUnknown is the zero value: unclassified or unfamiliar dependency.
	// Any unknown dependency causes assessment decline (Requirements 5.3, 5.7, 19.4).
	DependencyClassUnknown DependencyClass = iota
	// DependencyClassWireSafe indicates the dependency operates on bounded facts,
	// response events, or immutable source/digest contracts without full Call materialization.
	DependencyClassWireSafe
	// DependencyClassBlocker indicates the dependency requires full canonical Call
	// access or mutating request behavior. When occupied/active, assessment must decline.
	DependencyClassBlocker
)

// String returns a bounded static label for metrics and diagnostics.
func (c DependencyClass) String() string {
	switch c {
	case DependencyClassWireSafe:
		return "wire_safe"
	case DependencyClassBlocker:
		return "blocker"
	default:
		return "unknown"
	}
}

// IsWireSafe reports whether the dependency is proven wire-safe.
func (c DependencyClass) IsWireSafe() bool { return c == DependencyClassWireSafe }

// IsBlocker reports whether the dependency is a canonical-required blocker.
func (c DependencyClass) IsBlocker() bool { return c == DependencyClassBlocker }

// IsUnknown reports whether the dependency is unclassified or unfamiliar.
func (c DependencyClass) IsUnknown() bool { return c == DependencyClassUnknown }

// PortDependency represents one narrow port or callback fact in the dependency census.
type PortDependency struct {
	Name     string
	Class    DependencyClass
	Occupied bool
}

// standardNarrowPortCensus maps frozen narrow ports from evidence 1.8 to their
// default classification when occupied.
var standardNarrowPortCensus = map[string]DependencyClass{
	// Wire-safe ports when occupied/configured (evidence 1.8 section 3; Task 3.4):
	"core.store":                                DependencyClassWireSafe,
	"core.aleg_lifecycle":                       DependencyClassWireSafe,
	"core.clocks_rng_tuning":                    DependencyClassWireSafe,
	"core.prompt_cache_maintenance":             DependencyClassWireSafe,
	"core.conversation_view_observer":           DependencyClassWireSafe,
	"billing.credit_gate":                       DependencyClassWireSafe,
	"billing.leg_observer":                      DependencyClassWireSafe,
	"billing.terminal_usage_sink":               DependencyClassWireSafe,
	"billing.operator_rate_ref":                 DependencyClassWireSafe,
	"routing.selector_aliases_defaults":         DependencyClassWireSafe,
	"routing.health_observer_affinity_fallback": DependencyClassWireSafe,
	"routing.route_override_reader":             DependencyClassWireSafe,
	"routing.execution_policy_resolvers":        DependencyClassWireSafe,
	"security.session_manager":                  DependencyClassWireSafe,
	// Item 5: wire execution exercises the secure session recorder via canonical retryRecvStream
	// (RecordPostHookStreamEvent / recordClientFacing). security.session_recorder is wire-safe when occupied.
	"security.session_recorder":                 DependencyClassWireSafe,
	"security.flags_metrics_audit":              DependencyClassWireSafe,
	"accounting.token_observability":            DependencyClassWireSafe,
	"accounting.usage_authority":                DependencyClassWireSafe,
	"accounting.concurrency_leases":             DependencyClassWireSafe,
	"accounting.metering_recorder":              DependencyClassWireSafe,
	"accounting.coordinators_snapshot_terminal": DependencyClassWireSafe,
	"observability.logging_metrics":             DependencyClassWireSafe,
	"extension.runtime_snapshot":                DependencyClassWireSafe,
	"extension.terminal_policy_reader":          DependencyClassWireSafe,
	"extension.toolcall_finalization_cap":       DependencyClassWireSafe,
	"compaction.background_aux":                 DependencyClassWireSafe,

	// Blocker ports when occupied/configured (evidence 1.8 section 3; Task 3.4):
	"core.backends":                   DependencyClassBlocker, // Blocker when empty
	"core.conversation_view_reader":   DependencyClassBlocker,
	"core.conversation_view_tagger":   DependencyClassBlocker,
	"core.steering_writer_factory":    DependencyClassBlocker,
	"conversation.projection":         DependencyClassBlocker,
	"core.continuation":               DependencyClassBlocker,
	"continuation.resolver":           DependencyClassBlocker,
	"terminal.decision_provider":      DependencyClassBlocker,
	"terminal_decision.provider":      DependencyClassBlocker,
	"terminal_decision_provider":      DependencyClassBlocker,
	"billing.exposure_admission":      DependencyClassBlocker,
	"billing.identity_call_callbacks": DependencyClassBlocker,
	"routing.caps_resolver":           DependencyClassBlocker,
	"routing.catalog_resolver":        DependencyClassBlocker,
	"routing.eligibility_resolver":    DependencyClassBlocker,
	"routing.request_token_estimator": DependencyClassBlocker,
	"accounting.preflight":            DependencyClassBlocker,
	"accounting.stream_usage":         DependencyClassBlocker,
	"accounting.admin_count_service":  DependencyClassBlocker,
	"interleaved.processor":           DependencyClassBlocker,
	"compaction.detector":             DependencyClassBlocker,
	"backend.call_resolvers":          DependencyClassBlocker,
	"custom.call_callbacks":           DependencyClassBlocker,
	"traffic.port_bundle":             DependencyClassBlocker,
	"counting.token_rule":             DependencyClassBlocker,
	"core.two_phase_executor":         DependencyClassBlocker, // Blocker when missing
	"local_turn.handlers":             DependencyClassBlocker,
	"secret_guard.execution":          DependencyClassBlocker,
	"secret_guard.guards":             DependencyClassBlocker,
}

// LookupStandardPort returns the default classification for a known narrow port.
func LookupStandardPort(name string) (DependencyClass, bool) {
	class, ok := standardNarrowPortCensus[name]
	return class, ok
}

// DependencyCensus represents the frozen inventory of typed planes, hook chains,
// and narrow runtime ports/callbacks evaluated for wire eligibility (Task 11.3;
// Requirements 5, 13, 14, 15, 19; evidence 1.8, 1.9).
type DependencyCensus struct {
	Planes                    []PlaneEligibilityInput
	Hooks                     HookEligibilityInput
	Ports                     NarrowPortEligibilityInput
	TwoPhaseExecutorAvailable bool
	ExtraPorts                []PortDependency
}

// NewStandardDependencyCensus constructs a baseline dependency census covering
// every standard plane in the emitted serving catalog (unoccupied, with the
// emitted V1 access classes), empty hook
// bus, clear narrow ports (backends present), and two-phase executor available.
// The census itself is generation-agnostic: generation pinning lives on the sealed
// WireEligibilitySummary and is rechecked per evaluation. generationID is accepted
// for call-site symmetry with the gate constructor and is intentionally unused.
func NewStandardDependencyCensus(generationID string) DependencyCensus {
	_ = generationID
	planes := make([]PlaneEligibilityInput, WireEligibilityPlaneCount)
	for i := range WireEligibilityPlaneCount {
		id, _ := WireEligibilityPlaneID(i)
		planes[i] = PlaneEligibilityInput{
			ID:       id,
			Access:   standardPlaneV1Access[i],
			Occupied: false,
		}
	}
	return DependencyCensus{
		Planes: planes,
		Hooks:  HookEligibilityInput{},
		Ports: NarrowPortEligibilityInput{
			BackendsEmpty: false,
		},
		TwoPhaseExecutorAvailable: true,
	}
}

// RegisterPort registers or updates a narrow port or custom callback entry.
func (c *DependencyCensus) RegisterPort(name string, class DependencyClass, occupied bool) {
	for i := range c.ExtraPorts {
		if c.ExtraPorts[i].Name == name {
			c.ExtraPorts[i].Class = class
			c.ExtraPorts[i].Occupied = occupied
			return
		}
	}
	c.ExtraPorts = append(c.ExtraPorts, PortDependency{
		Name:     name,
		Class:    class,
		Occupied: occupied,
	})
}

// AddPort registers a port by name, automatically looking up its standard classification.
// If the port is unfamiliar, it is registered with DependencyClassUnknown (fails closed).
func (c *DependencyCensus) AddPort(name string, occupied bool) {
	class, ok := LookupStandardPort(name)
	if !ok {
		class = DependencyClassUnknown
	}
	c.RegisterPort(name, class, occupied)
}

// AuthorityAssessmentGate evaluates whether the frozen authority summary and
// current dependency census allow large-body fast-path execution (Task 11.3;
// Requirements 5, 13, 14, 15, 19).
//
// Invariants (Requirements 5.3, 5.7, 5.9, 5.10, 6.2, 19.4):
// - Every typed plane, hook chain, and non-plane port/callback must be known wire-safe.
// - Unknown port or plane declines immediately with DeclineReasonAuthorityBlocker.
// - Static summary is defensively rechecked (sealed, pinned to generation, zero blockers).
// - Zero hot-path reflection, zero heap allocations, zero I/O, zero network/store reads.
type AuthorityAssessmentGate struct {
	Summary      WireEligibilitySummary
	Census       DependencyCensus
	GenerationID string
}

// NewAuthorityAssessmentGate constructs an authority assessment gate.
func NewAuthorityAssessmentGate(summary WireEligibilitySummary, census DependencyCensus, generationID string) *AuthorityAssessmentGate {
	return &AuthorityAssessmentGate{
		Summary:      summary,
		Census:       census,
		GenerationID: generationID,
	}
}

// Evaluate evaluates the gate, returning AssessmentDecisionAccept if all authorities
// and census dependencies are wire-safe, or AssessmentDecisionDecline with a bounded
// reason (DeclineReasonAuthorityBlocker or DeclineReasonGenerationMismatch).
func (g *AuthorityAssessmentGate) Evaluate() (AssessmentDecision, DeclineReason) {
	// -------------------------------------------------------------------------
	// 1. Defensive static-summary re-check (Requirement 5.9, 5.10):
	// O(1), bitwise and struct inspection only, zero reflection, zero I/O.
	// -------------------------------------------------------------------------
	if !g.Summary.Sealed() {
		return AssessmentDecisionDecline, DeclineReasonAuthorityBlocker
	}
	// Fail closed on a missing generation binding: an empty expected generation
	// must decline, never skip the pin recheck (Requirements 5.3, 5.9).
	if strings.TrimSpace(g.GenerationID) == "" || !g.Summary.PinnedTo(g.GenerationID) {
		return AssessmentDecisionDecline, DeclineReasonGenerationMismatch
	}
	if g.Summary.HasStaticBlocker() {
		return AssessmentDecisionDecline, DeclineReasonAuthorityBlocker
	}
	if g.Summary.PlaneBlockers() != 0 || g.Summary.HookBlockers() != 0 || g.Summary.PortBlockers() != 0 {
		return AssessmentDecisionDecline, DeclineReasonAuthorityBlocker
	}

	// -------------------------------------------------------------------------
	// 2. Typed plane census verification (Requirements 5.1-5.4, 13.3-13.5, 19.4):
	// Every plane must be known (index ok), classified, and not occupied if canonical-required.
	// -------------------------------------------------------------------------
	for _, p := range g.Census.Planes {
		idx, ok := WireEligibilityPlaneIndex(p.ID)
		if !ok {
			// Unknown plane -> decline
			return AssessmentDecisionDecline, DeclineReasonAuthorityBlocker
		}
		if p.Access == PlaneAccessUnclassified {
			// Unclassified plane -> decline
			return AssessmentDecisionDecline, DeclineReasonAuthorityBlocker
		}
		// Parity check with frozen manifest order: the index must round-trip
		// through the frozen plane table back to the same plane ID.
		if rid, ok := WireEligibilityPlaneID(idx); !ok || rid != p.ID {
			return AssessmentDecisionDecline, DeclineReasonAuthorityBlocker
		}
		// Task 12.4: Local Turn and Secret Guard planes are non-negotiable static canonical blockers in V1
		// (Requirements 5.4, 13.4, 19.2, 19.4). They cannot be made wire-safe incidentally: if occupied,
		// or if their V1 canonical-required classification was weakened, assessment must decline.
		if isV1NonNegotiableCanonicalPlane(p.ID) {
			if p.Occupied || p.Access != PlaneAccessCanonicalRequired {
				return AssessmentDecisionDecline, DeclineReasonAuthorityBlocker
			}
		}
		if (p.Access == PlaneAccessCanonicalRequired || p.Access == PlaneAccessResponseOnly) && p.Occupied {
			// Blocker 2: Occupied canonical-required or response-only plane -> decline
			return AssessmentDecisionDecline, DeclineReasonAuthorityBlocker
		}
	}

	// -------------------------------------------------------------------------
	// 3. Hook chain census verification (Requirements 5.7, 13.3):
	// Independently re-evaluate the current census through the shared
	// hook policy: every occupied chain (submit, request-part,
	// response-part, tool) blocks when occupied (Blocker 2). The sealed
	// summary alone never authorizes.
	// -------------------------------------------------------------------------
	if _, hookBlockers := compileHookChains(g.Census.Hooks); hookBlockers != 0 {
		return AssessmentDecisionDecline, DeclineReasonAuthorityBlocker
	}

	// -------------------------------------------------------------------------
	// 4. Narrow port census verification (Requirements 5.6, 15.5, 19.4):
	// Independently re-evaluate the current census through the shared
	// narrow-port policy, including the two-phase executor capability.
	// -------------------------------------------------------------------------
	if portBlockers := compilePortBlockers(g.Census.Ports, g.Census.TwoPhaseExecutorAvailable); portBlockers != 0 {
		return AssessmentDecisionDecline, DeclineReasonAuthorityBlocker
	}

	// -------------------------------------------------------------------------
	// 5. Extra / custom ports and callbacks verification (Requirement 19.4):
	// Unknown port or occupied blocker declines.
	// -------------------------------------------------------------------------
	for _, ep := range g.Census.ExtraPorts {
		if ep.Class.IsUnknown() {
			// Unknown port -> decline
			return AssessmentDecisionDecline, DeclineReasonAuthorityBlocker
		}
		if ep.Class == DependencyClassBlocker && ep.Occupied {
			// Occupied blocker -> decline
			return AssessmentDecisionDecline, DeclineReasonAuthorityBlocker
		}
		// Task 12.4/12.5: Local Turn, Secret Guard, and Terminal Decision cannot be marked wire-safe incidentally
		if ep.Occupied && (strings.Contains(ep.Name, "local_turn") || strings.Contains(ep.Name, "secret_guard") || strings.Contains(ep.Name, "terminal_decision") || strings.Contains(ep.Name, "terminal.decision")) {
			return AssessmentDecisionDecline, DeclineReasonAuthorityBlocker
		}
	}

	return AssessmentDecisionAccept, DeclineReasonNone
}
