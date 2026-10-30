package siwc

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// ageRegistration makes a registration due for a refresh.
//
// A registration carries a fresh access token, and a refresh that is not
// due does not reach the provider at all.
func ageRegistration(t *testing.T, store *ProfileStore, record ProfileRecord) ProfileRecord {
	t.Helper()
	aged := record
	aged.Expiry = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.Save(aged); err != nil {
		t.Fatalf("age the registration: %v", err)
	}
	return aged
}

// The interleaving proofs of the credential publications.
//
// The refresh, the returning authorization and the sign-out all write the
// credentials of one registration, and a rotating credential is lost when
// two of them write over each other. The proofs below use the REAL
// authorization service and the REAL store: the property is a property of
// the publication rule, and a stubbed publication would prove nothing
// about it.

// TestReauthorization_IsNotOverwrittenByAConcurrentRefresh proves that a
// refresh cannot replace a session that was reauthorized while the refresh
// waited for the provider.
//
// The schedule is the one that loses a session:
//
//  1. the refresh takes the rotation lock, reads the credential and waits
//     for the provider;
//  2. the returning authorization completes and publishes its fresh token
//     set; and
//  3. the refresh finishes and publishes its rotation.
//
// The rotation of step 3 belongs to the credential of step 1. Publishing it
// would replace the session of step 2 with a result of the older session -
// and the older session's refresh token is the one the provider consumed.
func TestReauthorization_IsNotOverwrittenByAConcurrentRefresh(t *testing.T) {
	harness := newHarness(t)
	harness.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})
	registered, err := harness.authorizer.Register(context.Background(), RegisterRequest{
		ProfileID: "acct-one",
		Label:     "work",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	registered = ageRegistration(t, harness.profiles, registered)
	// The refresh waits for the provider.
	provider := newFakeProvider(t)
	refresher := newTestRefresher(
		t, harness.profiles, provider.endpoint("/api/accounts/oauth/token"), provider.client(),
	)
	release := provider.gateToken()
	defer release()
	refreshDone := make(chan error, 1)
	go func() {
		_, err := refresher.Refresh(context.Background(), registered.ID)
		refreshDone <- err
	}()
	provider.waitForTokenRequest(t)

	// The returning authorization completes while the refresh waits.
	reauthorized, err := harness.authorizer.Reauthorize(context.Background(), ReauthorizeRequest{
		ProfileID: registered.ID,
	})
	if err != nil {
		t.Fatalf("Reauthorize: %v", err)
	}
	if reauthorized.RefreshToken == registered.RefreshToken {
		t.Fatal("the reauthorization did not replace the credential")
	}

	// The refresh finishes and publishes its rotation.
	release()
	if err := <-refreshDone; err != nil {
		t.Fatalf("the refresh failed: %v", err)
	}

	stored, err := harness.profiles.Load(registered.ID)
	if err != nil {
		t.Fatalf("load the registration: %v", err)
	}
	if stored.RefreshToken != reauthorized.RefreshToken {
		t.Errorf(
			"the stored refresh token = %q, want the reauthorization's %q",
			stored.RefreshToken, reauthorized.RefreshToken,
		)
	}
	if stored.AccessToken != reauthorized.AccessToken {
		t.Errorf(
			"the stored access token = %q, want the reauthorization's %q",
			stored.AccessToken, reauthorized.AccessToken,
		)
	}
}

// TestRefresh_TerminalFailureDoesNotClearANewerSession proves that a
// terminal result about an old credential clears nothing.
//
// The refresh of the old credential fails terminally while a returning
// authorization publishes a fresh session. The terminal result belongs to
// the credential that is no longer there, and it must not clear the
// session that is.
func TestRefresh_TerminalFailureDoesNotClearANewerSession(t *testing.T) {
	harness := newHarness(t)
	harness.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})
	registered, err := harness.authorizer.Register(context.Background(), RegisterRequest{
		ProfileID: "acct-one",
		Label:     "work",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	registered = ageRegistration(t, harness.profiles, registered)
	provider := newFakeProvider(t)
	provider.setTokenResponse(400, `{"error":"invalid_grant"}`)
	refresher := newTestRefresher(
		t, harness.profiles, provider.endpoint("/api/accounts/oauth/token"), provider.client(),
	)
	release := provider.gateToken()
	defer release()
	refreshDone := make(chan error, 1)
	go func() {
		_, err := refresher.Refresh(context.Background(), registered.ID)
		refreshDone <- err
	}()
	provider.waitForTokenRequest(t)

	reauthorized, err := harness.authorizer.Reauthorize(context.Background(), ReauthorizeRequest{
		ProfileID: registered.ID,
	})
	if err != nil {
		t.Fatalf("Reauthorize: %v", err)
	}
	release()
	<-refreshDone

	stored, err := harness.profiles.Load(registered.ID)
	if err != nil {
		t.Fatalf("load the registration: %v", err)
	}
	if stored.RefreshToken != reauthorized.RefreshToken {
		t.Errorf(
			"the terminal result replaced the newer session: the stored refresh token is %q, want %q",
			stored.RefreshToken, reauthorized.RefreshToken,
		)
	}
	if stored.NeedsReauth {
		t.Error("a terminal result about an older credential marked the newer session as unusable")
	}
}

// TestLogout_PreservesAConcurrentReauthorization proves that revoking session A
// cannot clear session B, published while the revocation waits for the provider.
func TestLogout_PreservesAConcurrentReauthorization(t *testing.T) {
	harness := newHarness(t)
	harness.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})
	registered, err := harness.authorizer.Register(context.Background(), RegisterRequest{
		ProfileID: "acct-one",
		Label:     "work",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	provider := newFakeProvider(t)
	revoker := newTestRevoker(t, harness.profiles, provider, (&recordedSleeps{}).sleep)
	// The revocation is held in the provider, so that the reauthorization
	// completes while the sign-out is in flight.
	release := provider.gateRevocation()
	defer release()
	logoutDone := make(chan struct{})
	var result LogoutResult
	var logoutErr error
	go func() {
		defer close(logoutDone)
		result, logoutErr = revoker.Logout(context.Background(), registered.ID)
	}()
	provider.waitForRevocation(t)

	reauthorized, err := harness.authorizer.Reauthorize(context.Background(), ReauthorizeRequest{
		ProfileID: registered.ID,
		Label:     "personal",
	})
	if err != nil {
		t.Fatalf("Reauthorize: %v", err)
	}
	if reauthorized.RefreshToken == registered.RefreshToken {
		t.Fatal("reauthorization did not replace the credential")
	}
	beforeClear, err := harness.profiles.Load(registered.ID)
	if err != nil {
		t.Fatalf("load the reauthorized registration: %v", err)
	}
	release()
	<-logoutDone
	if logoutErr != nil {
		t.Fatalf("Logout: %v", logoutErr)
	}
	if result.LocalTokensCleared || result.RemoteRevocationConfirmed || result.RevocationOutcome != RevocationOutcomeSuperseded {
		t.Errorf("logout misreported the surviving session: %+v", result)
	}

	stored, err := harness.profiles.Load(registered.ID)
	if err != nil {
		t.Fatalf("load the registration: %v", err)
	}
	if !reflect.DeepEqual(stored, beforeClear) {
		t.Error("logout changed the concurrently reauthorized record")
	}
}
