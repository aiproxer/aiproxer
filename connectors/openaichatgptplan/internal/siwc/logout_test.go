package siwc

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"
)

// Tests for the sign-out.
//
// The contract under test is requirement 2.14 of the migration specification:
// the sign-out ends the renewable session through the revocation endpoint the
// provider's OIDC configuration names, retries a network or 5xx failure with
// a bounded backoff while the refresh token is still available, and
// distinguishes a confirmed remote revocation from a local-only completion.
//
// The proofs live at the wire and at the store: the request the provider
// received, the answer it gave, and the record that is on the disk
// afterwards.

// recordedSleeps records the delays a sign-out waited for.
type recordedSleeps struct {
	mu     sync.Mutex
	delays []time.Duration
}

// sleep records the delay and returns at once.
//
// The proof must not wait for real time: a test that sleeps is a test whose
// duration nobody controls.
func (r *recordedSleeps) sleep(_ context.Context, delay time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.delays = append(r.delays, delay)
	return nil
}

// recorded returns the delays that were waited for.
func (r *recordedSleeps) recorded() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Duration(nil), r.delays...)
}

// newTestRevoker returns a revoker over the given store and provider.
func newTestRevoker(
	t *testing.T,
	store *ProfileStore,
	fake *fakeProvider,
	sleep func(context.Context, time.Duration) error,
) *Revoker {
	t.Helper()
	oidc, err := NewOIDCClient(OIDCClientConfig{
		DiscoveryURL: fake.endpoint("/.well-known/openid-configuration"),
		HTTPClient:   fake.client(),
	})
	if err != nil {
		t.Fatalf("NewOIDCClient: %v", err)
	}
	revoker, err := NewRevoker(RevokerConfig{
		Profiles:   store,
		OIDC:       oidc,
		HTTPClient: fake.client(),
		Sleep:      sleep,
	})
	if err != nil {
		t.Fatalf("NewRevoker: %v", err)
	}
	return revoker
}

// TestLogout_ConfirmsTheRemoteRevocation proves the confirmed outcome.
//
// The request is form-encoded and carries the saved refresh token, the token
// type hint and the issued client id of the registration. The provider's
// answer is the documented success response - an EMPTY HTTP 200 - and the
// sign-out clears the local tokens while it retains the account mapping and
// this host's id.
func TestLogout_ConfirmsTheRemoteRevocation(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	revoker := newTestRevoker(t, store, fake, (&recordedSleeps{}).sleep)

	result, err := revoker.Logout(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if !result.RemoteRevocationConfirmed {
		t.Error("the remote revocation was not confirmed")
	}
	if result.RevocationOutcome != RevocationOutcomeConfirmed {
		t.Errorf("the outcome = %q, want %q", result.RevocationOutcome, RevocationOutcomeConfirmed)
	}
	if !result.LocalTokensCleared {
		t.Error("the local tokens were not cleared")
	}
	if result.Guidance != "" {
		t.Errorf("a confirmed revocation carries guidance: %q", result.Guidance)
	}
	form := fake.lastRevocation(t)
	if got := form.Get("token"); got != record.RefreshToken {
		t.Error("the revocation does not carry the saved refresh token")
	}
	if got := form.Get("token_type_hint"); got != "refresh_token" {
		t.Errorf("the token type hint = %q, want %q", got, "refresh_token")
	}
	if got := form.Get("client_id"); got != record.IssuedClientID {
		t.Errorf("the client id = %q, want the issued client id of the registration %q", got, record.IssuedClientID)
	}
	stored, loadErr := store.Load(record.ID)
	if loadErr != nil {
		t.Fatalf("load the registration: %v", loadErr)
	}
	if stored.AccessToken != "" || stored.RefreshToken != "" || stored.IDToken != "" {
		t.Error("the local tokens were not cleared")
	}
	if !stored.NeedsReauth {
		t.Error("the signed-out registration is not marked as needing a new authorization")
	}
	// The account mapping and this host's id are retained for a later
	// sign-in.
	if stored.Subject != record.Subject {
		t.Error("the subject was not retained")
	}
	if stored.IssuedClientID != record.IssuedClientID {
		t.Error("the issued client id was not retained")
	}
	if stored.HostID != record.HostID {
		t.Error("the host identity was not retained")
	}
}

// TestLogout_AcceptsAnAlreadyInvalidTokenAsSuccess proves the documented
// success semantics of an already-invalid token.
//
// The documentation: "An empty HTTP 200 is the revocation success response,
// including for an already-invalid token." The provider answers the empty
// 200, and the sign-out reports a confirmed revocation.
func TestLogout_AcceptsAnAlreadyInvalidTokenAsSuccess(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	// The token was already invalidated elsewhere: the provider never
	// issued it.
	record.RefreshToken = "a-token-the-provider-never-issued"
	saveTestProfile(t, store, record)
	revoker := newTestRevoker(t, store, fake, (&recordedSleeps{}).sleep)

	result, err := revoker.Logout(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if !result.RemoteRevocationConfirmed {
		t.Error("the revocation of an already-invalid token was not confirmed")
	}
	if !result.LocalTokensCleared {
		t.Error("the local tokens were not cleared")
	}
}

// TestLogout_DoesNotConfirmAResponseThatIsNotTheDocumentedSuccess proves
// that a 200 is only a confirmation when it is EMPTY.
//
// The documented success response is an empty HTTP 200. A 200 that CARRIES
// something is not that response, and the sign-out must not report a
// confirmation it did not observe.
func TestLogout_DoesNotConfirmAResponseThatIsNotTheDocumentedSuccess(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	fake.setRevocationResponse(http.StatusOK, `{"error":"invalid_client"}`)
	revoker := newTestRevoker(t, store, fake, (&recordedSleeps{}).sleep)

	result, err := revoker.Logout(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if result.RemoteRevocationConfirmed {
		t.Error("a response that is not the documented success was confirmed")
	}
	if result.RevocationOutcome != RevocationOutcomeUnexpected {
		t.Errorf("the outcome = %q, want %q", result.RevocationOutcome, RevocationOutcomeUnexpected)
	}
	if !result.LocalTokensCleared {
		t.Error("the local tokens were not cleared")
	}
	if result.Guidance == "" {
		t.Error("an unconfirmed revocation carries no guidance")
	}
}

// TestLogout_RetriesARetryableFailureUntilTheProviderConfirmsTheSession
// proves that the retry stops as soon as the provider confirms.
//
// The provider fails once and confirms the second time: the sign-out reports
// a confirmed revocation, and the provider was asked exactly twice.
func TestLogout_RetriesARetryableFailureUntilTheProviderConfirmsTheSession(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	revoker := newTestRevoker(t, store, fake, (&recordedSleeps{}).sleep)

	// The first attempt fails, the second one is confirmed. The fake
	// answers the first request with a 5xx and every later one with the
	// documented success.
	var attempts int
	var mu sync.Mutex
	fake.observeRevocation(func(url.Values) {
		mu.Lock()
		defer mu.Unlock()
		attempts++
		if attempts == 1 {
			fake.setRevocationResponse(http.StatusServiceUnavailable, "")
			return
		}
		fake.setRevocationResponse(http.StatusOK, "")
	})
	result, err := revoker.Logout(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if !result.RemoteRevocationConfirmed {
		t.Errorf("the revocation was not confirmed: %q", result.RevocationOutcome)
	}
	if requests := fake.revocationCount(); requests != 2 {
		t.Errorf("the provider was asked %d times, want 2", requests)
	}
}

// TestLogout_CompletesLocallyWhenTheRevocationCannotBeConfirmed proves the
// local-only outcome.
//
// The provider keeps failing. The sign-out completes LOCALLY: the tokens are
// cleared, the account mapping is retained, and the result says that the
// remote revocation was not confirmed and names the step the operator has
// to take.
func TestLogout_CompletesLocallyWhenTheRevocationCannotBeConfirmed(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	fake.setRevocationResponse(http.StatusServiceUnavailable, "")
	sleeps := &recordedSleeps{}
	revoker := newTestRevoker(t, store, fake, sleeps.sleep)

	result, err := revoker.Logout(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if result.RemoteRevocationConfirmed {
		t.Error("a provider that always failed confirmed the revocation")
	}
	if result.RevocationOutcome != RevocationOutcomeServer {
		t.Errorf("the outcome = %q, want %q", result.RevocationOutcome, RevocationOutcomeServer)
	}
	if !result.LocalTokensCleared {
		t.Error("the local tokens were not cleared")
	}
	if result.Guidance != RevocationGuidanceUnconfirmed {
		t.Errorf("the guidance = %q, want the unconfirmed-revocation guidance", result.Guidance)
	}
	if requests := fake.revocationCount(); requests != defaultRevocationAttempts {
		t.Errorf("the provider was asked %d times, want %d", requests, defaultRevocationAttempts)
	}
	// The attempts are made with a growing, bounded delay.
	delays := sleeps.recorded()
	if len(delays) != defaultRevocationAttempts-1 {
		t.Fatalf("the sign-out waited %d times, want %d", len(delays), defaultRevocationAttempts-1)
	}
	if delays[0] >= delays[1] {
		t.Errorf("the delay did not grow: %v", delays)
	}
	for _, delay := range delays {
		if delay > maxRevocationBackoff {
			t.Errorf("the delay %s is above the bound %s", delay, maxRevocationBackoff)
		}
	}
	stored, loadErr := store.Load(record.ID)
	if loadErr != nil {
		t.Fatalf("load the registration: %v", loadErr)
	}
	if stored.AccessToken != "" || stored.RefreshToken != "" {
		t.Error("the local tokens were not cleared")
	}
	if stored.Subject != record.Subject || stored.IssuedClientID != record.IssuedClientID {
		t.Error("the account mapping was not retained")
	}
	if stored.HostID != record.HostID {
		t.Error("the host identity was not retained")
	}
}

// TestLogout_RetriesANetworkFailure proves that a network failure is
// retried and bounded.
//
// The provider drops the connection of the revocation endpoint. The attempts
// are bounded, and the sign-out completes locally.
func TestLogout_RetriesANetworkFailure(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	fake.breakRevocation()
	revoker := newTestRevoker(t, store, fake, (&recordedSleeps{}).sleep)

	result, err := revoker.Logout(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if result.RemoteRevocationConfirmed {
		t.Error("an unreachable provider confirmed the revocation")
	}
	if result.RevocationOutcome != RevocationOutcomeNetwork {
		t.Errorf("the outcome = %q, want %q", result.RevocationOutcome, RevocationOutcomeNetwork)
	}
	if !result.LocalTokensCleared {
		t.Error("the local tokens were not cleared")
	}
	if result.Guidance == "" {
		t.Error("an unconfirmed revocation carries no guidance")
	}
	if requests := fake.revocationCount(); requests != defaultRevocationAttempts {
		t.Errorf("the provider was asked %d times, want %d", requests, defaultRevocationAttempts)
	}
}

// TestLogout_ReportsAnUnreachableProviderConfiguration proves the outcome of
// a provider whose configuration cannot be read.
//
// The revocation endpoint cannot be resolved, so the session cannot be ended
// remotely. The attempts are bounded, and the sign-out completes locally.
func TestLogout_ReportsAnUnreachableProviderConfiguration(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	// The provider is closed, so the configuration cannot be read.
	fake.server.Close()
	revoker := newTestRevoker(t, store, fake, (&recordedSleeps{}).sleep)

	result, err := revoker.Logout(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if result.RemoteRevocationConfirmed {
		t.Error("an unreachable provider confirmed the revocation")
	}
	if result.RevocationOutcome != RevocationOutcomeUnavailable {
		t.Errorf("the outcome = %q, want %q", result.RevocationOutcome, RevocationOutcomeUnavailable)
	}
	if !result.LocalTokensCleared {
		t.Error("the local tokens were not cleared")
	}
}

// TestLogout_AttemptsTheRevocationBeforeItClearsTheTokens proves the
// ORDER of the two halves.
//
// The revocation needs the refresh token that the clearing destroys, so the
// revocation is attempted first.
func TestLogout_AttemptsTheRevocationBeforeItClearsTheTokens(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	var tokenPresentAtRevocation bool
	fake.observeRevocation(func(url.Values) {
		stored, err := store.Load(record.ID)
		if err != nil {
			return
		}
		tokenPresentAtRevocation = stored.RefreshToken == record.RefreshToken
	})
	revoker := newTestRevoker(t, store, fake, (&recordedSleeps{}).sleep)

	if _, err := revoker.Logout(context.Background(), record.ID); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if !tokenPresentAtRevocation {
		t.Error("the tokens were cleared before the revocation was attempted")
	}
}

// TestLogout_UsesTheRevocationEndpointFromTheDiscoveryDocument proves that
// the endpoint comes from the provider's configuration.
//
// The connector never guesses an endpoint: the request arrives at the
// revocation endpoint the discovery document names.
func TestLogout_UsesTheRevocationEndpointFromTheDiscoveryDocument(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	revoker := newTestRevoker(t, store, fake, (&recordedSleeps{}).sleep)

	if _, err := revoker.Logout(context.Background(), record.ID); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if fake.revocationCount() != 1 {
		t.Fatalf("the revocation endpoint was called %d times, want 1", fake.revocationCount())
	}
}

// TestLogout_ReportsThatThereWasNoSessionToRevoke proves the outcome of a
// registration without a renewable session.
//
// There is nothing to revoke, so no request is made, and the connector does
// not claim a confirmation it did not observe.
func TestLogout_ReportsThatThereWasNoSessionToRevoke(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	record.RefreshToken = ""
	saveTestProfile(t, store, record)
	revoker := newTestRevoker(t, store, fake, (&recordedSleeps{}).sleep)

	result, err := revoker.Logout(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if result.RemoteRevocationConfirmed {
		t.Error("a sign-out without a session confirmed a revocation")
	}
	if result.RevocationOutcome != RevocationOutcomeNoSession {
		t.Errorf("the outcome = %q, want %q", result.RevocationOutcome, RevocationOutcomeNoSession)
	}
	if fake.revocationCount() != 0 {
		t.Error("the revocation endpoint was called for a registration without a session")
	}
	if !result.LocalTokensCleared {
		t.Error("the local tokens were not cleared")
	}
}

// TestLogout_TheFinalAttemptIsWithdrawnUnlessTheRevocationWasConfirmed
// proves that the caller's withdrawal is honoured when the LAST revocation
// attempt received an answer that confirmed nothing.
//
// The provider answers with a 503 and the WHOLE answer cancels the caller's
// context at EOF: the caller has received every byte of the answer, and the
// context is already done when the connector classifies it. No remote
// revocation was confirmed, and the caller went away - that is a withdrawal,
// not an unconfirmed revocation.
//
// The withdrawal ABORTS the sign-out: the failure is the caller's own, the
// local tokens are not cleared, and the saved credential stays. The CONTROL
// case is the documented success response: an empty HTTP 200 confirms the
// revocation, and the local record of that confirmation is the connector's
// obligation - the sign-out completes locally even though the caller's
// context is done.
func TestLogout_TheFinalAttemptIsWithdrawnUnlessTheRevocationWasConfirmed(t *testing.T) {
	run := func(t *testing.T, status int, body string) (LogoutResult, ProfileRecord, error) {
		t.Helper()
		store := newTestProfileStore(t)
		record := expiredTestProfile("acct-one")
		saveTestProfile(t, store, record)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		transport := &cancellingTransport{status: status, body: body, cancel: cancel}
		revoker, err := NewRevoker(RevokerConfig{
			Profiles:   store,
			OIDC:       newCancellingOIDC(t),
			HTTPClient: &http.Client{Transport: transport},
			// ONE attempt: the answer that arrives is the answer of the
			// LAST attempt, and there is no backoff after it that could
			// notice the cancellation.
			Attempts: 1,
		})
		if err != nil {
			t.Fatalf("NewRevoker: %v", err)
		}
		result, err := revoker.Logout(ctx, record.ID)
		stored, loadErr := store.Load(record.ID)
		if loadErr != nil {
			t.Fatalf("load the registration: %v", loadErr)
		}
		return result, stored, err
	}

	t.Run("nonconfirming", func(t *testing.T) {
		result, stored, err := run(t, http.StatusServiceUnavailable, `{"error":"server_error"}`)
		t.Logf("err=%v outcome=%q cleared=%v stored refresh=%q needsReauth=%v",
			err, result.RevocationOutcome, result.LocalTokensCleared,
			stored.RefreshToken, stored.NeedsReauth)
		if err == nil {
			t.Fatalf("a cancelled final attempt reported SUCCESS with no error")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the failure is not the caller's cancellation: %v", err)
		}
		if result.LocalTokensCleared {
			t.Errorf("the local sign-out was reported as completed although the caller withdrew")
		}
		if stored.RefreshToken == "" {
			t.Errorf("the saved refresh token was erased although no remote outcome was confirmed")
		}
		if stored.NeedsReauth {
			t.Errorf("the registration was marked although the caller withdrew")
		}
	})

	t.Run("confirmed", func(t *testing.T) {
		result, stored, err := run(t, http.StatusOK, "")
		t.Logf("err=%v outcome=%q confirmed=%v cleared=%v stored refresh=%q needsReauth=%v",
			err, result.RevocationOutcome, result.RemoteRevocationConfirmed,
			result.LocalTokensCleared, stored.RefreshToken, stored.NeedsReauth)
		if err != nil {
			t.Fatalf("the sign-out failed although the provider confirmed the revocation: %v", err)
		}
		if !result.RemoteRevocationConfirmed {
			t.Errorf("the confirmed revocation was not reported")
		}
		if !result.LocalTokensCleared {
			t.Errorf("the local sign-out was lost")
		}
		if stored.RefreshToken != "" || stored.AccessToken != "" {
			t.Errorf("the local tokens were kept: refresh=%q access=%q",
				stored.RefreshToken, stored.AccessToken)
		}
		if !stored.NeedsReauth {
			t.Errorf("the signed-out registration is not marked as needing a new authorization")
		}
	})
}

// TestOIDCClient_ThrottlesARepeatedDiscoveryFailure proves the bounded
// backoff of the discovery fetch.
//
// Three sign-ins in a row must not produce three requests: a provider that
// fails is asked once inside the renew window, and the failure of that
// attempt is reported to the callers that follow. The window is bounded, so
// a temporary failure cannot break the sign-in forever.
func TestOIDCClient_ThrottlesARepeatedDiscoveryFailure(t *testing.T) {
	fake := newFakeProvider(t)
	fake.setDiscoveryResponse(http.StatusOK, "this is not a document")
	clock := &fakeClock{now: time.Now()}
	client, err := NewOIDCClient(OIDCClientConfig{
		DiscoveryURL: fake.endpoint("/.well-known/openid-configuration"),
		HTTPClient:   fake.client(),
		Now:          clock.nowFunc,
	})
	if err != nil {
		t.Fatalf("NewOIDCClient: %v", err)
	}
	for range 3 {
		if _, err := client.Discovery(context.Background()); err == nil {
			t.Fatalf("the discovery succeeded against a provider that answers a malformed document")
		}
	}
	// The second and the third attempt are inside the window of the first
	// one, so they are refused without a request.
	if requests := fake.discoveryRequestCount(); requests != 1 {
		t.Fatalf("the provider was asked %d times, want 1", requests)
	}
	clock.advance(time.Second)
	if _, err := client.Discovery(context.Background()); err == nil {
		t.Fatal("the discovery succeeded against a provider that answers a malformed document")
	}
	if requests := fake.discoveryRequestCount(); requests != 1 {
		t.Errorf("the provider was asked %d times, want 1 (the failed attempt closed the window)", requests)
	}
	// The window is bounded: the attempt after it is admitted again.
	clock.advance(defaultRenewInterval)
	if _, err := client.Discovery(context.Background()); err == nil {
		t.Fatal("the discovery succeeded against a provider that answers a malformed document")
	}
	if requests := fake.discoveryRequestCount(); requests != 2 {
		t.Errorf("the provider was asked %d times, want 2 (the window is bounded)", requests)
	}
}
