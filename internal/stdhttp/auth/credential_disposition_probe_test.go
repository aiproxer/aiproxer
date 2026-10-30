package auth_test

import (
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/stdhttp/auth"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

// gateWouldDeny mirrors the ingress gate decision exactly: the gate refuses a
// quarantined source only when the probe proves the request cannot authenticate,
// expressed in the committed gate as `if in.Probe != nil && !in.Probe(r)`. The
// polarity is INVERTED relative to the enum name, so it is asserted explicitly
// here rather than left to inference.
func gateWouldDeny(probe httpcontract.CredentialProbe, r *http.Request) bool {
	return probe != nil && probe(r) == httpcontract.DefinitelyNoCredential
}

// TestCredentialDispositionProbe_matchesTheGatePolarity pins the two answers of
// the conservative probe against the gate's refusal rule for both a
// credential-free and a credential-bearing request, so an inverted adapter can
// never lock out every legitimate user behind shared NAT, VPN or corporate
// egress.
func TestCredentialDispositionProbe_matchesTheGatePolarity(t *testing.T) {
	t.Parallel()

	providers := []httpauth.Provider{credentialProvider(sdkauth.HandlerLocalAPIKey, sdkauth.LevelAPIKey, aipsdk.HTTPHeaders{})}
	probe := auth.NewCredentialPresenceDispositionProbe(providers)
	if probe == nil {
		t.Fatal("a local API-key chain must expose a disposition probe")
	}

	bare := probeRequest(nil)
	if got := probe(bare); got != httpcontract.DefinitelyNoCredential {
		t.Fatalf("credential-free disposition = %d, want DefinitelyNoCredential (%d)", got, httpcontract.DefinitelyNoCredential)
	}
	if !gateWouldDeny(probe, bare) {
		t.Fatal("a credential-free quarantined request must be refused before authentication")
	}

	credentialed := probeRequest(map[string]string{"Authorization": "Bearer sk-live-secret"})
	if got := probe(credentialed); got != httpcontract.MayAuthenticate {
		t.Fatalf("credential-bearing disposition = %d, want MayAuthenticate (%d)", got, httpcontract.MayAuthenticate)
	}
	if gateWouldDeny(probe, credentialed) {
		t.Fatal("a credential-bearing request must always reach the normal auth chain")
	}
}

// TestCredentialDispositionProbe_isTheSameDecisionPointAsTheBoolAggregate pins
// the single fail-open decision point: the disposition projection and the
// pre-existing boolean aggregate must never disagree for the same active
// provider set and request, so a composition root can project either without
// re-deriving the answer itself.
func TestCredentialDispositionProbe_isTheSameDecisionPointAsTheBoolAggregate(t *testing.T) {
	t.Parallel()

	apiKey := credentialProvider(sdkauth.HandlerLocalAPIKey, sdkauth.LevelAPIKey, aipsdk.HTTPHeaders{})
	apiKeyHeaders := credentialProvider(sdkauth.HandlerLocalAPIKey, sdkauth.LevelAPIKey, aipsdk.HTTPHeaders{APIKey: []string{"X-Corp-Key"}})
	noop := credentialProvider(sdkauth.HandlerLocalNoop, sdkauth.LevelNone, aipsdk.HTTPHeaders{})
	remote := credentialProvider(sdkauth.HandlerRemote, sdkauth.LevelAPIKeySSO, aipsdk.HTTPHeaders{})

	for _, tc := range []struct {
		name      string
		providers []httpauth.Provider
		header    map[string]string
	}{
		{name: "api key without credential", providers: []httpauth.Provider{apiKey}},
		{name: "api key with credential", providers: []httpauth.Provider{apiKey}, header: map[string]string{"Authorization": "Bearer sk"}},
		{name: "configured effective header", providers: []httpauth.Provider{apiKeyHeaders}, header: map[string]string{"X-Corp-Key": "sk"}},
		{name: "non effective header", providers: []httpauth.Provider{apiKeyHeaders}, header: map[string]string{"Authorization": "Bearer sk"}},
		{name: "api key plus credential free", providers: []httpauth.Provider{apiKey, noop}},
		{name: "credential free only", providers: []httpauth.Provider{noop}},
		{name: "api key plus remote", providers: []httpauth.Provider{apiKey, remote}},
		{name: "api key plus unanswerable", providers: []httpauth.Provider{apiKey, stubProvider{}}},
		{name: "unanswerable only", providers: []httpauth.Provider{stubProvider{}}},
		{name: "nil entries only", providers: []httpauth.Provider{nil, nil}},
		{name: "no providers", providers: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			disposition := auth.NewCredentialPresenceDispositionProbe(tc.providers)
			presence := auth.NewCredentialPresenceProbe(tc.providers)
			if (disposition == nil) != (presence == nil) {
				t.Fatalf("disposition nil = %v, presence nil = %v: both must fail open together", disposition == nil, presence == nil)
			}
			if disposition == nil {
				return
			}
			req := probeRequest(tc.header)
			wantMay := presence(req)
			got := disposition(req)
			want := httpcontract.MayAuthenticate
			if !wantMay {
				want = httpcontract.DefinitelyNoCredential
			}
			if got != want {
				t.Fatalf("disposition = %d, want %d; the boolean aggregate answered %v", got, want, wantMay)
			}
		})
	}
}

// TestCredentialDispositionProbe_failsOpenForUnusableActiveSets pins the
// fail-open posture of the projection itself: an unusable active provider set is
// a nil probe, which the gate treats as "may authenticate", never as a license
// to refuse before authentication.
func TestCredentialDispositionProbe_failsOpenForUnusableActiveSets(t *testing.T) {
	t.Parallel()

	for _, providers := range [][]httpauth.Provider{nil, {nil}, {nil, nil}, {stubProvider{}}} {
		probe := auth.NewCredentialPresenceDispositionProbe(providers)
		if probe != nil {
			t.Fatalf("unusable active set must project no probe, got one for %d providers", len(providers))
		}
		if gateWouldDeny(probe, probeRequest(nil)) {
			t.Fatal("a nil probe must never license a pre-auth refusal")
		}
	}
}
