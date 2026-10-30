package largebody

import (
	"context"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/routing"
)

// BackendWireProofGate composes exact initial-route candidate proof and late-route
// domain proof (route override and late selector envelopes), verifying pure backend
// wire support across all candidates and domain members (Task 11.7; Requirements 7, 8, 9, 21).
//
// Invariants (Requirements 7.2, 7.3, 7.4, 7.5, 8.1, 8.2, 8.3, 8.4, 9.1, 9.6, 21.5):
// - Pass immutable body mode and rewrite facts to every resolver call (both ResolveWireRequest and ResolveWireDomain).
// - Any candidate or domain member incompatibility declines the entire request (no pruning).
// - Homogeneous same-wire domain accepts; heterogeneous incompatible domain declines.
// - Proves whether a post-BeginTurn route override change falls strictly inside the accepted domain.
// - Pure and side-effect-free: strictly zero live store reads, zero provider network I/O.
type BackendWireProofGate struct {
	InitialGate      *InitialRouteAssessmentGate
	OverrideGate     *RouteOverrideAssessmentGate
	LateSelectorGate *LateSelectorAssessmentGate
	BackendResolver  WireBackendResolver
}

// NewBackendWireProofGate constructs a BackendWireProofGate.
func NewBackendWireProofGate(
	initialGate *InitialRouteAssessmentGate,
	overrideGate *RouteOverrideAssessmentGate,
	lateSelectorGate *LateSelectorAssessmentGate,
	backendResolver WireBackendResolver,
) *BackendWireProofGate {
	return &BackendWireProofGate{
		InitialGate:      initialGate,
		OverrideGate:     overrideGate,
		LateSelectorGate: lateSelectorGate,
		BackendResolver:  backendResolver,
	}
}

func (g *BackendWireProofGate) resolveBackend(backendID string) (WireBackend, bool) {
	if g.BackendResolver != nil {
		if wb, ok := g.BackendResolver.ResolveWireBackend(backendID); ok && wb != nil {
			return wb, true
		}
	}
	if g.InitialGate != nil && g.InitialGate.BackendResolver != nil {
		if wb, ok := g.InitialGate.BackendResolver.ResolveWireBackend(backendID); ok && wb != nil {
			return wb, true
		}
	}
	if g.OverrideGate != nil && g.OverrideGate.BackendResolver != nil {
		if wb, ok := g.OverrideGate.BackendResolver.ResolveWireBackend(backendID); ok && wb != nil {
			return wb, true
		}
	}
	if g.LateSelectorGate != nil && g.LateSelectorGate.BackendResolver != nil {
		if wb, ok := g.LateSelectorGate.BackendResolver.ResolveWireBackend(backendID); ok && wb != nil {
			return wb, true
		}
	}
	return nil, false
}

// Evaluate evaluates exact initial candidate wire proof and late route domain wire proof.
// Threads immutable body/rewrite facts to every resolver call.
// Any candidate or domain member incompatibility declines the entire request.
func (g *BackendWireProofGate) Evaluate(
	ctx context.Context,
	proof Proof,
) (AssessmentDecision, DeclineReason, WireRequestFacts, WireDomainFacts, []routing.AttemptCandidate) {
	if ctx != nil && ctx.Err() != nil {
		return AssessmentDecisionDecline, DeclineReasonCanceled, WireRequestFacts{}, WireDomainFacts{}, nil
	}
	if strings.TrimSpace(proof.ProfileID) == "" {
		return AssessmentDecisionDecline, DeclineReasonProofUncertain, WireRequestFacts{}, WireDomainFacts{}, nil
	}

	var wireReq WireRequestFacts
	var cands []routing.AttemptCandidate

	// 1. Exact initial candidate set wire proof (if configured)
	if g.InitialGate != nil {
		decision, reason, initialCands := g.InitialGate.Evaluate(ctx, proof)
		if decision == AssessmentDecisionDecline {
			return AssessmentDecisionDecline, reason, WireRequestFacts{}, WireDomainFacts{}, nil
		}
		cands = initialCands
		if len(cands) > 0 {
			candModel := cands[0].Primary.WireModel()
			wireReq = WireRequestFacts{
				ProfileID:            proof.ProfileID,
				Operation:            proof.Operation,
				Delivery:             proof.Delivery,
				BodyMode:             proof.Mode,
				Rewrite:              proof.Rewrite,
				ClientModel:          proof.ClientModel,
				CandidateModel:       candModel,
				MaxOutputTokens:      proof.MaxOutputTokens,
				RequiredCapabilities: proof.RequiredCapabilities,
			}
		}
	}

	// 2. Late route-override compatibility envelope (if configured)
	var domainFacts WireDomainFacts
	if g.OverrideGate != nil {
		ovDecision, ovReason, ovDomain := g.OverrideGate.Evaluate(ctx, proof)
		if ovDecision == AssessmentDecisionDecline {
			return AssessmentDecisionDecline, ovReason, WireRequestFacts{}, WireDomainFacts{}, nil
		}
		domainFacts = ovDomain
	}

	// 3. Other late selector authorities evaluation (if configured)
	if g.LateSelectorGate != nil {
		lsDecision, lsReason, lsDomain := g.LateSelectorGate.Evaluate(ctx, proof)
		if lsDecision == AssessmentDecisionDecline {
			return AssessmentDecisionDecline, lsReason, WireRequestFacts{}, WireDomainFacts{}, nil
		}
		unioned, unionReason, ok := unionWireDomainFacts(ctx, domainFacts, lsDomain)
		if !ok {
			return AssessmentDecisionDecline, unionReason, WireRequestFacts{}, WireDomainFacts{}, nil
		}
		domainFacts = unioned
	}

	return AssessmentDecisionAccept, DeclineReasonNone, wireReq, domainFacts, cands
}
