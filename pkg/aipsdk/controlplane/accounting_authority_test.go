package controlplane_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestAccountingAuthorityContractsRoundTrip(t *testing.T) {
	t.Parallel()
	now := time.Unix(123, 0).UTC()

	status := controlplane.AccountingAuthorityStatus{
		State:          controlplane.AccountingAuthorityAdvisoryOnly,
		Reason:         controlplane.ReasonUnsupported,
		LastUpdatedAt:  now,
		EvidenceState:  controlplane.EvidencePartial,
		RedactionState: controlplane.RedactionSummarized,
	}
	statusJSON := roundTripJSON(t, status)
	var statusBack controlplane.AccountingAuthorityStatus
	unmarshalJSON(t, statusJSON, &statusBack)
	if statusBack.State != controlplane.AccountingAuthorityAdvisoryOnly {
		t.Fatalf("status state lost: %#v", statusBack)
	}
	if statusBack.Reason != controlplane.ReasonUnsupported {
		t.Fatalf("status reason lost: %#v", statusBack)
	}
	if !statusBack.LastUpdatedAt.Equal(now) {
		t.Fatalf("status time lost: %#v", statusBack.LastUpdatedAt)
	}

	detail := controlplane.AccountingAuthorityDetail{
		Correlation: controlplane.Correlation{
			TraceID:    "trace-1",
			RequestID:  "req-1",
			SessionID:  "sess-1",
			ALegID:     "a-1",
			BLegID:     "b-1",
			AttemptSeq: 7,
			FrontendID: "openai-responses",
			BackendID:  "backend-1",
			Model:      "model-1",
		},
		Scope: controlplane.ScopeSnapshot{
			Principal:      scope.PrincipalScopeView{PrincipalID: scope.Known("principal-1")},
			PrincipalID:    scope.Known("principal-1"),
			TenantID:       scope.Unknown(),
			ProjectID:      scope.Known(""),
			OrganizationID: scope.Known("org-1"),
		},
		RuleID:          "rule-1",
		RuleType:        "quota",
		Outcome:         controlplane.AccountingOutcomeReserve,
		ReasonCode:      "reservation_required",
		Authority:       controlplane.AccountingAuthoritySourceReserved,
		ReservationID:   "reservation-1",
		SettlementState: controlplane.AccountingSettlementSettled,
		Unit:            "tokens",
		Limit:           1000,
		Consumed:        750,
		Reserved:        250,
		Remaining:       0,
		Adjustment:      -25,
		Currency:        "usd",
		WindowStart:     now,
		WindowEnd:       now.Add(time.Hour),
		WindowResetAt:   now.Add(2 * time.Hour),
		EvidenceState:   controlplane.EvidenceRecorded,
		RedactionState:  controlplane.RedactionNone,
	}
	detailJSON := roundTripJSON(t, detail)
	var detailBack controlplane.AccountingAuthorityDetail
	unmarshalJSON(t, detailJSON, &detailBack)
	if detailBack.Scope.TenantID.IsKnown() || !detailBack.Scope.TenantID.IsUnknown() {
		t.Fatalf("unknown tenant must round-trip as unknown: %#v", detailBack.Scope.TenantID)
	}
	if !detailBack.Scope.ProjectID.IsKnownEmpty() {
		t.Fatalf("known-empty project must round-trip as known-empty: %#v", detailBack.Scope.ProjectID)
	}
	if detailBack.ReservationID != "reservation-1" || detailBack.RuleID != "rule-1" {
		t.Fatalf("detail lost safe identifiers: %#v", detailBack)
	}

	limitQuery := controlplane.AccountingLimitStatusQuery{
		Common: controlplane.CommonFilters{
			Scope: controlplane.ScopeFilters{
				PrincipalID: scope.Known("principal-1"),
				TenantID:    scope.Unknown(),
				ProjectID:   scope.Known(""),
			},
			TraceID: "trace-1",
		},
		RuleID:          "rule-1",
		Authority:       controlplane.AccountingAuthoritySourceAuthoritative,
		SettlementState: controlplane.AccountingSettlementPending,
		EvidenceState:   controlplane.EvidenceRecorded,
		Limit:           75,
		Cursor:          controlplane.Cursor{Token: "cursor-1"},
		Visibility:      controlplane.VisibilityDefault,
	}
	limitJSON := roundTripJSON(t, limitQuery)
	var limitBack controlplane.AccountingLimitStatusQuery
	unmarshalJSON(t, limitJSON, &limitBack)
	if limitBack.Common.Scope.TenantID.IsKnown() || !limitBack.Common.Scope.TenantID.IsUnknown() {
		t.Fatalf("limit query tenant must remain unknown: %#v", limitBack.Common.Scope.TenantID)
	}
	if !limitBack.Common.Scope.ProjectID.IsKnownEmpty() {
		t.Fatalf("limit query project must remain known-empty: %#v", limitBack.Common.Scope.ProjectID)
	}
	if limitBack.Limit != 75 || limitBack.Cursor.Token != "cursor-1" {
		t.Fatalf("limit query metadata lost: %#v", limitBack)
	}

	decisionQuery := controlplane.AccountingDecisionQuery{
		Common: controlplane.CommonFilters{
			Scope: controlplane.ScopeFilters{
				PrincipalID: scope.Known("principal-1"),
				TenantID:    scope.Unknown(),
			},
			TraceID: "trace-1",
		},
		RuleID:          "rule-1",
		Authority:       controlplane.AccountingAuthoritySourceEstimated,
		SettlementState: controlplane.AccountingSettlementAdjusted,
		EvidenceState:   controlplane.EvidencePartial,
		Limit:           10,
		Cursor:          controlplane.Cursor{Token: "next"},
		Visibility:      controlplane.VisibilityPrivileged,
	}
	decisionJSON := roundTripJSON(t, decisionQuery)
	var decisionBack controlplane.AccountingDecisionQuery
	unmarshalJSON(t, decisionJSON, &decisionBack)
	if decisionBack.Visibility != controlplane.VisibilityPrivileged {
		t.Fatalf("decision query visibility lost: %#v", decisionBack.Visibility)
	}
	if decisionBack.Limit != 10 || decisionBack.Cursor.Token != "next" {
		t.Fatalf("decision query metadata lost: %#v", decisionBack)
	}

	page := controlplane.Page[controlplane.AccountingDecisionRow]{
		Items: []controlplane.AccountingDecisionRow{
			{
				Correlation: controlplane.Correlation{TraceID: "trace-1", RequestID: "req-1"},
				Scope: controlplane.ScopeSnapshot{
					PrincipalID: scope.Known("principal-1"),
					TenantID:    scope.Unknown(),
				},
				RuleID:         "rule-1",
				Outcome:        controlplane.AccountingOutcomeAdvisory,
				ReasonCode:     "advisory",
				Authority:      controlplane.AccountingAuthoritySourceAdvisory,
				EvidenceState:  controlplane.EvidenceRecorded,
				RedactionState: controlplane.RedactionSummarized,
			},
		},
		Next: controlplane.Cursor{Token: "cursor-2"},
		Unsupported: []controlplane.UnsupportedFilter{
			{Field: "backend_id", Reason: "not recorded for this store"},
		},
		Visibility: controlplane.VisibilityDefault,
	}
	pageJSON := roundTripJSON(t, page)
	var pageBack controlplane.Page[controlplane.AccountingDecisionRow]
	unmarshalJSON(t, pageJSON, &pageBack)
	if len(pageBack.Items) != 1 || pageBack.Items[0].RuleID != "rule-1" {
		t.Fatalf("page items lost: %#v", pageBack.Items)
	}
	if pageBack.Next.Token != "cursor-2" || pageBack.Visibility != controlplane.VisibilityDefault {
		t.Fatalf("page metadata lost: %#v", pageBack)
	}
	if len(pageBack.Unsupported) != 1 || pageBack.Unsupported[0].Field != "backend_id" {
		t.Fatalf("unsupported filter lost: %#v", pageBack.Unsupported)
	}
}

func TestAccountingAuthorityTypesAvoidForbiddenFields(t *testing.T) {
	t.Parallel()
	forbidden := []string{"Bearer", "APIKey", "Secret", "OAuth", "Header", "Password", "RawPayload", "RawBody", "RawUsageJSON", "ResumeToken", "AccessToken", "RefreshToken", "IDToken", "Prompt", "Provider", "ProviderPayload", "SQL", "Driver"}

	assertNoForbiddenFields(t, controlplane.AccountingAuthorityStatus{}, forbidden)
	assertNoForbiddenFields(t, controlplane.AccountingAuthorityDetail{}, forbidden)
	assertNoForbiddenFields(t, controlplane.AccountingLimitStatusQuery{}, forbidden)
	assertNoForbiddenFields(t, controlplane.AccountingDecisionQuery{}, forbidden)
	assertNoForbiddenFields(t, controlplane.AccountingLimitStatusRow{}, forbidden)
	assertNoForbiddenFields(t, controlplane.AccountingDecisionRow{}, forbidden)
}

func TestAccountingQueriesInterfaceCompiles(t *testing.T) {
	t.Parallel()
	var _ controlplane.AccountingQueries = accountingQueryStub{}
}

// TestAccountingDecisionRowDeltasOmitZero asserts the Released/Overage/Adjustment
// delta fields on AccountingDecisionRow serialize with ,omitzero: present when
// non-zero and absent when zero — matching the existing WindowStart/End/ResetAt
// ,omitzero contract.
func TestAccountingDecisionRowDeltasOmitZero(t *testing.T) {
	t.Parallel()

	// Populated deltas: non-zero fields must serialize as present; a zero
	// sibling (Overage here) must still be omitted.
	populated := controlplane.AccountingDecisionRow{
		Correlation:     controlplane.Correlation{TraceID: "trace-1", RequestID: "req-1"},
		RuleID:          "rule-1",
		Outcome:         controlplane.AccountingOutcomeReconcile,
		SettlementState: controlplane.AccountingSettlementSettled,
		Unit:            "tokens",
		Released:        40,
		Overage:         0,
		Adjustment:      40,
		EvidenceState:   controlplane.EvidenceRecorded,
		RedactionState:  controlplane.RedactionSummarized,
	}
	raw, err := json.Marshal(populated)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"released"`) {
		t.Fatalf("non-zero released must be present, got: %s", raw)
	}
	if !strings.Contains(string(raw), `"adjustment"`) {
		t.Fatalf("non-zero adjustment must be present, got: %s", raw)
	}
	if strings.Contains(string(raw), `"overage"`) {
		t.Fatalf("zero overage must be omitted (omitzero), got: %s", raw)
	}
	var backPop controlplane.AccountingDecisionRow
	unmarshalJSON(t, raw, &backPop)
	if backPop.Released != 40 || backPop.Overage != 0 || backPop.Adjustment != 40 {
		t.Fatalf("round-trip deltas lost: released=%d overage=%d adjustment=%d",
			backPop.Released, backPop.Overage, backPop.Adjustment)
	}

	// Zero deltas: all three must be omitted entirely.
	zero := controlplane.AccountingDecisionRow{
		Correlation:    controlplane.Correlation{TraceID: "trace-1", RequestID: "req-1"},
		RuleID:         "rule-1",
		Outcome:        controlplane.AccountingOutcomeReserve,
		Unit:           "tokens",
		EvidenceState:  controlplane.EvidenceRecorded,
		RedactionState: controlplane.RedactionSummarized,
	}
	rawZero, err := json.Marshal(zero)
	if err != nil {
		t.Fatalf("marshal zero: %v", err)
	}
	for _, key := range []string{"released", "overage", "adjustment"} {
		if strings.Contains(string(rawZero), `"`+key+`"`) {
			t.Fatalf("zero delta %q must be omitted (omitzero), got: %s", key, rawZero)
		}
	}
	var backZero controlplane.AccountingDecisionRow
	unmarshalJSON(t, rawZero, &backZero)
	if backZero.Released != 0 || backZero.Overage != 0 || backZero.Adjustment != 0 {
		t.Fatalf("zero deltas must round-trip as zero: %#v", backZero)
	}
}

type accountingQueryStub struct{}

func (accountingQueryStub) Status(context.Context) (controlplane.AccountingAuthorityStatus, error) {
	return controlplane.AccountingAuthorityStatus{}, nil
}

func (accountingQueryStub) LimitStatus(context.Context, controlplane.AccountingLimitStatusQuery) (controlplane.Page[controlplane.AccountingLimitStatusRow], error) {
	return controlplane.Page[controlplane.AccountingLimitStatusRow]{}, nil
}

func (accountingQueryStub) Decisions(context.Context, controlplane.AccountingDecisionQuery) (controlplane.Page[controlplane.AccountingDecisionRow], error) {
	return controlplane.Page[controlplane.AccountingDecisionRow]{}, nil
}

func roundTripJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func unmarshalJSON(t *testing.T, raw []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}
