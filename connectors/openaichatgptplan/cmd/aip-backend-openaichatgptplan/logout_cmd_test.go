package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/siwc"
)

// The operator sign-out and registration listing commands.
//
// The commands drive the same package the tests of the package exercise,
// against a fake provider. The proofs are about what the commands PRINT:
// the outcome of a revocation, and the identity of a registration.

// fakeProvider stands in for the provider's OIDC configuration and its
// revocation endpoint.
type fakeProvider struct {
	server *httptest.Server

	mu         sync.Mutex
	revocation []url.Values
	// status is the status the revocation endpoint answers with. Zero
	// means 200 with an empty body, which is the documented success
	// response.
	status int
	// body is the raw body the revocation endpoint answers with.
	body string
}

// newFakeProvider starts the fake provider.
func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	fake := &fakeProvider{}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.route))
	t.Cleanup(fake.server.Close)
	return fake
}

// route dispatches the two provider surfaces.
func (f *fakeProvider) route(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		body := `{"issuer":"` + f.server.URL + `",` +
			`"jwks_uri":"` + f.server.URL + `/.well-known/jwks.json",` +
			`"token_endpoint":"` + f.server.URL + `/api/accounts/oauth/token",` +
			`"revocation_endpoint":"` + f.server.URL + `/api/accounts/oauth/revoke"}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	case "/api/accounts/oauth/revoke":
		if err := r.ParseForm(); err != nil {
			http.Error(w, "unreadable form", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.revocation = append(f.revocation, r.PostForm)
		status := f.status
		body := f.body
		f.mu.Unlock()
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		if body != "" {
			_, _ = w.Write([]byte(body))
		}
	default:
		http.NotFound(w, r)
	}
}

// setResponse replaces the answer of the revocation endpoint.
func (f *fakeProvider) setResponse(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
	f.body = body
}

// revocationCount returns how often the revocation endpoint was called.
func (f *fakeProvider) revocationCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.revocation)
}

// oidcClient returns the OIDC client of the fake provider.
func (f *fakeProvider) oidcClient(t *testing.T) *siwc.OIDCClient {
	t.Helper()
	client, err := siwc.NewOIDCClient(siwc.OIDCClientConfig{
		DiscoveryURL: f.server.URL + "/.well-known/openid-configuration",
		HTTPClient:   f.server.Client(),
	})
	if err != nil {
		t.Fatalf("NewOIDCClient: %v", err)
	}
	return client
}

// testRegistration is the registration the command proofs work with.
//
// The values are the material that must not reach the command's output.
const (
	testAccessToken  = "command-access-token-material"
	testRefreshToken = "command-refresh-token-material"
	testIDToken      = "command-id-token-material"
	testSubject      = "command-subject-material"
	testEmailHint    = "person@example.invalid"
)

// saveTestRegistration publishes a registration in a fresh store.
func saveTestRegistration(t *testing.T, dir string) *siwc.ProfileStore {
	t.Helper()
	store, err := siwc.NewProfileStore(dir + "/profiles")
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	record := siwc.ProfileRecord{
		ID:             "acct-one",
		Label:          "work",
		Subject:        testSubject,
		EmailHint:      testEmailHint,
		IssuedClientID: "issued-client-one",
		HostID:         "urn:uuid:11111111-2222-4333-8444-555555555555",
		GrantedScopes:  []string{"openid", "chatgpt.tokens.use.direct"},
		AccessToken:    testAccessToken,
		RefreshToken:   testRefreshToken,
		IDToken:        testIDToken,
	}
	if err := store.SaveNew(record); err != nil {
		t.Fatalf("save the registration: %v", err)
	}
	if err := store.Select(record.ID); err != nil {
		t.Fatalf("select the registration: %v", err)
	}
	return store
}

// TestRunLogout_ReportsTheConfirmedRevocation proves that the command
// reports the confirmed outcome.
func TestRunLogout_ReportsTheConfirmedRevocation(t *testing.T) {
	fake := newFakeProvider(t)
	dir := t.TempDir()
	store := saveTestRegistration(t, dir)

	var stdout, stderr bytes.Buffer
	err := RunLogout(
		context.Background(),
		&stdout,
		&stderr,
		LogoutDependencies{HTTPClient: fake.server.Client(), OIDCClient: fake.oidcClient(t)},
		[]string{"--profile-store", dir + "/profiles"},
	)
	if err != nil {
		t.Fatalf("RunLogout: %v", err)
	}
	if fake.revocationCount() != 1 {
		t.Fatalf("the revocation endpoint was called %d times, want 1", fake.revocationCount())
	}
	if !strings.Contains(stdout.String(), "revoked") {
		t.Errorf("the output does not report the revocation: %q", stdout.String())
	}
	// The local tokens are cleared.
	stored, err := store.Load("acct-one")
	if err != nil {
		t.Fatalf("load the registration: %v", err)
	}
	if stored.AccessToken != "" || stored.RefreshToken != "" {
		t.Error("the local tokens were not cleared")
	}
	// The output carries no credential material.
	for _, material := range []string{testAccessToken, testRefreshToken, testIDToken, testSubject, testEmailHint} {
		if strings.Contains(stdout.String(), material) || strings.Contains(stderr.String(), material) {
			t.Errorf("the output carries account material: %q", stdout.String())
		}
	}
}

// TestRunLogout_ReportsTheUnconfirmedRevocation proves that the command
// reports the unconfirmed outcome and the guidance.
//
// The two outcomes are different facts, and the operator has to learn which
// one happened.
func TestRunLogout_ReportsTheUnconfirmedRevocation(t *testing.T) {
	fake := newFakeProvider(t)
	dir := t.TempDir()
	saveTestRegistration(t, dir)
	fake.setResponse(http.StatusServiceUnavailable, "")

	var stdout, stderr bytes.Buffer
	err := RunLogout(
		context.Background(),
		&stdout,
		&stderr,
		LogoutDependencies{HTTPClient: fake.server.Client(), OIDCClient: fake.oidcClient(t)},
		[]string{"--profile-store", dir + "/profiles"},
	)
	if err != nil {
		t.Fatalf("RunLogout: %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, "NOT confirmed") {
		t.Errorf("the output does not report the unconfirmed revocation: %q", output)
	}
	if !strings.Contains(output, "ChatGPT Settings") {
		t.Errorf("the output does not name the step the operator has to take: %q", output)
	}
}

func TestRunLogout_DoesNotClaimASupersededSessionWasSignedOut(t *testing.T) {
	fake := newFakeProvider(t)
	dir := t.TempDir()
	store := saveTestRegistration(t, dir)
	fake.setResponse(http.StatusServiceUnavailable, "")
	deps := LogoutDependencies{
		HTTPClient: fake.server.Client(),
		OIDCClient: fake.oidcClient(t),
		Sleep: func(context.Context, time.Duration) error {
			_, err := store.Publish("acct-one", func(current siwc.ProfileRecord) (siwc.ProfileRecord, error) {
				current.RefreshToken = "replacement-refresh-token"
				current.AccessToken = "replacement-access-token"
				return current, nil
			})
			fake.setResponse(http.StatusOK, "")
			return err
		},
	}
	var stdout, stderr bytes.Buffer
	if err := RunLogout(context.Background(), &stdout, &stderr, deps, []string{"--profile-store", dir + "/profiles"}); err != nil {
		t.Fatalf("RunLogout: %v", err)
	}
	if output := stdout.String(); strings.Contains(output, "Signed out") || !strings.Contains(output, "newer session was retained") {
		t.Errorf("command misreported the surviving session: %q", output)
	}
	stored, err := store.Load("acct-one")
	if err != nil {
		t.Fatal(err)
	}
	if stored.RefreshToken != "replacement-refresh-token" || stored.NeedsReauth {
		t.Error("logout did not preserve the replacement session")
	}
}

// TestRunLogout_WithoutASelectedRegistration proves that the command
// refuses an ambiguous sign-out.
func TestRunLogout_WithoutASelectedRegistration(t *testing.T) {
	fake := newFakeProvider(t)
	dir := t.TempDir()
	store, err := siwc.NewProfileStore(dir + "/profiles")
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	record := siwc.ProfileRecord{
		ID:             "acct-one",
		Subject:        testSubject,
		IssuedClientID: "issued-client-one",
		HostID:         "urn:uuid:11111111-2222-4333-8444-555555555555",
		AccessToken:    testAccessToken,
		RefreshToken:   testRefreshToken,
	}
	if err := store.SaveNew(record); err != nil {
		t.Fatalf("save the registration: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err = RunLogout(
		context.Background(),
		&stdout,
		&stderr,
		LogoutDependencies{HTTPClient: fake.server.Client(), OIDCClient: fake.oidcClient(t)},
		[]string{"--profile-store", dir + "/profiles"},
	)
	if err == nil {
		t.Fatal("the command signed out a registration without a selection")
	}
	if fake.revocationCount() != 0 {
		t.Error("the revocation endpoint was called for an ambiguous sign-out")
	}
}

// TestRunProfiles_ListsTheRegistrationsByTheirLocalKey proves the listing.
//
// The listing names every registration through its local key and its label,
// and it carries no token, no subject and no email hint.
func TestRunProfiles_ListsTheRegistrationsByTheirLocalKey(t *testing.T) {
	dir := t.TempDir()
	saveTestRegistration(t, dir)

	var stdout, stderr bytes.Buffer
	if err := RunProfiles(
		context.Background(), &stdout, &stderr, []string{"--profile-store", dir + "/profiles"},
	); err != nil {
		t.Fatalf("RunProfiles: %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, "acct-one") {
		t.Errorf("the listing does not name the registration: %q", output)
	}
	if !strings.Contains(output, "work") {
		t.Errorf("the listing does not name the label: %q", output)
	}
	for _, material := range []string{testAccessToken, testRefreshToken, testIDToken, testSubject, testEmailHint} {
		if strings.Contains(output, material) {
			t.Errorf("the listing carries account material: %q", output)
		}
	}
}

// TestRunProfiles_SelectsTheChosenRegistration proves the explicit
// selection.
func TestRunProfiles_SelectsTheChosenRegistration(t *testing.T) {
	dir := t.TempDir()
	store := saveTestRegistration(t, dir)
	second := siwc.ProfileRecord{
		ID:             "acct-two",
		Label:          "personal",
		Subject:        testSubject,
		IssuedClientID: "issued-client-two",
		HostID:         "urn:uuid:11111111-2222-4333-8444-555555555555",
		AccessToken:    testAccessToken,
		RefreshToken:   testRefreshToken,
	}
	if err := store.SaveNew(second); err != nil {
		t.Fatalf("save the registration: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := RunProfiles(
		context.Background(),
		&stdout,
		&stderr,
		[]string{"--profile-store", dir + "/profiles", "--select", "acct-two"},
	); err != nil {
		t.Fatalf("RunProfiles: %v", err)
	}
	selected, err := store.Selected()
	if err != nil {
		t.Fatalf("Selected: %v", err)
	}
	if selected != "acct-two" {
		t.Errorf("the selected registration = %q, want %q", selected, "acct-two")
	}
}
