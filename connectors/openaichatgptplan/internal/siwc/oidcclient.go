package siwc

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// The provider's OIDC configuration and its published key set.
//
// Both documents are UNTRUSTED INPUT. The discovery document names the
// endpoints the flow talks to and the issuer the ID token must name, and
// the key set names the keys a signature is verified against. A document
// that is read carelessly is a document an attacker controls, so the
// reading is bounded, strict and cached.
//
// # Bounding
//
// Every response body is read through a limit, and a document that exceeds
// its limit is REFUSED rather than truncated: a truncated JSON document is
// a malformed document, and refusing it says so instead of reporting a
// parse failure for a body that was never complete.
//
// The key set is bounded in its number of keys as well. A key set with an
// unreasonable number of keys is refused rather than sampled.
//
// # Strictness
//
// A document that repeats a key is refused (see jsonstrict.go), and a key
// set that names the same key id twice is refused: the document would
// otherwise decide which of the two keys is used, and the one that is
// dropped is invisible.
//
// # Caching
//
// The discovery document and the key set are cached, because a validation
// that reached the provider for every token would turn a sign-in into a
// chain of round trips. The key set is refreshed when it ages out, and
// when a token names a key id the cached set does not hold: the provider
// may have rotated its keys, and the documentation asks for exactly that.
//
// A forced refresh is RATE LIMITED. A token that names an unknown key id
// would otherwise turn every validation into a request to the provider,
// and an attacker who can present tokens could use the connector to
// hammer the provider. The limit costs a rotation a short retry, and the
// cost is named here so that the next reader does not re-open it as an
// unknown.
const (
	// DefaultDiscoveryURL is the production OIDC discovery document.
	DefaultDiscoveryURL = "https://auth.openai.com/.well-known/openid-configuration"

	// maxDiscoveryBytes bounds the discovery document.
	maxDiscoveryBytes = 64 << 10

	// maxJWKSBytes bounds the key set.
	maxJWKSBytes = 128 << 10

	// maxJWKSKeys bounds the number of keys in one key set.
	maxJWKSKeys = 64

	// minRSAKeyBits is the smallest accepted RSA modulus.
	//
	// A key shorter than this is refused: a short modulus is not a
	// legitimate provider key, and accepting one would make the signature
	// verification a formality.
	minRSAKeyBits = 2048

	// minRSAExponent and maxRSAExponent are the bounds of the public
	// exponent crypto/rsa accepts.
	//
	// The upper bound is the standard library's own: it refuses an
	// exponent above 2^31-1 so that its behaviour does not depend on the
	// width of int (crypto/internal/fips140/rsa/rsa.go, in the toolchain
	// this connector is built with). The bound is checked BEFORE the
	// exponent is narrowed to an int, because an exponent the standard
	// library cannot use is MALFORMED provider key material - not a key
	// that would fail at verification time, and not a signature failure of
	// one token.
	minRSAExponent = 3
	maxRSAExponent = 1<<31 - 1

	// defaultDiscoveryTTL is how long a fetched discovery document is served
	// from the cache.
	defaultDiscoveryTTL = time.Hour

	// defaultJWKSTTL is how long a fetched key set is served from the
	// cache.
	defaultJWKSTTL = time.Hour

	// defaultRenewInterval is the minimum interval between two fetches of
	// the key set while the cached set is still fresh.
	//
	// A token that names an unknown key id forces a refresh, and the
	// refresh is refused while the previous fetch is younger than this
	// interval. The provider is protected from a request per unknown key
	// id, and a key rotation costs the operator one retry.
	defaultRenewInterval = 30 * time.Second

	// defaultOIDCTimeout is the timeout of the discovery and key-set
	// requests.
	defaultOIDCTimeout = 30 * time.Second
)

// The failures of the OIDC configuration and the key set.
//
// They are CONSTANT and value-free: a document is untrusted input, and a
// diagnostic that repeats part of it would put unvetted text into an
// operator- or support-facing error.
var (
	// errMalformedDocument reports a document this build cannot use.
	errMalformedDocument = errors.New("siwc: the provider's document could not be read")
	// errDocumentTooLarge reports a document that exceeds its bound.
	errDocumentTooLarge = errors.New("siwc: the provider's document exceeds its bound")
	// errUnknownKey reports that the key set publishes no usable key for
	// the token's key id.
	errUnknownKey = errors.New("siwc: the provider's key set publishes no key for the token's key id")
	// errOIDCUnavailable reports that the provider's configuration or key
	// set could not be reached.
	errOIDCUnavailable = errors.New("siwc: the provider's OIDC configuration could not be used")
	// errKeySetThrottled reports that a key-set fetch was refused because
	// the previous attempt is younger than the renew interval.
	//
	// The cause is distinct from an unreachable provider, so the two are
	// distinguishable in the code and in a test. The CLASS stays the same
	// - the provider's key set could not be read - because the recovery is
	// the same: the sign-in is retried after the interval.
	errKeySetThrottled = errors.New("siwc: the provider's key set was not read again inside the renew interval")
	// errInsecureDocumentURL reports a document URL that is not
	// authenticated.
	errInsecureDocumentURL = errors.New("siwc: the provider's document URL must use HTTPS")
)

// OIDCDiscovery is the provider's OIDC discovery document.
//
// The document is the AUTHORITY for the provider's endpoints: the issuer
// the ID token must name, the key set the signature is verified against,
// and the revocation endpoint the sign-out uses. A value that is not in
// the document is not used: a connector that guessed an endpoint would
// talk to a destination of its own choosing.
type OIDCDiscovery struct {
	// Issuer is the issuer the ID token must name.
	Issuer string
	// AuthorizationEndpoint is the provider's authorization endpoint.
	AuthorizationEndpoint string
	// TokenEndpoint is the provider's token endpoint.
	TokenEndpoint string
	// JWKSURI is the URI of the provider's published key set.
	JWKSURI string
	// RevocationEndpoint is the provider's revocation endpoint.
	//
	// It is the endpoint the sign-out uses to end the renewable session.
	// The revocation CALL belongs to the sign-out, not to the validation.
	RevocationEndpoint string
	// UserInfoEndpoint is the provider's user-info endpoint.
	UserInfoEndpoint string
}

// OIDCClientConfig is the configuration of the OIDC client.
type OIDCClientConfig struct {
	// DiscoveryURL is the URI of the OIDC discovery document. Empty means
	// DefaultDiscoveryURL.
	DiscoveryURL string
	// HTTPClient performs the discovery and key-set requests. Empty means a
	// client with a bounded timeout.
	HTTPClient *http.Client
	// Now is the clock. Empty means time.Now.
	Now func() time.Time
	// DiscoveryTTL is how long a fetched discovery document is served from
	// the cache. Zero means defaultDiscoveryTTL.
	DiscoveryTTL time.Duration
	// JWKSTTL is how long a fetched key set is served from the cache. Zero
	// means defaultJWKSTTL.
	JWKSTTL time.Duration
	// RenewInterval is the minimum interval between two fetches of the key
	// set while the cached set is still fresh. Zero means
	// defaultRenewInterval. A NEGATIVE value removes the limit, and the
	// removal is the caller's own decision.
	RenewInterval time.Duration
}

// OIDCClient fetches the provider's OIDC configuration and its key set.
//
// The client is safe for concurrent use: the cache is guarded, and a fetch
// that runs while another fetch is running waits for it rather than
// starting a second one. The lock is held across the fetch on purpose: a
// validation that misses a key id must not send a request per miss.
type OIDCClient struct {
	discoveryURL  string
	httpClient    *http.Client
	now           func() time.Time
	discoveryTTL  time.Duration
	jwksTTL       time.Duration
	renewInterval time.Duration

	mu          sync.Mutex
	discovery   OIDCDiscovery
	discoveryAt time.Time
	// discoveryAttempt is the time of the last discovery fetch ATTEMPT.
	//
	// The attempt is recorded whether it succeeded or not: an attempt
	// that failed closed the window just as an attempt that succeeded
	// did. The window is the renew interval, the same window the key-set
	// fetch uses.
	discoveryAttempt time.Time
	// discoveryFailure is the failure of the last discovery attempt.
	//
	// It is kept so that a refused attempt reports WHY it was refused: a
	// caller that is told "the configuration is throttled" and a caller
	// that is told "the provider could not be reached" need different
	// words, and only the first one is a policy.
	discoveryFailure error
	keys             map[string]*rsa.PublicKey
	keysAt           time.Time
	// keysAuthority is the authority the cached keys were fetched for.
	keysAuthority keyAuthority
	// lastAttempt is the time of the last key-set fetch ATTEMPT.
	//
	// The attempt is recorded whether it succeeded or not: an attempt that
	// failed closed the window just as an attempt that succeeded did.
	lastAttempt time.Time
	// lastFailure is the failure of the last key-set fetch attempt.
	lastFailure error
}

// keyAuthority identifies the authority a key set belongs to.
//
// The issuer and the key-set URI together are the authority. A key set
// fetched for one of them says nothing about the other: the current
// configuration may publish another key set, and a token that verifies
// against the OLD key would be accepted by a connector that no longer
// trusts that key.
type keyAuthority struct {
	issuer  string
	jwksURI string
}

// NewOIDCClient returns the client of the provider's OIDC configuration.
func NewOIDCClient(config OIDCClientConfig) (*OIDCClient, error) {
	discoveryURL := strings.TrimSpace(config.DiscoveryURL)
	if discoveryURL == "" {
		discoveryURL = DefaultDiscoveryURL
	}
	parsed, err := url.Parse(discoveryURL)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return nil, errors.New("siwc: the OIDC discovery URL must be an absolute URL")
	}
	// The discovery document decides WHICH keys a signature is verified
	// against, so the source of that document must be authenticated. A
	// plaintext source can be rewritten by anyone on the path, and a
	// rewritten discovery document publishes a rewritten key set.
	if err := requireSecureDocumentURL(discoveryURL); err != nil {
		return nil, err
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	discoveryTTL := config.DiscoveryTTL
	if discoveryTTL == 0 {
		discoveryTTL = defaultDiscoveryTTL
	}
	jwksTTL := config.JWKSTTL
	if jwksTTL == 0 {
		jwksTTL = defaultJWKSTTL
	}
	renewInterval := config.RenewInterval
	if renewInterval == 0 {
		renewInterval = defaultRenewInterval
	}
	return &OIDCClient{
		discoveryURL:  discoveryURL,
		httpClient:    oidcHTTPClient(config.HTTPClient),
		now:           now,
		discoveryTTL:  discoveryTTL,
		jwksTTL:       jwksTTL,
		renewInterval: renewInterval,
	}, nil
}

// requireSecureDocumentURL refuses a document URL that is not
// authenticated.
//
// The discovery document and the key set decide WHICH keys a signature is
// verified against. A plaintext source can be rewritten by anyone on the
// path, and a rewritten key set is a key set the attacker chose.
//
// The loopback address is the ONE exception, and it is narrow: it is the
// local test server, it never leaves the host, and it is spelled as an IP
// literal rather than as a name that a resolver could point elsewhere.
//
// The refusal names the SCHEME and never the URL: a URL may carry
// credentials in its userinfo or its query, and those must not reach a
// diagnostic.
func requireSecureDocumentURL(uri string) error {
	return requireSecureURL(
		uri,
		"the document URL must use HTTPS: a plaintext source can publish its own signing keys",
	)
}

// requireSecureURL refuses a URL that is not authenticated.
//
// The rule is the same for every URL this connector reads, and the
// REFUSAL is the caller's: what a plaintext source would cost depends on
// what the caller would send to it. A document that decides which keys a
// signature is verified against and a request that carries the account's
// bearer credential are both refused in the clear, for their own reasons.
//
// The refusal names the SCHEME and never the URL: a URL may carry
// credentials in its userinfo or its query, and those must not reach a
// diagnostic.
func requireSecureURL(uri, refusal string) error {
	parsed, err := url.Parse(uri)
	if err != nil {
		return errors.New("siwc: the URL could not be read")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname()) {
		return nil
	}
	return errors.New("siwc: " + refusal)
}

// isLoopbackHost reports whether the host is the loopback address.
func isLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

// oidcHTTPClient returns the client the OIDC requests use.
//
// The client's own transport, timeout and cookie jar are the caller's: only
// the redirect policy is this connector's. A redirect is REFUSED, because a
// redirect would let the provider send the request to a destination of its
// choosing, and the key set decides which keys a signature is verified
// against.
func oidcHTTPClient(caller *http.Client) *http.Client {
	if caller == nil {
		return &http.Client{
			Timeout: defaultOIDCTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &http.Client{
		Transport: caller.Transport,
		Timeout:   caller.Timeout,
		Jar:       caller.Jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Discovery returns the provider's OIDC configuration.
//
// The document is fetched once and served from the cache until it ages
// out.
func (c *OIDCClient) Discovery(ctx context.Context) (OIDCDiscovery, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.discoveryLocked(ctx)
}

// discoveryLocked returns the provider's OIDC configuration.
//
// The caller holds the lock.
//
// The document is fetched once and served from the cache until it ages out.
//
// A FAILED fetch is RATE LIMITED by the same window the key-set fetch uses:
// the attempt is recorded before the fetch, so a provider that fails does
// not attract a request per validation, and a refused attempt reports the
// failure of the last attempt instead of a policy word. The window is
// BOUNDED, so a transient failure cannot break the sign-in permanently: the
// next attempt after the window is admitted again.
//
// The window and the sign-in recovery belong together. A transient failure
// of the provider must not stop a sign-in forever, and a document that
// cannot be read must not be fetched again and again: the first is a
// bounded wait, the second is a bounded number of requests.
func (c *OIDCClient) discoveryLocked(ctx context.Context) (OIDCDiscovery, error) {
	if c.discovery.Issuer != "" && c.now().Sub(c.discoveryAt) < c.discoveryTTL {
		return c.discovery, nil
	}
	if !c.admitDiscovery() {
		return OIDCDiscovery{}, c.discoveryThrottleFailure()
	}
	// The attempt is recorded BEFORE the fetch, so that a fetch that
	// fails still closes the window.
	c.discoveryAttempt = c.now()
	body, err := c.fetchDocument(ctx, c.discoveryURL, maxDiscoveryBytes)
	if err != nil {
		c.discoveryFailure = err
		return OIDCDiscovery{}, err
	}
	document, err := parseDiscovery(body)
	if err != nil {
		// A document that cannot be read is not read again inside the
		// window: the provider published it, and fetching it once more
		// would publish the same document.
		c.discoveryFailure = err
		return OIDCDiscovery{}, err
	}
	c.discovery = document
	c.discoveryAt = c.now()
	c.discoveryFailure = nil
	return document, nil
}

// admitDiscovery reports whether a discovery fetch may be attempted now.
//
// The window is the renew interval, the same window the key-set fetch uses:
// one fetch of the provider's configuration per window, whether the previous
// attempt succeeded or failed.
func (c *OIDCClient) admitDiscovery() bool {
	if c.renewInterval <= 0 {
		return true
	}
	if c.discoveryAttempt.IsZero() {
		return true
	}
	return c.now().Sub(c.discoveryAttempt) >= c.renewInterval
}

// discoveryThrottleFailure returns the failure a refused discovery fetch
// reports.
//
// The refusal names the LAST attempt: a failed attempt reports its own
// failure, because the provider could not serve the configuration and
// blaming the caller for it would be wrong.
func (c *OIDCClient) discoveryThrottleFailure() error {
	if c.discoveryFailure != nil {
		return c.discoveryFailure
	}
	return errOIDCUnavailable
}

// VerificationAuthority returns the provider's OIDC configuration and the
// public key the token's key id names.
//
// The two come from ONE snapshot. The configuration and the key set it
// selects are read together under one lock, so a concurrent refresh cannot
// pair a new configuration with an old key set: the issuer the token is
// checked against and the key its signature is verified with are always
// the ones the same authority published.
//
// A configuration that selects ANOTHER key set invalidates the cached one:
// the keys of the previous authority are not a fallback, and a key set
// that cannot be read leaves the validation impossible rather than
// letting an old key stand in.
func (c *OIDCClient) VerificationAuthority(
	ctx context.Context,
	kid string,
) (OIDCDiscovery, *rsa.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	trimmed := strings.TrimSpace(kid)
	if trimmed == "" {
		return OIDCDiscovery{}, nil, errUnknownKey
	}
	discovery, err := c.discoveryLocked(ctx)
	if err != nil {
		return OIDCDiscovery{}, nil, err
	}
	keys, err := c.currentKeys(ctx, discovery)
	if err != nil {
		return discovery, nil, err
	}
	if key, ok := keys[trimmed]; ok {
		return discovery, key, nil
	}
	// The key id is not in the set the authority selects. The provider may
	// have rotated its keys, so ONE refresh is allowed - and the refresh is
	// ADMITTED only when the previous attempt is older than the renew
	// interval.
	refreshed, err := c.fetchKeysAdmitted(ctx, discovery, errUnknownKey)
	if err != nil {
		// A FAILED refresh leaves the cache exactly as it was. The
		// authority did not change - the configuration still selects the
		// same key set - so the set the cache holds is still the one the
		// authority published, and the failure is reported to the caller
		// rather than papered over. The cache is never allowed to stand
		// in for an authority that has changed: that path drops the set
		// in currentKeys.
		return discovery, nil, err
	}
	// The refreshed set is PUBLISHED to the cache, whether or not it holds
	// the requested key id.
	//
	// The refresh is the authority's CURRENT key set: a cache that kept
	// the superseded set would accept a key the authority has withdrawn,
	// and a cache that kept it would also MISS the key the authority has
	// published - so the next validation of a rotated token would fail
	// while the cache still holds a key the provider has retired.
	//
	// The set is bound to the same authority snapshot that selected it.
	c.keys = refreshed
	c.keysAt = c.now()
	c.keysAuthority = keyAuthority{issuer: discovery.Issuer, jwksURI: discovery.JWKSURI}
	if key, ok := refreshed[trimmed]; ok {
		return discovery, key, nil
	}
	return discovery, nil, errUnknownKey
}

// currentKeys returns the key set the current authority selects.
//
// The cached set is used only while it is FRESH and while it was fetched
// for the SAME authority. A set that was fetched for another authority is
// DROPPED: the authority that selected it is no longer the one in force,
// and a token that verifies against its key would be accepted by a
// connector that no longer trusts that key.
//
// The key set of the current authority that cannot be read leaves the
// validation impossible. The previous set is NOT used as a fallback.
func (c *OIDCClient) currentKeys(
	ctx context.Context,
	discovery OIDCDiscovery,
) (map[string]*rsa.PublicKey, error) {
	authority := keyAuthority{issuer: discovery.Issuer, jwksURI: discovery.JWKSURI}
	now := c.now()
	if c.keys != nil && c.keysAuthority == authority && now.Sub(c.keysAt) < c.jwksTTL {
		return c.keys, nil
	}
	keys, err := c.fetchKeysAdmitted(ctx, discovery, errKeySetThrottled)
	if err != nil {
		c.keys = nil
		c.keysAuthority = keyAuthority{}
		return nil, err
	}
	c.keys = keys
	c.keysAt = c.now()
	c.keysAuthority = authority
	return keys, nil
}

// fetchKeysAdmitted fetches the key set the authority selects, when a fetch
// is ADMITTED.
//
// The admission is the rate limit, and it applies to EVERY fetch: the
// cold cache, the expired cache and the refreshed cache alike. The
// attempt - successful or not - closes the window, so a provider that
// fails does not attract a request per validation, and a failed attempt
// cannot be retried inside the window either.
//
// The fallback is the outcome a refused fetch reports when the previous
// attempt SUCCEEDED. It differs per caller, because the two callers know
// different things: a caller that has no key set at all cannot name a key,
// and a caller that holds a set which does not hold the key id can.
func (c *OIDCClient) fetchKeysAdmitted(
	ctx context.Context,
	discovery OIDCDiscovery,
	fallback error,
) (map[string]*rsa.PublicKey, error) {
	if !c.admitFetch() {
		return nil, c.throttleFailure(fallback)
	}
	// The attempt is recorded BEFORE the fetch, so that a fetch that
	// fails still closes the window.
	c.lastAttempt = c.now()
	keys, err := c.fetchKeys(ctx, discovery.JWKSURI)
	c.lastFailure = err
	if err != nil {
		return nil, err
	}
	return keys, nil
}

// admitFetch reports whether a key-set fetch may be attempted now.
func (c *OIDCClient) admitFetch() bool {
	if c.renewInterval <= 0 {
		return true
	}
	if c.lastAttempt.IsZero() {
		return true
	}
	return c.now().Sub(c.lastAttempt) >= c.renewInterval
}

// throttleFailure returns the failure a refused fetch reports.
//
// The refusal names the LAST attempt: a failed attempt reports its own
// failure, because the provider could not serve the key set and blaming
// the token for it would be wrong; a successful attempt reports the
// caller's fallback, because the set was read and the outcome is about
// the key it does or does not hold.
//
// The caller's fallback for the COLD cache is the throttled cause: the
// connector has no key set at all, and the reason it has none is that the
// previous attempt is too recent. The caller's fallback for the REFRESH
// is the unknown key: the set the connector holds does not carry the key
// id, and the refresh that would settle it is not admitted.
func (c *OIDCClient) throttleFailure(fallback error) error {
	if c.lastFailure != nil {
		return c.lastFailure
	}
	return fallback
}

// fetchKeys fetches and reads the provider's key set.
func (c *OIDCClient) fetchKeys(ctx context.Context, uri string) (map[string]*rsa.PublicKey, error) {
	body, err := c.fetchDocument(ctx, uri, maxJWKSBytes)
	if err != nil {
		return nil, err
	}
	return parseJWKS(body)
}

// fetchDocument fetches one bounded document.
//
// The body is read through a limit that is one byte wider than the bound,
// so a document that exceeds the bound is DETECTED instead of silently
// truncated. A truncated document would be a malformed document, and the
// connector would report a parse failure for a body it never read whole.
func (c *OIDCClient) fetchDocument(ctx context.Context, uri string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, errOIDCUnavailable
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, errOIDCUnavailable
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, limit))
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusOK {
		return nil, errOIDCUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, errOIDCUnavailable
	}
	if int64(len(body)) > limit {
		return nil, errDocumentTooLarge
	}
	return body, nil
}

// parseDiscovery reads the provider's OIDC configuration.
//
// The document must name an issuer and a key set, and the issuer must be
// an absolute URL: an ID token's issuer claim is compared with it, and a
// claim compared with a relative string would be a comparison with
// nonsense.
//
// The members are read by their EXACT names. A typed decode would match
// a member's name case-insensitively, so a document carrying both
// "jwks_uri" and "JWKS_URI" would decide by itself which key set the
// connector trusts.
//
// Every member the document carries must be readable as the shape it
// carries: a member that ARRIVED with a shape the connector cannot read
// makes the document UNREADABLE, and it is not read as though the
// document had not carried it.
func parseDiscovery(body []byte) (OIDCDiscovery, error) {
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return OIDCDiscovery{}, errMalformedDocument
	}
	members, err := jsonMembers(body)
	if err != nil {
		return OIDCDiscovery{}, errMalformedDocument
	}
	issuer, err := documentString(members, "issuer")
	if err != nil {
		return OIDCDiscovery{}, errMalformedDocument
	}
	issuer = strings.TrimSpace(issuer)
	if issuer == "" {
		return OIDCDiscovery{}, errMalformedDocument
	}
	parsed, err := url.Parse(issuer)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return OIDCDiscovery{}, errMalformedDocument
	}
	jwksURI, err := documentString(members, "jwks_uri")
	if err != nil {
		return OIDCDiscovery{}, errMalformedDocument
	}
	jwksURI = strings.TrimSpace(jwksURI)
	if jwksURI == "" {
		return OIDCDiscovery{}, errMalformedDocument
	}
	parsedJWKS, err := url.Parse(jwksURI)
	if err != nil || !parsedJWKS.IsAbs() || parsedJWKS.Host == "" {
		return OIDCDiscovery{}, errMalformedDocument
	}
	// The key set decides which keys a signature is verified against, so
	// the URI the configuration publishes must be authenticated too. A
	// configuration that points at a plaintext key set is refused: it
	// would downgrade the authenticity of every key.
	if err := requireSecureDocumentURL(jwksURI); err != nil {
		return OIDCDiscovery{}, errInsecureDocumentURL
	}
	authorizationEndpoint, err := documentString(members, "authorization_endpoint")
	if err != nil {
		return OIDCDiscovery{}, errMalformedDocument
	}
	tokenEndpoint, err := documentString(members, "token_endpoint")
	if err != nil {
		return OIDCDiscovery{}, errMalformedDocument
	}
	revocationEndpoint, err := documentString(members, "revocation_endpoint")
	if err != nil {
		return OIDCDiscovery{}, errMalformedDocument
	}
	userInfoEndpoint, err := documentString(members, "userinfo_endpoint")
	if err != nil {
		return OIDCDiscovery{}, errMalformedDocument
	}
	return OIDCDiscovery{
		Issuer:                issuer,
		AuthorizationEndpoint: strings.TrimSpace(authorizationEndpoint),
		TokenEndpoint:         strings.TrimSpace(tokenEndpoint),
		JWKSURI:               jwksURI,
		RevocationEndpoint:    strings.TrimSpace(revocationEndpoint),
		UserInfoEndpoint:      strings.TrimSpace(userInfoEndpoint),
	}, nil
}

// jwk is one key of the provider's key set.
//
// The members are read by their EXACT names. A typed decode would match a
// member's name case-insensitively, so a key that carries both "kid" and
// "KID" would decide by itself which key id the connector looks it up
// under.
//
// Every member carries the state of its presence and shape, because a
// member that ARRIVED with a shape the connector cannot read is not a
// member the connector may skip.
type jwk struct {
	KeyType   memberValue[string]
	KeyID     memberValue[string]
	Use       memberValue[string]
	Algorithm memberValue[string]
	Modulus   memberValue[string]
	Exponent  memberValue[string]
}

// readJWK reads one key by exact member name.
func readJWK(members map[string]json.RawMessage) jwk {
	return jwk{
		KeyType:   memberString(members, "kty"),
		KeyID:     memberString(members, "kid"),
		Use:       memberString(members, "use"),
		Algorithm: memberString(members, "alg"),
		Modulus:   memberString(members, "n"),
		Exponent:  memberString(members, "e"),
	}
}

// The key-set vocabulary.
const (
	// jwkKeyTypeRSA is the only key type this connector verifies with.
	jwkKeyTypeRSA = "RSA"
	// jwkUseSignature marks a key that signs.
	jwkUseSignature = "sig"
	// signingAlgRS256 is the only signature algorithm this connector
	// accepts.
	//
	// The provider's discovery document publishes it as the only supported
	// ID-token signing algorithm, and a token that names another algorithm
	// is REFUSED rather than attempted: an algorithm the connector does not
	// accept is not a signature it can verify.
	signingAlgRS256 = "RS256"
)

// parseJWKS reads the provider's key set.
//
// The set is read as a whole or refused. A key of another type, or a key
// for another use, or a key for another algorithm, is SKIPPED: the
// provider may publish keys this connector has no use for, and skipping
// one is not the same as sampling one of two values for the same name.
//
// A key set that names the same key id twice is REFUSED: the document
// would decide which of the two keys is used, and the one that is dropped
// would be invisible to every check that follows. The check covers EVERY
// entry of the set and not only the entries this connector would use: a
// set that names one key id twice - once with a usable algorithm and once
// with another - is just as ambiguous as a set that names it twice with
// two usable keys, and it would hide the ambiguity behind a filter.
//
// A key of the accepted type whose material is broken REFUSES the set: a
// document that carries a broken key is a document that cannot be trusted,
// and the keys this connector does use would come from it.
//
// The members are read by their EXACT names, and the count is bounded
// BEFORE the entries are read: a document with an unreasonable number of
// keys is refused rather than sampled.
func parseJWKS(body []byte) (map[string]*rsa.PublicKey, error) {
	if err := rejectDuplicateJSONKeys(body); err != nil {
		return nil, errMalformedDocument
	}
	members, err := jsonMembers(body)
	if err != nil {
		return nil, errMalformedDocument
	}
	entries := memberArray(members, "keys")
	if !entries.usable() {
		return nil, errMalformedDocument
	}
	if len(entries.Value) == 0 {
		return nil, errMalformedDocument
	}
	if len(entries.Value) > maxJWKSKeys {
		return nil, errMalformedDocument
	}
	keys := make(map[string]*rsa.PublicKey, len(entries.Value))
	seen := make(map[string]struct{}, len(entries.Value))
	for _, raw := range entries.Value {
		entryMembers, err := jsonMembers(raw)
		if err != nil {
			return nil, errMalformedDocument
		}
		entry := readJWK(entryMembers)
		// The key type and the key id are interpreted for EVERY entry: the
		// type decides whether the entry is a candidate at all, and the
		// id takes part in the ambiguity check below. A member that
		// ARRIVED with a shape the connector cannot read makes the set
		// unreadable - it is not a member the connector may skip.
		if entry.KeyType.State == memberInvalid || entry.KeyID.State == memberInvalid {
			return nil, errMalformedDocument
		}
		keyID := strings.TrimSpace(entry.KeyID.Value)
		if keyID != "" {
			if _, duplicate := seen[keyID]; duplicate {
				return nil, errMalformedDocument
			}
			seen[keyID] = struct{}{}
		}
		if !entry.KeyType.usable() || entry.KeyType.Value != jwkKeyTypeRSA {
			continue
		}
		// The use and the algorithm are interpreted for the entries that
		// are candidates, and the material for the entries that are.
		if entry.Use.State == memberInvalid || entry.Algorithm.State == memberInvalid {
			return nil, errMalformedDocument
		}
		if entry.Use.usable() && entry.Use.Value != jwkUseSignature {
			continue
		}
		if entry.Algorithm.usable() && entry.Algorithm.Value != signingAlgRS256 {
			continue
		}
		if keyID == "" {
			continue
		}
		if entry.Modulus.State != memberUsable || entry.Exponent.State != memberUsable {
			return nil, errMalformedDocument
		}
		key, err := parseRSAKey(entry)
		if err != nil {
			return nil, err
		}
		keys[keyID] = key
	}
	if len(keys) == 0 {
		return nil, errMalformedDocument
	}
	return keys, nil
}

// parseRSAKey reads the material of one RSA key.
//
// The key must be one crypto/rsa can USE, and the bounds are the standard
// library's own: the exponent must be an odd number of at least three and
// at most 2^31-1, and the modulus must be odd. A key the standard library
// refuses is MALFORMED provider key material: the key set is refused
// rather than carried into a verification that would fail as a signature
// error of one token.
//
// The exponent is checked BEFORE it is narrowed to an int: a value that
// does not fit the standard library's range must not depend on the width
// of the machine the connector runs on.
func parseRSAKey(entry jwk) (*rsa.PublicKey, error) {
	modulus, err := decodeBase64URL(entry.Modulus.Value)
	if err != nil || len(modulus) == 0 {
		return nil, errMalformedDocument
	}
	exponent, err := decodeBase64URL(entry.Exponent.Value)
	if err != nil || len(exponent) == 0 {
		return nil, errMalformedDocument
	}
	value := new(big.Int).SetBytes(exponent)
	if !value.IsInt64() {
		return nil, errMalformedDocument
	}
	exponentValue := value.Int64()
	if exponentValue < minRSAExponent || exponentValue > maxRSAExponent {
		return nil, errMalformedDocument
	}
	if exponentValue%2 == 0 {
		return nil, errMalformedDocument
	}
	modulusValue := new(big.Int).SetBytes(modulus)
	if modulusValue.BitLen() < minRSAKeyBits {
		return nil, errMalformedDocument
	}
	// crypto/rsa refuses an EVEN modulus, so a key it cannot use is
	// malformed provider key material.
	if modulusValue.Bit(0) == 0 {
		return nil, errMalformedDocument
	}
	return &rsa.PublicKey{N: modulusValue, E: int(exponentValue)}, nil
}

// decodeBase64URL decodes a base64url value.
//
// The unpadded form is the documented one, and the padded form is accepted
// as well: a publisher that pads its key material is not a publisher whose
// key material this connector cannot read.
func decodeBase64URL(value string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err == nil {
		return decoded, nil
	}
	return base64.URLEncoding.DecodeString(value)
}
