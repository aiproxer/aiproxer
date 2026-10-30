package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

// allowScope is a trusted, safe-by-construction scope snapshot shared by scope-bridge tests.
func allowScope() scope.PrincipalScopeView {
	return scope.PrincipalScopeView{
		SubjectKind:  scope.SubjectHuman,
		Origin:       scope.OriginClient,
		PrincipalID:  scope.Known("user-7"),
		DisplayName:  scope.Known("Alice"),
		AuthMethod:   scope.Known("oidc"),
		CredentialID: scope.Known("key-7"),
		Roles:        []string{"ops"},
		SafeClaims:   map[string]string{"team": "core"},
		TenantID:     scope.Known("t-7"),
	}
}

// TestPolicyProvider_allow_attachesScopeAndDerivedPrincipalToContext proves accepted requests
// carry matching authoritative scope and derived principal projection before proxy execution
// (requirements 1.1, 1.5, 2.1, 4.1, 7.3).
func TestPolicyProvider_allow_attachesScopeAndDerivedPrincipalToContext(t *testing.T) {
	t.Parallel()
	trusted := allowScope()
	stub := &stubCoreAuthenticator{dec: auth.Decision{Outcome: auth.OutcomeAllow, Scope: &trusted}}
	p := NewPolicyProvider(stub, nil, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerRemote, RequiredLevel: auth.LevelAPIKey,
	}, nil)
	var innerCtx context.Context
	rec := httptest.NewRecorder()
	Middleware(nil, []httpauth.Provider{p}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		innerCtx = r.Context()
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/chat", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	gotScope, ok := httpauth.ScopeFromContext(innerCtx)
	if !ok {
		t.Fatal("expected authoritative scope in request context")
	}
	if gotScope.PrincipalID.String() != "user-7" {
		t.Fatalf("scope PrincipalID %q", gotScope.PrincipalID)
	}
	gotPrincipal, ok := httpauth.PrincipalFromContext(innerCtx)
	if !ok || gotPrincipal.ID != "user-7" {
		t.Fatalf("principal %+v ok=%v", gotPrincipal, ok)
	}
	if proj := gotScope.Principal(); proj.ID != gotPrincipal.ID {
		t.Fatalf("principal %q must equal scope projection %q", gotPrincipal.ID, proj.ID)
	}
}

// TestPolicyProvider_allow_legacyPrincipal_attachesDerivedScope proves a principal-only allow
// still receives an authoritative derived scope at the bridge (precedence rung 2).
func TestPolicyProvider_allow_legacyPrincipal_attachesDerivedScope(t *testing.T) {
	t.Parallel()
	stub := &stubCoreAuthenticator{dec: auth.Decision{
		Outcome:        auth.OutcomeAllow,
		Principal:      execview.PrincipalView{ID: "legacy-1", DisplayName: "Legacy"},
		SatisfiedLevel: auth.LevelAPIKey,
	}}
	p := NewPolicyProvider(stub, nil, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey,
	}, nil)
	var innerCtx context.Context
	Middleware(nil, []httpauth.Provider{p}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		innerCtx = r.Context()
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/", nil))
	gotScope, ok := httpauth.ScopeFromContext(innerCtx)
	if !ok {
		t.Fatal("expected derived scope for legacy principal allow")
	}
	if gotScope.PrincipalID.String() != "legacy-1" {
		t.Fatalf("scope PrincipalID %q", gotScope.PrincipalID)
	}
	if gotScope.AuthMethod.String() != "api_key" {
		t.Fatalf("AuthMethod %q", gotScope.AuthMethod)
	}
	gotPrincipal, _ := httpauth.PrincipalFromContext(innerCtx)
	if gotPrincipal.ID != gotScope.Principal().ID {
		t.Fatalf("principal %q must match scope projection %q", gotPrincipal.ID, gotScope.Principal().ID)
	}
}

// TestPolicyProvider_deny_resultHasNoLifecycleScope proves denied decisions preserve the
// rejection shape and do not attach a successful lifecycle scope (requirement 1.6).
func TestPolicyProvider_deny_resultHasNoLifecycleScope(t *testing.T) {
	t.Parallel()
	stub := &stubCoreAuthenticator{dec: auth.Decision{Outcome: auth.OutcomeDeny, ReasonCode: "missing_api_key"}}
	p := NewPolicyProvider(stub, nil, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey,
	}, nil)
	res, err := p.Authenticate(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/", nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.Type != httpauth.TypeReject {
		t.Fatalf("type %q want reject", res.Type)
	}
	if res.Scope != nil {
		t.Fatalf("denied result must not carry lifecycle scope, got %+v", res.Scope)
	}
}

// TestPolicyProvider_challenge_resultHasNoLifecycleScope_butEvidenceHasAttribution proves
// challenged decisions preserve the challenge shape without lifecycle scope while still
// emitting safe attribution evidence when identity is available (requirements 1.6, 6.1).
func TestPolicyProvider_challenge_resultHasNoLifecycleScope_butEvidenceHasAttribution(t *testing.T) {
	t.Parallel()
	trusted := allowScope()
	stub := &stubCoreAuthenticator{dec: auth.Decision{
		Outcome:    auth.OutcomeChallenge,
		ReasonCode: "sso_required",
		Scope:      &trusted,
		Challenge:  auth.Challenge{Kind: auth.ChallengeSSORequired, Summary: "SSO required"},
	}}
	sink := &captureSink{}
	disp := coreauth.NewEventDispatcher(sink, coreauth.EventFailureBestEffort)
	p := NewPolicyProvider(stub, disp, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerRemote, RequiredLevel: auth.LevelAPIKey,
	}, nil)
	res, err := p.Authenticate(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/", nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.Type != httpauth.TypeChallenge {
		t.Fatalf("type %q want challenge", res.Type)
	}
	if res.Scope != nil {
		t.Fatal("challenged result must not carry lifecycle scope")
	}
	if len(sink.events) != 1 {
		t.Fatalf("events %d", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Outcome != auth.OutcomeChallenge {
		t.Fatalf("event outcome %q", ev.Outcome)
	}
	if ev.Scope == nil {
		t.Fatal("challenge evidence must carry safe scope attribution when available")
	}
	if ev.Scope.PrincipalID.String() != "user-7" {
		t.Fatalf("evidence scope PrincipalID %q", ev.Scope.PrincipalID)
	}
}

// TestPolicyProvider_allow_evidenceCarriesSafeScopeAndCompatFields proves success evidence
// includes trace correlation, outcome, reason, safe scope attribution, and existing
// compatibility fields (requirements 6.1, 6.5, 7.1).
func TestPolicyProvider_allow_evidenceCarriesSafeScopeAndCompatFields(t *testing.T) {
	t.Parallel()
	trusted := allowScope()
	stub := &stubCoreAuthenticator{dec: auth.Decision{Outcome: auth.OutcomeAllow, Scope: &trusted, ReasonCode: ""}}
	sink := &captureSink{}
	disp := coreauth.NewEventDispatcher(sink, coreauth.EventFailureBestEffort)
	p := NewPolicyProvider(stub, disp, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerRemote, RequiredLevel: auth.LevelAPIKey,
	}, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/", nil)
	req = req.WithContext(diag.WithTraceID(req.Context(), "trace-evidence-1"))
	if _, err := p.Authenticate(context.Background(), httptest.NewRecorder(), req); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("events %d", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Outcome != auth.OutcomeAllow {
		t.Fatalf("outcome %q", ev.Outcome)
	}
	if ev.TraceID != "trace-evidence-1" {
		t.Fatalf("evidence must carry trace correlation, got %q", ev.TraceID)
	}
	if ev.Scope == nil {
		t.Fatal("allow evidence must carry safe scope attribution")
	}
	if ev.Scope.PrincipalID.String() != "user-7" {
		t.Fatalf("evidence scope PrincipalID %q", ev.Scope.PrincipalID)
	}
	if ev.PrincipalID != "user-7" {
		t.Fatalf("compat PrincipalID %q want user-7", ev.PrincipalID)
	}
	if len(ev.PrincipalRoles) != 1 || ev.PrincipalRoles[0] != "ops" {
		t.Fatalf("compat roles %v", ev.PrincipalRoles)
	}
	if ev.PrincipalSafeClaims == nil || ev.PrincipalSafeClaims["team"] != "" {
		t.Fatalf("PrincipalSafeClaims must list keys with empty values, got %#v", ev.PrincipalSafeClaims)
	}
}

// TestPolicyProvider_evidence_excludesRawSecrets proves raw bearer material from the transport
// never reaches auth decision evidence, and safe scope attribution is emitted instead
// (requirements 2.6, 5.2).
func TestPolicyProvider_evidence_excludesRawSecrets(t *testing.T) {
	t.Parallel()
	trusted := allowScope()
	stub := &stubCoreAuthenticator{dec: auth.Decision{Outcome: auth.OutcomeAllow, Scope: &trusted}}
	sink := &captureSink{}
	disp := coreauth.NewEventDispatcher(sink, coreauth.EventFailureBestEffort)
	p := NewPolicyProvider(stub, disp, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerRemote, RequiredLevel: auth.LevelAPIKey,
	}, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/", nil)
	req.Header.Set("Authorization", "Bearer do-not-leak-this-secret-1234")
	if _, err := p.Authenticate(context.Background(), httptest.NewRecorder(), req); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("events %d", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Scope == nil {
		t.Fatal("expected safe scope evidence")
	}
	if scopeContains(ev.Scope, "do-not-leak-this-secret-1234") {
		t.Fatalf("raw secret leaked into evidence scope: %+v", ev.Scope)
	}
	if strings.Contains(ev.PrincipalID, "do-not-leak") || strings.Contains(strings.Join(ev.PrincipalRoles, ","), "do-not-leak") {
		t.Fatalf("raw secret leaked into compatibility fields: %+v", ev)
	}
}

// TestPolicyProvider_unsafeScope_deniesAndOmitsFromEvidence proves credential-like scope
// material is rejected before execution and omitted from evidence (requirements 2.6, 5.4, 8.5).
func TestPolicyProvider_unsafeScope_deniesAndOmitsFromEvidence(t *testing.T) {
	t.Parallel()
	unsafe := allowScope()
	unsafe.PrincipalID = scope.Known("bearer some-token-1234567890")
	stub := &stubCoreAuthenticator{dec: auth.Decision{Outcome: auth.OutcomeAllow, Scope: &unsafe}}
	sink := &captureSink{}
	disp := coreauth.NewEventDispatcher(sink, coreauth.EventFailureBestEffort)
	p := NewPolicyProvider(stub, disp, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerRemote, RequiredLevel: auth.LevelAPIKey,
	}, nil)
	res, err := p.Authenticate(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/", nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.Type != httpauth.TypeReject {
		t.Fatalf("unsafe scope must deny, got type %q", res.Type)
	}
	if res.Scope != nil {
		t.Fatal("unsafe denied result must not carry scope")
	}
	if len(sink.events) != 1 {
		t.Fatalf("events %d", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Outcome != auth.OutcomeDeny {
		t.Fatalf("evidence outcome %q want deny", ev.Outcome)
	}
	if ev.Scope != nil {
		t.Fatalf("unsafe scope must not appear in evidence, got %+v", ev.Scope)
	}
	if strings.Contains(ev.PrincipalID, "bearer") {
		t.Fatalf("unsafe principal id leaked into evidence: %q", ev.PrincipalID)
	}
}

// TestPolicyProvider_unsafeScope_overwritesStaleReasonCode proves a forced unsafe-scope deny
// always reports the unsafe_scope reason even when the original allow carried an unrelated
// informational reason code (requirement 2.6, 5.4).
func TestPolicyProvider_unsafeScope_overwritesStaleReasonCode(t *testing.T) {
	t.Parallel()
	unsafe := allowScope()
	unsafe.PrincipalID = scope.Known("bearer stale-reason-secret-1234567890")
	stub := &stubCoreAuthenticator{dec: auth.Decision{Outcome: auth.OutcomeAllow, Scope: &unsafe, ReasonCode: "degraded_allow"}}
	sink := &captureSink{}
	disp := coreauth.NewEventDispatcher(sink, coreauth.EventFailureBestEffort)
	p := NewPolicyProvider(stub, disp, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerRemote, RequiredLevel: auth.LevelAPIKey,
	}, nil)
	res, err := p.Authenticate(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/", nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.Type != httpauth.TypeReject {
		t.Fatalf("unsafe scope must deny, got type %q", res.Type)
	}
	if len(sink.events) != 1 {
		t.Fatalf("events %d", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Outcome != auth.OutcomeDeny {
		t.Fatalf("evidence outcome %q want deny", ev.Outcome)
	}
	if ev.ReasonCode != "unsafe_scope" {
		t.Fatalf("stale allow reason survived unsafe-scope deny: got %q want unsafe_scope", ev.ReasonCode)
	}
	if ev.Scope != nil {
		t.Fatalf("unsafe scope must not appear in evidence, got %+v", ev.Scope)
	}
}

// TestPolicyProvider_deny_unsafeScope_omittedFromEvidence proves a denied decision that carries
// credential-like scope material still renders the rejection shape but omits the unsafe scope
// from evidence entirely (requirements 1.6, 2.6, 5.4, 6.1).
func TestPolicyProvider_deny_unsafeScope_omittedFromEvidence(t *testing.T) {
	t.Parallel()
	unsafe := allowScope()
	unsafe.PrincipalID = scope.Known("bearer deny-secret-1234567890")
	stub := &stubCoreAuthenticator{dec: auth.Decision{
		Outcome:    auth.OutcomeDeny,
		ReasonCode: "missing_api_key",
		Scope:      &unsafe,
	}}
	sink := &captureSink{}
	disp := coreauth.NewEventDispatcher(sink, coreauth.EventFailureBestEffort)
	p := NewPolicyProvider(stub, disp, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey,
	}, nil)
	res, err := p.Authenticate(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/", nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.Type != httpauth.TypeReject {
		t.Fatalf("denied unsafe scope must keep reject shape, got type %q", res.Type)
	}
	if res.Scope != nil {
		t.Fatal("denied result must not carry lifecycle scope")
	}
	if len(sink.events) != 1 {
		t.Fatalf("events %d", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Outcome != auth.OutcomeDeny {
		t.Fatalf("evidence outcome %q want deny", ev.Outcome)
	}
	if ev.Scope != nil {
		t.Fatalf("unsafe denied scope must be omitted from evidence, got %+v", ev.Scope)
	}
	if scopeContains(ev.Scope, "deny-secret-1234567890") {
		t.Fatalf("unsafe material leaked into evidence scope")
	}
	if strings.Contains(ev.PrincipalID, "bearer") || strings.Contains(ev.PrincipalID, "deny-secret") {
		t.Fatalf("unsafe principal id leaked into evidence: %q", ev.PrincipalID)
	}
}

// TestPolicyProvider_challenge_unsafeScope_omittedFromEvidence proves a challenged decision that
// carries credential-like scope material still renders the challenge shape but omits the unsafe
// scope from evidence entirely (requirements 1.6, 2.6, 5.4, 6.1).
func TestPolicyProvider_challenge_unsafeScope_omittedFromEvidence(t *testing.T) {
	t.Parallel()
	unsafe := allowScope()
	unsafe.PrincipalID = scope.Known("bearer challenge-secret-1234567890")
	stub := &stubCoreAuthenticator{dec: auth.Decision{
		Outcome:    auth.OutcomeChallenge,
		ReasonCode: "sso_required",
		Scope:      &unsafe,
		Challenge:  auth.Challenge{Kind: auth.ChallengeSSORequired, Summary: "SSO required"},
	}}
	sink := &captureSink{}
	disp := coreauth.NewEventDispatcher(sink, coreauth.EventFailureBestEffort)
	p := NewPolicyProvider(stub, disp, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerRemote, RequiredLevel: auth.LevelAPIKey,
	}, nil)
	res, err := p.Authenticate(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/", nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.Type != httpauth.TypeChallenge {
		t.Fatalf("challenged unsafe scope must keep challenge shape, got type %q", res.Type)
	}
	if res.Scope != nil {
		t.Fatal("challenged result must not carry lifecycle scope")
	}
	if len(sink.events) != 1 {
		t.Fatalf("events %d", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Outcome != auth.OutcomeChallenge {
		t.Fatalf("evidence outcome %q want challenge", ev.Outcome)
	}
	if ev.Scope != nil {
		t.Fatalf("unsafe challenged scope must be omitted from evidence, got %+v", ev.Scope)
	}
	if strings.Contains(ev.PrincipalID, "bearer") || strings.Contains(ev.PrincipalID, "challenge-secret") {
		t.Fatalf("unsafe principal id leaked into evidence: %q", ev.PrincipalID)
	}
}

func scopeContains(v *scope.PrincipalScopeView, needle string) bool {
	if v == nil {
		return false
	}
	fields := []string{
		v.PrincipalID.String(), v.DisplayName.String(), v.AuthMethod.String(),
		v.CredentialID.String(), v.TenantID.String(), v.OrganizationID.String(),
		v.WorkspaceID.String(), v.ProjectID.String(), v.DepartmentID.String(),
		v.CostCenterID.String(), v.ParentTraceID.String(),
	}
	for _, f := range fields {
		if strings.Contains(f, needle) {
			return true
		}
	}
	for _, r := range v.Roles {
		if strings.Contains(r, needle) {
			return true
		}
	}
	for _, m := range []map[string]string{v.SafeClaims, v.PolicyLabels} {
		for k, val := range m {
			if strings.Contains(k, needle) || strings.Contains(val, needle) {
				return true
			}
		}
	}
	return false
}
