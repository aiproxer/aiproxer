package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	accountingstream "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/streamusage"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

// TestSettleReservationAuthorityLevel proves the settle authority level is
// derived from the settlement kind and whether provider usage is present in the
// event (requirement 8.4-8.6): Final with usage is Authoritative; Final with no
// usage (estimated fallback), Partial, Cancellation, and Unavailable are
// Estimated.
func TestSettleReservationAuthorityLevel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		kind      usagelimit.SettlementKind
		usageEv   aipapi.Event
		wantLevel usagelimit.AuthorityLevel
	}{
		{
			name: "final-with-usage-is-authoritative",
			kind: usagelimit.SettlementKindFinal,
			usageEv: aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: 5, TotalTokens: 5, Accounting: aipapi.UsageAccountingMetadata{
				Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
			}},
			wantLevel: usagelimit.AuthorityLevelAuthoritative,
		},
		{
			name:      "final-no-usage-fallback-is-estimated",
			kind:      usagelimit.SettlementKindFinal,
			usageEv:   aipapi.Event{},
			wantLevel: usagelimit.AuthorityLevelEstimated,
		},
		{
			name: "final-local-estimate-is-not-authoritative",
			kind: usagelimit.SettlementKindFinal,
			usageEv: aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: 5, TotalTokens: 5, Accounting: aipapi.UsageAccountingMetadata{
				Plane: aipapi.UsagePlaneClientVisible, Source: aipapi.UsageSourceLocalEstimator, Authority: aipapi.UsageAuthorityEstimated,
			}},
			wantLevel: usagelimit.AuthorityLevelEstimated,
		},
		{
			name:      "partial-is-estimated",
			kind:      usagelimit.SettlementKindPartial,
			usageEv:   aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: 3, TotalTokens: 3},
			wantLevel: usagelimit.AuthorityLevelEstimated,
		},
		{
			name:      "cancellation-is-estimated",
			kind:      usagelimit.SettlementKindCancellation,
			usageEv:   aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: 2, TotalTokens: 2},
			wantLevel: usagelimit.AuthorityLevelEstimated,
		},
		{
			name:      "unavailable-is-estimated",
			kind:      usagelimit.SettlementKindUnavailable,
			usageEv:   aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: 1, TotalTokens: 1},
			wantLevel: usagelimit.AuthorityLevelEstimated,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			auth := &recordingAuthorityService{
				admitResult: usagelimit.AdmissionResult{
					Allowed:        true,
					Reserved:       true,
					ReservationID:  "reservation-authority-level",
					ReservedAmount: authorityInputAmount(8),
					PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
				},
				status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
			}
			state := attemptAuthorityState{
				admissionInput:  testAuthorityAdmissionInput(8),
				admissionResult: auth.admitResult,
			}
			l := newAuthorityLifecycle(auth, nil, state, authorityCandidate())

			l.Settle(context.Background(), tc.kind, tc.usageEv, false)

			if auth.settleCalls.Load() != 1 {
				t.Fatalf("settle calls = %d, want 1", auth.settleCalls.Load())
			}
			got := auth.lastSettle()
			if got.Authority != tc.wantLevel {
				t.Fatalf("settle authority = %q, want %q", got.Authority, tc.wantLevel)
			}
		})
	}
}

// authoritativeUsageEvent builds the provider-authoritative final usage event
// used by the reconcile tests.
func authoritativeUsageEvent(inputTokens int) aipapi.Event {
	return aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: inputTokens, TotalTokens: inputTokens, Accounting: aipapi.UsageAccountingMetadata{
		Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
	}}
}

// TestReconcileAuthoritativeAdjustsEstimatedSettlement pins the runtime
// reconcile contract: ReconcileAuthoritative is a no-op unless the lifecycle is
// settled; for units whose evidence was estimated it submits one Settle carrying
// the Authoritative authority level, the authoritative replay sequence (5),
// cleared source keys, and the event's final usage/presence, and it returns the
// port's Applied answer. The store-side adjustment and replay idempotence are
// Enterprise rule semantics (TestEnterpriseRuleReconcileAuthoritativeAdjustsEstimatedSettlement).
func TestReconcileAuthoritativeAdjustsEstimatedSettlement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	newRecorder := func() *recordingAuthorityService {
		rec := &recordingAuthorityService{}
		rec.admitResult = usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "reservation-reconcile",
			ReservedAmount: authorityInputAmount(8),
		}
		return rec
	}
	newState := func(rec *recordingAuthorityService) attemptAuthorityState {
		return attemptAuthorityState{
			admissionInput:  testAuthorityAdmissionInput(8),
			admissionResult: rec.admitResult,
		}
	}

	t.Run("not settled is a no-op", func(t *testing.T) {
		t.Parallel()

		rec := newRecorder()
		l := newAuthorityLifecycle(rec, nil, newState(rec), authorityCandidate())

		if applied := l.ReconcileAuthoritative(ctx, authoritativeUsageEvent(5)); applied {
			t.Fatal("ReconcileAuthoritative before any settlement = true, want false")
		}
		if got := rec.settleCalls.Load(); got != 0 {
			t.Fatalf("settle calls = %d, want 0 (reconcile must not settle an open lifecycle)", got)
		}
	})

	t.Run("estimated settle then authoritative upgrade carries the reconcile contract", func(t *testing.T) {
		t.Parallel()

		rec := newRecorder()
		l := newAuthorityLifecycle(rec, nil, newState(rec), authorityCandidate())

		// Step 1: settle with an empty event (no usage) — the estimate fallback
		// applies and the unit's recorded evidence authority stays Estimated.
		if applied := l.Settle(ctx, usagelimit.SettlementKindPartial, aipapi.Event{}, false); !applied {
			t.Fatal("first Settle = false, want true")
		}
		if !l.Settled() {
			t.Fatal("expected lifecycle settled after first Settle")
		}

		// Step 2: ReconcileAuthoritative with actual usage (5) must submit the
		// authoritative adjustment for the estimated unit.
		if applied := l.ReconcileAuthoritative(ctx, authoritativeUsageEvent(5)); !applied {
			t.Fatal("ReconcileAuthoritative = false, want true (adjustment must apply)")
		}
		if got := rec.settleCalls.Load(); got != 2 {
			t.Fatalf("settle calls = %d, want 2 (initial settle + reconcile)", got)
		}
		got := rec.lastSettle()
		if got.Kind != usagelimit.SettlementKindFinal {
			t.Fatalf("reconcile kind = %q, want %q", got.Kind, usagelimit.SettlementKindFinal)
		}
		if got.Authority != usagelimit.AuthorityLevelAuthoritative {
			t.Fatalf("reconcile authority = %q, want %q", got.Authority, usagelimit.AuthorityLevelAuthoritative)
		}
		if got.Sequence != usagelimit.SettlementSequence(usagelimit.SettlementKindFinal, usagelimit.AuthorityLevelAuthoritative) || got.Sequence != 5 {
			t.Fatalf("reconcile sequence = %d, want the authoritative replay sequence 5", got.Sequence)
		}
		if !got.FinalUsagePresent {
			t.Fatal("reconcile FinalUsagePresent = false, want true for an authoritative usage event")
		}
		if got.FinalUsage != (usagelimit.Amount{Unit: usagelimit.AmountUnitInputTokens, Value: 5}) {
			t.Fatalf("reconcile final usage = %#v, want 5 input_tokens", got.FinalUsage)
		}
		if len(got.Reservations) != 1 {
			t.Fatalf("reconcile reservations = %#v, want one reservation", got.Reservations)
		}
		reservation := got.Reservations[0]
		if reservation.Reservation.RuleID != "rule-1" {
			t.Fatalf("reconcile rule id = %q, want rule-1", reservation.Reservation.RuleID)
		}
		if reservation.Reservation.ReservationKey.RuleID != "rule-1" {
			t.Fatalf("reconcile reservation key rule id = %q, want rule-1", reservation.Reservation.ReservationKey.RuleID)
		}
		if reservation.SourceKey != "" || reservation.Reservation.SourceKey != "" {
			t.Fatalf("reconcile source keys = %q/%q, want cleared for the authoritative replay",
				reservation.SourceKey, reservation.Reservation.SourceKey)
		}
		if reservation.Authority != usagelimit.AuthorityLevelAuthoritative {
			t.Fatalf("reconcile descriptor authority = %q, want authoritative", reservation.Authority)
		}
	})

	t.Run("replay with no remaining upgrade does not call the port", func(t *testing.T) {
		t.Parallel()

		rec := newRecorder()
		l := newAuthorityLifecycle(rec, nil, newState(rec), authorityCandidate())
		if applied := l.Settle(ctx, usagelimit.SettlementKindPartial, aipapi.Event{}, false); !applied {
			t.Fatal("first Settle = false, want true")
		}
		if applied := l.ReconcileAuthoritative(ctx, authoritativeUsageEvent(5)); !applied {
			t.Fatal("first ReconcileAuthoritative = false, want true")
		}

		// The unit is now recorded as Authoritative, so the replay finds no
		// upgrade to submit and never re-touches the port.
		if applied := l.ReconcileAuthoritative(ctx, authoritativeUsageEvent(5)); applied {
			t.Fatal("replay ReconcileAuthoritative = true, want false (no upgrade remains)")
		}
		if got := rec.settleCalls.Load(); got != 2 {
			t.Fatalf("settle calls after replay = %d, want 2 (no third call)", got)
		}
	})

	t.Run("non-authoritative event upgrades nothing", func(t *testing.T) {
		t.Parallel()

		rec := newRecorder()
		l := newAuthorityLifecycle(rec, nil, newState(rec), authorityCandidate())
		if applied := l.Settle(ctx, usagelimit.SettlementKindPartial, aipapi.Event{}, false); !applied {
			t.Fatal("first Settle = false, want true")
		}

		estimated := aipapi.Event{Kind: aipapi.EventUsageDelta, InputTokens: 5, TotalTokens: 5}
		if applied := l.ReconcileAuthoritative(ctx, estimated); applied {
			t.Fatal("ReconcileAuthoritative with estimated usage = true, want false")
		}
		if got := rec.settleCalls.Load(); got != 1 {
			t.Fatalf("settle calls = %d, want 1 (empty upgrade set must not call the port)", got)
		}
	})
}

// TestCancellationPathReconcileAuthoritativeAdjustsPriorSettle proves the full
// cancellation path: when a reservation is already settled (estimated) and
// finalizeBillingAfterCancel recovers authoritative usage,
// persistCancellationBilling routes to ReconcileAuthoritative to adjust the
// prior settlement instead of no-op (requirement 7.6, 8.4-8.6). The runtime
// routing is asserted on the recorded port input; the store-side adjustment is
// Enterprise rule semantics (TestEnterpriseRuleCancellationThenReconcileAdjustsPriorSettle).
func TestCancellationPathReconcileAuthoritativeAdjustsPriorSettle(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	rec := &recordingAuthorityService{}
	rec.admitResult = usagelimit.AdmissionResult{
		Allowed:        true,
		Reserved:       true,
		ReservationID:  "reservation-cancel-reconcile",
		ReservedAmount: authorityInputAmount(8),
	}
	admissionInput := testAuthorityAdmissionInput(8)
	admissionInput.Correlation.TraceID = "trace-cancel-reconcile"

	// Build the executor with the fake authority service and a FinalizeBilling
	// hook that returns authoritative usage (5, less than the estimate of 8).
	ex := TestExecutor()
	ex.Store = mustNewB2BUAStore(t)
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"backend-1": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
			}),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
			FinalizeBilling: func(ctx context.Context, in execbackend.BillingFinalizationInput) (aipapi.Event, error) {
				return aipapi.Event{
					Kind:          aipapi.EventUsageDelta,
					InputTokens:   5,
					TotalTokens:   5,
					CostNanoUnits: 30,
					Currency:      "USD",
					Accounting: aipapi.UsageAccountingMetadata{
						Plane:     aipapi.UsagePlaneProviderBillable,
						Source:    aipapi.UsageSourceProviderReported,
						Authority: aipapi.UsageAuthorityAuthoritative,
					},
				}, nil
			},
		},
	}
	ex.UsageAuthority = rec
	ex.StreamUsage = accountingstream.New(nil, accountingstream.Config{})

	state := attemptAuthorityState{admissionInput: admissionInput, admissionResult: rec.admitResult}

	rs := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{
				ID:         "request-1",
				Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions},
			},
			traceID: "trace-cancel-reconcile",
			aLegID:  "a-leg-1",
		}),
		attempt:          testAttemptSlot(b2bua.BLegRecord{BLegID: "bleg-1", Seq: 1}, authorityCandidate(), newAuthorityLifecycle(rec, nil, state, authorityCandidate()), newAttemptAccountingTracker(at)),
		responsePipeline: &responsePipeline{seenEvents: []aipapi.Event{}},
	}
	bindTestRuntimeOwners(rs, ex)

	// Step 1: settle as Partial with no usage — the estimate fallback applies and
	// the lifecycle is marked settled. Use the current attempt authority so the
	// settled guard is set on the session-owned value, not a separate copy.
	testAttemptSession(rs).authority.Settle(context.Background(), usagelimit.SettlementKindPartial, aipapi.Event{}, false)
	if !testAttemptSession(rs).authority.Settled() {
		t.Fatal("expected lifecycle settled after partial settle")
	}

	// Step 2: persistCancellationBilling. usageObserved is false, so it calls
	// finalizeBillingAfterCancel, which recovers authoritative usage {5}. The
	// reservation is already settled, so reconcileOrSettleCancellationAuthority
	// routes to ReconcileAuthoritative, which submits the authoritative
	// adjustment instead of no-opping.
	if testAttemptSession(rs).accounting.usageObserved {
		t.Fatal("test staging: usageObserved must be false before persistCancellationBilling (path 2)")
	}
	rs.terminal.persistCancellationBilling(context.Background(), rs.attempt.snapshot(), "client canceled", rs.facts.terminalFacts(), rs.responsePipeline)

	if got := rec.settleCalls.Load(); got != 2 {
		t.Fatalf("settle calls = %d, want 2 (partial settle + authoritative reconcile)", got)
	}
	got := rec.lastSettle()
	if got.Kind != usagelimit.SettlementKindFinal {
		t.Fatalf("reconcile kind = %q, want %q", got.Kind, usagelimit.SettlementKindFinal)
	}
	if got.Authority != usagelimit.AuthorityLevelAuthoritative {
		t.Fatalf("reconcile authority = %q, want %q", got.Authority, usagelimit.AuthorityLevelAuthoritative)
	}
	if got.Sequence != 5 {
		t.Fatalf("reconcile sequence = %d, want 5", got.Sequence)
	}
	if !got.FinalUsagePresent {
		t.Fatal("reconcile FinalUsagePresent = false, want true")
	}
	if got.FinalUsage != (usagelimit.Amount{Unit: usagelimit.AmountUnitInputTokens, Value: 5}) {
		t.Fatalf("reconcile final usage = %#v, want the recovered 5 input_tokens", got.FinalUsage)
	}
	if rec.releaseCalls.Load() != 0 {
		t.Fatalf("reconcile must not release; releases=%d", rec.releaseCalls.Load())
	}
	if !testAttemptSession(rs).authority.Settled() {
		t.Fatal("expected lifecycle to remain settled after reconcile")
	}
}

// mustNewB2BUAStore creates a b2bua MemoryStore for test executors, failing the
// test on error.
func mustNewB2BUAStore(t *testing.T) *b2bua.MemoryStore {
	t.Helper()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new b2bua store: %v", err)
	}
	return store
}
