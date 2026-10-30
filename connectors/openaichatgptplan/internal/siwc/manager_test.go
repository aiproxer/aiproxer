package siwc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Tests for the SIWC profile manager.
//
// The contract under test is requirement 2.13 of the migration specification
// and the account-identification rules of requirement 8: the saved
// registrations are listed and selected explicitly, the connector never
// rotates to another subscriber on its own, and a report identifies a
// registration through its non-secret local key.

// fakeAuthorizer stands in for the authorization service.
type fakeAuthorizer struct {
	store          *ProfileStore
	registered     []RegisterRequest
	reauthorized   []ReauthorizeRequest
	record         ProfileRecord
	err            error
	reauthorizeErr error
}

// Register records the request, persists the registration and answers it.
func (f *fakeAuthorizer) Register(_ context.Context, req RegisterRequest) (ProfileRecord, error) {
	f.registered = append(f.registered, req)
	if f.err != nil {
		return ProfileRecord{}, f.err
	}
	if f.store != nil {
		if err := f.store.SaveNew(f.record); err != nil {
			return ProfileRecord{}, err
		}
	}
	return f.record, nil
}

// Reauthorize records the request, persists the registration and answers it.
func (f *fakeAuthorizer) Reauthorize(_ context.Context, req ReauthorizeRequest) (ProfileRecord, error) {
	f.reauthorized = append(f.reauthorized, req)
	if f.reauthorizeErr != nil {
		return ProfileRecord{}, f.reauthorizeErr
	}
	if f.store != nil {
		if err := f.store.Save(f.record); err != nil {
			return ProfileRecord{}, err
		}
	}
	return f.record, nil
}

// fakeRefresher stands in for the refresher.
type fakeRefresher struct {
	requests []string
	record   ProfileRecord
	err      error
}

// Refresh records the request and answers the prepared outcome.
func (f *fakeRefresher) Refresh(_ context.Context, profileID string) (ProfileRecord, error) {
	f.requests = append(f.requests, profileID)
	if f.err != nil {
		return ProfileRecord{}, f.err
	}
	return f.record, nil
}

// fakeRevoker stands in for the revoker.
type fakeRevoker struct {
	requests []string
	result   LogoutResult
	err      error
}

// Logout records the request and answers the prepared outcome.
func (f *fakeRevoker) Logout(_ context.Context, profileID string) (LogoutResult, error) {
	f.requests = append(f.requests, profileID)
	if f.err != nil {
		return LogoutResult{}, f.err
	}
	return f.result, nil
}

// newTestManager returns a manager over the given store.
func newTestManager(t *testing.T, store *ProfileStore) *Manager {
	t.Helper()
	manager, err := NewManager(ManagerConfig{Profiles: store})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return manager
}

// TestManager_ListsTheSavedRegistrationsWithoutCredentials proves that the
// listing is the non-secret view.
//
// The listing is what a diagnostic, a listing command or a support
// transcript may carry: no access token, no refresh token, no retained ID
// token and no email hint.
func TestManager_ListsTheSavedRegistrationsWithoutCredentials(t *testing.T) {
	store := newTestProfileStore(t)
	saveTestProfile(t, store, testProfileRecord("acct-one"))
	saveTestProfile(t, store, testProfileRecord("acct-two"))
	manager := newTestManager(t, store)

	summaries, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("the listing holds %d registrations, want 2", len(summaries))
	}
	for _, summary := range summaries {
		rendered := fmt.Sprintf("%+v", summary)
		for _, material := range []string{testAccessToken, testRefreshToken, testIDToken, testEmailHint} {
			if strings.Contains(rendered, material) {
				t.Errorf("the listing carries credential material: %q", rendered)
			}
		}
	}
}

// TestManager_SelectChangesTheSelection proves that the selection is
// explicit.
//
// The operator names the registration they want to use, and the selection
// follows.
func TestManager_SelectChangesTheSelection(t *testing.T) {
	store := newTestProfileStore(t)
	saveTestProfile(t, store, testProfileRecord("acct-one"))
	saveTestProfile(t, store, testProfileRecord("acct-two"))
	manager := newTestManager(t, store)

	selected, err := manager.Selected(context.Background())
	if err != nil {
		t.Fatalf("Selected: %v", err)
	}
	if selected != "" {
		t.Fatalf("a registration is selected before the operator chose one: %q", selected)
	}
	if err := manager.Select(context.Background(), "acct-two"); err != nil {
		t.Fatalf("Select: %v", err)
	}
	selected, err = manager.Selected(context.Background())
	if err != nil {
		t.Fatalf("Selected: %v", err)
	}
	if selected != "acct-two" {
		t.Errorf("the selected registration = %q, want %q", selected, "acct-two")
	}
}

// TestManager_DoesNotRotateToAnotherRegistration proves that a failure does
// not move the operator.
//
// A registration whose credential is unusable, or whose plan allowance is
// exhausted, does not send the operator to another registration: a plan
// allowance is not a failover mechanism, and requirement 2.13 forbids the
// rotation.
func TestManager_DoesNotRotateToAnotherRegistration(t *testing.T) {
	store := newTestProfileStore(t)
	saveTestProfile(t, store, testProfileRecord("acct-one"))
	saveTestProfile(t, store, testProfileRecord("acct-two"))
	manager := newTestManager(t, store)
	if err := manager.Select(context.Background(), "acct-one"); err != nil {
		t.Fatalf("Select: %v", err)
	}
	// The refresh of the selected registration fails terminally.
	refresher := &fakeRefresher{
		err: NewRefreshError(RefreshClassTerminal, 0, "invalid_grant", "", nil),
	}
	manager.refresher = refresher

	if _, err := manager.Token(context.Background(), "acct-one"); err == nil {
		t.Fatal("the token of a registration with an unusable credential was returned")
	}
	selected, err := manager.Selected(context.Background())
	if err != nil {
		t.Fatalf("Selected: %v", err)
	}
	if selected != "acct-one" {
		t.Errorf("the selection moved to %q after a failure", selected)
	}
	// The other registration was not even asked.
	if len(refresher.requests) != 1 || refresher.requests[0] != "acct-one" {
		t.Errorf("the refresher was asked for %v, want only the selected registration", refresher.requests)
	}
	other, err := store.Load("acct-two")
	if err != nil {
		t.Fatalf("load the other registration: %v", err)
	}
	if other.AccessToken != testAccessToken || other.RefreshToken != testRefreshToken {
		t.Error("the other registration was changed")
	}
}

// TestManager_AddMakesTheSignedInRegistrationActive proves that a sign-in
// the operator asked for becomes the active registration.
//
// The documentation asks for the flow to be completed and validated before
// the account is made active, and the operator chose this account.
func TestManager_AddMakesTheSignedInRegistrationActive(t *testing.T) {
	store := newTestProfileStore(t)
	authorizer := &fakeAuthorizer{store: store, record: testProfileRecord("acct-new")}
	manager, err := NewManager(ManagerConfig{Profiles: store, Authorizer: authorizer})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	record, err := manager.Add(context.Background(), RegisterRequest{Label: "work"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if record.ID != "acct-new" {
		t.Errorf("the registered id = %q, want %q", record.ID, "acct-new")
	}
	selected, err := manager.Selected(context.Background())
	if err != nil {
		t.Fatalf("Selected: %v", err)
	}
	if selected != "acct-new" {
		t.Errorf("the selected registration = %q, want the registered one", selected)
	}
}

// TestManager_ReauthorizeMakesTheReauthorizedRegistrationActive proves the
// same for a returning authorization.
func TestManager_ReauthorizeMakesTheReauthorizedRegistrationActive(t *testing.T) {
	store := newTestProfileStore(t)
	authorizer := &fakeAuthorizer{store: store, record: testProfileRecord("acct-two")}
	manager, err := NewManager(ManagerConfig{Profiles: store, Authorizer: authorizer})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	if _, err := manager.Reauthorize(context.Background(), ReauthorizeRequest{
		ProfileID: "acct-two",
	}); err != nil {
		t.Fatalf("Reauthorize: %v", err)
	}
	selected, err := manager.Selected(context.Background())
	if err != nil {
		t.Fatalf("Selected: %v", err)
	}
	if selected != "acct-two" {
		t.Errorf("the selected registration = %q, want the reauthorized one", selected)
	}
}

// TestManager_LogoutReportsTheUnconfirmedRevocation proves that the
// unconfirmed revocation reaches the operator.
//
// The result and the notice carry the same fact: the session was not
// confirmed as ended, and the app can be disconnected in ChatGPT Settings.
func TestManager_LogoutReportsTheUnconfirmedRevocation(t *testing.T) {
	store := newTestProfileStore(t)
	var notices []string
	revoker := &fakeRevoker{result: LogoutResult{
		ProfileID:                 "acct-one",
		RemoteRevocationConfirmed: false,
		LocalTokensCleared:        true,
		RevocationOutcome:         RevocationOutcomeServer,
		Guidance:                  RevocationGuidanceUnconfirmed,
	}}
	manager, err := NewManager(ManagerConfig{
		Profiles: store,
		Revoker:  revoker,
		Notice:   func(message string) { notices = append(notices, message) },
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	result, err := manager.Logout(context.Background(), "acct-one")
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if result.RemoteRevocationConfirmed {
		t.Error("the revocation was confirmed")
	}
	if len(notices) != 1 || notices[0] != RevocationGuidanceUnconfirmed {
		t.Errorf("the notices = %v, want the unconfirmed-revocation guidance", notices)
	}
	// The sign-out does not move the operator to another registration.
	selected, err := manager.Selected(context.Background())
	if err != nil {
		t.Fatalf("Selected: %v", err)
	}
	if selected != "" {
		t.Errorf("the selection moved to %q after a sign-out", selected)
	}
}

// TestManager_TokenReturnsTheAccessToken proves the token surface.
func TestManager_TokenReturnsTheAccessToken(t *testing.T) {
	store := newTestProfileStore(t)
	refresher := &fakeRefresher{record: ProfileRecord{
		ID:          "acct-one",
		Subject:     "subject-acct-one",
		AccessToken: "a-fresh-access-token",
		Expiry:      time.Now().Add(time.Hour),
	}}
	manager, err := NewManager(ManagerConfig{Profiles: store, Refresher: refresher})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	grant, err := manager.Token(context.Background(), "acct-one")
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if grant.AccessToken != "a-fresh-access-token" {
		t.Errorf("the access token = %q, want the refreshed one", grant.AccessToken)
	}
	generation, ok := ProfileRecord{AccessToken: "a-fresh-access-token"}.CredentialGeneration()
	if !ok || grant.Generation != generation {
		t.Errorf("the grant generation does not name the credential the token came from")
	}
	if len(refresher.requests) != 1 || refresher.requests[0] != "acct-one" {
		t.Errorf("the refresher was asked for %v, want the named registration", refresher.requests)
	}
}

// TestManager_TokenReportsAnUnusableCredential proves that an unusable
// credential is reported, and not worked around.
func TestManager_TokenReportsAnUnusableCredential(t *testing.T) {
	store := newTestProfileStore(t)
	refresher := &fakeRefresher{
		err: NewRefreshError(RefreshClassTerminal, 0, "invalid_grant", "", nil),
	}
	manager, err := NewManager(ManagerConfig{Profiles: store, Refresher: refresher})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	if _, err := manager.Token(context.Background(), "acct-one"); err == nil {
		t.Fatal("a token was returned for an unusable credential")
	} else if !errors.Is(err, ErrRefreshTerminal) {
		t.Errorf("the failure is not the terminal class: %v", err)
	}
}

// TestDescribeRegistration_IdentifiesTheRegistrationByItsLocalKey proves
// the account-identification rule.
//
// A report names the local profile id and the label. The subject, the email
// hint and the tokens never appear in it.
func TestDescribeRegistration_IdentifiesTheRegistrationByItsLocalKey(t *testing.T) {
	record := testProfileRecord("acct-one")
	rendered := DescribeRegistration(record)
	if !strings.Contains(rendered, "acct-one") {
		t.Errorf("the rendering does not name the local key: %q", rendered)
	}
	if !strings.Contains(rendered, record.Label) {
		t.Errorf("the rendering does not name the label: %q", rendered)
	}
	for _, material := range []string{
		record.Subject,
		record.EmailHint,
		record.AccessToken,
		record.RefreshToken,
		record.IDToken,
	} {
		if strings.Contains(rendered, material) {
			t.Errorf("the rendering carries account material: %q", rendered)
		}
	}
	// A registration without a label is named by its key alone.
	record.Label = ""
	rendered = DescribeRegistration(record)
	if !strings.Contains(rendered, "acct-one") {
		t.Errorf("the rendering does not name the local key: %q", rendered)
	}
}
