package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
	"github.com/aiproxer/aiproxer/internal/infra/authevent"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

type capturingContinueProvider struct {
	gotCtx context.Context
}

func (p *capturingContinueProvider) Authenticate(ctx context.Context, _ http.ResponseWriter, _ *http.Request) (httpauth.AuthenticationResult, error) {
	p.gotCtx = ctx
	return httpauth.AuthenticationResult{Type: httpauth.TypeContinue}, nil
}

// mutatingAuthHeaderProvider replaces or deletes Authorization after an earlier Principal
// success, modeling a later Continue/Annotate provider that mutates the shared *http.Request.
type mutatingAuthHeaderProvider struct {
	gotCtx      context.Context
	replacement string // empty deletes the header
	resultType  httpauth.AuthenticationType
	annotate    http.Header
}

func (p *mutatingAuthHeaderProvider) Authenticate(ctx context.Context, _ http.ResponseWriter, r *http.Request) (httpauth.AuthenticationResult, error) {
	p.gotCtx = ctx
	if r != nil {
		if p.replacement == "" {
			r.Header.Del("Authorization")
		} else {
			r.Header.Set("Authorization", p.replacement)
		}
	}
	typ := p.resultType
	if typ == 0 {
		typ = httpauth.TypeContinue
	}
	return httpauth.AuthenticationResult{Type: typ, ResponseHeaders: p.annotate}, nil
}

// matcherProbeEventSink checks any context credential matcher against a sentinel and records
// an observable marker when the matcher binds that sentinel. Used to prove deferred attachment:
// later-provider auth-event dispatch must never emit the marker.
type matcherProbeEventSink struct {
	sentinel string
	marker   string
	emitted  []string
}

func (s *matcherProbeEventSink) OnAuthDecision(ctx context.Context, _ auth.AuthDecisionEvent) error {
	if m, ok := httpauth.CredentialMatcherFromContext(ctx); ok && m != nil {
		findings, err := m.ScanString(ctx, s.sentinel)
		if err == nil && len(findings) > 0 {
			s.emitted = append(s.emitted, s.marker)
		}
	}
	return nil
}

func (s *matcherProbeEventSink) OnSessionStart(context.Context, auth.SessionStartEvent) error {
	return nil
}

func policyAllowProvider(principalID, keyID string) *PolicyProvider {
	return NewPolicyProvider(&stubCoreAuthenticator{dec: auth.Decision{
		Outcome:   auth.OutcomeAllow,
		Principal: execview.PrincipalView{ID: principalID},
		Device:    auth.DeviceIdentity{KeyID: keyID},
	}}, nil, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey,
	}, nil)
}

func TestPeerIPFromRemoteAddr(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		addr string
		want string
	}{
		{name: "ipv4_port", addr: "203.0.113.10:54321", want: "203.0.113.10"},
		{name: "ipv6_bracket_port", addr: "[2001:db8::1]:443", want: "2001:db8::1"},
		{name: "host_only", addr: "203.0.113.11", want: "203.0.113.11"},
		{name: "ipv6_host_only", addr: "2001:db8::2", want: "2001:db8::2"},
		{name: "empty", addr: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := peerIPFromRemoteAddr(tc.addr); got != tc.want {
				t.Fatalf("peerIPFromRemoteAddr(%q)=%q want %q", tc.addr, got, tc.want)
			}
		})
	}
}

func TestPolicyProvider_allow_populatesIngressAttribution_peerIPFromRemoteAddr(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		remoteAddr string
		wantPeer   string
	}{
		{name: "ipv4", remoteAddr: "198.51.100.7:9999", wantPeer: "198.51.100.7"},
		{name: "ipv6", remoteAddr: "[2001:db8::9]:8443", wantPeer: "2001:db8::9"},
		{name: "host_only", remoteAddr: "198.51.100.8", wantPeer: "198.51.100.8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stub := &stubCoreAuthenticator{dec: auth.Decision{
				Outcome:        auth.OutcomeAllow,
				Principal:      execview.PrincipalView{ID: "u1"},
				Device:         auth.DeviceIdentity{ID: "dev-1", KeyID: "key-1", Fingerprint: "fp-1"},
				SatisfiedLevel: auth.LevelAPIKey,
			}}
			p := NewPolicyProvider(stub, nil, PolicySnapshot{
				AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey,
			}, nil)
			req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set("X-Forwarded-For", "203.0.113.50")
			req.Header.Set("X-Real-IP", "203.0.113.51")
			req.Header.Set("Forwarded", "for=203.0.113.52")
			res, err := p.Authenticate(req.Context(), httptest.NewRecorder(), req)
			if err != nil {
				t.Fatal(err)
			}
			if res.Type != httpauth.TypePrincipal {
				t.Fatalf("type: %v", res.Type)
			}
			if res.SatisfiedLevel != auth.LevelAPIKey {
				t.Fatalf("SatisfiedLevel: got %q want %q", res.SatisfiedLevel, auth.LevelAPIKey)
			}
			if res.IngressAttribution.PeerIP != tc.wantPeer {
				t.Fatalf("PeerIP: got %q want %q", res.IngressAttribution.PeerIP, tc.wantPeer)
			}
			if res.IngressAttribution.FrontendID != "openai_compatible" {
				t.Fatalf("FrontendID: %q", res.IngressAttribution.FrontendID)
			}
			if res.IngressAttribution.DeviceID != "dev-1" || res.IngressAttribution.KeyID != "key-1" || res.IngressAttribution.Fingerprint != "fp-1" {
				t.Fatalf("device fields: %+v", res.IngressAttribution)
			}
		})
	}
}

func TestPolicyProvider_allow_doesNotAttachMatcherDuringAuthenticate(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticBearerCredential
	stub := &stubCoreAuthenticator{dec: auth.Decision{
		Outcome:   auth.OutcomeAllow,
		Principal: execview.PrincipalView{ID: "u1"},
		Device:    auth.DeviceIdentity{KeyID: "app-key"},
	}}
	p := NewPolicyProvider(stub, nil, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey,
	}, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("Authorization", "Bearer "+secret)
	res, err := p.Authenticate(req.Context(), httptest.NewRecorder(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Type != httpauth.TypePrincipal {
		t.Fatalf("type: %v", res.Type)
	}
	if gotM, ok := httpauth.CredentialMatcherFromContext(req.Context()); ok || gotM != nil {
		t.Fatalf("authenticate must not attach matcher, got ok=%v matcher=%v", ok, gotM)
	}
}

func TestPolicyProvider_allow_noMatcherWithoutPresentedCredential(t *testing.T) {
	t.Parallel()
	stub := &stubCoreAuthenticator{dec: auth.Decision{
		Outcome:   auth.OutcomeAllow,
		Principal: execview.PrincipalView{ID: "u1"},
	}}
	p := NewPolicyProvider(stub, nil, PolicySnapshot{
		AccessMode: auth.AccessSingleUser, HandlerKind: auth.HandlerLocalNoop, RequiredLevel: auth.LevelNone,
	}, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.RemoteAddr = "127.0.0.1:2"
	res, err := p.Authenticate(req.Context(), httptest.NewRecorder(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Type != httpauth.TypePrincipal {
		t.Fatalf("type: %v", res.Type)
	}
	if gotM, ok := httpauth.CredentialMatcherFromContext(req.Context()); ok || gotM != nil {
		t.Fatalf("allow without presented credential must not attach matcher, got ok=%v matcher=%v", ok, gotM)
	}
}

func TestAcceptedRequestCredential_usesAuthoritativeAcceptanceEvidence(t *testing.T) {
	t.Parallel()
	withScope := func(method, credential string) *scope.PrincipalScopeView {
		return &scope.PrincipalScopeView{
			PrincipalID:  scope.Known("user-1"),
			AuthMethod:   scope.Known(method),
			CredentialID: scope.Known(credential),
		}
	}
	cases := []struct {
		name    string
		handler auth.HandlerKind
		result  httpauth.AuthenticationResult
		want    bool
	}{
		{name: "legacy local principal", handler: auth.HandlerLocalAPIKey, result: httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, Principal: execview.PrincipalView{ID: "user-1"}}, want: true},
		{name: "local noop principal", handler: auth.HandlerLocalNoop, result: httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, Principal: execview.PrincipalView{ID: "user-1"}}, want: false},
		{name: "local noop scope cannot authenticate", handler: auth.HandlerLocalNoop, result: httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, Scope: withScope("api_key", "key-1")}, want: false},
		{name: "remote none principal", handler: auth.HandlerRemote, result: httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, Principal: execview.PrincipalView{ID: "user-1"}}, want: false},
		{name: "remote api key level", handler: auth.HandlerRemote, result: httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, SatisfiedLevel: auth.LevelAPIKey}, want: true},
		{name: "remote sso api key level", handler: auth.HandlerRemote, result: httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, SatisfiedLevel: auth.LevelAPIKeySSO}, want: true},
		{name: "authoritative key id", handler: auth.HandlerRemote, result: httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, IngressAttribution: httpauth.IngressAttribution{KeyID: "key-1"}}, want: true},
		{name: "scope credential", handler: auth.HandlerRemote, result: httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, Scope: withScope("oidc", "key-1")}, want: true},
		{name: "scope auth method", handler: auth.HandlerRemote, result: httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, Scope: withScope("api_key", "")}, want: true},
		{name: "unknown remote scope", handler: auth.HandlerRemote, result: httpauth.AuthenticationResult{Type: httpauth.TypePrincipal, Scope: withScope("oidc", "")}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := &PolicyProvider{Policy: PolicySnapshot{HandlerKind: tc.handler}}
			if got := acceptedRequestCredential(p, tc.result); got != tc.want {
				t.Fatalf("acceptedRequestCredential()=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestPolicyProvider_nonAllowDoesNotAttachMatcher(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		dec  auth.Decision
		err  error
	}{
		{
			name: "deny",
			dec: auth.Decision{
				Outcome:   auth.OutcomeDeny,
				Principal: execview.PrincipalView{ID: "u1"},
			},
		},
		{
			name: "challenge",
			dec: auth.Decision{
				Outcome:   auth.OutcomeChallenge,
				Principal: execview.PrincipalView{ID: "u1"},
			},
		},
		{
			name: "error",
			err:  errors.New("auth backend unavailable"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stub := &stubCoreAuthenticator{dec: tc.dec, err: tc.err}
			p := NewPolicyProvider(stub, nil, PolicySnapshot{
				AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey,
			}, nil)
			req := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
			req.Header.Set("Authorization", "Bearer "+testkit.SyntheticBearerCredential)
			_, _ = p.Authenticate(req.Context(), httptest.NewRecorder(), req)
			if gotM, ok := httpauth.CredentialMatcherFromContext(req.Context()); ok || gotM != nil {
				t.Fatalf("non-allow path must not attach matcher, got ok=%v matcher=%v", ok, gotM)
			}
		})
	}
}

type attributionStubProvider struct {
	res httpauth.AuthenticationResult
}

func (s attributionStubProvider) Authenticate(context.Context, http.ResponseWriter, *http.Request) (httpauth.AuthenticationResult, error) {
	return s.res, nil
}

func TestMiddleware_attachesAttributionAndMatcher(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticBearerCredential
	attr := httpauth.IngressAttribution{PeerIP: "203.0.113.1", FrontendID: "gemini", KeyID: "kid"}
	authProvider := NewPolicyProvider(&stubCoreAuthenticator{dec: auth.Decision{
		Outcome:   auth.OutcomeAllow,
		Principal: execview.PrincipalView{ID: "u"},
		Device:    auth.DeviceIdentity{KeyID: "kid"},
	}}, nil, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey,
	}, nil)
	capture := &capturingContinueProvider{}
	var gotCtx context.Context
	h := Middleware(nil, []httpauth.Provider{authProvider, capture}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotCtx = r.Context()
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
	req.RemoteAddr = "203.0.113.1:1234"
	req.Header.Set("Authorization", "Bearer "+secret)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	gotAttr, ok := httpauth.IngressAttributionFromContext(gotCtx)
	if !ok {
		t.Fatal("expected attribution in final context")
	}
	if gotAttr.PeerIP != attr.PeerIP || gotAttr.FrontendID != "gemini" || gotAttr.KeyID != attr.KeyID {
		t.Fatalf("attribution got=%+v want=%+v", gotAttr, attr)
	}
	gotM, ok := httpauth.CredentialMatcherFromContext(gotCtx)
	if !ok || gotM == nil {
		t.Fatalf("downstream matcher ok=%v got=%v", ok, gotM)
	}
	var resolver secretguard.MatcherResolver = secretguard.ContextMatcherResolver{}
	resolved, err := resolver.Resolve(gotCtx)
	if err != nil {
		t.Fatal(err)
	}
	if resolved == nil {
		t.Fatal("ContextMatcherResolver must resolve middleware-attached matcher")
	}
	if capture.gotCtx == nil {
		t.Fatal("expected second provider to observe accumulated context")
	}
	if p, ok := httpauth.PrincipalFromContext(capture.gotCtx); !ok || p.ID != "u" {
		t.Fatalf("capture principal ok=%v got=%+v", ok, p)
	}
	if gotAttrMid, ok := httpauth.IngressAttributionFromContext(capture.gotCtx); !ok {
		t.Fatal("later provider must see accumulated ingress attribution")
	} else if gotAttrMid.KeyID != attr.KeyID || gotAttrMid.PeerIP != attr.PeerIP {
		t.Fatalf("later provider attribution got=%+v want=%+v", gotAttrMid, attr)
	}
	if m, ok := httpauth.CredentialMatcherFromContext(capture.gotCtx); ok || m != nil {
		t.Fatalf("later provider must not see deferred credential matcher, got ok=%v matcher=%v", ok, m)
	}
}

// adversarialBearerSentinel is an unmistakably fake Authorization bearer used only to prove
// deferred matcher attachment: later-chain audit logs must never contain it, while the
// downstream handler still receives a matcher that can detect/redact it.
const adversarialBearerSentinel = "aip-FAKE-ADVERSARIAL-BEARER-SENTINEL-do-not-log-009"

type authEventEmittingContinueProvider struct {
	events *coreauth.EventDispatcher
	gotCtx context.Context
	err    error
}

func (p *authEventEmittingContinueProvider) Authenticate(ctx context.Context, _ http.ResponseWriter, _ *http.Request) (httpauth.AuthenticationResult, error) {
	p.gotCtx = ctx
	if p.events != nil {
		err := p.events.DispatchAuthDecision(ctx, auth.AuthDecisionEvent{
			Time:       time.Unix(1700000099, 0).UTC(),
			TraceID:    "adversarial-chain-trace",
			AccessMode: auth.AccessMultiUser,
			Outcome:    auth.OutcomeAllow,
			Frontend:   "openai_compatible",
			ReasonCode: "chain_continue_audit",
		})
		if err != nil {
			p.err = err
			return httpauth.AuthenticationResult{}, err
		}
	}
	return httpauth.AuthenticationResult{Type: httpauth.TypeContinue}, nil
}

func TestMiddleware_deferredMatcher_laterProviderAuditLogsOmitBearerSentinel(t *testing.T) {
	t.Parallel()
	var logBuf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slogSink, err := authevent.NewSlogEventSink(log)
	if err != nil {
		t.Fatal(err)
	}
	const probeMarker = "MATCHER_BOUND_SENTINEL_IN_AUTH_EVENT_CTX"
	probe := &matcherProbeEventSink{sentinel: adversarialBearerSentinel, marker: probeMarker}
	dispatcher := coreauth.NewEventDispatcher(
		multiAuthEventSink{sinks: []coreauth.EventSink{probe, slogSink}},
		coreauth.EventFailureFailClosed,
	)

	first := policyAllowProvider("adv-user", "adv-key")
	later := &authEventEmittingContinueProvider{events: dispatcher}

	var handlerMatcher secretguard.Matcher
	var sawHandler bool
	h := Middleware(nil, []httpauth.Provider{first, later}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		sawHandler = true
		var ok bool
		handlerMatcher, ok = httpauth.CredentialMatcherFromContext(r.Context())
		if !ok || handlerMatcher == nil {
			t.Fatal("downstream handler must receive deferred credential matcher")
		}
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	req.RemoteAddr = "198.51.100.20:443"
	req.Header.Set("Authorization", "Bearer "+adversarialBearerSentinel)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if later.err != nil {
		t.Fatalf("later provider dispatch: %v", later.err)
	}
	if later.gotCtx == nil {
		t.Fatal("later provider did not run")
	}
	if m, ok := httpauth.CredentialMatcherFromContext(later.gotCtx); ok || m != nil {
		t.Fatalf("later provider must not observe matcher, got ok=%v matcher=%v", ok, m)
	}
	if p, ok := httpauth.PrincipalFromContext(later.gotCtx); !ok || p.ID != "adv-user" {
		t.Fatalf("later provider principal ok=%v got=%+v", ok, p)
	}
	if attr, ok := httpauth.IngressAttributionFromContext(later.gotCtx); !ok || attr.KeyID != "adv-key" {
		t.Fatalf("later provider attribution ok=%v got=%+v", ok, attr)
	}
	if len(probe.emitted) != 0 {
		t.Fatalf("later-provider auth-event dispatch must not emit matcher probe marker %q; got %v", probeMarker, probe.emitted)
	}

	logged := logBuf.String()
	if logged == "" {
		t.Fatal("expected real auth event sink to emit a log record")
	}
	if strings.Contains(logged, adversarialBearerSentinel) {
		t.Fatalf("auth audit logs must not contain bearer sentinel; log=%s", logged)
	}
	if !strings.Contains(logged, "aip.auth.auth_decision") {
		t.Fatalf("expected auth_decision log, got %q", logged)
	}
	if !sawHandler {
		t.Fatal("expected downstream handler")
	}
	findings, err := handlerMatcher.ScanString(context.Background(), "leak:"+adversarialBearerSentinel)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].SecretRefName != "adv-key" {
		t.Fatalf("handler matcher findings: %+v", findings)
	}
	redacted, findings, err := handlerMatcher.RedactString(context.Background(), "leak:"+adversarialBearerSentinel)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("redact findings: %+v", findings)
	}
	if strings.Contains(redacted, adversarialBearerSentinel) {
		t.Fatalf("redacted output still contains sentinel: %q", redacted)
	}
}

type multiAuthEventSink struct {
	sinks []coreauth.EventSink
}

func (m multiAuthEventSink) OnAuthDecision(ctx context.Context, ev auth.AuthDecisionEvent) error {
	for _, s := range m.sinks {
		if s == nil {
			continue
		}
		if err := s.OnAuthDecision(ctx, ev); err != nil {
			return err
		}
	}
	return nil
}

func (m multiAuthEventSink) OnSessionStart(ctx context.Context, ev auth.SessionStartEvent) error {
	for _, s := range m.sinks {
		if s == nil {
			continue
		}
		if err := s.OnSessionStart(ctx, ev); err != nil {
			return err
		}
	}
	return nil
}

func TestMiddleware_deferredMatcher_survivesLaterAuthorizationMutation(t *testing.T) {
	t.Parallel()
	const original = adversarialBearerSentinel
	const replacement = "aip-FAKE-REPLACEMENT-BEARER-should-not-bind-008"
	first := policyAllowProvider("orig-user", "orig-key")
	later := &mutatingAuthHeaderProvider{replacement: "Bearer " + replacement}
	var gotCtx context.Context
	var sawHandler bool
	h := Middleware(nil, []httpauth.Provider{first, later}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		sawHandler = true
		gotCtx = r.Context()
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	req.RemoteAddr = "198.51.100.30:443"
	req.Header.Set("Authorization", "Bearer "+original)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if later.gotCtx == nil {
		t.Fatal("later provider did not run")
	}
	if m, ok := httpauth.CredentialMatcherFromContext(later.gotCtx); ok || m != nil {
		t.Fatalf("later provider must not observe matcher, got ok=%v matcher=%v", ok, m)
	}
	if !sawHandler {
		t.Fatal("expected downstream handler")
	}
	gotM, ok := httpauth.CredentialMatcherFromContext(gotCtx)
	if !ok || gotM == nil {
		t.Fatal("downstream must receive credential matcher")
	}
	origFindings, err := gotM.ScanString(context.Background(), original)
	if err != nil {
		t.Fatal(err)
	}
	if len(origFindings) != 1 || origFindings[0].SecretRefName != "orig-key" {
		t.Fatalf("matcher must bind originally authenticated credential; findings=%+v", origFindings)
	}
	replFindings, err := gotM.ScanString(context.Background(), replacement)
	if err != nil {
		t.Fatal(err)
	}
	if len(replFindings) != 0 {
		t.Fatalf("matcher must not bind later Authorization replacement; findings=%+v", replFindings)
	}
	attr, ok := httpauth.IngressAttributionFromContext(gotCtx)
	if !ok || attr.KeyID != "orig-key" {
		t.Fatalf("KeyID attribution got ok=%v attr=%+v", ok, attr)
	}
	if p, ok := httpauth.PrincipalFromContext(gotCtx); !ok || p.ID != "orig-user" {
		t.Fatalf("principal ok=%v got=%+v", ok, p)
	}
	if _, ok := httpauth.ScopeFromContext(gotCtx); !ok {
		t.Fatal("expected non-nil scope on success path")
	}
}

func TestMiddleware_providerChain_matcherDeferralMatrix(t *testing.T) {
	t.Parallel()
	const original = adversarialBearerSentinel
	const replacement = "aip-FAKE-CHAIN-REPLACEMENT-BEARER-007"

	const (
		laterContinue               = "continue"
		laterAnnotate               = "annotate"
		laterReject                 = "reject"
		laterChallenge              = "challenge"
		laterProviderError          = "provider_error"
		laterSecondPrincipal        = "second_principal_replaces"
		laterSecondNilAttacher      = "second_principal_nil_attacher_clears"
		laterSecondNonAttacherKeeps = "second_principal_non_attacher_preserves"
	)

	cases := []struct {
		name          string
		later         string
		wantCode      int
		wantHandler   bool
		wantPrincipal string
		wantKeyID     string
		wantOrigBound bool
		wantReplBound bool
		wantMatcher   bool
	}{
		{
			name:  "continue_mutates_auth",
			later: laterContinue, wantCode: http.StatusOK, wantHandler: true,
			wantPrincipal: "p1", wantKeyID: "k1", wantOrigBound: true, wantMatcher: true,
		},
		{
			name:  "annotate_mutates_auth",
			later: laterAnnotate, wantCode: http.StatusOK, wantHandler: true,
			wantPrincipal: "p1", wantKeyID: "k1", wantOrigBound: true, wantMatcher: true,
		},
		{
			name:  "reject_after_principal",
			later: laterReject, wantCode: http.StatusUnauthorized, wantHandler: false,
		},
		{
			name:  "challenge_after_principal",
			later: laterChallenge, wantCode: http.StatusUnauthorized, wantHandler: false,
		},
		{
			name:  "provider_error_after_principal",
			later: laterProviderError, wantCode: http.StatusInternalServerError, wantHandler: false,
		},
		{
			name:  "multiple_principal_last_non_nil_attacher",
			later: laterSecondPrincipal, wantCode: http.StatusOK, wantHandler: true,
			wantPrincipal: "p2", wantKeyID: "k2", wantReplBound: true, wantMatcher: true,
		},
		{
			name:  "multiple_principal_nil_attacher_clears_prior",
			later: laterSecondNilAttacher, wantCode: http.StatusOK, wantHandler: true,
			wantPrincipal: "p2", wantKeyID: "k2", wantMatcher: false,
		},
		{
			name:  "multiple_principal_non_attacher_preserves_prior",
			later: laterSecondNonAttacherKeeps, wantCode: http.StatusOK, wantHandler: true,
			wantPrincipal: "p2", wantKeyID: "k2", wantOrigBound: true, wantMatcher: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			first := policyAllowProvider("p1", "k1")
			var laterProviders []httpauth.Provider
			var laterCtxs []*context.Context
			recordLater := func(got *context.Context) {
				laterCtxs = append(laterCtxs, got)
			}
			switch tc.later {
			case laterContinue:
				p := &mutatingAuthHeaderProvider{replacement: "Bearer " + replacement}
				laterProviders = []httpauth.Provider{p}
				recordLater(&p.gotCtx)
			case laterAnnotate:
				p := &mutatingAuthHeaderProvider{
					replacement: "Bearer " + replacement,
					resultType:  httpauth.TypeAnnotate,
					annotate:    http.Header{"Cache-Control": []string{"no-store"}},
				}
				laterProviders = []httpauth.Provider{p}
				recordLater(&p.gotCtx)
			case laterReject:
				p := &ctxCapturingStubProvider{res: httpauth.AuthenticationResult{
					Type: httpauth.TypeReject, HTTPStatus: http.StatusUnauthorized, Body: []byte("nope"),
				}}
				laterProviders = []httpauth.Provider{p}
				recordLater(&p.gotCtx)
			case laterChallenge:
				p := &ctxCapturingStubProvider{res: httpauth.AuthenticationResult{
					Type: httpauth.TypeChallenge, HTTPStatus: http.StatusUnauthorized, Body: []byte("auth"),
				}}
				laterProviders = []httpauth.Provider{p}
				recordLater(&p.gotCtx)
			case laterProviderError:
				p := &errorProvider{err: errors.New("provider boom")}
				laterProviders = []httpauth.Provider{p}
				recordLater(&p.gotCtx)
			case laterSecondPrincipal:
				mut := &mutatingAuthHeaderProvider{replacement: "Bearer " + replacement}
				second := &ctxCapturingProvider{inner: policyAllowProvider("p2", "k2")}
				laterProviders = []httpauth.Provider{mut, second}
				recordLater(&mut.gotCtx)
				recordLater(&second.gotCtx)
			case laterSecondNilAttacher:
				mut := &mutatingAuthHeaderProvider{replacement: ""} // delete Authorization
				second := &ctxCapturingProvider{inner: policyAllowProvider("p2", "k2")}
				laterProviders = []httpauth.Provider{mut, second}
				recordLater(&mut.gotCtx)
				recordLater(&second.gotCtx)
			case laterSecondNonAttacherKeeps:
				mut := &mutatingAuthHeaderProvider{replacement: "Bearer " + replacement}
				second := &principalWithoutAttacher{
					res: httpauth.AuthenticationResult{
						Type:               httpauth.TypePrincipal,
						Principal:          execview.PrincipalView{ID: "p2"},
						IngressAttribution: httpauth.IngressAttribution{KeyID: "k2"},
					},
				}
				laterProviders = []httpauth.Provider{mut, second}
				recordLater(&mut.gotCtx)
				recordLater(&second.gotCtx)
			default:
				t.Fatalf("unknown later kind %q", tc.later)
			}

			providers := append([]httpauth.Provider{first}, laterProviders...)
			var gotCtx context.Context
			var sawHandler bool
			h := Middleware(nil, providers, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				sawHandler = true
				gotCtx = r.Context()
			}))
			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			req.RemoteAddr = "203.0.113.80:443"
			req.Header.Set("Authorization", "Bearer "+original)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("code %d want %d", rec.Code, tc.wantCode)
			}
			if sawHandler != tc.wantHandler {
				t.Fatalf("handler called=%v want %v", sawHandler, tc.wantHandler)
			}
			for i, ctxp := range laterCtxs {
				if ctxp == nil || *ctxp == nil {
					t.Fatalf("later provider %d did not run", i)
				}
				if m, ok := httpauth.CredentialMatcherFromContext(*ctxp); ok || m != nil {
					t.Fatalf("later provider %d must not observe matcher", i)
				}
				// Every later provider, including a second Principal, must see prior
				// principal/scope/attribution on its incoming context — never the matcher.
				wantIncomingPrincipal := "p1"
				wantIncomingKeyID := "k1"
				if p, ok := httpauth.PrincipalFromContext(*ctxp); !ok || p.ID != wantIncomingPrincipal {
					t.Fatalf("later provider %d incoming principal ok=%v got=%+v want %q", i, ok, p, wantIncomingPrincipal)
				}
				if _, ok := httpauth.ScopeFromContext(*ctxp); !ok {
					t.Fatalf("later provider %d must observe non-nil scope", i)
				}
				if attr, ok := httpauth.IngressAttributionFromContext(*ctxp); !ok || attr.KeyID != wantIncomingKeyID {
					t.Fatalf("later provider %d incoming attribution ok=%v attr=%+v want KeyID %q", i, ok, attr, wantIncomingKeyID)
				}
			}
			if !tc.wantHandler {
				return
			}
			if p, ok := httpauth.PrincipalFromContext(gotCtx); !ok || p.ID != tc.wantPrincipal {
				t.Fatalf("downstream principal ok=%v got=%+v want %q", ok, p, tc.wantPrincipal)
			}
			if _, ok := httpauth.ScopeFromContext(gotCtx); !ok {
				t.Fatal("downstream must observe non-nil scope")
			}
			attr, ok := httpauth.IngressAttributionFromContext(gotCtx)
			if !ok || attr.KeyID != tc.wantKeyID {
				t.Fatalf("downstream KeyID ok=%v got=%+v want %q", ok, attr, tc.wantKeyID)
			}
			gotM, mok := httpauth.CredentialMatcherFromContext(gotCtx)
			if !tc.wantMatcher {
				if mok || gotM != nil {
					t.Fatalf("downstream matcher must be absent, ok=%v matcher=%v", mok, gotM)
				}
				return
			}
			if !mok || gotM == nil {
				t.Fatal("downstream must receive credential matcher")
			}
			origFindings, err := gotM.ScanString(context.Background(), original)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantOrigBound && (len(origFindings) != 1 || origFindings[0].SecretRefName != "k1") {
				t.Fatalf("expected original credential bound with k1; findings=%+v", origFindings)
			}
			if !tc.wantOrigBound && len(origFindings) != 0 {
				t.Fatalf("original credential must not bind; findings=%+v", origFindings)
			}
			replFindings, err := gotM.ScanString(context.Background(), replacement)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantReplBound {
				if len(replFindings) != 1 || replFindings[0].SecretRefName != "k2" {
					t.Fatalf("expected replacement credential bound with k2; findings=%+v", replFindings)
				}
			} else if len(replFindings) != 0 {
				t.Fatalf("replacement credential must not bind; findings=%+v", replFindings)
			}
		})
	}
}

// principalWithoutAttacher returns TypePrincipal but does not implement
// authSuccessContextAttacher, so middleware must preserve any earlier matcher.
type principalWithoutAttacher struct {
	gotCtx context.Context
	res    httpauth.AuthenticationResult
}

func (p *principalWithoutAttacher) Authenticate(ctx context.Context, _ http.ResponseWriter, _ *http.Request) (httpauth.AuthenticationResult, error) {
	p.gotCtx = ctx
	return p.res, nil
}

type ctxCapturingStubProvider struct {
	gotCtx context.Context
	res    httpauth.AuthenticationResult
}

func (p *ctxCapturingStubProvider) Authenticate(ctx context.Context, _ http.ResponseWriter, _ *http.Request) (httpauth.AuthenticationResult, error) {
	p.gotCtx = ctx
	return p.res, nil
}

// ctxCapturingProvider records the incoming Authenticate context, then delegates.
// Used so a second Principal provider can prove it sees prior principal/scope/attribution
// but never the pending credential matcher. It forwards authSuccessContextAttacher when
// the inner provider implements capture.
type ctxCapturingProvider struct {
	gotCtx context.Context
	inner  httpauth.Provider
}

func (p *ctxCapturingProvider) Authenticate(ctx context.Context, w http.ResponseWriter, r *http.Request) (httpauth.AuthenticationResult, error) {
	p.gotCtx = ctx
	if p.inner == nil {
		return httpauth.AuthenticationResult{Type: httpauth.TypeContinue}, nil
	}
	return p.inner.Authenticate(ctx, w, r)
}

func (p *ctxCapturingProvider) captureAuthSuccessMatcher(r *http.Request, res httpauth.AuthenticationResult) secretguard.Matcher {
	if p == nil {
		return nil
	}
	if attacher, ok := p.inner.(authSuccessContextAttacher); ok {
		return attacher.captureAuthSuccessMatcher(r, res)
	}
	return nil
}

type errorProvider struct {
	gotCtx context.Context
	err    error
}

func (p *errorProvider) Authenticate(ctx context.Context, _ http.ResponseWriter, _ *http.Request) (httpauth.AuthenticationResult, error) {
	p.gotCtx = ctx
	return httpauth.AuthenticationResult{}, p.err
}

func TestMiddleware_rejectAfterSuccess_doesNotAttachMatcher(t *testing.T) {
	t.Parallel()
	first := NewPolicyProvider(&stubCoreAuthenticator{dec: auth.Decision{
		Outcome:   auth.OutcomeAllow,
		Principal: execview.PrincipalView{ID: "u"},
		Device:    auth.DeviceIdentity{KeyID: "kid"},
	}}, nil, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey,
	}, nil)
	reject := attributionStubProvider{res: httpauth.AuthenticationResult{
		Type:       httpauth.TypeReject,
		HTTPStatus: http.StatusUnauthorized,
		Body:       []byte("nope"),
	}}
	var sawHandler bool
	h := Middleware(nil, []httpauth.Provider{first, reject}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		sawHandler = true
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+adversarialBearerSentinel)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code %d", rec.Code)
	}
	if sawHandler {
		t.Fatal("reject must not reach downstream handler")
	}
}

func TestContextMatcherResolver_afterMiddleware_noEnvFallbackWhenAbsent(t *testing.T) {
	// Not parallel: uses t.Setenv.
	const probe = "STDHTTP_AUTH_MATCHER_ABSENT_PROBE"
	t.Setenv(probe, "must-not-be-used-as-credential")
	if _, ok := os.LookupEnv(probe); !ok {
		t.Fatal("setup: probe env not set")
	}
	p := attributionStubProvider{res: httpauth.AuthenticationResult{
		Type:      httpauth.TypePrincipal,
		Principal: execview.PrincipalView{ID: "u"},
	}}
	var gotCtx context.Context
	h := Middleware(nil, []httpauth.Provider{p}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotCtx = r.Context()
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	var resolver secretguard.MatcherResolver = secretguard.ContextMatcherResolver{}
	got, err := resolver.Resolve(gotCtx)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("absent matcher must resolve to nil without env fallback")
	}
}

func TestMiddleware_customProviderDoesNotInventMatcher(t *testing.T) {
	t.Parallel()
	p := attributionStubProvider{res: httpauth.AuthenticationResult{
		Type:      httpauth.TypePrincipal,
		Principal: execview.PrincipalView{ID: "custom"},
	}}
	var gotCtx context.Context
	h := Middleware(nil, []httpauth.Provider{p}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotCtx = r.Context()
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if gotM, ok := httpauth.CredentialMatcherFromContext(gotCtx); ok || gotM != nil {
		t.Fatalf("custom provider must not invent matcher, got ok=%v matcher=%v", ok, gotM)
	}
}

func TestExactCredentialMatcher_scanAndRedactBytes(t *testing.T) {
	t.Parallel()
	secret := []byte(testkit.SyntheticUnicodeSecret)
	m := newExactCredentialMatcher(string(secret), "")
	input := append([]byte("x"), append(secret, []byte("y")...)...)
	findings, err := m.ScanBytes(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].SecretRefName != "request_credential" {
		t.Fatalf("findings: %+v", findings)
	}
	if findings[0].OccurrenceCount != 1 {
		t.Fatalf("count: %d", findings[0].OccurrenceCount)
	}
	out, findings, err := m.RedactBytes(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("redact findings: %+v", findings)
	}
	if bytes.Contains(out, secret) {
		t.Fatal("redacted output still contains secret bytes")
	}
	if len(out) != len(input) {
		t.Fatalf("length: got %d want %d", len(out), len(input))
	}
	want := append([]byte("x"), append(bytes.Repeat([]byte("*"), len(secret)), []byte("y")...)...)
	if !bytes.Equal(out, want) {
		t.Fatalf("redacted mismatch")
	}
}

func TestExactCredentialMatcher_scanOccurrencesPreservesSafeAttribution(t *testing.T) {
	t.Parallel()
	secret := []byte(testkit.SyntheticUnicodeSecret)
	m := newExactCredentialMatcher(string(secret), "accepted-key")
	positional, ok := m.(secretguard.PositionalMatcher)
	if !ok {
		t.Fatal("authenticated request matcher must expose the neutral positional capability")
	}
	input := append([]byte("left "), secret...)
	input = append(input, []byte(" middle ")...)
	input = append(input, secret...)
	got := positional.ScanOccurrences(input)
	if len(got) != 2 {
		t.Fatalf("positional occurrences = %d, want two", len(got))
	}
	wantStart := []int{len("left "), len("left ") + len(secret) + len(" middle ")}
	for i, occurrence := range got {
		if occurrence.Start != wantStart[i] || occurrence.End != wantStart[i]+len(secret) {
			t.Fatalf("occurrence[%d] span = %d..%d, want %d..%d", i, occurrence.Start, occurrence.End, wantStart[i], wantStart[i]+len(secret))
		}
		if occurrence.Finding.SecretRefName != "accepted-key" || occurrence.Finding.SourceCategory != secretguard.SourceCategoryRequestCred {
			t.Fatalf("occurrence[%d] attribution = %#v", i, occurrence.Finding)
		}
	}
}

func TestPolicyProvider_allow_matcherUsesDefaultRefWhenKeyIDEmpty(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticBearerCredential
	stub := &stubCoreAuthenticator{dec: auth.Decision{
		Outcome:   auth.OutcomeAllow,
		Principal: execview.PrincipalView{ID: "u1"},
	}}
	p := NewPolicyProvider(stub, nil, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey,
	}, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/foo", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	var gotM secretguard.Matcher
	var sawInner bool
	Middleware(nil, []httpauth.Provider{p}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		sawInner = true
		var ok bool
		gotM, ok = httpauth.CredentialMatcherFromContext(r.Context())
		if !ok || gotM == nil {
			t.Fatal("expected matcher in middleware context")
		}
	})).ServeHTTP(httptest.NewRecorder(), req)
	if !sawInner {
		t.Fatal("expected middleware to reach inner handler")
	}
	findings, err := gotM.ScanString(context.Background(), secret)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].SecretRefName != "request_credential" {
		t.Fatalf("findings: %+v", findings)
	}
}

// Ensure LocalAPIKeyAuthenticator path still builds matcher from request bearer (adapter),
// and Decision remains free of raw secret accessors.
func TestPolicyProvider_localAPIKeyAllow_matcherFromRequestNotDecision(t *testing.T) {
	t.Parallel()
	secret := testkit.SyntheticBearerCredential
	ak, err := coreauth.NewLocalAPIKeyAuthenticator([]coreauth.LocalAPIKeyRecord{
		{KeyID: "k-local", PrincipalID: "p1", Key: secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := NewPolicyProvider(ak, nil, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey,
	}, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/chat", nil)
	req.RemoteAddr = "10.0.0.1:80"
	req.Header.Set("Authorization", "Bearer "+secret)
	var gotM secretguard.Matcher
	var gotAttr httpauth.IngressAttribution
	var sawInner bool
	Middleware(nil, []httpauth.Provider{p}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		sawInner = true
		gotAttr, _ = httpauth.IngressAttributionFromContext(r.Context())
		var ok bool
		gotM, ok = httpauth.CredentialMatcherFromContext(r.Context())
		if !ok || gotM == nil {
			t.Fatal("expected matcher built from request bearer")
		}
	})).ServeHTTP(httptest.NewRecorder(), req)
	if !sawInner {
		t.Fatal("expected middleware to reach inner handler")
	}
	if gotAttr.KeyID != "k-local" {
		t.Fatalf("KeyID: %q", gotAttr.KeyID)
	}
	findings, err := gotM.ScanString(context.Background(), "body "+secret)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].SecretRefName != "k-local" {
		t.Fatalf("findings: %+v", findings)
	}
}
