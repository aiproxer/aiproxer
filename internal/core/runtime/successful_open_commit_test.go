package runtime

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

// This file pins the observable behavior of the steps that follow a
// successful backend Open on both payload paths: persist the open trace, then
// bind provider economic evidence. The refactor under test moves those two
// steps into attemptTx.commitSuccessfulOpen; these tests describe the
// behavior that must survive that move unchanged.

// weightedFirstSpyStore records every SetWeightedFirstConsumed call so a
// test can assert whether the weighted-first marker was consumed.
type weightedFirstSpyStore struct {
	b2bua.Store
	calls atomic.Int32
}

func (s *weightedFirstSpyStore) SetWeightedFirstConsumed(ctx context.Context, aLegID string, consumed bool) error {
	s.calls.Add(1)
	return s.Store.SetWeightedFirstConsumed(ctx, aLegID, consumed)
}

// TestSuccessfulOpenCommit_CanonicalNilStreamIsAFailedOpen pins the canonical
// half of the shared commit: an Open that succeeds but returns no stream
// opened nothing, so it is a failed open. It records a failed-open attempt trace, never
// reaches the evidence binder, and the acquisition itself reports the upstream
// fault instead of handing out a stream that only breaks at the first Recv.
func TestSuccessfulOpenCommit_CanonicalNilStreamIsAFailedOpen(t *testing.T) {
	t.Parallel()
	rig := newBindPanicRig(t)
	var sid domain.SessionID
	var opens atomic.Int32
	rig.ex.Backends = map[string]execbackend.Backend{
		"nilstream": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				if st, ok := execctx.SecureSessionTurnFromContext(ctx); ok {
					sid = st.SessionID
				}
				return nil, nil
			},
		},
	}
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-successful-open-canonical-nil"})
	stream, err := rig.ex.Execute(ctx, canonicalPanicCall("call-successful-open-canonical-nil", "nilstream:m"))
	require.Error(t, err, "an open that produced no stream is a failed open")
	require.Nil(t, stream, "a failed open hands out no stream")
	requireNilStreamOpenFailure(t, err)

	require.Equal(t, int32(1), opens.Load(), "the backend opens exactly once")
	evs, lerr := rig.memSS.ListAttemptEvidence(context.Background(), sid, domain.ReadOptions{Limit: 100})
	require.NoError(t, lerr)
	requireFailedOpenEvidence(t, evs)
}

// TestSuccessfulOpenCommit_WireNilStreamIsAFailedOpen pins the wire half of
// the shared commit: a wire Open that succeeds but returns no stream opened
// nothing, so it is a failed open. It records a failed-open attempt trace, never reaches
// the evidence binder, and the acquisition itself reports the upstream fault
// instead of handing out a stream that only breaks at the first Recv.
func TestSuccessfulOpenCommit_WireNilStreamIsAFailedOpen(t *testing.T) {
	t.Parallel()
	rig := newBindPanicRig(t)
	src := &countingBodySource{testSource: newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"nil stream"}]}`)}
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src.testSource, false, "b1:m1")
	acc.WireRequest.CandidateModel = "b1:m1"
	var sid domain.SessionID
	var opens atomic.Int32
	rig.ex.Backends = map[string]execbackend.Backend{
		"b1": {OpenWire: func(ctx context.Context, _ largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			opens.Add(1)
			if st, ok := execctx.SecureSessionTurnFromContext(ctx); ok {
				sid = st.SessionID
			}
			return nil, nil
		}},
	}
	res, err := rig.ex.ExecuteLargeBody(execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-successful-open-wire-nil"}), acc, src)
	require.Error(t, err, "an open that produced no stream is a failed open")
	require.Nil(t, res.Stream, "a failed open hands out no stream")
	requireNilStreamOpenFailure(t, err)

	require.Equal(t, int32(1), opens.Load(), "the backend opens exactly once")
	require.Equal(t, int32(1), src.closes.Load(), "the opened body closes exactly once")
	evs, lerr := rig.memSS.ListAttemptEvidence(context.Background(), sid, domain.ReadOptions{Limit: 100})
	require.NoError(t, lerr)
	requireFailedOpenEvidence(t, evs)
}

// TestSuccessfulOpenCommit_NilStreamFailsTheAcquisitionNotTheFirstRecv pins
// where a nil-stream Open surfaces. The missing stream is a failed open, so
// the acquisition itself fails on both payload paths: there is no stream to
// receive from, and no first Recv that can fail later.
//
// Both paths are pinned here because they used to defer the failure to the
// first Recv and then report it differently: the canonical retry loop
// re-opened the attempt until the route budget was spent, and the wire path
// reported a routing exhaustion that hid the real cause.
func TestSuccessfulOpenCommit_NilStreamFailsTheAcquisitionNotTheFirstRecv(t *testing.T) {
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
		ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-successful-open-canonical-nil-recv"})
		stream, err := rig.ex.Execute(ctx, canonicalPanicCall("call-successful-open-canonical-nil-recv", "nilstream:m"))
		require.Error(t, err, "the acquisition fails instead of handing out a broken stream")
		require.Nil(t, stream)
		requireNilStreamOpenFailure(t, err)
		require.NotErrorIs(t, err, aipapi.ErrMaxRouteAttempts, "the client does not see a route budget error")
		require.NotErrorIs(t, err, routing.ErrNoEligibleCandidate, "the client does not see a routing exhaustion")
		require.Equal(t, int32(1), opens.Load(), "the broken backend is not retried inside the same budget")
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
		res, err := rig.ex.ExecuteLargeBody(execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-successful-open-wire-nil-recv"}), acc, src)
		require.Error(t, err, "the acquisition fails instead of handing out a broken stream")
		require.Nil(t, res.Stream)
		requireNilStreamOpenFailure(t, err)
		require.NotErrorIs(t, err, aipapi.ErrMaxRouteAttempts, "the client does not see a route budget error")
		require.NotErrorIs(t, err, routing.ErrNoEligibleCandidate, "the client does not see a routing exhaustion")
		require.Equal(t, int32(1), opens.Load(), "the broken backend is opened exactly once")
	})
}

// TestSuccessfulOpenCommit_ErrorOpenRecordsFailedOpenTrace pins that a failed
// Open records a failed-open attempt trace on either payload path, with its
// failure outcome and pre-output classification: it never reads as a
// successful acquisition.
func TestSuccessfulOpenCommit_ErrorOpenRecordsFailedOpenTrace(t *testing.T) {
	t.Parallel()
	t.Run("canonical", func(t *testing.T) {
		t.Parallel()
		rig := newBindPanicRig(t)
		var sid domain.SessionID
		rig.ex.Backends = map[string]execbackend.Backend{
			"boom": {
				Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
				Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					if st, ok := execctx.SecureSessionTurnFromContext(ctx); ok {
						sid = st.SessionID
					}
					return nil, fmt.Errorf("open boom")
				},
			},
		}
		ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-successful-open-canonical-err"})
		_, err := rig.ex.Execute(ctx, canonicalPanicCall("call-successful-open-canonical-err", "boom:m"))
		require.Error(t, err, "a failed open must surface")

		evs, lerr := rig.memSS.ListAttemptEvidence(context.Background(), sid, domain.ReadOptions{Limit: 100})
		require.NoError(t, lerr)
		requireFailedOpenEvidence(t, evs)
	})
	t.Run("wire", func(t *testing.T) {
		t.Parallel()
		rig := newBindPanicRig(t)
		src := &countingBodySource{testSource: newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"open boom"}]}`)}
		acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src.testSource, false, "b1:m1")
		acc.WireRequest.CandidateModel = "b1:m1"
		var sid domain.SessionID
		rig.ex.Backends = map[string]execbackend.Backend{
			"b1": {OpenWire: func(ctx context.Context, _ largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				if st, ok := execctx.SecureSessionTurnFromContext(ctx); ok {
					sid = st.SessionID
				}
				return nil, fmt.Errorf("open boom")
			}},
		}
		_, err := rig.ex.ExecuteLargeBody(execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-successful-open-wire-err"}), acc, src)
		require.Error(t, err, "a failed open must surface")

		evs, lerr := rig.memSS.ListAttemptEvidence(context.Background(), sid, domain.ReadOptions{Limit: 100})
		require.NoError(t, lerr)
		requireFailedOpenEvidence(t, evs)
	})
}

// TestSuccessfulOpenCommit_CanonicalBinderPanicFailoverSkipsWeightedFirst
// pins the swallowed-failure seam on the canonical path: a binder panic on
// the first candidate is swallowed so the next candidate can run, the failed
// attempt still owns its cleanup, and the weighted-first marker of the failed
// candidate is never consumed.
func TestSuccessfulOpenCommit_CanonicalBinderPanicFailoverSkipsWeightedFirst(t *testing.T) {
	t.Parallel()
	rig := newBindPanicRig(t)
	spy := &weightedFirstSpyStore{Store: rig.ex.Store}
	rig.ex.Store = spy

	poisoned := &panicBinderStream{ManagedEventStream: makeStreamWithEvents(
		aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "must not surface"},
		aipapi.Event{Kind: aipapi.EventResponseFinished},
	)}
	var opensGood atomic.Int32
	var sid domain.SessionID
	rig.ex.Backends = map[string]execbackend.Backend{
		"badbind": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if st, ok := execctx.SecureSessionTurnFromContext(ctx); ok {
					sid = st.SessionID
				}
				return poisoned, nil
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

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-successful-open-canonical-panic"})
	// The [first] branch makes the first candidate MarkedFirst, so the
	// weighted-first marker is only consumed by a candidate that actually
	// completed its successful open.
	call := canonicalPanicCall("call-successful-open-canonical-panic", "[first]badbind:m^[weight=1]good:m")
	stream, err := rig.ex.Execute(ctx, call)
	require.NoError(t, err, "a swallowed binder panic must fail over")
	require.NotNil(t, stream)
	defer func() { _ = stream.Close() }()

	ev, rerr := stream.Recv(ctx)
	require.NoError(t, rerr)
	require.Equal(t, "good output", ev.Delta, "the failover candidate serves the output")

	require.Equal(t, int32(1), poisoned.closes.Load(), "the poisoned stream closes exactly once")
	require.Equal(t, int32(1), opensGood.Load(), "the second candidate opens after the swallowed panic")
	require.Equal(t, int32(0), spy.calls.Load(), "the failed attempt must not consume the weighted-first marker")
	requireBindFailureEvidence(t, rig, sid)
}

// TestSuccessfulOpenCommit_WireBinderPanicFailoverSkipsWeightedFirst pins the
// swallowed-failure seam on the wire path: a binder panic on the first
// candidate is swallowed so the next candidate can run, the failed attempt
// still owns its cleanup, and the weighted-first marker of the failed
// candidate is never consumed.
func TestSuccessfulOpenCommit_WireBinderPanicFailoverSkipsWeightedFirst(t *testing.T) {
	t.Parallel()
	rig := newBindPanicRig(t)
	spy := &weightedFirstSpyStore{Store: rig.ex.Store}
	rig.ex.Store = spy

	src := &countingBodySource{testSource: newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"binder panic"}]}`)}
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src.testSource, false, "badbind:m", "good:m")
	// The [first] branch makes the first candidate MarkedFirst, so the
	// weighted-first marker is only consumed by a candidate that actually
	// completed its successful open.
	acc.WireRequest.CandidateModel = "[first]badbind:m^[weight=1]good:m"

	poisoned := &panicBinderStream{ManagedEventStream: makeStreamWithEvents(
		aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "must not surface"},
		aipapi.Event{Kind: aipapi.EventResponseFinished},
	)}
	var opensGood atomic.Int32
	var sid domain.SessionID
	rig.ex.Backends = map[string]execbackend.Backend{
		"badbind": {OpenWire: func(ctx context.Context, _ largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			if st, ok := execctx.SecureSessionTurnFromContext(ctx); ok {
				sid = st.SessionID
			}
			return poisoned, nil
		}},
		"good": {OpenWire: func(context.Context, largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			opensGood.Add(1)
			return makeStreamWithEvents(
				aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "good output"},
				aipapi.Event{Kind: aipapi.EventResponseFinished},
			), nil
		}},
	}

	res, err := rig.ex.ExecuteLargeBody(execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-successful-open-wire-panic"}), acc, src)
	require.NoError(t, err, "a swallowed binder panic must fail over")
	require.NotNil(t, res.Stream)
	defer func() { _ = res.Stream.Close() }()

	require.Equal(t, int32(1), poisoned.closes.Load(), "the poisoned stream closes exactly once")
	require.Equal(t, int32(1), src.closes.Load(), "the opened body closes exactly once")
	require.Equal(t, int32(1), opensGood.Load(), "the second candidate opens after the swallowed panic")
	require.Equal(t, int32(0), spy.calls.Load(), "the failed attempt must not consume the weighted-first marker")
	requireBindFailureEvidence(t, rig, sid)
}

// requireBindFailureEvidence pins that a binder failure after a successful
// Open is not a failed open: the acquisition flag stays false and the outcome
// names the binding stage.
func requireBindFailureEvidence(t *testing.T, rig *bindPanicRig, sid domain.SessionID) {
	t.Helper()
	evs, err := rig.memSS.ListAttemptEvidence(context.Background(), sid, domain.ReadOptions{Limit: 100})
	require.NoError(t, err)
	require.NotEmpty(t, evs)
	require.False(t, evs[0].Trace.OpenFailed, "the backend acquired a stream")
	require.False(t, evs[0].Outcome.Success)
	require.Equal(t, "pre_output", evs[0].Outcome.ProviderStatus)
	require.Equal(t, "backend_evidence_binding_failed", evs[0].Outcome.ErrorCode)
}

func requireFailedOpenEvidence(t *testing.T, evs []domain.AttemptEvidence) {
	t.Helper()
	require.Len(t, evs, 1, "a failed open records one attempt")
	require.True(t, evs[0].Trace.OpenFailed, "the trace must not read as a successful acquisition")
	require.NotEmpty(t, evs[0].Trace.ResolvedBackend)
	require.False(t, evs[0].Outcome.Success)
	require.Equal(t, "pre_output", evs[0].Outcome.ProviderStatus)
	require.NotEmpty(t, evs[0].Outcome.DebugReason)
}

// TestSuccessfulOpenCommit_FailedOpenOutcomeDiagnostics pins the secure-session
// outcome of a failed Open on both payload paths: a recoverable failure keeps
// its distinctive reason, a backend-reported post-output phase is still
// pre-output for an open that failed, and an unclassified error gets a stable
// category.
func TestSuccessfulOpenCommit_FailedOpenOutcomeDiagnostics(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		err        error
		wantReason string
		wantCode   string
	}{
		{
			name: "recoverable reason survives",
			err: &aipapi.UpstreamFailureError{
				Phase: aipapi.PhasePreOutput, Recoverable: true, Reason: "provider rate limit exceeded",
			},
			wantReason: "provider rate limit exceeded",
		},
		{
			name:     "post-output phase is pre-output for a failed open",
			err:      &aipapi.UpstreamFailureError{Phase: aipapi.PhasePostOutput, CandidateKey: "b1:m1", Reason: "late"},
			wantCode: "b1:m1",
		},
		{
			name:       "nil stream gets its own category",
			err:        nilStreamOpenFailure("b1:m1"),
			wantReason: nilStreamOpenReason,
			wantCode:   "backend_open_nil_stream",
		},
		{name: "unclassified error gets a category", err: fmt.Errorf("connection refused"), wantCode: "backend_open_error"},
	}
	check := func(t *testing.T, evs []domain.AttemptEvidence, wantReason, wantCode string) {
		t.Helper()
		require.NotEmpty(t, evs)
		ev := evs[0]
		require.True(t, ev.Trace.OpenFailed)
		require.False(t, ev.Outcome.Success)
		require.Equal(t, "pre_output", ev.Outcome.ProviderStatus)
		if wantCode != "" {
			require.Equal(t, wantCode, ev.Outcome.ErrorCode)
		}
		require.Contains(t, ev.Outcome.DebugReason, wantReason)
	}
	for _, tc := range cases {
		t.Run("canonical/"+tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newBindPanicRig(t)
			var sid domain.SessionID
			rig.ex.Backends = map[string]execbackend.Backend{
				"b1": {
					Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
					Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
						if st, ok := execctx.SecureSessionTurnFromContext(ctx); ok {
							sid = st.SessionID
						}
						return nil, tc.err
					},
				},
			}
			ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-open-diag-canonical"})
			_, err := rig.ex.Execute(ctx, canonicalPanicCall("call-open-diag-canonical", "b1:m1"))
			require.Error(t, err)
			evs, lerr := rig.memSS.ListAttemptEvidence(context.Background(), sid, domain.ReadOptions{Limit: 100})
			require.NoError(t, lerr)
			check(t, evs, tc.wantReason, tc.wantCode)
		})
		t.Run("wire/"+tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newBindPanicRig(t)
			src := &countingBodySource{testSource: newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"open diag"}]}`)}
			acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src.testSource, false, "b1:m1")
			acc.WireRequest.CandidateModel = "b1:m1"
			var sid domain.SessionID
			rig.ex.Backends = map[string]execbackend.Backend{
				"b1": {OpenWire: func(ctx context.Context, _ largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
					if st, ok := execctx.SecureSessionTurnFromContext(ctx); ok {
						sid = st.SessionID
					}
					return nil, tc.err
				}},
			}
			_, err := rig.ex.ExecuteLargeBody(execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-open-diag-wire"}), acc, src)
			require.Error(t, err)
			evs, lerr := rig.memSS.ListAttemptEvidence(context.Background(), sid, domain.ReadOptions{Limit: 100})
			require.NoError(t, lerr)
			check(t, evs, tc.wantReason, tc.wantCode)
		})
	}
}
