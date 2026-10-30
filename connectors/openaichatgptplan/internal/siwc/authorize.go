package siwc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/oauthcred"
)

// The official Sign in with ChatGPT (SIWC) authorization surface.
//
// The values below are the ones OpenAI documents for the open-source and
// locally hosted plan-usage flow. They are constants of the provider
// contract, not configuration: a client that changes them talks a different
// protocol.
//
// The documented endpoints are:
//
//	https://auth.openai.com/api/accounts/authorize
//	https://auth.openai.com/api/accounts/oauth/token
//
// The flow is a public-client OAuth 2.0 flow with PKCE and an OIDC nonce.
// It needs no client secret and no partner API key.
const (
	// DynamicAgentClientID is the client id of the FIRST-TIME dynamic
	// registration entrypoint.
	//
	// It is not a client id: it is the door through which a client registers
	// itself and receives an issued client id of its own. The issued id is
	// what every later request uses, and it is what the registration
	// persists. The dynamic entry point is never persisted and never used
	// for the code exchange.
	DynamicAgentClientID = "dynamic_agent_client"

	// DefaultAgentName is the product's stable agent name.
	//
	// OpenAI shows it to the user as display metadata when the agent asks
	// for authorization, and the documentation requires the same name to be
	// used consistently across installations. It is therefore a constant of
	// the product, not a per-installation value: two installations of the
	// same product must present the same name.
	//
	// The name identifies AIProxer truthfully. It never identifies another
	// vendor's agent.
	DefaultAgentName = "AIProxer"

	// AuthorizationEndpoint is the documented authorization endpoint.
	AuthorizationEndpoint = "https://auth.openai.com/api/accounts/authorize"

	// TokenEndpoint is the documented token endpoint. The code exchange and
	// the refresh both use it.
	TokenEndpoint = "https://auth.openai.com/api/accounts/oauth/token"

	// PlanResource is the resource the plan usage is granted for.
	PlanResource = "https://api.openai.com/v1"

	// AuthorizationScope is the requested scope set.
	//
	// The identity scopes are "openid profile email". The ChatGPT plan usage
	// scopes are "offline_access resource.invoke chatgpt.tokens.use.direct":
	// the offline scope is what makes the grant renewable, and the direct
	// plan-use scope is what authorizes plan usage.
	AuthorizationScope = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"

	// LoopbackHost is the loopback host of the callback URI.
	//
	// The address is the literal loopback address, never the name
	// "localhost": the provider matches the redirect URI against the one
	// the authorization request carried, and a name would resolve to a
	// different string.
	LoopbackHost = "127.0.0.1"

	// CallbackPath is the path of the callback URI.
	//
	// The path is part of the redirect URI the provider matches, so it is
	// fixed: only the port may differ between attempts.
	CallbackPath = "/auth/callback"

	// DefaultAttemptTTL bounds the lifetime of one pending attempt.
	//
	// An attempt holds a fresh state, a fresh nonce and a PKCE verifier, and
	// a loopback listener. It is bounded in time so that an abandoned
	// attempt cannot keep a listener alive.
	DefaultAttemptTTL = 5 * time.Minute

	// maxAuthorizationAttempts bounds the restarts of one sign-in.
	//
	// A rejected authorization code is discarded and the sign-in starts
	// again with the issued client id retained. The bound keeps a provider
	// that rejects every code from turning one sign-in into an endless
	// sequence of browser round trips.
	maxAuthorizationAttempts = 3

	// maxTokenResponseBytes bounds the token endpoint's response body.
	maxTokenResponseBytes = 1 << 20

	// maxErrorDescriptionLength bounds the provider's error description.
	maxErrorDescriptionLength = 256

	// defaultReconcileTimeout bounds the reconciliation that follows a
	// complete credential answer.
	//
	// The bound is the CONNECTOR's: the provider has already issued the
	// credential, and the validation and the publication of it are the
	// connector's obligation. A caller whose own deadline has already
	// expired is not a reason a credential the provider issued may be
	// lost, and the bound is deliberately not the caller's.
	//
	// The value matches the other bounds of this package - the lock budget
	// and the OIDC timeout - because they bound the same kind of work: a
	// wait for something outside this process.
	defaultReconcileTimeout = 30 * time.Second

	// invalidGrantCode is the OAuth error code of a rejected authorization
	// code.
	invalidGrantCode = "invalid_grant"
)

// The failures of the authorization flow.
//
// They are sentinels so that a caller can classify an outcome without
// matching on message text. None of them carries credential material.
var (
	// ErrStateMismatch reports a callback that cannot prove it belongs to
	// the pending attempt.
	ErrStateMismatch = errors.New("siwc: the callback does not belong to the pending authorization attempt")

	// ErrAttemptExpired reports an attempt that outlived its lifetime
	// without a callback.
	ErrAttemptExpired = errors.New("siwc: the authorization attempt expired before the browser returned")

	// ErrAttemptClosed reports an attempt whose listener was shut down
	// before it produced a result.
	ErrAttemptClosed = errors.New("siwc: the authorization attempt was closed before the browser returned")

	// ErrRegistrationIncomplete reports a first registration whose callback
	// did not carry the issued client id.
	ErrRegistrationIncomplete = errors.New("siwc: the registration is incomplete: the callback carried no issued client id")

	// ErrIssuedClientIDMismatch reports a callback that carried a
	// different issued client id than the pending request.
	ErrIssuedClientIDMismatch = errors.New("siwc: the callback carried a different issued client id than the pending request")

	// ErrSubjectMismatch reports an authorized account that is not the
	// selected registration.
	ErrSubjectMismatch = errors.New("siwc: the authorized account does not match the selected registration")

	// ErrInvalidGrant reports an authorization code the token endpoint
	// rejected.
	ErrInvalidGrant = errors.New("siwc: the token endpoint rejected the authorization code")
)

// AttemptKind distinguishes the two authorization flows.
type AttemptKind int

const (
	// AttemptRegistration is a first registration.
	//
	// The authorization request carries the dynamic registration entry
	// point and the product's agent name, and the callback must carry the
	// issued client id the provider created for this registration.
	AttemptRegistration AttemptKind = iota

	// AttemptReauthorization is a returning authorization.
	//
	// The authorization request carries the saved issued client id, omits
	// the agent name, and may carry the retained identity hints of the
	// selected account. The callback may omit the issued client id, and it
	// may not carry a different one.
	AttemptReauthorization
)

// BrowserOpener opens the authorization URL in the operator's browser.
//
// The opener is a dependency because the browser is an operator surface: the
// connector process owns the callback listener, while the browser is a
// separate program that the operating system launches.
type BrowserOpener func(ctx context.Context, authorizationURL string) error

// Notice reports a non-fatal event to the operator.
//
// The notice carries operator-facing text only. It never carries a token, a
// code, a verifier or a state.
type Notice func(message string)

// ProfileAccess is the persistence seam of the authorization flow.
//
// The concrete implementation is the protected profile store of this
// package. The interface exists so that the flow can be exercised without a
// filesystem, and so that the flow depends on the two operations it needs
// rather than on the whole store.
type ProfileAccess interface {
	// Load returns the record of one saved registration.
	Load(id string) (ProfileRecord, error)
	// SaveNew publishes the record of one registration ONLY when that
	// registration does not exist yet.
	//
	// A first registration uses it, so that two overlapping registrations
	// of the same local id cannot destroy each other: the second one is
	// refused and neither record is replaced.
	SaveNew(rec ProfileRecord) error
	// Publish applies a change to the record of one registration and
	// publishes the result.
	//
	// The change is applied to the record that is CURRENTLY stored, and
	// the publication is serialized with every other publication of the
	// same registration. A writer therefore never publishes a record it
	// read before another writer changed it - which is what a returning
	// authorization needs: its flow waits for a browser, and a refresh
	// may have replaced the credentials in the meantime.
	//
	// The publication does NOT take a context: it is a REQUIRED COMMIT of
	// an outcome the provider has already committed, and it is bounded by
	// the store's own budget. A caller cannot ask for the commit to be
	// skipped.
	Publish(id string, change ProfileChange) (ProfileRecord, error)
}

// HostIDProvider creates or loads the stable agent-host identity.
//
// The concrete implementation is the host store of this package. The
// interface is consumed where the identity is consumed, so a caller that
// only reads an identity never gains the ability to replace it.
type HostIDProvider interface {
	// EnsureHostID returns this host's stable identity, creating it on the
	// first start.
	EnsureHostID() (string, error)
}

// TokenResponse is the token endpoint's successful response.
//
// It holds credential material, so it never renders itself into a log line
// and never appears in an error. The formatter below makes that guarantee
// for every verb fmt dispatches to it; the documented bypasses are the ones
// ProfileRecord documents.
type TokenResponse struct {
	// AccessToken is the bearer token for the public API.
	AccessToken string `json:"access_token"`
	// RefreshToken is the renewable credential of the grant.
	RefreshToken string `json:"refresh_token"`
	// IDToken is the OIDC identity token of the authorized account.
	IDToken string `json:"id_token"`
	// TokenType is the type of the access token.
	TokenType string `json:"token_type"`
	// ExpiresIn is the lifetime of the access token in seconds.
	ExpiresIn int64 `json:"expires_in"`
	// Scope is the space-separated set of granted scopes.
	Scope string `json:"scope"`
	// EarliestRefreshAt is the earliest time a refresh of this grant may
	// be attempted, in seconds since the epoch.
	//
	// The value is not used by this build yet - the refresh is the next
	// slice's work - but it is READ and its shape is checked, so that a
	// response that carries an unreadable member is refused rather than
	// half-read.
	EarliestRefreshAt int64 `json:"earliest_refresh_at"`
}

// String renders the response without its credential material.
func (r TokenResponse) String() string {
	return fmt.Sprintf(
		"token response (type %q, expires in %d seconds, %d scopes)",
		r.TokenType,
		r.ExpiresIn,
		len(strings.Fields(r.Scope)),
	)
}

// GoString renders the response without its credential material.
//
// The Go-syntax verb uses this method rather than the stringer, so a
// response formatted with "%#v" would otherwise print its tokens.
func (r TokenResponse) GoString() string { return r.String() }

// Format implements fmt.Formatter, so that no formatting verb can reach the
// response's fields.
//
// The guarantee holds when fmt can dispatch to this response, so the routes
// where it cannot are the ones ProfileRecord documents: "%T" is safe, "%w"
// and "%p" bypass every method on this type, and a response held in an
// unexported field or supplied as a non-interfaceable reflect.Value is
// rendered from its raw fields.
func (r TokenResponse) Format(state fmt.State, verb rune) {
	safe := r.String()
	if verb == 'q' {
		_, _ = io.WriteString(state, strconv.Quote(safe))
		return
	}
	_, _ = io.WriteString(state, safe)
}

// TokenError is a failed token exchange.
//
// The provider's own error code is preserved, because the recovery differs
// per code: a rejected authorization code is discarded and the sign-in
// starts again, while a malformed request is not retryable that way.
type TokenError struct {
	// StatusCode is the HTTP status of the exchange.
	StatusCode int
	// Code is the provider's OAuth error code.
	Code string
	// Description is the provider's bounded, redacted error description.
	Description string
}

// Error renders the failure without credential material.
func (e *TokenError) Error() string {
	if e.Description == "" {
		return fmt.Sprintf("siwc: the token endpoint returned %s (status %d)", e.Code, e.StatusCode)
	}
	return fmt.Sprintf("siwc: the token endpoint returned %s (status %d): %s", e.Code, e.StatusCode, e.Description)
}

// Is reports whether the failure is the rejected-authorization-code class.
//
// The comparison is by the provider's error code, so a caller can start a
// fresh authorization without matching on message text.
func (e *TokenError) Is(target error) bool {
	return target == ErrInvalidGrant && e.Code == invalidGrantCode
}

// AuthorizerConfig is the configuration of the authorization service.
type AuthorizerConfig struct {
	// AuthorizationEndpoint is the provider's authorization endpoint.
	// Empty means AuthorizationEndpoint.
	AuthorizationEndpoint string
	// TokenEndpoint is the provider's token endpoint. Empty means
	// TokenEndpoint.
	TokenEndpoint string
	// HTTPClient performs the code exchange. Empty means a client with a
	// bounded timeout.
	HTTPClient *http.Client
	// BrowserOpener opens the authorization URL. Empty means the attempt
	// only reports the URL.
	BrowserOpener BrowserOpener
	// Notice reports non-fatal events to the operator. Empty means no
	// notice is reported.
	Notice Notice
	// AttemptTTL bounds the lifetime of one attempt. Empty means
	// DefaultAttemptTTL.
	AttemptTTL time.Duration
	// ReconcileTimeout bounds the reconciliation that follows a complete
	// credential answer.
	//
	// The bound is the CONNECTOR's, and it is not the caller's: the
	// provider has already issued the credential, and the validation and
	// the publication of it are the connector's obligation. Zero means
	// defaultReconcileTimeout.
	ReconcileTimeout time.Duration
	// AgentName is the product's agent name. Empty means
	// DefaultAgentName.
	AgentName string
	// Profiles persists the registrations. It is required.
	Profiles ProfileAccess
	// Hosts provides the stable agent-host identity. It is required.
	Hosts HostIDProvider
	// Validator validates the ID token of a token response. It is
	// required.
	Validator IDTokenValidator
	// Now is the clock. Empty means time.Now.
	Now func() time.Time
}

// Authorizer is the SIWC authorization service.
//
// It owns the pending attempts, the loopback callback listener, the
// authorization request, the code exchange and the registration lifecycle
// the exchange feeds. Every value that belongs to one attempt - the state,
// the nonce, the PKCE verifier and the selected callback URI - lives in that
// attempt and nowhere else.
type Authorizer struct {
	config AuthorizerConfig
}

// NewAuthorizer returns the authorization service.
//
// The profile store, the host identity and the ID-token validator are
// required: an authorization flow that cannot persist a registration, that
// cannot name its host, or that cannot validate an identity is not a flow
// this connector may run.
func NewAuthorizer(config AuthorizerConfig) (*Authorizer, error) {
	if config.Profiles == nil {
		return nil, errors.New("siwc: the authorization service needs a profile store")
	}
	if config.Hosts == nil {
		return nil, errors.New("siwc: the authorization service needs a host identity provider")
	}
	if config.Validator == nil {
		return nil, errors.New("siwc: the authorization service needs an ID-token validator")
	}
	if strings.TrimSpace(config.AuthorizationEndpoint) == "" {
		config.AuthorizationEndpoint = AuthorizationEndpoint
	}
	if strings.TrimSpace(config.TokenEndpoint) == "" {
		config.TokenEndpoint = TokenEndpoint
	}
	if strings.TrimSpace(config.AgentName) == "" {
		config.AgentName = DefaultAgentName
	}
	if config.AttemptTTL <= 0 {
		config.AttemptTTL = DefaultAttemptTTL
	}
	if config.ReconcileTimeout <= 0 {
		config.ReconcileTimeout = defaultReconcileTimeout
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Authorizer{config: config}, nil
}

// reconcileContext derives the context of the reconciliation that follows a
// complete credential answer.
//
// This is the RECEIPT BOUNDARY of the package. Everything before it is the
// caller's interest: the browser wait and the code exchange, and a
// cancellation there withdraws the sign-in. Everything after it is the
// operation's obligation: the provider has already issued a complete
// credential set, and the validation and the publication of it belong to
// the connector.
//
// The derived context is the caller's WITHOUT its cancellation and without
// its deadline, and it carries the reconciliation's own bounded deadline
// instead. The values the caller attached are kept: they are the trace of
// the call, not its interest.
//
// The current post-receipt steps that take a context are given the
// reconciliation context. The helper returns an ordinary context, so this is
// a rule enforced at the current call sites, not one the type system enforces.
func reconcileContext(
	ctx context.Context,
	budget time.Duration,
) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if budget <= 0 {
		budget = defaultReconcileTimeout
	}
	return context.WithTimeout(context.WithoutCancel(ctx), budget)
}

// reconcileAfterReceipt derives the owned, bounded context of the
// reconciliation that follows a complete credential answer.
//
// The derivation happens at the receipt boundary, and the budget is the
// configured one: the connector's, not the caller's.
func (a *Authorizer) reconcileAfterReceipt(
	ctx context.Context,
) (context.Context, context.CancelFunc) {
	return reconcileContext(ctx, a.config.ReconcileTimeout)
}

// validateAfterReceipt validates the ID token of a complete credential set.
//
// The validation is the FIRST step of the post-receipt reconciliation, and
// it runs under the OWNED, bounded context: the provider has already
// issued the credential, and a caller that goes away between the answer
// and the validation does not take the credential with it.
//
// The validation is not weakened. Every constraint the validator enforces
// is enforced, and an unvalidated credential is never persisted: a
// validation that fails is a failed sign-in, and the flow publishes
// nothing.
func (a *Authorizer) validateAfterReceipt(
	ctx context.Context,
	outcome attemptOutcome,
) (Identity, error) {
	reconcile, cancel := a.reconcileAfterReceipt(ctx)
	defer cancel()
	return a.config.Validator.ValidateIDToken(reconcile, outcome.tokens, TokenExpectation{
		Nonce:    outcome.nonce,
		Audience: outcome.issuedClientID,
	})
}

// pendingAuthorization is the immutable state of one attempt.
//
// Every field belongs to exactly one attempt. The callback URI in
// particular is the value the authorization request carries and the value
// the code exchange must carry, byte for byte: it is never rebuilt from the
// listener, from a default, or from the address the operating system
// reports later.
//
// The deadline is the attempt's own bound. It is measured on the REAL
// clock, because it bounds a socket and a timer: an injected clock would
// not bound either of them.
type pendingAuthorization struct {
	kind           AttemptKind
	state          string
	nonce          string
	verifier       string
	challenge      string
	redirectURI    string
	hostID         string
	issuedClientID string
	idTokenHint    string
	loginHint      string
	deadline       time.Time
}

// Attempt is one pending authorization attempt.
//
// The attempt owns the selected callback URI and the loopback listener that
// serves it. It is bounded in time: an attempt that is not completed within
// its lifetime closes its listener and fails.
type Attempt struct {
	pending    pendingAuthorization
	listener   *callbackListener
	authorizer *Authorizer
}

// Kind reports whether the attempt is a first registration or a returning
// authorization.
func (att *Attempt) Kind() AttemptKind { return att.pending.kind }

// RedirectURI returns the exact callback URI this attempt selected.
//
// The value is the one the authorization request carried and the one the
// code exchange will carry. It is never reconstructed.
func (att *Attempt) RedirectURI() string { return att.pending.redirectURI }

// AuthorizationURL returns the URL the operator's browser has to open.
//
// The URL carries the attempt's state, its nonce and its PKCE challenge, so
// it is a credential-bearing value: it must not reach a log. Use
// RedactedAuthorizationURL for anything an operator or a transcript sees.
func (att *Attempt) AuthorizationURL() string { return att.authorizationURL() }

// RedactedAuthorizationURL returns the authorization URL with a retained
// ID-token hint redacted.
//
// The hint identifies the account, and the documentation requires that an
// authorization URL carrying one is redacted from logs and diagnostics.
func (att *Attempt) RedactedAuthorizationURL() string {
	return redactAuthorizationURL(att.authorizationURL())
}

// Await waits for the loopback callback and returns its validated result.
//
// The attempt fails when the callback does not belong to it, when the
// attempt expires, when the attempt is closed, or when the context is
// cancelled. No result is ever produced from a callback that failed the
// state check.
func (att *Attempt) Await(ctx context.Context) (*CallbackResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timer := time.NewTimer(time.Until(att.pending.deadline))
	defer timer.Stop()
	select {
	case outcome := <-att.listener.outcomes:
		if outcome.err != nil {
			return nil, outcome.err
		}
		return outcome.result, nil
	case <-att.listener.expired:
		return nil, ErrAttemptExpired
	case <-att.listener.closed:
		// The listener may have been closed by the expiry timer, and the
		// expiry is closed first, so the check is deterministic. A result
		// that was delivered before the close still counts: the close
		// alone does not discard it.
		select {
		case outcome := <-att.listener.outcomes:
			if outcome.err != nil {
				return nil, outcome.err
			}
			return outcome.result, nil
		default:
		}
		select {
		case <-att.listener.expired:
			return nil, ErrAttemptExpired
		default:
		}
		return nil, ErrAttemptClosed
	case <-ctx.Done():
		return nil, fmt.Errorf("siwc: the authorization attempt was cancelled: %w", ctx.Err())
	case <-timer.C:
		return nil, ErrAttemptExpired
	}
}

// pendingSecrets returns every value of the pending authorization that must
// not reach an operator- or support-facing error.
//
// The set is COMPLETE on purpose. The state, the nonce and the verifier
// belong to the attempt; the retained identity hints belong to the account
// and are still credential-bearing even when they are expired; the issued
// client id names the registration; and the authorization code is added by
// the caller that holds it.
//
// The issued client id is part of the set as well, and the reason is the
// POLICY, not the classification. The issued client id is a public protocol
// identifier: it names the registration, it travels in the authorization
// request in clear text, and it is not a bearer, refresh or authorization
// code credential. The policy of this package is ONE policy in ONE
// direction - a value the connector redacts from PROVIDER-SUPPLIED TEXT is
// redacted from it - and the issued client id is such a value: a provider
// that echoes it into an error description must not be able to put it into
// an operator-facing error.
//
// The SCOPE of the policy is the provider's text and the connector's own
// diagnostics. The issued client id still travels as a PROTOCOL PARAMETER
// where the protocol requires it: the authorization request carries it, and
// the manual URL the operator opens carries it. That is the provider's
// parameter and not a diagnostic, and it is the same kind of exposure as
// the launcher's arguments.
//
// The set is derived from the pending values AT THE POINT OF USE, and not
// pre-computed and stored: a value that arrives later - the provider's own
// callback code, for example - is not in the attempt, and an enumeration
// that is built once can never be complete for the values that arrive
// after it.
//
// A secret that is missing from the set cannot be removed from a provider's
// text, so the set is built in ONE place and every rendering path uses it.
func pendingSecrets(pending pendingAuthorization) []string {
	return []string{
		pending.state,
		pending.nonce,
		pending.verifier,
		pending.idTokenHint,
		pending.loginHint,
		pending.issuedClientID,
	}
}

// Close releases the attempt's listener.
//
// It is safe to call more than once, and it is safe to call after the
// attempt produced its result.
func (att *Attempt) Close() error {
	if att == nil || att.listener == nil {
		return nil
	}
	return att.listener.close()
}

// resolveIssuedClientID determines the client id the exchange must use.
//
// The rules are the documented ones. A first registration needs the issued
// client id the callback carried, and a registration whose callback carries
// none is incomplete. A returning authorization keeps the client id of the
// pending request when the callback omits it, and it rejects a callback that
// carries a different one: replacing the registration of the selected
// account with another registration's client id would bind the wrong
// credential to the wrong account.
func (att *Attempt) resolveIssuedClientID(callbackIssued string) (string, error) {
	issued := strings.TrimSpace(callbackIssued)
	if att.pending.kind == AttemptRegistration {
		if issued == "" {
			return "", ErrRegistrationIncomplete
		}
		if issued == DynamicAgentClientID {
			return "", fmt.Errorf(
				"%w: the callback carried the dynamic registration entry point instead of an issued client id",
				ErrRegistrationIncomplete,
			)
		}
		return issued, nil
	}
	expected := att.pending.issuedClientID
	if issued == "" {
		return expected, nil
	}
	if issued != expected {
		return "", ErrIssuedClientIDMismatch
	}
	return issued, nil
}

// authorizationURL builds the URL the browser opens.
//
// The parameter set is the documented one. A first registration carries the
// dynamic entry point, the product's agent name and the host identity. A
// returning authorization carries the saved issued client id, omits the
// agent name, and may carry the retained identity hints of the selected
// account.
func (att *Attempt) authorizationURL() string {
	return att.authorizer.buildAuthorizationURL(att.authorizationValues(true))
}

// fallbackAuthorizationURL builds the URL an operator opens BY HAND.
//
// The retained hints are OMITTED rather than redacted in place. The hints
// are optional, and a URL that carries a literal redaction marker where the
// ID token belongs would be refused by the provider: the operator has to be
// able to open the URL as it is.
//
// The URL therefore names the account by nothing at all, and the account is
// identified by the operator's own choice in the provider's account
// selector.
func (att *Attempt) fallbackAuthorizationURL() string {
	return att.authorizer.buildAuthorizationURL(att.authorizationValues(false))
}

// authorizationValues builds the query of the authorization request.
//
// The optional retained hints are included only when the request really
// carries them, and only for the request the provider receives.
func (att *Attempt) authorizationValues(includeHints bool) url.Values {
	pending := att.pending
	values := url.Values{}
	if pending.kind == AttemptRegistration {
		values.Set("client_id", DynamicAgentClientID)
		values.Set("agent_name_hint", att.agentName())
	} else {
		values.Set("client_id", pending.issuedClientID)
		if includeHints {
			if pending.idTokenHint != "" {
				values.Set("id_token_hint", pending.idTokenHint)
			}
			if pending.loginHint != "" {
				values.Set("login_hint", pending.loginHint)
			}
		}
	}
	values.Set("ext_agent_host_id", pending.hostID)
	values.Set("response_type", "code")
	values.Set("redirect_uri", pending.redirectURI)
	values.Set("scope", AuthorizationScope)
	values.Set("resource", PlanResource)
	values.Set("state", pending.state)
	values.Set("nonce", pending.nonce)
	values.Set("code_challenge_method", "S256")
	values.Set("code_challenge", pending.challenge)
	return values
}

// agentName returns the product's agent name for this attempt.
func (att *Attempt) agentName() string {
	if att.authorizer == nil {
		return DefaultAgentName
	}
	return att.authorizer.config.AgentName
}

// ReauthorizationOptions is the input of a returning authorization.
type ReauthorizationOptions struct {
	// HostID is THIS host's stable agent-host identity.
	//
	// The identity is the host's own, never a copy of the one a saved
	// registration happens to carry: a registration that was created on
	// another host describes that host, and the request this host sends
	// has to describe this host.
	HostID string
	// IssuedClientID is the saved issued client id of the registration.
	IssuedClientID string
	// IDTokenHint is the ID token retained from the previous sign-in. It
	// may be expired: it identifies the account, it is not a session.
	IDTokenHint string
	// LoginHint is the email hint saved from the validated ID token of the
	// same account.
	LoginHint string
}

// BeginRegistration starts a first registration attempt.
//
// The attempt carries the dynamic registration entry point, the product's
// agent name and the host's stable identity. The loopback listener is
// started before the browser is opened, and the selected callback URI is
// fixed for the lifetime of the attempt.
func (a *Authorizer) BeginRegistration(ctx context.Context, hostID string) (*Attempt, error) {
	return a.begin(ctx, AttemptRegistration, ReauthorizationOptions{HostID: hostID})
}

// BeginReauthorization starts a returning authorization attempt.
//
// The attempt carries the saved issued client id, omits the agent name and
// may carry the retained identity hints of the selected account. The
// callback listener is started before the browser is opened, and the
// selected callback URI is fixed for the lifetime of the attempt.
func (a *Authorizer) BeginReauthorization(ctx context.Context, opts ReauthorizationOptions) (*Attempt, error) {
	return a.begin(ctx, AttemptReauthorization, opts)
}

// begin prepares one pending attempt.
//
// The attempt's values are fresh: the state, the nonce and the PKCE
// verifier are generated per attempt, and the callback URI is selected per
// attempt. Nothing is carried over from an earlier attempt except the
// values that identify the account and the host.
func (a *Authorizer) begin(_ context.Context, kind AttemptKind, opts ReauthorizationOptions) (*Attempt, error) {
	hostID := strings.TrimSpace(opts.HostID)
	if hostID == "" {
		return nil, errors.New("siwc: the authorization attempt needs the host identity")
	}
	if kind == AttemptReauthorization {
		if strings.TrimSpace(opts.IssuedClientID) == "" {
			return nil, errors.New("siwc: a returning authorization needs the saved issued client id")
		}
		if opts.IssuedClientID == DynamicAgentClientID {
			return nil, fmt.Errorf(
				"siwc: %s is a registration entry point, not an issued client id",
				DynamicAgentClientID,
			)
		}
	}
	verifier, challenge, state, err := oauthcred.GeneratePKCE()
	if err != nil {
		return nil, err
	}
	nonce, err := newNonce()
	if err != nil {
		return nil, err
	}
	pending := pendingAuthorization{
		kind:           kind,
		state:          state,
		nonce:          nonce,
		verifier:       verifier,
		challenge:      challenge,
		hostID:         hostID,
		issuedClientID: strings.TrimSpace(opts.IssuedClientID),
		idTokenHint:    strings.TrimSpace(opts.IDTokenHint),
		loginHint:      strings.TrimSpace(opts.LoginHint),
		deadline:       time.Now().Add(a.config.AttemptTTL),
	}
	// The listener renders provider-supplied text in its own responses, so
	// it needs the attempt's COMPLETE pending values: the state, the nonce,
	// the verifier and the retained identity hints. A retained hint is
	// credential-bearing even when it is expired.
	//
	// The values are passed AT CONSTRUCTION, before the listener serves
	// its first request. The listener learns nothing about the attempt
	// afterwards, so the serving goroutine never reads a field another
	// goroutine writes.
	listener, err := newCallbackListener(pending.state, a.config.AttemptTTL, pendingSecrets(pending)...)
	if err != nil {
		return nil, err
	}
	// The callback URI is the one the listener really serves, and it is
	// filled in before the attempt exists: the pending value is the
	// attempt's own, and the exchange carries it byte for byte.
	pending.redirectURI = listener.redirectURI()
	attempt := &Attempt{
		pending:  pending,
		listener: listener,
	}
	attempt.authorizer = a
	return attempt, nil
}

// buildAuthorizationURL renders the query against the configured endpoint.
//
// The values are URL-encoded by the query encoder, and the endpoint's own
// path is preserved.
func (a *Authorizer) buildAuthorizationURL(values url.Values) string {
	endpoint := a.config.AuthorizationEndpoint
	parsed, err := url.Parse(endpoint)
	if err != nil {
		// A malformed endpoint is a configuration error that the
		// constructor cannot see, because the endpoint may also come from
		// the configuration. The raw endpoint is returned rather than a
		// fabricated one, so the failure stays visible in the URL the
		// operator sees.
		return endpoint + "?" + values.Encode()
	}
	parsed.RawQuery = values.Encode()
	return parsed.String()
}

// Exchange swaps the authorization code for the token set.
//
// The exchange is a form-encoded authorization-code grant to the documented
// token endpoint. It carries the issued client id, the code, the PKCE
// verifier, the resource and the EXACT callback URI the authorization
// request carried.
//
// The callback URI is read from the pending attempt. It is never rebuilt
// from the listener's current state and never substituted: the provider
// matches the redirect URI of the exchange against the one of the
// authorization request, and a URI that differs in scheme, host, port or
// path is a different URI.
//
// The exchange is BOUNDED by the attempt's own deadline, independently of
// the HTTP client the caller supplied: a client without a timeout must not
// be able to hold the exchange open past the attempt, and the deadline
// covers the response body read as well as the request.
//
// No client secret is sent: the flow is a public-client flow.
func (a *Authorizer) Exchange(ctx context.Context, attempt *Attempt, issuedClientID, code string) (TokenResponse, error) {
	if attempt == nil {
		return TokenResponse{}, errors.New("siwc: the code exchange needs a pending attempt")
	}
	clientID := strings.TrimSpace(issuedClientID)
	if clientID == "" {
		return TokenResponse{}, errors.New("siwc: the code exchange needs the issued client id")
	}
	if clientID == DynamicAgentClientID {
		return TokenResponse{}, fmt.Errorf(
			"siwc: %s is a registration entry point, not an issued client id; the exchange must carry the issued id",
			DynamicAgentClientID,
		)
	}
	authorizationCode := strings.TrimSpace(code)
	if authorizationCode == "" {
		return TokenResponse{}, errors.New("siwc: the code exchange needs the authorization code")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// The attempt's deadline bounds the exchange, whatever client the
	// caller supplied. A client without a timeout would otherwise hold the
	// request - or the response body read - open indefinitely.
	exchangeCtx, cancel := context.WithDeadline(ctx, attempt.pending.deadline)
	defer cancel()

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", clientID)
	form.Set("code", authorizationCode)
	form.Set("code_verifier", attempt.pending.verifier)
	form.Set("redirect_uri", attempt.pending.redirectURI)
	form.Set("resource", PlanResource)

	request, err := http.NewRequestWithContext(exchangeCtx, http.MethodPost, a.config.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return TokenResponse{}, &TransportError{Class: "request", cause: classifyExchangeCause(err)}
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := a.exchangeClient().Do(request)
	if err != nil {
		return TokenResponse{}, &TransportError{Class: "transport", cause: classifyExchangeCause(err)}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxTokenResponseBytes))
		_ = response.Body.Close()
	}()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxTokenResponseBytes+1))
	if err != nil {
		return TokenResponse{}, &TransportError{
			Class:      "body",
			StatusCode: response.StatusCode,
			cause:      classifyExchangeCause(err),
		}
	}
	if len(body) > maxTokenResponseBytes {
		// The response is longer than the bound. It is refused BEFORE it
		// is decoded: a bounded PREFIX of an oversized response can be a
		// complete and valid document, and decoding that prefix would
		// accept a response the connector never read whole.
		//
		// The failure is an unclassified provider error: the response
		// carries no code the connector may report.
		return TokenResponse{}, &TokenError{
			StatusCode: response.StatusCode,
			Code:       "provider_error",
		}
	}
	// ACCEPTED RESIDUAL - the response headers are never read. The
	// exchange reads the status and the body, and nothing else: a header
	// is provider metadata, and the one header that could carry request
	// material - the Location of a redirect - is refused by class below
	// without its value ever being rendered. The rule is stated here so
	// that the next reader does not re-open it as an unknown: a header
	// the connector does not read cannot leak through it.
	if response.StatusCode != http.StatusOK {
		return TokenResponse{}, a.tokenError(
			response.StatusCode,
			body,
			append(pendingSecrets(attempt.pending), authorizationCode, clientID)...,
		)
	}
	// The response is read by EXACT member name. Go's typed JSON decoder
	// matches a field's name case-insensitively, so a response that
	// carries both "scope" and "SCOPE" would decide by itself which
	// granted scope set the plan-usage check reads, and a response that
	// carries both "id_token" and "ID_TOKEN" would decide which token is
	// validated. The members are read exactly instead.
	tokens, err := decodeTokenResponse(body)
	if err != nil {
		return TokenResponse{}, errors.New("siwc: the token endpoint returned a response this build cannot read")
	}
	// ACCEPTED RESIDUAL - the repeated JSON key rule. A body that repeats
	// a key keeps the LAST value of that key: the members are decoded
	// into a map, and a map keeps the last value of a repeated name. A
	// provider that answers the token endpoint with a repeated key
	// therefore decides which value is read, and the connector does not
	// refuse the body. The rule is stated here so that the next reader
	// does not re-open it as an unknown: the token endpoint is a
	// documented JSON document, and a body that repeats a key is
	// malformed rather than a shape this build has to survive. The CASE
	// of a member name is NOT the same rule: a member that differs only
	// in case is a different member, and it never overwrites the one the
	// check reads.
	if strings.TrimSpace(tokens.AccessToken) == "" {
		return TokenResponse{}, errors.New("siwc: the token endpoint returned no access token")
	}
	return tokens, nil
}

// decodeTokenResponse reads the token endpoint's response by exact member
// name.
//
// The members are the DOCUMENT's members: a member whose name differs only
// in case is a different member, and it never overwrites the one whose
// name the connector asks for. A member the connector does not recognize
// is carried and ignored, so an extension field is never collateral of
// the rule.
//
// PRESENCE IS SEPARATE FROM VALIDITY. Every member the response carries
// must be readable as the shape it carries: a member that ARRIVED with a
// shape the connector cannot read makes the response UNREADABLE, and the
// exchange is refused with a value-free error. It is never read as though
// the response had not carried it.
//
// That rule is what keeps a granted scope from being lost. An unreadable
// scope is not an absent scope: an absent scope falls back to the
// callback's scope, and a fallback that silently substitutes one grant
// for another would authorize a plan usage that no successful token
// response granted.
//
// The failure is a CONSTANT: the response carries credential material, and
// a diagnostic that repeated a part of it would put that material into an
// operator- or support-facing error.
func decodeTokenResponse(body []byte) (TokenResponse, error) {
	members, err := jsonMembers(body)
	if err != nil {
		return TokenResponse{}, errMalformedDocument
	}
	tokens := TokenResponse{}
	if tokens.AccessToken, err = documentString(members, "access_token"); err != nil {
		return TokenResponse{}, err
	}
	if tokens.RefreshToken, err = documentString(members, "refresh_token"); err != nil {
		return TokenResponse{}, err
	}
	if tokens.IDToken, err = documentString(members, "id_token"); err != nil {
		return TokenResponse{}, err
	}
	if tokens.TokenType, err = documentString(members, "token_type"); err != nil {
		return TokenResponse{}, err
	}
	if tokens.Scope, err = documentString(members, "scope"); err != nil {
		return TokenResponse{}, err
	}
	if tokens.ExpiresIn, err = documentNumber(members, "expires_in"); err != nil {
		return TokenResponse{}, err
	}
	if tokens.EarliestRefreshAt, err = documentNumber(members, "earliest_refresh_at"); err != nil {
		return TokenResponse{}, err
	}
	return tokens, nil
}

// ErrExchangeTransport is the classified cause of a transport failure.
//
// The transport's own message is never rendered: it can carry the URL the
// client was redirected to, and a redirect can carry the authorization code
// in its query.
var ErrExchangeTransport = errors.New("siwc: the code exchange transport failed")

// TransportError is a classified failure of the code exchange's transport.
//
// The class is preserved, because the recovery differs per class, and the
// cancellation and deadline classification is preserved through Unwrap: an
// operator who interrupted the sign-in and a provider that stalled must
// stay distinguishable. The underlying transport text is NOT rendered.
type TransportError struct {
	// Class names the failure: "request", "transport" or "body".
	Class string
	// StatusCode is the HTTP status, when the provider answered.
	StatusCode int
	// cause is the classified cause.
	cause error
}

// Error renders the classified failure.
func (e *TransportError) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("siwc: the code exchange %s failed (status %d)", e.Class, e.StatusCode)
	}
	return fmt.Sprintf("siwc: the code exchange %s failed", e.Class)
}

// Unwrap returns the classified cause.
func (e *TransportError) Unwrap() error { return e.cause }

// classifyExchangeCause maps a transport failure to a classified cause.
//
// A cancelled context and an expired deadline keep their classification.
// Everything else becomes the generic transport failure, because the
// transport's own text can carry a URL the provider chose - and that URL can
// carry the authorization code.
func classifyExchangeCause(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return ErrExchangeTransport
	}
}

// exchangeClient returns the client the exchange uses.
//
// The exchange MUST NOT follow a redirect. The authorization code is in the
// request, and a redirect would let the provider send it to a destination
// of its choosing; a redirect is therefore refused, and the Location the
// provider sent is never rendered.
//
// The caller's transport, timeout and cookie jar are the caller's: only
// the redirect policy is this connector's.
func (a *Authorizer) exchangeClient() *http.Client {
	caller := a.config.HTTPClient
	if caller == nil {
		caller = &http.Client{}
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

// tokenError classifies a failed exchange.
//
// The provider's error code is CLASSIFIED, and the provider's description is
// REDACTED before it is bounded. The order matters: a description that is
// truncated first can no longer be matched against the complete secret it
// carries, and a prefix of a credential would survive.
//
// A provider that answers the error field with free text - for example by
// echoing the authorization code it received - therefore never reaches an
// operator-facing error: the field is a code, and an undocumented value is
// reported as an unclassified provider error.
//
// A redirect is refused rather than reported: the provider's Location is
// never rendered.
func (a *Authorizer) tokenError(status int, body []byte, secrets ...string) *TokenError {
	if status >= 300 && status < 400 {
		return &TokenError{StatusCode: status, Code: "redirect_refused"}
	}
	var payload struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	code := ""
	description := ""
	// The same repeated-JSON-key rule as in the success path applies
	// here: a body that repeats a key keeps the LAST value of that key.
	// The rule is ACCEPTED and stated in Exchange, and the description is
	// redacted and bounded below in any case.
	if err := json.Unmarshal(body, &payload); err == nil {
		code = payload.Error
		description = payload.Description
	}
	return &TokenError{
		StatusCode:  status,
		Code:        classifyProviderErrorCode(code),
		Description: safeProviderText(description, maxErrorDescriptionLength, secrets...),
	}
}

// providerErrorCodes are the OAuth error codes the provider may report in
// the error field of a token response or a callback.
//
// The field is a CODE: it names a class of failure, and the recovery
// differs per code. A value outside this set is not a code, and it is not
// rendered: a body that answers a code field with free text may carry
// request material that the provider echoed back.
var providerErrorCodes = map[string]bool{
	"access_denied":             true,
	"consent_required":          true,
	"expired_token":             true,
	"interaction_required":      true,
	"invalid_client":            true,
	"invalid_grant":             true,
	"invalid_request":           true,
	"invalid_scope":             true,
	"invalid_token":             true,
	"login_required":            true,
	"server_error":              true,
	"temporarily_unavailable":   true,
	"unauthorized":              true,
	"unauthorized_client":       true,
	"unsupported_grant_type":    true,
	"unsupported_response_type": true,
	// The refresh codes. The documentation names them for the refresh
	// path, and they are codes of the same vocabulary: a code the
	// connector does not know would be reported as an unclassified
	// provider error, and the refresh would lose the distinction between
	// an unusable token and a refused request.
	"invalid_refresh_token":     true,
	"token_expired":             true,
	"refresh_token_expired":     true,
	"refresh_token_invalidated": true,
	"refresh_token_reused":      true,
}

// classifyProviderErrorCode classifies a provider-supplied error field.
//
// A documented code is kept, because the recovery differs per code. Any
// other value is reported as an unclassified provider error: the value is
// never rendered, because the provider's error field is a code and a body
// that answers it with free text may echo the request material.
func classifyProviderErrorCode(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "provider_error"
	}
	if providerErrorCodes[trimmed] {
		return trimmed
	}
	return "provider_error"
}

// safeProviderText prepares provider-supplied text for an operator- or
// support-facing error.
//
// The order is fixed: the known secrets are removed FIRST, and the result is
// bounded SECOND. A bound applied first would shorten a secret beyond
// recognition, and the prefix that survives the bound would then no longer
// match the complete secret.
//
// ACCEPTED RESIDUAL - the literal redaction. A removed secret is replaced
// by the literal marker "[REDACTED]", and the marker neither hides the
// LENGTH of the removed value nor the fact that a value was removed at
// all. The rule is stated here so that the next reader does not re-open it
// as an unknown: the marker is a fixed string that names no value, and the
// redaction removes the value itself, which is the property that matters.
//
// ACCEPTED RESIDUAL - the LITERAL match. The redaction removes a secret by
// matching it literally, so a TRANSFORMED echo of a secret is not
// recognized: a value the provider re-encoded, truncated, hashed or
// otherwise reshaped before echoing it does not match the complete secret
// and therefore survives the redaction. The limitation is ACCEPTED for this
// slice and is stated here so that the next reader does not re-open it as an
// unknown: the accidental echo - the provider that returns the value it was
// given - is the case the redaction covers, and a provider that deliberately
// reshapes a value is a different threat model.
//
// The widening of that rule belongs to the SHARED redaction helper
// (connector-support/oauthcred.Redact), and not to this connector: every
// connector that redacts provider-supplied text would need the same rule,
// and a change there is a change of the shared contract. It is DEFERRED,
// and this task does not widen it.
func safeProviderText(text string, limit int, secrets ...string) string {
	redacted := oauthcred.Redact(text, secrets...)
	return boundText(redacted, limit)
}

// attemptOutcome is what one attempt produced.
//
// It carries the issued client id even when the exchange failed, because a
// rejected code is discarded while the issued client id is retained for the
// fresh attempt that follows.
type attemptOutcome struct {
	issuedClientID string
	tokens         TokenResponse
	scope          string
	nonce          string
}

// runAttempt performs one attempt: it opens the browser, waits for the
// callback, resolves the issued client id and exchanges the code.
//
// The manual path is UNCONDITIONAL. A detached launcher's outcome is
// unobservable BY DESIGN: the launcher is started by a shell that
// backgrounds it and exits, so the command learns that the shell exited,
// never that a browser opened the URL. "The launch succeeded" therefore
// cannot be confirmed, and the only way to keep the operator from waiting
// for a browser that never appeared is to ALWAYS hand them a usable URL.
//
// The URL is the HINT-FREE one. The retained hints identify the account,
// and the URL is written to the operator's terminal. The URL carries only
// the attempt's own values - the state, the nonce and the PKCE challenge -
// which are single-use and bounded by the attempt's lifetime. It carries no
// token, no hint and no code.
func (a *Authorizer) runAttempt(ctx context.Context, attempt *Attempt) (attemptOutcome, error) {
	if err := a.openBrowser(ctx, attempt); err != nil {
		a.notify(
			"the browser could not be opened automatically; open this URL to continue the sign-in: " +
				attempt.fallbackAuthorizationURL(),
		)
	} else {
		a.notify(
			"if the browser did not open, open this URL to continue the sign-in: " +
				attempt.fallbackAuthorizationURL(),
		)
	}
	callback, err := attempt.Await(ctx)
	if err != nil {
		return attemptOutcome{}, err
	}
	issuedClientID, err := attempt.resolveIssuedClientID(callback.IssuedClientID)
	if err != nil {
		return attemptOutcome{}, err
	}
	tokens, err := a.Exchange(ctx, attempt, issuedClientID, callback.Code)
	outcome := attemptOutcome{
		issuedClientID: issuedClientID,
		tokens:         tokens,
		scope:          callback.Scope,
		nonce:          attempt.pending.nonce,
	}
	if err != nil {
		return outcome, err
	}
	return outcome, nil
}

// openBrowser opens the authorization URL in the operator's browser.
//
// A browser that cannot be opened is not a failure of the attempt: the
// operator can open the URL by hand, and the URL is reported for exactly
// that case.
func (a *Authorizer) openBrowser(ctx context.Context, attempt *Attempt) error {
	if a.config.BrowserOpener == nil {
		return errors.New("siwc: no browser opener is configured")
	}
	return a.config.BrowserOpener(ctx, attempt.AuthorizationURL())
}

// notify reports a non-fatal event to the operator.
func (a *Authorizer) notify(message string) {
	if a.config.Notice == nil {
		return
	}
	a.config.Notice(message)
}

// now returns the current time through the configured clock.
func (a *Authorizer) now() time.Time { return a.config.Now() }

// RegisterRequest is the input of a first registration.
type RegisterRequest struct {
	// ProfileID is the local id of the new registration. Empty means a
	// fresh id is generated.
	ProfileID string
	// Label is the operator-facing label of the registration.
	Label string
}

// Register runs the complete first registration.
//
// The flow creates or loads the stable host identity, starts the callback
// listener, opens the browser, waits for the callback, exchanges the code,
// validates the ID token and persists the registration.
//
// A rejected authorization code is discarded and the flow starts again with
// the issued client id retained: the registration itself already exists at
// that point, so the fresh attempt is a returning authorization.
//
// # The caller-reporting policy
//
// The PRE-ANSWER work is the caller's: the browser wait and the code
// exchange run under the caller's context, and a cancellation there
// withdraws the sign-in. The provider was not asked, nothing was
// committed, and the call reports the caller's own cancellation.
//
// The POST-ANSWER work is the connector's: the provider has issued a
// complete credential set, and the validation of it runs under an OWNED,
// bounded reconciliation - the persistence of it is a required commit, and
// the store bounds that on its own. A cancellation that
// arrives after the answer does NOT withdraw the sign-in: the credential
// set is validated, persisted, and the call reports SUCCESS. The
// reconciliation may hold the caller up to the reconciliation budget
// (30 s) in the rare contention case, and that bound is deliberately not
// the caller's own, already expired, deadline.
//
// A credential set that does not validate is never persisted: the
// validation is not weakened by the reconciliation, and a failed
// validation publishes nothing.
func (a *Authorizer) Register(ctx context.Context, req RegisterRequest) (ProfileRecord, error) {
	hostID, err := a.config.Hosts.EnsureHostID()
	if err != nil {
		return ProfileRecord{}, err
	}
	profileID := strings.TrimSpace(req.ProfileID)
	if profileID == "" {
		profileID, err = NewProfileID()
		if err != nil {
			return ProfileRecord{}, err
		}
	} else if _, loadErr := a.config.Profiles.Load(profileID); loadErr == nil {
		// A registration must never take over a saved registration: the
		// documentation forbids overwriting one registration's credentials
		// with another account's. A returning sign-in is the operation
		// that replaces the credentials of a saved registration.
		return ProfileRecord{}, fmt.Errorf(
			"siwc: a registration with the id %q already exists; reauthorize it instead of registering over it",
			profileID,
		)
	} else if !errors.Is(loadErr, fs.ErrNotExist) {
		return ProfileRecord{}, loadErr
	}
	issuedClientID := ""
	kind := AttemptRegistration
	var lastErr error
	for range maxAuthorizationAttempts {
		attempt, err := a.begin(ctx, kind, ReauthorizationOptions{
			HostID:         hostID,
			IssuedClientID: issuedClientID,
		})
		if err != nil {
			return ProfileRecord{}, err
		}
		outcome, err := a.runAttempt(ctx, attempt)
		_ = attempt.Close()
		if err != nil {
			if errors.Is(err, ErrInvalidGrant) && outcome.issuedClientID != "" {
				// The code is discarded. The issued client id is retained
				// for the fresh attempt, which is a returning
				// authorization: the registration already exists.
				issuedClientID = outcome.issuedClientID
				kind = AttemptReauthorization
				lastErr = err
				continue
			}
			return ProfileRecord{}, err
		}
		identity, err := a.validateAfterReceipt(ctx, outcome)
		if err != nil {
			return ProfileRecord{}, classifyValidatorError(err)
		}
		scopes := grantedScopes(outcome)
		record := ProfileRecord{
			ID:             profileID,
			Label:          strings.TrimSpace(req.Label),
			Subject:        identity.Subject,
			EmailHint:      identity.EmailHint,
			IssuedClientID: outcome.issuedClientID,
			HostID:         hostID,
			GrantedScopes:  scopes,
			AccessToken:    outcome.tokens.AccessToken,
			RefreshToken:   outcome.tokens.RefreshToken,
			IDToken:        outcome.tokens.IDToken,
			Expiry:         a.expiry(outcome.tokens),
			NeedsReauth:    false,
		}
		// The publication is ATOMIC and create-if-absent. Two overlapping
		// registrations of the same local id therefore cannot destroy each
		// other: the second one is refused, and the record of the first
		// one is never replaced. A pre-check alone would not hold: the
		// other registration can be published between the check and the
		// write.
		if err := a.config.Profiles.SaveNew(record); err != nil {
			return ProfileRecord{}, err
		}
		// The plan-usage outcome is reported AFTER the publication, so
		// that the notice states a persisted fact.
		a.reportPlanUsage(record.GrantedScopes)
		return record, nil
	}
	return ProfileRecord{}, fmt.Errorf(
		"siwc: the registration did not complete after %d attempts: %w",
		maxAuthorizationAttempts,
		lastErr,
	)
}

// ReauthorizeRequest is the input of a returning authorization.
type ReauthorizeRequest struct {
	// ProfileID identifies the saved registration to reauthorize.
	ProfileID string
	// Label optionally replaces the operator-facing label.
	Label string
}

// Reauthorize runs the complete returning authorization.
//
// The flow reads the saved registration, reuses its issued client id and
// its retained identity hints, and confirms that the authorized account is
// the selected one BEFORE it replaces the credentials.
//
// The host identity comes from the HOST, and not from the saved
// registration. A registration that was copied from another host carries
// that host's identity, and a request that this host sends has to describe
// this host: the identity is the host store's to decide, and the store is
// the authority for it.
//
// A rejected authorization code is discarded and the flow starts again with
// the issued client id retained.
//
// # The caller-reporting policy
//
// The PRE-ANSWER work is the caller's: the browser wait and the code
// exchange run under the caller's context, and a cancellation there
// withdraws the sign-in. The provider was not asked, nothing was
// committed, and the call reports the caller's own cancellation.
//
// The POST-ANSWER work is the connector's: the provider has issued a
// complete credential set, and the validation of it runs under an OWNED,
// bounded reconciliation - the publication of it is a required commit, and
// the store bounds that on its own. A cancellation that
// arrives after the answer does NOT withdraw the sign-in: the credential
// set is validated, published, and the call reports SUCCESS. The
// reconciliation may hold the caller up to the reconciliation budget
// (30 s) in the rare contention case, and that bound is deliberately not
// the caller's own, already expired, deadline.
//
// A credential set that does not validate is never published, and a
// credential set of a DIFFERENT account is never published over the
// selected registration: the subject is confirmed before the publication,
// and the validation is not weakened by the reconciliation.
func (a *Authorizer) Reauthorize(ctx context.Context, req ReauthorizeRequest) (ProfileRecord, error) {
	profileID := strings.TrimSpace(req.ProfileID)
	if profileID == "" {
		return ProfileRecord{}, errors.New("siwc: a returning authorization needs the profile id of the selected registration")
	}
	existing, err := a.config.Profiles.Load(profileID)
	if err != nil {
		return ProfileRecord{}, err
	}
	issuedClientID := existing.IssuedClientID
	if issuedClientID == "" {
		return ProfileRecord{}, errors.New("siwc: the selected registration carries no issued client id")
	}
	// The host identity is resolved from the AUTHORITY before the attempt
	// loop, so that every attempt of this sign-in carries the same
	// identity, and a failure to resolve it stops the sign-in before the
	// operator is sent to the browser.
	hostID, err := a.config.Hosts.EnsureHostID()
	if err != nil {
		return ProfileRecord{}, err
	}
	// The registration's recorded host provenance is reconciled to this
	// host's identity, and the reconciliation is REPORTED.
	//
	// The notice BEFORE the attempt reports the DECISION: the
	// registration names another host, and this authorization uses THIS
	// host's identity. The decision is a fact before the sign-in, and the
	// operator has to learn it before the browser opens.
	//
	// The notice AFTER the successful save reports the PERSISTED FACT.
	// A failed sign-in leaves the stored host id unchanged, and a notice
	// that claims a reconciliation before the record carries it would be
	// a claim the store does not support.
	resolvedHostID, reconciled := ReconcileProfileHost(hostID, existing.HostID)
	if reconciled {
		a.notify(
			"the registration was created on another host; this authorization uses this host's identity",
		)
	}
	var lastErr error
	for range maxAuthorizationAttempts {
		attempt, err := a.begin(ctx, AttemptReauthorization, ReauthorizationOptions{
			HostID:         resolvedHostID,
			IssuedClientID: issuedClientID,
			IDTokenHint:    existing.IDToken,
			LoginHint:      existing.EmailHint,
		})
		if err != nil {
			return ProfileRecord{}, err
		}
		outcome, err := a.runAttempt(ctx, attempt)
		_ = attempt.Close()
		if err != nil {
			if errors.Is(err, ErrInvalidGrant) {
				// The code is discarded; the issued client id is retained
				// for the fresh attempt.
				lastErr = err
				continue
			}
			return ProfileRecord{}, err
		}
		identity, err := a.validateAfterReceipt(ctx, outcome)
		if err != nil {
			return ProfileRecord{}, classifyValidatorError(err)
		}
		// The subject is the authoritative identity of the registration.
		// It is confirmed BEFORE the credentials are replaced, so a
		// sign-in of a different account cannot take over the selected
		// registration.
		if err := confirmSubject(existing.Subject, identity); err != nil {
			return ProfileRecord{}, err
		}
		scopes := grantedScopes(outcome)
		updated, err := a.config.Profiles.Publish(
			existing.ID, func(current ProfileRecord) (ProfileRecord, error) {
				// The change is applied to the record that is stored
				// NOW. The sign-in holds a fresh token set, and it is
				// applied onto the state that is there: a credential
				// that a concurrent refresh already rotated is never put
				// back, and a reauthorization that completed while this
				// one waited for the browser is not undone.
				merged := current
				if label := strings.TrimSpace(req.Label); label != "" {
					merged.Label = label
				}
				merged.Subject = identity.Subject
				if identity.EmailHint != "" {
					merged.EmailHint = identity.EmailHint
				}
				merged.IssuedClientID = issuedClientID
				// The saved record names THIS host, not the host the
				// registration was created on.
				merged.HostID = resolvedHostID
				merged.GrantedScopes = scopes
				merged.AccessToken = outcome.tokens.AccessToken
				merged.RefreshToken = outcome.tokens.RefreshToken
				merged.IDToken = outcome.tokens.IDToken
				merged.Expiry = a.expiry(outcome.tokens)
				merged.NeedsReauth = false
				return merged, nil
			},
		)
		if err != nil {
			return ProfileRecord{}, err
		}
		// The plan-usage outcome is reported AFTER the publication, so
		// that the notice states a persisted fact.
		a.reportPlanUsage(updated.GrantedScopes)
		if reconciled {
			// The record carries this host's identity NOW, and only a
			// successful save makes that true. The notice reports the
			// persisted fact, and it is emitted after the save for that
			// reason.
			a.notify(
				"the registration's recorded host provenance was reconciled to this host's identity",
			)
		}
		return updated, nil
	}
	return ProfileRecord{}, fmt.Errorf(
		"siwc: the returning authorization did not complete after %d attempts: %w",
		maxAuthorizationAttempts,
		lastErr,
	)
}

// expiry returns the expiry of a token set.
func (a *Authorizer) expiry(tokens TokenResponse) time.Time {
	if tokens.ExpiresIn <= 0 {
		return time.Time{}
	}
	return a.now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
}

// grantedScopes returns the scopes the provider granted.
//
// The token response's scope is authoritative; the callback's scope is the
// fallback for a provider that only reports it there. The scopes are
// RECORDED here and JUDGED by the plan-usage check.
//
// The scope string is a SET: a scope that appears twice is one scope, and
// the set holds it once.
func grantedScopes(outcome attemptOutcome) []string {
	raw := strings.TrimSpace(outcome.tokens.Scope)
	if raw == "" {
		raw = strings.TrimSpace(outcome.scope)
	}
	return parseScopeSet(raw)
}

// reportPlanUsage reports the plan-usage outcome of a completed sign-in.
//
// The granted scopes decide whether the registration may use the ChatGPT
// plan, and the documentation requires the sign-in to be RETAINED when the
// provider granted the identity scopes without the direct plan-use scope:
// the account IS authenticated, the registration is kept, and only the
// plan usage is disabled.
//
// The outcome is therefore a NOTICE and not a failure. The scopes
// themselves are persisted with the registration, and the registration's
// AllowsPlanUsage reports the plan usage to whoever uses it later.
//
// The notice names the scope that is missing and the two ways forward. It
// names no token, no scope of the account and no identifier of it.
func (a *Authorizer) reportPlanUsage(scopes []string) {
	if err := CheckDirectPlanUsage(scopes); err == nil {
		return
	}
	a.notify(
		"the sign-in is retained, but ChatGPT plan usage is disabled for this registration: " +
			"the granted scopes do not include " + PlanUseScope + ". " +
			"Enable ChatGPT plan usage, or configure another supported billing path such as your own API key.",
	)
}

// newNonce returns a fresh OIDC nonce.
//
// The nonce is bound to one attempt and is checked against the ID token's
// nonce claim. It is a random value with no structure: it carries no
// identity and no state of its own.
func newNonce() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("siwc: generate the authorization nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// boundText shortens a text to a maximum number of characters.
//
// The bound is applied to the characters, not to the bytes, so a truncated
// text stays valid text.
func boundText(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "..."
}

// redactAuthorizationURL replaces the retained hints of an authorization URL
// with a placeholder.
//
// The hints identify the account: the ID token names the account and the
// email names the person, and neither belongs in a log or a diagnostic.
// Both are therefore redacted, not only the ID token.
//
// The rest of the URL is preserved byte for byte, so the redaction never
// hides a parameter that is not secret. The redacted URL is for DISPLAY
// only: a URL that carries a redaction marker where the ID token belongs
// would be refused by the provider, so the URL an operator opens by hand is
// the hint-free one instead.
func redactAuthorizationURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parts := strings.Split(parsed.RawQuery, "&")
	changed := false
	for index, part := range parts {
		key, _, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		decoded, err := url.QueryUnescape(key)
		if err != nil {
			continue
		}
		if decoded != "id_token_hint" && decoded != "login_hint" {
			continue
		}
		parts[index] = key + "=" + url.QueryEscape("[REDACTED]")
		changed = true
	}
	if !changed {
		return raw
	}
	redacted := *parsed
	redacted.RawQuery = strings.Join(parts, "&")
	return redacted.String()
}
