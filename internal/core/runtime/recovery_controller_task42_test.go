package runtime

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

var (
	errTask42Admission = errors.New("task42: admission failed")
	errTask42Parallel  = errors.New("task42: parallel arms failed")
)

func TestTask42RecoveryReplacementErrorPrecedence(t *testing.T) {
	t.Parallel()

	const selector = "backend:model"
	transportWant := &aipapi.TransportRejectError{
		Operation: aipapi.OperationOpenAIChatCompletions,
		Mode:      aipapi.TransportModeStreaming,
	}
	capabilityWant := &aipapi.RejectError{Missing: []aipapi.Capability{aipapi.CapabilityVision}}

	cases := []struct {
		name      string
		configure func(*recoveryController)
		check     func(*testing.T, error)
	}{
		{
			name: "transport rejection wins",
			configure: func(r *recoveryController) {
				failures := r.getFailures()
				failures.TransportReject = aipapi.TransportNegotiationResult{
					Kind:      aipapi.NegotiationReject,
					Operation: transportWant.Operation,
					Mode:      transportWant.Mode,
				}
				failures.AdmissionErr = errTask42Admission
				failures.CapabilityReject = aipapi.NegotiationResult{Kind: aipapi.NegotiationReject, Missing: capabilityWant.Missing}
				failures.ContextLimit = true
				failures.TransformExcludes.noteTransform("not-representable")
				failures.ParallelFailure = errTask42Parallel
			},
			check: func(t *testing.T, err error) {
				t.Helper()
				var got *aipapi.TransportRejectError
				if !errors.As(err, &got) || !reflect.DeepEqual(got, transportWant) {
					t.Fatalf("error = %v, want transport reject %+v", err, transportWant)
				}
				if !errors.Is(err, aipapi.ErrTransportReject) {
					t.Fatalf("error = %v, want ErrTransportReject", err)
				}
			},
		},
		{
			name: "admission failure follows transport",
			configure: func(r *recoveryController) {
				failures := r.getFailures()
				failures.AdmissionErr = errTask42Admission
				failures.CapabilityReject = aipapi.NegotiationResult{Kind: aipapi.NegotiationReject, Missing: capabilityWant.Missing}
				failures.ContextLimit = true
				failures.TransformExcludes.noteTransform("not-representable")
				failures.ParallelFailure = errTask42Parallel
			},
			check: func(t *testing.T, err error) {
				t.Helper()
				if err != errTask42Admission {
					t.Fatalf("error = %v, want independent admission error %v", err, errTask42Admission)
				}
			},
		},
		{
			name: "capability rejection follows admission",
			configure: func(r *recoveryController) {
				failures := r.getFailures()
				failures.CapabilityReject = aipapi.NegotiationResult{Kind: aipapi.NegotiationReject, Missing: capabilityWant.Missing}
				failures.ContextLimit = true
				failures.TransformExcludes.noteTransform("not-representable")
				failures.ParallelFailure = errTask42Parallel
			},
			check: func(t *testing.T, err error) {
				t.Helper()
				var got *aipapi.RejectError
				if !errors.As(err, &got) || !reflect.DeepEqual(got, capabilityWant) {
					t.Fatalf("error = %v, want capability reject %+v", err, capabilityWant)
				}
				if !errors.Is(err, aipapi.ErrCapabilityReject) {
					t.Fatalf("error = %v, want ErrCapabilityReject", err)
				}
			},
		},
		{
			name: "context limit exhaustion follows capability",
			configure: func(r *recoveryController) {
				failures := r.getFailures()
				failures.ContextLimit = true
				failures.TransformExcludes.noteTransform("not-representable")
				failures.ParallelFailure = errTask42Parallel
			},
			check: func(t *testing.T, err error) {
				t.Helper()
				if err != aipapi.ErrAllCandidatesContextLimitExceeded {
					t.Fatalf("error = %v, want context-limit sentinel", err)
				}
			},
		},
		{
			name: "transform exclusion follows context limit",
			configure: func(r *recoveryController) {
				failures := r.getFailures()
				failures.TransformExcludes.noteTransform("not-representable")
				failures.ParallelFailure = errTask42Parallel
			},
			check: func(t *testing.T, err error) {
				t.Helper()
				if err != aipapi.ErrAllCandidatesExcluded {
					t.Fatalf("error = %v, want aggregate transform-exclusion sentinel", err)
				}
			},
		},
		{
			name: "parallel failure follows transform exclusion",
			configure: func(r *recoveryController) {
				failures := r.getFailures()
				failures.ParallelFailure = errTask42Parallel
			},
			check: func(t *testing.T, err error) {
				t.Helper()
				if err != errTask42Parallel {
					t.Fatalf("error = %v, want independent parallel failure %v", err, errTask42Parallel)
				}
			},
		},
		{
			name: "routing no-eligible error is final fallback",
			check: func(t *testing.T, err error) {
				t.Helper()
				if !errors.Is(err, routing.ErrNoEligibleCandidate) {
					t.Fatalf("error = %v, want wrapped routing.ErrNoEligibleCandidate", err)
				}
				want := "executor: expand failover: routing: no eligible route candidate"
				if err.Error() != want {
					t.Fatalf("error = %q, want exact wrapper %q", err, want)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sel, err := routing.Parse(selector)
			if err != nil {
				t.Fatalf("parse selector: %v", err)
			}
			ex := TestExecutor()
			var opens atomic.Int32
			ex.Backends = map[string]execbackend.Backend{
				"backend": {
					Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
						opens.Add(1)
						return nil, errors.New("task42: backend opened unexpectedly")
					},
				},
			}
			r := &recoveryController{
				e:        ex,
				opener:   newReplacementOpener(ex, nil, nil),
				sel:      sel,
				session:  &routing.SessionRoutingState{},
				excluded: map[string]struct{}{selector: {}},
				rng:      routing.NewSeededRng(42),
			}
			if tc.configure != nil {
				tc.configure(r)
			}
			_, gotErr := r.openReplacement(context.Background(), recvTurnFacts{}.terminalFacts(), nil, false)
			if gotErr == nil {
				t.Fatal("openReplacement error = nil, want no-eligible precedence result")
			}
			tc.check(t, gotErr)
			if got := opens.Load(); got != 0 {
				t.Fatalf("backend opens = %d, want 0 after replacement exhaustion", got)
			}
		})
	}
}

func TestTask42RecoveryBoundaryPassesRetiredPriorBeforeOpen(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	r := &recoveryController{opener: func(_ context.Context, req replacementOpenRequest) (replacementOpenResult, error) {
		if !req.prior.retired {
			t.Fatal("replacement opener received an unretired prior attempt")
		}
		opens.Add(1)
		return replacementOpenResult{opened: true}, nil
	}}
	prior := &attemptSession{authority: authorityLifecycle{
		control: &authorityLifecycleControl{terminal: authorityTerminalReleased},
	}}
	if _, err := r.openReplacement(context.Background(), recvTurnFacts{}.terminalFacts(), prior, false); err != nil {
		t.Fatalf("openReplacement: %v", err)
	}
	if got := opens.Load(); got != 1 {
		t.Fatalf("replacement opener calls = %d, want 1", got)
	}
}

func TestTask42RecoveryBoundaryNeverOpensCommittedOrUnretired(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		terminal *turnTerminal
		prior    *attemptSession
		wantErr  error
	}{
		{
			name:     "committed turn",
			terminal: func() *turnTerminal { terminal := newTurnTerminal(); terminal.markCommitted(nil); return terminal }(),
			wantErr:  errRecoveryTurnCommitted,
		},
		{
			name: "unretired prior attempt",
			prior: &attemptSession{authority: newAuthorityLifecycle(nil, nil, attemptAuthorityState{
				admissionInput:  testAuthorityAdmissionInput(1),
				admissionResult: usagelimit.AdmissionResult{Allowed: true, Reserved: true},
			}, authorityCandidate())},
			wantErr: errRecoveryPriorAttemptNotRetired,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var opens atomic.Int32
			r := &recoveryController{opener: func(context.Context, replacementOpenRequest) (replacementOpenResult, error) {
				opens.Add(1)
				return replacementOpenResult{opened: true}, nil
			}}
			_, err := r.openReplacement(context.Background(), recvTurnFacts{}.terminalFacts(), tc.prior, tc.terminal != nil && tc.terminal.committed())
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("openReplacement error = %v, want %v", err, tc.wantErr)
			}
			if got := opens.Load(); got != 0 {
				t.Fatalf("opener calls = %d, want 0 for %s", got, tc.name)
			}
		})
	}
}

func TestTask42MandatoryRecorderCommittedNoReplacementOpen(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	ex.SecureSessionRecordingMandatory = true
	var opens atomic.Int32
	recovery := &recoveryController{opener: func(context.Context, replacementOpenRequest) (replacementOpenResult, error) {
		opens.Add(1)
		return replacementOpenResult{opened: true}, nil
	}}
	s := &retryRecvStream{
		terminal:         newTurnTerminal(),
		recovery:         recovery,
		responsePipeline: &responsePipeline{recordingOutcome: responseRecordingMandatoryPostCommitFailure},
		attempt:          testAttemptSlot(b2bua.BLegRecord{}, routing.AttemptCandidate{Key: "committed-candidate"}, authorityLifecycle{}),
		facts: testRecvTurnFacts(recvTurnFacts{
			traceID: "task42-mandatory",
			aLegID:  "task42-a-leg",
		}),
	}
	bindTestRuntimeOwners(s, ex)
	s.terminal.markCommitted(s.attempt.snapshot())
	request := s.facts.terminalFacts()
	request.replacementBlocked = true
	_, err := s.recovery.tryReplacementIteration(context.Background(), request, s.attempt.require(), s.terminal.committed())
	if err == nil {
		t.Fatal("tryReplacementIteration error = nil, want committed mandatory-recorder failure")
	}
	var got *aipapi.UpstreamFailureError
	if !errors.As(err, &got) || got.Phase != aipapi.PhasePostOutput || got.Recoverable {
		t.Fatalf("error = %v, want non-recoverable post-output UpstreamFailureError", err)
	}
	if got.Reason != "secure session mandatory recorder failure after committed output" {
		t.Fatalf("error reason = %q, want mandatory recorder reason", got.Reason)
	}
	if got := opens.Load(); got != 0 {
		t.Fatalf("replacement opener calls = %d, want 0 after committed mandatory-recorder failure", got)
	}
}
