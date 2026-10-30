package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestSettlementAuthorityIsIndependentPerTokenUnit(t *testing.T) {
	t.Parallel()
	rec := &recordingAuthorityService{admitResult: usagelimit.AdmissionResult{
		Reserved: true,
		Reservations: []usagelimit.AdmissionReservation{
			{ReservationID: "r-input", RuleID: "input", ReservedAmount: usagelimit.Amount{Unit: usagelimit.AmountUnitInputTokens, Value: 8}},
			{ReservationID: "r-output", RuleID: "output", ReservedAmount: usagelimit.Amount{Unit: usagelimit.AmountUnitOutputTokens, Value: 6}},
		},
	}}
	state := attemptAuthorityState{admissionInput: testAuthorityAdmissionInput(8), admissionResult: rec.admitResult}
	lifecycle := newAuthorityLifecycle(rec, nil, state, authorityCandidate())
	inputOnly := aipapi.Event{
		Kind: aipapi.EventUsageDelta, InputTokens: 5, TotalTokens: 5,
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
		},
	}
	if !lifecycle.Settle(context.Background(), usagelimit.SettlementKindFinal, inputOnly, false) {
		t.Fatal("initial settle was not applied")
	}
	initial := rec.lastSettle()
	if len(initial.Reservations) != 2 {
		t.Fatalf("settlement descriptors = %d, want 2", len(initial.Reservations))
	}
	if initial.Reservations[0].Authority != usagelimit.AuthorityLevelAuthoritative {
		t.Fatalf("input authority = %q, want authoritative", initial.Reservations[0].Authority)
	}
	if initial.Reservations[1].Authority != usagelimit.AuthorityLevelEstimated || initial.Reservations[1].FinalUsage.Value != 6 {
		t.Fatalf("missing output descriptor = %#v, want estimated fallback 6", initial.Reservations[1])
	}
	outputOnly := aipapi.Event{
		Kind: aipapi.EventUsageDelta, OutputTokens: 3, TotalTokens: 3,
		Accounting: inputOnly.Accounting,
	}
	if !lifecycle.ReconcileAuthoritative(context.Background(), outputOnly) {
		t.Fatal("output reconciliation was not applied")
	}
	reconciled := rec.lastSettle()
	if len(reconciled.Reservations) != 1 || reconciled.Reservations[0].Reservation.Unit != usagelimit.AmountUnitOutputTokens {
		t.Fatalf("reconciled descriptors = %#v, want output only", reconciled.Reservations)
	}
}

func TestAuthorityUsageEventDropsClientVisibleScopeFromBillableMerge(t *testing.T) {
	t.Parallel()

	provider := aipapi.ScopedUsageDelta{
		InputTokens:  20,
		OutputTokens: 5,
		TotalTokens:  25,
		Accounting: aipapi.UsageAccountingMetadata{
			Plane:     aipapi.UsagePlaneProviderBillable,
			Source:    aipapi.UsageSourceProviderReported,
			Authority: aipapi.UsageAuthorityAuthoritative,
		},
	}
	client := aipapi.ScopedUsageDelta{
		InputTokens:  3,
		OutputTokens: 40,
		TotalTokens:  43,
		Accounting: aipapi.UsageAccountingMetadata{
			Plane:     aipapi.UsagePlaneClientVisible,
			Source:    aipapi.UsageSourceLocalTokenizer,
			Authority: aipapi.UsageAuthorityEstimated,
		},
	}
	got := authorityUsageEvent([]aipapi.Event{{
		Kind:          aipapi.EventUsageDelta,
		UsageScopes:   []aipapi.ScopedUsageDelta{client, provider},
		CostNanoUnits: 99,
		Currency:      "USD",
		CostSource:    string(aipapi.UsageSourceLocalEstimator),
	}})
	if got.Kind != aipapi.EventUsageDelta || len(got.UsageScopes) != 1 {
		t.Fatalf("authority merge = %#v, want one provider scope", got)
	}
	if got.UsageScopes[0].Accounting != provider.Accounting || got.InputTokens != provider.InputTokens || got.OutputTokens != provider.OutputTokens {
		t.Fatalf("authority merge selected wrong counters/scope: %#v", got)
	}
	if authorityForSettlement(usagelimit.SettlementKindFinal, got) != usagelimit.AuthorityLevelAuthoritative {
		t.Fatalf("authority = %q, want authoritative", authorityForSettlement(usagelimit.SettlementKindFinal, got))
	}
	if got.CostNanoUnits != 0 || got.Currency != "" || got.CostSource != "" {
		t.Fatalf("ambiguous event-level cost leaked into provider merge: %#v", got)
	}
}

func TestMergeUsageEventsReturnsAbsentEventWhenNothingIsIncluded(t *testing.T) {
	t.Parallel()

	if got := mergeUsageEventsForClient(nil, false); got.Kind != "" {
		t.Fatalf("empty merge kind = %q, want absent event", got.Kind)
	}
	got := mergeUsageEventsForClient([]aipapi.Event{{Kind: aipapi.EventUsageDelta, UsageScopes: []aipapi.ScopedUsageDelta{{
		Accounting: aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneProviderBillable},
	}}}}, true)
	if got.Kind != "" {
		t.Fatalf("all-skipped merge kind = %q, want absent event", got.Kind)
	}
}

func TestUnmarkedProviderUsageIsNotAuthoritative(t *testing.T) {
	t.Parallel()

	if authoritativeProviderAccounting(aipapi.UsageAccountingMetadata{
		Plane:     aipapi.UsagePlaneProviderBillable,
		Authority: aipapi.UsageAuthorityAuthoritative,
	}) {
		t.Fatal("provider usage without an explicit provider source must not be authoritative")
	}
}
