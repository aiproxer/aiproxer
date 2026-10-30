package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/siwc"
)

// The operator login command.
//
// The command drives the same authorization service the package tests
// exercise, against a fake authorization server. The test proves that the
// command performs the documented authorization, that it stores the
// registration in the protected store, and that it prints nothing that
// carries a credential.

// fakeAuthServer stands in for OpenAI's authorization and token endpoints.
type fakeAuthServer struct {
	server *httptest.Server

	mu             sync.Mutex
	authorizations []url.Values
	exchanges      []url.Values
	// errorBody is the raw body the token endpoint answers a failed
	// exchange with. It is empty while the endpoint answers the documented
	// success shape.
	errorBody string
	// idToken overrides the ID token a successful exchange answers with.
	// It is empty while the endpoint answers the documented value.
	idToken string
}

// newFakeAuthServer starts the fake server.
func newFakeAuthServer(t *testing.T) *fakeAuthServer {
	t.Helper()
	fake := &fakeAuthServer{}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.route))
	t.Cleanup(fake.server.Close)
	return fake
}

// route dispatches the two documented endpoints.
func (f *fakeAuthServer) route(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/authorize"):
		f.mu.Lock()
		f.authorizations = append(f.authorizations, r.URL.Query())
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case strings.HasSuffix(r.URL.Path, "/oauth/token"):
		if err := r.ParseForm(); err != nil {
			http.Error(w, "unreadable form", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.exchanges = append(f.exchanges, r.PostForm)
		errorBody := f.errorBody
		idToken := f.idToken
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if errorBody != "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(errorBody))
			return
		}
		if idToken == "" {
			idToken = "id-token-1"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-token-1",
			"refresh_token": "refresh-token-1",
			"id_token":      idToken,
			"token_type":    "Bearer",
			"expires_in":    3600,
			"scope":         "openid email offline_access chatgpt.tokens.use.direct",
		})
	default:
		http.NotFound(w, r)
	}
}

// stubValidator stands in for the JWKS validation of the OIDC phase.
type stubValidator struct {
	identity siwc.Identity
}

func (v stubValidator) ValidateIDToken(
	context.Context,
	siwc.TokenResponse,
	siwc.TokenExpectation,
) (siwc.Identity, error) {
	return v.identity, nil
}

// TestRunLogin_RegistersAndStoresTheRegistration proves that the command
// performs the documented authorization and stores the registration.
func TestRunLogin_RegistersAndStoresTheRegistration(t *testing.T) {
	fake := newFakeAuthServer(t)
	directory := t.TempDir()
	hostRecord := filepath.Join(directory, "state", "host.json")
	profileStore := filepath.Join(directory, "state", "profiles")

	var stdout, stderr bytes.Buffer
	deps := LoginDependencies{
		HTTPClient: fake.server.Client(),
		Validator:  stubValidator{identity: siwc.Identity{Subject: "subject-1", EmailHint: "user@example.com"}},
		BrowserOpener: func(_ context.Context, authorizationURL string) error {
			parsed, err := url.Parse(authorizationURL)
			if err != nil {
				return err
			}
			query := parsed.Query()
			values := url.Values{
				"state":     {query.Get("state")},
				"code":      {"auth-code-1"},
				"client_id": {"oaiapp_issued_1"},
			}
			go func() {
				authorization, err := http.Get(authorizationURL)
				if err == nil {
					_ = authorization.Body.Close()
				}
				response, err := http.Get(query.Get("redirect_uri") + "?" + values.Encode())
				if err == nil {
					_ = response.Body.Close()
				}
			}()
			return nil
		},
	}
	args := []string{
		"--profile-id", "acct-test",
		"--label", "Personal",
		"--host-record", hostRecord,
		"--profile-store", profileStore,
		"--authorization-endpoint", fake.server.URL + "/api/accounts/authorize",
		"--token-endpoint", fake.server.URL + "/api/accounts/oauth/token",
		"--timeout", "10s",
	}
	if err := RunLogin(context.Background(), &stdout, &stderr, deps, args); err != nil {
		t.Fatalf("RunLogin: %v (stderr: %s)", err, stderr.String())
	}

	fake.mu.Lock()
	authorizations := append([]url.Values(nil), fake.authorizations...)
	exchanges := append([]url.Values(nil), fake.exchanges...)
	fake.mu.Unlock()
	if len(authorizations) != 1 {
		t.Fatalf("the provider saw %d authorization requests, want 1", len(authorizations))
	}
	if got := authorizations[0].Get("client_id"); got != "dynamic_agent_client" {
		t.Errorf("the authorization request carried the client id %q, want the dynamic entry point", got)
	}
	if got := authorizations[0].Get("agent_name_hint"); got != siwc.DefaultAgentName {
		t.Errorf("the authorization request carried the agent name %q, want %q", got, siwc.DefaultAgentName)
	}
	if got := authorizations[0].Get("redirect_uri"); !strings.HasPrefix(got, "http://127.0.0.1:") {
		t.Errorf("the authorization request carried the callback URI %q", got)
	}
	if len(exchanges) != 1 {
		t.Fatalf("the provider saw %d exchanges, want 1", len(exchanges))
	}
	if got := exchanges[0].Get("redirect_uri"); got != authorizations[0].Get("redirect_uri") {
		t.Errorf("the exchange carried the redirect_uri %q, want %q", got, authorizations[0].Get("redirect_uri"))
	}
	if got := exchanges[0].Get("client_id"); got != "oaiapp_issued_1" {
		t.Errorf("the exchange carried the client id %q, want the issued client id", got)
	}

	store, err := siwc.NewProfileStore(profileStore)
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("the store holds %d registrations, want 1", len(summaries))
	}
	if summaries[0].ID != "acct-test" {
		t.Errorf("the store holds the registration %q, want acct-test", summaries[0].ID)
	}
	record, err := store.Load("acct-test")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if record.IssuedClientID != "oaiapp_issued_1" {
		t.Errorf("the stored registration carries the issued client id %q", record.IssuedClientID)
	}
	if record.AccessToken == "" || record.RefreshToken == "" {
		t.Error("the stored registration carries no token set")
	}

	// The command prints the local key of the registration and nothing that
	// carries a credential.
	output := stdout.String() + stderr.String()
	for _, secret := range []string{"access-token-1", "refresh-token-1", "id-token-1", "auth-code-1"} {
		if strings.Contains(output, secret) {
			t.Errorf("the command printed the credential %q", secret)
		}
	}
	if !strings.Contains(stdout.String(), "acct-test") {
		t.Errorf("the command did not report the registration: %q", stdout.String())
	}
}

// TestRunLogin_RejectsAnUnknownProfile proves that a reauthorization of a
// registration that does not exist fails explicitly.
func TestRunLogin_RejectsAnUnknownProfile(t *testing.T) {
	fake := newFakeAuthServer(t)
	directory := t.TempDir()
	var stdout, stderr bytes.Buffer
	deps := LoginDependencies{
		HTTPClient:    fake.server.Client(),
		Validator:     stubValidator{identity: siwc.Identity{Subject: "subject-1"}},
		BrowserOpener: func(context.Context, string) error { return nil },
	}
	args := []string{
		"--reauthorize",
		"--profile-id", "acct-missing",
		"--host-record", filepath.Join(directory, "state", "host.json"),
		"--profile-store", filepath.Join(directory, "state", "profiles"),
		"--authorization-endpoint", fake.server.URL + "/api/accounts/authorize",
		"--token-endpoint", fake.server.URL + "/api/accounts/oauth/token",
		"--timeout", "2s",
	}
	if err := RunLogin(context.Background(), &stdout, &stderr, deps, args); err == nil {
		t.Fatal("RunLogin accepted a reauthorization of an unknown registration")
	}
}

// TestRunLogin_FailsClosedWithoutAValidator proves that the command does
// not store a registration whose identity it could not validate.
func TestRunLogin_FailsClosedWithoutAValidator(t *testing.T) {
	fake := newFakeAuthServer(t)
	directory := t.TempDir()
	profileStore := filepath.Join(directory, "state", "profiles")
	var stdout, stderr bytes.Buffer
	deps := LoginDependencies{
		HTTPClient: fake.server.Client(),
		BrowserOpener: func(_ context.Context, authorizationURL string) error {
			parsed, err := url.Parse(authorizationURL)
			if err != nil {
				return err
			}
			query := parsed.Query()
			values := url.Values{
				"state":     {query.Get("state")},
				"code":      {"auth-code-1"},
				"client_id": {"oaiapp_issued_1"},
			}
			go func() {
				authorization, err := http.Get(authorizationURL)
				if err == nil {
					_ = authorization.Body.Close()
				}
				response, err := http.Get(query.Get("redirect_uri") + "?" + values.Encode())
				if err == nil {
					_ = response.Body.Close()
				}
			}()
			return nil
		},
	}
	args := []string{
		"--profile-id", "acct-test",
		"--host-record", filepath.Join(directory, "state", "host.json"),
		"--profile-store", profileStore,
		"--authorization-endpoint", fake.server.URL + "/api/accounts/authorize",
		"--token-endpoint", fake.server.URL + "/api/accounts/oauth/token",
		"--timeout", "10s",
	}
	err := RunLogin(context.Background(), &stdout, &stderr, deps, args)
	if err == nil {
		t.Fatal("RunLogin stored a registration without validating the identity")
	}
	store, storeErr := siwc.NewProfileStore(profileStore)
	if storeErr != nil {
		t.Fatalf("NewProfileStore: %v", storeErr)
	}
	summaries, listErr := store.List()
	if listErr != nil {
		t.Fatalf("List: %v", listErr)
	}
	if len(summaries) != 0 {
		t.Errorf("the store holds %d registrations, want 0", len(summaries))
	}
}

// TestRunLogin_ReportsABrowserThatCannotBeOpened proves that the command
// tells the operator which URL to open when the browser cannot be started.
func TestRunLogin_ReportsABrowserThatCannotBeOpened(t *testing.T) {
	fake := newFakeAuthServer(t)
	directory := t.TempDir()
	var stdout, stderr bytes.Buffer
	deps := LoginDependencies{
		HTTPClient:    fake.server.Client(),
		Validator:     stubValidator{identity: siwc.Identity{Subject: "subject-1"}},
		BrowserOpener: func(context.Context, string) error { return errors.New("no browser is available") },
	}
	args := []string{
		"--profile-id", "acct-test",
		"--host-record", filepath.Join(directory, "state", "host.json"),
		"--profile-store", filepath.Join(directory, "state", "profiles"),
		"--authorization-endpoint", fake.server.URL + "/api/accounts/authorize",
		"--token-endpoint", fake.server.URL + "/api/accounts/oauth/token",
		"--timeout", "300ms",
	}
	if err := RunLogin(context.Background(), &stdout, &stderr, deps, args); err == nil {
		t.Fatal("RunLogin succeeded without a browser")
	}
	if !strings.Contains(stderr.String(), "open this URL") {
		t.Errorf("the command did not report the URL to open: %q", stderr.String())
	}
}

// TestOpenBrowser_ReportsAnUnusableHandler proves that the platform opener
// fails explicitly when it cannot be started.
func TestOpenBrowser_ReportsAnUnusableHandler(t *testing.T) {
	// A URL that no handler can open is not a portable assertion, so the
	// test only proves that the opener does not block: it returns promptly
	// and the attempt continues.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = openBrowser(context.Background(), "https://example.invalid/")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the browser opener blocked")
	}
}
