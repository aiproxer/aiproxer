package runtime

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

// This file pins the behavior of a backend Open that succeeds without
// producing a usable stream. Such an open opened nothing, so it is a failed
// open: the attempt is never recorded as a successful open, the route fails over
// to the next candidate instead of retrying the broken one, and an exhausted
// route reports the upstream fault instead of a routing exhaustion.

// requireNilStreamOpenFailure asserts that err is the upstream failure for a
// backend Open that returned no stream: a pre-output upstream failure that
// names the cause, so the client is told what actually happened upstream
// instead of a routing exhaustion.
func requireNilStreamOpenFailure(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	uf, ok := errors.AsType[*aipapi.UpstreamFailureError](err)
	require.True(t, ok, "the failure is an upstream failure: %v", err)
	require.Equal(t, nilStreamOpenReason, uf.Reason, "the failure names the backend that opened no stream")
	require.Equal(t, aipapi.PhasePreOutput, uf.Phase)
	require.True(t, aipapi.IsRecoverablePreOutput(err), "the failure is a recoverable pre-output upstream failure")
	require.True(t, IsNilStreamOpenError(err), "the failure matches the frontend wire predicate")
}

// typedNilStream is a managed stream implementation handed back as a typed nil
// pointer: the interface value is not nil, but the stream behind it is.
type typedNilStream struct{}

func (s *typedNilStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (s *typedNilStream) Close() error { return nil }

func (s *typedNilStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

// TestNilStreamOpen_CanonicalFailsOverToNextCandidate pins the canonical
// failover: a backend that opens no stream is failed over like any other open
// failure, so the next candidate serves the turn and the broken backend is
// opened once instead of once per remaining attempt.
func TestNilStreamOpen_CanonicalFailsOverToNextCandidate(t *testing.T) {
	t.Parallel()
	rig := newBindPanicRig(t)
	var sid domain.SessionID
	var nilBLegID string
	var opensNil, opensGood atomic.Int32
	rig.ex.Backends = map[string]execbackend.Backend{
		"nilstream": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opensNil.Add(1)
				if st, ok := execctx.SecureSessionTurnFromContext(ctx); ok {
					sid = st.SessionID
				}
				if lineage, ok := promptcache.ObservationLineageFromContext(ctx); ok {
					nilBLegID = lineage.BLegID
				}
				return nil, nil
			},
		},
		"good": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opensGood.Add(1)
				return makeStreamWithEvents(
					aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "good output"},
					aipapi.Event{Kind: aipapi.EventResponseFinished},
				), nil
			},
		},
	}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-nil-stream-canonical-failover"})
	stream, err := rig.ex.Execute(ctx, canonicalPanicCall("call-nil-stream-canonical-failover", "nilstream:m|good:m"))
	require.NoError(t, err, "a backend that opened no stream must fail over to the next candidate")
	require.NotNil(t, stream)
	defer func() { _ = stream.Close() }()

	ev, rerr := stream.Recv(ctx)
	require.NoError(t, rerr)
	require.Equal(t, "good output", ev.Delta, "the failover candidate serves the output")

	require.Equal(t, int32(1), opensNil.Load(), "the broken backend is opened once")
	require.Equal(t, int32(1), opensGood.Load(), "the next candidate is opened once")

	evs, lerr := rig.memSS.ListAttemptEvidence(context.Background(), sid, domain.ReadOptions{Limit: 100})
	require.NoError(t, lerr)
	require.Len(t, evs, 2, "the failed open and the opened candidate each record an attempt")
	require.NotEmpty(t, nilBLegID, "the failed B-leg is observable")
	require.Equal(t, nilBLegID, evs[0].Trace.BLegID, "the failed B-leg is in the audit trail")
	require.True(t, evs[0].Trace.OpenFailed, "a backend that opened no stream is a failed open")
	require.Equal(t, "pre_output", evs[0].Outcome.ProviderStatus)
	require.NotEqual(t, nilBLegID, evs[1].Trace.BLegID)
	require.False(t, evs[1].Trace.OpenFailed, "the opened candidate acquired a stream")
}

// TestNilStreamOpen_WireFailsOverToNextCandidate pins the wire failover: a
// backend that opens no stream is failed over like any other open failure, so
// the next candidate serves the turn and the body opened for the failed
// attempt is released with it.
func TestNilStreamOpen_WireFailsOverToNextCandidate(t *testing.T) {
	t.Parallel()
	rig := newBindPanicRig(t)
	src := &countingBodySource{testSource: newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"nil stream"}]}`)}
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src.testSource, false, "nilstream:m", "good:m")
	acc.WireRequest.CandidateModel = "nilstream:m|good:m"
	var sid domain.SessionID
	var nilBLegID string
	var opensNil, opensGood atomic.Int32
	rig.ex.Backends = map[string]execbackend.Backend{
		"nilstream": {OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			opensNil.Add(1)
			if st, ok := execctx.SecureSessionTurnFromContext(ctx); ok {
				sid = st.SessionID
			}
			nilBLegID = req.BLegID
			return nil, nil
		}},
		"good": {OpenWire: func(context.Context, largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			opensGood.Add(1)
			return makeStreamWithEvents(
				aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "good output"},
				aipapi.Event{Kind: aipapi.EventResponseFinished},
			), nil
		}},
	}

	res, err := rig.ex.ExecuteLargeBody(execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-nil-stream-wire-failover"}), acc, src)
	require.NoError(t, err, "a backend that opened no stream must fail over to the next candidate")
	require.NotNil(t, res.Stream)

	ev, rerr := res.Stream.Recv(context.Background())
	require.NoError(t, rerr)
	require.Equal(t, "good output", ev.Delta, "the failover candidate serves the output")

	require.Equal(t, int32(1), opensNil.Load(), "the broken backend is opened once")
	require.Equal(t, int32(1), opensGood.Load(), "the next candidate is opened once")
	require.Equal(t, int32(1), src.closes.Load(), "the failed attempt releases its own opened body")
	require.NoError(t, res.Stream.Close())
	require.Equal(t, int32(2), src.closes.Load(), "each attempt closes its own opened body exactly once")

	evs, lerr := rig.memSS.ListAttemptEvidence(context.Background(), sid, domain.ReadOptions{Limit: 100})
	require.NoError(t, lerr)
	require.Len(t, evs, 2, "the failed open and the opened candidate each record an attempt")
	require.NotEmpty(t, nilBLegID, "the failed B-leg is observable")
	require.Equal(t, nilBLegID, evs[0].Trace.BLegID, "the failed B-leg is in the audit trail")
	require.True(t, evs[0].Trace.OpenFailed, "a backend that opened no stream is a failed open")
	require.Equal(t, "pre_output", evs[0].Outcome.ProviderStatus)
	require.NotEqual(t, nilBLegID, evs[1].Trace.BLegID)
	require.False(t, evs[1].Trace.OpenFailed, "the opened candidate acquired a stream")
}

// TestNilStreamOpen_ExhaustedRouteReportsTheUpstreamFault pins the terminal
// error of a route whose candidates all opened no stream. The client sees the
// upstream fault, not a routing exhaustion: the routing verdict only says
// that no candidate was left, while the fault names what actually happened.
func TestNilStreamOpen_ExhaustedRouteReportsTheUpstreamFault(t *testing.T) {
	t.Parallel()
	t.Run("canonical", func(t *testing.T) {
		t.Parallel()
		rig := newBindPanicRig(t)
		var opens atomic.Int32
		rig.ex.Backends = map[string]execbackend.Backend{
			"nilstream": {
				Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
				Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					opens.Add(1)
					return nil, nil
				},
			},
		}
		ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-nil-stream-canonical-fault"})
		_, err := rig.ex.Execute(ctx, canonicalPanicCall("call-nil-stream-canonical-fault", "nilstream:m"))
		requireNilStreamOpenFailure(t, err)
		require.NotErrorIs(t, err, routing.ErrNoEligibleCandidate, "the routing exhaustion does not hide the upstream fault")
		require.NotErrorIs(t, err, aipapi.ErrMaxRouteAttempts, "the route budget is not reported as the cause")
		require.Equal(t, int32(1), opens.Load(), "the broken backend is opened once")
	})
	t.Run("wire", func(t *testing.T) {
		t.Parallel()
		rig := newBindPanicRig(t)
		src := &countingBodySource{testSource: newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"nil stream"}]}`)}
		acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src.testSource, false, "b1:m1")
		acc.WireRequest.CandidateModel = "b1:m1"
		var opens atomic.Int32
		rig.ex.Backends = map[string]execbackend.Backend{
			"b1": {OpenWire: func(context.Context, largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return nil, nil
			}},
		}
		_, err := rig.ex.ExecuteLargeBody(execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-nil-stream-wire-fault"}), acc, src)
		requireNilStreamOpenFailure(t, err)
		require.NotErrorIs(t, err, routing.ErrNoEligibleCandidate, "the routing exhaustion does not hide the upstream fault")
		require.NotErrorIs(t, err, aipapi.ErrMaxRouteAttempts, "the route budget is not reported as the cause")
		require.Equal(t, int32(1), opens.Load(), "the broken backend is opened once")
	})
}

// TestNilStreamOpen_BudgetExhaustionReportsTheUpstreamFault pins the same
// terminal error when the route has more candidates than the attempt budget
// allows. The budget runs out while the route is failing, and the upstream
// fault is what the client has to see: "max attempts exhausted" alone would
// hide the backend that opened no stream.
func TestNilStreamOpen_BudgetExhaustionReportsTheUpstreamFault(t *testing.T) {
	t.Parallel()
	rig := newBindPanicRig(t)
	require.Equal(t, 3, rig.ex.effectiveMaxAttempts(), "the default route budget is three attempts")
	var opens atomic.Int32
	nilStream := execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			opens.Add(1)
			return nil, nil
		},
	}
	rig.ex.Backends = map[string]execbackend.Backend{
		"nilstream":  nilStream,
		"nilstream2": nilStream,
		"nilstream3": nilStream,
		"nilstream4": nilStream,
	}
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-nil-stream-canonical-budget"})
	_, err := rig.ex.Execute(ctx, canonicalPanicCall("call-nil-stream-canonical-budget", "nilstream:m|nilstream2:m|nilstream3:m|nilstream4:m"))
	requireNilStreamOpenFailure(t, err)
	require.NotErrorIs(t, err, aipapi.ErrMaxRouteAttempts, "the route budget is not reported as the cause")
	require.Equal(t, int32(3), opens.Load(), "the budget is spent once per candidate, not once per retry")
}

// TestNilStreamOpen_ParallelBudgetExhaustionReportsTheUpstreamFault pins the
// terminal budget-error resolution across candidate shapes. A sequential
// candidate that opened no stream consumes the only attempt slot, and the
// parallel group behind it cannot acquire one: the route ends with the budget
// error, and the client must still see the upstream fault that caused it.
func TestNilStreamOpen_ParallelBudgetExhaustionReportsTheUpstreamFault(t *testing.T) {
	t.Parallel()
	rig := newBindPanicRig(t)
	rig.ex.MaxAttempts = 1
	var opensNil, opensRace atomic.Int32
	nilStream := execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			opensNil.Add(1)
			return nil, nil
		},
	}
	rig.ex.Backends = map[string]execbackend.Backend{
		"nilstream": nilStream,
		"raceA": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opensRace.Add(1)
				return makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventResponseFinished}), nil
			},
		},
		"raceB": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opensRace.Add(1)
				return makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventResponseFinished}), nil
			},
		},
	}
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-nil-stream-parallel-budget"})
	_, err := rig.ex.Execute(ctx, canonicalPanicCall("call-nil-stream-parallel-budget", "nilstream:m|raceA:m!raceB:m"))
	requireNilStreamOpenFailure(t, err)
	require.NotErrorIs(t, err, aipapi.ErrMaxRouteAttempts, "the route budget is not reported as the cause")
	require.Equal(t, int32(1), opensNil.Load(), "the sequential candidate is opened once")
	require.Equal(t, int32(0), opensRace.Load(), "the parallel group cannot acquire a budget slot")
}

// TestNilStreamOpen_ParallelArmFailureKeepsItsTypedIdentity pins that the
// typed upstream failure survives the parallel worker/reducer boundary. Each
// race arm keeps its own failure history, and the reducer merges it into the
// shared one: the terminal error must still name the backend and the phase,
// not only repeat a joined message.
func TestNilStreamOpen_ParallelArmFailureKeepsItsTypedIdentity(t *testing.T) {
	t.Parallel()
	rig := newBindPanicRig(t)
	var opens atomic.Int32
	nilStream := execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			opens.Add(1)
			return nil, nil
		},
	}
	rig.ex.Backends = map[string]execbackend.Backend{
		"nilA": nilStream,
		"nilB": nilStream,
	}
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-nil-stream-parallel-identity"})
	_, err := rig.ex.Execute(ctx, canonicalPanicCall("call-nil-stream-parallel-identity", "nilA:m!nilB:m"))
	requireNilStreamOpenFailure(t, err)
	uf, ok := errors.AsType[*aipapi.UpstreamFailureError](err)
	require.True(t, ok)
	require.Contains(t, []string{"nilA:m", "nilB:m"}, uf.CandidateKey, "the terminal error names the arm that opened no stream")
	require.Equal(t, int32(2), opens.Load(), "each race arm is opened once")
}

// TestNilStreamOpen_LegIsFailedNotNeverStarted pins the billing consequence.
// An open the backend answered is not a known-zero leg: the provider may have
// processed the request even though no stream came back, so the leg is
// recorded as failed (which keeps the provider charge open for reconciliation)
// instead of never-started (an authoritative "not billable" verdict).
func TestNilStreamOpen_LegIsFailedNotNeverStarted(t *testing.T) {
	t.Parallel()
	rig := newBindPanicRig(t)
	rig.ex.Backends = map[string]execbackend.Backend{
		"nilstream": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, nil
			},
		},
	}
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-nil-stream-leg"})
	_, err := rig.ex.Execute(ctx, canonicalPanicCall("call-nil-stream-leg", "nilstream:m"))
	require.Error(t, err)

	rig.sink.mu.Lock()
	legs := append([]usageevidence.CallLegUsageRecord(nil), rig.sink.legs...)
	rig.sink.mu.Unlock()
	require.Len(t, legs, 1, "the failed attempt records exactly one leg")
	require.Equal(t, usageevidence.LegOutcomeFailed, legs[0].Outcome,
		"an open the backend answered is not a known-zero leg")
}

// TestNilStreamOpen_FailedAttemptStaysInTheLineageStore pins that the failed
// attempt is not lost. The secure-session attempt list only carries attempts
// whose backend opened a stream, so a failed open - a nil-stream open like any
// other - is diagnosable through the B-leg lineage row, which keeps the
// backend, the outcome and the reason.
func TestNilStreamOpen_FailedAttemptStaysInTheLineageStore(t *testing.T) {
	t.Parallel()
	rig := newBindPanicRig(t)
	rig.ex.Backends = map[string]execbackend.Backend{
		"nilstream": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, nil
			},
		},
	}
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-nil-stream-lineage"})
	call := canonicalPanicCall("call-nil-stream-lineage", "nilstream:m")
	_, err := rig.ex.Execute(ctx, call)
	require.Error(t, err)

	aLegID := strings.TrimSpace(call.Session.ALegID)
	require.NotEmpty(t, aLegID, "expected an A-leg after execute")
	records, lerr := rig.ex.Store.LoadAttempts(context.Background(), aLegID)
	require.NoError(t, lerr)
	require.Len(t, records, 1, "the failed attempt keeps its lineage row")
	require.Equal(t, aipapi.AttemptSwallowedFailure, records[0].Outcome)
	require.Equal(t, nilStreamOpenReason, records[0].Reason, "the lineage row names the failure")
	require.Equal(t, "nilstream", records[0].BackendID)
}

// TestNilStreamOpen_TypedNilStreamIsAFailedOpen pins the acquisition check
// against a typed-nil stream: an interface holding a nil pointer is not a
// usable stream either, and it must not reach the successful-open commit.
func TestNilStreamOpen_TypedNilStreamIsAFailedOpen(t *testing.T) {
	t.Parallel()
	t.Run("canonical", func(t *testing.T) {
		t.Parallel()
		rig := newBindPanicRig(t)
		var opens atomic.Int32
		rig.ex.Backends = map[string]execbackend.Backend{
			"typednil": {
				Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
				Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					opens.Add(1)
					var stream *typedNilStream
					return stream, nil
				},
			},
		}
		ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-nil-stream-typed-nil"})
		_, err := rig.ex.Execute(ctx, canonicalPanicCall("call-nil-stream-typed-nil", "typednil:m"))
		requireNilStreamOpenFailure(t, err)
		require.Equal(t, int32(1), opens.Load(), "the backend is opened once")
	})
	t.Run("wire", func(t *testing.T) {
		t.Parallel()
		rig := newBindPanicRig(t)
		src := &countingBodySource{testSource: newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"typed nil"}]}`)}
		acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src.testSource, false, "b1:m1")
		acc.WireRequest.CandidateModel = "b1:m1"
		var opens atomic.Int32
		rig.ex.Backends = map[string]execbackend.Backend{
			"b1": {OpenWire: func(context.Context, largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				var stream *typedNilStream
				return stream, nil
			}},
		}
		_, err := rig.ex.ExecuteLargeBody(execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-nil-stream-typed-nil-wire"}), acc, src)
		requireNilStreamOpenFailure(t, err)
		require.Equal(t, int32(1), opens.Load(), "the backend is opened once")
	})
}

// TestNilStreamOpen_CanceledOpenIsNotABackendFault pins the cancellation
// precedence: an adapter that returns no stream because its context was
// canceled did not open a broken backend, so the attempt is recorded as a
// cancellation - not as the swallowed nil-stream backend fault - and the
// client sees the withdrawal.
func TestNilStreamOpen_CanceledOpenIsNotABackendFault(t *testing.T) {
	t.Parallel()
	rig := newBindPanicRig(t)
	ctx, cancel := context.WithCancel(execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-nil-stream-canceled"}))
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
	call := canonicalPanicCall("call-nil-stream-canceled", "nilstream:m")
	_, err := rig.ex.Execute(ctx, call)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled, "the cancellation owns the attempt")
	require.NotErrorIs(t, err, aipapi.ErrRecoverablePreOutput, "a canceled open is not a swallowed upstream failure")
	_, ok := errors.AsType[*aipapi.UpstreamFailureError](err)
	require.False(t, ok, "a canceled open is not reported as an upstream backend fault")

	records, lerr := rig.ex.Store.LoadAttempts(context.Background(), strings.TrimSpace(call.Session.ALegID))
	require.NoError(t, lerr)
	require.Len(t, records, 1, "the canceled attempt keeps its lineage row")
	require.Equal(t, aipapi.AttemptCancelled, records[0].Outcome, "a canceled open is a cancellation, not a backend fault")
	require.NotEqual(t, nilStreamOpenReason, records[0].Reason, "a canceled open never records the nil-stream backend fault")
}

// TestNilStreamOpen_CanceledParallelLegIsNotABackendFault pins the parallel
// cancellation precedence: a race leg whose adapter returns no stream because
// the race decided against it is a cancellation, not a backend fault, so it
// neither fails the turn nor penalizes the backend.
func TestNilStreamOpen_CanceledParallelLegIsNotABackendFault(t *testing.T) {
	t.Parallel()
	rig := newBindPanicRig(t)
	var opensLoser atomic.Int32
	loserWaiting := make(chan struct{})
	rig.ex.Backends = map[string]execbackend.Backend{
		"winner": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				// The other arm must be waiting before this one answers, so
				// the race decides against an arm that is mid-open.
				<-loserWaiting
				return makeStreamWithEvents(
					aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "winner output"},
					aipapi.Event{Kind: aipapi.EventResponseFinished},
				), nil
			},
		},
		"loser": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opensLoser.Add(1)
				close(loserWaiting)
				// The race decides while this arm waits; the adapter then
				// returns nothing instead of the context error.
				<-ctx.Done()
				return nil, nil
			},
		},
	}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-nil-stream-parallel-canceled"})
	call := canonicalPanicCall("call-nil-stream-parallel-canceled", "winner:m!loser:m")
	stream, err := rig.ex.Execute(ctx, call)
	require.NoError(t, err, "a canceled race leg must not fail the turn")
	require.NotNil(t, stream)
	defer func() { _ = stream.Close() }()

	ev, rerr := stream.Recv(ctx)
	require.NoError(t, rerr)
	require.Equal(t, "winner output", ev.Delta, "the winning arm serves the output")
	require.Equal(t, int32(1), opensLoser.Load(), "the losing arm is opened once")

	// The winner's first frame is not a persistence barrier: the race hands the
	// winner its stream before the canceled leg's rollback record is written.
	// Join the bridge's own cleanup-completion signal before reading the store.
	retryStream, ok := stream.(*retryRecvStream)
	require.True(t, ok, "a parallel race publishes a retry stream")
	attempt := retryStream.attempt.snapshot()
	require.NotNil(t, attempt, "a parallel race publishes its attempt session")
	bridge, ok := attempt.loadInner().(*parallelBridgeStream)
	require.True(t, ok, "a parallel race installs its bridge stream")
	require.NotNil(t, bridge.losersDone, "a race with a losing arm reports loser cleanup")
	select {
	case err := <-bridge.losersDone:
		require.NoError(t, err, "the losing arm's rollback completes cleanly")
	case <-time.After(cancelLosersTimeout):
		t.Fatal("timed out waiting for the losing arm's cancellation record")
	}

	records, lerr := rig.ex.Store.LoadAttempts(context.Background(), strings.TrimSpace(call.Session.ALegID))
	require.NoError(t, lerr)
	loserOutcome, loserReason := aipapi.AttemptOutcome(""), ""
	for _, record := range records {
		if record.BackendID == "loser" {
			loserOutcome, loserReason = record.Outcome, record.Reason
		}
	}
	require.Equal(t, aipapi.AttemptCancelled, loserOutcome, "a canceled race leg is a cancellation, not a backend fault")
	require.NotEqual(t, nilStreamOpenReason, loserReason, "a canceled race leg never records the nil-stream backend fault")
}

// TestNilStreamOpen_ParallelLegReportsNoStream pins the parallel path. A race// leg whose backend opens no stream is a failed open, so the round never
// reaches a receive on a stream that cannot arrive, and it reports the honest
// cause for the leg instead of racing a stream that does not exist.
func TestNilStreamOpen_ParallelLegReportsNoStream(t *testing.T) {
	t.Parallel()
	rig := newBindPanicRig(t)
	var opens atomic.Int32
	nilStream := execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			opens.Add(1)
			return nil, nil
		},
	}
	rig.ex.Backends = map[string]execbackend.Backend{
		"nilstream":  nilStream,
		"nilstream2": nilStream,
	}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-nil-stream-parallel"})
	stream, err := rig.ex.Execute(ctx, canonicalPanicCall("call-nil-stream-parallel", "nilstream:m!nilstream2:m"))
	require.Error(t, err, "a race whose legs opened no stream cannot produce a turn")
	require.Nil(t, stream)
	requireNilStreamOpenFailure(t, err)
	require.Equal(t, int32(2), opens.Load(), "each race leg is opened once")
}
