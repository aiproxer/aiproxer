package runtimebundle_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/affinity"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/configreload"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	stdhttpauth "github.com/aiproxer/aiproxer/internal/stdhttp/auth"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

// policyBaseConfig returns a process/candidate baseline for task 4.4 tests:
// multi_user + local_api_key auth, one enabled local-stub backend, and a
// Diagnostics/Server projection compatible across every reloadable-only
// variant produced by policyCandidateConfig so configreload.Classify sees
// only reloadable changes unless a test deliberately flips a startup-only
// field.
func policyBaseConfig(t *testing.T, backendID, text string, keys []config.AuthLocalAPIKeyRecord) *config.Config {
	t.Helper()
	cfg := &config.Config{
		Access: config.AccessConfig{Mode: "multi_user"},
		Auth: config.AuthConfig{
			Handler:       "local_api_key",
			RequiredLevel: "api_key",
			LocalAPIKeys:  keys,
		},
		Server: config.ServerConfig{
			Address:                "127.0.0.1:0",
			AuthMode:               config.AuthModeExternal,
			MaxRequestBodyBytes:    1024,
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 4096,
		},
		Routing:     config.RoutingConfig{MaxAttempts: 3, DefaultRoute: backendID + ":stub-default"},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind:    "local-stub",
				ID:      backendID,
				Enabled: true,
				Config: genYAMLNode(t, fmt.Sprintf(`
text: %q
input_tokens: 1
output_tokens: 1
`, text)),
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return cfg
}

func policyProcess(t *testing.T, cfg *config.Config) *runtimebundle.ProcessServices {
	t.Helper()
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: stdFactoryCatalog(t)},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	return ps
}

// TestCompileCandidate_RestartRequiredBeforeResourceConstruction proves a
// startup-only-field change (auth.handler) fails with a typed
// *configreload.RestartRequiredError before any generation resource is
// constructed, and that ProcessServices survives to serve a later valid
// candidate (req 3.5, 7.5; design "Wire Classify").
func TestCompileCandidate_RestartRequiredBeforeResourceConstruction(t *testing.T) {
	t.Parallel()
	keys := []config.AuthLocalAPIKeyRecord{{KeyID: "k1", PrincipalID: "user-a", Key: "process-baseline-key-16"}}
	base := policyBaseConfig(t, "base", "base-text", keys)
	ps := policyProcess(t, base)

	restartCandidate := policyBaseConfig(t, "base", "base-text", keys)
	restartCandidate.Auth.Handler = "local_noop" // startup-only (req 7.3)

	_, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Bus:       hooks.New(hooks.Config{}),
		Candidate: restartCandidate,
	})
	if err == nil {
		t.Fatal("expected restart-required error")
	}
	var restartErr *configreload.RestartRequiredError
	if !errors.As(err, &restartErr) {
		t.Fatalf("want *configreload.RestartRequiredError, got %T: %v", err, err)
	}
	if restartErr.TotalBlocked == 0 || len(restartErr.RestartRequiredFields) == 0 {
		t.Fatalf("expected populated restart-required fields, got %+v", restartErr)
	}
	found := false
	for _, f := range restartErr.RestartRequiredFields {
		if f == "auth.handler" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected auth.handler in blocked fields, got %v", restartErr.RestartRequiredFields)
	}
	if ps.Closed() {
		t.Fatal("ProcessServices must survive a rejected candidate")
	}

	// A subsequent valid candidate must still compile after the rejection.
	ok, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Bus:       hooks.New(hooks.Config{}),
		Candidate: policyBaseConfig(t, "base", "base-text", keys),
	})
	if err != nil {
		t.Fatalf("recover compile after restart-required rejection: %v", err)
	}
	_ = ok.Close()
}

// TestCompileCandidate_ReloadableOnlyCandidateCompiles proves a candidate that
// only changes reloadable rows (backend, auth keys) compiles without a
// restart-required error.
func TestCompileCandidate_ReloadableOnlyCandidateCompiles(t *testing.T) {
	t.Parallel()
	keys := []config.AuthLocalAPIKeyRecord{{KeyID: "k1", PrincipalID: "user-a", Key: "process-baseline-key-16"}}
	base := policyBaseConfig(t, "base", "base-text", keys)
	ps := policyProcess(t, base)

	newKeys := []config.AuthLocalAPIKeyRecord{{KeyID: "k2", PrincipalID: "user-b", Key: "candidate-rotated-key-16"}}
	cand := policyBaseConfig(t, "base", "base-text-v2", newKeys)

	c, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Bus:       hooks.New(hooks.Config{}),
		Candidate: cand,
	})
	if err != nil {
		t.Fatalf("reloadable-only candidate must compile: %v", err)
	}
	defer func() { _ = c.Close() }()
}

// TestCompileCandidate_DefaultRouteUnconfiguredBackendFails proves an explicit
// candidate whose default route names a backend absent from its own backend
// rows fails compile before publication (req 9.2).
func TestCompileCandidate_DefaultRouteUnconfiguredBackendFails(t *testing.T) {
	t.Parallel()
	keys := []config.AuthLocalAPIKeyRecord{{KeyID: "k1", PrincipalID: "user-a", Key: "process-baseline-key-16"}}
	base := policyBaseConfig(t, "base", "base-text", keys)
	ps := policyProcess(t, base)

	cand := policyBaseConfig(t, "base", "base-text", keys)
	cand.Routing.DefaultRoute = "totally-unconfigured-backend:model"

	_, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Bus:       hooks.New(hooks.Config{}),
		Candidate: cand,
	})
	if err == nil {
		t.Fatal("expected compile failure for unconfigured default-route backend")
	}
	if !contains(err.Error(), "totally-unconfigured-backend") {
		t.Fatalf("error should name the unconfigured backend: %v", err)
	}
	if ps.Closed() {
		t.Fatal("ProcessServices must survive invalid candidate")
	}
}

// TestCompileCandidate_ModelAliasUnconfiguredBackendFails proves an alias
// replacement naming an unconfigured backend fails compile (req 9.2).
func TestCompileCandidate_ModelAliasUnconfiguredBackendFails(t *testing.T) {
	t.Parallel()
	keys := []config.AuthLocalAPIKeyRecord{{KeyID: "k1", PrincipalID: "user-a", Key: "process-baseline-key-16"}}
	base := policyBaseConfig(t, "base", "base-text", keys)
	ps := policyProcess(t, base)

	cand := policyBaseConfig(t, "base", "base-text", keys)
	cand.ModelAliases = []config.ModelAliasConfig{{Pattern: `^friendly$`, Replacement: "ghost-backend:model"}}
	if err := config.Validate(cand); err != nil {
		t.Fatalf("validate candidate: %v", err)
	}

	_, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Bus:       hooks.New(hooks.Config{}),
		Candidate: cand,
	})
	if err == nil {
		t.Fatal("expected compile failure for unconfigured alias-replacement backend")
	}
	if !contains(err.Error(), "ghost-backend") {
		t.Fatalf("error should name the unconfigured backend: %v", err)
	}
}

// TestCompileCandidate_AuthLocalAPIKeysDifferPerCandidate proves two
// candidates under the same fixed auth.handler produce independent
// accept/deny decisions from their own local_api_keys rows (req 7.4).
func TestCompileCandidate_AuthLocalAPIKeysDifferPerCandidate(t *testing.T) {
	t.Parallel()
	keysA := []config.AuthLocalAPIKeyRecord{{KeyID: "k1", PrincipalID: "user-a", Key: "candidate-a-key-1234567"}}
	base := policyBaseConfig(t, "base", "base-text", keysA)
	ps := policyProcess(t, base)

	candA := policyBaseConfig(t, "base", "base-text", keysA)
	keysB := []config.AuthLocalAPIKeyRecord{{KeyID: "k2", PrincipalID: "user-b", Key: "candidate-b-key-7654321"}}
	candB := policyBaseConfig(t, "base", "base-text", keysB)

	a, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Bus: hooks.New(hooks.Config{}), Candidate: candA,
	})
	if err != nil {
		t.Fatalf("compile A: %v", err)
	}
	defer func() { _ = a.Close() }()
	b, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Bus: hooks.New(hooks.Config{}), Candidate: candB,
	})
	if err != nil {
		t.Fatalf("compile B: %v", err)
	}
	defer func() { _ = b.Close() }()

	assertBearerOutcome := func(t *testing.T, providers []httpauth.Provider, bearer string, wantOK bool, wantPrincipal string) {
		t.Helper()
		var gotPrincipal string
		h := stdhttpauth.Middleware(nil, providers, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			p, _ := httpauth.PrincipalFromContext(r.Context())
			gotPrincipal = p.ID
		}))
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		ok := rr.Code == http.StatusOK
		if ok != wantOK {
			t.Fatalf("bearer %q: status=%d want ok=%v", bearer, rr.Code, wantOK)
		}
		if wantOK && gotPrincipal != wantPrincipal {
			t.Fatalf("bearer %q: principal=%q want %q", bearer, gotPrincipal, wantPrincipal)
		}
	}

	assertBearerOutcome(t, runtimebundle.CandidateHTTPAuthProviders(a), "candidate-a-key-1234567", true, "user-a")
	assertBearerOutcome(t, runtimebundle.CandidateHTTPAuthProviders(a), "candidate-b-key-7654321", false, "")
	assertBearerOutcome(t, runtimebundle.CandidateHTTPAuthProviders(b), "candidate-b-key-7654321", true, "user-b")
	assertBearerOutcome(t, runtimebundle.CandidateHTTPAuthProviders(b), "candidate-a-key-1234567", false, "")
}

// TestCompileCandidate_MaxPendingWireEventsDiffersPerCandidate proves the
// generation-owned executor reflects each candidate's own
// server.max_pending_wire_events value (req 7.4, 9.1, 16.4).
func TestCompileCandidate_MaxPendingWireEventsDiffersPerCandidate(t *testing.T) {
	t.Parallel()
	keys := []config.AuthLocalAPIKeyRecord{{KeyID: "k1", PrincipalID: "user-a", Key: "process-baseline-key-16"}}
	base := policyBaseConfig(t, "base", "base-text", keys)
	ps := policyProcess(t, base)

	candLow := policyBaseConfig(t, "base", "base-text", keys)
	candLow.Server.MaxPendingWireEvents = 7
	candHigh := policyBaseConfig(t, "base", "base-text", keys)
	candHigh.Server.MaxPendingWireEvents = 700

	low, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Bus: hooks.New(hooks.Config{}), Candidate: candLow,
	})
	if err != nil {
		t.Fatalf("compile low: %v", err)
	}
	defer func() { _ = low.Close() }()
	high, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Bus: hooks.New(hooks.Config{}), Candidate: candHigh,
	})
	if err != nil {
		t.Fatalf("compile high: %v", err)
	}
	defer func() { _ = high.Close() }()

	if low.Executor().MaxPendingWireEvents != 7 {
		t.Fatalf("low.Executor().MaxPendingWireEvents = %d, want 7", low.Executor().MaxPendingWireEvents)
	}
	if high.Executor().MaxPendingWireEvents != 700 {
		t.Fatalf("high.Executor().MaxPendingWireEvents = %d, want 700", high.Executor().MaxPendingWireEvents)
	}
}

// TestCompileCandidate_HTTPClientMaxIdleConnsDiffersPerCandidate proves two
// candidates with different http_client.max_idle_conns receive distinct
// generation-owned upstream *http.Client/*http.Transport instances tuned to
// their own value (req 7.4, 9.1).
func TestCompileCandidate_HTTPClientMaxIdleConnsDiffersPerCandidate(t *testing.T) {
	t.Parallel()
	keys := []config.AuthLocalAPIKeyRecord{{KeyID: "k1", PrincipalID: "user-a", Key: "process-baseline-key-16"}}
	base := policyBaseConfig(t, "base", "base-text", keys)
	ps := policyProcess(t, base)

	lowN, highN := 11, 911
	candLow := policyBaseConfig(t, "base", "base-text", keys)
	candLow.HTTPClient.MaxIdleConns = &lowN
	candHigh := policyBaseConfig(t, "base", "base-text", keys)
	candHigh.HTTPClient.MaxIdleConns = &highN

	low, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Bus: hooks.New(hooks.Config{}), Candidate: candLow,
	})
	if err != nil {
		t.Fatalf("compile low: %v", err)
	}
	defer func() { _ = low.Close() }()
	high, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Bus: hooks.New(hooks.Config{}), Candidate: candHigh,
	})
	if err != nil {
		t.Fatalf("compile high: %v", err)
	}
	defer func() { _ = high.Close() }()

	if runtimebundle.CandidateUpstreamHTTP(low) == runtimebundle.CandidateUpstreamHTTP(high) {
		t.Fatal("expected distinct generation-owned upstream clients")
	}
	lowTr, ok := runtimebundle.CandidateUpstreamHTTP(low).Transport.(*http.Transport)
	if !ok {
		t.Fatalf("low transport type = %T", runtimebundle.CandidateUpstreamHTTP(low).Transport)
	}
	highTr, ok := runtimebundle.CandidateUpstreamHTTP(high).Transport.(*http.Transport)
	if !ok {
		t.Fatalf("high transport type = %T", runtimebundle.CandidateUpstreamHTTP(high).Transport)
	}
	if lowTr == highTr {
		t.Fatal("expected distinct underlying *http.Transport instances")
	}
	if lowTr.MaxIdleConns != lowN {
		t.Fatalf("low transport MaxIdleConns = %d, want %d", lowTr.MaxIdleConns, lowN)
	}
	if highTr.MaxIdleConns != highN {
		t.Fatalf("high transport MaxIdleConns = %d, want %d", highTr.MaxIdleConns, highN)
	}
}

// TestCompileGeneration_MaxRequestBodyBytesDiffersPerCandidate proves the
// composed handler enforces each generation's own server.max_request_body_bytes
// against a real HTTP request (req 7.4, 9.1, 16.4).
func TestCompileGeneration_MaxRequestBodyBytesDiffersPerCandidate(t *testing.T) {
	t.Parallel()
	keys := []config.AuthLocalAPIKeyRecord{{KeyID: "k1", PrincipalID: "user-a", Key: "process-baseline-key-16"}}
	base := policyBaseConfig(t, "base", "base-text", keys)
	base.Plugins.Frontends = []config.PluginConfig{{ID: "openai-responses", Enabled: true}}
	ps := policyProcess(t, base)

	tinyCand := policyBaseConfig(t, "base", "base-text", keys)
	tinyCand.Plugins.Frontends = []config.PluginConfig{{ID: "openai-responses", Enabled: true}}
	tinyCand.Server.MaxRequestBodyBytes = 32

	roomyCand := policyBaseConfig(t, "base", "base-text", keys)
	roomyCand.Plugins.Frontends = []config.PluginConfig{{ID: "openai-responses", Enabled: true}}
	roomyCand.Server.MaxRequestBodyBytes = 1 << 20

	tiny, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: tinyCand, Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("compile tiny: %v", err)
	}
	defer func() { _ = tiny.Close() }()
	roomy, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: roomyCand, Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("compile roomy: %v", err)
	}
	defer func() { _ = roomy.Close() }()

	bigBody := fmt.Sprintf(`{"model":"stub-default","stream":false,"input":[{"role":"user","content":%q}]}`, bigContent(2048))
	newReq := func(bearer string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(bigBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+bearer)
		return req
	}

	rrTiny := httptest.NewRecorder()
	tiny.Handler().ServeHTTP(rrTiny, newReq("process-baseline-key-16"))
	if rrTiny.Code == http.StatusOK {
		t.Fatalf("tiny generation should reject oversized body, got 200: %s", rrTiny.Body.String())
	}

	rrRoomy := httptest.NewRecorder()
	roomy.Handler().ServeHTTP(rrRoomy, newReq("process-baseline-key-16"))
	if rrRoomy.Code != http.StatusOK {
		t.Fatalf("roomy generation should accept the same body, got %d: %s", rrRoomy.Code, rrRoomy.Body.String())
	}
}

// TestCompileCandidate_HealthPolicyReloadableSharedObservation proves each
// candidate's routing health circuit-breaker policy (threshold/open-for) is
// generation-scoped and reloadable while the underlying failure/blockedUntil
// observation counters remain process-shared for a compatible backend
// identity, and that affinity identity survives the same health-policy change
// (req 7.4, 9.1; design "Health policy reload").
func TestCompileCandidate_HealthPolicyReloadableSharedObservation(t *testing.T) {
	t.Parallel()
	keys := []config.AuthLocalAPIKeyRecord{{KeyID: "k1", PrincipalID: "user-a", Key: "process-baseline-key-16"}}
	base := policyBaseConfig(t, "base", "base-text", keys)
	ps := policyProcess(t, base)

	strictCand := policyBaseConfig(t, "base", "base-text", keys)
	strictCand.Routing.Health.CircuitBreaker = config.CircuitBreakerConfig{
		Enabled: true, FailureThreshold: 1, OpenFor: "1h",
	}
	lenientCand := policyBaseConfig(t, "base", "base-text", keys)
	lenientCand.Routing.Health.CircuitBreaker = config.CircuitBreakerConfig{
		Enabled: true, FailureThreshold: 5, OpenFor: "1h",
	}

	strict, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Bus: hooks.New(hooks.Config{}), Candidate: strictCand,
	})
	if err != nil {
		t.Fatalf("compile strict: %v", err)
	}
	defer func() { _ = strict.Close() }()
	lenient, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Bus: hooks.New(hooks.Config{}), Candidate: lenientCand,
	})
	if err != nil {
		t.Fatalf("compile lenient: %v", err)
	}
	defer func() { _ = lenient.Close() }()

	key := affinity.Key{Scope: affinity.ScopeSession, ID: "policy-health-sess"}
	if err := strict.Executor().AffinityStore.Set(context.Background(), affinity.Binding{
		Key: key, BackendID: "base", CandidateKey: "base:m",
	}); err != nil {
		t.Fatalf("affinity set via strict: %v", err)
	}
	if b, ok, err := lenient.Executor().AffinityStore.Get(context.Background(), key); err != nil || !ok || b.BackendID != "base" {
		t.Fatalf("affinity identity must survive health-policy-only change: ok=%v backend=%q err=%v", ok, b.BackendID, err)
	}

	sink, ok := strict.Executor().CandidateHealth.(interface {
		OnRoutingAttemptOutcome(string, aipapi.AttemptOutcome)
	})
	if !ok {
		t.Fatalf("expected RoutingAttemptOutcomeSink, got %T", strict.Executor().CandidateHealth)
	}
	sink.OnRoutingAttemptOutcome("base:m", aipapi.AttemptSurfacedFailure)

	if u := strict.Executor().CandidateHealth.UnhealthyCandidateKeys(); len(u) != 1 {
		t.Fatalf("strict (threshold=1) must open after its own single failure, got %v", u)
	}
	if u := lenient.Executor().CandidateHealth.UnhealthyCandidateKeys(); len(u) != 1 {
		t.Fatalf("lenient generation must observe the shared failure recorded via strict, got %v", u)
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func bigContent(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}
