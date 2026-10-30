package runtime

import (
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// mapBackendPanic maps a recovered backend panic to an orchestration error that participates in
// existing failover and post-output surfacing rules. committed must reflect whether client-visible
// output was already committed for the active attempt.
func mapBackendPanic(pe *safety.PanicError, committed bool, candidateKey string) error {
	if pe == nil {
		return nil
	}
	reason := diag.TruncErrDetail(pe, attemptReasonMaxRunes)
	if committed {
		return &aipapi.UpstreamFailureError{
			Phase:        aipapi.PhasePostOutput,
			Recoverable:  false,
			Reason:       reason,
			CandidateKey: candidateKey,
		}
	}
	return aipapi.RecoverablePreOutputError(pe)
}

// mapStreamPanic maps a recovered stream-side panic (recv path, completion gates) using the same
// commit gating as the backend mapper; candidate key is omitted for stream-local failures.
func mapStreamPanic(pe *safety.PanicError, committed bool) error {
	return mapBackendPanic(pe, committed, "")
}
