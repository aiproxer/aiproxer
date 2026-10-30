package runtime

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestFailureHistory_Precedence(t *testing.T) {
	errAdmission := errors.New("admission error")
	errParallel := errors.New("parallel error")
	errUpstream := errors.New("upstream failure")

	baseErr := errors.New("base error")

	tests := []struct {
		name     string
		history  candidateFailureHistory
		expected error
	}{
		{
			name: "transport priority over admission",
			history: candidateFailureHistory{
				TransportReject: aipapi.TransportNegotiationResult{
					Kind:      aipapi.NegotiationReject,
					Operation: "op",
					Mode:      aipapi.TransportModeStreaming,
				},
				AdmissionErr:     errAdmission,
				CapabilityReject: aipapi.NegotiationResult{Kind: aipapi.NegotiationReject, Missing: []aipapi.Capability{"streaming"}},
				ContextLimit:     true,
				ParallelFailure:  errParallel,
			},
			expected: aipapi.ErrTransportReject,
		},
		{
			name: "admission priority over capability",
			history: candidateFailureHistory{
				AdmissionErr:     errAdmission,
				CapabilityReject: aipapi.NegotiationResult{Kind: aipapi.NegotiationReject, Missing: []aipapi.Capability{"streaming"}},
				ContextLimit:     true,
				ParallelFailure:  errParallel,
			},
			expected: errAdmission,
		},
		{
			name: "capability priority over context limit",
			history: candidateFailureHistory{
				CapabilityReject: aipapi.NegotiationResult{Kind: aipapi.NegotiationReject, Missing: []aipapi.Capability{"streaming"}},
				ContextLimit:     true,
				ParallelFailure:  errParallel,
			},
			expected: &aipapi.RejectError{Missing: []aipapi.Capability{"streaming"}},
		},
		{
			name: "context limit priority over transform excludes",
			history: candidateFailureHistory{
				ContextLimit:    true,
				ParallelFailure: errParallel,
			},
			expected: aipapi.ErrAllCandidatesContextLimitExceeded,
		},
		{
			name: "transform excludes priority over parallel failure",
			history: candidateFailureHistory{
				TransformExcludes: func() *transformExcludeTracker {
					t := &transformExcludeTracker{}
					t.noteTransform("some_reason")
					return t
				}(),
				ParallelFailure: errParallel,
			},
			expected: aipapi.ErrAllCandidatesExcluded,
		},
		{
			name: "parallel failure fallback",
			history: candidateFailureHistory{
				ParallelFailure: errParallel,
			},
			expected: errParallel,
		},
		{
			name: "upstream failure priority over parallel failure",
			history: candidateFailureHistory{
				SwallowedUpstreamFailure: errUpstream,
				ParallelFailure:          errParallel,
			},
			expected: errUpstream,
		},
		{
			name: "capability rejection priority over an older upstream failure",
			history: candidateFailureHistory{
				SwallowedCapabilityReject: &aipapi.RejectError{Missing: []aipapi.Capability{"streaming"}},
				SwallowedUpstreamFailure:  errUpstream,
			},
			expected: &aipapi.RejectError{Missing: []aipapi.Capability{"streaming"}},
		},
		{
			name: "context limit priority over an older upstream failure",
			history: candidateFailureHistory{
				SwallowedUpstreamFailure: errUpstream,
				ContextLimit:             true,
			},
			expected: aipapi.ErrAllCandidatesContextLimitExceeded,
		},
		{
			name: "upstream failure fallback",
			history: candidateFailureHistory{
				SwallowedUpstreamFailure: errUpstream,
			},
			expected: errUpstream,
		},
		{
			name:     "base error fallback",
			history:  candidateFailureHistory{},
			expected: baseErr,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.history.FinalError(baseErr)
			if !errors.Is(got, tc.expected) && got.Error() != tc.expected.Error() {
				t.Errorf("FinalError() = %v, want %v", got, tc.expected)
			}
		})
	}
}
