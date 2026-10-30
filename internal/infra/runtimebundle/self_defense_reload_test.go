package runtimebundle_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/configreload"
	"github.com/aiproxer/aiproxer/internal/core/ingressdefense"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/internal/testkit/localstubreg"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"gopkg.in/yaml.v3"
)

// This file certifies the composition/reload half of ingress self-defense
// (requirements 1.3, 7.2-7.6, 8.4, 10.5): policy-only reload through the real
// standard HTTP composition, atomic last-good rollback for invalid and
// restart-required candidates, and the process/generation split of adaptive
// state. Every request here travels through the real
// [stdhttp.ComposeStandardHTTP] handler graph and the real
// [runtimehost.GenerationDispatcher] publication path; nothing re-reads a policy
// after the fact to decide an outcome.

// sdReloadInitial is the configured first-offense quarantine of the shared
// fixture. It is short enough that a test can distinguish one-offense (10s) from
// two-offense (20s) backoff with explicit fake instants, and long enough that no
// wall-clock wait is ever required.
const sdReloadInitial = 10 * time.Second

// sdReloadPolicy is the reloadable policy every fixture generation compiles
// from. It mirrors the process-configured adaptive parameters so a test can seed
// or inspect process state with the same policy a generation would use.
func sdReloadPolicy(exempt ...string) ingressdefense.Policy {
	policy := ingressdefense.Policy{
		Enabled:           true,
		AuthFailures:      5,
		FailureWindow:     time.Minute,
		InitialQuarantine: sdReloadInitial,
		MaxQuarantine:     2 * time.Hour,
	}
	for _, cidr := range exempt {
		policy.AdaptiveExemptCIDRs = append(policy.AdaptiveExemptCIDRs, netip.MustParsePrefix(cidr))
	}
	return policy
}

// sdReloadConfig builds one valid, fully defaulted standard-distribution config
// with ingress self-defense present. mutate runs before validation, so every
// fixture starts from a config whose non-self-defense sections are byte-for-byte
// identical: a candidate built from it therefore differs only in the fields the
// test actually changed, and reload classification cannot be confused by
// unrelated defaulted sections.
func sdReloadConfig(t *testing.T, mutate func(*config.Config)) *config.Config {
	t.Helper()
	var backend yaml.Node
	if err := yaml.Unmarshal([]byte("text: \"ok\"\ninput_tokens: 1\noutput_tokens: 1\n"), &backend); err != nil {
		t.Fatal(err)
	}
	for backend.Kind == yaml.DocumentNode && len(backend.Content) > 0 {
		backend = *backend.Content[0]
	}
	cfg := &config.Config{
		Access: config.AccessConfig{
			Mode: "single_user",
			// Fixed GeoIP enforcement stays off: self-defense must still receive
			// the shared client-address trust configuration (requirements 2.1,
			// 8.5) without any country database.
			GeoIP: config.GeoIPConfig{
				ClientIP: config.GeoIPClientConfig{
					Source:         config.ClientIPSourceXForwardedFor,
					TrustedProxies: []string{"10.0.0.0/8"},
				},
			},
			SelfDefense: config.SelfDefenseConfig{
				Adaptive: config.SelfDefenseAdaptiveConfig{
					AuthFailures:      new(5),
					Window:            "1m",
					InitialQuarantine: sdReloadInitial.String(),
					MaxQuarantine:     "2h",
					StateTTL:          "24h",
					MaxEntries:        new(1024),
				},
			},
		},
		// local_api_key is the only handler kind whose credential-presence probe
		// can prove a request cannot authenticate as presented, so it is the
		// fixture that makes the shared-address quarantine refusal observable.
		Auth: config.AuthConfig{
			Handler:      "local_api_key",
			LocalAPIKeys: []config.AuthLocalAPIKeyRecord{{KeyID: "k1", PrincipalID: "p1", Key: "sk-aip-self-defense-0123456789"}},
		},
		Routing:     config.RoutingConfig{MaxAttempts: 3, DefaultRoute: "sd-reload-stub:stub-default"},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes:    1024,
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 4096,
		},
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
			Backends: []config.PluginConfig{
				{ID: "openai-responses", Enabled: false},
				{Kind: "local-stub", ID: "sd-reload-stub", Enabled: true, Config: backend},
			},
		},
	}
	if mutate != nil {
		mutate(cfg)
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("fixture config invalid: %v", err)
	}
	return cfg
}

// sdReloadClone copies an already validated startup config and applies one
// deliberate change to it. It is how an invalid candidate is produced: the
// result differs from the active config in exactly the field under test, so the
// rejection can only come from that field's own validation.
func sdReloadClone(base *config.Config, mutate func(*config.Config)) *config.Config {
	out := *base
	mutate(&out)
	return &out
}

// sdReloadHost starts the one process that owns the adaptive state. Its startup
// config fixes the restart-required capacity/TTL and is the baseline every
// candidate is classified against.
func sdReloadHost(t *testing.T, cfg *config.Config) *runtimebundle.ProcessServices {
	t.Helper()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	if err := localstubreg.RegisterInProcess(reg); err != nil {
		t.Fatal(err)
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	if ps.IngressDefense == nil {
		t.Fatal("ProcessServices must own the process-lifetime adaptive state")
	}
	return ps
}

// sdReloadGeneration is one compiled immutable generation: the real standard
// data-plane handler plus the exact self-defense projection the generation
// admitted, so a test can compare the policies two generations published.
type sdReloadGeneration struct {
	plane  *sdReloadPlane
	policy *ingressdefense.Policy
	state  *ingressdefense.State
}

// sdReloadPlane is a minimal published request plane. It exists only so a test
// can attach a barrier in front of one generation's composed handler; the
// barrier is a channel gate in the test harness, never a sleep.
type sdReloadPlane struct {
	handler http.Handler
	cand    *runtimebundle.CandidateHTTPCompile
}

func (p *sdReloadPlane) Handler() http.Handler {
	if p == nil {
		return http.NotFoundHandler()
	}
	return p.handler
}

func (p *sdReloadPlane) Quiesce(context.Context) error { return nil }

func (p *sdReloadPlane) Close() error {
	if p == nil || p.cand == nil {
		return nil
	}
	return p.cand.Close()
}

// sdReloadCompile compiles one candidate through the real publication-time
// candidate compiler and composes the real standard HTTP handler graph for it.
// barrier, when non-nil, is installed in front of the composed handler only for
// this generation.
func sdReloadCompile(t *testing.T, ps *runtimebundle.ProcessServices, cfg *config.Config, barrier *sdReloadBarrier) sdReloadGeneration {
	t.Helper()
	cand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: cfg, Compose: selfDefenseStubComposer,
	})
	if err != nil {
		t.Fatalf("CompileCandidate: %v", err)
	}
	input := cand.StandardHTTPInput(cfg, nil, "")
	handler, err := stdhttp.ComposeStandardHTTP(context.Background(), cfg, testkit.DiscardLogger(), input)
	if err != nil {
		_ = cand.Close()
		t.Fatalf("ComposeStandardHTTP: %v", err)
	}
	plane := &sdReloadPlane{handler: handler, cand: cand}
	t.Cleanup(func() { _ = plane.Close() })
	if barrier != nil {
		plane.handler = barrier.Wrap(handler)
	}
	var policy *ingressdefense.Policy
	if projected := input.Security.SelfDefense.Policy; projected != nil {
		copied := *projected
		policy = &copied
	}
	return sdReloadGeneration{plane: plane, policy: policy, state: input.Security.SelfDefense.State}
}

// sdReloadBarrier parks the first request that reaches one generation's composed
// handler until the test releases it. It is installed inside the published
// generation, so the generation lease is already acquired when the request
// parks: that is what makes an in-flight request's admitted-generation pinning
// observable.
type sdReloadBarrier struct {
	entered   chan struct{}
	release   chan struct{}
	enterOnce sync.Once
	once      sync.Once
}

func newSDReloadBarrier() *sdReloadBarrier {
	return &sdReloadBarrier{entered: make(chan struct{}), release: make(chan struct{})}
}

func (b *sdReloadBarrier) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.enterOnce.Do(func() { close(b.entered) })
		<-b.release
		next.ServeHTTP(w, r)
	})
}

// Release unblocks the parked request. The barrier then admits no further
// request, so later assertions run against the released generation normally.
func (b *sdReloadBarrier) Release() { b.once.Do(func() { close(b.release) }) }

// sdReloadRequest builds one data-plane request from a trusted proxy hop, the
// fixture's only forwarding-trust configuration.
func sdReloadRequest(target, clientAddr string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "http://data-plane.test"+target, nil)
	req.RemoteAddr = "10.0.0.9:1234"
	req.Header.Set("X-Forwarded-For", clientAddr)
	return req
}

func sdReloadServe(t *testing.T, h http.Handler, target, clientAddr string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, sdReloadRequest(target, clientAddr))
	return rec
}

// sdReloadChurn returns n distinct documentation-range source addresses for
// bounded-capacity assertions.
func sdReloadChurn(n int) []netip.Addr {
	out := make([]netip.Addr, 0, n)
	for i := range n {
		out = append(out, netip.AddrFrom4([4]byte{198, 18, byte(i / 254), byte(i%254 + 1)}))
	}
	return out
}

// TestSelfDefenseOmittedDefaultsOnAndExplicitFalseDisablesPerGeneration proves
// requirement 1.2 through the real composition: an omitted access.self_defense
// block makes the gate refuse an impossible path, an explicit enabled=false
// candidate publishes a generation whose gate is structurally absent (the same
// request reaches transport auth and mutates no adaptive state), and a further
// omitted candidate restores the default-on gate. The fast path therefore
// appears and disappears per generation, not per process.
func TestSelfDefenseOmittedDefaultsOnAndExplicitFalseDisablesPerGeneration(t *testing.T) {
	t.Parallel()

	base := sdReloadConfig(t, nil)
	ps := sdReloadHost(t, base)
	disabled := sdReloadClone(base, func(c *config.Config) { c.Access.SelfDefense.Enabled = new(false) })
	if err := config.Validate(disabled); err != nil {
		t.Fatalf("disabled candidate invalid: %v", err)
	}

	first := sdReloadCompile(t, ps, base, nil)
	firstAddr := "203.0.113.1"
	if first.policy == nil || !first.policy.Enabled {
		t.Fatalf("omitted config must project the documented default-on policy, got %+v", first.policy)
	}
	rec := sdReloadServe(t, first.plane.Handler(), "/.env", firstAddr)
	if rec.Code != http.StatusNotFound || rec.Body.String() != "Not Found\n" {
		t.Fatalf("omitted config response = %d %q, want the self-defense generic 404", rec.Code, rec.Body.String())
	}
	if !ps.IngressDefense.IsQuarantined(netip.MustParseAddr(firstAddr), time.Now()) {
		t.Fatal("the default-on gate must record the impossible-path offense")
	}
	if got := ps.IngressDefense.Len(); got != 1 {
		t.Fatalf("entries = %d, want the single recorded offense", got)
	}

	second := sdReloadCompile(t, ps, disabled, nil)
	if second.plane == first.plane {
		t.Fatal("a reload must publish a new immutable handler graph")
	}
	if second.policy != nil {
		t.Fatalf("an explicitly disabled generation must project no self-defense policy, got %+v", second.policy)
	}
	rec = sdReloadServe(t, second.plane.Handler(), "/.env", "203.0.113.2")
	if rec.Code == http.StatusNotFound || rec.Body.String() == "Not Found\n" {
		t.Fatalf("disabled response = %d %q, want the request to pass the absent gate", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("disabled response = %d, want the existing transport-auth 401 to own the request", rec.Code)
	}
	if got := ps.IngressDefense.Len(); got != 1 {
		t.Fatalf("entries = %d, want a disabled generation to perform no path matching and no adaptive lookup", got)
	}
	if !ps.IngressDefense.IsQuarantined(netip.MustParseAddr(firstAddr), time.Now()) {
		t.Fatal("a disabled generation must not clear the quarantine recorded by the enabled one")
	}

	third := sdReloadCompile(t, ps, base, nil)
	if third.policy == nil {
		t.Fatal("a re-omitted configuration must project the default-on policy again")
	}
	rec = sdReloadServe(t, third.plane.Handler(), "/.env", "203.0.113.3")
	if rec.Code != http.StatusNotFound || rec.Body.String() != "Not Found\n" {
		t.Fatalf("re-omitted response = %d %q, want the default-on gate restored", rec.Code, rec.Body.String())
	}
	if got := ps.IngressDefense.Len(); got != 2 {
		t.Fatalf("entries = %d, want the restored gate to record exactly one new offense", got)
	}
	if first.state != third.state {
		t.Fatal("every enabled generation must borrow the one process-owned adaptive state")
	}
	if second.state == ps.IngressDefense {
		t.Fatal("a disabled generation must not even borrow the process-owned state")
	}
}

// TestSelfDefensePolicyOnlyReloadKeepsProcessStateAndEscalatesSameEntry proves
// requirements 7.3 and 7.6 at the state level: a policy-only reload publishes a
// new generation without touching the process-owned adaptive state, and an
// offense recorded against the surviving entry before the reload is escalated
// (not restarted) by the new generation. A reload that REPLACED or RESIZED the
// state would downgrade the second offense back to the first-offense window,
// which the explicit fake instants below detect. This test seeds its offense
// after both generations have compiled, so it does not by itself observe a
// compile-time in-place clear of the state table; that direction is covered by
// TestManagementRecoveryListenerStaysUsableAfterSelfDefenseCandidateFailures.
func TestSelfDefensePolicyOnlyReloadKeepsProcessStateAndEscalatesSameEntry(t *testing.T) {
	t.Parallel()

	base := sdReloadConfig(t, nil)
	ps := sdReloadHost(t, base)
	reloaded := sdReloadClone(base, func(c *config.Config) { c.Access.SelfDefense.Adaptive.AuthFailures = new(3) })
	if err := config.Validate(reloaded); err != nil {
		t.Fatalf("reloaded candidate invalid: %v", err)
	}

	first := sdReloadCompile(t, ps, base, nil)
	reloadedGen := sdReloadCompile(t, ps, reloaded, nil)
	if first.policy == nil || reloadedGen.policy == nil {
		t.Fatal("both generations must project an enabled self-defense policy")
	}
	if first.policy.AuthFailures == reloadedGen.policy.AuthFailures {
		t.Fatal("the fixture must actually reload a different policy")
	}
	if first.state != ps.IngressDefense || reloadedGen.state != ps.IngressDefense {
		t.Fatal("both generations must borrow the identical process-owned state instance")
	}

	addr := netip.MustParseAddr("203.0.113.20")
	start := time.Now()
	ps.IngressDefense.RecordProbe(addr, start, *first.policy)
	if got := ps.IngressDefense.Len(); got != 1 {
		t.Fatalf("entries = %d, want the single pre-reload offense", got)
	}
	if !ps.IngressDefense.IsQuarantined(addr, start.Add(5*time.Second)) {
		t.Fatal("one offense must quarantine for the configured initial window")
	}
	if ps.IngressDefense.IsQuarantined(addr, start.Add(15*time.Second)) {
		t.Fatal("one offense must not reach the doubled quarantine window")
	}

	rec := sdReloadServe(t, reloadedGen.plane.Handler(), "/.env", addr.String())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("reloaded generation response = %d, want the generic 404", rec.Code)
	}
	if got := ps.IngressDefense.Len(); got != 1 {
		t.Fatalf("entries = %d, want the reloaded generation to escalate the surviving entry, not add one", got)
	}
	if !ps.IngressDefense.IsQuarantined(addr, start.Add(15*time.Second)) {
		t.Fatal("a second offense after a policy-only reload must escalate to the doubled window")
	}
	if ps.IngressDefense.IsQuarantined(addr, start.Add(25*time.Second)) {
		t.Fatal("the escalated quarantine must still saturate at the configured backoff, not run away")
	}
}

// TestSelfDefenseInFlightRequestKeepsAdmittedGenerationPolicyAcrossPublication
// is the in-flight generation proof (requirement 7.3). The reload adds an
// adaptive exemption the old generation does not have, so the two generations
// disagree about the very same source address. One request is admitted to the
// first generation and parked inside it (its generation lease is already held);
// the new generation is then published and serves its own request for the same
// address; only afterwards is the parked request released. The parked request
// must still be refused-and-recorded under the old policy, while the active
// generation keeps treating the address as adaptively exempt.
func TestSelfDefenseInFlightRequestKeepsAdmittedGenerationPolicyAcrossPublication(t *testing.T) {
	t.Parallel()

	base := sdReloadConfig(t, nil)
	ps := sdReloadHost(t, base)
	exempt := sdReloadClone(base, func(c *config.Config) {
		c.Access.SelfDefense.Adaptive.ExemptCIDRs = []string{"203.0.113.30/32"}
		c.Access.SelfDefense.Adaptive.AuthFailures = new(3)
	})
	if err := config.Validate(exempt); err != nil {
		t.Fatalf("exempt candidate invalid: %v", err)
	}

	barrier := newSDReloadBarrier()
	first := sdReloadCompile(t, ps, base, barrier)
	second := sdReloadCompile(t, ps, exempt, nil)
	if first.policy == nil || second.policy == nil {
		t.Fatal("both generations must project an enabled self-defense policy")
	}
	if first.policy.AuthFailures == second.policy.AuthFailures {
		t.Fatal("the two generations must publish observably different policies")
	}
	if len(second.policy.AdaptiveExemptCIDRs) != 1 {
		t.Fatalf("reloaded exemptions = %v, want the single /32", second.policy.AdaptiveExemptCIDRs)
	}
	if len(first.policy.AdaptiveExemptCIDRs) != 0 {
		t.Fatalf("admitted generation exemptions = %v, want none", first.policy.AdaptiveExemptCIDRs)
	}

	mgr := runtimehost.NewManager(4, nil)
	if err := mgr.Publish(mgr.PrepareRequestPlane("g1", first.plane)); err != nil {
		t.Fatalf("publish g1: %v", err)
	}
	disp := runtimehost.NewGenerationDispatcher(mgr)

	parked := httptest.NewRecorder()
	parkedDone := make(chan struct{})
	go func() {
		defer close(parkedDone)
		disp.ServeHTTP(parked, sdReloadRequest("/.env", "203.0.113.30"))
	}()
	<-barrier.entered
	if got := mgr.Active().ID(); got != 1 {
		t.Fatalf("active generation = %d, want the admitted g1", got)
	}

	if err := mgr.Publish(mgr.PrepareRequestPlane("g2", second.plane)); err != nil {
		t.Fatalf("publish g2: %v", err)
	}
	if got := mgr.Active().ID(); got != 2 {
		t.Fatalf("active generation = %d, want the newly published g2", got)
	}

	fresh := sdReloadServe(t, disp, "/.env", "203.0.113.30")
	if fresh.Code != http.StatusNotFound || fresh.Body.String() != "Not Found\n" {
		t.Fatalf("new generation response = %d %q, want the generic 404 even for an exempt source", fresh.Code, fresh.Body.String())
	}
	if got := ps.IngressDefense.Len(); got != 0 {
		t.Fatalf("entries = %d, want the new generation's exemption to suppress the adaptive offense", got)
	}

	barrier.Release()
	<-parkedDone
	if parked.Code != http.StatusNotFound || parked.Body.String() != "Not Found\n" {
		t.Fatalf("parked response = %d %q, want the admitted generation's generic 404", parked.Code, parked.Body.String())
	}
	if got := ps.IngressDefense.Len(); got != 1 {
		t.Fatalf("entries = %d, want the in-flight request to have used its admitted generation's policy", got)
	}
	if !ps.IngressDefense.IsQuarantined(netip.MustParseAddr("203.0.113.30"), time.Now()) {
		t.Fatal("the in-flight request must have recorded the offense its own generation policy requires")
	}

	after := sdReloadServe(t, disp, "/.env", "203.0.113.30")
	if after.Code != http.StatusNotFound {
		t.Fatalf("post-release response = %d, want the generic 404", after.Code)
	}
	if got := ps.IngressDefense.Len(); got != 1 {
		t.Fatalf("entries = %d, want the active generation to keep exempting the address", got)
	}
}

// TestSelfDefenseCapacityAndTTLCandidatesAreRestartRequiredAndRejectAtomically
// proves requirements 7.4 and the restart-required half of 7.5. Each candidate
// changes only a process-state sizing field, or a reloadable policy field
// together with one, and every one of them must be refused atomically: the
// published generation does not change, the process state instance and its
// startup capacity survive untouched, and the last-good generation keeps
// enforcing its own policy.
func TestSelfDefenseCapacityAndTTLCandidatesAreRestartRequiredAndRejectAtomically(t *testing.T) {
	t.Parallel()

	base := sdReloadConfig(t, nil)
	ps := sdReloadHost(t, base)
	lastGood := sdReloadCompile(t, ps, base, nil)
	if lastGood.policy == nil {
		t.Fatal("the last-good generation must project an enabled self-defense policy")
	}
	mgr := runtimehost.NewManager(4, nil)
	if err := mgr.Publish(mgr.PrepareRequestPlane("g1", lastGood.plane)); err != nil {
		t.Fatalf("publish last-good: %v", err)
	}

	candidates := []struct {
		name       string
		candidate  *config.Config
		wantFields []string
	}{
		{
			name:       "capacity only",
			candidate:  sdReloadClone(base, func(c *config.Config) { c.Access.SelfDefense.Adaptive.MaxEntries = new(4096) }),
			wantFields: []string{"access.self_defense.adaptive.max_entries"},
		},
		{
			name:       "state ttl only",
			candidate:  sdReloadClone(base, func(c *config.Config) { c.Access.SelfDefense.Adaptive.StateTTL = "48h" }),
			wantFields: []string{"access.self_defense.adaptive.state_ttl"},
		},
		{
			name: "reloadable policy mixed with capacity",
			candidate: sdReloadClone(base, func(c *config.Config) {
				c.Access.SelfDefense.Adaptive.ExemptCIDRs = []string{"192.0.2.0/24"}
				c.Access.SelfDefense.Adaptive.MaxEntries = new(4096)
			}),
			wantFields: []string{"access.self_defense.adaptive.max_entries"},
		},
	}
	for _, tc := range candidates {
		if err := config.Validate(tc.candidate); err != nil {
			t.Fatalf("%s: fixture candidate invalid: %v", tc.name, err)
		}
		changes, err := configreload.Classify(base, tc.candidate)
		if err == nil {
			t.Fatalf("%s: Classify succeeded, want a restart-required rejection", tc.name)
		}
		if changes != nil {
			t.Fatalf("%s: atomic reject must publish no reloadable changes, got %v", tc.name, changes)
		}
		var restart *configreload.RestartRequiredError
		if !errors.As(err, &restart) {
			t.Fatalf("%s: Classify error = %v, want *RestartRequiredError", tc.name, err)
		}
		for _, field := range tc.wantFields {
			if !slices.Contains(restart.RestartRequiredFields, field) {
				t.Fatalf("%s: restart fields %v missing %q", tc.name, restart.RestartRequiredFields, field)
			}
		}
		if slices.Contains(restart.RestartRequiredFields, "access.self_defense.adaptive.exempt_cidrs") {
			t.Fatalf("%s: a reloadable field must never be reported restart-required: %v", tc.name, restart.RestartRequiredFields)
		}
	}

	// One recorded offense proves the last-good generation is genuinely
	// enforcing, not merely still published.
	addr := netip.MustParseAddr("203.0.113.40")
	ps.IngressDefense.RecordProbe(addr, time.Now(), *lastGood.policy)
	if !ps.IngressDefense.IsQuarantined(addr, time.Now()) {
		t.Fatal("the last-good generation's policy must quarantine the recorded offense")
	}

	for _, tc := range candidates {
		_, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
			Process: ps, Candidate: tc.candidate, Compose: selfDefenseStubComposer,
		})
		if err == nil {
			t.Fatalf("%s: candidate publication must be rejected", tc.name)
		}
		if _, ok := errors.AsType[*configreload.RestartRequiredError](err); !ok {
			t.Fatalf("%s: publication error = %v, want the restart-required rejection to surface unchanged", tc.name, err)
		}
		if got := mgr.Active().ID(); got != 1 {
			t.Fatalf("%s: active generation = %d, want the last-good generation to stay published", tc.name, got)
		}
	}

	// The rejected capacity candidate must not have resized the process state:
	// unique-address churn still stops at the startup capacity.
	for _, churn := range sdReloadChurn(1024 + 96) {
		ps.IngressDefense.RecordProbe(churn, time.Now(), *lastGood.policy)
	}
	if got, want := ps.IngressDefense.Len(), 1024; got != want {
		t.Fatalf("entries under churn = %d, want the startup max_entries %d; a rejected capacity candidate must not resize process state", got, want)
	}
	if got := sdReloadServe(t, runtimehost.NewGenerationDispatcher(mgr), "/.env", "203.0.113.41").Code; got != http.StatusNotFound {
		t.Fatalf("last-good generation response = %d, want the generic 404", got)
	}
}

// TestSelfDefenseInvalidCandidateLeavesLastGoodEnforcingItsOriginalPolicy proves
// the invalid half of requirement 7.5. Malformed and out-of-bounds candidates
// are refused before any generation exists, the published generation and the
// process state survive, and the surviving generation still escalates an entry
// recorded before the failed attempts. A later valid policy-only reload proves
// the process was not wedged by the rejected candidates.
func TestSelfDefenseInvalidCandidateLeavesLastGoodEnforcingItsOriginalPolicy(t *testing.T) {
	t.Parallel()

	base := sdReloadConfig(t, nil)
	ps := sdReloadHost(t, base)
	lastGood := sdReloadCompile(t, ps, base, nil)
	mgr := runtimehost.NewManager(4, nil)
	if err := mgr.Publish(mgr.PrepareRequestPlane("g1", lastGood.plane)); err != nil {
		t.Fatalf("publish last-good: %v", err)
	}

	addr := netip.MustParseAddr("203.0.113.50")
	start := time.Now()
	ps.IngressDefense.RecordProbe(addr, start, *lastGood.policy)
	if ps.IngressDefense.IsQuarantined(addr, start.Add(15*time.Second)) {
		t.Fatal("a single offense must not reach the doubled quarantine window")
	}

	invalid := []struct {
		name      string
		candidate *config.Config
		wantPath  string
	}{
		{
			name:      "malformed adaptive exemption cidr",
			candidate: sdReloadClone(base, func(c *config.Config) { c.Access.SelfDefense.Adaptive.ExemptCIDRs = []string{"not-a-cidr"} }),
			wantPath:  "access.self_defense.adaptive.exempt_cidrs",
		},
		{
			name:      "out of bounds failure window",
			candidate: sdReloadClone(base, func(c *config.Config) { c.Access.SelfDefense.Adaptive.Window = "999ms" }),
			wantPath:  "access.self_defense.adaptive.window",
		},
		{
			name: "max quarantine below initial quarantine",
			candidate: sdReloadClone(base, func(c *config.Config) {
				c.Access.SelfDefense.Adaptive.InitialQuarantine = "1h"
				c.Access.SelfDefense.Adaptive.MaxQuarantine = "30m"
			}),
			wantPath: "access.self_defense.adaptive.max_quarantine",
		},
	}
	for _, tc := range invalid {
		_, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
			Process: ps, Candidate: tc.candidate, Compose: selfDefenseStubComposer,
		})
		if err == nil {
			t.Fatalf("%s: invalid candidate must be rejected", tc.name)
		}
		if !strings.Contains(err.Error(), tc.wantPath) {
			t.Fatalf("%s: error = %v, want the offending self-defense field %q", tc.name, err, tc.wantPath)
		}
		if _, ok := errors.AsType[*configreload.RestartRequiredError](err); ok {
			t.Fatalf("%s: an invalid candidate must not be reported as restart-required: %v", tc.name, err)
		}
		if got := mgr.Active().ID(); got != 1 {
			t.Fatalf("%s: active generation = %d, want the last-good generation to stay published", tc.name, got)
		}
		if got := ps.IngressDefense.Len(); got != 1 {
			t.Fatalf("%s: entries = %d, want the pre-attempt entry to survive", tc.name, got)
		}
	}

	rec := sdReloadServe(t, runtimehost.NewGenerationDispatcher(mgr), "/.env", addr.String())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("last-good response = %d, want the generic 404", rec.Code)
	}
	if got := ps.IngressDefense.Len(); got != 1 {
		t.Fatalf("entries = %d, want the surviving generation to escalate the surviving entry", got)
	}
	if !ps.IngressDefense.IsQuarantined(addr, start.Add(15*time.Second)) {
		t.Fatal("the surviving generation must still enforce its original policy on the surviving state")
	}

	recovered := sdReloadClone(base, func(c *config.Config) { c.Access.SelfDefense.Adaptive.AuthFailures = new(4) })
	if err := config.Validate(recovered); err != nil {
		t.Fatalf("recovery candidate invalid: %v", err)
	}
	if _, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: recovered, Compose: selfDefenseStubComposer,
	}); err != nil {
		t.Fatalf("a valid policy-only reload must still publish after rejected candidates: %v", err)
	}
}

// TestSelfDefenseGeoIPHardDenyPrecedesSelfDefenseInEveryPublishedGeneration
// proves requirement 8.4 at the composition and reload level: a fixed GeoIP
// CIDR denial wins over the self-defense gate for the same request, creates no
// adaptive state, and keeps winning after a policy-only self-defense reload.
// A non-denied source proves the 403 is the fixed policy's, not a blanket block.
func TestSelfDefenseGeoIPHardDenyPrecedesSelfDefenseInEveryPublishedGeneration(t *testing.T) {
	t.Parallel()

	denyGeoIP := func(c *config.Config) {
		c.Access.GeoIP = config.GeoIPConfig{
			Enabled: true,
			Order:   "deny_allow",
			Deny:    config.GeoIPRuleConfig{CIDRs: []string{"203.0.113.0/24"}},
			ClientIP: config.GeoIPClientConfig{
				Source:         config.ClientIPSourceXForwardedFor,
				TrustedProxies: []string{"10.0.0.0/8"},
			},
		}
	}
	base := sdReloadConfig(t, denyGeoIP)
	ps := sdReloadHost(t, base)
	if ps.GeoIP != nil {
		t.Fatal("a CIDR-only fixed policy must not open a GeoIP country database")
	}
	reloaded := sdReloadClone(base, func(c *config.Config) { c.Access.SelfDefense.Adaptive.AuthFailures = new(3) })
	if err := config.Validate(reloaded); err != nil {
		t.Fatalf("reloaded candidate invalid: %v", err)
	}

	for i, gen := range []struct {
		name string
		cfg  *config.Config
	}{{"initial generation", base}, {"reloaded generation", reloaded}} {
		compiled := sdReloadCompile(t, ps, gen.cfg, nil)
		allowedAddr := netip.AddrFrom4([4]byte{198, 51, 100, byte(9 + i)})
		before := ps.IngressDefense.Len()
		denied := sdReloadServe(t, compiled.plane.Handler(), "/.env", "203.0.113.9")
		if denied.Code != http.StatusForbidden {
			t.Fatalf("%s: denied impossible path = %d, want the GeoIP hard denial 403", gen.name, denied.Code)
		}
		if got := ps.IngressDefense.Len(); got != before {
			t.Fatalf("%s: entries = %d, want a GeoIP-denied request to be invisible to self-defense", gen.name, got)
		}
		if ps.IngressDefense.IsQuarantined(netip.MustParseAddr("203.0.113.9"), time.Now()) {
			t.Fatalf("%s: a GeoIP-denied source must never enter adaptive state", gen.name)
		}
		allowed := sdReloadServe(t, compiled.plane.Handler(), "/.env", allowedAddr.String())
		if allowed.Code != http.StatusNotFound || allowed.Body.String() != "Not Found\n" {
			t.Fatalf("%s: allowed impossible path = %d %q, want the self-defense generic 404", gen.name, allowed.Code, allowed.Body.String())
		}
		if got, want := ps.IngressDefense.Len(), before+1; got != want {
			t.Fatalf("%s: entries = %d, want exactly the allowed source recorded (%d)", gen.name, got, want)
		}
		if !ps.IngressDefense.IsQuarantined(allowedAddr, time.Now()) {
			t.Fatalf("%s: the allowed source must be tracked by the self-defense gate", gen.name)
		}
	}
}

// TestSelfDefenseWithoutGeoIPEnforcementResolvesTrustedForwardedChain proves the
// end-to-end dimension of requirements 2.1, 2.2 and 8.5: with fixed GeoIP
// enforcement off and no country database, a generation still resolves the
// client address through the configured trusted-proxy chain, keys adaptive
// state on the forwarded address rather than the proxy hop, and ignores the
// forwarding header when the direct peer is not trusted.
func TestSelfDefenseWithoutGeoIPEnforcementResolvesTrustedForwardedChain(t *testing.T) {
	t.Parallel()

	base := sdReloadConfig(t, nil)
	ps := sdReloadHost(t, base)
	compiled := sdReloadCompile(t, ps, base, nil)
	if ps.GeoIP != nil {
		t.Fatal("the fixture must not open a GeoIP country database")
	}
	if compiled.plane.Handler() == nil {
		t.Fatal("no composed handler")
	}

	forwarded := netip.MustParseAddr("198.51.100.60")
	proxy := netip.MustParseAddr("10.0.0.9")
	untrustedPeer := netip.MustParseAddr("203.0.113.200")

	rec := sdReloadServe(t, compiled.plane.Handler(), "/.env", forwarded.String())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("trusted-proxy request = %d, want the generic 404", rec.Code)
	}
	now := time.Now()
	if !ps.IngressDefense.IsQuarantined(forwarded, now) {
		t.Fatal("state must be keyed on the forwarded client address of a trusted proxy chain")
	}
	if ps.IngressDefense.IsQuarantined(proxy, now) {
		t.Fatal("state must never be keyed on the trusted proxy hop itself")
	}

	spoofed := httptest.NewRequest(http.MethodGet, "http://data-plane.test/.env", nil)
	spoofed.RemoteAddr = untrustedPeer.String() + ":1234"
	spoofed.Header.Set("X-Forwarded-For", forwarded.String())
	rec = httptest.NewRecorder()
	compiled.plane.Handler().ServeHTTP(rec, spoofed)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("untrusted-peer request = %d, want the generic 404", rec.Code)
	}
	if !ps.IngressDefense.IsQuarantined(untrustedPeer, time.Now()) {
		t.Fatal("an untrusted peer must be tracked under its direct address, never the spoofed header")
	}
	if got := ps.IngressDefense.Len(); got != 2 {
		t.Fatalf("entries = %d, want exactly the forwarded client and the untrusted direct peer", got)
	}
}

// TestValidateStructuralRejectsSelfDefenseWithoutBindingTheDataPlaneListener
// proves requirement 7.2 on the real check-config path. Structural validation
// rejects an invalid self-defense duration with the offending field path, and
// accepts a valid one even though the configured data-plane address is already
// bound exclusively by this test, which proves it never binds a listener and
// never constructs the process-owned adaptive state to do so.
func TestValidateStructuralRejectsSelfDefenseWithoutBindingTheDataPlaneListener(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	addr := ln.Addr().String()

	valid := sdReloadCheckConfigYAML(t, addr, "30s", "1024", "")
	if _, err := runtimebundle.ValidateStructural(context.Background(), runtimebundle.ValidateStructuralInput{
		ConfigPath: valid,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	}); err != nil {
		t.Fatalf("ValidateStructural rejected a valid self-defense block: %v", err)
	}

	for _, tc := range []struct {
		name     string
		window   string
		maxEntry string
		exempt   string
		wantPath string
	}{
		{name: "duration below the floor", window: "999ms", maxEntry: "1024", wantPath: "access.self_defense.adaptive.window"},
		{name: "entry bound below the floor", window: "30s", maxEntry: "16", wantPath: "access.self_defense.adaptive.max_entries"},
		{name: "malformed exemption cidr", window: "30s", maxEntry: "1024", exempt: "nope", wantPath: "access.self_defense.adaptive.exempt_cidrs"},
	} {
		path := sdReloadCheckConfigYAML(t, addr, tc.window, tc.maxEntry, tc.exempt)
		_, err := runtimebundle.ValidateStructural(context.Background(), runtimebundle.ValidateStructuralInput{
			ConfigPath: path,
			Mandatory:  aipsdk.StandardDistributionRequirements(),
		})
		if err == nil {
			t.Fatalf("%s: check-config must reject the invalid self-defense configuration", tc.name)
		}
		if !strings.Contains(err.Error(), tc.wantPath) {
			t.Fatalf("%s: error = %v, want the offending self-defense field %q", tc.name, err, tc.wantPath)
		}
	}

	// The pre-bound listener is still ours: structural validation never took it.
	if tcp, ok := ln.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(time.Now().Add(50 * time.Millisecond))
		if conn, acceptErr := ln.Accept(); acceptErr == nil {
			_ = conn.Close()
			t.Fatal("check-config must not bind or dial the configured data-plane address")
		}
	}
}

// sdReloadCheckConfigYAML materializes a real standard-distribution config file
// whose only self-defense variation is the one under test. It reuses the
// shipped example so the file stays a genuine operator configuration.
func sdReloadCheckConfigYAML(t *testing.T, listenAddr, window, maxEntries, exempt string) string {
	t.Helper()
	base, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(base, &doc); err != nil {
		t.Fatal(err)
	}
	root := &doc
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	setYAMLChild(t, root, "server", map[string]any{"address": listenAddr})
	adaptive := map[string]any{
		"auth_failures":      5,
		"window":             window,
		"initial_quarantine": sdReloadInitial.String(),
		"max_quarantine":     "2h",
		"state_ttl":          "24h",
		"max_entries":        mustAtoi(t, maxEntries),
	}
	if exempt != "" {
		adaptive["exempt_cidrs"] = []any{exempt}
	}
	setYAMLChild(t, root, "access", map[string]any{
		"geoip": map[string]any{
			"client_ip": map[string]any{"source": "x_forwarded_for", "trusted_proxies": []any{"10.0.0.0/8"}},
		},
		"self_defense": map[string]any{"adaptive": adaptive},
	})

	out, err := yaml.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "check-config-self-defense.yaml")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// setYAMLChild replaces (or appends) one top-level mapping key with a freshly
// built node tree, so the test never depends on string markers in a shipped
// example file.
func setYAMLChild(t *testing.T, root *yaml.Node, key string, value map[string]any) {
	t.Helper()
	node := &yaml.Node{}
	if err := node.Encode(value); err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			root.Content[i+1] = node
			return
		}
	}
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, node)
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("parse int %q: %v", s, err)
	}
	return n
}
