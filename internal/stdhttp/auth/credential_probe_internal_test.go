package auth

import (
	"context"
	"net/http"
	"testing"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

// presenceStub is a provider that implements the private optional capability
// directly, so the aggregate is proven to be capability-driven rather than
// specific to [PolicyProvider].
type presenceStub struct {
	disposition credentialDisposition
}

func (s presenceStub) Authenticate(context.Context, http.ResponseWriter, *http.Request) (httpauth.AuthenticationResult, error) {
	return httpauth.AuthenticationResult{Type: httpauth.TypeContinue}, nil
}

func (s presenceStub) credentialPresence(*http.Request) credentialDisposition { return s.disposition }

func TestCredentialPresenceProbe_aggregateFollowsThePrivateCapability(t *testing.T) {
	t.Parallel()
	noCredential := presenceStub{disposition: definitelyNoCredential}
	mayAuth := presenceStub{disposition: mayAuthenticate}
	for _, tc := range []struct {
		name      string
		providers []httpauth.Provider
		wantMay   bool
	}{
		{name: "every provider proves no credential", providers: []httpauth.Provider{noCredential, noCredential}, wantMay: false},
		{name: "one provider may authenticate", providers: []httpauth.Provider{noCredential, mayAuth}, wantMay: true},
		{name: "future disposition value fails open", providers: []httpauth.Provider{
			noCredential, presenceStub{disposition: credentialDisposition(42)},
		}, wantMay: true},
		{name: "typed nil standard provider fails open", providers: []httpauth.Provider{(*PolicyProvider)(nil)}, wantMay: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			probe := NewCredentialPresenceProbe(tc.providers)
			if probe == nil {
				t.Fatal("probe must not fail open structurally; every stub implements the capability")
			}
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/models", nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := probe(req); got != tc.wantMay {
				t.Fatalf("may authenticate = %v, want %v", got, tc.wantMay)
			}
		})
	}
}

func TestCredentialPresenceProbe_standardProviderFailsOpenOnNilRequest(t *testing.T) {
	t.Parallel()
	probe := NewCredentialPresenceProbe([]httpauth.Provider{
		NewPolicyProvider(nil, nil, PolicySnapshot{HandlerKind: sdkauth.HandlerLocalAPIKey}, nil),
	})
	if probe == nil {
		t.Fatal("a local API-key chain must expose a probe")
	}
	if !probe(nil) {
		t.Fatal("an absent request cannot be proven credential-free")
	}
}
