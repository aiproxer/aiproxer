package siwc

import (
	"context"
	"errors"
	"io/fs"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/oauthcred"
)

// The authorization flow of the SIWC connector.
//
// The tests drive the real flow against a fake authorization server: the
// browser is simulated, the callback travels a real HTTP round trip to the
// loopback listener, and the code exchange travels a real HTTP round trip to
// the token endpoint. Nothing about the wire is stubbed, because the two
// properties this task is about - the exact parameters and the exact reuse
// of the callback URI - are wire properties.

// fakeValidator is the ID-token validator of the tests.
//
// It stands in for the JWKS validation of the OIDC phase: the flow depends
// on the validated identity, and the tests provide one.
//
// The validator RECORDS the expectation it received, so that a test can
// prove that the flow supplied the attempt's nonce and the issued client
// id the token must be addressed to.
type fakeValidator struct {
	mu          sync.Mutex
	identity    Identity
	err         error
	nonces      []string
	expectation []TokenExpectation
}

// ValidateIDToken returns the configured identity and records the
// expectation.
func (v *fakeValidator) ValidateIDToken(
	_ context.Context,
	_ TokenResponse,
	expected TokenExpectation,
) (Identity, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.nonces = append(v.nonces, expected.Nonce)
	v.expectation = append(v.expectation, expected)
	if v.err != nil {
		return Identity{}, v.err
	}
	return v.identity, nil
}

// expectations returns the expectations the validator received.
func (v *fakeValidator) expectations() []TokenExpectation {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]TokenExpectation(nil), v.expectation...)
}

// setIdentity replaces the identity the validator returns.
func (v *fakeValidator) setIdentity(identity Identity) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.identity = identity
}

// setError makes the validator fail.
//
// The failure may carry anything: the flow classifies it and never renders
// it, so a validator that reports the token in its own error cannot put
// the token into an operator-facing error.
func (v *fakeValidator) setError(err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.err = err
}

// harness is the wiring of one test: the fake server, the protected stores
// and the authorization service.
type harness struct {
	authorizer *Authorizer
	profiles   *ProfileStore
	hosts      *HostStore
	fake       *fakeAuthServer
	validator  *fakeValidator

	mu        sync.Mutex
	delivery  callbackDelivery
	noBrowser bool
	notices   []string
	browser   sync.WaitGroup
	// deliveryError is the failure of the last simulated browser delivery.
	deliveryError string
}

// newHarness wires an authorization service against the fake server and
// temporary stores.
func newHarness(t *testing.T, options ...func(*AuthorizerConfig)) *harness {
	t.Helper()
	fake := newFakeAuthServer(t)
	directory := t.TempDir()
	hosts, err := NewHostStore(filepath.Join(directory, "state", "host.json"))
	if err != nil {
		t.Fatalf("NewHostStore: %v", err)
	}
	profiles, err := NewProfileStore(filepath.Join(directory, "state", "profiles"))
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	validator := &fakeValidator{identity: Identity{Subject: "subject-1", EmailHint: "user@example.com"}}
	h := &harness{
		fake:      fake,
		profiles:  profiles,
		hosts:     hosts,
		validator: validator,
	}
	config := AuthorizerConfig{
		AuthorizationEndpoint: fake.server.URL + "/api/accounts/authorize",
		TokenEndpoint:         fake.server.URL + "/api/accounts/oauth/token",
		HTTPClient:            fake.server.Client(),
		Profiles:              profiles,
		Hosts:                 hosts,
		Validator:             validator,
		Notice: func(message string) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.notices = append(h.notices, message)
		},
		BrowserOpener: func(_ context.Context, authorizationURL string) error {
			h.mu.Lock()
			delivery := h.delivery
			noBrowser := h.noBrowser
			h.mu.Unlock()
			if noBrowser {
				return errors.New("the browser is not available")
			}
			h.browser.Go(func() {
				deliveryErr := fake.deliverCallback(t, authorizationURL, delivery)
				h.mu.Lock()
				defer h.mu.Unlock()
				if deliveryErr != nil {
					h.deliveryError = deliveryErr.Error()
				}
			})
			return nil
		},
	}
	for _, option := range options {
		option(&config)
	}
	authorizer, err := NewAuthorizer(config)
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}
	h.authorizer = authorizer
	t.Cleanup(func() { h.browser.Wait() })
	return h
}

// hostID returns the stable host identity of the harness.
func (h *harness) hostID(t *testing.T) string {
	t.Helper()
	hostID, err := h.hosts.EnsureHostID()
	if err != nil {
		t.Fatalf("EnsureHostID: %v", err)
	}
	return hostID
}

// browserDeliveryError returns the failure of the last simulated browser
// delivery, if any.
func (h *harness) browserDeliveryError() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.deliveryError
}

// noticeMessages returns the notices the flow reported.
func (h *harness) noticeMessages() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.notices...)
}

// setDelivery replaces what the simulated browser delivers.
func (h *harness) setDelivery(delivery callbackDelivery) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.delivery = delivery
}

// TestRegister_SendsTheDocumentedParametersAndReusesTheCallbackURI proves
// the two central properties of a first registration: the authorization
// request carries the documented parameters, and the code exchange carries
// the very callback URI the authorization request carried.
func TestRegister_SendsTheDocumentedParametersAndReusesTheCallbackURI(t *testing.T) {
	h := newHarness(t)
	h.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})

	record, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test", Label: "Personal"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	authorizations := h.fake.authorizationRequests()
	if len(authorizations) != 1 {
		t.Fatalf("the provider saw %d authorization requests, want 1", len(authorizations))
	}
	authorization := authorizations[0]
	for key, want := range map[string]string{
		"client_id":             DynamicAgentClientID,
		"agent_name_hint":       DefaultAgentName,
		"ext_agent_host_id":     h.hostID(t),
		"response_type":         "code",
		"scope":                 AuthorizationScope,
		"resource":              PlanResource,
		"code_challenge_method": "S256",
	} {
		if got := authorization.Get(key); got != want {
			t.Errorf("the authorization request carried %s=%q, want %q", key, got, want)
		}
	}
	for _, key := range []string{"state", "nonce", "code_challenge", "redirect_uri"} {
		if authorization.Get(key) == "" {
			t.Errorf("the authorization request carried no %s", key)
		}
	}
	for _, key := range []string{"id_token_hint", "login_hint", "client_secret"} {
		if authorization.Has(key) {
			t.Errorf("a first registration must not carry %s", key)
		}
	}
	redirectURI := authorization.Get("redirect_uri")
	if !strings.HasPrefix(redirectURI, "http://127.0.0.1:") {
		t.Errorf("the callback URI %q is not a loopback URI on 127.0.0.1", redirectURI)
	}
	if !strings.HasSuffix(redirectURI, CallbackPath) {
		t.Errorf("the callback URI %q does not end in %q", redirectURI, CallbackPath)
	}
	if strings.Contains(redirectURI, "localhost") {
		t.Errorf("the callback URI %q substitutes localhost for the loopback address", redirectURI)
	}

	exchanges := h.fake.exchangeRequests()
	if len(exchanges) != 1 {
		t.Fatalf("the provider saw %d exchanges, want 1", len(exchanges))
	}
	form := exchanges[0]
	if got := form.Get("redirect_uri"); got != redirectURI {
		t.Errorf("the exchange carried redirect_uri=%q, want the authorization request's %q", got, redirectURI)
	}
	if got := form.Get("client_id"); got != "oaiapp_issued_1" {
		t.Errorf("the exchange carried client_id=%q, want the issued client id", got)
	}
	if got := form.Get("code"); got != "auth-code-1" {
		t.Errorf("the exchange carried code=%q, want the callback's code", got)
	}
	if got := form.Get("code_verifier"); got == "" {
		t.Error("the exchange carried no code_verifier")
	}
	if got := form.Get("resource"); got != PlanResource {
		t.Errorf("the exchange carried resource=%q, want %q", got, PlanResource)
	}
	if got := form.Get("grant_type"); got != "authorization_code" {
		t.Errorf("the exchange carried grant_type=%q, want authorization_code", got)
	}
	for _, key := range []string{"client_secret", "agent_name_hint", "ext_agent_host_id"} {
		if form.Has(key) {
			t.Errorf("the exchange must not carry %s", key)
		}
	}
	wantFormKeys := []string{"client_id", "code", "code_verifier", "grant_type", "redirect_uri", "resource"}
	if got := sortedKeys(form); !equalStrings(got, wantFormKeys) {
		t.Errorf("the exchange carried the keys %v, want %v", got, wantFormKeys)
	}

	// The issued client id is persisted, and the dynamic registration entry
	// point is not.
	if record.IssuedClientID != "oaiapp_issued_1" {
		t.Errorf("the registration recorded the issued client id %q, want %q", record.IssuedClientID, "oaiapp_issued_1")
	}
	if record.IssuedClientID == DynamicAgentClientID {
		t.Error("the registration recorded the dynamic registration entry point as its issued client id")
	}
	if record.Subject != "subject-1" {
		t.Errorf("the registration recorded the subject %q, want %q", record.Subject, "subject-1")
	}
	if record.HostID != h.hostID(t) {
		t.Errorf("the registration recorded the host id %q, want %q", record.HostID, h.hostID(t))
	}
	if record.AccessToken != "access-token-1" || record.RefreshToken != "refresh-token-1" {
		t.Errorf("the registration recorded the token set %q/%q, want the exchanged one", record.AccessToken, record.RefreshToken)
	}
	if len(record.GrantedScopes) == 0 {
		t.Error("the registration recorded no granted scopes")
	}
	if record.Expiry.IsZero() {
		t.Error("the registration recorded no expiry")
	}

	stored, err := h.profiles.Load("acct-test")
	if err != nil {
		t.Fatalf("Load the registration: %v", err)
	}
	if stored.IssuedClientID != "oaiapp_issued_1" {
		t.Errorf("the stored registration carries the issued client id %q", stored.IssuedClientID)
	}
}

// TestRegister_CarriesFreshStateNonceAndPKCEPerAttempt proves that the
// values of an attempt are fresh and that the PKCE challenge belongs to
// the attempt's verifier.
func TestRegister_CarriesFreshStateNonceAndPKCEPerAttempt(t *testing.T) {
	h := newHarness(t)
	hostID := h.hostID(t)

	first, err := h.authorizer.BeginRegistration(context.Background(), hostID)
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	defer func() { _ = first.Close() }()
	second, err := h.authorizer.BeginRegistration(context.Background(), hostID)
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	defer func() { _ = second.Close() }()

	if first.pending.state == second.pending.state {
		t.Error("two attempts shared their state")
	}
	if first.pending.nonce == second.pending.nonce {
		t.Error("two attempts shared their nonce")
	}
	if first.pending.verifier == second.pending.verifier {
		t.Error("two attempts shared their PKCE verifier")
	}
	if got := first.pending.challenge; got != oauthcred.ComputePKCEChallenge(first.pending.verifier) {
		t.Errorf("the challenge %q is not the S256 digest of the attempt's verifier", got)
	}
	if first.pending.state == "" || first.pending.nonce == "" || first.pending.verifier == "" {
		t.Error("the attempt carries an empty state, nonce or verifier")
	}
	// Only the port may differ between two attempts.
	firstURI := first.RedirectURI()
	secondURI := second.RedirectURI()
	if firstURI == secondURI {
		t.Errorf("two attempts selected the same callback URI %q", firstURI)
	}
	if stripPort(firstURI) != stripPort(secondURI) {
		t.Errorf("the callback URIs %q and %q differ in more than the port", firstURI, secondURI)
	}
}

// TestBegin_RejectsAnAttemptWithoutItsRequiredIdentity proves that an
// attempt cannot start without the values the authorization request has to
// carry.
//
// The host identity is part of the request and describes THIS host, so an
// empty value would send an empty ext_agent_host_id to the provider. A
// returning authorization additionally needs the saved issued client id,
// and the registration entry point is not one. Every case is refused with
// no attempt returned, so a caller cannot use a partially initialized
// attempt.
func TestBegin_RejectsAnAttemptWithoutItsRequiredIdentity(t *testing.T) {
	h := newHarness(t)
	hostID := h.hostID(t)
	cases := []struct {
		name  string
		begin func(context.Context) (*Attempt, error)
	}{
		{
			name: "registration without a host identity",
			begin: func(ctx context.Context) (*Attempt, error) {
				return h.authorizer.BeginRegistration(ctx, "")
			},
		},
		{
			name: "returning authorization without a host identity",
			begin: func(ctx context.Context) (*Attempt, error) {
				return h.authorizer.BeginReauthorization(ctx, ReauthorizationOptions{
					IssuedClientID: "oaiapp_issued_1",
				})
			},
		},
		{
			name: "returning authorization without an issued client id",
			begin: func(ctx context.Context) (*Attempt, error) {
				return h.authorizer.BeginReauthorization(ctx, ReauthorizationOptions{
					HostID: hostID,
				})
			},
		},
		{
			name: "returning authorization with the registration entry point",
			begin: func(ctx context.Context) (*Attempt, error) {
				return h.authorizer.BeginReauthorization(ctx, ReauthorizationOptions{
					HostID:         hostID,
					IssuedClientID: DynamicAgentClientID,
				})
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			attempt, err := testCase.begin(context.Background())
			if err == nil {
				if attempt != nil {
					_ = attempt.Close()
				}
				t.Fatal("the attempt started without a required identity")
			}
			if attempt != nil {
				_ = attempt.Close()
				t.Fatal("the refusal returned an attempt")
			}
		})
	}
}

// TestRegister_StateMismatchFailsSafely proves that a callback that cannot
// prove it belongs to the attempt neither exchanges a code nor persists a
// registration.
func TestRegister_StateMismatchFailsSafely(t *testing.T) {
	h := newHarness(t)
	h.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1", state: "another-attempt-state"})

	_, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"})
	if !errors.Is(err, ErrStateMismatch) {
		t.Fatalf("Register error = %v, want a state mismatch", err)
	}
	if got := len(h.fake.exchangeRequests()); got != 0 {
		t.Errorf("the provider saw %d exchanges after a state mismatch, want 0", got)
	}
	if _, err := h.profiles.Load("acct-test"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a state mismatch persisted a registration: %v", err)
	}
}

// TestRegister_CancellationFailsSafely proves that a cancelled sign-in
// exchanges no code and persists no registration.
func TestRegister_CancellationFailsSafely(t *testing.T) {
	h := newHarness(t)
	h.mu.Lock()
	h.noBrowser = true
	h.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := h.authorizer.Register(ctx, RegisterRequest{ProfileID: "acct-test"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Register error = %v, want a cancellation", err)
	}
	if got := len(h.fake.exchangeRequests()); got != 0 {
		t.Errorf("the provider saw %d exchanges after a cancellation, want 0", got)
	}
	if _, err := h.profiles.Load("acct-test"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a cancellation persisted a registration: %v", err)
	}
}

// TestRegister_TimeoutFailsSafely proves that an attempt that is never
// answered expires instead of waiting forever, and that it exchanges no
// code.
func TestRegister_TimeoutFailsSafely(t *testing.T) {
	h := newHarness(t, func(config *AuthorizerConfig) {
		config.AttemptTTL = 150 * time.Millisecond
	})
	h.mu.Lock()
	h.noBrowser = true
	h.mu.Unlock()

	_, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"})
	if !errors.Is(err, ErrAttemptExpired) {
		t.Fatalf("Register error = %v, want an expired attempt", err)
	}
	if got := len(h.fake.exchangeRequests()); got != 0 {
		t.Errorf("the provider saw %d exchanges after an expired attempt, want 0", got)
	}
	if _, err := h.profiles.Load("acct-test"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an expired attempt persisted a registration: %v", err)
	}
}

// TestRegister_MissingIssuedClientIDIsIncomplete proves that a first
// registration whose callback carries no issued client id is refused
// before any code is exchanged.
func TestRegister_MissingIssuedClientIDIsIncomplete(t *testing.T) {
	h := newHarness(t)
	h.setDelivery(callbackDelivery{code: "auth-code-1", omitClientID: true})

	_, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"})
	if !errors.Is(err, ErrRegistrationIncomplete) {
		t.Fatalf("Register error = %v, want an incomplete registration", err)
	}
	if got := len(h.fake.exchangeRequests()); got != 0 {
		t.Errorf("the provider saw %d exchanges without an issued client id, want 0", got)
	}
	if _, err := h.profiles.Load("acct-test"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an incomplete registration persisted a registration: %v", err)
	}
}

// TestRegister_NeverPersistsTheDynamicRegistrationEntrypoint proves that
// the dynamic entry point is not an issued client id.
func TestRegister_NeverPersistsTheDynamicRegistrationEntrypoint(t *testing.T) {
	h := newHarness(t)
	h.setDelivery(callbackDelivery{code: "auth-code-1", clientID: DynamicAgentClientID})

	_, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"})
	if !errors.Is(err, ErrRegistrationIncomplete) {
		t.Fatalf("Register error = %v, want an incomplete registration", err)
	}
	if got := len(h.fake.exchangeRequests()); got != 0 {
		t.Errorf("the provider saw %d exchanges with the dynamic entry point, want 0", got)
	}
	if _, err := h.profiles.Load("acct-test"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the dynamic entry point was persisted: %v", err)
	}
}

// TestRegister_AccessDeniedStopsTheAttemptWithoutAnExchange proves the
// documented rule: a denied authorization stops the attempt and no code is
// exchanged.
func TestRegister_AccessDeniedStopsTheAttemptWithoutAnExchange(t *testing.T) {
	h := newHarness(t)
	h.setDelivery(callbackDelivery{errorCode: "access_denied"})

	_, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"})
	var callbackErr *CallbackError
	if !errors.As(err, &callbackErr) {
		t.Fatalf("Register error = %v, want an OAuth error", err)
	}
	if callbackErr.Code != "access_denied" {
		t.Errorf("the callback error carried the code %q, want access_denied", callbackErr.Code)
	}
	if got := len(h.fake.exchangeRequests()); got != 0 {
		t.Errorf("the provider saw %d exchanges after a denied authorization, want 0", got)
	}
}

// TestRegister_RejectedCodeStartsAFreshAuthorizationWithTheIssuedClientID
// proves the documented recovery: a rejected code is discarded and the
// fresh authorization keeps the issued client id.
func TestRegister_RejectedCodeStartsAFreshAuthorizationWithTheIssuedClientID(t *testing.T) {
	h := newHarness(t)
	h.fake.mu.Lock()
	h.fake.invalidGrants = 1
	h.fake.mu.Unlock()
	h.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})

	record, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	authorizations := h.fake.authorizationRequests()
	if len(authorizations) != 2 {
		t.Fatalf("the provider saw %d authorization requests, want 2", len(authorizations))
	}
	if got := authorizations[0].Get("client_id"); got != DynamicAgentClientID {
		t.Errorf("the first authorization carried the client id %q, want the dynamic entry point", got)
	}
	second := authorizations[1]
	if got := second.Get("client_id"); got != "oaiapp_issued_1" {
		t.Errorf("the fresh authorization carried the client id %q, want the retained issued client id", got)
	}
	if second.Has("agent_name_hint") {
		t.Error("the fresh authorization carried an agent name hint")
	}
	if first, second := authorizations[0].Get("state"), second.Get("state"); first == second {
		t.Error("the fresh authorization reused the state of the failed attempt")
	}
	if got := len(h.fake.exchangeRequests()); got != 2 {
		t.Fatalf("the provider saw %d exchanges, want 2", got)
	}
	last := h.fake.exchangeRequests()[1]
	if got := last.Get("client_id"); got != "oaiapp_issued_1" {
		t.Errorf("the second exchange carried the client id %q, want the issued client id", got)
	}
	if record.IssuedClientID != "oaiapp_issued_1" {
		t.Errorf("the registration recorded the issued client id %q", record.IssuedClientID)
	}
}

// TestExchange_SendsThePendingCallbackURI proves that the exchange carries
// the callback URI of the pending attempt and never a reconstructed one.
//
// The test mutates the pending URI after the authorization request was sent.
// A reconstruction from the listener or from a default would still carry
// the original URI and would be accepted by the provider; the pending value
// is refused.
func TestExchange_SendsThePendingCallbackURI(t *testing.T) {
	h := newHarness(t)
	h.fake.mu.Lock()
	h.fake.rejectMismatch = true
	h.fake.mu.Unlock()

	attempt, err := h.authorizer.BeginRegistration(context.Background(), h.hostID(t))
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	defer func() { _ = attempt.Close() }()
	h.fake.recordAuthorization(attempt.AuthorizationURL())

	mutated := "http://127.0.0.1:1" + CallbackPath
	attempt.pending.redirectURI = mutated

	_, err = h.authorizer.Exchange(context.Background(), attempt, "oaiapp_issued_1", "auth-code-1")
	if err == nil {
		t.Fatal("the exchange succeeded with a callback URI the provider did not authorize")
	}
	var tokenErr *TokenError
	if !errors.As(err, &tokenErr) {
		t.Fatalf("Exchange error = %v, want a token error", err)
	}
	if tokenErr.Code != "invalid_grant" {
		t.Errorf("the token error carried the code %q, want invalid_grant", tokenErr.Code)
	}
	exchanges := h.fake.exchangeRequests()
	if len(exchanges) != 1 {
		t.Fatalf("the provider saw %d exchanges, want 1", len(exchanges))
	}
	if got := exchanges[0].Get("redirect_uri"); got != mutated {
		t.Errorf("the exchange carried the redirect_uri %q, want the pending value %q", got, mutated)
	}
}

// TestAttempt_RedactedAuthorizationURLCannotDiscloseTheRetainedHint proves
// the operator-facing accessor does not hand out the retained ID-token
// hint.
//
// The authorization URL itself has to carry the hint to the provider, so
// the value is credential-bearing. RedactedAuthorizationURL is the accessor
// a log or a transcript may use; it must remove the hint and keep the URL
// usable.
func TestAttempt_RedactedAuthorizationURLCannotDiscloseTheRetainedHint(t *testing.T) {
	h := newHarness(t)
	const idTokenHint = "synthetic-retained-id-token"
	attempt, err := h.authorizer.BeginReauthorization(context.Background(), ReauthorizationOptions{
		HostID:         h.hostID(t),
		IssuedClientID: "oaiapp_issued_1",
		IDTokenHint:    idTokenHint,
	})
	if err != nil {
		t.Fatalf("BeginReauthorization: %v", err)
	}
	defer func() { _ = attempt.Close() }()

	raw := attempt.AuthorizationURL()
	if !strings.Contains(raw, idTokenHint) {
		t.Fatalf("the authorization URL does not carry the retained hint: %q", raw)
	}
	redacted := attempt.RedactedAuthorizationURL()
	if strings.Contains(redacted, idTokenHint) {
		t.Errorf("the redacted authorization URL discloses the retained hint: %q", redacted)
	}
	if !strings.Contains(redacted, "client_id=oaiapp_issued_1") {
		t.Errorf("the redacted authorization URL lost the client id: %q", redacted)
	}
	if !strings.Contains(redacted, "state=") {
		t.Errorf("the redacted authorization URL lost the state: %q", redacted)
	}
}

// TestReauthorize_ReusesTheSavedIssuedClientIDAndConfirmsTheSubject proves
// the returning authorization: it reuses the saved issued client id, omits
// the agent name, carries the retained hints, and replaces the credentials
// only after the subject was confirmed.
func TestReauthorize_ReusesTheSavedIssuedClientIDAndConfirmsTheSubject(t *testing.T) {
	h := newHarness(t)
	h.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})
	registered, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test", Label: "Personal"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// A returning authorization may omit the client id in the callback.
	h.setDelivery(callbackDelivery{code: "auth-code-2", omitClientID: true})
	updated, err := h.authorizer.Reauthorize(context.Background(), ReauthorizeRequest{ProfileID: "acct-test"})
	if err != nil {
		t.Fatalf("Reauthorize: %v", err)
	}

	authorizations := h.fake.authorizationRequests()
	if len(authorizations) != 2 {
		t.Fatalf("the provider saw %d authorization requests, want 2", len(authorizations))
	}
	second := authorizations[1]
	if got := second.Get("client_id"); got != "oaiapp_issued_1" {
		t.Errorf("the returning authorization carried the client id %q, want the saved issued client id", got)
	}
	if second.Has("agent_name_hint") {
		t.Error("the returning authorization carried an agent name hint")
	}
	if got := second.Get("id_token_hint"); got != registered.IDToken {
		t.Errorf("the returning authorization carried the id_token_hint %q, want the retained one", got)
	}
	if got := second.Get("login_hint"); got != registered.EmailHint {
		t.Errorf("the returning authorization carried the login_hint %q, want the saved one", got)
	}
	if got := second.Get("ext_agent_host_id"); got != registered.HostID {
		t.Errorf("the returning authorization carried the host id %q, want %q", got, registered.HostID)
	}
	exchanges := h.fake.exchangeRequests()
	if len(exchanges) != 2 {
		t.Fatalf("the provider saw %d exchanges, want 2", len(exchanges))
	}
	if got := exchanges[1].Get("client_id"); got != "oaiapp_issued_1" {
		t.Errorf("the returning exchange carried the client id %q, want the saved issued client id", got)
	}
	if got := exchanges[1].Get("redirect_uri"); got != second.Get("redirect_uri") {
		t.Errorf("the returning exchange carried the redirect_uri %q, want %q", got, second.Get("redirect_uri"))
	}
	if updated.AccessToken != "access-token-2" {
		t.Errorf("the returning authorization recorded the access token %q, want the new one", updated.AccessToken)
	}
	if updated.IssuedClientID != "oaiapp_issued_1" {
		t.Errorf("the returning authorization recorded the issued client id %q", updated.IssuedClientID)
	}
	stored, err := h.profiles.Load("acct-test")
	if err != nil {
		t.Fatalf("Load the registration: %v", err)
	}
	if stored.AccessToken != "access-token-2" {
		t.Errorf("the stored registration holds the access token %q, want the replaced one", stored.AccessToken)
	}
	if stored.Label != "Personal" {
		t.Errorf("the returning authorization changed the label to %q", stored.Label)
	}
}

// TestReauthorize_RejectsAnotherAccountBeforeReplacingTheCredentials proves
// that the subject is confirmed before any credential is replaced.
func TestReauthorize_RejectsAnotherAccountBeforeReplacingTheCredentials(t *testing.T) {
	h := newHarness(t)
	h.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})
	if _, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	h.setDelivery(callbackDelivery{code: "auth-code-2", omitClientID: true})
	h.validator.setIdentity(Identity{Subject: "another-subject", EmailHint: "other@example.com"})
	_, err := h.authorizer.Reauthorize(context.Background(), ReauthorizeRequest{ProfileID: "acct-test"})
	if !errors.Is(err, ErrSubjectMismatch) {
		t.Fatalf("Reauthorize error = %v, want a subject mismatch", err)
	}
	stored, err := h.profiles.Load("acct-test")
	if err != nil {
		t.Fatalf("Load the registration: %v", err)
	}
	if stored.AccessToken != "access-token-1" {
		t.Errorf("the stored registration holds the access token %q, want the untouched one", stored.AccessToken)
	}
	if stored.Subject != "subject-1" {
		t.Errorf("the stored registration holds the subject %q, want the untouched one", stored.Subject)
	}
}

// TestReauthorize_RejectsACallbackWithAnotherIssuedClientID proves that a
// returning authorization refuses a callback that names another
// registration's client id.
func TestReauthorize_RejectsACallbackWithAnotherIssuedClientID(t *testing.T) {
	h := newHarness(t)
	h.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})
	if _, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	before := len(h.fake.exchangeRequests())

	h.setDelivery(callbackDelivery{code: "auth-code-2", clientID: "oaiapp_other"})
	_, err := h.authorizer.Reauthorize(context.Background(), ReauthorizeRequest{ProfileID: "acct-test"})
	if !errors.Is(err, ErrIssuedClientIDMismatch) {
		t.Fatalf("Reauthorize error = %v, want an issued client id mismatch", err)
	}
	if got := len(h.fake.exchangeRequests()); got != before {
		t.Errorf("the provider saw %d exchanges after a client id mismatch, want %d", got, before)
	}
	stored, err := h.profiles.Load("acct-test")
	if err != nil {
		t.Fatalf("Load the registration: %v", err)
	}
	if stored.IssuedClientID != "oaiapp_issued_1" {
		t.Errorf("the stored registration holds the issued client id %q, want the untouched one", stored.IssuedClientID)
	}
}

// TestRegister_GeneratesACanonicalProfileID proves that a registration
// without an explicit id still receives a key the store accepts.
func TestRegister_GeneratesACanonicalProfileID(t *testing.T) {
	h := newHarness(t)
	h.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})

	record, err := h.authorizer.Register(context.Background(), RegisterRequest{})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := validateProfileID(record.ID); err != nil {
		t.Fatalf("the generated profile id %q is not canonical: %v", record.ID, err)
	}
	if _, err := h.profiles.Load(record.ID); err != nil {
		t.Fatalf("Load the registration: %v", err)
	}
}

// TestRegister_RefusesToOverwriteASavedRegistration proves that a first
// registration never takes over a saved registration: the documentation
// forbids overwriting one registration's credentials with another account's.
func TestRegister_RefusesToOverwriteASavedRegistration(t *testing.T) {
	h := newHarness(t)
	h.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})
	if _, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	before := len(h.fake.exchangeRequests())

	h.setDelivery(callbackDelivery{code: "auth-code-2", clientID: "oaiapp_other"})
	_, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"})
	if err == nil {
		t.Fatal("Register overwrote a saved registration")
	}
	if got := len(h.fake.exchangeRequests()); got != before {
		t.Errorf("the provider saw %d exchanges after the refusal, want %d", got, before)
	}
	stored, err := h.profiles.Load("acct-test")
	if err != nil {
		t.Fatalf("Load the registration: %v", err)
	}
	if stored.IssuedClientID != "oaiapp_issued_1" {
		t.Errorf("the stored registration holds the issued client id %q, want the untouched one", stored.IssuedClientID)
	}
	if stored.AccessToken != "access-token-1" {
		t.Errorf("the stored registration holds the access token %q, want the untouched one", stored.AccessToken)
	}
}

// TestNewAuthorizer_RequiresItsCollaborators proves that the service fails
// closed when it could not persist, name its host or validate an identity.
func TestNewAuthorizer_RequiresItsCollaborators(t *testing.T) {
	complete := AuthorizerConfig{
		Profiles:  &ProfileStore{},
		Hosts:     &HostStore{},
		Validator: UnavailableIDTokenValidator{},
	}
	if _, err := NewAuthorizer(complete); err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}
	missing := []func(AuthorizerConfig) AuthorizerConfig{
		func(config AuthorizerConfig) AuthorizerConfig { config.Profiles = nil; return config },
		func(config AuthorizerConfig) AuthorizerConfig { config.Hosts = nil; return config },
		func(config AuthorizerConfig) AuthorizerConfig { config.Validator = nil; return config },
	}
	for index, mutate := range missing {
		config := mutate(complete)
		if _, err := NewAuthorizer(config); err == nil {
			t.Errorf("case %d: NewAuthorizer accepted an incomplete configuration", index)
		}
	}
}

// TestUnavailableIDTokenValidator_FailsClosed proves that the placeholder
// of the OIDC phase never produces an identity.
func TestUnavailableIDTokenValidator_FailsClosed(t *testing.T) {
	identity, err := UnavailableIDTokenValidator{}.ValidateIDToken(
		context.Background(),
		TokenResponse{},
		TokenExpectation{Nonce: "nonce", Audience: "oaiapp_1"},
	)
	if !errors.Is(err, ErrIDTokenValidationUnavailable) {
		t.Fatalf("ValidateIDToken error = %v, want the unavailable failure", err)
	}
	if identity.Subject != "" {
		t.Errorf("the unavailable validator returned the subject %q", identity.Subject)
	}
}

// TestRedactAuthorizationURL_RedactsBothRetainedHintsAndPreservesTheRest
// proves that both retained hints are redacted and that the rest of the URL
// survives.
//
// The hints identify the account: the ID token names the account and the
// email names the person. The documentation requires both to be redacted, so
// a redaction that removed only the ID token would still disclose the email
// and would fail the contract the accessor exists for.
func TestRedactAuthorizationURL_RedactsBothRetainedHintsAndPreservesTheRest(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		hint string
	}{
		{
			name: "id token hint",
			raw:  "https://auth.openai.com/api/accounts/authorize?client_id=oaiapp_1&id_token_hint=secret-token&state=abc",
			hint: "id_token_hint",
		},
		{
			name: "login hint",
			raw:  "https://auth.openai.com/api/accounts/authorize?client_id=oaiapp_1&login_hint=user%40example.com&state=abc",
			hint: "login_hint",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			redacted := redactAuthorizationURL(testCase.raw)
			parsed, err := url.Parse(redacted)
			if err != nil {
				t.Fatalf("parse the redacted URL %q: %v", redacted, err)
			}
			if got := parsed.Query().Get(testCase.hint); got != "[REDACTED]" {
				t.Errorf("the redacted %s = %q, want the redaction marker", testCase.hint, got)
			}
			if got := parsed.Query().Get("client_id"); got != "oaiapp_1" {
				t.Errorf("the redacted URL lost the client id: %q", got)
			}
			if got := parsed.Query().Get("state"); got != "abc" {
				t.Errorf("the redacted URL lost the state: %q", got)
			}
		})
	}
	withoutHint := "https://auth.openai.com/api/accounts/authorize?client_id=oaiapp_1"
	if got := redactAuthorizationURL(withoutHint); got != withoutHint {
		t.Errorf("the redacted URL %q differs from the original %q", got, withoutHint)
	}
}

// sortedKeys returns the parameter names of a query, sorted.
func sortedKeys(values url.Values) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// equalStrings reports whether two slices hold the same values.
func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// stripPort removes the port of a loopback URI.
func stripPort(uri string) string {
	parsed, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	parsed.Host = LoopbackHost
	return parsed.String()
}
