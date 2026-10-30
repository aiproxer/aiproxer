// Package causepolicy contains the provider-independent ALG eligibility policy
// for canonical terminal causes and bounded evidence.
package causepolicy

import (
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

// Eligibility is the only authority-free action exposed by this policy.
type Eligibility string

const (
	EligibilityStop     Eligibility = "stop"
	EligibilityVerifier Eligibility = "verifier_eligible"
)

// Reason is a bounded explanation for the eligibility result.
type Reason string

const (
	ReasonAuthoritative        Reason = "authoritative_candidate"
	ReasonPreOutputTransport   Reason = "pre_output_transport"
	ReasonMissingTrajectory    Reason = "missing_trajectory"
	ReasonVerifierEligible     Reason = "verifier_eligible"
	ReasonUnsafeAction         Reason = "unsafe_action"
	ReasonInvalidInput         Reason = "invalid_input"
	ReasonInsufficientEvidence Reason = "insufficient_evidence"
	ReasonOutputUncommitted    Reason = "output_not_committed"
	ReasonUnsupportedCause     Reason = "unsupported_cause"
	ReasonExplicitCompletion   Reason = "explicit_completion"
)

// Result is a small value-only policy result. It grants no terminal,
// lifecycle, verifier, or recovery authority.
type Result struct {
	Eligibility Eligibility
	Reason      Reason
}

// Evaluate classifies one canonical terminal candidate. Invalid or incomplete
// evidence fails closed; only committed normal/transport/limit/provider-error
// candidates with a safe trajectory and complete tool facts may be offered to
// a verifier.
func Evaluate(input terminaldecision.Input) Result {
	if err := input.Validate(); err != nil {
		return stop(ReasonInvalidInput)
	}
	if input.Candidate.Cause.Authoritative() {
		return stop(ReasonAuthoritative)
	}
	if input.Candidate.Cause == terminaldecision.CandidateCauseTransport && !input.Candidate.OutputCommitted {
		return stop(ReasonPreOutputTransport)
	}
	if input.Evidence.ExplicitCompletion {
		return stop(ReasonExplicitCompletion)
	}
	if !input.Candidate.OutputCommitted {
		return stop(ReasonOutputUncommitted)
	}
	switch input.Candidate.Cause {
	case terminaldecision.CandidateCauseNormal,
		terminaldecision.CandidateCauseTransport,
		terminaldecision.CandidateCauseLimit,
		terminaldecision.CandidateCauseProviderError:
		// These candidate causes can reach verifier policy.
	default:
		return stop(ReasonUnsupportedCause)
	}
	if !safeTrajectory(input) {
		return stop(ReasonMissingTrajectory)
	}
	if !safeActions(input) {
		return stop(ReasonUnsafeAction)
	}
	if strings.TrimSpace(input.Evidence.Objective) == "" || strings.TrimSpace(input.Evidence.CandidateText) == "" {
		return stop(ReasonInsufficientEvidence)
	}
	return Result{Eligibility: EligibilityVerifier, Reason: ReasonVerifierEligible}
}

func safeTrajectory(input terminaldecision.Input) bool {
	return strings.TrimSpace(input.Evidence.Lineage.TrajectoryRef) != "" ||
		strings.TrimSpace(input.Continuation.TrajectoryRef) != ""
}

func safeActions(input terminaldecision.Input) bool {
	for i := 0; i < int(input.Evidence.ActionCount); i++ {
		action := input.Evidence.Actions[i]
		switch action.Kind {
		case aipapi.ItemKindToolCall, aipapi.ItemKindToolResult:
			if action.Status != aipapi.ItemStatusCompleted {
				return false
			}
		}
	}
	return true
}

func stop(reason Reason) Result {
	return Result{Eligibility: EligibilityStop, Reason: reason}
}
