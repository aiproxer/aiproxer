package stdhttp

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	cpadmin "github.com/aiproxer/aiproxer/internal/stdhttp/admin/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// Task 4.2: the OSS HTTP layer mounts the accounting-authority query surface
// through the narrow [cpadmin.AccountingAuthorityQueries] port. These tests
// drive it with a bounded paging fake instead of a real usage-authority
// service (the concrete service -> adapter -> HTTP preservation test lives in
// runtimebundle) while retaining the original wire assertions: auth, path,
// decoded decision pages with first/last cursors, the limits
// unsupported-settlement outcome, request projection (including
// settlement/time filters), error mapping and the leak scan.

// authorityFakeNextToken is the continuation the fake serves while more rows
// remain, so the test round-trips a real cursor through the wire.
const authorityFakeNextToken = "authority-page-2-token"

type authorityFakeQueries struct {
	status        cp.AccountingAuthorityStatus
	statusErr     error
	limits        cpadmin.LimitStatusResult
	limitsErr     error
	lastLimits    cp.AccountingLimitStatusQuery
	lastDecisions cp.AccountingDecisionQuery
	limitsCalls   int
}

func (f *authorityFakeQueries) Status(context.Context) (cp.AccountingAuthorityStatus, error) {
	return f.status, f.statusErr
}

func (f *authorityFakeQueries) Limits(_ context.Context, q cp.AccountingLimitStatusQuery) (cpadmin.LimitStatusResult, error) {
	f.lastLimits = q
	f.limitsCalls++
	// The live service reports a bounded unsupported outcome (not an error)
	// for filters it cannot honour; compute it from the projected query so the
	// wire assertion exercises the handler's request projection.
	if string(q.SettlementState) != "" {
		return cpadmin.LimitStatusResult{
			State: cpadmin.QueryStateUnsupported,
			Page:  cp.Page[cp.AccountingLimitStatusRow]{Unsupported: []cp.UnsupportedFilter{{Field: "settlement_state"}}},
		}, nil
	}
	if f.limitsErr != nil {
		return cpadmin.LimitStatusResult{}, f.limitsErr
	}
	return f.limits, nil
}

func (f *authorityFakeQueries) Decisions(_ context.Context, q cp.AccountingDecisionQuery) (cpadmin.DecisionHistoryResult, error) {
	f.lastDecisions = q
	if !q.Common.TimeRange.From.IsZero() || !q.Common.TimeRange.To.IsZero() {
		return cpadmin.DecisionHistoryResult{
			State: cpadmin.QueryStateUnsupported,
			Page:  cp.Page[cp.AccountingDecisionRow]{Unsupported: []cp.UnsupportedFilter{{Field: "time_range"}}},
		}, nil
	}
	limitRow := authorityHTTPLimitRow()
	row := cp.AccountingDecisionRow{
		Correlation: limitRow.Correlation,
		Scope:       limitRow.Scope,
		RuleID:      "tenant.requests",
	}
	if q.Cursor.Token == "" {
		return cpadmin.DecisionHistoryResult{
			State: cpadmin.QueryStateReady,
			Page: cp.Page[cp.AccountingDecisionRow]{
				Items: []cp.AccountingDecisionRow{row},
				Next:  cp.Cursor{Token: authorityFakeNextToken},
			},
		}, nil
	}
	return cpadmin.DecisionHistoryResult{
		State: cpadmin.QueryStateReady,
		Page:  cp.Page[cp.AccountingDecisionRow]{Items: []cp.AccountingDecisionRow{row}},
	}, nil
}

var _ cpadmin.AccountingAuthorityQueries = (*authorityFakeQueries)(nil)

type authorityPageResponse[T any] struct {
	State cpadmin.QueryState `json:"state"`
	Page  cp.Page[T]         `json:"page"`
}

func authorityHTTPConfig(enabled bool) *config.Config {
	return &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    config.RoutingConfig{DefaultRoute: "stub:model", MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: config.DiagnosticsConfig{
			SharedSecret: strings.Repeat("s", 12),
		},
		Accounting: config.AccountingConfig{
			Authority: config.AccountingAuthorityConfig{
				Enabled:        enabled,
				Mode:           "strict",
				Store:          "memory",
				StartupPosture: "fail_closed",
				Query: config.AccountingAuthorityQueryConfig{
					Enabled:         enabled,
					PathPrefix:      "/authority",
					DefaultPageSize: 25,
					MaxPageSize:     50,
				},
			},
		},
	}
}

func authorityHTTPInput(t *testing.T, cfg *config.Config, queries cpadmin.AccountingAuthorityQueries) StandardHTTPInput {
	t.Helper()
	ex := runtime.TestExecutor()
	reg := pluginreg.NewRegistry()
	return StandardHTTPInput{
		Core:      HTTPCoreInput{Executor: ex},
		Frontends: frontendInputForTest(cfg, ex, reg),
		Security:  HTTPSecurityInput{UsageAuthority: queries},
	}
}

func authorityReadyFake() *authorityFakeQueries {
	return &authorityFakeQueries{
		status: cp.AccountingAuthorityStatus{State: cp.AccountingAuthorityReady},
		limits: cpadmin.LimitStatusResult{
			State: cpadmin.QueryStateReady,
			Page:  cp.Page[cp.AccountingLimitStatusRow]{Items: []cp.AccountingLimitStatusRow{authorityHTTPLimitRow()}},
		},
	}
}

func TestAccountingAuthorityQueryNotMountedWhenDisabled(t *testing.T) {
	t.Parallel()
	cfg := authorityHTTPConfig(false)
	in := authorityHTTPInput(t, cfg, authorityReadyFake())
	app := mustRuntimeApp(t, cfg)
	ctx := context.Background()
	startTestApp(ctx, t, app)
	h, err := ComposeStandardHTTP(ctx, cfg, slog.Default(), in)
	if err != nil {
		t.Fatalf("ComposeStandardHTTP: %v", err)
	}

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/authority/status", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestAccountingAuthorityQueryMountedAndProtected(t *testing.T) {
	t.Parallel()
	cfg := authorityHTTPConfig(true)
	queries := authorityReadyFake()
	in := authorityHTTPInput(t, cfg, queries)
	app := mustRuntimeApp(t, cfg)
	ctx := context.Background()
	startTestApp(ctx, t, app)
	h, err := ComposeStandardHTTP(ctx, cfg, slog.Default(), in)
	if err != nil {
		t.Fatalf("ComposeStandardHTTP: %v", err)
	}
	secret := cfg.Diagnostics.SharedSecret

	missing := httptest.NewRecorder()
	h.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/authority/status", nil))
	if missing.Code != http.StatusForbidden {
		t.Fatalf("missing secret status = %d body=%s", missing.Code, missing.Body.String())
	}

	okReq := httptest.NewRequest(http.MethodGet, "/authority/status", nil)
	okReq.Header.Set(diag.HeaderDiagnosticsSecret, secret)
	ok := httptest.NewRecorder()
	h.ServeHTTP(ok, okReq)
	if ok.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", ok.Code, ok.Body.String())
	}
	if !strings.Contains(ok.Body.String(), `"state":"ready"`) {
		t.Fatalf("status body missing ready state: %s", ok.Body.String())
	}
	assertNoUnsafeAuthorityBody(t, ok.Body.String())

	// Decision-history page 1 decodes its bounded page: ready state, one row
	// and a continuation cursor.
	page1Req := httptest.NewRequest(http.MethodGet, "/authority/decision-history?limit=1&rule_id=tenant.requests", nil)
	page1Req.Header.Set(diag.HeaderDiagnosticsSecret, secret)
	page1 := httptest.NewRecorder()
	h.ServeHTTP(page1, page1Req)
	if page1.Code != http.StatusOK {
		t.Fatalf("page1 status = %d body=%s", page1.Code, page1.Body.String())
	}
	page1Body := decodeAuthorityPage[cp.AccountingDecisionRow](t, page1.Body.Bytes())
	if page1Body.State != cpadmin.QueryStateReady {
		t.Fatalf("page1 state = %q body=%s", page1Body.State, page1.Body.String())
	}
	if len(page1Body.Page.Items) != 1 || page1Body.Page.Items[0].RuleID != "tenant.requests" {
		t.Fatalf("page1 items = %+v body=%s", page1Body.Page.Items, page1.Body.String())
	}
	if page1Body.Page.Next.Token != authorityFakeNextToken {
		t.Fatalf("page1 next cursor = %q want %q body=%s", page1Body.Page.Next.Token, authorityFakeNextToken, page1.Body.String())
	}
	if queries.lastDecisions.Limit != 1 || queries.lastDecisions.RuleID != "tenant.requests" || queries.lastDecisions.Cursor.Token != "" {
		t.Fatalf("page1 projected query = %+v", queries.lastDecisions)
	}
	assertNoUnsafeAuthorityBody(t, page1.Body.String())

	// Page 2 consumes the cursor, serves the final row and no further cursor.
	page2Req := httptest.NewRequest(http.MethodGet, "/authority/decision-history?limit=1&rule_id=tenant.requests&cursor="+authorityFakeNextToken, nil)
	page2Req.Header.Set(diag.HeaderDiagnosticsSecret, secret)
	page2 := httptest.NewRecorder()
	h.ServeHTTP(page2, page2Req)
	if page2.Code != http.StatusOK {
		t.Fatalf("page2 status = %d body=%s", page2.Code, page2.Body.String())
	}
	page2Body := decodeAuthorityPage[cp.AccountingDecisionRow](t, page2.Body.Bytes())
	if page2Body.State != cpadmin.QueryStateReady {
		t.Fatalf("page2 state = %q body=%s", page2Body.State, page2.Body.String())
	}
	if len(page2Body.Page.Items) != 1 || page2Body.Page.Items[0].RuleID != "tenant.requests" {
		t.Fatalf("page2 items = %+v body=%s", page2Body.Page.Items, page2.Body.String())
	}
	if page2Body.Page.Next.Token != "" {
		t.Fatalf("page2 next cursor = %q want empty body=%s", page2Body.Page.Next.Token, page2.Body.String())
	}
	if queries.lastDecisions.Cursor.Token != authorityFakeNextToken {
		t.Fatalf("page2 projected cursor = %q want %q", queries.lastDecisions.Cursor.Token, authorityFakeNextToken)
	}
	assertNoUnsafeAuthorityBody(t, page2.Body.String())

	// The bounded limits query projects its filter values once and serves the
	// ready page with the retained row.
	limitsReq := httptest.NewRequest(http.MethodGet, "/authority/limits?limit=1&rule_id=tenant.requests&unit=requests", nil)
	limitsReq.Header.Set(diag.HeaderDiagnosticsSecret, secret)
	limitsRec := httptest.NewRecorder()
	h.ServeHTTP(limitsRec, limitsReq)
	if limitsRec.Code != http.StatusOK {
		t.Fatalf("limits status = %d body=%s", limitsRec.Code, limitsRec.Body.String())
	}
	limitsBody := decodeAuthorityPage[cp.AccountingLimitStatusRow](t, limitsRec.Body.Bytes())
	if limitsBody.State != cpadmin.QueryStateReady {
		t.Fatalf("limits state = %q body=%s", limitsBody.State, limitsRec.Body.String())
	}
	if len(limitsBody.Page.Items) != 1 || limitsBody.Page.Items[0].RuleID != "tenant.requests" {
		t.Fatalf("limits items = %+v body=%s", limitsBody.Page.Items, limitsRec.Body.String())
	}
	if queries.limitsCalls != 1 {
		t.Fatalf("limits port calls = %d want 1", queries.limitsCalls)
	}
	if queries.lastLimits.Limit != 1 || queries.lastLimits.RuleID != "tenant.requests" ||
		queries.lastLimits.Unit != "requests" || queries.lastLimits.Cursor.Token != "" {
		t.Fatalf("projected limits query = %+v", queries.lastLimits)
	}
	assertNoUnsafeAuthorityBody(t, limitsRec.Body.String())

	// A settlement-state filter projects into the query and reports the
	// bounded unsupported state naming the field, never a served page.
	limitsUnsupportedReq := httptest.NewRequest(http.MethodGet, "/authority/limits?limit=1&rule_id=tenant.requests&settlement_state=settled", nil)
	limitsUnsupportedReq.Header.Set(diag.HeaderDiagnosticsSecret, secret)
	limitsUnsupported := httptest.NewRecorder()
	h.ServeHTTP(limitsUnsupported, limitsUnsupportedReq)
	if limitsUnsupported.Code != http.StatusOK {
		t.Fatalf("limits unsupported status = %d body=%s", limitsUnsupported.Code, limitsUnsupported.Body.String())
	}
	limitsUnsupportedBody := decodeAuthorityPage[cp.AccountingLimitStatusRow](t, limitsUnsupported.Body.Bytes())
	if limitsUnsupportedBody.State != cpadmin.QueryStateUnsupported {
		t.Fatalf("limits unsupported state = %q body=%s", limitsUnsupportedBody.State, limitsUnsupported.Body.String())
	}
	if len(limitsUnsupportedBody.Page.Unsupported) != 1 || limitsUnsupportedBody.Page.Unsupported[0].Field != "settlement_state" {
		t.Fatalf("limits unsupported filters = %#v body=%s", limitsUnsupportedBody.Page.Unsupported, limitsUnsupported.Body.String())
	}
	if string(queries.lastLimits.SettlementState) != "settled" {
		t.Fatalf("projected settlement filter = %q want settled", queries.lastLimits.SettlementState)
	}
	assertNoUnsafeAuthorityBody(t, limitsUnsupported.Body.String())

	// A time-range filter on decision history projects into the query range
	// and reports the bounded unsupported state naming the field.
	unsupportedReq := httptest.NewRequest(http.MethodGet, "/authority/decision-history?limit=1&rule_id=tenant.requests&from=2026-01-01T00:00:00Z", nil)
	unsupportedReq.Header.Set(diag.HeaderDiagnosticsSecret, secret)
	unsupported := httptest.NewRecorder()
	h.ServeHTTP(unsupported, unsupportedReq)
	if unsupported.Code != http.StatusOK {
		t.Fatalf("unsupported status = %d body=%s", unsupported.Code, unsupported.Body.String())
	}
	unsupportedBody := decodeAuthorityPage[cp.AccountingDecisionRow](t, unsupported.Body.Bytes())
	if unsupportedBody.State != cpadmin.QueryStateUnsupported {
		t.Fatalf("unsupported state = %q body=%s", unsupportedBody.State, unsupported.Body.String())
	}
	if len(unsupportedBody.Page.Unsupported) != 1 || unsupportedBody.Page.Unsupported[0].Field != "time_range" {
		t.Fatalf("unsupported filters = %#v body=%s", unsupportedBody.Page.Unsupported, unsupported.Body.String())
	}
	if queries.lastDecisions.Common.TimeRange.From.IsZero() {
		t.Fatalf("projected time range = %+v want non-zero from", queries.lastDecisions.Common.TimeRange)
	}
	assertNoUnsafeAuthorityBody(t, unsupported.Body.String())

	// Handler-level query validation stays ahead of the port.
	callsBefore := queries.limitsCalls
	invalidReq := httptest.NewRequest(http.MethodGet, "/authority/decision-history?limit=bad", nil)
	invalidReq.Header.Set(diag.HeaderDiagnosticsSecret, secret)
	invalid := httptest.NewRecorder()
	h.ServeHTTP(invalid, invalidReq)
	assertSafeAuthorityJSONError(t, invalid, http.StatusBadRequest, "invalid_query")

	tooBroadReq := httptest.NewRequest(http.MethodGet, "/authority/limits?limit=101", nil)
	tooBroadReq.Header.Set(diag.HeaderDiagnosticsSecret, secret)
	tooBroad := httptest.NewRecorder()
	h.ServeHTTP(tooBroad, tooBroadReq)
	assertSafeAuthorityJSONError(t, tooBroad, http.StatusBadRequest, "too_broad")
	if queries.limitsCalls != callsBefore {
		t.Fatalf("validation failures must not reach the port: calls=%d want %d", queries.limitsCalls, callsBefore)
	}

	// Port errors map to stable, leak-free wire errors.
	queries.limitsErr = usagelimit.ErrDegraded
	degradedReq := httptest.NewRequest(http.MethodGet, "/authority/limits?limit=1", nil)
	degradedReq.Header.Set(diag.HeaderDiagnosticsSecret, secret)
	degraded := httptest.NewRecorder()
	h.ServeHTTP(degraded, degradedReq)
	assertSafeAuthorityJSONError(t, degraded, http.StatusServiceUnavailable, "degraded")

	queries.limitsErr = usagelimit.ErrUnavailable
	unavailableReq := httptest.NewRequest(http.MethodGet, "/authority/limits?limit=1", nil)
	unavailableReq.Header.Set(diag.HeaderDiagnosticsSecret, secret)
	unavailable := httptest.NewRecorder()
	h.ServeHTTP(unavailable, unavailableReq)
	assertSafeAuthorityJSONError(t, unavailable, http.StatusServiceUnavailable, "unavailable")

	queries.limitsErr = usagelimit.ErrUnsupportedFilter
	unsupportedFilterReq := httptest.NewRequest(http.MethodGet, "/authority/limits?limit=1", nil)
	unsupportedFilterReq.Header.Set(diag.HeaderDiagnosticsSecret, secret)
	unsupportedFilter := httptest.NewRecorder()
	h.ServeHTTP(unsupportedFilter, unsupportedFilterReq)
	assertSafeAuthorityJSONError(t, unsupportedFilter, http.StatusBadRequest, "unsupported_filter")
}

func authorityHTTPLimitRow() cp.AccountingLimitStatusRow {
	return cp.AccountingLimitStatusRow{
		Correlation: cp.Correlation{
			TraceID:    "trace-1",
			RequestID:  "request-1",
			SessionID:  "session-1",
			ALegID:     "a-1",
			BLegID:     "b-1",
			AttemptSeq: 1,
			BackendID:  "backend-1",
			Model:      "model-1",
		},
		Scope: cp.ScopeSnapshot{
			Principal: scope.PrincipalScopeView{
				SubjectKind:    scope.SubjectUnknown,
				Origin:         scope.OriginInternal,
				PrincipalID:    scope.Known("principal-1"),
				TenantID:       scope.Known("tenant-1"),
				OrganizationID: scope.Known("organization-1"),
				WorkspaceID:    scope.Known("workspace-1"),
				ProjectID:      scope.Known("project-1"),
				DepartmentID:   scope.Known("department-1"),
				CostCenterID:   scope.Known("cost-center-1"),
			},
			PrincipalID:    scope.Known("principal-1"),
			CredentialID:   scope.Unknown(),
			TenantID:       scope.Known("tenant-1"),
			OrganizationID: scope.Known("organization-1"),
			WorkspaceID:    scope.Known("workspace-1"),
			ProjectID:      scope.Known("project-1"),
			DepartmentID:   scope.Known("department-1"),
			CostCenterID:   scope.Known("cost-center-1"),
		},
		RuleID:         "tenant.requests",
		RuleType:       "quota",
		Unit:           "requests",
		Limit:          10,
		Remaining:      10,
		Authority:      cp.AccountingAuthoritySourceAuthoritative,
		EvidenceState:  cp.EvidenceRecorded,
		RedactionState: cp.RedactionSummarized,
	}
}

func decodeAuthorityPage[T any](t *testing.T, raw []byte) authorityPageResponse[T] {
	t.Helper()
	var out authorityPageResponse[T]
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode authority page: %v body=%s", err, string(raw))
	}
	return out
}

func assertSafeAuthorityJSONError(t *testing.T, rr *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	if rr.Code != wantStatus {
		t.Fatalf("status = %d, want %d body=%s", rr.Code, wantStatus, rr.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v body=%s", err, rr.Body.String())
	}
	if len(body) != 1 || body["error"] != wantCode {
		t.Fatalf("error body = %#v, want code %q", body, wantCode)
	}
	for _, leak := range []string{"authoritystore", "matching limit", "limit must be positive", "backend not available"} {
		if strings.Contains(rr.Body.String(), leak) {
			t.Fatalf("error body leaked raw detail %q: %s", leak, rr.Body.String())
		}
	}
	assertNoUnsafeAuthorityBody(t, rr.Body.String())
}

func assertNoUnsafeAuthorityBody(t *testing.T, body string) {
	t.Helper()

	low := strings.ToLower(body)
	for _, leak := range []string{
		"bearer ",
		"api key",
		"api-key",
		"oauth",
		"resume token",
		"resume_token",
		"prompt",
		"response",
		"provider payload",
		"raw payload",
		"raw body",
		"raw headers",
		"authorization:",
		"sql",
		"driver",
		"dsn",
	} {
		if strings.Contains(low, leak) {
			t.Fatalf("authority response leaked forbidden substring %q: %s", leak, body)
		}
	}
}
