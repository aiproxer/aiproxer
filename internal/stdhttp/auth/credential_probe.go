package auth

import (
	"net/http"

	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

// credentialDisposition is the private, closed answer of the conservative
// credential-presence probe. mayAuthenticate is the safe default and
// definitelyNoCredential is the only license to refuse a quarantined source
// before authentication. Neither value carries credential material, so the probe
// can never become a credential-validity oracle.
type credentialDisposition uint8

const (
	// mayAuthenticate reports that the chain might still authenticate the request
	// as presented, so the request must reach the normal auth chain.
	mayAuthenticate credentialDisposition = iota
	// definitelyNoCredential reports that the provider can prove the request
	// carries no credential material it could authenticate with.
	definitelyNoCredential
)

// credentialPresenceProber is the private optional provider capability, modelled
// on [authSuccessContextAttacher]. Only an in-package provider can implement it,
// and a provider that cannot prove the request cannot authenticate as presented
// leaves mayAuthenticate, so unknown, custom and future providers, and any
// disposition a future revision adds, fail open to the normal auth chain.
type credentialPresenceProber interface {
	credentialPresence(r *http.Request) credentialDisposition
}

// credentialPresence implements [credentialPresenceProber]. A local_api_key chain
// provably denies a credential-free request, because its local authenticator
// answers missing_api_key before any remote leg runs, so an absent effective
// API-key header proves the chain cannot authenticate as presented. Every other
// handler kind may still authenticate, credential-free local_noop included. The
// effective credential is compared, never stored, returned, logged or retained,
// so this probe answers a question about the request and never discloses whether
// any particular credential is valid.
func (p *PolicyProvider) credentialPresence(r *http.Request) credentialDisposition {
	if p == nil || r == nil || p.Policy.HandlerKind != auth.HandlerLocalAPIKey {
		return mayAuthenticate
	}
	if p.headers().APIKeyFrom(r.Header) == "" {
		return definitelyNoCredential
	}
	return mayAuthenticate
}

// NewCredentialPresenceProbe aggregates one fixed active provider set into the
// conservative pre-auth credential-presence probe used to decide whether a
// quarantined source may still be refused before authentication. The answer is
// "cannot authenticate as presented" only when EVERY active provider proves it;
// a provider that is credential-free, can authenticate the request, or cannot
// answer at all makes the whole chain fail open, and an unusable active set fails
// open structurally with a nil result. This deliberately biases toward legitimate
// access rather than maximum auth-backend shielding, so a legitimate user behind
// shared NAT, VPN or corporate egress is never locked out by another actor on the
// same public address.
//
// The decision itself lives in [NewCredentialPresenceDispositionProbe], the
// disposition-returning sibling the composition root projects through the
// cycle-neutral self-defense contract; it must not be reimplemented there. This
// boolean form is only its inverse view, so the two can never answer differently
// for the same request and the composition root needs no second aggregate.
func NewCredentialPresenceProbe(providers []httpauth.Provider) func(r *http.Request) bool {
	probe := NewCredentialPresenceDispositionProbe(providers)
	if probe == nil {
		return nil
	}
	return func(r *http.Request) bool {
		return probe(r) != httpcontract.DefinitelyNoCredential
	}
}
