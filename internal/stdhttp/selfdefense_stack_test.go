package stdhttp

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coreconfig "github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	coregeoip "github.com/aiproxer/aiproxer/internal/core/geoip"
	corehttp "github.com/aiproxer/aiproxer/internal/core/http"
	"github.com/aiproxer/aiproxer/internal/core/ingressdefense"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	adminreload "github.com/aiproxer/aiproxer/internal/stdhttp/admin/configreload"
	"github.com/aiproxer/aiproxer/internal/testkit"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

// selfDefenseObserverSpy records the finite self-defense reasons the gate and
// the auth observer report. It never sees a path, address or credential.
type selfDefenseObserverSpy struct {
	denials     []ingressdefense.Reason
	transitions []ingressdefense.Reason
	entryCounts []int
}

func (s *selfDefenseObserverSpy) Denial(reason ingressdefense.Reason) {
	s.denials = append(s.denials, reason)
}

func (s *selfDefenseObserverSpy) QuarantineTransition(reason ingressdefense.Reason, entryCount int) {
	s.transitions = append(s.transitions, reason)
	s.entryCounts = append(s.entryCounts, entryCount)
}

func selfDefenseStackPolicy(threshold int) *ingressdefense.Policy {
	return &ingressdefense.Policy{
		Enabled: true, AuthFailures: threshold, FailureWindow: time.Minute,
		InitialQuarantine: time.Hour, MaxQuarantine: 2 * time.Hour,
	}
}

func selfDefenseStackState(t *testing.T) *ingressdefense.State {
	t.Helper()
	state, err := ingressdefense.NewState(ingressdefense.StateLimits{MaxEntries: 64, StateTTL: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func selfDefenseRequest(target, remote string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "http://example.test"+target, nil)
	req.RemoteAddr = remote
	return req
}

// selfDefenseAuthSpy records transport-auth invocations without changing any
// response, so a test can prove the gate refused before authentication.
type selfDefenseAuthSpy struct {
	calls int
	res   httpauth.AuthenticationResult
}

func (s *selfDefenseAuthSpy) Authenticate(ctx context.Context, w http.ResponseWriter, r *http.Request) (httpauth.AuthenticationResult, error) {
	s.calls++
	return s.res, nil
}

// TestStackHTTPHandlerSelfDefenseRefusalIsInsideGeoIPAndOuterRecovery proves
// the normative position: security headers, server policy and outer recovery
// still wrap the gate, and a GeoIP hard denial wins before any self-defense
// behavior for the same request (requirement 8.4).
func TestStackHTTPHandlerSelfDefenseRefusalIsInsideGeoIPAndOuterRecovery(t *testing.T) {
	t.Parallel()

	geoPolicy, err := coregeoip.Compile(coregeoip.CompileInput{
		Order: coregeoip.OrderDenyAllow,
		Deny:  coregeoip.RuleConfig{Countries: []string{"RU"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	observer := &selfDefenseObserverSpy{}
	state := selfDefenseStackState(t)
	now := time.Date(2026, time.March, 2, 12, 0, 0, 0, time.UTC)
	innerCalls := 0
	h := stackHTTPHandler(stackHTTPInput{
		Cfg:      &coreconfig.Config{},
		Log:      testkit.DiscardLogger(),
		TraceGen: diag.NewTraceIDGenerator(),
		Security: HTTPSecurityInput{
			GeoIP: GeoIPSecurityInput{
				Policy:   geoPolicy,
				Lookup:   stackGeoIPLookup{},
				Resolver: GeoIPResolverConfig{Source: "direct"},
			},
			SelfDefense: SelfDefenseSecurityInput{
				Policy:          selfDefenseStackPolicy(2),
				State:           state,
				Resolver:        GeoIPResolverConfig{Source: "direct"},
				ImpossiblePaths: true,
				Observer:        observer,
				Now:             func() time.Time { return now },
			},
		},
		Inner: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { innerCalls++ }),
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, selfDefenseRequest("/.env", "198.51.100.10:443"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want the GeoIP hard denial 403", rec.Code)
	}
	if innerCalls != 0 {
		t.Fatalf("inner calls = %d, want 0", innerCalls)
	}
	if len(observer.denials) != 0 {
		t.Fatalf("self-defense denials = %v, want none: GeoIP hard denial must win before self-defense", observer.denials)
	}
	if state.Len() != 0 {
		t.Fatalf("adaptive entries = %d, want 0: self-defense must not observe a GeoIP-denied request", state.Len())
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("outer security headers = %v, want them still effective outside the gate", rec.Header())
	}
}

// TestStackHTTPHandlerSelfDefenseGateIsWrappedByOuterRecovery proves the
// recovery half of requirement 10.4: a panic raised inside the gate itself is
// still contained by outerRecoveryMiddleware, so a self-defense bug cannot take
// the server down and no panic escapes to the transport.
func TestStackHTTPHandlerSelfDefenseGateIsWrappedByOuterRecovery(t *testing.T) {
	t.Parallel()

	state := selfDefenseStackState(t)
	now := time.Date(2026, time.March, 2, 12, 0, 0, 0, time.UTC)
	policy := selfDefenseStackPolicy(2)
	quarantineProbe := state.RecordProbe(
		netip.MustParseAddr("198.51.100.10"), now, *policy)
	if !quarantineProbe.QuarantineStarted {
		t.Fatalf("probe transition = %+v, want a started quarantine for the fixture", quarantineProbe)
	}
	innerCalls := 0
	h := stackHTTPHandler(stackHTTPInput{
		Cfg:      &coreconfig.Config{},
		Log:      testkit.DiscardLogger(),
		TraceGen: diag.NewTraceIDGenerator(),
		Security: HTTPSecurityInput{
			SelfDefense: SelfDefenseSecurityInput{
				Policy:   policy,
				State:    state,
				Resolver: GeoIPResolverConfig{Source: "direct"},
				Now:      func() time.Time { return now },
				Probe: func(*http.Request) CredentialDisposition {
					panic("injected gate panic")
				},
			},
		},
		Inner: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { innerCalls++ }),
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, selfDefenseRequest("/v1/models", "198.51.100.10:443"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 from the outer recovery middleware", rec.Code)
	}
	if innerCalls != 0 {
		t.Fatalf("inner calls = %d, want 0", innerCalls)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("outer security headers = %v, want them still effective after a recovered gate panic", rec.Header())
	}
}

// TestStackHTTPHandlerSelfDefenseRefusalSkipsInnerObservabilityAndAuth proves
// the gate sits inside the global wrappers but outside every expensive or noisy
// inner layer: a deterministic refusal reaches no tracing, metrics, request ID,
// access log, transport auth or route work.
func TestStackHTTPHandlerSelfDefenseRefusalSkipsInnerObservabilityAndAuth(t *testing.T) {
	t.Parallel()

	registry := metrics.NewRegistry()
	httpProm := metrics.RegisterHTTPMetrics(registry, false)
	var logBuf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	observer := &selfDefenseObserverSpy{}
	state := selfDefenseStackState(t)
	now := time.Date(2026, time.March, 2, 12, 0, 0, 0, time.UTC)
	authSpy := &selfDefenseAuthSpy{res: httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, Principal: execview.PrincipalView{ID: "alice"}}}
	innerCalls := 0
	cfg := &coreconfig.Config{Logging: coreconfig.LoggingConfig{AccessLog: true}}
	h := stackHTTPHandler(stackHTTPInput{
		Cfg:      cfg,
		Log:      log,
		TraceGen: diag.NewTraceIDGenerator(),
		HTTPProm: httpProm,
		Security: HTTPSecurityInput{
			HTTPAuthProviders: []httpauth.Provider{authSpy},
			SelfDefense: SelfDefenseSecurityInput{
				Policy:          selfDefenseStackPolicy(2),
				State:           state,
				Resolver:        GeoIPResolverConfig{Source: "direct"},
				ImpossiblePaths: true,
				Observer:        observer,
				Now:             func() time.Time { return now },
			},
		},
		Inner: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { innerCalls++ }),
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, selfDefenseRequest("/.git/config", "198.51.100.11:443"))
	if rec.Code != http.StatusNotFound || rec.Body.String() != "Not Found\n" {
		t.Fatalf("response = %d %q, want the generic 404", rec.Code, rec.Body.String())
	}
	if len(observer.denials) != 1 || observer.denials[0] != ingressdefense.ReasonImpossiblePath {
		t.Fatalf("denials = %v, want exactly one impossible_path", observer.denials)
	}
	if authSpy.calls != 0 {
		t.Fatalf("transport auth calls = %d, want 0: the gate must refuse before authentication", authSpy.calls)
	}
	if innerCalls != 0 {
		t.Fatalf("route calls = %d, want 0", innerCalls)
	}
	if logBuf.Len() != 0 {
		t.Fatalf("access log = %q, want no per-request line for a gate refusal", logBuf.String())
	}
	if got := rec.Header().Get(corehttp.HeaderTraceID); got != "" {
		t.Fatalf("trace/request-id header = %q, want the request-id layer skipped", got)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range families {
		if strings.Contains(mf.GetName(), "http_request") {
			t.Fatalf("general HTTP metrics recorded %s for a gate refusal", mf.GetName())
		}
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("outer security headers = %v, want them still effective outside the gate", rec.Header())
	}
	if state.Len() != 1 {
		t.Fatalf("adaptive entries = %d, want the recorded probe offense", state.Len())
	}
}

// TestStackHTTPHandlerSelfDefenseQuarantineRefusalReachesAuthWhenItMayProveIt
// proves the shared-address safety boundary: a proven credential-free request is
// refused before authentication, while a request the configured provider chain
// might still authenticate always reaches that same chain.
func TestStackHTTPHandlerSelfDefenseQuarantineRefusalReachesAuthWhenItMayProveIt(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		probe     CredentialProbe
		wantCode  int
		wantInner int
	}{
		{name: "definitely no credential is refused before auth", probe: func(*http.Request) CredentialDisposition { return DefinitelyNoCredential }, wantCode: http.StatusTooManyRequests},
		{name: "may authenticate reaches the auth chain", probe: func(*http.Request) CredentialDisposition { return MayAuthenticate }, wantCode: http.StatusOK, wantInner: 1},
		{name: "unknown chain fails open to auth", probe: nil, wantCode: http.StatusOK, wantInner: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state := selfDefenseStackState(t)
			now := time.Date(2026, time.March, 2, 12, 0, 0, 0, time.UTC)
			observer := &selfDefenseObserverSpy{}
			addr := netip.MustParseAddr("198.51.100.12")
			state.RecordProbe(addr, now, *selfDefenseStackPolicy(2))
			authSpy := &selfDefenseAuthSpy{res: httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, Principal: execview.PrincipalView{ID: "alice"}}}
			innerCalls := 0
			h := stackHTTPHandler(stackHTTPInput{
				Cfg:      &coreconfig.Config{},
				Log:      testkit.DiscardLogger(),
				TraceGen: diag.NewTraceIDGenerator(),
				Security: HTTPSecurityInput{
					HTTPAuthProviders: []httpauth.Provider{authSpy},
					SelfDefense: SelfDefenseSecurityInput{
						Policy:          selfDefenseStackPolicy(2),
						State:           state,
						Resolver:        GeoIPResolverConfig{Source: "direct"},
						ImpossiblePaths: true,
						Probe:           tc.probe,
						Observer:        observer,
						Now:             func() time.Time { return now },
					},
				},
				Inner: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					innerCalls++
					w.WriteHeader(http.StatusOK)
				}),
			})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, selfDefenseRequest("/v1/models", "198.51.100.12:443"))
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
			if innerCalls != tc.wantInner {
				t.Fatalf("route calls = %d, want %d", innerCalls, tc.wantInner)
			}
			wantAuth := 0
			if tc.wantInner > 0 {
				wantAuth = 1
			}
			if authSpy.calls != wantAuth {
				t.Fatalf("transport auth calls = %d, want %d", authSpy.calls, wantAuth)
			}
			if tc.wantCode == http.StatusTooManyRequests {
				if len(observer.denials) != 1 || observer.denials[0] != ingressdefense.ReasonActiveQuarantine {
					t.Fatalf("denials = %v, want exactly one active_quarantine", observer.denials)
				}
				return
			}
			if len(observer.denials) != 0 {
				t.Fatalf("denials = %v, want none for a request that may authenticate", observer.denials)
			}
		})
	}
}

// TestStackHTTPHandlerSelfDefenseAuthObservationUsesTheGateSnapshot proves the
// paired auth observer is wired to the process-owned state and the generation
// policy: a terminal pre-principal 401 counts one offense, the threshold starts
// exactly one finite-reason quarantine, and a full successful chain clears the
// exact source address.
func TestStackHTTPHandlerSelfDefenseAuthObservationUsesTheGateSnapshot(t *testing.T) {
	t.Parallel()

	state := selfDefenseStackState(t)
	now := time.Date(2026, time.March, 2, 12, 0, 0, 0, time.UTC)
	observer := &selfDefenseObserverSpy{}
	policy := selfDefenseStackPolicy(2)
	addr := netip.MustParseAddr("198.51.100.13")
	build := func(res httpauth.AuthenticationResult) http.Handler {
		return stackHTTPHandler(stackHTTPInput{
			Cfg:      &coreconfig.Config{},
			Log:      testkit.DiscardLogger(),
			TraceGen: diag.NewTraceIDGenerator(),
			Security: HTTPSecurityInput{
				HTTPAuthProviders: []httpauth.Provider{&selfDefenseAuthSpy{res: res}},
				SelfDefense: SelfDefenseSecurityInput{
					Policy: policy, State: state, Resolver: GeoIPResolverConfig{Source: "direct"},
					ImpossiblePaths: true, Observer: observer, Now: func() time.Time { return now },
				},
			},
			Inner: http.NotFoundHandler(),
		})
	}
	unauthorized := httpauth.AuthenticationResult{Type: httpauth.TypeReject, HTTPStatus: http.StatusUnauthorized}
	accepted := httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, Principal: execview.PrincipalView{ID: "alice"}}

	build(unauthorized).ServeHTTP(httptest.NewRecorder(), selfDefenseRequest("/v1/models", addr.String()+":443"))
	if state.IsQuarantined(addr, now) {
		t.Fatal("one counted unauthenticated failure is below the configured threshold")
	}
	if len(observer.transitions) != 0 {
		t.Fatalf("transitions = %v, want none below the threshold", observer.transitions)
	}

	build(unauthorized).ServeHTTP(httptest.NewRecorder(), selfDefenseRequest("/v1/models", addr.String()+":443"))
	if !state.IsQuarantined(addr, now) {
		t.Fatal("the thresholded unauthenticated failure must start a quarantine")
	}
	if len(observer.transitions) != 1 || observer.transitions[0] != ingressdefense.ReasonAuthFailureThreshold {
		t.Fatalf("transitions = %v, want exactly one auth_failure_threshold", observer.transitions)
	}
	if len(observer.entryCounts) != 1 || observer.entryCounts[0] != state.Len() {
		t.Fatalf("entry counts = %v, want the post-transition entry count %d", observer.entryCounts, state.Len())
	}

	build(accepted).ServeHTTP(httptest.NewRecorder(), selfDefenseRequest("/v1/models", addr.String()+":443"))
	if state.Len() != 0 {
		t.Fatalf("entries = %d, want a full successful chain to clear the exact address", state.Len())
	}
}

// TestStackHTTPHandlerDisabledSelfDefenseOmitsGateAndAuthObservation proves the
// structural disabled posture: the same process-owned quarantine is ignored
// entirely, so a disabled generation performs no path matching, no adaptive
// lookup and no auth-outcome recording, and the request keeps the pre-existing
// behavior.
func TestStackHTTPHandlerDisabledSelfDefenseOmitsGateAndAuthObservation(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		input SelfDefenseSecurityInput
	}{
		{name: "self-defense absent", input: SelfDefenseSecurityInput{}},
		{name: "policy disabled", input: SelfDefenseSecurityInput{
			Policy: &ingressdefense.Policy{
				Enabled:           false,
				AuthFailures:      2,
				FailureWindow:     time.Minute,
				InitialQuarantine: time.Hour,
				MaxQuarantine:     2 * time.Hour,
			},
			State: selfDefenseStackState(t), Resolver: GeoIPResolverConfig{Source: "direct"},
			ImpossiblePaths: true,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state := selfDefenseStackState(t)
			now := time.Date(2026, time.March, 2, 12, 0, 0, 0, time.UTC)
			// A quarantine the process state already holds: a disabled generation
			// must not consult it, so the credential-free request below reaches
			// the auth chain instead of being refused.
			state.RecordProbe(netip.MustParseAddr("198.51.100.14"), now, *selfDefenseStackPolicy(2))
			observer := &selfDefenseObserverSpy{}
			authSpy := &selfDefenseAuthSpy{res: httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, Principal: execview.PrincipalView{ID: "alice"}}}
			innerCalls := 0
			input := tc.input
			if input.State == nil {
				input.State = state
			}
			input.Observer = observer
			input.Now = func() time.Time { return now }
			h := stackHTTPHandler(stackHTTPInput{
				Cfg:      &coreconfig.Config{},
				Log:      testkit.DiscardLogger(),
				TraceGen: diag.NewTraceIDGenerator(),
				Security: HTTPSecurityInput{
					HTTPAuthProviders: []httpauth.Provider{authSpy},
					SelfDefense:       input,
				},
				Inner: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					innerCalls++
					w.WriteHeader(http.StatusOK)
				}),
			})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, selfDefenseRequest("/.env", "198.51.100.14:443"))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: a disabled generation must not run the impossible-path matcher", rec.Code)
			}
			if innerCalls != 1 {
				t.Fatalf("route calls = %d, want 1", innerCalls)
			}
			if authSpy.calls != 1 {
				t.Fatalf("transport auth calls = %d, want 1", authSpy.calls)
			}
			if len(observer.denials) != 0 || len(observer.transitions) != 0 {
				t.Fatalf("observations = %v/%v, want none from a disabled generation", observer.denials, observer.transitions)
			}
			if state.Len() != 1 {
				t.Fatalf("entries = %d, want the pre-existing quarantine untouched by a disabled generation", state.Len())
			}
		})
	}
}

// TestSelfDefenseGateIsNotInstalledOnTheManagementListener proves requirement
// 1.3: the gate belongs to the standard data-plane handler graph only. The
// separate process-owned management/recovery surface answers an impossible path
// with its own routing, never with the self-defense refusal, and the management
// adapter imports no part of the feature.
func TestSelfDefenseGateIsNotInstalledOnTheManagementListener(t *testing.T) {
	t.Parallel()

	h, err := adminreload.NewHandler(adminreload.Options{
		Address:  "127.0.0.1:0",
		AuthMode: adminreload.AuthModeLocalTrust,
	}, isolatedCoordinator{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9999/.env", nil)
	req.RemoteAddr = "198.51.100.15:443"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("management status = %d, want its own 404", rec.Code)
	}
	if rec.Body.String() == "Not Found\n" {
		t.Fatal("the management surface must not answer with the self-defense generic refusal body")
	}
	if rec.Body.String() != "404 page not found\n" {
		t.Fatalf("management body = %q, want the management adapter's own not-found rendering", rec.Body.String())
	}

	entries, err := os.ReadDir(filepath.Join("admin", "configreload"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join("admin", "configreload", name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		for _, forbidden := range []string{"stdhttp/selfdefense", "stdhttp/contract", "selfdefense.Middleware"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("management adapter %s references %q; ingress self-defense must never wrap the management/recovery listener", name, forbidden)
			}
		}
	}
}

// TestSelfDefenseGateHasExactlyOneDataPlaneCallSite seals the placement: the
// gate is installed in exactly one place in the standard HTTP surface, so no
// other handler graph (management, admin, upgrade) can acquire it by accident.
func TestSelfDefenseGateHasExactlyOneDataPlaneCallSite(t *testing.T) {
	t.Parallel()

	var sites []string
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(src), "selfdefense.Middleware(") {
			sites = append(sites, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 1 || sites[0] != "middleware.go" {
		t.Fatalf("self-defense gate call sites = %v, want exactly [middleware.go]", sites)
	}
}

// isolatedCoordinator is a management-coordinator stub that never reloads.
type isolatedCoordinator struct{}

func (isolatedCoordinator) Reload(context.Context, sdkreload.Trigger) sdkreload.Result {
	return sdkreload.Result{Category: sdkreload.ResultInvalid, ReasonCategory: "not_implemented"}
}

func (isolatedCoordinator) Status() sdkreload.Status { return sdkreload.Status{} }

func (isolatedCoordinator) FixedSourcePath() string { return "/isolated/config.yaml" }
