package siwc

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

// The regressions of the required commit.
//
// The provider has already committed when the connector records the
// outcome: a rotated refresh token, a refused token, a confirmed
// revocation, a fresh token set. The local record of that outcome is the
// only copy of a renewable credential, and a commit that is contingent on
// the CALLER's context loses it.
//
// The mechanism is a response body that cancels the caller's context at
// EOF: the whole provider answer is delivered, and the context is already
// done when the connector reaches its commit. It is deterministic and it
// sleeps for nothing.

// cancellingBody is a response body that cancels the caller's context when
// the WHOLE body has been delivered.
//
// The cancellation happens at the EOF read, so the caller has received every
// byte of the provider's answer and the context is already done when the
// connector reaches its publication step.
type cancellingBody struct {
	data   []byte
	offset int
	cancel context.CancelFunc
	once   sync.Once
}

func (b *cancellingBody) Read(p []byte) (int, error) {
	if b.offset < len(b.data) {
		n := copy(p, b.data[b.offset:])
		b.offset += n
		return n, nil
	}
	b.once.Do(b.cancel)
	return 0, io.EOF
}

func (b *cancellingBody) Close() error { return nil }

// cancellingTransport answers every request with the same canned response
// and cancels the caller's context at the end of it.
type cancellingTransport struct {
	status int
	body   string
	cancel context.CancelFunc

	mu    sync.Mutex
	forms []string
}

func (t *cancellingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	form := ""
	if req.Body != nil {
		data, err := io.ReadAll(req.Body)
		if err == nil {
			form = string(data)
		}
	}
	t.mu.Lock()
	t.forms = append(t.forms, form)
	t.mu.Unlock()
	cancel := t.cancel
	if cancel == nil {
		cancel = func() {}
	}
	return &http.Response{
		StatusCode: t.status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       &cancellingBody{data: []byte(t.body), cancel: cancel},
		Request:    req,
	}, nil
}

func (t *cancellingTransport) requestCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.forms)
}

func (t *cancellingTransport) form(index int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if index >= len(t.forms) {
		return ""
	}
	return t.forms[index]
}

// TestRefresh_CancellationAfterTheRotationDoesNotLoseTheReplacement proves
// that the replacement survives a cancellation that arrives after the
// provider's answer.
//
// The provider consumed the saved token and answered with a replacement. The
// caller's context is done when the connector records the outcome, and the
// replacement is the only copy of the renewable credential: the commit is
// the connector's obligation and not the caller's withdrawal.
func TestRefresh_CancellationAfterTheRotationDoesNotLoseTheReplacement(t *testing.T) {
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := &cancellingTransport{
		status: http.StatusOK,
		body: `{"access_token":"refreshed-access-token",` +
			`"refresh_token":"rotated-refresh-token","token_type":"bearer",` +
			`"expires_in":3600,"scope":"openid offline_access chatgpt.tokens.use.direct"}`,
		cancel: cancel,
	}
	refresher := newTestRefresher(t, store, "https://provider.invalid/token", &http.Client{
		Transport: transport,
	})

	if _, err := refresher.Refresh(ctx, record.ID); err != nil {
		t.Fatalf("the refresh failed after the provider answered: %v", err)
	}
	if requests := transport.requestCount(); requests != 1 {
		t.Errorf("the provider was asked %d times, want 1", requests)
	}
	stored, err := store.Load(record.ID)
	if err != nil {
		t.Fatalf("load the registration: %v", err)
	}
	if stored.RefreshToken != "rotated-refresh-token" {
		t.Errorf("the replacement was lost: the store holds %q", stored.RefreshToken)
	}
	if stored.AccessToken != "refreshed-access-token" {
		t.Errorf("the replacement access token was lost: the store holds %q", stored.AccessToken)
	}
	if stored.NeedsReauth {
		t.Error("a successful rotation marked the registration as needing a new authorization")
	}
}

// TestRefresh_CancellationAfterTheTerminalAnswerStillQuarantines proves
// that the quarantine survives a cancellation that arrives after the
// provider's answer.
//
// The provider refused the token. The unusable tokens must be cleared and
// the registration must be marked as needing a new authorization, or the
// next refresh presents the refused token again.
func TestRefresh_CancellationAfterTheTerminalAnswerStillQuarantines(t *testing.T) {
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := &cancellingTransport{
		status: http.StatusBadRequest,
		body:   `{"error":"refresh_token_reused"}`,
		cancel: cancel,
	}
	refresher := newTestRefresher(t, store, "https://provider.invalid/token", &http.Client{
		Transport: transport,
	})

	if _, err := refresher.Refresh(ctx, record.ID); err == nil {
		t.Fatal("a refused token was reported as a success")
	}
	stored, err := store.Load(record.ID)
	if err != nil {
		t.Fatalf("load the registration: %v", err)
	}
	if stored.RefreshToken != "" {
		t.Errorf("the refused refresh token was kept: %q", stored.RefreshToken)
	}
	if stored.AccessToken != "" {
		t.Errorf("the access token of a refused registration was kept: %q", stored.AccessToken)
	}
	if !stored.NeedsReauth {
		t.Error("the registration is not marked as needing a new authorization")
	}
}

// TestLogout_CancellationAfterTheConfirmedRevocationStillSignsOutLocally
// proves that the local sign-out survives a cancellation that arrives after
// the provider confirmed the revocation.
//
// The provider confirmed the revocation with the documented empty HTTP 200.
// The local record of that confirmation is what stops the connector from
// using the tokens, so the commit is the connector's obligation.
func TestLogout_CancellationAfterTheConfirmedRevocationStillSignsOutLocally(t *testing.T) {
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := &cancellingTransport{status: http.StatusOK, body: "", cancel: cancel}
	revoker, err := NewRevoker(RevokerConfig{
		Profiles:   store,
		OIDC:       newCancellingOIDC(t),
		HTTPClient: &http.Client{Transport: transport},
		Sleep:      func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewRevoker: %v", err)
	}

	result, err := revoker.Logout(ctx, record.ID)
	if err != nil {
		t.Fatalf("the sign-out failed after the provider confirmed the revocation: %v", err)
	}
	if !result.RemoteRevocationConfirmed {
		t.Error("the confirmed revocation was not reported")
	}
	if !result.LocalTokensCleared {
		t.Error("the local sign-out was lost")
	}
	stored, loadErr := store.Load(record.ID)
	if loadErr != nil {
		t.Fatalf("load the registration: %v", loadErr)
	}
	if stored.RefreshToken != "" {
		t.Errorf("the refresh token was kept: %q", stored.RefreshToken)
	}
	if stored.AccessToken != "" {
		t.Errorf("the access token was kept: %q", stored.AccessToken)
	}
	if !stored.NeedsReauth {
		t.Error("the signed-out registration is not marked as needing a new authorization")
	}
}

// TestReauthorization_CancellationAfterTheTokenSetStillPublishesIt proves
// that the fresh credential set survives a cancellation that arrives after
// the provider issued it.
//
// The sign-in has received a token set that the provider will not issue
// again. The publication is the record of it, and it is the connector's
// obligation to record it.
//
// The proof drives the REAL FLOW - a returning authorization through the
// authorization service - and not the store's publication in isolation: the
// caller's cancellation must not prevent the commit on the path a caller
// actually takes.
func TestReauthorization_CancellationAfterTheTokenSetStillPublishesIt(t *testing.T) {
	harness := newHarness(t)
	harness.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})
	registered, err := harness.authorizer.Register(context.Background(), RegisterRequest{
		ProfileID: "acct-one",
		Label:     "work",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// The returning authorization exchanges its code through a transport
	// that cancels the caller's context at the end of the answer: the whole
	// token set is delivered, and the context is done when the
	// authorization service publishes it.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := &cancellingTransport{
		status: http.StatusOK,
		body: `{"access_token":"reauthorized-access-token",` +
			`"refresh_token":"reauthorized-refresh-token","token_type":"bearer",` +
			`"expires_in":3600,"scope":"openid offline_access chatgpt.tokens.use.direct"}`,
		cancel: cancel,
	}
	authorizer, err := NewAuthorizer(AuthorizerConfig{
		AuthorizationEndpoint: harness.fake.server.URL + "/api/accounts/authorize",
		TokenEndpoint:         "https://provider.invalid/token",
		HTTPClient:            &http.Client{Transport: transport},
		BrowserOpener:         harness.authorizer.config.BrowserOpener,
		Profiles:              harness.profiles,
		Hosts:                 harness.hosts,
		Validator:             harness.validator,
	})
	if err != nil {
		t.Fatalf("NewAuthorizer: %v", err)
	}

	if _, err := authorizer.Reauthorize(ctx, ReauthorizeRequest{
		ProfileID: registered.ID,
	}); err != nil {
		t.Fatalf("the returning authorization failed after the provider answered: %v", err)
	}
	stored, err := harness.profiles.Load(registered.ID)
	if err != nil {
		t.Fatalf("load the registration: %v", err)
	}
	if stored.RefreshToken != "reauthorized-refresh-token" {
		t.Errorf("the fresh credential set was lost: %q", stored.RefreshToken)
	}
	if stored.AccessToken != "reauthorized-access-token" {
		t.Errorf("the fresh access token was lost: %q", stored.AccessToken)
	}
}

// TestRefresh_CancellationBeforeTheProviderAnswerIsStillRefused proves that
// the PRE-PROVIDER cancellation is still the caller's withdrawal.
//
// The caller's context is cancelled before the request leaves: there is no
// provider outcome, so there is nothing to reconcile, and the cancellation
// is reported as the caller's own.
func TestRefresh_CancellationBeforeTheProviderAnswerIsStillRefused(t *testing.T) {
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	transport := &cancellingTransport{
		status: http.StatusOK,
		body:   `{"access_token":"refreshed-access-token","refresh_token":"rotated"}`,
		cancel: cancel,
	}
	refresher := newTestRefresher(t, store, "https://provider.invalid/token", &http.Client{
		Transport: transport,
	})

	_, err := refresher.Refresh(ctx, record.ID)
	if err == nil {
		t.Fatal("a cancelled caller reported a success")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the failure is not the caller's cancellation: %v", err)
	}
	if requests := transport.requestCount(); requests != 0 {
		t.Errorf("the provider was asked %d times, want 0", requests)
	}
	stored, loadErr := store.Load(record.ID)
	if loadErr != nil {
		t.Fatalf("load the registration: %v", loadErr)
	}
	if stored.RefreshToken != record.RefreshToken {
		t.Error("a pre-provider cancellation erased the credentials")
	}
}

// newCancellingOIDC returns an OIDC client that publishes a revocation
// endpoint.
func newCancellingOIDC(t *testing.T) *OIDCClient {
	t.Helper()
	document := `{"issuer":"https://provider.invalid",` +
		`"authorization_endpoint":"https://provider.invalid/authorize",` +
		`"token_endpoint":"https://provider.invalid/token",` +
		`"jwks_uri":"https://provider.invalid/jwks",` +
		`"revocation_endpoint":"https://provider.invalid/revoke"}`
	client, err := NewOIDCClient(OIDCClientConfig{
		DiscoveryURL: "https://provider.invalid/.well-known/openid-configuration",
		HTTPClient: &http.Client{Transport: &cancellingTransport{
			status: http.StatusOK,
			body:   document,
		}},
	})
	if err != nil {
		t.Fatalf("NewOIDCClient: %v", err)
	}
	return client
}
