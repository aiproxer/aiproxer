package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/stdhttp/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

// credentialProvider builds the standard provider used by the probe matrix. The
// authenticator is nil on purpose: the probe must answer from the configured
// handler kind and the effective API-key header names alone, without running
// authentication.
func credentialProvider(kind sdkauth.HandlerKind, level sdkauth.RequiredLevel, headers aipsdk.HTTPHeaders) *auth.PolicyProvider {
	p := auth.NewPolicyProvider(nil, nil, auth.PolicySnapshot{
		HandlerKind: kind, RequiredLevel: level, AccessMode: sdkauth.AccessMultiUser,
	}, nil)
	p.HTTPHeaders = headers
	return p
}

func probeRequest(header map[string]string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	for name, value := range header {
		req.Header.Set(name, value)
	}
	return req
}

// mayAuthenticate runs the aggregate and reports whether the active chain might
// still authenticate the request as presented. A nil aggregate is fail-open to
// the normal auth chain and therefore always "may authenticate".
func mayAuthenticate(t *testing.T, providers []httpauth.Provider, req *http.Request) bool {
	t.Helper()
	probe := auth.NewCredentialPresenceProbe(providers)
	if probe == nil {
		return true
	}
	return probe(req)
}

func TestCredentialPresenceProbe_localAPIKeyAnswersPresenceOnly(t *testing.T) {
	t.Parallel()
	providers := []httpauth.Provider{credentialProvider(sdkauth.HandlerLocalAPIKey, sdkauth.LevelAPIKey, aipsdk.HTTPHeaders{})}
	for _, tc := range []struct {
		name    string
		header  map[string]string
		wantMay bool
	}{
		{name: "no header at all", header: nil, wantMay: false},
		{name: "empty authorization", header: map[string]string{"Authorization": ""}, wantMay: false},
		{name: "whitespace only authorization", header: map[string]string{"Authorization": "   "}, wantMay: false},
		{name: "bearer without token", header: map[string]string{"Authorization": "Bearer "}, wantMay: false},
		{name: "non bearer authorization is not an api key", header: map[string]string{"Authorization": "Basic dXNlcjpwdw=="}, wantMay: false},
		{name: "bearer authorization", header: map[string]string{"Authorization": "Bearer sk-live-secret"}, wantMay: true},
		{name: "vendor x-api-key", header: map[string]string{"x-api-key": "sk-live-secret"}, wantMay: true},
		{name: "google api key", header: map[string]string{"x-goog-api-key": "sk-live-secret"}, wantMay: true},
		{name: "azure api key", header: map[string]string{"api-key": "sk-live-secret"}, wantMay: true},
		{name: "unrelated header", header: map[string]string{"X-Trace-ID": "trace-1"}, wantMay: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := mayAuthenticate(t, providers, probeRequest(tc.header)); got != tc.wantMay {
				t.Fatalf("may authenticate = %v, want %v", got, tc.wantMay)
			}
		})
	}
}

func TestCredentialPresenceProbe_localAPIKeySSOStillNeedsTheLocalKey(t *testing.T) {
	t.Parallel()
	providers := []httpauth.Provider{credentialProvider(sdkauth.HandlerLocalAPIKey, sdkauth.LevelAPIKeySSO, aipsdk.HTTPHeaders{})}
	if mayAuthenticate(t, providers, probeRequest(nil)) {
		t.Fatal("api_key_sso cannot authenticate a request with no local API key")
	}
	if !mayAuthenticate(t, providers, probeRequest(map[string]string{"Authorization": "Bearer sk-live-secret"})) {
		t.Fatal("api_key_sso with a local API key must reach the remote leg")
	}
}

func TestCredentialPresenceProbe_effectiveHeaderNamesAreTheConfiguredOnes(t *testing.T) {
	t.Parallel()
	providers := []httpauth.Provider{
		credentialProvider(sdkauth.HandlerLocalAPIKey, sdkauth.LevelAPIKey, aipsdk.HTTPHeaders{APIKey: []string{"X-Corp-Key"}}),
	}
	if mayAuthenticate(t, providers, probeRequest(map[string]string{"Authorization": "Bearer sk-live-secret"})) {
		t.Fatal("a header outside the configured effective set carries no configured credential material")
	}
	if !mayAuthenticate(t, providers, probeRequest(map[string]string{"X-Corp-Key": "sk-live-secret"})) {
		t.Fatal("a configured effective header must reach authentication")
	}
}

func TestCredentialPresenceProbe_credentialFreeAndFutureKindsAlwaysMayAuthenticate(t *testing.T) {
	t.Parallel()
	for _, kind := range []sdkauth.HandlerKind{
		sdkauth.HandlerLocalNoop,
		sdkauth.HandlerRemote,
		sdkauth.HandlerKind("api_key_sso_remote"),
		sdkauth.HandlerKind("custom"),
		sdkauth.HandlerKind("future_kind"),
		sdkauth.HandlerKind(""),
	} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			providers := []httpauth.Provider{credentialProvider(kind, sdkauth.LevelNone, aipsdk.HTTPHeaders{})}
			if !mayAuthenticate(t, providers, probeRequest(nil)) {
				t.Fatalf("handler kind %q must fail open to auth without a credential", kind)
			}
			if !mayAuthenticate(t, providers, probeRequest(map[string]string{"Authorization": "Bearer sk-live-secret"})) {
				t.Fatalf("handler kind %q with a credential must reach auth", kind)
			}
		})
	}
}

func TestCredentialPresenceProbe_providerWithoutTheCapabilityFailsOpen(t *testing.T) {
	t.Parallel()
	if probe := auth.NewCredentialPresenceProbe([]httpauth.Provider{stubProvider{}}); probe != nil {
		t.Fatal("a provider that cannot answer must fail open to the normal auth chain")
	}
}

func TestCredentialPresenceProbe_aggregateRequiresEveryActiveProvider(t *testing.T) {
	t.Parallel()
	apiKey := credentialProvider(sdkauth.HandlerLocalAPIKey, sdkauth.LevelAPIKey, aipsdk.HTTPHeaders{})
	noop := credentialProvider(sdkauth.HandlerLocalNoop, sdkauth.LevelNone, aipsdk.HTTPHeaders{})
	remote := credentialProvider(sdkauth.HandlerRemote, sdkauth.LevelAPIKeySSO, aipsdk.HTTPHeaders{})

	for _, tc := range []struct {
		name      string
		providers []httpauth.Provider
		header    map[string]string
		wantMay   bool
	}{
		{name: "two api key providers without credential", providers: []httpauth.Provider{apiKey, apiKey}, wantMay: false},
		{
			name: "two api key providers with credential", providers: []httpauth.Provider{apiKey, apiKey},
			header: map[string]string{"Authorization": "Bearer sk-live-secret"}, wantMay: true,
		},
		{name: "api key plus credential free", providers: []httpauth.Provider{apiKey, noop}, wantMay: true},
		{name: "credential free plus api key", providers: []httpauth.Provider{noop, apiKey}, wantMay: true},
		{name: "api key plus remote sso", providers: []httpauth.Provider{apiKey, remote}, wantMay: true},
		{name: "api key plus unknown provider", providers: []httpauth.Provider{apiKey, stubProvider{}}, wantMay: true},
		{name: "nil entries are not active providers", providers: []httpauth.Provider{nil, apiKey, nil}, wantMay: false},
		{name: "only nil entries", providers: []httpauth.Provider{nil, nil}, wantMay: true},
		{name: "no providers configured", providers: nil, wantMay: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := mayAuthenticate(t, tc.providers, probeRequest(tc.header)); got != tc.wantMay {
				t.Fatalf("may authenticate = %v, want %v", got, tc.wantMay)
			}
		})
	}
}

// TestCredentialPresenceProbe_neverRetainsCredentialMaterial proves the answer is
// presence-only and stateless: two different secrets are indistinguishable, and
// a request without a credential after a request with one is still answered from
// that request alone.
func TestCredentialPresenceProbe_neverRetainsCredentialMaterial(t *testing.T) {
	t.Parallel()
	probe := auth.NewCredentialPresenceProbe([]httpauth.Provider{
		credentialProvider(sdkauth.HandlerLocalAPIKey, sdkauth.LevelAPIKey, aipsdk.HTTPHeaders{}),
	})
	if probe == nil {
		t.Fatal("a local API-key chain must expose a probe")
	}
	first := probeRequest(map[string]string{"Authorization": "Bearer sk-secret-one"})
	second := probeRequest(map[string]string{"Authorization": "Bearer sk-secret-two"})
	bare := probeRequest(nil)
	if !probe(first) || !probe(second) {
		t.Fatal("a request carrying credential material must reach auth")
	}
	if probe(bare) {
		t.Fatal("no earlier request's credential may be carried into a later answer")
	}
	// The probe answers from the request alone; it never rewrites request state.
	if got := first.Header.Get("Authorization"); got != "Bearer sk-secret-one" {
		t.Fatalf("probe mutated the request header: %q", got)
	}
}
