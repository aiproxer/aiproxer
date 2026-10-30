package causepolicy

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

func TestEvaluateCanonicalCausePolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		input           terminaldecision.Input
		wantEligibility Eligibility
		wantReason      Reason
	}{
		{
			name:            "authoritative refusal always stops",
			input:           causePolicyInput(terminaldecision.CandidateCauseRefusal, true),
			wantEligibility: EligibilityStop,
			wantReason:      ReasonAuthoritative,
		},
		{
			name:            "authoritative content filter always stops",
			input:           causePolicyInput(terminaldecision.CandidateCauseContentFilter, true),
			wantEligibility: EligibilityStop,
			wantReason:      ReasonAuthoritative,
		},
		{
			name:            "authoritative cancellation always stops",
			input:           causePolicyInput(terminaldecision.CandidateCauseCancellation, true),
			wantEligibility: EligibilityStop,
			wantReason:      ReasonAuthoritative,
		},
		{
			name:            "authoritative denial always stops",
			input:           causePolicyInput(terminaldecision.CandidateCauseAuthorityDenied, true),
			wantEligibility: EligibilityStop,
			wantReason:      ReasonAuthoritative,
		},
		{
			name:            "pre-output transport delegates to core recovery",
			input:           causePolicyInput(terminaldecision.CandidateCauseTransport, false),
			wantEligibility: EligibilityStop,
			wantReason:      ReasonPreOutputTransport,
		},
		{
			name:            "post-output transport requires a safe trajectory",
			input:           withoutTrajectory(causePolicyInput(terminaldecision.CandidateCauseTransport, true)),
			wantEligibility: EligibilityStop,
			wantReason:      ReasonMissingTrajectory,
		},
		{
			name:            "post-output limit can be verifier eligible",
			input:           causePolicyInput(terminaldecision.CandidateCauseLimit, true),
			wantEligibility: EligibilityVerifier,
			wantReason:      ReasonVerifierEligible,
		},
		{
			name:            "committed normal completion remains verifier eligible",
			input:           causePolicyInput(terminaldecision.CandidateCauseNormal, true),
			wantEligibility: EligibilityVerifier,
			wantReason:      ReasonVerifierEligible,
		},
		{
			name: "completed tool result remains eligible",
			input: withAction(causePolicyInput(terminaldecision.CandidateCauseProviderError, true), terminaldecision.ActionFact{
				CallID: "tool-1", Kind: aipapi.ItemKindToolResult, Status: aipapi.ItemStatusCompleted, Name: "lookup",
			}),
			wantEligibility: EligibilityVerifier,
			wantReason:      ReasonVerifierEligible,
		},
		{
			name: "partial tool state always stops",
			input: withAction(causePolicyInput(terminaldecision.CandidateCauseProviderError, true), terminaldecision.ActionFact{
				CallID: "tool-1", Kind: aipapi.ItemKindToolCall, Status: aipapi.ItemStatusIncomplete, Name: "lookup",
			}),
			wantEligibility: EligibilityStop,
			wantReason:      ReasonUnsafeAction,
		},
		{
			name:            "unknown cause stops conservatively",
			input:           causePolicyInput(terminaldecision.CandidateCause("future"), true),
			wantEligibility: EligibilityStop,
			wantReason:      ReasonInvalidInput,
		},
		{
			name:            "missing evidence stops",
			input:           withoutEvidence(causePolicyInput(terminaldecision.CandidateCauseProviderError, true)),
			wantEligibility: EligibilityStop,
			wantReason:      ReasonInsufficientEvidence,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := Evaluate(test.input)
			if got.Eligibility != test.wantEligibility || got.Reason != test.wantReason {
				t.Fatalf("Evaluate() = %+v, want eligibility %q reason %q", got, test.wantEligibility, test.wantReason)
			}
		})
	}
}

func causePolicyInput(cause terminaldecision.CandidateCause, committed bool) terminaldecision.Input {
	return terminaldecision.Input{
		Candidate: terminaldecision.CanonicalTerminalCandidate{
			Cause:           cause,
			Reference:       "candidate-1",
			OutputCommitted: committed,
		},
		Request: terminaldecision.RequestIdentity{
			RequestID: "request-1",
			TraceID:   "trace-1",
			ALegID:    "a-leg-1",
			BLegID:    "b-leg-1",
		},
		Policy: terminaldecision.PolicySnapshot{
			Revision:                "policy-1",
			MaxContinuationAttempts: 3,
		},
		Continuation: terminaldecision.ContinuationEvidence{
			TrajectoryRef: "trajectory-1",
			Attempt:       1,
		},
		Evidence: terminaldecision.Evidence{
			Objective:     "finish the existing objective",
			CandidateText: "unfinished result",
			Lineage: terminaldecision.EvidenceLineage{
				TrajectoryRef: "trajectory-1",
				ProgressRef:   "progress-1",
			},
		},
		Deadline: time.Unix(1, 0).UTC(),
	}
}

func withoutTrajectory(input terminaldecision.Input) terminaldecision.Input {
	input.Continuation.TrajectoryRef = ""
	input.Evidence.Lineage.TrajectoryRef = ""
	return input
}

func withoutEvidence(input terminaldecision.Input) terminaldecision.Input {
	input.Evidence.Objective = ""
	input.Evidence.CandidateText = ""
	return input
}

func withAction(input terminaldecision.Input, action terminaldecision.ActionFact) terminaldecision.Input {
	input.Evidence.Actions[0] = action
	input.Evidence.ActionCount = 1
	return input
}
