package auth

import (
	"net/http"

	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

// NewCredentialPresenceDispositionProbe is the disposition-returning sibling of
// [NewCredentialPresenceProbe] and the single fail-open decision point of the
// conservative credential-presence question. Both constructors run the same
// aggregate over the same fixed active provider set, so a composition root that
// projects the cycle-neutral disposition can never disagree with the boolean
// form: it must not re-derive, wrap or cache the answer itself.
//
// The answer is [httpcontract.DefinitelyNoCredential] only when EVERY active
// provider proves the request cannot authenticate as presented; a provider that
// is credential-free, can authenticate the request, or cannot answer at all makes
// the whole chain [httpcontract.MayAuthenticate], and an unusable active set
// returns a nil probe, which is structurally the safe default.
func NewCredentialPresenceDispositionProbe(providers []httpauth.Provider) httpcontract.CredentialProbe {
	active := compactNonNilHTTPAuthProviders(providers)
	if len(active) == 0 {
		return nil
	}
	probers := make([]credentialPresenceProber, 0, len(active))
	for _, p := range active {
		prober, ok := p.(credentialPresenceProber)
		if !ok {
			return nil
		}
		probers = append(probers, prober)
	}
	return func(r *http.Request) httpcontract.CredentialDisposition {
		for _, prober := range probers {
			if prober.credentialPresence(r) != definitelyNoCredential {
				return httpcontract.MayAuthenticate
			}
		}
		return httpcontract.DefinitelyNoCredential
	}
}
