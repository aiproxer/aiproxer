package selfdefense

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/ingressdefense"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	geoipingress "github.com/aiproxer/aiproxer/internal/stdhttp/geoip"
)

const (
	testPeer    = "198.51.100.10:443"
	testClient  = "203.0.113.10:443"
	testTrustee = "192.0.2.2:443"
)

var testNow = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

type recordedTransition struct {
	reason      ingressdefense.Reason
	entryCount  int
	quarantined bool
}

type gateObserver struct {
	denials      []ingressdefense.Reason
	transitions  []recordedTransition
	unknownCalls int
}

func (o *gateObserver) Denial(reason ingressdefense.Reason) {
	o.denials = append(o.denials, reason)
}

func (o *gateObserver) QuarantineTransition(reason ingressdefense.Reason, entryCount int) {
	o.transitions = append(o.transitions, recordedTransition{reason: reason, entryCount: entryCount, quarantined: true})
}

func (o *gateObserver) closed() bool {
	known := map[ingressdefense.Reason]bool{}
	for _, reason := range ingressdefense.AllReasons() {
		known[reason] = true
	}
	for _, reason := range o.denials {
		if !known[reason] {
			o.unknownCalls++
		}
	}
	for _, transition := range o.transitions {
		if !known[transition.reason] {
			o.unknownCalls++
		}
	}
	return o.unknownCalls == 0
}

func testPolicy() *ingressdefense.Policy {
	return &ingressdefense.Policy{
		Enabled:           true,
		AuthFailures:      2,
		FailureWindow:     time.Minute,
		InitialQuarantine: time.Minute,
		MaxQuarantine:     time.Hour,
	}
}

func testState(t *testing.T) *ingressdefense.State {
	t.Helper()
	state, err := ingressdefense.NewState(ingressdefense.StateLimits{MaxEntries: 256, StateTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func testInput(t *testing.T, state *ingressdefense.State, observer Observer) Input {
	t.Helper()
	return Input{
		Policy:          testPolicy(),
		State:           state,
		Resolver:        geoipingress.ResolverConfig{Source: geoipingress.SourceDirect},
		ImpossiblePaths: true,
		Observer:        observer,
		Now:             func() time.Time { return testNow },
	}
}

func newRequest(t *testing.T, method, target, remote string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = remote
	return req
}

func neverDownstream(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("downstream handler must not run for a self-defense rejection")
	})
}

// comparableHandler is a comparable http.Handler, so a construction-level
// identity assertion can use plain interface equality. Two http.HandlerFunc
// values cannot be compared at all, so the disabled fast path cannot be proven by
// comparing a function value; this type can, and a wrapped gate changes the
// dynamic type and fails the comparison without panicking.
type comparableHandler struct{ name string }

func (h comparableHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(comparableHandlerStatus)
}

const comparableHandlerStatus = http.StatusTeapot

// TestMiddlewareDisabledPolicyIsStructuralFastPath pins requirement 1.2: a nil or
// disabled policy omits all self-defense request-side work structurally, so the
// gate returns the input handler itself rather than a wrapper that branches per
// request.
func TestMiddlewareDisabledPolicyIsStructuralFastPath(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		policy *ingressdefense.Policy
	}{
		{name: "nil policy", policy: nil},
		{name: "disabled policy", policy: &ingressdefense.Policy{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			next := comparableHandler{name: "next"}
			h := Middleware(Input{Policy: tc.policy, State: testState(t), ImpossiblePaths: true}, next)
			if h != http.Handler(next) {
				t.Fatalf("disabled gate returned a %T, want the input handler value %T unchanged: the fast path must be structural, not a per-request branch", h, next)
			}

			// An unresolvable peer and an impossible path would both reject when the
			// gate is active; a structural fast path observes neither and delegates
			// to the input handler, whose own status code proves the delegation.
			req := newRequest(t, http.MethodGet, "/.env", "proxy.example:443")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != comparableHandlerStatus {
				t.Fatalf("disabled gate = %d, want the input handler's own %d response", rec.Code, comparableHandlerStatus)
			}
			if got, ok := httpcontract.SourceAddr(req.Context()); ok || got.IsValid() {
				t.Fatalf("disabled gate attached source address %s", got)
			}
		})
	}
}

func TestMiddlewareImpossiblePathRejectsBeforeDownstreamWithGeneric404(t *testing.T) {
	t.Parallel()

	state := testState(t)
	observer := new(gateObserver)
	var attached netip.Addr
	h := Middleware(testInput(t, state, observer), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addr, ok := httpcontract.SourceAddr(r.Context())
		if !ok {
			t.Fatal("gate must attach the resolved source address to the request context")
		}
		attached = addr
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(t, http.MethodGet, "/.git/config", testPeer))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want generic 404", rec.Code)
	}
	if body := rec.Body.String(); body != "Not Found\n" {
		t.Fatalf("body = %q, want the generic not-found body", body)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	if attached.IsValid() {
		t.Fatalf("downstream ran with source %s; the deterministic 404 must precede every handler", attached)
	}
	if !state.IsQuarantined(netip.MustParseAddr("198.51.100.10"), testNow) {
		t.Fatal("a matched impossible path must record exactly one offense for the exact source address")
	}
	if state.Len() != 1 {
		t.Fatalf("tracked entries = %d, want exactly the offending source", state.Len())
	}
	if len(observer.denials) != 1 || observer.denials[0] != ingressdefense.ReasonImpossiblePath {
		t.Fatalf("denials = %v, want one impossible-path denial", observer.denials)
	}
	if len(observer.transitions) != 1 || observer.transitions[0].reason != ingressdefense.ReasonImpossiblePath {
		t.Fatalf("transitions = %+v, want one impossible-path quarantine transition", observer.transitions)
	}
	if !observer.closed() {
		t.Fatalf("observer received non-closed reasons: %+v", observer)
	}
}

func TestMiddlewareRejectsPercentEncodedTraversalTarget(t *testing.T) {
	t.Parallel()

	state := testState(t)
	h := Middleware(testInput(t, state, nil), neverDownstream(t))
	for _, target := range []string{"/%2e%2e/etc/passwd", "/%2e%2e%2f%2e%2e%2fetc/shadow", "/..%2f..%2fetc/hosts"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, newRequest(t, http.MethodGet, target, testPeer))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("target %q: code = %d, want 404", target, rec.Code)
		}
	}
}

func TestMiddlewareImpossiblePathDisabledKeepsAdaptiveBehavior(t *testing.T) {
	t.Parallel()

	state := testState(t)
	observer := new(gateObserver)
	in := testInput(t, state, observer)
	in.ImpossiblePaths = false
	in.Probe = func(*http.Request) bool { return false }

	delegated := 0
	h := Middleware(in, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		delegated++
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(t, http.MethodGet, "/.env", testPeer))
	if rec.Code != http.StatusOK || delegated != 1 {
		t.Fatalf("matcher-disabled request = %d delegated=%d, want a normal delegation", rec.Code, delegated)
	}
	if len(observer.denials) != 0 {
		t.Fatalf("denials = %v, want none while the matcher is disabled", observer.denials)
	}
	if state.Len() != 0 {
		t.Fatalf("tracked entries = %d, want none for an unmatched target", state.Len())
	}

	// Adaptive behavior is retained: the source is quarantined by an auth-failure
	// window and then refused with the generic 429.
	for range 2 {
		state.RecordAuthFailure(netip.MustParseAddr("198.51.100.10"), testNow, *in.Policy)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(t, http.MethodGet, "/v1/chat/completions", testPeer))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("quarantined request = %d, want 429 with the matcher disabled", rec.Code)
	}
	if len(observer.denials) != 1 || observer.denials[0] != ingressdefense.ReasonActiveQuarantine {
		t.Fatalf("denials = %v, want one active-quarantine denial", observer.denials)
	}
}

func TestMiddlewareAdaptiveExemptSourceGetsGeneric404WithoutAdaptiveState(t *testing.T) {
	t.Parallel()

	state := testState(t)
	observer := new(gateObserver)
	in := testInput(t, state, observer)
	in.Policy = testPolicy()
	in.Policy.AdaptiveExemptCIDRs = []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}
	h := Middleware(in, neverDownstream(t))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(t, http.MethodGet, "/.env", testPeer))

	if rec.Code != http.StatusNotFound || rec.Body.String() != "Not Found\n" {
		t.Fatalf("response = %d %q, want the deterministic generic 404", rec.Code, rec.Body.String())
	}
	if state.Len() != 0 {
		t.Fatalf("tracked entries = %d, want an exempt source to create no adaptive state", state.Len())
	}
	if state.IsQuarantined(netip.MustParseAddr("198.51.100.10"), testNow) {
		t.Fatal("an adaptive-exempt source must not be quarantined")
	}
	if len(observer.transitions) != 0 {
		t.Fatalf("transitions = %+v, want none for an adaptive-exempt source", observer.transitions)
	}
	if len(observer.denials) != 1 || observer.denials[0] != ingressdefense.ReasonImpossiblePath {
		t.Fatalf("denials = %v, want the finite impossible-path denial", observer.denials)
	}
}

func TestMiddlewareAdaptiveExemptSourceSkipsQuarantineLookup(t *testing.T) {
	t.Parallel()

	state := testState(t)
	in := testInput(t, state, nil)
	in.Policy = testPolicy()
	in.Policy.AdaptiveExemptCIDRs = []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}
	// A quarantined address that is adaptively exempt must delegate, proving the
	// exemption is applied before the quarantine lookup.
	state.RecordProbe(netip.MustParseAddr("198.51.100.10"), testNow, *in.Policy)
	if !state.IsQuarantined(netip.MustParseAddr("198.51.100.10"), testNow) {
		t.Fatal("fixture setup: source must be quarantined")
	}
	in.Probe = func(*http.Request) bool {
		t.Error("an adaptive-exempt source must not reach the credential probe")
		return true
	}
	delegated := 0
	h := Middleware(in, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		delegated++
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(t, http.MethodGet, "/v1/chat/completions", testPeer))
	if rec.Code != http.StatusOK || delegated != 1 {
		t.Fatalf("response = %d delegated=%d, want a delegation for an exempt source", rec.Code, delegated)
	}
}

func TestMiddlewareQuarantinedSourceIsRefusedOnlyWhenItCannotAuthenticate(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		probe      CredentialProbe
		wantCode   int
		wantReason ingressdefense.Reason
	}{
		{name: "proven without credential", probe: func(*http.Request) bool { return false }, wantCode: http.StatusTooManyRequests, wantReason: ingressdefense.ReasonActiveQuarantine},
		{name: "may authenticate", probe: func(*http.Request) bool { return true }, wantCode: http.StatusOK},
		{name: "no probe configured", probe: nil, wantCode: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			state := testState(t)
			observer := new(gateObserver)
			in := testInput(t, state, observer)
			in.Probe = tc.probe
			state.RecordProbe(netip.MustParseAddr("198.51.100.10"), testNow, *in.Policy)
			delegated := 0
			h := Middleware(in, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				delegated++
				w.WriteHeader(http.StatusOK)
			}))

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, newRequest(t, http.MethodGet, "/v1/chat/completions", testPeer))

			if rec.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d", rec.Code, tc.wantCode)
			}
			if tc.wantCode == http.StatusOK {
				if delegated != 1 {
					t.Fatalf("delegated = %d, want the normal auth chain to run", delegated)
				}
				if len(observer.denials) != 0 {
					t.Fatalf("denials = %v, want none", observer.denials)
				}
				return
			}
			if delegated != 0 {
				t.Fatal("a proven pre-auth refusal must not reach the auth chain")
			}
			if rec.Body.String() != "Too Many Requests\n" {
				t.Fatalf("body = %q, want the generic throttled body", rec.Body.String())
			}
			if len(observer.denials) != 1 || observer.denials[0] != tc.wantReason {
				t.Fatalf("denials = %v, want one %s", observer.denials, tc.wantReason)
			}
		})
	}
}

func TestMiddlewareQuarantineDenialDoesNotEscalateState(t *testing.T) {
	t.Parallel()

	state := testState(t)
	in := testInput(t, state, new(gateObserver))
	in.Probe = func(*http.Request) bool { return false }
	addr := netip.MustParseAddr("198.51.100.10")
	state.RecordProbe(addr, testNow, *in.Policy)
	if !state.IsQuarantined(addr, testNow) {
		t.Fatal("fixture setup: source must start quarantined")
	}
	entries := state.Len()
	h := Middleware(in, neverDownstream(t))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(t, http.MethodGet, "/v1/chat/completions", testPeer))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("code = %d, want 429", rec.Code)
	}
	if !state.IsQuarantined(addr, testNow) || state.Len() != entries {
		t.Fatal("an early quarantine refusal is a read-only decision, not a new offense")
	}
}

func TestMiddlewareForgeryHeadersAreNotSourceAuthority(t *testing.T) {
	t.Parallel()

	trusted := []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	for _, tc := range []struct {
		name       string
		remote     string
		source     geoipingress.Source
		headers    map[string]string
		wantTraced string
	}{
		{
			name: "untrusted peer with spoofed xff", remote: testClient, source: geoipingress.SourceXForwardedFor,
			headers: map[string]string{"X-Forwarded-For": "198.51.100.10, 192.0.2.1"}, wantTraced: "203.0.113.10",
		},
		{
			name: "untrusted peer with spoofed forwarded", remote: testClient, source: geoipingress.SourceForwarded,
			headers: map[string]string{"Forwarded": "for=198.51.100.10"}, wantTraced: "203.0.113.10",
		},
		{
			name: "trusted proxy chain", remote: testTrustee, source: geoipingress.SourceXForwardedFor,
			headers: map[string]string{"X-Forwarded-For": "198.51.100.10, 192.0.2.1"}, wantTraced: "198.51.100.10",
		},
		{
			name: "direct peer with spoofed headers", remote: testPeer, source: geoipingress.SourceDirect,
			headers: map[string]string{"X-Forwarded-For": "198.51.100.10", "Forwarded": "for=198.51.100.10"}, wantTraced: "198.51.100.10",
		},
		{
			name: "ipv4 mapped direct peer", remote: "[::ffff:198.51.100.10]:443", source: geoipingress.SourceDirect,
			wantTraced: "198.51.100.10",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			state := testState(t)
			in := testInput(t, state, nil)
			in.Resolver = geoipingress.ResolverConfig{Source: tc.source, TrustedProxies: trusted}
			h := Middleware(in, neverDownstream(t))
			req := newRequest(t, http.MethodGet, "/.env", tc.remote)
			for name, value := range tc.headers {
				req.Header.Set(name, value)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("code = %d, want 404", rec.Code)
			}
			traced := netip.MustParseAddr(tc.wantTraced)
			if !state.IsQuarantined(traced, testNow) {
				t.Fatalf("adaptive state is not keyed on the resolved source %s", traced)
			}
			if state.Len() != 1 {
				t.Fatalf("tracked entries = %d, want exactly one", state.Len())
			}
		})
	}
}

func TestMiddlewareUnresolvableSourceFailsClosedWithGeneric403(t *testing.T) {
	t.Parallel()

	trusted := []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	for _, tc := range []struct {
		name    string
		remote  string
		source  geoipingress.Source
		headers map[string][]string
	}{
		{name: "malformed direct peer", remote: "proxy.example:443", source: geoipingress.SourceDirect},
		{name: "missing authoritative header", remote: testTrustee, source: geoipingress.SourceXForwardedFor},
		{name: "unknown forwarded node", remote: testTrustee, source: geoipingress.SourceForwarded, headers: map[string][]string{"Forwarded": {"for=unknown"}}},
		{
			name: "chain without untrusted hop", remote: testTrustee, source: geoipingress.SourceXForwardedFor,
			headers: map[string][]string{"X-Forwarded-For": {"192.0.2.1, 192.0.2.9"}},
		},
		{
			name: "overlong hop chain", remote: testTrustee, source: geoipingress.SourceXForwardedFor,
			headers: map[string][]string{"X-Forwarded-For": repeatedHops(geoipingress.MaxForwardedHops + 1)},
		},
		{
			name: "oversized header", remote: testTrustee, source: geoipingress.SourceXForwardedFor,
			headers: map[string][]string{"X-Forwarded-For": {strings.Repeat("1", geoipingress.MaxForwardedHeaderBytes+1)}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			state := testState(t)
			observer := new(gateObserver)
			in := testInput(t, state, observer)
			in.Resolver = geoipingress.ResolverConfig{Source: tc.source, TrustedProxies: trusted}
			h := Middleware(in, neverDownstream(t))
			req := newRequest(t, http.MethodGet, "/v1/chat/completions", tc.remote)
			for name, values := range tc.headers {
				for _, value := range values {
					req.Header.Add(name, value)
				}
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden || rec.Body.String() != "Forbidden\n" {
				t.Fatalf("response = %d %q, want a generic 403", rec.Code, rec.Body.String())
			}
			if state.Len() != 0 {
				t.Fatalf("tracked entries = %d, want no adaptive state for an unresolvable source", state.Len())
			}
			if len(observer.denials) != 1 || observer.denials[0] != ingressdefense.ReasonClientIPError {
				t.Fatalf("denials = %v, want one client-ip-error denial", observer.denials)
			}
		})
	}
}

func repeatedHops(count int) []string {
	out := make([]string, 0, count)
	for range count {
		out = append(out, "198.51.100.10")
	}
	return out
}

func TestMiddlewareNeverInspectsRequestBody(t *testing.T) {
	t.Parallel()

	payload := `{"model":"gpt-4o","messages":[{"role":"user","content":"' OR 1=1; DROP TABLE users;-- <script>alert(1)</script> ../../../../etc/passwd $(rm -rf /) analyze this malware sample"}]}`
	state := testState(t)
	observer := new(gateObserver)
	h := Middleware(testInput(t, state, observer), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("downstream body read: %v", err)
		}
		if string(body) != payload {
			t.Fatalf("downstream body = %q, want the untouched prompt payload", body)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?prompt='+OR+1%3D1--&path=../../etc/passwd", strings.NewReader(payload))
	req.RemoteAddr = testPeer
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want the legitimate prompt request to reach the handler", rec.Code)
	}
	if state.Len() != 0 || len(observer.denials) != 0 {
		t.Fatalf("state=%d denials=%v, want body content to be invisible to self-defense", state.Len(), observer.denials)
	}
}

func TestMiddlewareIgnoresHostileQueryValues(t *testing.T) {
	t.Parallel()

	targets := []string{
		"/v1/chat/completions?file=/etc/passwd",
		"/v1/chat/completions?path=../../../../etc/passwd",
		"/v1/responses?x=<script>alert(1)</script>",
		"/v1/messages?q='+OR+1%3D1--+",
		"/v1/models?url=http://169.254.169.254/latest/meta-data/",
		"/v1/chat/completions?file=.env&next=../wp-login.php",
	}
	state := testState(t)
	observer := new(gateObserver)
	h := Middleware(testInput(t, state, observer), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, target := range targets {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, newRequest(t, http.MethodGet, target, testPeer))
		if rec.Code != http.StatusOK {
			t.Errorf("target %q: code = %d, want the legitimate request to be delegated", target, rec.Code)
		}
	}
	if state.Len() != 0 || len(observer.denials) != 0 {
		t.Fatalf("state=%d denials=%v, want query values to be invisible to self-defense", state.Len(), observer.denials)
	}
}

func TestMiddlewareGenericResponsesRevealNoInternals(t *testing.T) {
	t.Parallel()

	state := testState(t)
	observer := new(gateObserver)
	in := testInput(t, state, observer)
	in.Probe = func(*http.Request) bool { return false }
	state.RecordProbe(netip.MustParseAddr("198.51.100.10"), testNow, *in.Policy)
	h := Middleware(in, neverDownstream(t))

	for _, tc := range []struct {
		target    string
		wantCode  int
		wantBody  string
		forbidden []string
	}{
		{
			target: "/.env", wantCode: http.StatusNotFound, wantBody: "Not Found\n",
			forbidden: []string{"env", "impossible", "self", "defense", "198.51.100.10", "rule", "offense"},
		},
		{
			target: "/v1/chat/completions", wantCode: http.StatusTooManyRequests, wantBody: "Too Many Requests\n",
			forbidden: []string{"198.51.100.10", "quarantine", "score", "level", "offense", "throttl", "retry-after"},
		},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, newRequest(t, http.MethodGet, tc.target, testPeer))
		if rec.Code != tc.wantCode || rec.Body.String() != tc.wantBody {
			t.Fatalf("target %q: response = %d %q, want %d %q", tc.target, rec.Code, rec.Body.String(), tc.wantCode, tc.wantBody)
		}
		payload := strings.ToLower(rec.Body.String() + " " + rec.Header().Get("Retry-After"))
		for _, needle := range tc.forbidden {
			if strings.Contains(payload, strings.ToLower(needle)) {
				t.Fatalf("target %q: generic response leaks %q: %q", tc.target, needle, payload)
			}
		}
	}
}

// TestRecordTransitionEmitsOnlyForAStartedQuarantine pins the guard that keeps
// the observer inside the closed reason vocabulary. Production RecordProbe always
// sets QuarantineStarted, so the zero Transition is currently unreachable through
// the gate; the guard is the contract that a future below-threshold probe path
// must not be able to break, because a Transition without a started quarantine
// carries Reason("") and an empty reason is outside ingressdefense.AllReasons.
//
// recordTransition is the single chokepoint the gate uses, so testing it
// directly needs no fake state, no mock framework and no second seam.
func TestRecordTransitionEmitsOnlyForAStartedQuarantine(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		transition ingressdefense.Transition
		wantEmit   bool
	}{
		{
			name:       "zero transition",
			transition: ingressdefense.Transition{},
		},
		{
			name:       "below threshold auth failure shape",
			transition: ingressdefense.Transition{EntryCount: 7},
		},
		{
			name:       "reason set without a started quarantine",
			transition: ingressdefense.Transition{Reason: ingressdefense.ReasonAuthFailureThreshold, EntryCount: 7},
		},
		{
			name:       "stale deadline without a started quarantine",
			transition: ingressdefense.Transition{QuarantineUntil: testNow.Add(time.Hour), EntryCount: 7},
		},
		{
			name:       "started impossible-path probe",
			transition: ingressdefense.Transition{QuarantineStarted: true, QuarantineUntil: testNow.Add(time.Minute), Reason: ingressdefense.ReasonImpossiblePath, EntryCount: 1},
			wantEmit:   true,
		},
		{
			name:       "escalated quarantine carries a fresh reason",
			transition: ingressdefense.Transition{QuarantineStarted: true, QuarantineUntil: testNow.Add(time.Hour), Reason: ingressdefense.ReasonAuthFailureThreshold, EntryCount: 42},
			wantEmit:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			observer := new(gateObserver)
			recordTransition(observer, tc.transition)

			if !observer.closed() {
				t.Fatalf("observer received a reason outside the closed vocabulary: %+v", observer.transitions)
			}
			if !tc.wantEmit {
				if len(observer.transitions) != 0 {
					t.Fatalf("transitions = %+v, want no emission for %+v", observer.transitions, tc.transition)
				}
				return
			}
			if len(observer.transitions) != 1 {
				t.Fatalf("transitions = %+v, want exactly one emission for %+v", observer.transitions, tc.transition)
			}
			got := observer.transitions[0]
			if got.reason != tc.transition.Reason || got.entryCount != tc.transition.EntryCount {
				t.Fatalf("emitted %+v, want reason %q and entry count %d", got, tc.transition.Reason, tc.transition.EntryCount)
			}
		})
	}
}

// TestRecordTransitionToleratesAnAbsentObserver keeps the metrics seam strictly
// non-authoritative, so a nil observer can never become a nil dereference on the
// refusal path.
func TestRecordTransitionToleratesAnAbsentObserver(t *testing.T) {
	t.Parallel()

	recordTransition(nil, ingressdefense.Transition{})
	recordTransition(nil, ingressdefense.Transition{QuarantineStarted: true, Reason: ingressdefense.ReasonImpossiblePath, EntryCount: 1})
}

// TestMiddlewareObserverNeverSeesAnOpenOrEmptyReason is the end-to-end half of
// the same contract: across every gate outcome the observer is called with
// members of the closed reason set only, never with an empty or invented reason.
// Requirement 9.3 and 9.4 forbid a label outside that vocabulary.
func TestMiddlewareObserverNeverSeesAnOpenOrEmptyReason(t *testing.T) {
	t.Parallel()

	addr := netip.MustParseAddr("198.51.100.10")
	for _, tc := range []struct {
		name    string
		remote  string
		target  string
		mutate  func(in *Input, state *ingressdefense.State)
		wantAny bool
	}{
		{
			name:   "impossible path probe",
			remote: testPeer, target: "/.git/config", wantAny: true,
		},
		{
			name:   "legitimate delegation",
			remote: testPeer, target: "/v1/chat/completions",
		},
		{
			name:   "unresolvable source",
			remote: "proxy.example:443", target: "/v1/chat/completions", wantAny: true,
		},
		{
			name:   "quarantine denial",
			remote: testPeer, target: "/v1/chat/completions", wantAny: true,
			mutate: func(in *Input, state *ingressdefense.State) {
				state.RecordProbe(addr, testNow, *in.Policy)
				in.Probe = func(*http.Request) bool { return false }
			},
		},
		{
			name:   "matcher disabled with an active quarantine",
			remote: testPeer, target: "/.env", wantAny: true,
			mutate: func(in *Input, state *ingressdefense.State) {
				in.ImpossiblePaths = false
				state.RecordProbe(addr, testNow, *in.Policy)
				in.Probe = func(*http.Request) bool { return false }
			},
		},
		{
			name:   "adaptively exempt source",
			remote: testPeer, target: "/.env", wantAny: true,
			mutate: func(in *Input, _ *ingressdefense.State) {
				in.Policy = testPolicy()
				in.Policy.AdaptiveExemptCIDRs = []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}
			},
		},
		{
			name:   "escalating repeat probe",
			remote: testPeer, target: "/wp-login.php", wantAny: true,
			mutate: func(in *Input, state *ingressdefense.State) {
				state.RecordProbe(addr, testNow, *in.Policy)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			state := testState(t)
			observer := new(gateObserver)
			in := testInput(t, state, observer)
			if tc.mutate != nil {
				tc.mutate(&in, state)
			}
			h := Middleware(in, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, newRequest(t, http.MethodGet, tc.target, tc.remote))

			observed := len(observer.denials) + len(observer.transitions)
			if (observed > 0) != tc.wantAny {
				t.Fatalf("observations = %d (denials=%v transitions=%+v), want observed=%v", observed, observer.denials, observer.transitions, tc.wantAny)
			}
			if !observer.closed() {
				t.Fatalf("observer received a reason outside ingressdefense.AllReasons: denials=%v transitions=%+v", observer.denials, observer.transitions)
			}
			for _, transition := range observer.transitions {
				if transition.reason == "" {
					t.Fatalf("observer received the empty reason: %+v", observer.transitions)
				}
			}
		})
	}
}

func TestMiddlewareAttachesNormalizedSourceAddrToContext(t *testing.T) {
	t.Parallel()

	var attached netip.Addr
	var ok bool
	h := Middleware(testInput(t, testState(t), nil), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attached, ok = httpcontract.SourceAddr(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(t, http.MethodGet, "/v1/models", "[::ffff:198.51.100.10]:443"))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if !ok || attached != netip.MustParseAddr("198.51.100.10") {
		t.Fatalf("attached = %s ok=%v, want the normalized peer address", attached, ok)
	}
}

// TestMiddlewareDoesNotChangeTransportAuthAttribution pins requirement 2.5 on the
// request the gate actually delegates. The gate publishes its resolved source
// address through a shallow r.WithContext copy, so assertions on the outer
// request prove nothing about attribution: the delegated request is the only
// request the transport-auth chain and every inner observer can see.
func TestMiddlewareDoesNotChangeTransportAuthAttribution(t *testing.T) {
	t.Parallel()

	state := testState(t)
	in := testInput(t, state, nil)
	in.Resolver = geoipingress.ResolverConfig{
		Source:         geoipingress.SourceXForwardedFor,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")},
	}
	var delegated *http.Request
	h := Middleware(in, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delegated = r
		w.WriteHeader(http.StatusOK)
	}))
	req := newRequest(t, http.MethodPost, "/v1/chat/completions", testTrustee)
	req.Header.Set("X-Forwarded-For", "198.51.100.10, 192.0.2.1")
	req.Header.Set("Authorization", "Bearer presentation-only")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want a delegation", rec.Code)
	}
	if delegated == nil {
		t.Fatal("the downstream handler must run exactly once for a legitimate request")
	}
	// The delegated request keeps the transport-auth attribution inputs verbatim:
	// the direct peer, the forwarding chain and the presented credential are the
	// auth chain's own inputs, so rewriting any of them here would change who the
	// request authenticates as.
	if delegated.RemoteAddr != testTrustee {
		t.Fatalf("delegated RemoteAddr = %q, want the untouched direct peer %q; the gate must not change auth attribution", delegated.RemoteAddr, testTrustee)
	}
	if got, want := delegated.Header.Get("X-Forwarded-For"), "198.51.100.10, 192.0.2.1"; got != want {
		t.Fatalf("delegated X-Forwarded-For = %q, want %q", got, want)
	}
	if got, want := delegated.Header.Get("Authorization"), "Bearer presentation-only"; got != want {
		t.Fatalf("delegated Authorization = %q, want %q", got, want)
	}
	// The gate's own decision still resolves the client from the trusted chain, so
	// the snapshot and the untouched attribution inputs coexist: the outer gate
	// normalizes nothing on the request.
	if addr, ok := httpcontract.SourceAddr(delegated.Context()); !ok || addr != netip.MustParseAddr("198.51.100.10") {
		t.Fatalf("delegated source snapshot = %s ok=%v, want the resolved forwarding client", addr, ok)
	}
	if req.RemoteAddr != testTrustee {
		t.Fatalf("outer RemoteAddr rewritten to %q", req.RemoteAddr)
	}
}

func TestMiddlewareOffenseNeverEscalatesToASubnet(t *testing.T) {
	t.Parallel()

	state := testState(t)
	h := Middleware(testInput(t, state, nil), neverDownstream(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(t, http.MethodGet, "/.env", "198.51.100.10:443"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
	if state.Len() != 1 {
		t.Fatalf("tracked entries = %d, want exactly the offending address", state.Len())
	}
	for _, neighbour := range []string{"198.51.100.0", "198.51.100.1", "198.51.100.11", "198.51.101.10"} {
		if state.IsQuarantined(netip.MustParseAddr(neighbour), testNow) {
			t.Fatalf("neighbour %s was quarantined; self-defense must never widen an address into a prefix", neighbour)
		}
	}
}

func TestMiddlewareObserverAbsenceDoesNotChangeTheDecision(t *testing.T) {
	t.Parallel()

	h := Middleware(testInput(t, testState(t), nil), neverDownstream(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(t, http.MethodGet, "/wp-login.php", testPeer))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404 without an observer", rec.Code)
	}
}

func TestMiddlewareNormalizesAndMatchesAdaptiveExemptionByCidr(t *testing.T) {
	t.Parallel()

	state := testState(t)
	in := testInput(t, state, nil)
	in.Policy = testPolicy()
	// The direct peer arrives IPv4-mapped; the exemption must be matched against
	// the normalized address exactly as the adaptive state keys it.
	in.Policy.AdaptiveExemptCIDRs = []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}
	h := Middleware(in, neverDownstream(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(t, http.MethodGet, "/.env", "[::ffff:198.51.100.10]:443"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
	if state.Len() != 0 {
		t.Fatalf("tracked entries = %d, want a normalized exemption match to suppress state", state.Len())
	}
}
