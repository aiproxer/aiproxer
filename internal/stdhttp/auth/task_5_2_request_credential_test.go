package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

func TestTask52AcceptedCredentialMatcher_resolvesFromIngressForBearerAndAPIKey(t *testing.T) {
	t.Parallel()

	const credential = "opaque-request-credential-task-5-2-2026"
	tests := []struct {
		name       string
		headerName string
		headerVal  string
		headers    aipsdk.HTTPHeaders
	}{
		{
			name:       "authorization_bearer",
			headerName: aipsdk.HeaderAuthorization,
			headerVal:  "Bearer " + credential,
		},
		{
			name:       "x_api_key",
			headerName: aipsdk.HeaderAPIKey,
			headerVal:  credential,
			headers:    aipsdk.HTTPHeaders{APIKey: []string{aipsdk.HeaderAPIKey}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			authenticator, err := coreauth.NewLocalAPIKeyAuthenticator([]coreauth.LocalAPIKeyRecord{
				{KeyID: "accepted-" + tc.name, PrincipalID: "request-user", Key: credential},
			})
			if err != nil {
				t.Fatal(err)
			}
			provider := NewPolicyProvider(&coreauth.PolicyAuthenticator{
				Handler:  sdkauth.HandlerLocalAPIKey,
				Required: sdkauth.LevelAPIKey,
				APIKey:   authenticator,
			}, nil, PolicySnapshot{
				AccessMode: sdkauth.AccessMultiUser, HandlerKind: sdkauth.HandlerLocalAPIKey, RequiredLevel: sdkauth.LevelAPIKey,
			}, nil)
			provider.HTTPHeaders = tc.headers

			var gotCtx context.Context
			h := Middleware(nil, []httpauth.Provider{provider}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				gotCtx = r.Context()
			}))
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			req.Header.Set(tc.headerName, tc.headerVal)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("authentication status: got %d", rec.Code)
			}
			if gotCtx == nil {
				t.Fatal("downstream handler did not receive a request context")
			}

			// Resolve through the same multi-user source used by feature composition. This
			// proves the accepted ingress value reaches the request-scoped resolver without
			// exposing a raw secret accessor.
			src, err := engine.NewMultiUserSource(nil)
			if err != nil {
				t.Fatal(err)
			}
			matcher, err := src.MatcherResolver().Resolve(gotCtx)
			if err != nil {
				t.Fatal(err)
			}
			if matcher == nil {
				t.Fatal("accepted ingress credential must resolve to a request matcher")
			}
			findings, err := matcher.ScanString(gotCtx, "credential="+credential+" "+strings.ToUpper(credential))
			if err != nil {
				t.Fatal(err)
			}
			if len(findings) != 1 || findings[0].SourceCategory != secretguard.SourceCategoryRequestCred || findings[0].OccurrenceCount != 1 {
				t.Fatalf("opaque accepted credential findings: %+v", findings)
			}
		})
	}
}

func TestTask52AcceptedCredentialMatcher_concurrentRequestsStayIsolated(t *testing.T) {
	t.Parallel()

	const (
		credentialA = "opaque-request-credential-A-task-5-2-2026"
		credentialB = "opaque-request-credential-B-task-5-2-2026"
	)
	authenticator, err := coreauth.NewLocalAPIKeyAuthenticator([]coreauth.LocalAPIKeyRecord{
		{KeyID: "accepted-a", PrincipalID: "request-user-a", Key: credentialA},
		{KeyID: "accepted-b", PrincipalID: "request-user-b", Key: credentialB},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := NewPolicyProvider(&coreauth.PolicyAuthenticator{
		Handler:  sdkauth.HandlerLocalAPIKey,
		Required: sdkauth.LevelAPIKey,
		APIKey:   authenticator,
	}, nil, PolicySnapshot{
		AccessMode: sdkauth.AccessMultiUser, HandlerKind: sdkauth.HandlerLocalAPIKey, RequiredLevel: sdkauth.LevelAPIKey,
	}, nil)
	h := Middleware(nil, []httpauth.Provider{provider}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		matcher, err := (secretguard.ContextMatcherResolver{}).Resolve(r.Context())
		if err != nil {
			t.Errorf("resolve matcher: %v", err)
			return
		}
		if matcher == nil {
			t.Error("expected request matcher")
			return
		}
		credential := r.Header.Get(aipsdk.HeaderAPIKey)
		findings, err := matcher.ScanString(r.Context(), credential)
		if err != nil {
			t.Errorf("scan own credential: %v", err)
			return
		}
		if len(findings) != 1 || findings[0].OccurrenceCount != 1 {
			t.Errorf("own credential findings: %+v", findings)
		}
		other := credentialA
		if credential == credentialA {
			other = credentialB
		}
		findings, err = matcher.ScanString(r.Context(), other)
		if err != nil {
			t.Errorf("scan other credential: %v", err)
			return
		}
		if len(findings) != 0 {
			t.Errorf("request matcher crossed credential boundary: %+v", findings)
		}
	}))

	var wg sync.WaitGroup
	for _, credential := range []string{credentialA, credentialB} {
		wg.Go(func() {
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			req.Header.Set(aipsdk.HeaderAPIKey, credential)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("authentication status for %q: got %d", credential, rec.Code)
			}
		})
	}
	wg.Wait()
}

type task52RemoteDecider struct {
	decision sdkauth.Decision
}

func (d task52RemoteDecider) Decide(context.Context, sdkauth.InboundCallMeta) (sdkauth.Decision, error) {
	return d.decision, nil
}

func TestTask52CredentialFreeRemoteSuccess_doesNotBindUnacceptedHeader(t *testing.T) {
	t.Parallel()

	const arbitraryHeaderCredential = "opaque-remote-unaccepted-header-task-5-2-2026"
	provider := NewPolicyProvider(&coreauth.PolicyAuthenticator{
		Handler:  sdkauth.HandlerRemote,
		Required: sdkauth.LevelAPIKey,
		Remote: task52RemoteDecider{decision: sdkauth.Decision{
			Outcome:   sdkauth.OutcomeAllow,
			Principal: execview.PrincipalView{ID: "remote-user"},
		}},
	}, nil, PolicySnapshot{
		AccessMode: sdkauth.AccessMultiUser, HandlerKind: sdkauth.HandlerRemote, RequiredLevel: sdkauth.LevelAPIKey,
	}, nil)
	var gotCtx context.Context
	h := Middleware(nil, []httpauth.Provider{provider}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotCtx = r.Context()
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set(aipsdk.HeaderAuthorization, "Bearer "+arbitraryHeaderCredential)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("credential-free remote success status: got %d", rec.Code)
	}
	m, err := (secretguard.ContextMatcherResolver{}).Resolve(gotCtx)
	if err != nil {
		t.Fatal(err)
	}
	if m != nil {
		t.Fatal("remote success without satisfied credential must not bind an unrelated Authorization header")
	}
}

func TestTask52AcceptedLocalAPIKeyMatcher_ignoresWeakerSingleUserPolicy(t *testing.T) {
	t.Parallel()

	const credential = "opaque-local-api-key-weaker-policy-task-5-2-2026"
	for _, tc := range []struct {
		name       string
		headerName string
		headerVal  string
		headers    aipsdk.HTTPHeaders
	}{
		{name: "authorization_bearer", headerName: aipsdk.HeaderAuthorization, headerVal: "Bearer " + credential},
		{name: "x_api_key", headerName: aipsdk.HeaderAPIKey, headerVal: credential, headers: aipsdk.HTTPHeaders{APIKey: []string{aipsdk.HeaderAPIKey}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			authenticator, err := coreauth.NewLocalAPIKeyAuthenticator([]coreauth.LocalAPIKeyRecord{
				{KeyID: "weaker-policy-" + tc.name, PrincipalID: "single-user", Key: credential},
			})
			if err != nil {
				t.Fatal(err)
			}
			provider := NewPolicyProvider(&coreauth.PolicyAuthenticator{
				Handler:  sdkauth.HandlerLocalAPIKey,
				Required: sdkauth.LevelNone,
				APIKey:   authenticator,
			}, nil, PolicySnapshot{
				AccessMode: sdkauth.AccessSingleUser, HandlerKind: sdkauth.HandlerLocalAPIKey, RequiredLevel: sdkauth.LevelNone,
			}, nil)
			provider.HTTPHeaders = tc.headers

			var gotCtx context.Context
			h := Middleware(nil, []httpauth.Provider{provider}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				gotCtx = r.Context()
			}))
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			req.Header.Set(tc.headerName, tc.headerVal)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("accepted local API key status: got %d", rec.Code)
			}
			matcher, err := (secretguard.ContextMatcherResolver{}).Resolve(gotCtx)
			if err != nil {
				t.Fatal(err)
			}
			if matcher == nil {
				t.Fatal("accepted local API key must bind even when the single-user policy level is none")
			}
			findings, err := matcher.ScanString(gotCtx, credential)
			if err != nil {
				t.Fatal(err)
			}
			if len(findings) != 1 || findings[0].OccurrenceCount != 1 {
				t.Fatalf("accepted local API key findings: %+v", findings)
			}
		})
	}
}

func TestTask52CredentialFreeSuccess_doesNotBindUnacceptedHeader(t *testing.T) {
	t.Parallel()

	const arbitraryHeaderCredential = "opaque-unaccepted-header-task-5-2-2026"
	provider := NewPolicyProvider(&coreauth.PolicyAuthenticator{
		Handler:  sdkauth.HandlerLocalNoop,
		Required: sdkauth.LevelNone,
		Noop:     coreauth.LocalNoOpAuthenticator{},
	}, nil, PolicySnapshot{
		AccessMode: sdkauth.AccessSingleUser, HandlerKind: sdkauth.HandlerLocalNoop, RequiredLevel: sdkauth.LevelNone,
	}, nil)
	var gotCtx context.Context
	h := Middleware(nil, []httpauth.Provider{provider}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotCtx = r.Context()
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set(aipsdk.HeaderAuthorization, "Bearer "+arbitraryHeaderCredential)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("credential-free success status: got %d", rec.Code)
	}
	matcher, err := (secretguard.ContextMatcherResolver{}).Resolve(gotCtx)
	if err != nil {
		t.Fatal(err)
	}
	if matcher != nil {
		t.Fatal("credential-free local_noop must not bind an unrelated Authorization header")
	}
}

func TestTask52MatcherContract_hasNoRawCredentialGetter(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeFor[secretguard.Matcher]()
	forbidden := map[string]struct{}{
		"Secret": {}, "Secrets": {}, "Value": {}, "Values": {}, "Raw": {}, "Credential": {},
	}
	for method := range typ.Methods() {
		if _, ok := forbidden[method.Name]; ok {
			t.Fatalf("request matcher contract exposes raw credential accessor %q", method.Name)
		}
	}
}
