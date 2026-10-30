package runtimebundle

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	cpadmin "github.com/aiproxer/aiproxer/internal/stdhttp/admin/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// Synthetic fixture for the adapter preservation test; not a real credential.
const authorityAdapterHTTPSecret = "ocbs-authority-adapter-secret" // gitleaks:allow

type adaptedAuthorityPage[T any] struct {
	State usagelimit.QueryState `json:"state"`
	Page  cp.Page[T]            `json:"page"`
}

// TestAccountingAuthorityQueries_AdapterPreservesHTTPPages is the bounded
// host preservation test for the identity adapter: an injected query-capable
// port flows through adaptAccountingAuthorityQueries into the controlplane
// HTTP handler, and result pages, continuation cursors, states, and request
// filters pass through unchanged behind the protected-secret boundary. The
// real-service vectors live with the Enterprise service (task 5.3).
func TestAccountingAuthorityQueries_AdapterPreservesHTTPPages(t *testing.T) {
	t.Parallel()
	port := &fakeAuthorityQueries{
		status:        cp.AccountingAuthorityStatus{State: cp.AccountingAuthorityReady},
		limitState:    usagelimit.QueryStateUnsupported,
		decisionState: usagelimit.QueryStateReady,
		limitPage: cp.Page[cp.AccountingLimitStatusRow]{
			Items:       []cp.AccountingLimitStatusRow{{RuleID: "tenant.requests", Limit: 10, Remaining: 9}},
			Unsupported: []cp.UnsupportedFilter{{Field: "settlement_state", Reason: "not recorded"}},
		},
		decisionPage: cp.Page[cp.AccountingDecisionRow]{
			Items: []cp.AccountingDecisionRow{{RuleID: "tenant.requests", Outcome: cp.AccountingOutcomeAllow}},
			Next:  cp.Cursor{Token: "cursor-1"},
		},
		decisionNextPage: cp.Page[cp.AccountingDecisionRow]{
			Items: []cp.AccountingDecisionRow{{RuleID: "tenant.requests", Outcome: cp.AccountingOutcomeAllow}},
		},
	}
	handler := diag.WrapDiagnosticsProtectHeaders(authorityAdapterHTTPSecret, nil, cpadmin.NewAccountingAuthorityHandler(cpadmin.AuthorityOptions{
		Queries:         adaptAccountingAuthorityQueries(port),
		DefaultPageSize: 25,
		MaxPageSize:     50,
	}))

	serve := func(target, secret string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if secret != "" {
			req.Header.Set(diag.HeaderDiagnosticsSecret, secret)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	denied := serve("/decision-history?limit=1", "")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("missing secret status=%d want 403", denied.Code)
	}
	if port.decisionCallsPerformed != 0 {
		t.Fatal("unauthorized request must not reach the port")
	}

	page1 := serve("/decision-history?limit=1&rule_id=tenant.requests", authorityAdapterHTTPSecret)
	if page1.Code != http.StatusOK {
		t.Fatalf("page1 status=%d body=%s", page1.Code, page1.Body.String())
	}
	var page1Body adaptedAuthorityPage[cp.AccountingDecisionRow]
	if err := json.Unmarshal(page1.Body.Bytes(), &page1Body); err != nil {
		t.Fatalf("decode page1: %v body=%s", err, page1.Body.String())
	}
	if page1Body.State != usagelimit.QueryStateReady {
		t.Fatalf("page1 state=%q body=%s", page1Body.State, page1.Body.String())
	}
	if len(page1Body.Page.Items) != 1 || page1Body.Page.Items[0].RuleID != "tenant.requests" {
		t.Fatalf("page1 items=%+v body=%s", page1Body.Page.Items, page1.Body.String())
	}
	if page1Body.Page.Next.Token != "cursor-1" {
		t.Fatalf("page1 next cursor missing: %s", page1.Body.String())
	}
	if got := port.lastDecisionQuery.Cursor.Token; got != "" {
		t.Fatalf("first page carried cursor %q", got)
	}

	page2 := serve("/decision-history?limit=1&rule_id=tenant.requests&cursor=cursor-1", authorityAdapterHTTPSecret)
	if page2.Code != http.StatusOK {
		t.Fatalf("page2 status=%d body=%s", page2.Code, page2.Body.String())
	}
	var page2Body adaptedAuthorityPage[cp.AccountingDecisionRow]
	if err := json.Unmarshal(page2.Body.Bytes(), &page2Body); err != nil {
		t.Fatalf("decode page2: %v body=%s", err, page2.Body.String())
	}
	if page2Body.State != usagelimit.QueryStateReady || page2Body.Page.Next.Token != "" {
		t.Fatalf("page2 state=%q next=%q body=%s", page2Body.State, page2Body.Page.Next.Token, page2.Body.String())
	}
	if got := port.lastDecisionQuery.Cursor.Token; got != "cursor-1" {
		t.Fatalf("continuation cursor not passed through: %q", got)
	}
	if got := port.lastDecisionQuery.RuleID; got != "tenant.requests" {
		t.Fatalf("rule filter not passed through: %q", got)
	}
	if got := port.lastDecisionQuery.Limit; got != 1 {
		t.Fatalf("limit not passed through: %d", got)
	}

	// A bounded unsupported outcome (state + field) must survive the adapter.
	unsupported := serve("/limits?limit=1&rule_id=tenant.requests&settlement_state=settled", authorityAdapterHTTPSecret)
	if unsupported.Code != http.StatusOK {
		t.Fatalf("unsupported status=%d body=%s", unsupported.Code, unsupported.Body.String())
	}
	var unsupportedBody adaptedAuthorityPage[cp.AccountingLimitStatusRow]
	if err := json.Unmarshal(unsupported.Body.Bytes(), &unsupportedBody); err != nil {
		t.Fatalf("decode unsupported: %v body=%s", err, unsupported.Body.String())
	}
	if unsupportedBody.State != usagelimit.QueryStateUnsupported ||
		len(unsupportedBody.Page.Unsupported) != 1 || unsupportedBody.Page.Unsupported[0].Field != "settlement_state" {
		t.Fatalf("unsupported body=%s", unsupported.Body.String())
	}
	if got := port.lastLimitQuery.SettlementState; got != cp.AccountingSettlementState("settled") {
		t.Fatalf("settlement filter not passed through: %q", got)
	}
}
