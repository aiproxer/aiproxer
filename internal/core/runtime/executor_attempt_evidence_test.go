package runtime

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestIsAttemptFailureForEvidence_ExcludesCancellationAndSuccess is a focused
// unit test for the attempt-record boundary predicate. It proves cancellation
// and success outcomes are never classified as attempt failures for evidence,
// so the runtime never invokes the attempt evidence seam for them (requirement
// 6.4). Surfaced and swallowed failures with a non-nil DetailErr remain eligible.
func TestIsAttemptFailureForEvidence_ExcludesCancellationAndSuccess(t *testing.T) {
	t.Parallel()
	boom := errors.New("upstream boom")
	cand := routing.AttemptCandidate{}
	cand.Primary.Backend = "openai"

	cases := []struct {
		name    string
		outcome aipapi.AttemptOutcome
		detail  error
		want    bool
	}{
		{"cancelled with error detail", aipapi.AttemptCancelled, boom, false},
		{"cancelled without error detail", aipapi.AttemptCancelled, nil, false},
		{"success with error detail", aipapi.AttemptSuccess, boom, false},
		{"success without error detail", aipapi.AttemptSuccess, nil, false},
		{"surfaced failure with error detail", aipapi.AttemptSurfacedFailure, boom, true},
		{"surfaced failure without error detail", aipapi.AttemptSurfacedFailure, nil, false},
		{"swallowed failure with error detail", aipapi.AttemptSwallowedFailure, boom, true},
		{"swallowed failure without error detail", aipapi.AttemptSwallowedFailure, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := recordAttemptParams{Cand: cand, Outcome: tc.outcome, DetailErr: tc.detail}
			if got := isAttemptFailureForEvidence(p); got != tc.want {
				t.Fatalf("isAttemptFailureForEvidence(%s, detail=%v): got %v want %v", tc.outcome, tc.detail, got, tc.want)
			}
		})
	}
}
