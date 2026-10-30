package auth_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/ingressdefense"
	"github.com/aiproxer/aiproxer/internal/stdhttp/auth"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

// recorderSpy captures every recorded source address and, when events is set,
// the order of hook calls relative to the termination response and the route
// delegation.
type recorderSpy struct {
	addrs  []netip.Addr
	events *[]string
}

func (s *recorderSpy) record(addr netip.Addr) {
	s.addrs = append(s.addrs, addr)
	if s.events != nil {
		*s.events = append(*s.events, "record")
	}
}

func (s *recorderSpy) clear(addr netip.Addr) {
	s.addrs = append(s.addrs, addr)
	if s.events != nil {
		*s.events = append(*s.events, "clear")
	}
}

func (s *recorderSpy) hooks() auth.SelfDefenseHooks {
	return auth.SelfDefenseHooks{RecordAuthFailure: s.record, ClearSource: s.clear}
}

// orderingWriter reports hook calls that happened before the response status was
// written, so "record before writing the existing termination response" is
// observable rather than assumed.
type orderingWriter struct {
	*httptest.ResponseRecorder
	events *[]string
}

func (w *orderingWriter) WriteHeader(code int) {
	*w.events = append(*w.events, "write-header")
	w.ResponseRecorder.WriteHeader(code)
}

const observedAddr = "198.51.100.7"

func gatedRequest(t *testing.T, addr string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	if addr != "" {
		req = req.WithContext(httpcontract.WithSourceAddr(req.Context(), netip.MustParseAddr(addr)))
	}
	return req
}

func TestSelfDefenseMiddleware_countsOnlyTerminalPrePrincipal401(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		chain    []httpauth.Provider
		wantCode int
		wantCall bool
	}{
		{name: "pre principal reject 401", chain: []httpauth.Provider{stubProvider{res: httpauth.AuthenticationResult{
			Type: httpauth.TypeReject, HTTPStatus: http.StatusUnauthorized, Body: []byte(`{"error":"unauthorized"}`),
		}}}, wantCode: http.StatusUnauthorized, wantCall: true},
		{name: "pre principal reject with default status", chain: []httpauth.Provider{stubProvider{res: httpauth.AuthenticationResult{
			Type: httpauth.TypeReject, Body: []byte("no"),
		}}}, wantCode: http.StatusUnauthorized, wantCall: true},
		{name: "pre principal challenge 401", chain: []httpauth.Provider{stubProvider{res: httpauth.AuthenticationResult{
			Type: httpauth.TypeChallenge, Headers: http.Header{"Www-Authenticate": []string{`Bearer realm="aip"`}},
			Body: []byte("challenge"),
		}}}, wantCode: http.StatusUnauthorized, wantCall: true},
		{name: "pre principal continue then reject 401", chain: []httpauth.Provider{
			stubProvider{res: httpauth.AuthenticationResult{Type: httpauth.TypeContinue}},
			stubProvider{res: httpauth.AuthenticationResult{Type: httpauth.TypeReject, HTTPStatus: http.StatusUnauthorized}},
		}, wantCode: http.StatusUnauthorized, wantCall: true},
		{name: "authorization denial 403 is not evidence", chain: []httpauth.Provider{stubProvider{res: httpauth.AuthenticationResult{
			Type: httpauth.TypeReject, HTTPStatus: http.StatusForbidden, Body: []byte("forbidden"),
		}}}, wantCode: http.StatusForbidden},
		{name: "provider system failure 503 is not evidence", chain: []httpauth.Provider{stubProvider{res: httpauth.AuthenticationResult{
			Type: httpauth.TypeReject, HTTPStatus: http.StatusServiceUnavailable, Body: []byte("unavailable"),
		}}}, wantCode: http.StatusServiceUnavailable},
		{name: "provider error is not evidence", chain: []httpauth.Provider{errProvider{}}, wantCode: http.StatusInternalServerError},
		{name: "unusable provider result is not evidence", chain: []httpauth.Provider{
			stubProvider{res: httpauth.AuthenticationResult{Type: httpauth.AuthenticationType(99)}},
		}, wantCode: http.StatusInternalServerError},
		{name: "principal then reject 401 is not evidence", chain: []httpauth.Provider{
			stubProvider{res: httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, Principal: execview.PrincipalView{ID: "alice"}}},
			stubProvider{res: httpauth.AuthenticationResult{Type: httpauth.TypeReject, HTTPStatus: http.StatusUnauthorized}},
		}, wantCode: http.StatusUnauthorized},
		{name: "principal then challenge 401 is not evidence", chain: []httpauth.Provider{
			stubProvider{res: httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, Principal: execview.PrincipalView{ID: "alice"}}},
			stubProvider{res: httpauth.AuthenticationResult{Type: httpauth.TypeChallenge, HTTPStatus: http.StatusUnauthorized}},
		}, wantCode: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spy := &recorderSpy{}
			h := auth.SelfDefenseMiddleware(nil, tc.chain, spy.hooks(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Error("inner handler must not run for a terminal auth outcome")
			}))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, gatedRequest(t, observedAddr))
			if rec.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d", rec.Code, tc.wantCode)
			}
			if got := spy.addrs != nil; got != tc.wantCall {
				t.Fatalf("recorded = %v, want recorded = %v", spy.addrs, tc.wantCall)
			}
			if tc.wantCall && (len(spy.addrs) != 1 || spy.addrs[0] != netip.MustParseAddr(observedAddr)) {
				t.Fatalf("recorded addrs = %v, want exactly the snapshotted %s", spy.addrs, observedAddr)
			}
		})
	}
}

func TestSelfDefenseMiddleware_recordsBeforeWritingTerminationResponse(t *testing.T) {
	t.Parallel()
	var events []string
	spy := &recorderSpy{events: &events}
	chain := []httpauth.Provider{stubProvider{res: httpauth.AuthenticationResult{
		Type: httpauth.TypeReject, HTTPStatus: http.StatusUnauthorized, Body: []byte("nope"),
	}}}
	w := &orderingWriter{ResponseRecorder: httptest.NewRecorder(), events: &events}
	auth.SelfDefenseMiddleware(nil, chain, spy.hooks(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("inner handler must not run")
	})).ServeHTTP(w, gatedRequest(t, observedAddr))
	if want := []string{"record", "write-header"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("event order = %v, want %v", events, want)
	}
}

func TestSelfDefenseMiddleware_clearsAfterFullChainWithPrincipal(t *testing.T) {
	t.Parallel()
	principal := stubProvider{res: httpauth.AuthenticationResult{
		Type: httpauth.TypePrincipal, Principal: execview.PrincipalView{ID: "alice"},
	}}
	for _, tc := range []struct {
		name     string
		chain    []httpauth.Provider
		wantCall bool
	}{
		{name: "full successful chain", chain: []httpauth.Provider{
			stubProvider{res: httpauth.AuthenticationResult{Type: httpauth.TypeContinue}},
			principal,
			stubProvider{res: httpauth.AuthenticationResult{
				Type: httpauth.TypeAnnotate, ResponseHeaders: http.Header{"Vary": []string{"Accept"}},
			}},
		}, wantCall: true},
		{name: "chain without any principal", chain: []httpauth.Provider{
			stubProvider{res: httpauth.AuthenticationResult{Type: httpauth.TypeContinue}},
			stubProvider{res: httpauth.AuthenticationResult{Type: httpauth.TypeContinue}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spy := &recorderSpy{}
			var delegated bool
			h := auth.SelfDefenseMiddleware(nil, tc.chain, spy.hooks(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				delegated = true
			}))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, gatedRequest(t, observedAddr))
			if !delegated {
				t.Fatal("a completed chain must delegate to the route mux")
			}
			if (spy.addrs != nil) != tc.wantCall {
				t.Fatalf("cleared = %v, want cleared = %v", spy.addrs, tc.wantCall)
			}
			if tc.wantCall && (len(spy.addrs) != 1 || spy.addrs[0] != netip.MustParseAddr(observedAddr)) {
				t.Fatalf("cleared addrs = %v, want exactly the snapshotted %s", spy.addrs, observedAddr)
			}
		})
	}
}

func TestSelfDefenseMiddleware_clearsImmediatelyBeforeRouteDelegation(t *testing.T) {
	t.Parallel()
	var events []string
	spy := &recorderSpy{events: &events}
	chain := []httpauth.Provider{stubProvider{res: httpauth.AuthenticationResult{
		Type: httpauth.TypePrincipal, Principal: execview.PrincipalView{ID: "alice"},
	}}}
	h := auth.SelfDefenseMiddleware(nil, chain, spy.hooks(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		events = append(events, "delegate")
	}))
	h.ServeHTTP(httptest.NewRecorder(), gatedRequest(t, observedAddr))
	if want := []string{"clear", "delegate"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("event order = %v, want %v", events, want)
	}
}

// TestSelfDefenseMiddleware_usesTheGateSnapshotOnly proves the observer consumes
// the identity the gate snapshotted: it never re-derives an address from the
// forwarding headers authentication itself is allowed to ignore, and it does
// nothing at all when no snapshot is present (self-defense absent or disabled).
func TestSelfDefenseMiddleware_usesTheGateSnapshotOnly(t *testing.T) {
	t.Parallel()
	chain := []httpauth.Provider{stubProvider{res: httpauth.AuthenticationResult{
		Type: httpauth.TypeReject, HTTPStatus: http.StatusUnauthorized,
	}}}

	spy := &recorderSpy{}
	rec := httptest.NewRecorder()
	auth.SelfDefenseMiddleware(nil, chain, spy.hooks(), http.NotFoundHandler()).ServeHTTP(rec, gatedRequest(t, observedAddr))
	if len(spy.addrs) != 1 || spy.addrs[0] != netip.MustParseAddr(observedAddr) {
		t.Fatalf("recorded addrs = %v, want the snapshotted %s and not the forwarded %s",
			spy.addrs, observedAddr, "203.0.113.9")
	}

	ungated := &recorderSpy{}
	rec = httptest.NewRecorder()
	auth.SelfDefenseMiddleware(nil, chain, ungated.hooks(), http.NotFoundHandler()).ServeHTTP(rec, gatedRequest(t, ""))
	if ungated.addrs != nil {
		t.Fatalf("recorded addrs = %v, want no observation without a gate snapshot", ungated.addrs)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want the existing auth response %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestSelfDefenseMiddleware_normalizesIPv4MappedSnapshot(t *testing.T) {
	t.Parallel()
	spy := &recorderSpy{}
	chain := []httpauth.Provider{stubProvider{res: httpauth.AuthenticationResult{
		Type: httpauth.TypePrincipal, Principal: execview.PrincipalView{ID: "alice"},
	}}}
	auth.SelfDefenseMiddleware(nil, chain, spy.hooks(), http.NotFoundHandler()).
		ServeHTTP(httptest.NewRecorder(), gatedRequest(t, "::ffff:198.51.100.7"))
	if len(spy.addrs) != 1 || spy.addrs[0] != netip.MustParseAddr(observedAddr) {
		t.Fatalf("cleared addrs = %v, want the unmapped %s", spy.addrs, observedAddr)
	}
}

// TestSelfDefenseMiddleware_partialHooksObserveOnlyTheirOwnOutcome proves each
// hook is independent: the composition root may project either half of the
// observation without the other mutating adaptive state.
func TestSelfDefenseMiddleware_partialHooksObserveOnlyTheirOwnOutcome(t *testing.T) {
	t.Parallel()
	rejecting := []httpauth.Provider{stubProvider{res: httpauth.AuthenticationResult{
		Type: httpauth.TypeReject, HTTPStatus: http.StatusUnauthorized,
	}}}
	accepting := []httpauth.Provider{stubProvider{res: httpauth.AuthenticationResult{
		Type: httpauth.TypePrincipal, Principal: execview.PrincipalView{ID: "alice"},
	}}}

	only := &recorderSpy{}
	auth.SelfDefenseMiddleware(nil, accepting, auth.SelfDefenseHooks{ClearSource: only.clear}, http.NotFoundHandler()).
		ServeHTTP(httptest.NewRecorder(), gatedRequest(t, observedAddr))
	if len(only.addrs) != 1 {
		t.Fatalf("clear-only hooks addrs = %v, want one clear", only.addrs)
	}

	failing := &recorderSpy{}
	auth.SelfDefenseMiddleware(nil, rejecting, auth.SelfDefenseHooks{ClearSource: failing.clear}, http.NotFoundHandler()).
		ServeHTTP(httptest.NewRecorder(), gatedRequest(t, observedAddr))
	if failing.addrs != nil {
		t.Fatalf("clear-only hooks must never clear on a terminal rejection: %v", failing.addrs)
	}
}

// TestSelfDefenseMiddleware_preservesAuthResponsesByteForByte pins the existing
// renderer/status/body semantics against the observed variant for every terminal
// and non-terminal outcome, and pins the compatibility wrapper to the same
// responses, so observation can only ever add state, never change a response.
func TestSelfDefenseMiddleware_preservesAuthResponsesByteForByte(t *testing.T) {
	t.Parallel()
	annotate := http.Header{"Vary": []string{"Accept"}, "Set-Cookie": []string{"a=b"}}
	challenge := http.Header{"Www-Authenticate": []string{`Bearer realm="aip"`}, "Set-Cookie": []string{"a=b"}}
	principal := stubProvider{res: httpauth.AuthenticationResult{
		Type: httpauth.TypePrincipal, Principal: execview.PrincipalView{ID: "alice"},
	}}
	for _, tc := range []struct {
		name  string
		chain []httpauth.Provider
	}{
		{name: "no providers", chain: nil},
		{name: "only nil providers", chain: []httpauth.Provider{nil}},
		{name: "continue only", chain: []httpauth.Provider{stubProvider{res: httpauth.AuthenticationResult{Type: httpauth.TypeContinue}}}},
		{name: "annotate only", chain: []httpauth.Provider{stubProvider{res: httpauth.AuthenticationResult{
			Type: httpauth.TypeAnnotate, ResponseHeaders: annotate,
		}}}},
		{name: "principal", chain: []httpauth.Provider{principal}},
		{name: "reject", chain: []httpauth.Provider{stubProvider{res: httpauth.AuthenticationResult{
			Type: httpauth.TypeReject, HTTPStatus: http.StatusUnauthorized, Body: []byte(`{"error":"nope"}`),
			ContentType: "application/json; charset=utf-8",
		}}}},
		{name: "challenge", chain: []httpauth.Provider{stubProvider{res: httpauth.AuthenticationResult{
			Type: httpauth.TypeChallenge, HTTPStatus: http.StatusUnauthorized, Headers: challenge, Body: []byte("denied"),
		}}}},
		{name: "provider error", chain: []httpauth.Provider{leakyErrProvider{err: context.DeadlineExceeded}}},
		{name: "unusable result", chain: []httpauth.Provider{stubProvider{res: httpauth.AuthenticationResult{
			Type: httpauth.AuthenticationType(99),
		}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			compat := observeResponse(t, auth.Middleware(nil, tc.chain, http.NotFoundHandler()))
			observed := observeResponse(t, auth.SelfDefenseMiddleware(nil, tc.chain, auth.SelfDefenseHooks{}, http.NotFoundHandler()))
			live := observeResponse(t, auth.SelfDefenseMiddleware(nil, tc.chain, (&recorderSpy{}).hooks(), http.NotFoundHandler()))
			if !reflect.DeepEqual(observed, compat) {
				t.Fatalf("zero-hook variant = %+v, compatibility wrapper = %+v", observed, compat)
			}
			if !reflect.DeepEqual(live, compat) {
				t.Fatalf("observed variant = %+v, compatibility wrapper = %+v", live, compat)
			}
		})
	}
}

type observedResponse struct {
	code    int
	headers string
	body    string
}

func observeResponse(t *testing.T, h http.Handler) observedResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte("{}")))
	h.ServeHTTP(rec, req)
	keys := make([]string, 0, len(rec.Header()))
	for name := range rec.Header() {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	var headers strings.Builder
	for _, name := range keys {
		headers.WriteString(name + ": " + rec.Header().Get(name) + "\n")
	}
	return observedResponse{code: rec.Code, headers: headers.String(), body: rec.Body.String()}
}

// TestSelfDefenseMiddleware_zeroHooksCostNothingOnTheRequestPath pins the
// disabled posture: the compatibility wrapper and the zero-hook options variant
// perform exactly the same allocations for the same request, so switching the
// variant on cannot add per-request work to a generation with self-defense off.
// It runs serially because [testing.AllocsPerRun] forbids a parallel test.
func TestSelfDefenseMiddleware_zeroHooksCostNothingOnTheRequestPath(t *testing.T) {
	chain := []httpauth.Provider{
		stubProvider{res: httpauth.AuthenticationResult{Type: httpauth.TypeContinue}},
		stubProvider{res: httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, Principal: execview.PrincipalView{ID: "alice"}}},
	}
	next := http.NotFoundHandler()
	req := gatedRequest(t, observedAddr)
	rec := httptest.NewRecorder()

	compat := auth.Middleware(nil, chain, next)
	zeroHooks := auth.SelfDefenseMiddleware(nil, chain, auth.SelfDefenseHooks{}, next)
	liveHooks := auth.SelfDefenseMiddleware(nil, chain, (&recorderSpy{}).hooks(), next)

	compatAllocs := testing.AllocsPerRun(200, func() { compat.ServeHTTP(rec, req) })
	zeroAllocs := testing.AllocsPerRun(200, func() { zeroHooks.ServeHTTP(rec, req) })
	liveAllocs := testing.AllocsPerRun(200, func() { liveHooks.ServeHTTP(rec, req) })
	if zeroAllocs != compatAllocs {
		t.Fatalf("zero-hook variant allocates %v, compatibility wrapper %v", zeroAllocs, compatAllocs)
	}
	if liveAllocs > compatAllocs {
		t.Fatalf("observed variant allocates %v, compatibility wrapper %v", liveAllocs, compatAllocs)
	}
}

// TestSelfDefenseMiddleware_drivesBoundedAdaptiveState proves the observer against
// the real process state a composition root projects: below-threshold counted
// failures never quarantine, the threshold starts quarantine with the closed
// reason vocabulary, and a full successful chain clears the exact address.
func TestSelfDefenseMiddleware_drivesBoundedAdaptiveState(t *testing.T) {
	t.Parallel()
	state, err := ingressdefense.NewState(ingressdefense.StateLimits{MaxEntries: 64, StateTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	policy := ingressdefense.Policy{
		Enabled: true, AuthFailures: 2, FailureWindow: time.Minute,
		InitialQuarantine: 30 * time.Second, MaxQuarantine: time.Minute,
	}
	now := time.Date(2026, time.March, 2, 12, 0, 0, 0, time.UTC)
	addr := netip.MustParseAddr(observedAddr)
	var reasons []ingressdefense.Reason
	hooks := auth.SelfDefenseHooks{
		RecordAuthFailure: func(got netip.Addr) {
			transition := state.RecordAuthFailure(got, now, policy)
			// The counted-failure Transition below the threshold is the zero value:
			// its reason is read only when a quarantine actually started, so no
			// label can escape the closed vocabulary.
			if transition.QuarantineStarted {
				reasons = append(reasons, transition.Reason)
			}
		},
		ClearSource: func(got netip.Addr) { state.Clear(got) },
	}
	unauthorized := []httpauth.Provider{stubProvider{res: httpauth.AuthenticationResult{
		Type: httpauth.TypeReject, HTTPStatus: http.StatusUnauthorized,
	}}}
	accepted := []httpauth.Provider{stubProvider{res: httpauth.AuthenticationResult{
		Type: httpauth.TypePrincipal, Principal: execview.PrincipalView{ID: "alice"},
	}}}

	auth.SelfDefenseMiddleware(nil, unauthorized, hooks, http.NotFoundHandler()).
		ServeHTTP(httptest.NewRecorder(), gatedRequest(t, observedAddr))
	if state.IsQuarantined(addr, now) {
		t.Fatal("one counted failure is below the configured threshold")
	}
	if reasons != nil {
		t.Fatalf("reasons = %v, want none below the threshold", reasons)
	}

	auth.SelfDefenseMiddleware(nil, unauthorized, hooks, http.NotFoundHandler()).
		ServeHTTP(httptest.NewRecorder(), gatedRequest(t, observedAddr))
	if !state.IsQuarantined(addr, now) {
		t.Fatal("the thresholded failure must start a quarantine")
	}
	if !reflect.DeepEqual(reasons, []ingressdefense.Reason{ingressdefense.ReasonAuthFailureThreshold}) {
		t.Fatalf("reasons = %v, want [%s]", reasons, ingressdefense.ReasonAuthFailureThreshold)
	}

	auth.SelfDefenseMiddleware(nil, accepted, hooks, http.NotFoundHandler()).
		ServeHTTP(httptest.NewRecorder(), gatedRequest(t, observedAddr))
	if state.IsQuarantined(addr, now) {
		t.Fatal("a full successful chain must clear the exact-address state")
	}
	if state.Len() != 0 {
		t.Fatalf("entry count = %d, want the cleared address to leave no entry", state.Len())
	}
}
