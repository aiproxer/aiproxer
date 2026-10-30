package runtime

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

// panicBinderStream opens successfully then panics in BindEconomicEvidence.
// onBind is an optional hook observing the bind identity before the panic.
type panicBinderStream struct {
	aipapi.ManagedEventStream
	closes  atomic.Int32
	openCtx atomic.Value
	onBind  func(coremetering.ObservationIdentity)
}

func (s *panicBinderStream) BindEconomicEvidence(id coremetering.ObservationIdentity) {
	if s.onBind != nil {
		s.onBind(id)
	}
	panic("binder boom")
}

func (s *panicBinderStream) Close() error {
	s.closes.Add(1)
	return s.ManagedEventStream.Close()
}

// countingBodySource counts fresh wire body opens/closes per acquisition.
type countingBodySource struct {
	*testSource
	closes atomic.Int32
}

func (s *countingBodySource) Open() (io.ReadCloser, error) {
	rc, err := s.testSource.Open()
	if err != nil {
		return nil, err
	}
	return &countingCloser{ReadCloser: rc, n: &s.closes}, nil
}

type countingCloser struct {
	io.ReadCloser
	n *atomic.Int32
}

func (r *countingCloser) Close() error {
	r.n.Add(1)
	return r.ReadCloser.Close()
}

// bindPanicRig serves actual requests through existing counting fixtures.
// memSS is the real secure-session store behind ex.SecureSession, kept for
// persisted trace/outcome assertions instead of mock counters.
type bindPanicRig struct {
	ex    *Executor
	prov  *wireParityRecordingProvider
	sink  *capturingTerminalUsageSink
	memSS *memory.Store
}

func newBindPanicRig(t *testing.T) *bindPanicRig {
	t.Helper()
	ex, _, _ := setupTestExecutor(t)
	prov := &wireParityRecordingProvider{id: "coord-bind-panic"}
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{Slots: []authoritycoord.RequestSlot{{
		ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: prov, Strength: authority.StrengthRequired,
	}}}
	ex.BillingExposureAdmission = stubExposureAdmission{exposure: billingport.CallExposure{AccountID: "acct-bind-panic"}}
	sink := &capturingTerminalUsageSink{}
	ex.TerminalUsageSink = sink
	memSS := memory.New(memory.Options{SimulateDurable: true})
	ex.SecureSession = testSecureManager(t, memSS, ex.Store)
	return &bindPanicRig{ex: ex, prov: prov, sink: sink, memSS: memSS}
}

// canonicalPanicCall is the shared minimal canonical request for binder and
// StoreID panic tests.
func canonicalPanicCall(id, selector string) *aipapi.Call {
	return &aipapi.Call{
		ID:    id,
		Route: aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("panic probe")},
		}},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeStreaming,
		},
	}
}

// TestWireBindingPanic_ExhaustedRollsBackAndAborts is the P1 regression:
// binder panic with no spare surfaces; stream/body dispose once, launch
// drains, leg Failed once, exposure aborts once, authority releases once.
// The opening trace persists before the binder in the real store, so the
// swallowed outcome lands on a real trace.
func TestWireBindingPanic_ExhaustedRollsBackAndAborts(t *testing.T) {
	t.Parallel()
	rig := newBindPanicRig(t)
	src := &countingBodySource{testSource: newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"binder panic"}]}`)}
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src.testSource, false, "b1:m1")
	acc.WireRequest.CandidateModel = "b1:m1"
	var sid domain.SessionID
	var sawBefore atomic.Bool
	poisoned := &panicBinderStream{ManagedEventStream: makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventResponseFinished})}
	poisoned.onBind = func(id coremetering.ObservationIdentity) {
		evs, _ := rig.memSS.ListAttemptEvidence(context.Background(), sid, domain.ReadOptions{Limit: 100})
		for _, ev := range evs {
			if id.BLegID != "" && ev.Trace.BLegID == id.BLegID {
				sawBefore.Store(true)
			}
		}
	}
	rig.ex.Backends = map[string]execbackend.Backend{
		"b1": {OpenWire: func(ctx context.Context, _ largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			poisoned.openCtx.Store(ctx)
			if st, ok := execctx.SecureSessionTurnFromContext(ctx); ok {
				sid = st.SessionID
			}
			return poisoned, nil
		}},
	}
	_, err := rig.ex.ExecuteLargeBody(execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-bind-panic"}), acc, src)
	require.Error(t, err, "binder panic with no spare must surface, not escape")
	require.Equal(t, int32(1), poisoned.closes.Load(), "poisoned stream must close once via rollback")
	stashed, ok := poisoned.openCtx.Load().(context.Context)
	require.True(t, ok && stashed.Err() != nil, "poisoned launch context must drain")
	require.Equal(t, int32(1), src.closes.Load(), "the opened body must close once")
	admit, settle, release := rig.prov.Counts()
	require.Equal(t, [3]int{1, 0, 1}, [3]int{admit, settle, release}, "authority acquired once, released once")
	rig.sink.mu.Lock()
	legs := append([]usageevidence.CallLegUsageRecord(nil), rig.sink.legs...)
	calls := append([]usageevidence.CallUsageRecord(nil), rig.sink.calls...)
	rig.sink.mu.Unlock()
	require.Len(t, legs, 1, "poisoned attempt terminalizes exactly once")
	require.Equal(t, usageevidence.LegOutcomeFailed, legs[0].Outcome, "b1 must be Failed after actual open")
	require.Len(t, calls, 1, "admitted exposure aborts exactly once")
	require.Equal(t, usageevidence.TurnOutcomeFailed, calls[0].Outcome)
	require.True(t, sawBefore.Load(), "opening trace must precede provider binder in real store")
	evs, err := rig.memSS.ListAttemptEvidence(context.Background(), sid, domain.ReadOptions{Limit: 100})
	require.NoError(t, err)
	require.Len(t, evs, 1, "exactly one persisted trace")
	require.Equal(t, evs[0].Trace.BLegID, evs[0].Outcome.BLegID, "outcome lands on the trace")
	require.NotEmpty(t, evs[0].Trace.BLegID)
	require.False(t, evs[0].Outcome.Success)
}

// TestCanonicalBindingPanic_FailoverClosesStream proves the shared P1
// seam: containment, disposal, launch drain, failover. The poisoned attempt
// persists its trace before the binder; the store holds both attempts.
func TestCanonicalBindingPanic_FailoverClosesStream(t *testing.T) {
	t.Parallel()
	rig := newBindPanicRig(t)
	var sid domain.SessionID
	var sawBefore atomic.Bool
	poisoned := &panicBinderStream{ManagedEventStream: makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "must not surface"}, aipapi.Event{Kind: aipapi.EventResponseFinished})}
	poisoned.onBind = func(id coremetering.ObservationIdentity) {
		evs, _ := rig.memSS.ListAttemptEvidence(context.Background(), sid, domain.ReadOptions{Limit: 100})
		for _, ev := range evs {
			if id.BLegID != "" && ev.Trace.BLegID == id.BLegID {
				sawBefore.Store(true)
			}
		}
	}
	rig.ex.Backends = map[string]execbackend.Backend{
		"badbind": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				poisoned.openCtx.Store(ctx)
				if st, ok := execctx.SecureSessionTurnFromContext(ctx); ok {
					sid = st.SessionID
				}
				return poisoned, nil
			},
		},
		"good": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "good output"}, aipapi.Event{Kind: aipapi.EventResponseFinished}), nil
			},
		},
	}
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-bind-panic"})
	stream, err := rig.ex.Execute(ctx, canonicalPanicCall("call-bind-panic-canonical", "badbind:m|good:m"))
	require.NoError(t, err, "binder panic must fail over to good")
	first, err := stream.Recv(ctx)
	require.NoError(t, err)
	require.Equal(t, "good output", first.Delta)
	require.NoError(t, stream.Close())

	require.Equal(t, int32(1), poisoned.closes.Load(), "poisoned canonical stream must close once")
	stashed, ok := poisoned.openCtx.Load().(context.Context)
	require.True(t, ok && stashed.Err() != nil, "poisoned canonical launch must drain")
	require.True(t, sawBefore.Load(), "poisoned opening must precede its binder in real store")
	evs, err := rig.memSS.ListAttemptEvidence(context.Background(), sid, domain.ReadOptions{Limit: 100})
	require.NoError(t, err)
	require.Len(t, evs, 2, "poisoned attempt plus failover persist one trace each")
	require.NotEqual(t, evs[0].Trace.BLegID, evs[1].Trace.BLegID)
	for _, ev := range evs {
		require.NotEmpty(t, ev.Trace.BLegID)
		require.Equal(t, ev.Trace.BLegID, ev.Outcome.BLegID, "each trace owns its outcome")
	}
}

// TestWireStoreIDResolverPanic_FailsClosedBeforeExposureAdmission proves
// P2: zero admissions, zero aborts, no fabricated identity.
func TestWireStoreIDResolverPanic_FailsClosedBeforeExposureAdmission(t *testing.T) {
	t.Parallel()
	rig := newBindPanicRig(t)
	rig.ex.BillingIdentity.StoreID = func(context.Context) string { panic("store identity boom") }
	var admits atomic.Int32
	rig.ex.BillingExposureAdmission = &testExposureAdmission{
		admitFunc: func(_ context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
			admits.Add(1)
			return billingport.CallExposure{AccountID: in.AccountID}, nil
		},
	}
	// No backend is configured: store resolution panics before admission,
	// so no backend may run. If ordering ever regressed, the counting
	// admission below still fails the test.
	src := newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"store panic"}]}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)
	_, err := rig.ex.ExecuteLargeBody(execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-store-panic"}), acc, src)
	require.ErrorIs(t, err, ErrBillingAdmissionDenied, "want fail-closed error on StoreID panic")
	require.Equal(t, int32(0), admits.Load(), "want zero exposure admissions")
	rig.sink.mu.Lock()
	require.Equal(t, 0, len(rig.sink.calls), "want zero aborts when nothing was admitted")
	rig.sink.mu.Unlock()
}

// Every fallible identity resolver must finish before exposure admission.
func TestBillingIdentityCallbackPanic_PrecedesExposureAdmission(t *testing.T) {
	t.Parallel()
	panicRef := func(context.Context, aipapi.Call) usageevidence.VersionRef { panic("identity callback failed") }
	panicWireRef := func(context.Context) usageevidence.VersionRef { panic("wire identity callback failed") }
	for name, tc := range map[string]struct {
		wire bool
		id   BillingIdentity
	}{
		"store":                {id: BillingIdentity{StoreID: func(context.Context) string { panic("store callback failed") }}},
		"account":              {id: BillingIdentity{AccountID: func(context.Context, aipapi.Call) string { panic("account callback failed") }}},
		"pricing":              {id: BillingIdentity{CustomerPricingRef: panicRef}},
		"policy":               {id: BillingIdentity{ChargePolicyRef: panicRef}},
		"wire/pricing":         {wire: true, id: BillingIdentity{WireCustomerPricingRef: panicWireRef}},
		"wire/policy":          {wire: true, id: BillingIdentity{WireChargePolicyRef: panicWireRef}},
		"wire/bounded-pricing": {wire: true, id: BillingIdentity{WireBounded: true, CustomerPricingRef: panicRef}},
		"wire/bounded-policy":  {wire: true, id: BillingIdentity{WireBounded: true, ChargePolicyRef: panicRef}},
	} {
		t.Run(name, func(t *testing.T) {
			ex := TestExecutor()
			ex.BillingIdentity = tc.id
			admissions := 0
			ex.BillingExposureAdmission = exposureAdmissionFunc(func(context.Context, BillingExposureAdmissionInput) (billingport.CallExposure, error) {
				admissions++
				return billingport.CallExposure{AccountID: "direct-account"}, nil
			})
			prep := &preparedRequest{call: &aipapi.Call{ID: "direct-call"}, identity: &identityBoundTurn{traceID: "direct-trace", aLeg: b2bua.ALegRecord{ALegID: "direct-a"}}}
			require.NoError(t, stampBillingCallID(prep))
			var err error
			if tc.wire {
				_, err = ex.AuthorizeWireBilling(context.Background(), WireBillingExposureArgs{BillingCallID: prep.billingCallID})
			} else {
				err = ex.authorizeBillingOnce(context.Background(), prep, &routePlanState{})
			}
			require.ErrorIs(t, err, ErrBillingAdmissionDenied)
			var panicErr *safety.PanicError
			require.ErrorAs(t, err, &panicErr)
			require.Zero(t, admissions, "fallible identity resolution must precede admission")
			require.False(t, prep.billingIdentityStamped)
		})
	}
}

// TestCanonicalStoreIDPanic_FailsClosedWithGuardCleanup proves the inherited
// canonical exception-safety gap: a panicking StoreID resolver after
// request-authority admission returns a typed fail-closed error, releases the
// acquired authority exactly once, and runs no backend.
func TestCanonicalStoreIDPanic_FailsClosedWithGuardCleanup(t *testing.T) {
	t.Parallel()
	rig := newBindPanicRig(t)
	rig.ex.BillingIdentity.StoreID = func(context.Context) string { panic("store identity boom") }
	var backendCalls atomic.Int32
	rig.ex.Backends = map[string]execbackend.Backend{
		"never": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				backendCalls.Add(1)
				return makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventResponseFinished}), nil
			},
		},
	}
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-store-panic-canonical"})
	_, execErr := rig.ex.Execute(ctx, canonicalPanicCall("call-store-panic-canonical", "never:m"))
	require.ErrorIs(t, execErr, ErrBillingAdmissionDenied, "want fail-closed admission error")
	var pe *safety.PanicError
	require.True(t, errors.As(execErr, &pe), "want typed panic error, got %T: %v", execErr, execErr)
	require.Equal(t, int32(0), backendCalls.Load(), "no backend may run after StoreID failure")
	admit, _, release := rig.prov.Counts()
	require.Equal(t, 1, admit, "request authority must be acquired once")
	require.Equal(t, 1, release, "acquired authority must be released once via guard cleanup")
}

func TestBillingIdentityFallbacks_FreezeBeforeAdmission(t *testing.T) {
	t.Parallel()
	for _, wire := range []bool{false, true} {
		for _, authoritative := range []bool{false, true} {
			ex := TestExecutor()
			fallback := billingport.CallExposure{AccountID: "fallback-account", PricingRef: usageevidence.VersionRef{ID: "fallback-pricing", Version: "1"}, ChargePolicyRef: usageevidence.VersionRef{ID: "fallback-policy", Version: "1"}}
			want := fallback
			if authoritative {
				want = billingport.CallExposure{AccountID: "admitted-account", PricingRef: usageevidence.VersionRef{ID: "admitted-pricing", Version: "2"}, ChargePolicyRef: usageevidence.VersionRef{ID: "admitted-policy", Version: "2"}}
			}
			pricingCalls, policyCalls := 0, 0
			ex.BillingIdentity = BillingIdentity{
				WireBounded: true,
				AccountID:   func(context.Context, aipapi.Call) string { return fallback.AccountID },
				CustomerPricingRef: func(context.Context, aipapi.Call) usageevidence.VersionRef {
					pricingCalls++
					return fallback.PricingRef
				},
				ChargePolicyRef: func(context.Context, aipapi.Call) usageevidence.VersionRef {
					policyCalls++
					return fallback.ChargePolicyRef
				},
			}
			ex.BillingExposureAdmission = exposureAdmissionFunc(func(context.Context, BillingExposureAdmissionInput) (billingport.CallExposure, error) {
				require.Equal(t, [2]int{1, 1}, [2]int{pricingCalls, policyCalls}, "callbacks must finish before admission")
				fallback = billingport.CallExposure{AccountID: "too-late"}
				if authoritative {
					return want, nil
				}
				return billingport.CallExposure{}, nil
			})
			prep := &preparedRequest{call: &aipapi.Call{ID: "fallback-call"}, identity: &identityBoundTurn{traceID: "fallback-trace", aLeg: b2bua.ALegRecord{ALegID: "fallback-a"}}}
			require.NoError(t, stampBillingCallID(prep))
			var got billingport.CallExposure
			if wire {
				var err error
				got, err = ex.AuthorizeWireBilling(context.Background(), WireBillingExposureArgs{BillingCallID: prep.billingCallID})
				require.NoError(t, err)
			} else {
				require.NoError(t, ex.authorizeBillingOnce(context.Background(), prep, &routePlanState{}))
				require.True(t, prep.billingIdentityStamped)
				got = prep.billingExposure
			}
			require.Equal(t, want, got, "wire=%t authoritative=%t", wire, authoritative)
			require.Equal(t, [2]int{1, 1}, [2]int{pricingCalls, policyCalls}, "no callback may run after admission")
		}
	}
}

// TestStampBillingStoreID_NilEmptyExactlyOnce preserves the ordinary contract:
// nil/empty resolvers stay an explicit absence, stamped once, never re-resolved.
func TestStampBillingStoreID_NilEmptyExactlyOnce(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	for name, resolve := range map[string]func(context.Context) string{
		"nil":   nil,
		"blank": func(context.Context) string { calls.Add(1); return "  " },
	} {
		ex := &Executor{}
		ex.BillingIdentity.StoreID = resolve
		prep := &preparedRequest{}
		require.NoError(t, ex.stampBillingStoreID(context.Background(), prep), name)
		require.True(t, prep.billingStoreIDStamped, name)
		require.Empty(t, prep.billingStoreID, name)
		require.NoError(t, ex.stampBillingStoreID(context.Background(), prep), name)
	}
	require.Equal(t, int32(1), calls.Load(), "resolver runs exactly once")
}
