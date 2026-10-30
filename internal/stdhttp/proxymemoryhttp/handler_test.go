package proxymemoryhttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	proxymemoryhttp "github.com/aiproxer/aiproxer/internal/stdhttp/proxymemoryhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

// The HTTP fixtures: two eligible principals, one ineligible principal and
// opaque 64-hex scope identifiers, the exact shape the service validates.
const (
	httpTestTenant         = "tenant-http"
	httpTestPrincipal      = "alice-http"
	httpTestOtherPrincipal = "bob-http"
	httpTestIneligible     = "mallory-http"

	httpTestScope        = "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
	httpTestOtherScope   = "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2"
	httpTestUnknownScope = "c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3"
)

// httpTestPublishedAtMS is 2023-11-14T22:13:20Z, the RFC 3339 rendering the
// REST bank statuses must use.
const httpTestPublishedAtMS = int64(1_700_000_000_000)

// httpTestBankKeys is the exact key set of one bank entry of the bank list
// (design §11.2).
var httpTestBankKeys = []string{
	"scope", "project_label", "version", "published_at",
	"pending_sessions", "extractions", "pending_notes",
}

// testOwner identifies one seeded bank inside the fake store.
type testOwner struct {
	principal string
	scope     string
}

// fakeStore is the in-memory proxymemory.Store of the HTTP tests. The embedded
// Store stays nil, so any method outside the surface these tests exercise
// panics loudly instead of silently passing. Every method predicates on the
// owner, so another principal's bank behaves exactly like an unknown one.
type fakeStore struct {
	proxymemory.Store

	tenant string

	banks     map[testOwner]proxymemory.BankStatus
	summaries map[testOwner]string
	documents map[testOwner]proxymemory.Documents

	listPrincipals []proxymemory.Principal

	addedNotes []struct {
		owner testOwner
		note  proxymemory.Note
	}

	bankStatusErr error
	documentsErr  error

	// settings holds the stored owner-scoped settings, keyed by the owner's
	// tenant and principal. The settings key holds no project, so the map has
	// no scope dimension.
	settings    map[proxymemory.Principal]proxymemory.StoredOwnerSettings
	settingsErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		tenant:    httpTestTenant,
		banks:     map[testOwner]proxymemory.BankStatus{},
		summaries: map[testOwner]string{},
		documents: map[testOwner]proxymemory.Documents{},
	}
}

// seedBank gives one principal a bank under one scope identifier.
func (s *fakeStore) seedBank(principal, scopeID string, version int64) {
	s.banks[testOwner{principal: principal, scope: scopeID}] = proxymemory.BankStatus{
		ScopeID:         scopeID,
		ProjectLabel:    "/w/" + principal,
		Version:         version,
		PublishedAtMS:   httpTestPublishedAtMS,
		PendingSessions: 2,
		Extractions:     3,
		PendingNotes:    4,
	}
}

func (s *fakeStore) key(o proxymemory.Owner) testOwner {
	return testOwner{principal: o.PrincipalID, scope: o.ProjectKey}
}

func (s *fakeStore) ListBanks(_ context.Context, p proxymemory.Principal) ([]proxymemory.BankStatus, error) {
	s.listPrincipals = append(s.listPrincipals, p)
	banks := make([]proxymemory.BankStatus, 0)
	for key, bank := range s.banks {
		if key.principal == p.PrincipalID && p.TenantID == s.tenant {
			banks = append(banks, bank)
		}
	}
	sort.Slice(banks, func(i, j int) bool { return banks[i].ScopeID < banks[j].ScopeID })
	return banks, nil
}

func (s *fakeStore) BankStatus(_ context.Context, o proxymemory.Owner) (proxymemory.BankStatus, error) {
	if s.bankStatusErr != nil {
		return proxymemory.BankStatus{}, s.bankStatusErr
	}
	bank, ok := s.banks[s.key(o)]
	if !ok {
		return proxymemory.BankStatus{}, proxymemory.ErrNotFound
	}
	return bank, nil
}

func (s *fakeStore) PublishedSummary(_ context.Context, o proxymemory.Owner) (string, int64, error) {
	summary, ok := s.summaries[s.key(o)]
	if !ok {
		return "", 0, proxymemory.ErrNotFound
	}
	return summary, 1, nil
}

func (s *fakeStore) Documents(_ context.Context, o proxymemory.Owner) (proxymemory.Documents, error) {
	if s.documentsErr != nil {
		return proxymemory.Documents{}, s.documentsErr
	}
	docs, ok := s.documents[s.key(o)]
	if !ok {
		return proxymemory.Documents{}, proxymemory.ErrNotFound
	}
	return docs, nil
}

func (s *fakeStore) AddNote(_ context.Context, o proxymemory.Owner, n proxymemory.Note) error {
	key := s.key(o)
	if _, ok := s.banks[key]; !ok {
		return proxymemory.ErrNotFound
	}
	s.addedNotes = append(s.addedNotes, struct {
		owner testOwner
		note  proxymemory.Note
	}{key, n})
	return nil
}

func (s *fakeStore) Forget(_ context.Context, o proxymemory.Owner, _ int64) error {
	key := s.key(o)
	if _, ok := s.banks[key]; !ok {
		return proxymemory.ErrNotFound
	}
	delete(s.banks, key)
	delete(s.summaries, key)
	delete(s.documents, key)
	return nil
}

// ReadSettings returns the owner's stored settings. An owner who never wrote
// one has empty settings rather than a missing row.
func (s *fakeStore) ReadSettings(_ context.Context, p proxymemory.Principal) (proxymemory.StoredOwnerSettings, error) {
	if s.settingsErr != nil {
		return proxymemory.StoredOwnerSettings{}, s.settingsErr
	}
	return s.settings[p], nil
}

// WriteSettings replaces the owner's stored settings.
func (s *fakeStore) WriteSettings(_ context.Context, p proxymemory.Principal, value proxymemory.StoredOwnerSettings) error {
	if s.settingsErr != nil {
		return s.settingsErr
	}
	if s.settings == nil {
		s.settings = map[proxymemory.Principal]proxymemory.StoredOwnerSettings{}
	}
	s.settings[p] = value
	return nil
}

// DeleteSettings removes the owner's stored settings.
func (s *fakeStore) DeleteSettings(_ context.Context, p proxymemory.Principal) error {
	if s.settingsErr != nil {
		return s.settingsErr
	}
	if _, ok := s.settings[p]; !ok {
		return proxymemory.ErrNotFound
	}
	delete(s.settings, p)
	return nil
}

// newTestService builds a real Service over the fake store with the two
// eligible fixtures allowlisted and the documented limits.
func newTestService(t *testing.T, store proxymemory.Store, metrics *proxymemory.Metrics) *proxymemory.Service {
	t.Helper()
	cfg := proxymemory.DefaultConfig()
	cfg.Principals = []string{httpTestPrincipal, httpTestOtherPrincipal}
	cfg.ExtractRoute = "route-extract"
	cfg.ConsolidateRoute = "route-consolidate"
	return proxymemory.NewService(cfg, store, metrics, func() time.Time {
		return time.UnixMilli(1_800_000_000_000).UTC()
	})
}

// scopedRequest builds one request carrying the authoritative scope snapshot
// the auth middleware attaches in production.
func scopedRequest(method, target string, body io.Reader, principal string) *http.Request {
	req := httptest.NewRequest(method, target, body)
	return req.WithContext(httpauth.WithScope(req.Context(), scope.PrincipalScopeView{
		PrincipalID: scope.Known(principal),
		TenantID:    scope.Known(httpTestTenant),
	}))
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// decodeBody decodes one JSON response body into dest.
func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, dest any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dest); err != nil {
		t.Fatalf("response body %q is not valid JSON: %v", rec.Body.String(), err)
	}
}

// requireErrorBody requires the exact content-free error document.
func requireErrorBody(t *testing.T, rec *httptest.ResponseRecorder, code string) {
	t.Helper()
	want := `{"error":"` + code + `"}` + "\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

// requireKeys requires that a decoded JSON object carries exactly want.
func requireKeys(t *testing.T, raw json.RawMessage, want []string) map[string]json.RawMessage {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("object %s is not valid JSON: %v", raw, err)
	}
	got := make([]string, 0, len(obj))
	for key := range obj {
		got = append(got, key)
	}
	sort.Strings(got)
	sorted := append([]string(nil), want...)
	sort.Strings(sorted)
	if strings.Join(got, ",") != strings.Join(sorted, ",") {
		t.Fatalf("object keys = %v, want %v", got, want)
	}
	return obj
}

func TestNewHandlerNil(t *testing.T) {
	if got := proxymemoryhttp.NewHandler(nil); got != nil {
		t.Fatalf("NewHandler(nil) = %#v, want nil", got)
	}
}

// TestHandlerUnauthenticated pins the missing-scope 401 of design §11.2. The
// check precedes routing, so every route answers the same way.
func TestHandlerUnauthenticated(t *testing.T) {
	h := proxymemoryhttp.NewHandler(newTestService(t, newFakeStore(), nil))
	cases := []struct {
		method string
		target string
	}{
		{http.MethodGet, "/v1/aip/memory"},
		{http.MethodGet, "/v1/aip/memory/" + httpTestScope},
		{http.MethodDelete, "/v1/aip/memory/" + httpTestScope},
		{http.MethodGet, "/v1/aip/memory/settings"},
		{http.MethodPut, "/v1/aip/memory/settings"},
		{http.MethodDelete, "/v1/aip/memory/settings"},
		{http.MethodPost, "/v1/aip/memory/mcp"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
			rec := serve(h, httptest.NewRequest(tc.method, tc.target, nil))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
			requireErrorBody(t, rec, "unauthenticated")
		})
	}
}

// TestHandlerIneligiblePrincipalIsNotFoundOnEveryRoute pins Requirement 7.6:
// an authenticated but non-eligible principal cannot probe or use memory.
func TestHandlerIneligiblePrincipalIsNotFoundOnEveryRoute(t *testing.T) {
	store := newFakeStore()
	store.seedBank(httpTestPrincipal, httpTestScope, 1)
	h := proxymemoryhttp.NewHandler(newTestService(t, store, nil))
	cases := []struct {
		method string
		target string
	}{
		{http.MethodGet, "/v1/aip/memory"},
		{http.MethodGet, "/v1/aip/memory/" + httpTestScope},
		{http.MethodDelete, "/v1/aip/memory/" + httpTestScope},
		{http.MethodGet, "/v1/aip/memory/settings"},
		{http.MethodPut, "/v1/aip/memory/settings"},
		{http.MethodDelete, "/v1/aip/memory/settings"},
		{http.MethodPost, "/v1/aip/memory/mcp"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
			rec := serve(h, scopedRequest(tc.method, tc.target, nil, httpTestIneligible))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
			}
			requireErrorBody(t, rec, "not_found")
		})
	}
}

// TestHandlerListBanks pins the caller-scoped list of design §11.2 and the
// exact bank field set.
func TestHandlerListBanks(t *testing.T) {
	store := newFakeStore()
	store.seedBank(httpTestPrincipal, httpTestScope, 7)
	store.seedBank(httpTestOtherPrincipal, httpTestOtherScope, 9)
	h := proxymemoryhttp.NewHandler(newTestService(t, store, nil))

	rec := serve(h, scopedRequest(http.MethodGet, "/v1/aip/memory", nil, httpTestPrincipal))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}

	var body struct {
		Banks []json.RawMessage `json:"banks"`
	}
	decodeBody(t, rec, &body)
	if len(body.Banks) != 1 {
		t.Fatalf("banks = %d entries, want only the caller's one", len(body.Banks))
	}
	bank := requireKeys(t, body.Banks[0], httpTestBankKeys)
	requireJSONString(t, bank["scope"], httpTestScope)
	requireJSONString(t, bank["project_label"], "/w/"+httpTestPrincipal)
	requireJSONString(t, bank["published_at"], "2023-11-14T22:13:20Z")
	requireJSONValue(t, bank["version"], "7")
	requireJSONValue(t, bank["pending_sessions"], "2")
	requireJSONValue(t, bank["extractions"], "3")
	requireJSONValue(t, bank["pending_notes"], "4")

	want := proxymemory.Principal{TenantID: httpTestTenant, PrincipalID: httpTestPrincipal}
	if len(store.listPrincipals) != 1 || store.listPrincipals[0] != want {
		t.Fatalf("ListBanks principals = %+v, want [%+v]", store.listPrincipals, want)
	}
}

// TestHandlerStatus pins the single-bank response with its published summary.
func TestHandlerStatus(t *testing.T) {
	store := newFakeStore()
	store.seedBank(httpTestPrincipal, httpTestScope, 7)
	store.summaries[testOwner{principal: httpTestPrincipal, scope: httpTestScope}] = "the published summary"
	h := proxymemoryhttp.NewHandler(newTestService(t, store, nil))

	rec := serve(h, scopedRequest(http.MethodGet, "/v1/aip/memory/"+httpTestScope, nil, httpTestPrincipal))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	detail := requireKeys(t, rec.Body.Bytes(), append(httpTestBankKeys, "memory_summary"))
	requireJSONString(t, detail["scope"], httpTestScope)
	requireJSONString(t, detail["memory_summary"], "the published summary")
}

// TestHandlerStatusNotOwnedEqualsUnknown pins the no-oracle rule of design
// §11.2: another principal's scope answers byte-for-byte like an unknown one.
func TestHandlerStatusNotOwnedEqualsUnknown(t *testing.T) {
	store := newFakeStore()
	store.seedBank(httpTestOtherPrincipal, httpTestOtherScope, 1)
	store.summaries[testOwner{principal: httpTestOtherPrincipal, scope: httpTestOtherScope}] = "bob's secret"
	h := proxymemoryhttp.NewHandler(newTestService(t, store, nil))

	notOwned := serve(h, scopedRequest(http.MethodGet, "/v1/aip/memory/"+httpTestOtherScope, nil, httpTestPrincipal))
	unknown := serve(h, scopedRequest(http.MethodGet, "/v1/aip/memory/"+httpTestUnknownScope, nil, httpTestPrincipal))
	if notOwned.Code != http.StatusNotFound || unknown.Code != http.StatusNotFound {
		t.Fatalf("statuses = %d and %d, want 404 and 404", notOwned.Code, unknown.Code)
	}
	if notOwned.Body.String() != unknown.Body.String() {
		t.Fatalf("not-owned body %q differs from unknown-scope body %q", notOwned.Body.String(), unknown.Body.String())
	}
	requireErrorBody(t, notOwned, "not_found")
	if strings.Contains(notOwned.Body.String(), "secret") {
		t.Fatal("response leaked another bank's content")
	}
}

// TestHandlerForget pins the 204-then-404 delete of design §11.2.
func TestHandlerForget(t *testing.T) {
	store := newFakeStore()
	store.seedBank(httpTestPrincipal, httpTestScope, 1)
	store.summaries[testOwner{principal: httpTestPrincipal, scope: httpTestScope}] = "the published summary"
	h := proxymemoryhttp.NewHandler(newTestService(t, store, nil))

	rec := serve(h, scopedRequest(http.MethodDelete, "/v1/aip/memory/"+httpTestScope, nil, httpTestPrincipal))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("first DELETE status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("first DELETE body = %q, want empty", rec.Body.String())
	}

	rec = serve(h, scopedRequest(http.MethodDelete, "/v1/aip/memory/"+httpTestScope, nil, httpTestPrincipal))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second DELETE status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	requireErrorBody(t, rec, "not_found")
}

// TestHandlerUnknownPathIsNotFound pins the unknown-path 404 of design §11.2.
func TestHandlerUnknownPathIsNotFound(t *testing.T) {
	h := proxymemoryhttp.NewHandler(newTestService(t, newFakeStore(), nil))
	cases := []string{
		"/v1/aip/memory/",
		"/v1/aip/memory/" + httpTestScope + "/extra",
		"/v1/other",
	}
	for _, target := range cases {
		t.Run(target, func(t *testing.T) {
			rec := serve(h, scopedRequest(http.MethodGet, target, nil, httpTestPrincipal))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
			}
			requireErrorBody(t, rec, "not_found")
		})
	}
}

// TestHandlerMethodNotAllowed pins the 405 mapping of design §11.2.
func TestHandlerMethodNotAllowed(t *testing.T) {
	store := newFakeStore()
	store.seedBank(httpTestPrincipal, httpTestScope, 1)
	h := proxymemoryhttp.NewHandler(newTestService(t, store, nil))
	cases := []struct {
		name   string
		method string
		target string
		allow  string
	}{
		{"item PUT", http.MethodPut, "/v1/aip/memory/" + httpTestScope, "GET, DELETE"},
		{"collection PUT", http.MethodPut, "/v1/aip/memory", "GET"},
		{"collection POST", http.MethodPost, "/v1/aip/memory", "GET"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(h, scopedRequest(tc.method, tc.target, nil, httpTestPrincipal))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
			}
			if got := rec.Header().Get("Allow"); got != tc.allow {
				t.Fatalf("Allow = %q, want %q", got, tc.allow)
			}
			requireErrorBody(t, rec, "method_not_allowed")
		})
	}
}

// TestHandlerStoreErrorIsUnavailable pins the content-free 500 and its counted
// store_unavailable (design §Error Handling).
func TestHandlerStoreErrorIsUnavailable(t *testing.T) {
	store := newFakeStore()
	store.seedBank(httpTestPrincipal, httpTestScope, 1)
	store.bankStatusErr = errors.New("boom: connection dropped")
	metrics := proxymemory.NewPrometheusCollector()
	h := proxymemoryhttp.NewHandler(newTestService(t, store, metrics), metrics)

	rec := serve(h, scopedRequest(http.MethodGet, "/v1/aip/memory/"+httpTestScope, nil, httpTestPrincipal))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	requireErrorBody(t, rec, "unavailable")
	if strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("body %q leaked the store error", rec.Body.String())
	}
	requireEventCount(t, metrics, 1)
}

// requireEventCount requires exactly one counted event series carrying want.
// It fails cleanly when the handler counted nothing (the sibling tests then
// still run) instead of panicking inside testutil.
func requireEventCount(t *testing.T, metrics *proxymemory.Metrics, want float64) {
	t.Helper()
	if got := testutil.CollectAndCount(metrics); got != 1 {
		t.Fatalf("event series = %d, want exactly 1", got)
	}
	if got := testutil.ToFloat64(metrics); got != want {
		t.Fatalf("event count = %v, want %v", got, want)
	}
}

func requireJSONString(t *testing.T, raw json.RawMessage, want string) {
	t.Helper()
	var got string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("value %s is not a JSON string: %v", raw, err)
	}
	if got != want {
		t.Fatalf("value = %q, want %q", got, want)
	}
}

func requireJSONValue(t *testing.T, raw json.RawMessage, want string) {
	t.Helper()
	if got := strings.TrimSpace(string(raw)); got != want {
		t.Fatalf("value = %s, want %s", got, want)
	}
}
