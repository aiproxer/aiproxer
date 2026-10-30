package usageevidence

import (
	"errors"
)

var (
	ErrCallIncomplete    = errors.New("billing: call is not complete")
	ErrCallClaimConflict = errors.New("billing: complete-call claim conflict")
)

type CompleteCall struct {
	Closure CallUsageRecord
	Legs    []CallLegUsageRecord
}

func JoinCompleteCall(closure CallUsageRecord, legs []CallLegUsageRecord) (CompleteCall, error) {
	if err := validateSealedCallUsage(closure); err != nil {
		return CompleteCall{}, err
	}
	byBLeg := make(map[string]CallLegUsageRecord, len(legs))
	for _, leg := range legs {
		if err := validateSealedCallLegUsage(leg); err != nil {
			continue
		}
		if leg.CallID != closure.CallID {
			continue
		}
		if closure.SubmissionID != "" && leg.SubmissionID != closure.SubmissionID {
			continue
		}
		if !closure.Workload.IsZero() && leg.Workload != closure.Workload {
			continue
		}
		byBLeg[leg.BLegID] = leg
	}
	out := make([]CallLegUsageRecord, 0, len(closure.ExpectedBLegIDs))
	for _, id := range closure.ExpectedBLegIDs {
		leg, ok := byBLeg[id]
		if !ok {
			return CompleteCall{}, ErrCallIncomplete
		}
		out = append(out, leg)
	}
	return CompleteCall{Closure: closure, Legs: out}, nil
}
