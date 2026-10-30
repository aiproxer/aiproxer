// This file is the composed acceptance certification of ingress self-defense
// (spec GROUP 7, tasks 7.1 and 7.2) at the standard data-plane handler graph.
//
// Every request here travels the real [stackHTTPHandler] graph — outer security
// headers, outer recovery, the self-defense ingress gate, request ID, access
// log, transport auth with the paired self-defense outcome observer, and a real
// [http.ServeMux] carrying a legitimate LLM route — against a real
// [ingressdefense.State] and a real transport-auth chain: the standard
// [stdauth.PolicyProvider] over the real [coreauth] authenticators wherever the
// standard handler kinds are answerable, and plain [httpauth.Provider] values
// for the terminal-result shapes only a remote/custom chain can produce.
//
// This file owns the authentication half of the certification (the composed
// classification matrix, the shared-address sequence and the escalation ladder);
// selfdefense_acceptance_boundary_test.go owns the false-positive, spoofing and
// resource-isolation half.
//
// The lower layers own unit-level proofs already and none of it is repeated
// here: the state machine (internal/core/ingressdefense/state_test.go), the gate
// matcher and shared-NAT probe table
// (internal/stdhttp/selfdefense/middleware_test.go), the auth disposition matrix
// (internal/stdhttp/auth/selfdefense_observer_test.go), route non-collision
// (internal/stdhttp/selfdefense/route_collision_test.go), the GeoIP-wins
// ordering and management-listener isolation
// (internal/stdhttp/selfdefense_stack_test.go), and the composed reload,
// trusted-forwarding chain, shared-NAT round trip and process metrics
// (internal/infra/runtimebundle/self_defense_*_test.go). This file certifies only
// what those layers cannot observe: the guarantees as one composed sequence.
package stdhttp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
	coreconfig "github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/ingressdefense"
	stdauth "github.com/aiproxer/aiproxer/internal/stdhttp/auth"
	"github.com/aiproxer/aiproxer/internal/testkit"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

// acceptanceAPIKey is the only credential the local API-key chain of this file
// accepts. It satisfies the configured minimum key length and exists solely to
// make the real transport-auth chain answerable.
const acceptanceAPIKey = "sk-aip-acceptance-0123456789"

// acceptanceWrongKey is a well-formed credential the local API-key chain
// rejects, so a request carrying it exercises the terminal 401 path rather than
// the missing-credential path.
const acceptanceWrongKey = "sk-aip-acceptance-wrong-0001"

// acceptanceRoute is a legitimate LLM route of the mounted mux. It is not an
// impossible path, so a request for it may only be refused by authentication,
// never by the self-defense gate.
const acceptanceRoute = "/v1/chat/completions"

// acceptanceThreshold is the counted-failure threshold of every fixture. Two
// keeps a scenario short while still separating "below the threshold" from
// "quarantined".
const acceptanceThreshold = 2

// acceptanceNow is the fixed instant every acceptance fixture injects, so no
// wall-clock wait and no timing race is ever needed to observe a transition.
var acceptanceNow = time.Date(2026, time.March, 2, 12, 0, 0, 0, time.UTC)

// acceptanceClock is the injected instant of one composed graph. Advancing it is
// the only way a test moves time, so every deadline in this file is exact and no
// assertion ever depends on a wall-clock wait.
type acceptanceClock struct {
	mu  sync.Mutex
	now time.Time
}

func newAcceptanceClock() *acceptanceClock {
	return &acceptanceClock{now: acceptanceNow}
}

func (c *acceptanceClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *acceptanceClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// acceptanceAuthSink is a bounded auth-event sink. It discards the event and
// only counts it, so no request material can reach it, and its counter is what
// proves a real provider chain actually ran rather than being skipped.
type acceptanceAuthSink struct{ decisions int }

func (s *acceptanceAuthSink) OnAuthDecision(context.Context, sdkauth.AuthDecisionEvent) error {
	s.decisions++
	return nil
}

func (s *acceptanceAuthSink) OnSessionStart(context.Context, sdkauth.SessionStartEvent) error {
	return nil
}

// acceptanceOSIdentity is a fixed OS identity provider for the credential-free
// local-noop chain.
type acceptanceOSIdentity struct{ principal string }

func (o acceptanceOSIdentity) Current(context.Context) (coreauth.OSIdentitySnapshot, error) {
	return coreauth.OSIdentitySnapshot{PrincipalID: o.principal}, nil
}

// acceptanceStubProvider is a plain [httpauth.Provider] that returns one fixed
// result. It deliberately does NOT implement the private credential-presence
// capability of the standard provider, so the aggregate probe must fail open for
// any chain that contains it: this is the unknown/custom/future provider shape.
type acceptanceStubProvider struct {
	res   httpauth.AuthenticationResult
	err   error
	calls int
}

func (p *acceptanceStubProvider) Authenticate(context.Context, http.ResponseWriter, *http.Request) (httpauth.AuthenticationResult, error) {
	p.calls++
	return p.res, p.err
}

// acceptancePrincipal is the accepted result a provider chain establishes.
var acceptancePrincipal = httpauth.AuthenticationResult{
	Type:      httpauth.TypePrincipal,
	Principal: execview.PrincipalView{ID: "acceptance-user"},
}

// acceptanceChain is one transport-auth chain plus a bounded count of how many
// times it actually ran. "Reached authentication" is therefore proven by an
// invocation, never by a status code alone.
type acceptanceChain struct {
	providers []httpauth.Provider
	sink      *acceptanceAuthSink
	stubs     []*acceptanceStubProvider
}

func (c *acceptanceChain) invocations() int {
	if c == nil {
		return 0
	}
	total := 0
	for _, stub := range c.stubs {
		total += stub.calls
	}
	if c.sink != nil {
		total += c.sink.decisions
	}
	return total
}

// acceptanceLocalAPIKeyChain is the real standard local API-key provider: the
// only handler kind whose private probe can prove a request cannot authenticate
// as presented.
func acceptanceLocalAPIKeyChain(t *testing.T) *acceptanceChain {
	t.Helper()
	authenticator, err := coreauth.NewLocalAPIKeyAuthenticator([]coreauth.LocalAPIKeyRecord{
		{KeyID: "acceptance", PrincipalID: "acceptance-user", Key: acceptanceAPIKey},
	})
	if err != nil {
		t.Fatal(err)
	}
	sink := &acceptanceAuthSink{}
	policy := coreauth.PolicyAuthenticator{
		Handler:  sdkauth.HandlerLocalAPIKey,
		Required: sdkauth.LevelAPIKey,
		APIKey:   authenticator,
	}
	provider := stdauth.NewPolicyProvider(
		policy,
		coreauth.NewEventDispatcher(sink, coreauth.EventFailureBestEffort),
		stdauth.PolicySnapshot{
			AccessMode:    sdkauth.AccessMultiUser,
			HandlerKind:   sdkauth.HandlerLocalAPIKey,
			RequiredLevel: sdkauth.LevelAPIKey,
		},
		nil,
	)
	return &acceptanceChain{providers: []httpauth.Provider{provider}, sink: sink}
}

// acceptanceLocalNoopChain is the real credential-free standard provider. A
// request carrying no credential at all still authenticates, so the gate must
// never refuse one of its requests by source address.
func acceptanceLocalNoopChain(t *testing.T) *acceptanceChain {
	t.Helper()
	sink := &acceptanceAuthSink{}
	policy := coreauth.PolicyAuthenticator{
		Handler:  sdkauth.HandlerLocalNoop,
		Required: sdkauth.LevelNone,
		Noop:     coreauth.LocalNoOpAuthenticator{OS: acceptanceOSIdentity{principal: "acceptance-noop"}},
	}
	provider := stdauth.NewPolicyProvider(
		policy,
		coreauth.NewEventDispatcher(sink, coreauth.EventFailureBestEffort),
		stdauth.PolicySnapshot{
			AccessMode:    sdkauth.AccessSingleUser,
			HandlerKind:   sdkauth.HandlerLocalNoop,
			RequiredLevel: sdkauth.LevelNone,
		},
		nil,
	)
	return &acceptanceChain{providers: []httpauth.Provider{provider}, sink: sink}
}

// acceptanceStubChain is an unknown/custom/future chain: plain providers with no
// private credential-presence capability, so the gate must fail open for them.
func acceptanceStubChain(results ...*acceptanceStubProvider) *acceptanceChain {
	providers := make([]httpauth.Provider, 0, len(results))
	for _, stub := range results {
		providers = append(providers, stub)
	}
	return &acceptanceChain{providers: providers, stubs: results}
}

// acceptanceFrontend is the legitimate LLM route of the mounted mux. It records
// every request that reaches it together with the exact bytes it received, so a
// test can assert both that a route was reached and that the request arrived
// unmodified.
type acceptanceFrontend struct {
	calls      int
	lastMethod string
	lastTarget string
	lastAuth   string
	lastBody   []byte
	lastErr    error
}

func (f *acceptanceFrontend) record(r *http.Request, body []byte, err error) {
	f.calls++
	f.lastMethod = r.Method
	f.lastTarget = r.URL.Path
	f.lastAuth = r.Header.Get("Authorization")
	f.lastBody = body
	f.lastErr = err
}

func (f *acceptanceFrontend) post() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		f.record(r, body, err)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[]}`))
	})
}

func (f *acceptanceFrontend) models() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.record(r, nil, nil)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	})
}

// acceptanceStack is one fully composed standard data-plane handler graph with a
// real adaptive state, a real auth chain, and a real mounted LLM route.
type acceptanceStack struct {
	handler  http.Handler
	state    *ingressdefense.State
	policy   ingressdefense.Policy
	observer *selfDefenseObserverSpy
	frontend *acceptanceFrontend
}

// acceptanceOptions configures one composed acceptance stack.
type acceptanceOptions struct {
	// providers is the real transport-auth chain the graph runs.
	providers []httpauth.Provider
	// policy is the generation policy; nil uses the fixture policy.
	policy *ingressdefense.Policy
	// resolver is the shared client-address trust configuration.
	resolver GeoIPResolverConfig
	// geoPolicy installs a fixed GeoIP enforcement gate OUTSIDE the
	// self-defense gate, so the two can be compared for the same request.
	geoPolicy GeoIPSecurityInput
	// impossiblePaths toggles the fixed matcher; every fixture enables it,
	// because default-on impossible-path rejection is part of what is certified.
	impossiblePaths bool
	// inner, when non-nil, replaces the default mounted LLM route.
	inner http.Handler
	// clock, when non-nil, replaces the fixed acceptance instant and lets a
	// scenario advance time deterministically.
	clock *acceptanceClock
}

// newAcceptanceStack composes the real graph for one scenario.
func newAcceptanceStack(t *testing.T, opts acceptanceOptions) acceptanceStack {
	t.Helper()
	policy := acceptancePolicy(acceptanceThreshold)
	if opts.policy != nil {
		policy = *opts.policy
	}
	state := selfDefenseStackState(t)
	observer := &selfDefenseObserverSpy{}
	frontend := &acceptanceFrontend{}
	inner := opts.inner
	if inner == nil {
		mux := http.NewServeMux()
		mux.Handle("POST "+acceptanceRoute, frontend.post())
		mux.Handle("GET /v1/models", frontend.models())
		inner = mux
	}
	clock := opts.clock
	if clock == nil {
		clock = newAcceptanceClock()
	}
	security := HTTPSecurityInput{
		HTTPAuthProviders: opts.providers,
		GeoIP:             opts.geoPolicy,
		SelfDefense: SelfDefenseSecurityInput{
			Policy:          &policy,
			State:           state,
			Resolver:        opts.resolver,
			ImpossiblePaths: opts.impossiblePaths,
			// The probe is projected exactly as the composition root projects
			// it: the single fail-open aggregate over the very provider slice
			// the auth chain runs. A nil result (an unusable active set) is the
			// structural safe default.
			Probe:    stdauth.NewCredentialPresenceDispositionProbe(opts.providers),
			Observer: observer,
			Now:      clock.Now,
		},
	}
	handler := stackHTTPHandler(stackHTTPInput{
		Cfg:      &coreconfig.Config{},
		Log:      testkit.DiscardLogger(),
		TraceGen: diag.NewTraceIDGenerator(),
		Security: security,
		Inner:    inner,
	})
	return acceptanceStack{
		handler:  handler,
		state:    state,
		policy:   policy,
		observer: observer,
		frontend: frontend,
	}
}

// acceptancePolicy is the composed fixture policy: a small counted-failure
// threshold and a one-hour first-offense quarantine doubling to two hours, so a
// fresh offense and a resumed escalation are observably different with explicit
// fake instants and no wall-clock wait.
func acceptancePolicy(threshold int) ingressdefense.Policy {
	return ingressdefense.Policy{
		Enabled:           true,
		AuthFailures:      threshold,
		FailureWindow:     time.Minute,
		InitialQuarantine: time.Hour,
		MaxQuarantine:     2 * time.Hour,
	}
}

// acceptanceRequest builds one bodyless data-plane request from a direct peer.
// credential is written as a bearer token when non-empty. A request that presents
// forwarding headers uses acceptanceForwardedRequest instead, so the two trust
// scenarios can never be confused with one another.
func acceptanceRequest(method, target, remote, credential string) *http.Request {
	req := httptest.NewRequest(method, "http://example.test"+target, nil)
	req.RemoteAddr = remote
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	return req
}

// acceptanceHostPort renders a direct-peer RemoteAddr for a bare IP literal. An
// IPv6 literal must be bracketed, exactly as a real listener would deliver it.
func acceptanceHostPort(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]:443"
	}
	return host + ":443"
}

// acceptanceBodyRequest builds one data-plane request carrying a JSON body.
func acceptanceBodyRequest(target, remote, credential string, body []byte) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "http://example.test"+target, bytes.NewReader(body))
	req.RemoteAddr = remote
	req.Header.Set("Authorization", "Bearer "+credential)
	req.Header.Set("Content-Type", "application/json")
	return req
}

// acceptanceServe drives one request through a composed graph.
func acceptanceServe(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// acceptanceCountReasons counts the observations the graph reported for one
// closed reason.
func acceptanceCountReasons(reasons []ingressdefense.Reason, reason ingressdefense.Reason) int {
	n := 0
	for _, got := range reasons {
		if got == reason {
			n++
		}
	}
	return n
}

// acceptanceCountTransitions counts the quarantine transitions the graph
// observed for one closed reason.
func acceptanceCountTransitions(observer *selfDefenseObserverSpy, reason ingressdefense.Reason) int {
	return acceptanceCountReasons(observer.transitions, reason)
}

// TestAcceptanceAuthOutcomeClassificationThroughComposedStack is the
// composed-level classification matrix of requirement 4.2. Each case drives its
// own real auth chain from a fresh source address through the real graph until
// the fixture threshold, and then decides three things: the status the auth
// chain itself produced, whether the graph treated that outcome as counted
// hostile evidence, and whether a later credential-free request from the same
// address is refused in front of authentication or still reaches the chain.
//
// The counted/not-counted split is the whole requirement: only a terminal
// pre-principal 401 is hostile evidence. A 403 entitlement denial, a 5xx outcome,
// a provider error, and a request that already established an accepted principal
// are legitimate traffic that must not be quarantined, and a successful chain
// must leave no entry behind.
func TestAcceptanceAuthOutcomeClassificationThroughComposedStack(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		chain func(t *testing.T) *acceptanceChain
		// credential is presented on the counted requests; empty means none.
		credential string
		// wantCode is the status the auth chain itself must produce.
		wantCode int
		// wantCounted is whether the graph must treat that outcome as counted
		// hostile evidence for the source address.
		wantCounted bool
		// wantPreAuthRefusal is whether the later credential-free request from
		// the same address is refused IN FRONT OF authentication. A false value
		// is the safe default: an unprovable credential question must reach the
		// chain.
		wantPreAuthRefusal bool
		// wantFollowUp is the status of that credential-free request.
		wantFollowUp int
	}{
		{
			name:               "local API key with a valid credential authenticates and leaves no entry",
			chain:              acceptanceLocalAPIKeyChain,
			credential:         acceptanceAPIKey,
			wantCode:           http.StatusOK,
			wantCounted:        false,
			wantPreAuthRefusal: false,
			wantFollowUp:       http.StatusUnauthorized,
		},
		{
			name:               "local API key with a wrong credential is counted hostile evidence",
			chain:              acceptanceLocalAPIKeyChain,
			credential:         acceptanceWrongKey,
			wantCode:           http.StatusUnauthorized,
			wantCounted:        true,
			wantPreAuthRefusal: true,
			wantFollowUp:       http.StatusTooManyRequests,
		},
		{
			name:               "local noop authenticates a credential-free request and is never refused in front of auth",
			chain:              acceptanceLocalNoopChain,
			wantCode:           http.StatusOK,
			wantCounted:        false,
			wantPreAuthRefusal: false,
			wantFollowUp:       http.StatusOK,
		},
		{
			name: "an unknown custom provider still yields counted terminal 401 evidence but never a pre-auth refusal",
			chain: func(*testing.T) *acceptanceChain {
				return acceptanceStubChain(&acceptanceStubProvider{
					res: httpauth.AuthenticationResult{Type: httpauth.TypeReject, HTTPStatus: http.StatusUnauthorized},
				})
			},
			credential:         acceptanceWrongKey,
			wantCode:           http.StatusUnauthorized,
			wantCounted:        true,
			wantPreAuthRefusal: false,
			wantFollowUp:       http.StatusUnauthorized,
		},
		{
			name: "a custom provider that accepts clears the exact entry and keeps serving the address",
			chain: func(*testing.T) *acceptanceChain {
				return acceptanceStubChain(&acceptanceStubProvider{res: acceptancePrincipal})
			},
			wantCode:           http.StatusOK,
			wantCounted:        false,
			wantPreAuthRefusal: false,
			wantFollowUp:       http.StatusOK,
		},
		{
			name: "an established principal followed by a 401 is not counted",
			chain: func(*testing.T) *acceptanceChain {
				return acceptanceStubChain(
					&acceptanceStubProvider{res: acceptancePrincipal},
					&acceptanceStubProvider{res: httpauth.AuthenticationResult{Type: httpauth.TypeReject, HTTPStatus: http.StatusUnauthorized}},
				)
			},
			credential:         acceptanceWrongKey,
			wantCode:           http.StatusUnauthorized,
			wantCounted:        false,
			wantPreAuthRefusal: false,
			wantFollowUp:       http.StatusUnauthorized,
		},
		{
			name: "a 403 entitlement denial is not counted",
			chain: func(*testing.T) *acceptanceChain {
				return acceptanceStubChain(&acceptanceStubProvider{
					res: httpauth.AuthenticationResult{Type: httpauth.TypeReject, HTTPStatus: http.StatusForbidden, Body: []byte("entitlement denied")},
				})
			},
			credential:         acceptanceWrongKey,
			wantCode:           http.StatusForbidden,
			wantCounted:        false,
			wantPreAuthRefusal: false,
			wantFollowUp:       http.StatusForbidden,
		},
		{
			name: "a 5xx outcome is not counted",
			chain: func(*testing.T) *acceptanceChain {
				return acceptanceStubChain(&acceptanceStubProvider{
					res: httpauth.AuthenticationResult{Type: httpauth.TypeReject, HTTPStatus: http.StatusServiceUnavailable},
				})
			},
			credential:         acceptanceWrongKey,
			wantCode:           http.StatusServiceUnavailable,
			wantCounted:        false,
			wantPreAuthRefusal: false,
			wantFollowUp:       http.StatusServiceUnavailable,
		},
		{
			name: "a provider error is not counted",
			chain: func(*testing.T) *acceptanceChain {
				return acceptanceStubChain(&acceptanceStubProvider{err: errors.New("auth backend unreachable")})
			},
			credential:         acceptanceWrongKey,
			wantCode:           http.StatusInternalServerError,
			wantCounted:        false,
			wantPreAuthRefusal: false,
			wantFollowUp:       http.StatusInternalServerError,
		},
		{
			name: "a 401 challenge is counted terminal pre-principal evidence but never a pre-auth refusal",
			chain: func(*testing.T) *acceptanceChain {
				return acceptanceStubChain(&acceptanceStubProvider{
					res: httpauth.AuthenticationResult{Type: httpauth.TypeChallenge},
				})
			},
			credential:         acceptanceWrongKey,
			wantCode:           http.StatusUnauthorized,
			wantCounted:        true,
			wantPreAuthRefusal: false,
			wantFollowUp:       http.StatusUnauthorized,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			const host = "198.51.100.70"
			chain := tc.chain(t)
			stack := newAcceptanceStack(t, acceptanceOptions{
				providers:       chain.providers,
				impossiblePaths: true,
			})
			for i := range stack.policy.AuthFailures {
				rec := acceptanceServe(stack.handler,
					acceptanceRequest(http.MethodPost, acceptanceRoute, host+":443", tc.credential))
				if rec.Code != tc.wantCode {
					t.Fatalf("request %d status = %d, want the auth chain's own %d", i+1, rec.Code, tc.wantCode)
				}
			}
			addr := netip.MustParseAddr(host)
			if quarantined := stack.state.IsQuarantined(addr, acceptanceNow); quarantined != tc.wantCounted {
				t.Fatalf("quarantined = %v after %d requests presenting credential %q, want %v",
					quarantined, stack.policy.AuthFailures, tc.credential, tc.wantCounted)
			}
			if got := acceptanceCountTransitions(stack.observer, ingressdefense.ReasonAuthFailureThreshold); (got == 1) != tc.wantCounted {
				t.Fatalf("auth-failure-threshold transitions = %d, want counted=%v (%v)", got, tc.wantCounted, stack.observer.transitions)
			}
			if tc.wantCounted && stack.state.Len() == 0 {
				t.Fatal("a counted offense must leave the exact-address entry behind")
			}
			if !tc.wantCounted && stack.state.Len() != 0 {
				t.Fatalf("adaptive entries = %d, want 0: a non-counted outcome must leave no state", stack.state.Len())
			}

			// A credential-free follow-up from the same address decides the
			// shared-address boundary: the gate may refuse it in front of
			// authentication only when the whole chain provably cannot
			// authenticate it as presented.
			invocationsBefore := chain.invocations()
			rec := acceptanceServe(stack.handler, acceptanceRequest(http.MethodPost, acceptanceRoute, host+":443", ""))
			if rec.Code != tc.wantFollowUp {
				t.Fatalf("credential-free follow-up = %d, want %d", rec.Code, tc.wantFollowUp)
			}
			if refusedInFrontOfAuth := rec.Code == http.StatusTooManyRequests; refusedInFrontOfAuth != tc.wantPreAuthRefusal {
				t.Fatalf("credential-free follow-up refused in front of auth = %v (status %d), want %v",
					refusedInFrontOfAuth, rec.Code, tc.wantPreAuthRefusal)
			}
			if !tc.wantPreAuthRefusal && chain.invocations() == invocationsBefore {
				t.Fatal("a request that is not refused in front of authentication must actually reach the auth chain")
			}

			// Route reachability is asserted positively: only an authenticated
			// request may reach the mounted LLM route, and it must reach it once
			// per such request.
			wantRouteCalls := 0
			if tc.wantCode == http.StatusOK {
				wantRouteCalls += stack.policy.AuthFailures
			}
			if tc.wantFollowUp == http.StatusOK {
				wantRouteCalls++
			}
			if got := stack.frontend.calls; got != wantRouteCalls {
				t.Fatalf("route calls = %d, want %d: authentication must be the only thing that keeps a request off the route", got, wantRouteCalls)
			}
		})
	}
}

// TestAcceptanceSharedAddressValidCredentialReachesTheLegitimateRoute is the
// composed shared-NAT scenario of requirements 5.1-5.3, driven as one request
// sequence. One hostile actor at address X drives X past the auth-failure
// threshold; the conservative probe then refuses X's own credential-free traffic
// in front of authentication; and a legitimate user behind the SAME public
// address still authenticates, is served by the mounted LLM route, and clears
// X's exact adaptive entry.
//
// The success leg is asserted positively twice: the real auth chain must have
// actually run, and the mounted route must have observed the authenticated
// request, so a status check alone could not pass if the gate had refused or
// short-circuited it.
func TestAcceptanceSharedAddressValidCredentialReachesTheLegitimateRoute(t *testing.T) {
	t.Parallel()

	const hostile = "198.51.100.71"
	chain := acceptanceLocalAPIKeyChain(t)
	stack := newAcceptanceStack(t, acceptanceOptions{
		providers:       chain.providers,
		impossiblePaths: true,
	})

	// A brute-force actor on the shared address: one wrong credential per
	// request until the configured threshold starts a quarantine.
	for i := range stack.policy.AuthFailures {
		rec := acceptanceServe(stack.handler,
			acceptanceRequest(http.MethodPost, acceptanceRoute, hostile+":443", acceptanceWrongKey))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("hostile request %d = %d, want the auth chain's 401", i+1, rec.Code)
		}
	}
	addr := netip.MustParseAddr(hostile)
	if !stack.state.IsQuarantined(addr, acceptanceNow) {
		t.Fatal("the counted unauthenticated failures must start a quarantine for the shared address")
	}
	if got := acceptanceCountTransitions(stack.observer, ingressdefense.ReasonAuthFailureThreshold); got != 1 {
		t.Fatalf("auth-failure-threshold transitions = %d, want exactly 1", got)
	}

	// The quarantine now refuses the attacker's own credential-free traffic in
	// front of authentication, which is the pre-auth capability of the feature.
	before := chain.invocations()
	rec := acceptanceServe(stack.handler, acceptanceRequest(http.MethodPost, acceptanceRoute, hostile+":443", ""))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("credential-free request from a quarantined address = %d, want the generic 429", rec.Code)
	}
	if rec.Body.String() != "Too Many Requests\n" {
		t.Fatalf("quarantine refusal body = %q, want the generic body", rec.Body.String())
	}
	if stack.frontend.calls != 0 {
		t.Fatalf("route calls = %d, want 0: a pre-auth refusal must not reach the route", stack.frontend.calls)
	}
	if chain.invocations() != before {
		t.Fatal("a pre-auth refusal must not invoke the auth chain")
	}

	// The legitimate user on the SAME address presents a valid credential.
	rec = acceptanceServe(stack.handler, acceptanceRequest(http.MethodPost, acceptanceRoute, hostile+":443", acceptanceAPIKey))
	if rec.Code != http.StatusOK {
		t.Fatalf("valid credential on a quarantined address = %d, want the mounted LLM route's 200", rec.Code)
	}
	if chain.invocations() != before+1 {
		t.Fatalf("auth chain invocations = %d, want exactly one more: the valid credential must reach authentication", chain.invocations())
	}
	if stack.frontend.calls != 1 {
		t.Fatalf("route calls = %d, want 1: the valid credential must reach the legitimate route", stack.frontend.calls)
	}
	if got := stack.frontend.lastAuth; got != "Bearer "+acceptanceAPIKey {
		t.Fatalf("route observed credential header %q, want the presented valid credential", got)
	}
	if got := stack.frontend.lastTarget; got != acceptanceRoute {
		t.Fatalf("route observed target %q, want %q", got, acceptanceRoute)
	}
	if got := stack.state.Len(); got != 0 {
		t.Fatalf("adaptive entries after a full successful chain = %d, want the exact address cleared", got)
	}
}

// TestAcceptanceAuthEscalationThroughComposedStack certifies the auth escalation
// invariant of the acceptance matrix (requirements 4.3-4.4) through the composed
// graph and the paired auth observer, with the injected instant making every
// deadline exact.
//
// Each cycle drives the configured threshold of counted unauthenticated
// failures from the same address. The first cycle must quarantine for the
// initial window, the second must extend it to the doubled window, and further
// cycles must saturate at the configured maximum instead of running away. The
// cycles still carry a credential, which is the shared-address rule: the gate
// may never refuse a credential-bearing request in front of authentication, so
// every counted request really does reach the auth chain.
func TestAcceptanceAuthEscalationThroughComposedStack(t *testing.T) {
	t.Parallel()

	const hostile = "198.51.100.75"
	chain := acceptanceLocalAPIKeyChain(t)
	stack := newAcceptanceStack(t, acceptanceOptions{
		providers:       chain.providers,
		impossiblePaths: true,
	})
	addr := netip.MustParseAddr(hostile)
	initial, ceiling := stack.policy.InitialQuarantine, stack.policy.MaxQuarantine

	cycle := func() {
		t.Helper()
		for i := range stack.policy.AuthFailures {
			rec := acceptanceServe(stack.handler,
				acceptanceRequest(http.MethodPost, acceptanceRoute, hostile+":443", acceptanceWrongKey))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("counted request %d = %d, want the auth chain's 401: a credential-bearing request must reach authentication", i+1, rec.Code)
			}
		}
		if got := stack.state.Len(); got != 1 {
			t.Fatalf("adaptive entries = %d, want exactly the hostile address", got)
		}
	}

	// Cycle 1: the threshold starts the first-offense window.
	cycle()
	if !stack.state.IsQuarantined(addr, acceptanceNow.Add(initial/2)) {
		t.Fatal("the first threshold crossing must quarantine for the initial window")
	}
	if stack.state.IsQuarantined(addr, acceptanceNow.Add(initial+time.Minute)) {
		t.Fatal("the first threshold crossing must not already reach the doubled window")
	}

	// Cycle 2: a further qualifying offense doubles the window.
	cycle()
	if !stack.state.IsQuarantined(addr, acceptanceNow.Add(initial+30*time.Minute)) {
		t.Fatal("a second threshold crossing must escalate into the doubled window")
	}
	if stack.state.IsQuarantined(addr, acceptanceNow.Add(initial+initial+time.Minute)) {
		t.Fatal("the second threshold crossing must stay at the doubled window")
	}

	// Further cycles saturate at the configured ceiling instead of running away.
	for range 3 {
		cycle()
	}
	if !stack.state.IsQuarantined(addr, acceptanceNow.Add(ceiling-30*time.Minute)) {
		t.Fatal("the backoff must saturate at the configured maximum window")
	}
	if stack.state.IsQuarantined(addr, acceptanceNow.Add(ceiling+time.Minute)) {
		t.Fatal("the backoff must never exceed the configured maximum window")
	}
	// Each threshold crossing is exactly one finite-reason transition, and each
	// counted request really did run the auth chain.
	if got := acceptanceCountTransitions(stack.observer, ingressdefense.ReasonAuthFailureThreshold); got != 5 {
		t.Fatalf("auth-failure-threshold transitions = %d, want one per threshold crossing (5)", got)
	}
	if got, want := chain.invocations(), 5*stack.policy.AuthFailures; got != want {
		t.Fatalf("auth chain invocations = %d, want one per counted request (%d)", got, want)
	}
	// While quarantined, a credential-free request is still refused in front of
	// authentication: the escalation does not weaken the pre-auth capability.
	rec := acceptanceServe(stack.handler, acceptanceRequest(http.MethodPost, acceptanceRoute, hostile+":443", ""))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("credential-free request during an escalated quarantine = %d, want the generic 429", rec.Code)
	}
}

// TestAcceptanceQuarantineAndStateExpiryNeedNoOperatorAction certifies
// requirements 4.5 and 4.6 through the composed graph. A quarantine is
// temporary: once its deadline passes the gate stops treating the source as
// quarantined, so the very same credential-free request that was refused with the
// generic 429 now reaches the auth chain again, with no operator action and no
// restart. And once the source has been hostile-inactive for the state TTL, its
// entry and its accumulated offense level are gone, so the same address starts
// from a clean slate rather than resuming an old escalation.
//
// Time only moves through the injected clock, so every boundary below is exact.
func TestAcceptanceQuarantineAndStateExpiryNeedNoOperatorAction(t *testing.T) {
	t.Parallel()

	const hostile = "198.51.100.76"
	clock := newAcceptanceClock()
	chain := acceptanceLocalAPIKeyChain(t)
	stack := newAcceptanceStack(t, acceptanceOptions{
		providers:       chain.providers,
		impossiblePaths: true,
		clock:           clock,
	})
	addr := netip.MustParseAddr(hostile)

	// One hostile probe starts the first-offense quarantine.
	rec := acceptanceServe(stack.handler, acceptanceRequest(http.MethodGet, "/.env", hostile+":443", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("impossible path = %d, want the generic 404", rec.Code)
	}
	if !stack.state.IsQuarantined(addr, clock.Now()) {
		t.Fatal("the hostile probe must start a quarantine")
	}
	rec = acceptanceServe(stack.handler, acceptanceRequest(http.MethodPost, acceptanceRoute, hostile+":443", ""))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("credential-free request inside the quarantine = %d, want the generic 429", rec.Code)
	}

	// Requirement 4.5: the deadline passes and the source is served again, with
	// no operator action. The entry is still tracked; only the quarantine ended.
	clock.Advance(stack.policy.InitialQuarantine + time.Minute)
	invocations := chain.invocations()
	rec = acceptanceServe(stack.handler, acceptanceRequest(http.MethodPost, acceptanceRoute, hostile+":443", ""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("credential-free request after the quarantine deadline = %d, want the auth chain's 401: expiry must need no operator action", rec.Code)
	}
	if chain.invocations() != invocations+1 {
		t.Fatal("the expired source must reach the auth chain again")
	}
	if got := stack.state.Len(); got != 1 {
		t.Fatalf("adaptive entries = %d, want 1: the entry outlives its own quarantine", got)
	}

	// Requirement 4.6: after the hostile-inactivity TTL the entry and its
	// accumulated offense level are gone. The decisive observable is the WINDOW
	// LENGTH of the next quarantine: with the level still accumulated it would be
	// the doubled window, and only a genuinely expired entry restarts at the
	// first-offense window. The counted 401 above is the hostile event that sets
	// the TTL baseline, so the clock is advanced from exactly there. No lookup
	// happens in between, so the next hostile event itself is the first thing to
	// touch the expired entry.
	clock.Advance(24*time.Hour + time.Minute)
	rec = acceptanceServe(stack.handler, acceptanceRequest(http.MethodGet, "/.env", hostile+":443", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("impossible path after the state TTL = %d, want the generic 404", rec.Code)
	}
	if !stack.state.IsQuarantined(addr, clock.Now().Add(stack.policy.InitialQuarantine/2)) {
		t.Fatal("a post-TTL hostile event must build a quarantine again")
	}
	if stack.state.IsQuarantined(addr, clock.Now().Add(stack.policy.InitialQuarantine+time.Minute)) {
		t.Fatal("a post-TTL hostile event must restart at the first-offense window: the expired offense level must not resume")
	}
}

// TestAcceptanceSuccessIsNotPermanentTrust pins requirement 5.5. A successful
// authentication is a reset, never a trust cache: after the clear, the very same
// address is refused deterministically for an impossible path even while
// presenting a valid credential, and the later hostile event builds FRESH
// adaptive state. Freshness is proven observably, not asserted: a fresh
// first-offense quarantine ends after the initial window, while a state that
// had silently retained its pre-clear offense level would still be escalating
// into the doubled window.
func TestAcceptanceSuccessIsNotPermanentTrust(t *testing.T) {
	t.Parallel()

	const shared = "198.51.100.72"
	chain := acceptanceLocalAPIKeyChain(t)
	stack := newAcceptanceStack(t, acceptanceOptions{
		providers:       chain.providers,
		impossiblePaths: true,
	})
	addr := netip.MustParseAddr(shared)

	rec := acceptanceServe(stack.handler, acceptanceRequest(http.MethodPost, acceptanceRoute, shared+":443", acceptanceAPIKey))
	if rec.Code != http.StatusOK {
		t.Fatalf("valid credential = %d, want 200", rec.Code)
	}
	if got := chain.invocations(); got != 1 {
		t.Fatalf("auth chain invocations = %d, want exactly 1 for the successful request", got)
	}
	if got := stack.state.Len(); got != 0 {
		t.Fatalf("adaptive entries after the success = %d, want 0", got)
	}

	// A prior success must not exempt a later deterministic refusal, and it must
	// not even matter that the request carries a valid credential.
	rec = acceptanceServe(stack.handler, acceptanceRequest(http.MethodGet, "/.env", shared+":443", acceptanceAPIKey))
	if rec.Code != http.StatusNotFound || rec.Body.String() != "Not Found\n" {
		t.Fatalf("impossible path after a success = %d %q, want the generic 404", rec.Code, rec.Body.String())
	}
	if got := chain.invocations(); got != 1 {
		t.Fatalf("auth chain invocations = %d, want the deterministic refusal to precede authentication", got)
	}
	if got := stack.frontend.calls; got != 1 {
		t.Fatalf("route calls = %d, want 1: the deterministic refusal must precede the route", got)
	}
	if got := acceptanceCountTransitions(stack.observer, ingressdefense.ReasonImpossiblePath); got != 1 {
		t.Fatalf("impossible-path transitions = %d, want exactly 1 fresh offense", got)
	}

	// The state built after the clear is a first-offense state, not a resumed
	// escalation: the initial window is active and the doubled window is not.
	if !stack.state.IsQuarantined(addr, acceptanceNow) {
		t.Fatal("a hostile event after a successful chain must build fresh adaptive state")
	}
	if !stack.state.IsQuarantined(addr, acceptanceNow.Add(stack.policy.InitialQuarantine-time.Minute)) {
		t.Fatal("the fresh first-offense window must still be active one minute in")
	}
	if stack.state.IsQuarantined(addr, acceptanceNow.Add(stack.policy.InitialQuarantine+time.Minute)) {
		t.Fatal("the fresh state must stop at the first-offense window: a cleared entry may not resume its old offense level")
	}
	if got := stack.state.Len(); got != 1 {
		t.Fatalf("adaptive entries = %d, want exactly the fresh entry for the shared address", got)
	}
}
