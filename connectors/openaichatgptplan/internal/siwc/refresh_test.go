package siwc

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// Tests for the serialized rotating refresh.
//
// The contract under test is requirement 2.9 and 2.10 of the migration
// specification: the refresh is serialized per registration, the replacement
// refresh token is persisted atomically, a terminal failure requires a new
// authorization, and a temporary failure leaves the saved credentials alone.
//
// The proofs live at the wire and at the store: the request the provider
// received, the answer it gave, and the record that is on the disk
// afterwards.

// expiredTestProfile returns a registration whose access token has expired.
//
// A refresh is only attempted for a token that is near its expiry, so a
// proof about the refresh needs a record that is due.
func expiredTestProfile(id string) ProfileRecord {
	record := testProfileRecord(id)
	record.Expiry = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	return record
}

// saveTestProfile publishes a registration in the store.
func saveTestProfile(t *testing.T, store *ProfileStore, record ProfileRecord) {
	t.Helper()
	if err := store.SaveNew(record); err != nil {
		t.Fatalf("save the registration: %v", err)
	}
}

// newTestRefresher returns a refresher over the given store.
func newTestRefresher(
	t *testing.T,
	store *ProfileStore,
	endpoint string,
	client *http.Client,
) *Refresher {
	t.Helper()
	refresher, err := NewRefresher(RefresherConfig{
		Profiles:      store,
		TokenEndpoint: endpoint,
		HTTPClient:    client,
		Now:           time.Now,
	})
	if err != nil {
		t.Fatalf("NewRefresher: %v", err)
	}
	return refresher
}

// requireRefreshClass asserts the class of a classified refresh failure.
func requireRefreshClass(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("the refresh succeeded, want the %q class", want)
	}
	var classified *RefreshError
	if !errors.As(err, &classified) {
		t.Fatalf("the failure is not a classified refresh failure: %v", err)
	}
	if classified.Class != want {
		t.Fatalf("the failure class = %q, want %q", classified.Class, want)
	}
}

// TestRefresher_SendsTheDocumentedRefreshRequest proves that the refresh
// carries the documented request.
//
// The request is form-encoded, it carries the grant type, the issued client
// id of the REGISTRATION, the saved refresh token and the plan resource -
// and it does NOT carry a scope, because the documentation requires the
// omission so that the grant is retained.
func TestRefresher_SendsTheDocumentedRefreshRequest(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	refresher := newTestRefresher(t, store, fake.endpoint("/api/accounts/oauth/token"), fake.client())

	if _, err := refresher.Refresh(context.Background(), record.ID); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	form := fake.lastTokenRequest(t)
	if got := form.Get("grant_type"); got != "refresh_token" {
		t.Errorf("the grant type = %q, want %q", got, "refresh_token")
	}
	if got := form.Get("client_id"); got != record.IssuedClientID {
		t.Errorf("the client id = %q, want the issued client id of the registration %q", got, record.IssuedClientID)
	}
	if got := form.Get("refresh_token"); got != record.RefreshToken {
		t.Error("the request does not carry the saved refresh token")
	}
	if got := form.Get("resource"); got != PlanResource {
		t.Errorf("the resource = %q, want %q", got, PlanResource)
	}
	if _, present := form["scope"]; present {
		t.Error("the refresh request carries a scope; the documentation requires it to be omitted")
	}
}

// TestRefresher_PersistsTheReplacementRefreshToken proves that the
// replacement is on the disk.
//
// The proof reads the STORE, not the value the refresh returned: a
// replacement that was returned but not persisted would leave the
// registration with the token the provider consumed.
func TestRefresher_PersistsTheReplacementRefreshToken(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	refresher := newTestRefresher(t, store, fake.endpoint("/api/accounts/oauth/token"), fake.client())

	renewed, err := refresher.Refresh(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	stored, err := store.Load(record.ID)
	if err != nil {
		t.Fatalf("load the registration: %v", err)
	}
	if stored.RefreshToken != "rotated-refresh-token" {
		t.Error("the replacement refresh token was not persisted")
	}
	if stored.AccessToken != "refreshed-access-token" {
		t.Error("the refreshed access token was not persisted")
	}
	if stored.RefreshToken == record.RefreshToken {
		t.Error("the registration still holds the token the provider consumed")
	}
	if !stored.Expiry.After(time.Now()) {
		t.Errorf("the expiry was not advanced: %s", stored.Expiry)
	}
	if stored.NeedsReauth {
		t.Error("a refreshed registration is marked as needing a new authorization")
	}
	// The account mapping is untouched by a refresh.
	if stored.Subject != record.Subject || stored.IssuedClientID != record.IssuedClientID {
		t.Error("the refresh changed the account mapping")
	}
	if stored.HostID != record.HostID {
		t.Error("the refresh changed the host identity")
	}
	if renewed.RefreshToken != stored.RefreshToken {
		t.Error("the returned record and the stored record disagree")
	}
}

// TestRefresher_ConcurrentRefreshesReachTheProviderOnce proves that the
// refresh is serialized per registration.
//
// A rotating refresh token is single-use: two refreshes of one token
// invalidate each other. The proof therefore counts the requests the
// provider received, not the calls the test made.
func TestRefresher_ConcurrentRefreshesReachTheProviderOnce(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	refresher := newTestRefresher(t, store, fake.endpoint("/api/accounts/oauth/token"), fake.client())

	const callers = 8
	results := make([]ProfileRecord, callers)
	failures := make([]error, callers)
	// The provider is held while the first refresh is in flight, so the
	// other callers really wait for the same registration instead of
	// arriving after it finished.
	release := fake.gateToken()
	defer release()
	var group sync.WaitGroup
	group.Add(callers)
	for index := range callers {
		go func(slot int) {
			defer group.Done()
			results[slot], failures[slot] = refresher.Refresh(context.Background(), record.ID)
		}(index)
	}
	fake.waitForTokenRequest(t)
	release()
	group.Wait()
	for index, err := range failures {
		if err != nil {
			t.Fatalf("the refresh of caller %d failed: %v", index, err)
		}
	}
	if requests := fake.tokenRequestCount(); requests != 1 {
		t.Fatalf("the provider was asked %d times, want 1", requests)
	}
	for index, result := range results {
		if result.AccessToken != "refreshed-access-token" {
			t.Errorf("caller %d received another access token", index)
		}
	}
}

// TestRefresher_TwoRefreshersReachTheProviderOnce proves that two
// REFRESHERS over one store reach the provider once.
//
// Both refreshers run in THIS process, and they are kept apart by the
// store's in-process admission - the admission is the store's, so the two
// refreshers share it. The cross-process case is a different proof:
// TestRefresh_CrossProcessHolderExcludesTheRefresh, where the other holder
// is a separate PROCESS and the operating system's lock is what excludes
// it.
func TestRefresher_TwoRefreshersReachTheProviderOnce(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	endpoint := fake.endpoint("/api/accounts/oauth/token")
	first := newTestRefresher(t, store, endpoint, fake.client())
	second := newTestRefresher(t, store, endpoint, fake.client())

	var group sync.WaitGroup
	group.Add(2)
	failures := make([]error, 2)
	release := fake.gateToken()
	defer release()
	go func() {
		defer group.Done()
		_, failures[0] = first.Refresh(context.Background(), record.ID)
	}()
	go func() {
		defer group.Done()
		_, failures[1] = second.Refresh(context.Background(), record.ID)
	}()
	fake.waitForTokenRequest(t)
	release()
	group.Wait()
	for index, err := range failures {
		if err != nil {
			t.Fatalf("the refresh of refresher %d failed: %v", index, err)
		}
	}
	if requests := fake.tokenRequestCount(); requests != 1 {
		t.Fatalf("the provider was asked %d times, want 1", requests)
	}
}

// TestRefresher_TerminalFailureRequiresReauthorization proves the terminal
// class.
//
// The documentation names the unusable refresh codes, and the recovery is
// the same for all of them: "Clear unusable tokens and repeat OAuth with the
// saved issued client ID." The registration is therefore KEPT - with its
// account mapping and its issued client id - and the unusable tokens are
// gone.
func TestRefresher_TerminalFailureRequiresReauthorization(t *testing.T) {
	for _, code := range []string{
		"invalid_grant",
		"invalid_refresh_token",
		"token_expired",
		"refresh_token_expired",
		"refresh_token_invalidated",
		"refresh_token_reused",
	} {
		t.Run(code, func(t *testing.T) {
			fake := newFakeProvider(t)
			store := newTestProfileStore(t)
			record := expiredTestProfile("acct-one")
			saveTestProfile(t, store, record)
			fake.setTokenResponse(http.StatusBadRequest, `{"error":"`+code+`"}`)
			refresher := newTestRefresher(
				t, store, fake.endpoint("/api/accounts/oauth/token"), fake.client(),
			)

			_, err := refresher.Refresh(context.Background(), record.ID)
			requireRefreshClass(t, err, RefreshClassTerminal)
			if !errors.Is(err, ErrRefreshTerminal) {
				t.Error("the failure is not the terminal sentinel")
			}
			stored, loadErr := store.Load(record.ID)
			if loadErr != nil {
				t.Fatalf("load the registration: %v", loadErr)
			}
			if !stored.NeedsReauth {
				t.Error("the registration is not marked as needing a new authorization")
			}
			if stored.AccessToken != "" {
				t.Error("the unusable access token was kept")
			}
			if stored.RefreshToken != "" {
				t.Error("the unusable refresh token was kept")
			}
			// The account mapping and the issued client id are kept: the
			// reauthorization needs them.
			if stored.IssuedClientID != record.IssuedClientID {
				t.Error("the issued client id was not retained")
			}
			if stored.Subject != record.Subject {
				t.Error("the subject was not retained")
			}
			if stored.HostID != record.HostID {
				t.Error("the host identity was not retained")
			}
		})
	}
}

// TestRefresher_TransientFailureKeepsTheCredentials proves the transient
// class.
//
// A provider that failed or a network that failed says nothing about the
// credential, and the documentation is explicit: "Do not erase credentials
// solely because of a temporary network or infrastructure failure."
func TestRefresher_TransientFailureKeepsTheCredentials(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{name: "the provider failed", status: http.StatusServiceUnavailable, body: `{"error":"server_error"}`},
		{name: "the provider is unavailable", status: http.StatusBadGateway, body: ""},
		{name: "the provider says it is temporary", status: http.StatusServiceUnavailable, body: `{"error":"temporarily_unavailable"}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fake := newFakeProvider(t)
			store := newTestProfileStore(t)
			record := expiredTestProfile("acct-one")
			saveTestProfile(t, store, record)
			fake.setTokenResponse(testCase.status, testCase.body)
			refresher := newTestRefresher(
				t, store, fake.endpoint("/api/accounts/oauth/token"), fake.client(),
			)

			_, err := refresher.Refresh(context.Background(), record.ID)
			requireRefreshClass(t, err, RefreshClassTransient)
			if !errors.Is(err, ErrRefreshTransient) {
				t.Error("the failure is not the transient sentinel")
			}
			stored, loadErr := store.Load(record.ID)
			if loadErr != nil {
				t.Fatalf("load the registration: %v", loadErr)
			}
			if stored.AccessToken != record.AccessToken {
				t.Error("the access token was erased by a temporary failure")
			}
			if stored.RefreshToken != record.RefreshToken {
				t.Error("the refresh token was erased by a temporary failure")
			}
			if stored.NeedsReauth {
				t.Error("a temporary failure marked the registration as needing a new authorization")
			}
		})
	}
}

// TestRefresher_NetworkFailureKeepsTheCredentials proves that a transport
// failure is transient.
//
// The provider is unreachable: the connection is refused, and the saved
// credentials survive.
func TestRefresher_NetworkFailureKeepsTheCredentials(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	// The fake is closed, so the endpoint cannot be reached.
	fake.server.Close()
	refresher := newTestRefresher(t, store, fake.endpoint("/api/accounts/oauth/token"), fake.client())

	_, err := refresher.Refresh(context.Background(), record.ID)
	requireRefreshClass(t, err, RefreshClassTransient)
	stored, loadErr := store.Load(record.ID)
	if loadErr != nil {
		t.Fatalf("load the registration: %v", loadErr)
	}
	if stored.AccessToken != record.AccessToken || stored.RefreshToken != record.RefreshToken {
		t.Error("a network failure erased the saved credentials")
	}
	if stored.NeedsReauth {
		t.Error("a network failure marked the registration as needing a new authorization")
	}
}

// TestRefresher_InvalidClientIsAConfigurationFault proves that the
// invalid-client code is neither of the two credential classes.
//
// The documentation says: "Invalid client: invalid_client. Fix the client
// configuration." The credentials are not shown to be unusable, so they
// survive; and the fault is not a temporary one, so the outcome is not the
// transient class.
func TestRefresher_InvalidClientIsAConfigurationFault(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	fake.setTokenResponse(http.StatusUnauthorized, `{"error":"invalid_client"}`)
	refresher := newTestRefresher(t, store, fake.endpoint("/api/accounts/oauth/token"), fake.client())

	_, err := refresher.Refresh(context.Background(), record.ID)
	requireRefreshClass(t, err, RefreshClassInvalidClient)
	if !errors.Is(err, ErrRefreshInvalidClient) {
		t.Error("the failure is not the invalid-client sentinel")
	}
	if errors.Is(err, ErrRefreshTerminal) {
		t.Error("a client-configuration fault was classified as a credential fault")
	}
	if errors.Is(err, ErrRefreshTransient) {
		t.Error("a client-configuration fault was classified as a temporary failure")
	}
	stored, loadErr := store.Load(record.ID)
	if loadErr != nil {
		t.Fatalf("load the registration: %v", loadErr)
	}
	if stored.AccessToken != record.AccessToken || stored.RefreshToken != record.RefreshToken {
		t.Error("a client-configuration fault erased the saved credentials")
	}
}

// TestRefresher_UnreadableErrorBodyIsNotAbsent proves that a malformed
// error body is not read as "no error".
//
// The provider refused, and the connector cannot tell why. The outcome is
// its own class: it is not a temporary failure, because the connector does
// not know that, and it is not a terminal one, because the connector does
// not know that either.
func TestRefresher_UnreadableErrorBodyIsNotAbsent(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "the body is not JSON", body: `this is not json`},
		{name: "the body is not an object", body: `["invalid_grant"]`},
		{name: "the error member has another shape", body: `{"error":42}`},
		{name: "the body carries no error member", body: `{"message":"nope"}`},
		{name: "the body repeats the error member", body: `{"error":"invalid_grant","error":"server_error"}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fake := newFakeProvider(t)
			store := newTestProfileStore(t)
			record := expiredTestProfile("acct-one")
			saveTestProfile(t, store, record)
			fake.setTokenResponse(http.StatusBadRequest, testCase.body)
			refresher := newTestRefresher(
				t, store, fake.endpoint("/api/accounts/oauth/token"), fake.client(),
			)

			_, err := refresher.Refresh(context.Background(), record.ID)
			requireRefreshClass(t, err, RefreshClassUnreadable)
			if errors.Is(err, ErrRefreshTransient) {
				t.Error("an unreadable failure was classified as a temporary failure")
			}
			if errors.Is(err, ErrRefreshTerminal) {
				t.Error("an unreadable failure was classified as an unusable credential")
			}
			stored, loadErr := store.Load(record.ID)
			if loadErr != nil {
				t.Fatalf("load the registration: %v", loadErr)
			}
			if stored.AccessToken != record.AccessToken || stored.RefreshToken != record.RefreshToken {
				t.Error("an unreadable failure erased the saved credentials")
			}
		})
	}
}

// TestRefresher_UsesTheIssuedClientIDOfTheNamedRegistration proves that the
// client id comes from the AUTHORITY.
//
// Two registrations carry two issued client ids. The refresh of one of them
// must carry its own id, never the other one: the documentation forbids
// combining one registration's client id with another registration's
// tokens.
func TestRefresher_UsesTheIssuedClientIDOfTheNamedRegistration(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	first := expiredTestProfile("acct-one")
	second := expiredTestProfile("acct-two")
	saveTestProfile(t, store, first)
	saveTestProfile(t, store, second)
	refresher := newTestRefresher(t, store, fake.endpoint("/api/accounts/oauth/token"), fake.client())

	if _, err := refresher.Refresh(context.Background(), second.ID); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	form := fake.lastTokenRequest(t)
	if got := form.Get("client_id"); got != second.IssuedClientID {
		t.Errorf("the client id = %q, want the issued client id of the registration %q", got, second.IssuedClientID)
	}
	if got := form.Get("refresh_token"); got != second.RefreshToken {
		t.Error("the request carries another registration's refresh token")
	}
	// The other registration is untouched.
	other, err := store.Load(first.ID)
	if err != nil {
		t.Fatalf("load the other registration: %v", err)
	}
	if other.RefreshToken != first.RefreshToken || other.AccessToken != first.AccessToken {
		t.Error("the refresh of one registration changed another registration")
	}
}

// TestRefresher_DoesNotAskTheProviderAgainAfterATerminalFailure proves that
// a terminal outcome is not retried.
//
// A rotating token is single-use, and a credential the provider refused must
// not be presented again: the second attempt would be a second use of a
// token that is already unusable.
func TestRefresher_DoesNotAskTheProviderAgainAfterATerminalFailure(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	fake.setTokenResponse(http.StatusBadRequest, `{"error":"invalid_grant"}`)
	refresher := newTestRefresher(t, store, fake.endpoint("/api/accounts/oauth/token"), fake.client())

	if _, err := refresher.Refresh(context.Background(), record.ID); err == nil {
		t.Fatal("the refresh succeeded against a provider that refused the token")
	}
	if _, err := refresher.Refresh(context.Background(), record.ID); err == nil {
		t.Fatal("the second refresh succeeded against a provider that refused the token")
	}
	if requests := fake.tokenRequestCount(); requests != 1 {
		t.Errorf("the provider was asked %d times, want 1", requests)
	}
}

// TestRefresher_IssuesOneRequestPerRefresh proves that a failed refresh is
// not retried with the same token.
//
// A rotating token is single-use: a retry after an ambiguous failure would
// present the token a second time, and the provider would treat the second
// use as a reuse. The caller retries by calling Refresh again, and the call
// re-reads the saved record.
func TestRefresher_IssuesOneRequestPerRefresh(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	fake.setTokenResponse(http.StatusServiceUnavailable, `{"error":"server_error"}`)
	refresher := newTestRefresher(t, store, fake.endpoint("/api/accounts/oauth/token"), fake.client())

	if _, err := refresher.Refresh(context.Background(), record.ID); err == nil {
		t.Fatal("the refresh succeeded against a failing provider")
	}
	if requests := fake.tokenRequestCount(); requests != 1 {
		t.Errorf("the provider was asked %d times, want 1", requests)
	}
}

// TestRefresher_StoresTheGrantedScopesAsASet proves that the scope string
// is parsed as a SET.
//
// A scope that appears twice is one scope, and the set holds it once.
func TestRefresher_StoresTheGrantedScopesAsASet(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	fake.setTokenResponse(http.StatusOK, `{"access_token":"a","refresh_token":"b","expires_in":3600,`+
		`"scope":"openid openid offline_access chatgpt.tokens.use.direct"}`)
	refresher := newTestRefresher(t, store, fake.endpoint("/api/accounts/oauth/token"), fake.client())

	if _, err := refresher.Refresh(context.Background(), record.ID); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	stored, err := store.Load(record.ID)
	if err != nil {
		t.Fatalf("load the registration: %v", err)
	}
	if len(stored.GrantedScopes) != 3 {
		t.Fatalf("the stored scopes are %v, want three distinct scopes", stored.GrantedScopes)
	}
	if !PlanUsageEnabled(stored.GrantedScopes) {
		t.Error("the stored scope set does not authorize the plan usage")
	}
}

// TestRefresher_RedactsTheProviderDescriptionBeforeBounding proves that the
// provider's own text cannot carry a token into an operator-facing error.
//
// The provider echoes the refresh token it received. The error must not
// carry it, and the description must not be truncated before the redaction
// had a chance to remove it.
func TestRefresher_RedactsTheProviderDescriptionBeforeBounding(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	description := strings.Repeat("x", 400) + " " + record.RefreshToken
	fake.setTokenResponse(
		http.StatusBadRequest,
		`{"error":"invalid_request","error_description":"`+description+`"}`,
	)
	refresher := newTestRefresher(t, store, fake.endpoint("/api/accounts/oauth/token"), fake.client())

	_, err := refresher.Refresh(context.Background(), record.ID)
	if err == nil {
		t.Fatal("the refresh succeeded against a provider that refused the request")
	}
	requireNoTokenMaterial(t, err, tokenMaterial{name: "refresh token", value: record.RefreshToken})
}

// TestRefresher_KeepsTheSavedRefreshTokenWhenTheProviderSendsNoReplacement
// proves that an absent member is not a reason to discard a credential.
//
// The connector never throws away the only copy of a renewable credential
// because a member is absent. The documented provider always sends a
// replacement, and a response that sends none keeps the saved token.
func TestRefresher_KeepsTheSavedRefreshTokenWhenTheProviderSendsNoReplacement(t *testing.T) {
	fake := newFakeProvider(t)
	store := newTestProfileStore(t)
	record := expiredTestProfile("acct-one")
	saveTestProfile(t, store, record)
	fake.setTokenResponse(http.StatusOK, `{"access_token":"refreshed-access-token","expires_in":3600}`)
	refresher := newTestRefresher(t, store, fake.endpoint("/api/accounts/oauth/token"), fake.client())

	if _, err := refresher.Refresh(context.Background(), record.ID); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	stored, err := store.Load(record.ID)
	if err != nil {
		t.Fatalf("load the registration: %v", err)
	}
	if stored.RefreshToken != record.RefreshToken {
		t.Error("the saved refresh token was discarded because the response carried no replacement")
	}
}
