package siwc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The safety properties of the authorization service that the review
// demanded: a provider must not be able to turn its own error response into
// a credential disclosure, two overlapping registrations must not destroy
// each other, the exchange must end with its attempt, and the operator's
// fallback must not carry a retained hint.

// TestExchange_ProviderErrorCannotDiscloseTheAuthorizationCode proves that
// a provider that echoes the authorization code in the OAuth error field
// cannot get the code into an operator- or support-facing error.
//
// The error field is a CODE, so an undocumented value is classified instead
// of being rendered.
func TestExchange_ProviderErrorCannotDiscloseTheAuthorizationCode(t *testing.T) {
	const authorizationCode = "synthetic-authorization-credential"
	h := newHarness(t)
	h.fake.mu.Lock()
	h.fake.errorBody = `{"error":"` + authorizationCode + `","error_description":"request rejected"}`
	h.fake.mu.Unlock()

	attempt, err := h.authorizer.BeginRegistration(context.Background(), h.hostID(t))
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	defer func() { _ = attempt.Close() }()

	_, err = h.authorizer.Exchange(context.Background(), attempt, "oaiapp_issued_1", authorizationCode)
	if err == nil {
		t.Fatal("the exchange succeeded against a provider that rejected it")
	}
	if strings.Contains(err.Error(), authorizationCode) {
		t.Errorf("the exchange error discloses the authorization code: %q", err.Error())
	}
	var tokenErr *TokenError
	if !errors.As(err, &tokenErr) {
		t.Fatalf("the exchange error is %v, want a token error", err)
	}
	if tokenErr.Code != "provider_error" {
		t.Errorf("the token error carries the code %q, want the classified provider_error", tokenErr.Code)
	}
}

// TestExchange_TruncationCannotExposeACredentialPrefix proves that the
// bound on the provider's description cannot expose a prefix of a
// credential.
//
// The redaction has to happen BEFORE the bound: a secret that crosses the
// bound no longer exists as a whole, and a prefix of it would survive.
func TestExchange_TruncationCannotExposeACredentialPrefix(t *testing.T) {
	secret := strings.Repeat("synthetic-credential-", 20)
	h := newHarness(t)
	h.fake.mu.Lock()
	h.fake.errorBody = `{"error":"invalid_request","error_description":"` + strings.Repeat("x", 230) + secret + `"}`
	h.fake.mu.Unlock()

	attempt, err := h.authorizer.BeginRegistration(context.Background(), h.hostID(t))
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	defer func() { _ = attempt.Close() }()

	_, err = h.authorizer.Exchange(context.Background(), attempt, "oaiapp_issued_1", secret)
	if err == nil {
		t.Fatal("the exchange succeeded against a provider that rejected it")
	}
	if strings.Contains(err.Error(), secret[:26]) {
		t.Errorf("the exchange error exposes a prefix of the credential: %q", err.Error())
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the exchange error discloses the credential: %q", err.Error())
	}
}

// TestCallbackError_CannotDiscloseThePendingValues proves that the
// provider's own callback error text cannot carry the attempt's pending
// values into an operator-facing error.
func TestCallbackError_CannotDiscloseThePendingValues(t *testing.T) {
	h := newHarness(t)
	attempt, err := h.authorizer.BeginRegistration(context.Background(), h.hostID(t))
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	defer func() { _ = attempt.Close() }()

	values := url.Values{
		"state":             {attempt.pending.state},
		"error":             {"access_denied"},
		"error_description": {"rejected for " + attempt.pending.state},
	}
	response, err := http.Get(attempt.listener.redirectURI() + "?" + values.Encode())
	if err != nil {
		t.Fatalf("deliver the callback: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	outcome := <-attempt.listener.outcomes
	var callbackErr *CallbackError
	if !errors.As(outcome.err, &callbackErr) {
		t.Fatalf("the callback produced the error %v, want an OAuth error", outcome.err)
	}
	if callbackErr.Code != "access_denied" {
		t.Errorf("the callback error carries the code %q, want access_denied", callbackErr.Code)
	}
	if strings.Contains(callbackErr.Description, attempt.pending.state) {
		t.Errorf("the callback error discloses the attempt's state: %q", callbackErr.Description)
	}
	if strings.Contains(callbackErr.Error(), attempt.pending.state) {
		t.Errorf("the callback error discloses the attempt's state: %q", callbackErr.Error())
	}
}

// TestRegister_OverlappingRegistrationsCannotOverwriteEachOther proves
// that two overlapping registrations of the same local id cannot destroy
// each other's registration.
//
// The second registration publishes while the first one is still waiting
// for the operator's browser. The first one must then be REFUSED, and the
// record that is stored must still be the one the second registration
// published.
func TestRegister_OverlappingRegistrationsCannotOverwriteEachOther(t *testing.T) {
	first := newHarness(t)
	second := newHarness(t)
	second.authorizer.config.Profiles = first.profiles
	second.authorizer.config.Hosts = first.hosts
	second.validator.setIdentity(Identity{Subject: "other-subject", EmailHint: "other@example.com"})
	first.setDelivery(callbackDelivery{code: "first-code", clientID: "oaiapp_first"})
	second.setDelivery(callbackDelivery{code: "second-code", clientID: "oaiapp_second"})

	original := first.authorizer.config.BrowserOpener
	first.authorizer.config.BrowserOpener = func(ctx context.Context, target string) error {
		// The first registration has passed its pre-check and is waiting
		// for the operator's browser. A second, independent registration
		// completes against the same physical store.
		if _, err := second.authorizer.Register(ctx, RegisterRequest{ProfileID: "acct-overlap"}); err != nil {
			return err
		}
		return original(ctx, target)
	}

	_, err := first.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-overlap"})
	if err == nil {
		t.Fatal("the overlapping registration replaced the published registration")
	}
	stored, err := first.profiles.Load("acct-overlap")
	if err != nil {
		t.Fatalf("Load the registration: %v", err)
	}
	if stored.Subject != "other-subject" {
		t.Errorf("the stored registration holds the subject %q, want the published one", stored.Subject)
	}
	if stored.IssuedClientID != "oaiapp_second" {
		t.Errorf("the stored registration holds the issued client id %q, want the published one", stored.IssuedClientID)
	}
	if stored.AccessToken != "access-token-1" {
		t.Errorf("the stored registration holds the access token %q, want the published one", stored.AccessToken)
	}
}

// TestExchange_IsBoundedByTheAttemptDeadline proves that the exchange ends
// with its attempt, whatever HTTP client the caller supplied.
//
// A client without a timeout must not be able to hold the exchange - or the
// response body read - open past the attempt's deadline.
func TestExchange_IsBoundedByTheAttemptDeadlineWhenTheProviderStalls(t *testing.T) {
	cases := []struct {
		name    string
		handler func(release <-chan struct{}) http.HandlerFunc
	}{
		{
			name: "stalled headers",
			handler: func(release <-chan struct{}) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					<-release
				}
			},
		},
		{
			name: "stalled body",
			handler: func(release <-chan struct{}) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"access_token":"synthetic-`))
					<-release
				}
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			release := make(chan struct{})
			server := httptest.NewServer(testCase.handler(release))
			// The release runs first: the test server waits for its
			// outstanding handler, and the handler waits for the release.
			defer server.Close()
			defer close(release)

			h := newHarness(t, func(config *AuthorizerConfig) {
				// A client without a timeout: the attempt's deadline has
				// to be the bound.
				config.HTTPClient = http.DefaultClient
				config.TokenEndpoint = server.URL
				config.AttemptTTL = 200 * time.Millisecond
			})
			attempt, err := h.authorizer.BeginRegistration(context.Background(), h.hostID(t))
			if err != nil {
				t.Fatalf("BeginRegistration: %v", err)
			}
			defer func() { _ = attempt.Close() }()

			done := make(chan error, 1)
			go func() {
				_, exchangeErr := h.authorizer.Exchange(context.Background(), attempt, "oaiapp_issued_1", "auth-code-1")
				done <- exchangeErr
			}()
			select {
			case exchangeErr := <-done:
				if exchangeErr == nil {
					t.Fatal("the exchange succeeded against a stalled provider")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the exchange outlived the attempt's deadline")
			}
		})
	}
}

// TestReauthorize_BrowserFallbackCarriesNoRetainedHint proves that the
// fallback the operator is told to open carries neither retained hint.
//
// The hints identify the account, and the fallback is written to the
// operator's terminal. The URL also has to be USABLE: a redaction marker
// where the ID token belongs would be refused by the provider, so the
// hints are omitted instead.
func TestReauthorize_BrowserFallbackCarriesNoRetainedHint(t *testing.T) {
	h := newHarness(t)
	notices := make(chan string, 4)
	h.authorizer.config.BrowserOpener = func(context.Context, string) error {
		return errors.New("the browser is not available")
	}
	h.authorizer.config.Notice = func(message string) {
		notices <- message
	}

	attempt, err := h.authorizer.BeginReauthorization(context.Background(), ReauthorizationOptions{
		HostID:         h.hostID(t),
		IssuedClientID: "oaiapp_issued_1",
		IDTokenHint:    "synthetic-id-token",
		LoginHint:      "synthetic-account@example.invalid",
	})
	if err != nil {
		t.Fatalf("BeginReauthorization: %v", err)
	}
	defer func() { _ = attempt.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = h.authorizer.runAttempt(ctx, attempt)

	var notice string
	select {
	case notice = <-notices:
	case <-time.After(2 * time.Second):
		t.Fatal("the command reported no fallback URL")
	}
	if strings.Contains(notice, "synthetic-id-token") {
		t.Errorf("the fallback discloses the retained ID token: %q", notice)
	}
	if strings.Contains(notice, "synthetic-account@example.invalid") {
		t.Errorf("the fallback discloses the retained account hint: %q", notice)
	}
	if strings.Contains(notice, url.QueryEscape("synthetic-account@example.invalid")) {
		t.Errorf("the fallback discloses the encoded account hint: %q", notice)
	}
	if strings.Contains(notice, "[REDACTED]") {
		t.Errorf("the fallback carries a redaction marker and would be refused: %q", notice)
	}
	if !strings.Contains(notice, "client_id=oaiapp_issued_1") {
		t.Errorf("the fallback does not name the registration: %q", notice)
	}
}

// TestCallback_CannotDiscloseTheCodeItCarries proves that a callback that
// carries BOTH an OAuth error and an authorization code cannot put the code
// into an operator- or support-facing error.
//
// The callback's own code ARRIVES from the provider, so it is not in the
// attempt's pre-computed secret set, and the bound on the description does
// not save it: a code that crosses the bound leaves a prefix behind.
//
// The case is covered twice: with a code that fits in the bound, and with a
// code that crosses it.
//
// The classified code stays VISIBLE: "access_denied" is a decision of the
// user, and the recovery differs per code.
func TestCallback_CannotDiscloseTheCodeItCarries(t *testing.T) {
	const shortCode = "synthetic-mixed-callback-authorization-code"
	for _, testCase := range []struct {
		name string
		code string
	}{
		{name: "short code", code: shortCode},
		{name: "bound crossing code", code: strings.Repeat(shortCode+"-", 12)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			h := newHarness(t)
			h.setDelivery(callbackDelivery{
				code:             testCase.code,
				clientID:         "oaiapp_issued_1",
				errorCode:        "access_denied",
				errorDescription: "rejected: " + testCase.code,
			})

			_, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"})
			if err == nil {
				t.Fatal("the registration succeeded against a denied callback")
			}
			var callbackErr *CallbackError
			if !errors.As(err, &callbackErr) {
				t.Fatalf("the registration failed with %v, want an OAuth error", err)
			}
			if callbackErr.Code != "access_denied" {
				t.Errorf("the callback error carried the code %q, want access_denied", callbackErr.Code)
			}
			// A mixed callback carries a code, and no code may be
			// exchanged.
			if got := len(h.fake.exchangeRequests()); got != 0 {
				t.Errorf("the provider saw %d exchanges after a mixed callback, want 0", got)
			}
			if strings.Contains(callbackErr.Description, testCase.code) {
				t.Errorf("the callback error discloses the code: %q", callbackErr.Description)
			}
			if strings.Contains(callbackErr.Description, testCase.code[:26]) {
				t.Errorf("the callback error exposes a prefix of the code: %q", callbackErr.Description)
			}
			if strings.Contains(err.Error(), testCase.code) {
				t.Errorf("the error discloses the code: %q", err.Error())
			}
			if strings.Contains(err.Error(), testCase.code[:26]) {
				t.Errorf("the error exposes a prefix of the code: %q", err.Error())
			}
		})
	}
}

// TestReauthorize_UsesThisHostsIdentity proves that a returning
// authorization identifies THIS host, even when the registration was
// created on another host.
//
// The profile travels, the host record does not: a profile that was copied
// from host A into host B still names host A, and host B has to identify
// itself as host B.
//
// The same-host case is the control: a registration that was created here
// keeps its provenance, and nothing is reconciled.
func TestReauthorize_UsesThisHostsIdentity(t *testing.T) {
	t.Run("a copied profile is reconciled to this host", func(t *testing.T) {
		source := newHarness(t)
		source.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})
		registered, err := source.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"})
		if err != nil {
			t.Fatalf("Register: %v", err)
		}

		destination := newHarness(t)
		destinationHostID := destination.hostID(t)
		if destinationHostID == registered.HostID {
			t.Fatal("the two hosts share their identity")
		}
		// Only the profile is copied: the host record stays with the
		// source host.
		if err := destination.profiles.Save(registered); err != nil {
			t.Fatalf("Save the copied registration: %v", err)
		}

		destination.setDelivery(callbackDelivery{code: "auth-code-2", omitClientID: true})
		updated, err := destination.authorizer.Reauthorize(context.Background(), ReauthorizeRequest{ProfileID: "acct-test"})
		if err != nil {
			t.Fatalf("Reauthorize: %v", err)
		}

		authorizations := destination.fake.authorizationRequests()
		if len(authorizations) != 1 {
			t.Fatalf("the provider saw %d authorization requests, want 1", len(authorizations))
		}
		if got := authorizations[0].Get("ext_agent_host_id"); got != destinationHostID {
			t.Errorf("the returning authorization carried the host id %q, want this host's %q", got, destinationHostID)
		}
		if updated.HostID != destinationHostID {
			t.Errorf("the returning authorization recorded the host id %q, want this host's %q", updated.HostID, destinationHostID)
		}
		stored, err := destination.profiles.Load("acct-test")
		if err != nil {
			t.Fatalf("Load the registration: %v", err)
		}
		if stored.HostID != destinationHostID {
			t.Errorf("the stored registration carries the host id %q, want this host's %q", stored.HostID, destinationHostID)
		}
		// The reconciliation is REPORTED: it is a change of the
		// registration's provenance, and a change that is not reported is
		// a hidden one.
		if !reportsReconciliation(destination.noticeMessages()) {
			t.Errorf("the reconciliation was not reported: %v", destination.noticeMessages())
		}
	})

	t.Run("a registration of this host keeps its provenance", func(t *testing.T) {
		h := newHarness(t)
		h.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})
		registered, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"})
		if err != nil {
			t.Fatalf("Register: %v", err)
		}

		h.setDelivery(callbackDelivery{code: "auth-code-2", omitClientID: true})
		updated, err := h.authorizer.Reauthorize(context.Background(), ReauthorizeRequest{ProfileID: "acct-test"})
		if err != nil {
			t.Fatalf("Reauthorize: %v", err)
		}
		authorizations := h.fake.authorizationRequests()
		if len(authorizations) != 2 {
			t.Fatalf("the provider saw %d authorization requests, want 2", len(authorizations))
		}
		if got := authorizations[1].Get("ext_agent_host_id"); got != registered.HostID {
			t.Errorf("the returning authorization carried the host id %q, want %q", got, registered.HostID)
		}
		if updated.HostID != registered.HostID {
			t.Errorf("the returning authorization recorded the host id %q, want %q", updated.HostID, registered.HostID)
		}
		if reportsReconciliation(h.noticeMessages()) {
			t.Errorf("a registration of this host reported a reconciliation: %v", h.noticeMessages())
		}
	})
}

// reportsReconciliation reports whether one of the notices names the
// reconciliation of a registration's recorded host provenance.
func reportsReconciliation(notices []string) bool {
	for _, notice := range notices {
		if strings.Contains(notice, "reconciled") {
			return true
		}
	}
	return false
}

// TestValidatorError_NeverRendersTheValidatorText proves that a validator
// that reports the token in its own error cannot put the token into an
// operator- or support-facing error.
//
// The validator is an implementation detail that the OIDC phase owns, and
// the flow renders the CLASS of a rejection, never the validator's text.
func TestValidatorError_NeverRendersTheValidatorText(t *testing.T) {
	const token = "synthetic-id-token-credential"
	h := newHarness(t)
	h.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})
	h.validator.setError(errors.New("the token " + token + " is not valid"))

	_, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"})
	if err == nil {
		t.Fatal("the registration succeeded against a rejected ID token")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("the error discloses the ID token: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "ID token") {
		t.Errorf("the error does not name the rejected identity: %q", err.Error())
	}
}

// TestTokenResponse_FormattingNeverReachesTheTokens proves that no
// formatting verb reaches the tokens.
//
// The verbs below include %d and %#v, which fmt does NOT route through the
// Stringer: the response is safe because it implements fmt.Formatter, and a
// response that only implemented Stringer would print its fields for them.
func TestTokenResponse_FormattingNeverReachesTheTokens(t *testing.T) {
	response := TokenResponse{
		AccessToken:  "synthetic-access-token",
		RefreshToken: "synthetic-refresh-token",
		IDToken:      "synthetic-id-token",
		TokenType:    "Bearer",
		ExpiresIn:    3600,
		Scope:        "openid email",
	}
	for _, rendered := range []string{
		response.String(),
		response.GoString(),
		sprintf("%v", response),
		sprintf("%s", response),
		sprintf("%q", response),
		sprintf("%+v", response),
		sprintf("%#v", response),
		sprintf("%d", response),
		sprintf("%t", response),
	} {
		for _, secret := range []string{"synthetic-access-token", "synthetic-refresh-token", "synthetic-id-token"} {
			if strings.Contains(rendered, secret) {
				t.Errorf("the formatted response discloses %q: %q", secret, rendered)
			}
		}
	}
}

// TestCallbackResult_FormattingNeverReachesTheCode proves that no formatting
// verb reaches the authorization code.
//
// The verbs below include %d and %#v, which fmt does NOT route through the
// Stringer: the result is safe because it implements fmt.Formatter, and a
// result that only implemented Stringer would print its fields for them.
func TestCallbackResult_FormattingNeverReachesTheCode(t *testing.T) {
	result := CallbackResult{Code: "synthetic-authorization-code", IssuedClientID: "oaiapp_issued_1", Scope: "openid"}
	for _, rendered := range []string{
		result.String(),
		result.GoString(),
		sprintf("%v", result),
		sprintf("%s", result),
		sprintf("%q", result),
		sprintf("%+v", result),
		sprintf("%#v", result),
		sprintf("%d", result),
		sprintf("%t", result),
	} {
		if strings.Contains(rendered, "synthetic-authorization-code") {
			t.Errorf("the formatted result discloses the authorization code: %q", rendered)
		}
	}
}

// TestReauthorize_CallbackErrorCannotDiscloseTheRetainedHint proves that a
// provider that echoes the retained identity hints back in the callback
// cannot put them into an operator-facing error.
//
// The retained hints belong to the attempt's secret set even when they are
// expired: the ID token names the account, and the login hint names the
// person. The test covers a hint that is LONGER than the bound as well,
// because a secret that crosses the bound must be removed whole.
func TestReauthorize_CallbackErrorCannotDiscloseTheRetainedHint(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		hint  string
		email string
	}{
		{name: "short hint", hint: "synthetic-retained-id-token", email: "synthetic-account@example.invalid"},
		{name: "bound crossing hint", hint: strings.Repeat("synthetic-retained-id-token-", 20), email: "synthetic-account@example.invalid"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			h := newHarness(t)
			h.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})
			registered, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"})
			if err != nil {
				t.Fatalf("Register: %v", err)
			}
			registered.IDToken = testCase.hint
			registered.EmailHint = testCase.email
			if err := h.profiles.Save(registered); err != nil {
				t.Fatalf("Save the registration: %v", err)
			}

			// The provider answers the returning callback with an OAuth
			// error whose description echoes the retained hints back.
			h.authorizer.config.BrowserOpener = func(_ context.Context, authorizationURL string) error {
				parsed, parseErr := url.Parse(authorizationURL)
				if parseErr != nil {
					return parseErr
				}
				query := parsed.Query()
				values := url.Values{
					"state":             {query.Get("state")},
					"error":             {"access_denied"},
					"error_description": {"rejected hint " + query.Get("id_token_hint") + " and " + query.Get("login_hint")},
				}
				response, getErr := http.Get(query.Get("redirect_uri") + "?" + values.Encode())
				if getErr != nil {
					return getErr
				}
				defer func() { _ = response.Body.Close() }()
				_, _ = io.Copy(io.Discard, response.Body)
				return nil
			}

			_, err = h.authorizer.Reauthorize(context.Background(), ReauthorizeRequest{ProfileID: "acct-test"})
			if err == nil {
				t.Fatal("the returning authorization succeeded against a denied callback")
			}
			if strings.Contains(err.Error(), testCase.hint) {
				t.Errorf("the error discloses the retained ID token: %q", err.Error())
			}
			if len(testCase.hint) > 26 && strings.Contains(err.Error(), testCase.hint[:26]) {
				t.Errorf("the error exposes a prefix of the retained ID token: %q", err.Error())
			}
			if strings.Contains(err.Error(), testCase.email) {
				t.Errorf("the error discloses the retained account hint: %q", err.Error())
			}
		})
	}
}

// TestExchange_RefusesARedirectWithoutRenderingTheLocation proves that the
// exchange refuses a provider's redirect instead of following it.
//
// The authorization code is in the request, and the provider must not choose
// the destination of a request that carries it. The redirect target is a
// REACHABLE server here: a client that followed the redirect would deliver
// the code to it, and the exchange would then succeed.
func TestExchange_RefusesARedirectWithoutRenderingTheLocation(t *testing.T) {
	const authorizationCode = "synthetic-redirected-authorization-code"
	var destinationRequests atomic.Int64
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		destinationRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"redirected-access-token","refresh_token":"redirected-refresh-token","token_type":"Bearer","expires_in":3600}`)
	}))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Location", destination.URL+"?credential="+url.QueryEscape(r.PostForm.Get("code")))
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	h := newHarness(t, func(config *AuthorizerConfig) {
		// The ordinary client: the exchange must refuse the redirect even
		// when the client would follow one.
		config.HTTPClient = &http.Client{}
		config.TokenEndpoint = server.URL
	})
	attempt, err := h.authorizer.BeginRegistration(context.Background(), h.hostID(t))
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	defer func() { _ = attempt.Close() }()

	_, err = h.authorizer.Exchange(context.Background(), attempt, "oaiapp_issued_1", authorizationCode)
	if err == nil {
		t.Fatal("the exchange followed the provider's redirect")
	}
	var tokenErr *TokenError
	if !errors.As(err, &tokenErr) {
		t.Fatalf("the exchange failed with %v, want a classified token error", err)
	}
	if tokenErr.Code != "redirect_refused" {
		t.Errorf("the token error carries the code %q, want the classified redirect_refused", tokenErr.Code)
	}
	if got := destinationRequests.Load(); got != 0 {
		t.Errorf("the redirect destination received %d requests, want 0", got)
	}
	if strings.Contains(err.Error(), authorizationCode) {
		t.Errorf("the exchange error discloses the authorization code: %q", err.Error())
	}
	if strings.Contains(err.Error(), destination.URL) || strings.Contains(err.Error(), "credential=") {
		t.Errorf("the exchange error renders the provider's Location: %q", err.Error())
	}
}

// TestRefresher_RefusesARedirectWithoutRenderingTheLocation proves that the
// refresh refuses a provider's redirect instead of following it.
//
// The refresh token is in the request, and a redirect would let the provider
// send the single-use credential to a destination of its choosing. The
// redirect target is a REACHABLE server here: a refresher that followed the
// redirect would deliver the token to it and renew the registration.
func TestRefresher_RefusesARedirectWithoutRenderingTheLocation(t *testing.T) {
	record := expiredTestProfile("acct-one")
	store := newTestProfileStore(t)
	saveTestProfile(t, store, record)

	var destinationRequests atomic.Int64
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		destinationRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"redirected-access-token","refresh_token":"redirected-refresh-token","expires_in":3600}`)
	}))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Location", destination.URL+"?credential="+url.QueryEscape(r.PostForm.Get("refresh_token")))
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	// The ordinary client: the refresher must refuse the redirect even when
	// the client would follow one.
	refresher := newTestRefresher(t, store, server.URL, &http.Client{})
	_, err := refresher.Refresh(context.Background(), record.ID)
	if err == nil {
		t.Fatal("the refresh followed the provider's redirect")
	}
	requireRefreshClass(t, err, RefreshClassRejected)
	var refreshErr *RefreshError
	if !errors.As(err, &refreshErr) {
		t.Fatalf("the refresh failed with %v, want a classified refresh error", err)
	}
	if refreshErr.Code != "redirect_refused" {
		t.Errorf("the refresh error carries the code %q, want the classified redirect_refused", refreshErr.Code)
	}
	if got := destinationRequests.Load(); got != 0 {
		t.Errorf("the redirect destination received %d requests, want 0", got)
	}
	if strings.Contains(err.Error(), record.RefreshToken) {
		t.Errorf("the refresh error discloses the refresh token: %q", err.Error())
	}
	if strings.Contains(err.Error(), destination.URL) || strings.Contains(err.Error(), "credential=") {
		t.Errorf("the refresh error renders the provider's Location: %q", err.Error())
	}
	stored, loadErr := store.Load(record.ID)
	if loadErr != nil {
		t.Fatalf("load the registration: %v", loadErr)
	}
	if stored.RefreshToken != record.RefreshToken || stored.AccessToken != record.AccessToken {
		t.Error("a refused redirect erased the saved credentials")
	}
	if stored.NeedsReauth {
		t.Error("a refused redirect marked the registration as needing a new authorization")
	}
}

// TestExchange_TransportFailureKeepsItsClassification proves that the
// classified transport failure still tells a cancellation and a deadline
// apart, without rendering the transport's own text.
func TestExchange_TransportFailureKeepsItsClassification(t *testing.T) {
	h := newHarness(t, func(config *AuthorizerConfig) {
		config.HTTPClient = &http.Client{}
	})
	attempt, err := h.authorizer.BeginRegistration(context.Background(), h.hostID(t))
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	defer func() { _ = attempt.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = h.authorizer.Exchange(ctx, attempt, "oaiapp_issued_1", "auth-code-1")
	if err == nil {
		t.Fatal("the exchange succeeded with a cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the exchange error is %v, want the cancellation", err)
	}
	if _, ok := errors.AsType[*TransportError](err); !ok {
		t.Errorf("the exchange error is %v, want a classified transport failure", err)
	}
}

// TestTransportError_ErrorCannotDiscloseTheTransportText proves that the
// classified transport failure renders its class and status without the
// transport's own message.
//
// The transport's message can carry a URL the provider chose, and that URL
// can carry the authorization code. The rendered error is operator-facing,
// so the cause text must never reach it.
func TestTransportError_ErrorCannotDiscloseTheTransportText(t *testing.T) {
	const transportText = "dial unsupported-scheme://destination/?credential=synthetic-authorization-code"
	cases := []struct {
		name string
		err  *TransportError
		want []string
	}{
		{
			name: "without a status",
			err:  &TransportError{Class: "transport", cause: errors.New(transportText)},
			want: []string{"transport"},
		},
		{
			name: "with a status",
			err:  &TransportError{Class: "body", StatusCode: http.StatusBadGateway, cause: errors.New(transportText)},
			want: []string{"body", "502"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			message := testCase.err.Error()
			if strings.Contains(message, "synthetic-authorization-code") || strings.Contains(message, "unsupported-scheme") {
				t.Errorf("the rendered error discloses the transport text: %q", message)
			}
			for _, want := range testCase.want {
				if !strings.Contains(message, want) {
					t.Errorf("the rendered error %q does not name %q", message, want)
				}
			}
		})
	}
}

// TestClassifyProviderErrorCode proves that only documented codes are
// rendered.
func TestClassifyProviderErrorCode(t *testing.T) {
	for code, want := range map[string]string{
		"access_denied":    "access_denied",
		"invalid_grant":    "invalid_grant",
		"invalid_request":  "invalid_request",
		"server_error":     "server_error",
		"":                 "provider_error",
		"echoed-secret":    "provider_error",
		"INVALID_GRANT":    "provider_error",
		"access_denied ok": "provider_error",
	} {
		if got := classifyProviderErrorCode(code); got != want {
			t.Errorf("classifyProviderErrorCode(%q) = %q, want %q", code, got, want)
		}
	}
}

// TestSafeProviderText_RedactsBeforeItBounds proves the order of the two
// steps: the secrets are removed first, and the result is bounded second.
func TestSafeProviderText_RedactsBeforeItBounds(t *testing.T) {
	secret := strings.Repeat("synthetic-credential-", 20)
	text := strings.Repeat("x", 230) + secret
	safe := safeProviderText(text, 256, secret)
	if strings.Contains(safe, secret[:26]) {
		t.Errorf("the bounded text exposes a prefix of the credential: %q", safe)
	}
	if len([]rune(safe)) > 260 {
		t.Errorf("the bounded text is %d characters long", len([]rune(safe)))
	}
}

// TestTokenErrorRendering_StaysWithinTheBounds proves that the rendered
// error stays bounded even when the provider's text does not.
func TestTokenErrorRendering_StaysWithinTheBounds(t *testing.T) {
	body, err := json.Marshal(map[string]string{
		"error":             "invalid_request",
		"error_description": strings.Repeat("y", 4096),
	})
	if err != nil {
		t.Fatalf("marshal the error body: %v", err)
	}
	authorizer := &Authorizer{}
	tokenErr := authorizer.tokenError(http.StatusBadRequest, body)
	if len([]rune(tokenErr.Description)) > maxErrorDescriptionLength+4 {
		t.Errorf("the description is %d characters long", len([]rune(tokenErr.Description)))
	}
	if len([]rune(tokenErr.Error())) > maxErrorDescriptionLength+128 {
		t.Errorf("the rendered error is %d characters long", len([]rune(tokenErr.Error())))
	}
}

// sprintf is fmt.Sprintf under a name that does not shadow the package.
func sprintf(format string, value any) string {
	return fmt.Sprintf(format, value)
}
