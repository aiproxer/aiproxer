package policydecision_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

func TestAccountingReasonCodesAreStable(t *testing.T) {
	t.Parallel()
	want := map[policydecision.AccountingReasonCode]string{
		policydecision.AccountingReasonAllowed:           "allowed",
		policydecision.AccountingReasonAdvisory:          "advisory",
		policydecision.AccountingReasonClamped:           "clamped",
		policydecision.AccountingReasonReserved:          "reserved",
		policydecision.AccountingReasonReconciled:        "reconciled",
		policydecision.AccountingReasonQuotaExceeded:     "quota_exceeded",
		policydecision.AccountingReasonRateLimited:       "rate_limited",
		policydecision.AccountingReasonBudgetExceeded:    "budget_exceeded",
		policydecision.AccountingReasonReservationFailed: "reservation_failed",
		policydecision.AccountingReasonUnavailable:       "unavailable",
		policydecision.AccountingReasonError:             "error",
	}
	for got, w := range want {
		if string(got) != w {
			t.Fatalf("reason code drift: got %q want %q", got, w)
		}
		if !got.IsKnown() {
			t.Fatalf("reason code %q must be known", got)
		}
	}
	if policydecision.AccountingReasonCode("bogus").IsKnown() {
		t.Fatalf("unknown reason code reported known")
	}
}

func TestProjectAccountingRecordPreservesDecisionAndAddsSafeAnnotations(t *testing.T) {
	t.Parallel()
	base := policydecision.Record{
		Stage:         feature.StageIDPreRequest,
		Provider:      policydecision.ProviderRef{ID: "acct", Stage: feature.StageIDPreRequest},
		Outcome:       policydecision.OutcomeAllow,
		Effect:        policydecision.EffectAnnotate,
		ClientMessage: "advisory accounting evidence",
	}
	got, ok := policydecision.ProjectAccountingRecord(base, policydecision.AccountingProjection{
		ReasonCode:       policydecision.AccountingReasonBudgetExceeded,
		RuleID:           "tenant.quota",
		Authority:        "authoritative",
		ReservationID:    "reservation-1",
		SettlementStatus: "reserved",
	})
	if !ok {
		t.Fatalf("projecting accounting record must succeed")
	}
	if got.Outcome != base.Outcome || got.Effect != base.Effect || got.Stage != base.Stage {
		t.Fatalf("projection must preserve the base policy decision: %#v", got)
	}
	if got.ReasonCode != string(policydecision.AccountingReasonBudgetExceeded) {
		t.Fatalf("reason code lost: %#v", got)
	}
	if got.ClientCategory == "" {
		t.Fatalf("projection must populate a stable client category")
	}
	if got.Annotations["accounting.rule_id"] != "tenant.quota" {
		t.Fatalf("rule annotation lost: %#v", got.Annotations)
	}
	if got.Annotations["accounting.reason"] != "budget_exceeded" {
		t.Fatalf("reason annotation lost: %#v", got.Annotations)
	}
	if got.Annotations["accounting.authority"] != "authoritative" {
		t.Fatalf("authority annotation lost: %#v", got.Annotations)
	}
	if got.Annotations["accounting.reservation_id"] != "reservation-1" {
		t.Fatalf("reservation annotation lost: %#v", got.Annotations)
	}
	if got.Annotations["accounting.settlement_status"] != "reserved" {
		t.Fatalf("settlement annotation lost: %#v", got.Annotations)
	}
	if err := policydecision.ValidateRecord(got); err != nil {
		t.Fatalf("projected record must remain legal: %v", err)
	}
}

func TestProjectAccountingRecordRejectsUnknownReasonCode(t *testing.T) {
	t.Parallel()
	_, ok := policydecision.ProjectAccountingRecord(policydecision.Record{
		Stage:   feature.StageIDPreRequest,
		Outcome: policydecision.OutcomeAllow,
		Effect:  policydecision.EffectAnnotate,
	}, policydecision.AccountingProjection{
		ReasonCode: policydecision.AccountingReasonCode("bogus"),
	})
	if ok {
		t.Fatalf("unknown reason code must be rejected")
	}
}

func TestProjectAccountingRecordPreservesPreExistingAnnotations(t *testing.T) {
	t.Parallel()
	base := policydecision.Record{
		Stage:    feature.StageIDPreRequest,
		Provider: policydecision.ProviderRef{ID: "acct", Stage: feature.StageIDPreRequest},
		Outcome:  policydecision.OutcomeAllow,
		Effect:   policydecision.EffectAnnotate,
		Annotations: map[string]string{
			"custom.key": "custom.value",
		},
	}
	got, ok := policydecision.ProjectAccountingRecord(base, policydecision.AccountingProjection{
		ReasonCode: policydecision.AccountingReasonBudgetExceeded,
		RuleID:     "tenant.quota",
	})
	if !ok {
		t.Fatalf("projecting accounting record must succeed")
	}
	if got.Annotations["custom.key"] != "custom.value" {
		t.Errorf("pre-existing annotation lost: got %q, want %q", got.Annotations["custom.key"], "custom.value")
	}
	if got.Annotations["accounting.rule_id"] != "tenant.quota" {
		t.Errorf("new accounting annotation lost: got %q", got.Annotations["accounting.rule_id"])
	}
}
