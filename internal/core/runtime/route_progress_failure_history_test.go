package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// TestRouteProgressSharedAuthority asserts that the route plan state progress and
// the recovery controller in the stream are the exact same instance.
func TestRouteProgressSharedAuthority(t *testing.T) {
	ex := TestExecutor()
	// Build a route plan
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "backendA:model-x"},
		Session: aipapi.SessionRef{
			ALegID: "test-aleg",
		},
	}
	preSession := session.SessionView{
		ALegID: "test-aleg",
	}
	ibt, err := newIdentityBoundTurn(
		"trace-1",
		call,
		execview.PrincipalView{},
		scope.PrincipalScopeView{},
		false,
		aipworkspace.WorkspaceView{},
		b2bua.ALegRecord{ALegID: "test-aleg"},
		routeAuthoritySnapshot{},
		execctx.SecureSessionTurn{},
		false,
		preSession,
	)
	if err != nil {
		t.Fatal(err)
	}
	prep := &preparedRequest{
		identity: ibt,
		call:     ibt.call,
	}
	plan, err := ex.buildRoutePlan(context.Background(), prep)
	if err != nil {
		t.Fatalf("failed to build route plan: %v", err)
	}

	// Now check that progress is created
	if plan.progress == nil {
		t.Fatal("expected non-nil plan.progress")
	}

	// Create a recovery input for assemble mimicking executor_assemble_stream
	rc := newRecoveryController(recoveryControllerInput{
		budget:   plan.progress.budget,
		ttft:     plan.progress.ttft,
		sel:      plan.sel,
		session:  plan.progress.session,
		excluded: plan.progress.excluded,
		rng:      plan.rng,
	})

	if rc != plan.progress {
		t.Errorf("expected rc (%p) and plan.progress (%p) to be the exact same instance", rc, plan.progress)
	}
}

// TestErrorPrecedenceMatrix asserts the exact precedence order of failures.
func TestErrorPrecedenceMatrix(t *testing.T) {
	errTransport := &aipapi.TransportRejectError{Operation: "op", Mode: "mode"}
	errAdmission := errors.New("admission err")
	errCapability := &aipapi.RejectError{Missing: []aipapi.Capability{aipapi.CapabilityVision}}
	errSwallowedCapability := aipapi.RecoverablePreOutputError(aipapi.ErrCapabilityReject)
	errContextLimit := aipapi.ErrAllCandidatesContextLimitExceeded
	errTransform := aipapi.ErrAllCandidatesExcluded
	errParallel := errors.New("parallel err")
	errBase := errors.New("base err")

	cases := []struct {
		name      string
		configure func(*candidateFailureHistory)
		expected  error
	}{
		{
			name: "all errors present -> transport wins",
			configure: func(h *candidateFailureHistory) {
				h.TransportReject = aipapi.TransportNegotiationResult{
					Kind:      aipapi.NegotiationReject,
					Operation: "op",
					Mode:      "mode",
				}
				h.AdmissionErr = errAdmission
				h.CapabilityReject = aipapi.NegotiationResult{
					Kind:    aipapi.NegotiationReject,
					Missing: []aipapi.Capability{aipapi.CapabilityVision},
				}
				h.ContextLimit = true
				h.TransformExcludes.noteTransform("reason")
				h.ParallelFailure = errParallel
			},
			expected: errTransport,
		},
		{
			name: "admission present -> admission wins over capability",
			configure: func(h *candidateFailureHistory) {
				h.AdmissionErr = errAdmission
				h.CapabilityReject = aipapi.NegotiationResult{
					Kind:    aipapi.NegotiationReject,
					Missing: []aipapi.Capability{aipapi.CapabilityVision},
				}
				h.ContextLimit = true
				h.TransformExcludes.noteTransform("reason")
				h.ParallelFailure = errParallel
			},
			expected: errAdmission,
		},
		{
			name: "capability present -> capability wins over context limit",
			configure: func(h *candidateFailureHistory) {
				h.CapabilityReject = aipapi.NegotiationResult{
					Kind:    aipapi.NegotiationReject,
					Missing: []aipapi.Capability{aipapi.CapabilityVision},
				}
				h.ContextLimit = true
				h.TransformExcludes.noteTransform("reason")
				h.ParallelFailure = errParallel
			},
			expected: errCapability,
		},
		{
			name: "swallowed capability present -> wins over context limit",
			configure: func(h *candidateFailureHistory) {
				h.SwallowedCapabilityReject = errSwallowedCapability
				h.ContextLimit = true
				h.TransformExcludes.noteTransform("reason")
				h.ParallelFailure = errParallel
			},
			expected: errSwallowedCapability,
		},
		{
			name: "negotiation capability present -> wins over swallowed capability",
			configure: func(h *candidateFailureHistory) {
				h.CapabilityReject = aipapi.NegotiationResult{
					Kind:    aipapi.NegotiationReject,
					Missing: []aipapi.Capability{aipapi.CapabilityVision},
				}
				h.SwallowedCapabilityReject = errSwallowedCapability
				h.ContextLimit = true
			},
			expected: errCapability,
		},
		{
			name: "admission present -> admission wins over swallowed capability",
			configure: func(h *candidateFailureHistory) {
				h.AdmissionErr = errAdmission
				h.SwallowedCapabilityReject = errSwallowedCapability
				h.ContextLimit = true
			},
			expected: errAdmission,
		},
		{
			name: "context limit present -> context limit wins over transform",
			configure: func(h *candidateFailureHistory) {
				h.ContextLimit = true
				h.TransformExcludes.noteTransform("reason")
				h.ParallelFailure = errParallel
			},
			expected: errContextLimit,
		},
		{
			name: "transform present -> transform wins over parallel",
			configure: func(h *candidateFailureHistory) {
				h.TransformExcludes.noteTransform("reason")
				h.ParallelFailure = errParallel
			},
			expected: errTransform,
		},
		{
			name: "parallel present -> parallel wins over base",
			configure: func(h *candidateFailureHistory) {
				h.ParallelFailure = errParallel
			},
			expected: errParallel,
		},
		{
			name: "none present -> base wins",
			configure: func(h *candidateFailureHistory) {
			},
			expected: errBase,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}}
			tc.configure(h)
			got := h.FinalError(errBase)
			switch tc.expected {
			case errTransport:
				if _, ok := errors.AsType[*aipapi.TransportRejectError](got); !ok {
					t.Fatalf("expected TransportRejectError, got %v", got)
				}
			case errCapability:
				if _, ok := errors.AsType[*aipapi.RejectError](got); !ok {
					t.Fatalf("expected RejectError, got %v", got)
				}
			default:
				if !errors.Is(got, tc.expected) && got.Error() != tc.expected.Error() {
					t.Fatalf("got error %v, want %v", got, tc.expected)
				}
			}
		})
	}
}
