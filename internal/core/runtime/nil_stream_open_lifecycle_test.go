package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/policy"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
)

// This file pins the lifecycle and economic rules around a backend Open that
// returned no usable stream: the raw acquisition facts survive the error
// classification, a canceled open is not a backend failure, a rejected stream
// never becomes cleanup ownership, and an executed candidate is never reported
// as a transform exclusion.

// typedNilLifecycleCalls counts every lifecycle and telemetry call a rejected
// typed-nil stream received. It must stay zero: the terminal owner must never
// call a stream it rejected.
var typedNilLifecycleCalls atomic.Int32

// panickingTypedNilStream is a managed stream implementation handed back as a
// typed nil pointer. Every lifecycle and cancellation-telemetry method panics,
// so a rejected stream that reached the terminal owner would panic there.
type panickingTypedNilStream struct{}

func (s *panickingTypedNilStream) Recv(context.Context) (aipapi.Event, error) {
	typedNilLifecycleCalls.Add(1)
	panic("typed nil stream received")
}

func (s *panickingTypedNilStream) Close() error {
	typedNilLifecycleCalls.Add(1)
	panic("typed nil stream closed")
}

func (s *panickingTypedNilStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	typedNilLifecycleCalls.Add(1)
	panic("typed nil stream canceled")
}

func (s *panickingTypedNilStream) CancellationOutcomeSeen() bool {
	typedNilLifecycleCalls.Add(1)
	panic("typed nil telemetry")
}

func (s *panickingTypedNilStream) CancellationForcedAbort() bool {
	typedNilLifecycleCalls.Add(1)
	panic("typed nil telemetry")
}

func (s *panickingTypedNilStream) CancellationHandshakeNegotiated() bool {
	typedNilLifecycleCalls.Add(1)
	panic("typed nil telemetry")
}

// TestNilStreamOpen_TTFTExpiredOpenIsNotAKnownZeroLeg pins the raw
// acquisition fact under a deadline. The provider may have processed the
// request before the TTFT budget expired, so the leg must not be recorded as
// never-started - an authoritative known-zero verdict on the customer and the
// provider side. The route must also report the backend fault, not the
// deadline.
func TestNilStreamOpen_TTFTExpiredOpenIsNotAKnownZeroLeg(t *testing.T) {
	t.Parallel()
	rig := newBindPanicRig(t)
	rig.ex.Now = ttftRemainingClock(100 * time.Millisecond)
	rig.ex.Backends = map[string]execbackend.Backend{
		"nilstream": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				<-ctx.Done()
				// The adapter returns nothing after the deadline.
				return nil, nil
			},
		},
	}
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-nil-stream-ttft"})
	_, err := rig.ex.Execute(ctx, canonicalPanicCall("call-nil-stream-ttft", "[ttft_timeout=1]nilstream:m"))
	require.Error(t, err)
	requireNilStreamOpenFailure(t, err)

	rig.sink.mu.Lock()
	legs := append([]usageevidence.CallLegUsageRecord(nil), rig.sink.legs...)
	rig.sink.mu.Unlock()
	require.Len(t, legs, 1, "the expired attempt records exactly one leg")
	require.Equal(t, usageevidence.LegOutcomeFailed, legs[0].Outcome,
		"an open the backend answered is not a known-zero leg, whatever ended it")

	// The provider side reads the same classification: only a never-started
	// leg is a known-zero verdict, so a failed leg without provider evidence
	// stays pending (TestEvaluateALegProviderPendingNoEvidence in the billing
	// domain, which moves to the Enterprise tree).
}

// TestNilStreamOpen_CanceledOpenDoesNotCountAgainstBackendHealth pins the
// cancellation attribution. A client that cancels its own request must not
// count as a backend failure: the recorded attempt outcome is a cancellation,
// and the backend's circuit breaker does not open.
func TestNilStreamOpen_CanceledOpenDoesNotCountAgainstBackendHealth(t *testing.T) {
	t.Parallel()
	rig := newBindPanicRig(t)
	breaker := policy.NewCircuitBreaker(policy.CircuitBreakerOptions{FailureThreshold: 1})
	rig.ex.CandidateHealth = breaker
	ctx, cancel := context.WithCancel(execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-nil-stream-canceled-health"}))
	rig.ex.Backends = map[string]execbackend.Backend{
		"nilstream": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				// The adapter's own context was canceled: it returns nothing
				// instead of the context error.
				cancel()
				return nil, nil
			},
		},
	}
	call := canonicalPanicCall("call-nil-stream-canceled-health", "nilstream:m")
	_, err := rig.ex.Execute(ctx, call)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)

	aLegID := call.Session.ALegID
	require.NotEmpty(t, aLegID, "expected an A-leg after execute")
	records, lerr := rig.ex.Store.LoadAttempts(context.Background(), aLegID)
	require.NoError(t, lerr)
	require.Len(t, records, 1)
	require.Equal(t, aipapi.AttemptCancelled, records[0].Outcome,
		"a canceled attempt is not a backend failure")
	require.Empty(t, breaker.UnhealthyCandidateKeys(),
		"a client cancellation must not open the backend's circuit breaker")
}

// TestNilStreamOpen_CanceledWireOpenDoesNotCountAgainstBackendHealth pins the
// same cancellation attribution on the wire path.
func TestNilStreamOpen_CanceledWireOpenDoesNotCountAgainstBackendHealth(t *testing.T) {
	t.Parallel()
	rig := newBindPanicRig(t)
	breaker := policy.NewCircuitBreaker(policy.CircuitBreakerOptions{FailureThreshold: 1})
	rig.ex.CandidateHealth = breaker
	ctx, cancel := context.WithCancel(execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-nil-stream-wire-canceled-health"}))
	src := &countingBodySource{testSource: newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"canceled"}]}`)}
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src.testSource, false, "b1:m1")
	acc.WireRequest.CandidateModel = "b1:m1"
	rig.ex.Backends = map[string]execbackend.Backend{
		"b1": {OpenWire: func(context.Context, largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			// The adapter's own context was canceled: it returns nothing
			// instead of the context error.
			cancel()
			return nil, nil
		}},
	}
	_, err := rig.ex.ExecuteLargeBody(ctx, acc, src)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, breaker.UnhealthyCandidateKeys(),
		"a client cancellation must not open the backend's circuit breaker")
}

// TestNilStreamOpen_ParentDeadlineDoesNotCountAgainstBackendHealth pins the
// cancellation provenance for a request deadline that no TTFT budget owns. The
// backend waited for the request's own deadline to expire and returned nothing;
// the request ended the attempt, so the backend is not at fault, and the
// potentially dispatched leg keeps its economic liability.
func TestNilStreamOpen_ParentDeadlineDoesNotCountAgainstBackendHealth(t *testing.T) {
	t.Parallel()
	rig := newBindPanicRig(t)
	breaker := policy.NewCircuitBreaker(policy.CircuitBreakerOptions{FailureThreshold: 1})
	rig.ex.CandidateHealth = breaker
	ctx, cancel := context.WithTimeout(execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-nil-stream-deadline"}), 100*time.Millisecond)
	defer cancel()
	var sid domain.SessionID
	rig.ex.Backends = map[string]execbackend.Backend{
		"nilstream": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if st, ok := execctx.SecureSessionTurnFromContext(ctx); ok {
					sid = st.SessionID
				}
				<-ctx.Done()
				// The adapter returns nothing after the request deadline.
				return nil, nil
			},
		},
	}
	call := canonicalPanicCall("call-nil-stream-deadline", "nilstream:m")
	stream, err := rig.ex.Execute(ctx, call)
	require.Error(t, err)
	require.Nil(t, stream)
	require.ErrorIs(t, err, context.DeadlineExceeded, "the request deadline is preserved")

	aLegID := call.Session.ALegID
	require.NotEmpty(t, aLegID, "expected an A-leg after execute")
	records, lerr := rig.ex.Store.LoadAttempts(context.Background(), aLegID)
	require.NoError(t, lerr)
	require.Len(t, records, 1)
	require.Equal(t, aipapi.AttemptCancelled, records[0].Outcome,
		"a request deadline is a caller cancellation, not a backend failure")
	require.Empty(t, breaker.UnhealthyCandidateKeys(),
		"a request deadline must not open the backend's circuit breaker")

	rig.sink.mu.Lock()
	legs := append([]usageevidence.CallLegUsageRecord(nil), rig.sink.legs...)
	rig.sink.mu.Unlock()
	require.Len(t, legs, 1)
	require.Equal(t, usageevidence.LegOutcomeFailed, legs[0].Outcome,
		"the potentially dispatched leg keeps its economic liability")
	evs, serr := rig.memSS.ListAttemptEvidence(context.Background(), sid, domain.ReadOptions{Limit: 100})
	require.NoError(t, serr)
	require.Len(t, evs, 1, "a stream-less open records a failed-open trace")
	require.True(t, evs[0].Trace.OpenFailed, "it never reads as a successful acquisition")
}

// TestNilStreamOpen_BackendCancellationStaysABackendFailure pins the inverse
// provenance case. The caller did not cancel anything, so an adapter that
// returns its own cancellation stays a backend failure and counts against the
// backend's health, on both payload paths.
func TestNilStreamOpen_BackendCancellationStaysABackendFailure(t *testing.T) {
	t.Parallel()
	t.Run("canonical", func(t *testing.T) {
		t.Parallel()
		rig := newBindPanicRig(t)
		breaker := policy.NewCircuitBreaker(policy.CircuitBreakerOptions{FailureThreshold: 1})
		rig.ex.CandidateHealth = breaker
		rig.ex.Backends = map[string]execbackend.Backend{
			"nilstream": {
				Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
				Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					return nil, context.Canceled
				},
			},
		}
		call := canonicalPanicCall("call-nil-stream-backend-canceled", "nilstream:m")
		ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-nil-stream-backend-canceled"})
		_, err := rig.ex.Execute(ctx, call)
		require.Error(t, err)

		records, lerr := rig.ex.Store.LoadAttempts(context.Background(), call.Session.ALegID)
		require.NoError(t, lerr)
		require.Len(t, records, 1)
		require.Equal(t, aipapi.AttemptSurfacedFailure, records[0].Outcome,
			"an adapter cancellation the caller did not cause stays a backend failure")
		require.NotEmpty(t, breaker.UnhealthyCandidateKeys(),
			"a backend-originated cancellation counts against the backend's health")
	})
	t.Run("wire", func(t *testing.T) {
		t.Parallel()
		rig := newBindPanicRig(t)
		breaker := policy.NewCircuitBreaker(policy.CircuitBreakerOptions{FailureThreshold: 1})
		rig.ex.CandidateHealth = breaker
		src := &countingBodySource{testSource: newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"backend canceled"}]}`)}
		acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src.testSource, false, "b1:m1")
		acc.WireRequest.CandidateModel = "b1:m1"
		rig.ex.Backends = map[string]execbackend.Backend{
			"b1": {OpenWire: func(context.Context, largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				return nil, context.Canceled
			}},
		}
		_, err := rig.ex.ExecuteLargeBody(execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-nil-stream-wire-backend-canceled"}), acc, src)
		require.Error(t, err)
		require.NotEmpty(t, breaker.UnhealthyCandidateKeys(),
			"a backend-originated cancellation counts against the backend's health on the wire path too")
	})
}

// TestNilStreamOpen_RejectedTypedNilStreamNeverBecomesOwnership pins that a
// rejected stream is normalized to a genuinely nil interface before the attempt
// owns it. The terminal owner would otherwise call the lifecycle and the
// cancellation telemetry of a typed-nil stream.
func TestNilStreamOpen_RejectedTypedNilStreamNeverBecomesOwnership(t *testing.T) {
	typedNilLifecycleCalls.Store(0)
	rig := newBindPanicRig(t)
	rig.ex.Backends = map[string]execbackend.Backend{
		"typednil": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				var stream *panickingTypedNilStream
				return stream, nil
			},
		},
	}
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-nil-stream-typed-nil-ownership"})
	_, err := rig.ex.Execute(ctx, canonicalPanicCall("call-nil-stream-typed-nil-ownership", "typednil:m"))
	require.Error(t, err)
	requireNilStreamOpenFailure(t, err)
	require.Zero(t, typedNilLifecycleCalls.Load(),
		"the rejected stream must not receive a lifecycle or telemetry call")

	rig.sink.mu.Lock()
	legs := append([]usageevidence.CallLegUsageRecord(nil), rig.sink.legs...)
	rig.sink.mu.Unlock()
	require.Len(t, legs, 1, "the failed attempt terminalizes exactly once")
	require.Equal(t, usageevidence.LegOutcomeFailed, legs[0].Outcome)
}

// excludeBackendTransform excludes one backend from the attempt plane, the way
// an unrepresentable request transform excludes a candidate.
type excludeBackendTransform struct {
	backendID string
}

func (t *excludeBackendTransform) ID() string { return "exclude-backend-transform" }

func (t *excludeBackendTransform) Order() int { return 0 }

func (t *excludeBackendTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (t *excludeBackendTransform) HandleAttempt(_ context.Context, _ *aipapi.Call, meta request.AttemptMeta, _ request.Services) (request.AttemptDecision, error) {
	if meta.BackendID == t.backendID {
		return request.AttemptDecision{Kind: request.AttemptExcludeCandidate, ReasonCode: "candidate_excluded"}, nil
	}
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

// TestNilStreamOpen_ExecutedCandidateIsNotReportedAsExcluded pins the mixed
// verdict. A route where one candidate was contacted and opened no stream and
// another was excluded by an attempt transform must not report that every
// candidate was excluded: one candidate was executed.
func TestNilStreamOpen_ExecutedCandidateIsNotReportedAsExcluded(t *testing.T) {
	t.Parallel()
	rig := newBindPanicRig(t)
	cs := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, "exclude-backend",
		[]request.AttemptTransform{&excludeBackendTransform{backendID: "excluded"}}))
	bundle := aipfeature.BundleFromPlanes(cs.Freeze(), nil)
	require.NoError(t, bundle.Validate())
	rig.ex.Bus = hooks.New(hooks.Config{})
	rig.ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(rig.ex.Bus, extensions.SnapshotOptions{
		FeaturePlanes: bundle.PlaneSet,
	})
	rig.ex.Backends = map[string]execbackend.Backend{
		"nilstream": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, nil
			},
		},
		"excluded": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventResponseFinished}), nil
			},
		},
	}
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-nil-stream-mixed"})
	_, err := rig.ex.Execute(ctx, canonicalPanicCall("call-nil-stream-mixed", "nilstream:m|excluded:m"))
	require.Error(t, err)
	requireNilStreamOpenFailure(t, err)
	require.NotErrorIs(t, err, aipapi.ErrAllCandidatesExcluded,
		"an executed candidate is not a transform exclusion")
}

// TestResolveTerminalOpenError_UpstreamFaultOnlyReplacesBudgetExhaustion pins
// that the budget-error substitution only applies when the route's final
// verdict IS the recorded upstream fault. A more specific recorded verdict
// outranks that fault in candidateFailureHistory.FinalError, so the
// pre-existing attempt-budget error survives and no unrelated route failure
// changes behavior.
func TestResolveTerminalOpenError_UpstreamFaultOnlyReplacesBudgetExhaustion(t *testing.T) {
	t.Parallel()
	budgetErr := fmt.Errorf("executor: %w", aipapi.ErrMaxRouteAttempts)

	t.Run("no recorded verdict", func(t *testing.T) {
		t.Parallel()
		history := &candidateFailureHistory{}
		require.Equal(t, budgetErr, resolveTerminalOpenError(history, budgetErr))
	})
	t.Run("capability rejection outranks a recorded upstream fault", func(t *testing.T) {
		t.Parallel()
		// Both verdicts are present, as they are when one candidate opened no
		// stream and another was refused for a missing capability: the
		// capability verdict is the route's final reason, so the upstream fault
		// must not replace the budget error.
		history := &candidateFailureHistory{
			SwallowedCapabilityReject: &aipapi.RejectError{Missing: []aipapi.Capability{"streaming"}},
			SwallowedUpstreamFailure:  errors.New("backend open returned no stream"),
		}
		require.Equal(t, budgetErr, resolveTerminalOpenError(history, budgetErr))
	})
	t.Run("upstream fault", func(t *testing.T) {
		t.Parallel()
		upstream := errors.New("backend open returned no stream")
		history := &candidateFailureHistory{SwallowedUpstreamFailure: upstream}
		require.Equal(t, upstream, resolveTerminalOpenError(history, budgetErr))
	})
	t.Run("a different error is untouched", func(t *testing.T) {
		t.Parallel()
		history := &candidateFailureHistory{SwallowedUpstreamFailure: errors.New("upstream")}
		other := errors.New("some other failure")
		require.Equal(t, other, resolveTerminalOpenError(history, other))
	})
}
