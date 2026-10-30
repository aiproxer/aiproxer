package largebody

import (
	"context"
	"sort"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/core/routing"
)

// RouteOverrideAssessmentGate evaluates whether the late route-override compatibility
// envelope derived from generation validator, known backends, and execution composition
// policy is wire-compatible across all legal outcomes (Task 11.5; Requirements 7, 8).
//
// Invariants (Requirements 6.2, 7.1, 7.2, 7.3, 7.5, 8.2, 8.3, 8.4, 8.5):
// - Mere presence of RouteOverrideReader shall NOT automatically block.
// - Pure and side-effect-free: strictly ZERO live store reads (no Snapshot/Get calls).
// - Derives all legal outcomes from generation validator + known backends + execution policy.
// - Unbounded override model domain requires universal backend proof (AnyAcceptedModel); otherwise decline.
// - Any incompatible candidate backend declines the entire request (no candidate pruning).
type RouteOverrideAssessmentGate struct {
	// OverrideReader is the optional narrow route-override snapshot reader.
	// When nil, route overrides are not configured and the gate accepts with no domain.
	// When non-nil, its presence alone must not block; the gate derives the legal
	// envelope without calling Snapshot or reading the store.
	OverrideReader routeoverride.Reader

	// GenerationValidator encapsulates the route validation policy of the active generation.
	GenerationValidator *routing.GenerationSelectorValidator

	// KnownBackends is the set of backends known to the current generation.
	// If GenerationValidator is provided, its KnownBackends is used by default.
	KnownBackends map[string]struct{}

	// ExecutionCompositionPolicy is the generation's execution composition policy.
	// BackendExecutionResolver resolves backend execution classes.
	// Both are carried for constructor symmetry with the generation validator;
	// execution-mode domain narrowing is 11.7 scope. The 11.5 envelope treats
	// every known backend as a legal post-override target (exact set, not a superset).
	ExecutionCompositionPolicy config.ExecutionCompositionPolicy
	BackendExecutionResolver   routing.BackendExecutionResolver

	// BackendResolver resolves configured backend IDs into WireBackend instances
	// for pure domain support checks.
	BackendResolver WireBackendResolver

	// CandidateModels specifies an optional finite model catalog.
	// When empty/nil, the model domain is unbounded (UniversalModel: true)
	// and requires backend universal proof (AnyAcceptedModel).
	CandidateModels []string
}

// NewRouteOverrideAssessmentGate constructs a RouteOverrideAssessmentGate.
func NewRouteOverrideAssessmentGate(
	reader routeoverride.Reader,
	val *routing.GenerationSelectorValidator,
	backendResolver WireBackendResolver,
) *RouteOverrideAssessmentGate {
	gate := &RouteOverrideAssessmentGate{
		OverrideReader:      reader,
		GenerationValidator: val,
		BackendResolver:     backendResolver,
	}
	if val != nil {
		gate.KnownBackends = val.KnownBackends
		gate.ExecutionCompositionPolicy = val.ExecutionCompositionPolicy
		gate.BackendExecutionResolver = val.BackendExecutionResolver
	}
	return gate
}

// CandidateBackends returns the sorted set of all backend IDs that can legally be selected
// by a post-commit route override under this generation's validator.
// Returns nil if KnownBackends is unconstrained (nil).
func (g *RouteOverrideAssessmentGate) CandidateBackends() []string {
	if g == nil {
		return nil
	}
	if g.GenerationValidator != nil {
		return g.GenerationValidator.LegalCandidateBackends()
	}
	if g.KnownBackends == nil {
		return nil
	}
	backends := make([]string, 0, len(g.KnownBackends))
	for id := range g.KnownBackends {
		backends = append(backends, id)
	}
	sort.Strings(backends)
	return backends
}

// Evaluate derives the legal post-override outcome envelope and verifies pure wire
// domain support across all legal outcomes WITHOUT reading the live store.
func (g *RouteOverrideAssessmentGate) Evaluate(ctx context.Context, proof Proof) (AssessmentDecision, DeclineReason, WireDomainFacts) {
	if ctx != nil && ctx.Err() != nil {
		return AssessmentDecisionDecline, DeclineReasonCanceled, WireDomainFacts{}
	}
	if strings.TrimSpace(proof.ProfileID) == "" {
		return AssessmentDecisionDecline, DeclineReasonProofUncertain, WireDomainFacts{}
	}

	// 1. If RouteOverrideReader is nil, route overrides are not configured/active.
	// No late route override can occur, so the envelope is empty and accepted.
	if g.OverrideReader == nil {
		return AssessmentDecisionAccept, DeclineReasonNone, WireDomainFacts{}
	}

	// 2. Derive legal candidate backends from the generation validator / known backends.
	// Note: We strictly DO NOT call g.OverrideReader.Snapshot or read the store.
	candidateBackends := g.CandidateBackends()
	if len(candidateBackends) == 0 {
		// Unconstrained/unknown backends (nil) or no known backends (empty) cannot be proven.
		return AssessmentDecisionDecline, DeclineReasonRouteIncompatible, WireDomainFacts{}
	}

	// 3. Determine model domain: unbounded by default (UniversalModel: true) unless
	// explicitly bounded by CandidateModels.
	isUniversal := len(g.CandidateModels) == 0
	domainFacts := WireDomainFacts{
		ProfileID:            proof.ProfileID,
		Operation:            proof.Operation,
		Delivery:             proof.Delivery,
		BodyMode:             proof.Mode,
		Rewrite:              proof.Rewrite,
		UniversalModel:       isUniversal,
		CandidateModels:      g.CandidateModels,
		RequiredCapabilities: proof.RequiredCapabilities,
	}

	budget := SemanticFactBudget(ctx)
	if err := domainFacts.Validate(budget); err != nil {
		return AssessmentDecisionDecline, DeclineReasonProofUncertain, WireDomainFacts{}
	}

	if g.BackendResolver == nil {
		return AssessmentDecisionDecline, DeclineReasonBackendIncompatible, WireDomainFacts{}
	}

	// 4. Prove wire domain support for EVERY legal candidate backend (NO PRUNING).
	for _, backendID := range candidateBackends {
		wb, ok := g.BackendResolver.ResolveWireBackend(backendID)
		if !ok || wb == nil {
			return AssessmentDecisionDecline, DeclineReasonBackendIncompatible, WireDomainFacts{}
		}

		support := wb.ResolveWireDomain(ctx, domainFacts)
		if ctx != nil && ctx.Err() != nil {
			return AssessmentDecisionDecline, DeclineReasonCanceled, WireDomainFacts{}
		}

		if !support.Compatible {
			// Any incompatible candidate backend declines the entire wire request (Requirement 8.4)
			return AssessmentDecisionDecline, DeclineReasonBackendIncompatible, WireDomainFacts{}
		}

		if domainFacts.UniversalModel && !support.AnyAcceptedModel {
			// Unbounded override model domain needs backend universal proof such as AnyAcceptedModel;
			// otherwise decline (Requirements 7.3, 8.2).
			return AssessmentDecisionDecline, DeclineReasonBackendIncompatible, WireDomainFacts{}
		}
	}

	return AssessmentDecisionAccept, DeclineReasonNone, domainFacts
}
