package runtimebundle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	cpadmin "github.com/aiproxer/aiproxer/internal/stdhttp/admin/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// fakeAuthorityPort implements only the four-method usage-limit port, with no
// optional host surfaces.
type fakeAuthorityPort struct{}

var _ usagelimit.Authority = fakeAuthorityPort{}

func (fakeAuthorityPort) Admit(context.Context, usagelimit.AdmissionInput) (usagelimit.AdmissionResult, error) {
	return usagelimit.AdmissionResult{Allowed: true, Outcome: usagelimit.DecisionOutcomeAllow}, nil
}

func (fakeAuthorityPort) Settle(context.Context, usagelimit.SettleInput) (usagelimit.SettleResult, error) {
	return usagelimit.SettleResult{}, nil
}

func (fakeAuthorityPort) Release(context.Context, usagelimit.ReleaseInput) (usagelimit.ReleaseResult, error) {
	return usagelimit.ReleaseResult{}, nil
}

func (fakeAuthorityPort) ApplyUsage(context.Context, usagelimit.ApplyUsageCommand) (usagelimit.ApplyUsageResult, error) {
	return usagelimit.ApplyUsageResult{}, nil
}

// fakeAuthorityQueries implements the four-method port plus the optional
// status/query surfaces; it records the bounded queries it receives so the
// HTTP pass-through (filters, cursors, states) is asserted on what the port
// actually saw.
type fakeAuthorityQueries struct {
	fakeAuthorityPort

	status        cp.AccountingAuthorityStatus
	limitState    usagelimit.QueryState
	limitPage     cp.Page[cp.AccountingLimitStatusRow]
	decisionState usagelimit.QueryState
	decisionPage  cp.Page[cp.AccountingDecisionRow]
	// decisionNextPage is returned when the query carries the continuation
	// cursor, so two-page pass-through is exercised.
	decisionNextPage cp.Page[cp.AccountingDecisionRow]

	statusCalls            int
	lastLimitQuery         cp.AccountingLimitStatusQuery
	lastDecisionQuery      cp.AccountingDecisionQuery
	limitCallsPerformed    int
	decisionCallsPerformed int
}

var (
	_ usagelimit.Authority               = (*fakeAuthorityQueries)(nil)
	_ cpadmin.AccountingAuthorityQueries = (*fakeAuthorityQueries)(nil)
)

func (f *fakeAuthorityQueries) Status(context.Context) (cp.AccountingAuthorityStatus, error) {
	f.statusCalls++
	return f.status, nil
}

func (f *fakeAuthorityQueries) Limits(_ context.Context, q cp.AccountingLimitStatusQuery) (usagelimit.LimitStatusResult, error) {
	f.limitCallsPerformed++
	f.lastLimitQuery = q
	return usagelimit.LimitStatusResult{State: f.limitState, Page: f.limitPage}, nil
}

func (f *fakeAuthorityQueries) Decisions(_ context.Context, q cp.AccountingDecisionQuery) (usagelimit.DecisionHistoryResult, error) {
	f.decisionCallsPerformed++
	f.lastDecisionQuery = q
	page := f.decisionPage
	if q.Cursor.Token != "" {
		page = f.decisionNextPage
	}
	return usagelimit.DecisionHistoryResult{State: f.decisionState, Page: page}, nil
}

// Task 4.2/5.3: the accounting-authority adapter projects the injected port
// onto the narrow stdhttp contract. A nil or non-query port leaves the
// optional authority routes disabled; a query-capable port passes through.

func TestAdaptAccountingAuthorityQueries_nilPortIsNil(t *testing.T) {
	t.Parallel()
	if adaptAccountingAuthorityQueries(nil) != nil {
		t.Fatal("nil port must adapt to nil AccountingAuthorityQueries")
	}
}

func TestAdaptAccountingAuthorityQueries_nonQueryPortLeavesRoutesDisabled(t *testing.T) {
	t.Parallel()
	queries := adaptAccountingAuthorityQueries(fakeAuthorityPort{})
	if queries != nil {
		t.Fatalf("non-query port adapted to %T, want nil (routes disabled)", queries)
	}
	h := cpadmin.NewAccountingAuthorityHandler(cpadmin.AuthorityOptions{Queries: queries})
	for _, path := range []string{"/", "/status", "/limits", "/decision-history"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s: status=%d want 404; body=%s", path, rr.Code, rr.Body.String())
		}
	}
}

func TestAdaptAccountingAuthorityQueries_queryPortPassesThrough(t *testing.T) {
	t.Parallel()
	port := &fakeAuthorityQueries{}
	queries := adaptAccountingAuthorityQueries(port)
	if queries == nil {
		t.Fatal("query-capable port must adapt to non-nil AccountingAuthorityQueries")
	}
	if _, ok := queries.(*fakeAuthorityQueries); !ok {
		t.Fatalf("identity projection changed the port type: %T", queries)
	}
	if _, err := queries.Status(context.Background()); err != nil {
		t.Fatalf("status through the adapter: %v", err)
	}
	if port.statusCalls != 1 {
		t.Fatalf("status calls=%d want 1 (identity projection)", port.statusCalls)
	}
}
