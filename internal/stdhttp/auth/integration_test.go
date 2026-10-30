//go:build integration

package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

func TestIntegration_localNoop_allow(t *testing.T) {
	t.Parallel()
	pa := coreauth.PolicyAuthenticator{
		Handler:  auth.HandlerLocalNoop,
		Required: auth.LevelNone,
		Noop:     coreauth.LocalNoOpAuthenticator{OS: testOSID{snap: coreauth.OSIdentitySnapshot{PrincipalID: "u-local"}}},
	}
	sink := &orderSink{}
	disp := coreauth.NewEventDispatcher(sink, coreauth.EventFailureBestEffort)
	pol := PolicySnapshot{AccessMode: auth.AccessSingleUser, HandlerKind: auth.HandlerLocalNoop, RequiredLevel: auth.LevelNone}
	prov := NewPolicyProvider(&pa, disp, pol, nil)
	var steps []string
	rec := httptest.NewRecorder()
	Middleware(nil, []httpauth.Provider{prov}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if sink.events < 1 {
			t.Error("auth event not recorded before handler")
		}
		p, ok := httpauth.PrincipalFromContext(r.Context())
		if !ok || p.ID != "u-local" {
			t.Fatalf("principal %+v", p)
		}
		steps = append(steps, "inner")
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if len(steps) != 1 {
		t.Fatalf("steps: %v", steps)
	}
}

func TestIntegration_localAPIKey_denyNoHeader(t *testing.T) {
	t.Parallel()
	ak, err := coreauth.NewLocalAPIKeyAuthenticator([]coreauth.LocalAPIKeyRecord{
		{KeyID: "kid", PrincipalID: "p1", Key: "test-local-api-key-16"},
	})
	if err != nil {
		t.Fatal(err)
	}
	pa := coreauth.PolicyAuthenticator{
		Handler:  auth.HandlerLocalAPIKey,
		Required: auth.LevelAPIKey,
		APIKey:   ak,
	}
	sink := &orderSink{}
	disp := coreauth.NewEventDispatcher(sink, coreauth.EventFailureBestEffort)
	prov := NewPolicyProvider(&pa, disp, PolicySnapshot{
		AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey,
	}, nil)
	var sawInner int
	h := Middleware(nil, []httpauth.Provider{prov}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { sawInner++ }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/", nil))
	if sawInner != 0 {
		t.Fatal("inner should not run")
	}
}

func TestIntegration_localAPIKey_allow(t *testing.T) {
	t.Parallel()
	ak, err := coreauth.NewLocalAPIKeyAuthenticator([]coreauth.LocalAPIKeyRecord{
		{KeyID: "kid", PrincipalID: "p1", Key: "test-local-api-key-16"},
	})
	if err != nil {
		t.Fatal(err)
	}
	pa := coreauth.PolicyAuthenticator{Handler: auth.HandlerLocalAPIKey, Required: auth.LevelAPIKey, APIKey: ak}
	disp := coreauth.NewEventDispatcher(&orderSink{}, coreauth.EventFailureBestEffort)
	prov := NewPolicyProvider(&pa, disp, PolicySnapshot{AccessMode: auth.AccessMultiUser, HandlerKind: auth.HandlerLocalAPIKey, RequiredLevel: auth.LevelAPIKey}, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", nil)
	req.Header.Set("Authorization", "Bearer test-local-api-key-16")
	var pid string
	Middleware(nil, []httpauth.Provider{prov}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		p, _ := httpauth.PrincipalFromContext(r.Context())
		pid = p.ID
	})).ServeHTTP(httptest.NewRecorder(), req)
	if pid != "p1" {
		t.Fatalf("principal %q", pid)
	}
}

func TestIntegration_remoteAllow(t *testing.T) {
	t.Parallel()
	pa := coreauth.PolicyAuthenticator{
		Handler:  auth.HandlerRemote,
		Required: auth.LevelNone,
		Remote:   remoteStub{decision: auth.Decision{Outcome: auth.OutcomeAllow, Principal: execview.PrincipalView{ID: "r1"}}},
	}
	disp := coreauth.NewEventDispatcher(&orderSink{}, coreauth.EventFailureBestEffort)
	prov := NewPolicyProvider(&pa, disp, PolicySnapshot{AccessMode: auth.AccessSingleUser, HandlerKind: auth.HandlerRemote, RequiredLevel: auth.LevelNone}, nil)
	var inner bool
	Middleware(nil, []httpauth.Provider{prov}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { inner = true })).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	if !inner {
		t.Fatal("inner expected")
	}
}

func TestIntegration_remoteDeny_skipsInner(t *testing.T) {
	t.Parallel()
	pa := coreauth.PolicyAuthenticator{
		Handler: auth.HandlerRemote,
		Remote:  remoteStub{decision: auth.Decision{Outcome: auth.OutcomeDeny, ReasonCode: "remote_denied"}},
	}
	disp := coreauth.NewEventDispatcher(&orderSink{}, coreauth.EventFailureBestEffort)
	prov := NewPolicyProvider(&pa, disp, PolicySnapshot{AccessMode: auth.AccessSingleUser, HandlerKind: auth.HandlerRemote, RequiredLevel: auth.LevelNone}, nil)
	saw := false
	Middleware(nil, []httpauth.Provider{prov}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { saw = true })).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	if saw {
		t.Fatal("inner ran")
	}
}

func TestIntegration_remoteChallenge_terminates(t *testing.T) {
	t.Parallel()
	pa := coreauth.PolicyAuthenticator{
		Handler: auth.HandlerRemote,
		Remote:  remoteStub{decision: auth.Decision{Outcome: auth.OutcomeChallenge, Challenge: auth.Challenge{Kind: auth.ChallengeSSORequired, Summary: "s"}}},
	}
	disp := coreauth.NewEventDispatcher(&orderSink{}, coreauth.EventFailureBestEffort)
	prov := NewPolicyProvider(&pa, disp, PolicySnapshot{AccessMode: auth.AccessSingleUser, HandlerKind: auth.HandlerRemote, RequiredLevel: auth.LevelNone}, nil)
	saw := false
	rec := httptest.NewRecorder()
	Middleware(nil, []httpauth.Provider{prov}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { saw = true })).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if saw {
		t.Fatal("inner ran")
	}
	if rec.Code == http.StatusOK {
		t.Fatalf("expected challenge status, got 200")
	}
	if rec.Code < http.StatusBadRequest {
		t.Fatalf("code %d", rec.Code)
	}
}

func TestIntegration_remoteDecideError_usesRendererNotHTTP500(t *testing.T) {
	t.Parallel()
	pa := coreauth.PolicyAuthenticator{
		Handler: auth.HandlerRemote,
		Remote:  remoteStub{err: errors.New("upstream unreachable")},
	}
	disp := coreauth.NewEventDispatcher(&orderSink{}, coreauth.EventFailureBestEffort)
	prov := NewPolicyProvider(&pa, disp, PolicySnapshot{
		AccessMode: auth.AccessSingleUser, HandlerKind: auth.HandlerRemote, RequiredLevel: auth.LevelNone,
	}, nil)
	rec := httptest.NewRecorder()
	Middleware(nil, []httpauth.Provider{prov}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("inner must not run when remote Decide fails")
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 from rendered remote_unavailable, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "upstream unreachable") {
		t.Fatalf("response must not leak transport error message")
	}
	if !strings.Contains(body, "service_unavailable") && !strings.Contains(body, "unavailable") {
		t.Fatalf("expected safe JSON body, got %q", body)
	}
}
