package modelcatalog

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// EligibilityDecision is the routing-time outcome for context limits before backend open.
type EligibilityDecision struct {
	IsEligible bool              `json:"eligible"`
	Reason     EligibilityReason `json:"reason,omitempty"`
	Facts      EffectiveFacts    `json:"facts,omitzero"`
	Estimate   SizeEstimate      `json:"estimate,omitzero"`
}

// EligibilityResolverImpl decides context-limit eligibility from already-resolved [EffectiveFacts]
// (design §EligibilityResolver). Wire it into [github.com/aiproxer/aiproxer/internal/core/runtime.Executor.EligibilityResolver].
type EligibilityResolverImpl struct {
	est sizeEstimator
}

// NewEligibilityResolver returns a resolver that never performs capability negotiation;
// it only compares a conservative size estimate to a known context limit from facts.
func NewEligibilityResolver(est sizeEstimator) *EligibilityResolverImpl {
	if est == nil {
		est = DefaultSizeEstimator{}
	}
	return &EligibilityResolverImpl{est: est}
}

// Check implements the executor eligibility contract.
func (e *EligibilityResolverImpl) Check(
	ctx context.Context,
	candidate routing.AttemptCandidate,
	call aipapi.Call,
	facts EffectiveFacts,
) EligibilityDecision {
	_ = candidate
	est := e.est.Estimate(ctx, call)

	if facts.Facts.ContextLimit.State != LimitPresent {
		return EligibilityDecision{IsEligible: true, Reason: EligibilityEligible, Facts: facts, Estimate: est}
	}
	if !est.Available {
		return EligibilityDecision{IsEligible: true, Reason: EligibilityEligible, Facts: facts, Estimate: est}
	}
	if est.Input > facts.Facts.ContextLimit.Tokens {
		return EligibilityDecision{IsEligible: false, Reason: EligibilityContextLimitExceeded, Facts: facts, Estimate: est}
	}
	return EligibilityDecision{IsEligible: true, Reason: EligibilityEligible, Facts: facts, Estimate: est}
}
