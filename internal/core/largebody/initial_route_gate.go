package largebody

import (
	"context"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/routing"
)

// WireBackendResolver resolves a configured backend into its WireBackend capability
// for pure wire compatibility checks (Requirements 7, 8).
type WireBackendResolver interface {
	ResolveWireBackend(backendID string) (WireBackend, bool)
}

// WireBackendResolverFunc adapts a function into a WireBackendResolver.
type WireBackendResolverFunc func(backendID string) (WireBackend, bool)

func (f WireBackendResolverFunc) ResolveWireBackend(backendID string) (WireBackend, bool) {
	if f == nil {
		return nil, false
	}
	return f(backendID)
}

// WireBackendMap adapts a map into a WireBackendResolver.
type WireBackendMap map[string]WireBackend

func (m WireBackendMap) ResolveWireBackend(backendID string) (WireBackend, bool) {
	if m == nil {
		return nil, false
	}
	wb, ok := m[backendID]
	return wb, ok
}

// InitialRouteAssessmentGate evaluates whether the exact initial route candidate set
// derived under canonical rules is wire-compatible across all candidates (Task 11.4;
// Requirements 7, 8, 10).
//
// Invariants (Requirements 7.3, 8.3, 8.4, 10.3, 10.4):
// - Reuses current alias resolution, default backend, execution composition, and native model rules.
// - Preserves sequential/fallback, weighted, and parallel/race candidate order and membership exactly.
// - Proves backend wire support for every possible candidate; any incompatible candidate declines the whole request.
// - Core shall NOT prune or reorder candidates to force wire compatibility.
// - Pure and side-effect-free: zero provider network I/O, zero store reads/writes.
type InitialRouteAssessmentGate struct {
	Aliases                    *routing.AliasResolver
	DefaultBackend             string
	BackendExecutionResolver   routing.BackendExecutionResolver
	ExecutionCompositionPolicy config.ExecutionCompositionPolicy
	NativeModelResolver        routing.NativeModelResolver
	// DefaultRouteBindings are the generation-owned model bindings the derived
	// default route needs. They are consulted only for a request whose route
	// origin is that default route, so the wire lane and the canonical lane
	// resolve the same model identity for it.
	DefaultRouteBindings routing.DefaultRouteBindings
	BackendResolver      WireBackendResolver
}

// NewInitialRouteAssessmentGate constructs an InitialRouteAssessmentGate.
func NewInitialRouteAssessmentGate(
	aliases *routing.AliasResolver,
	defaultBackend string,
	execResolver routing.BackendExecutionResolver,
	policy config.ExecutionCompositionPolicy,
	nativeResolver routing.NativeModelResolver,
	backendResolver WireBackendResolver,
) *InitialRouteAssessmentGate {
	return &InitialRouteAssessmentGate{
		Aliases:                    aliases,
		DefaultBackend:             defaultBackend,
		BackendExecutionResolver:   execResolver,
		ExecutionCompositionPolicy: policy,
		NativeModelResolver:        nativeResolver,
		BackendResolver:            backendResolver,
	}
}

// routeModelResolver returns the resolver the assessment composes the route
// with.
//
// A request whose route origin is the generation's derived default route keeps
// the generation's frozen model bindings, so the wire lane addresses the model
// the backend published even after an inventory refresh withdrew it. Every
// other route keeps the lane's own resolution.
func (g *InitialRouteAssessmentGate) routeModelResolver(proof Proof) routing.NativeModelResolver {
	if !proof.RouteOrigin.IsDefaultRoute() {
		return g.NativeModelResolver
	}
	return routing.NewFrozenDefaultModelResolver(g.NativeModelResolver, g.DefaultRouteBindings)
}

// Evaluate evaluates proof through canonical routing composition and verifies pure
// wire support across the entire initial candidate set.
// If any candidate is incompatible, the entire request declines (no pruning).
func (g *InitialRouteAssessmentGate) Evaluate(ctx context.Context, proof Proof) (AssessmentDecision, DeclineReason, []routing.AttemptCandidate) {
	if ctx != nil && ctx.Err() != nil {
		return AssessmentDecisionDecline, DeclineReasonCanceled, nil
	}
	if strings.TrimSpace(proof.ProfileID) == "" {
		return AssessmentDecisionDecline, DeclineReasonProofUncertain, nil
	}

	rawSelector := strings.TrimSpace(proof.RouteSelector)
	if rawSelector == "" {
		rawSelector = strings.TrimSpace(proof.ClientModel)
	}
	if rawSelector == "" {
		return AssessmentDecisionDecline, DeclineReasonRouteIncompatible, nil
	}

	// 1. Canonical routing composition: alias, parse, default backend, execution composition, native binding
	cands, sel, err := routing.ComposeInitialCandidates(
		rawSelector,
		g.Aliases,
		g.DefaultBackend,
		g.BackendExecutionResolver,
		g.ExecutionCompositionPolicy,
		g.routeModelResolver(proof),
	)
	if err != nil || sel == nil || len(cands) == 0 {
		return AssessmentDecisionDecline, DeclineReasonRouteIncompatible, nil
	}

	// 2. Candidate set verification (order and membership must match InitialCandidates)
	expectedCands := routing.InitialCandidates(sel)
	if !routing.CandidateSetsEqual(cands, expectedCands) {
		return AssessmentDecisionDecline, DeclineReasonRouteIncompatible, nil
	}

	// 3. Request-size constraints check (Requirements 7, 8, 19):
	// Content-dependent estimator/requirement without exact source contract
	// declines. Proof-level assessment carries no source reader, so an exact
	// token contract cannot be evaluated at this layer (replay-level counting
	// with exact semantics lives in the 10.4 wire-counter gate where the
	// immutable source is available).
	if routing.SelectorHasRequestSizeConstraints(sel) {
		return AssessmentDecisionDecline, DeclineReasonCountingUnsupported, nil
	}

	// 4. Prove wire support for every candidate in the set (NO PRUNING)
	for _, cand := range cands {
		backendID := strings.TrimSpace(cand.Primary.Backend)
		if backendID == "" {
			return AssessmentDecisionDecline, DeclineReasonRouteIncompatible, nil
		}
		if g.BackendResolver == nil {
			return AssessmentDecisionDecline, DeclineReasonBackendIncompatible, nil
		}
		wb, ok := g.BackendResolver.ResolveWireBackend(backendID)
		if !ok || wb == nil {
			return AssessmentDecisionDecline, DeclineReasonBackendIncompatible, nil
		}

		candModel := cand.Primary.WireModel()
		candFacts := WireRequestFacts{
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

		backendCand := routing.BackendFacingCandidate(cand)
		support := wb.ResolveWireRequest(ctx, candFacts, backendCand)
		if !support.Compatible {
			// Any incompatible candidate declines the entire wire request (Requirement 8.4)
			return AssessmentDecisionDecline, DeclineReasonBackendIncompatible, nil
		}

		if support.NeedsModelRewrite && !proof.Rewrite.NeedsModelRewrite() {
			return AssessmentDecisionDecline, DeclineReasonRewriteUnsupported, nil
		}
		if candModel != proof.ClientModel {
			if !support.NeedsModelRewrite || !proof.Rewrite.NeedsModelRewrite() {
				return AssessmentDecisionDecline, DeclineReasonRewriteUnsupported, nil
			}
		}
	}

	return AssessmentDecisionAccept, DeclineReasonNone, cands
}

// EvaluateRouteFacts evaluates routing candidates directly from Task 12.1 bounded wire facts.
func (g *InitialRouteAssessmentGate) EvaluateRouteFacts(
	ctx context.Context,
	route WireRouteFacts,
	protocol WireProtocolFacts,
	rewrite WireRewriteFacts,
	source WireSourceFacts,
	maxOutput WireMaxOutputFacts,
) (AssessmentDecision, DeclineReason, []routing.AttemptCandidate) {
	proof := Proof{
		ProfileID:       route.ProfileID,
		RouteSelector:   route.RouteSelector,
		RouteOrigin:     route.RouteOrigin,
		ClientModel:     route.ClientModel,
		Operation:       protocol.Operation,
		Delivery:        protocol.Delivery,
		MaxOutputTokens: maxOutput.MaxOutputTokens,
		Source:          source.SourceDigest,
		BodyBytes:       source.BodyBytes,
		Mode:            source.BodyMode,
		Rewrite:         rewrite.Semantics,
		ModelSpan:       rewrite.ModelSpan,
	}
	return g.Evaluate(ctx, proof)
}

// EvaluateTurnFacts evaluates routing candidates directly from WireTurnFacts.
func (g *InitialRouteAssessmentGate) EvaluateTurnFacts(
	ctx context.Context,
	facts WireTurnFacts,
) (AssessmentDecision, DeclineReason, []routing.AttemptCandidate) {
	return g.EvaluateRouteFacts(ctx, facts.Route, facts.Protocol, facts.Rewrite, facts.Source, facts.MaxOutput)
}

// ProveCandidateSet verifies that expected matches the canonical composition output in exact order
// and membership, and that every candidate in that set is wire-compatible without pruning.
func (g *InitialRouteAssessmentGate) ProveCandidateSet(ctx context.Context, proof Proof, expected []routing.AttemptCandidate) (AssessmentDecision, DeclineReason) {
	decision, reason, cands := g.Evaluate(ctx, proof)
	if decision == AssessmentDecisionDecline {
		return decision, reason
	}
	if !routing.CandidateSetsEqual(cands, expected) {
		return AssessmentDecisionDecline, DeclineReasonRouteIncompatible
	}
	return AssessmentDecisionAccept, DeclineReasonNone
}
