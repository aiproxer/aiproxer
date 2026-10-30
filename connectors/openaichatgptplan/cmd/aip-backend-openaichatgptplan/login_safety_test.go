package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/siwc"
)

// The operator login command.
//
// The tests drive the command against a fake authorization server. They
// cover the properties the review demanded: a provider must not be able to
// turn its own answer into a credential disclosure in the command's
// OPERATIONAL output, the command must reach the exchange and be bounded
// there, and the browser launcher must be detached and bounded.
//
// Every regression here reaches the real path: a valid callback is
// delivered, the token endpoint is proven to have received the exchange, and
// the expected error CLASS is asserted. A test that never reaches the
// provider proves nothing about the provider's answer.

// fakeTokenServer stands in for the token endpoint.
//
// It records the exchanges it received and can answer with a body the
// implementer chooses.
type fakeTokenServer struct {
	server *httptest.Server

	mu        sync.Mutex
	exchanges []url.Values
	// answer is the raw body a failed exchange answers with.
	answer string
	// status is the HTTP status of a failed exchange.
	status int
	// redirect is the Location a failed exchange answers with. An empty
	// value answers with the raw body.
	redirect string
}

// newFakeTokenServer starts the fake token endpoint.
func newFakeTokenServer(t *testing.T) *fakeTokenServer {
	t.Helper()
	fake := &fakeTokenServer{status: http.StatusBadRequest}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.route))
	t.Cleanup(fake.server.Close)
	return fake
}

// route answers the token endpoint.
func (f *fakeTokenServer) route(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "unreadable form", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.exchanges = append(f.exchanges, r.PostForm)
	answer := f.answer
	status := f.status
	redirect := f.redirect
	f.mu.Unlock()
	if redirect != "" {
		w.Header().Set("Location", redirect)
		w.WriteHeader(http.StatusFound)
		return
	}
	if answer != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token":  "access-token-1",
		"refresh_token": "refresh-token-1",
		"id_token":      "id-token-1",
		"token_type":    "Bearer",
		"expires_in":    3600,
		"scope":         "openid email offline_access chatgpt.tokens.use.direct",
	})
}

// exchangeCount returns how many exchanges the endpoint received.
func (f *fakeTokenServer) exchangeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.exchanges)
}

// answerWith makes the endpoint answer a failed exchange with the given
// body.
func (f *fakeTokenServer) answerWith(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
	f.answer = body
}

// answerWithRedirect makes the endpoint answer a redirect.
func (f *fakeTokenServer) answerWithRedirect(location string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.redirect = location
}

// callbackOpener returns a browser opener that performs the callback the
// provider would deliver.
//
// The opener parses the authorization URL the command built, extracts the
// state and the callback URI, and delivers the query the provider would
// send. A returning authorization may deliver an OAuth error instead of a
// code.
func callbackOpener(t *testing.T, errorCode, errorDescription string) func(context.Context, string) error {
	t.Helper()
	return func(_ context.Context, authorizationURL string) error {
		parsed, err := url.Parse(authorizationURL)
		if err != nil {
			return err
		}
		query := parsed.Query()
		values := url.Values{"state": {query.Get("state")}}
		if errorCode != "" {
			values.Set("error", errorCode)
			if errorDescription != "" {
				values.Set("error_description", errorDescription)
			}
		} else {
			values.Set("code", "synthetic-authorization-code")
			values.Set("client_id", "oaiapp_issued_1")
		}
		response, err := http.Get(query.Get("redirect_uri") + "?" + values.Encode())
		if err != nil {
			return err
		}
		defer func() { _ = response.Body.Close() }()
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
}

// TestRunLogin_ProviderErrorCannotDiscloseTheAuthorizationCode proves that
// the command's OPERATIONAL output cannot carry a credential the provider
// echoed back.
//
// The test reaches the exchange: a valid callback is delivered, the token
// endpoint is proven to have received it, and the expected error CLASS is
// asserted. The scanned text is the output main renders, not an unrelated
// timeout.
func TestRunLogin_ProviderErrorCannotDiscloseTheAuthorizationCode(t *testing.T) {
	const authorizationCode = "synthetic-authorization-credential"
	token := newFakeTokenServer(t)
	token.answerWith(http.StatusBadRequest, `{"error":"`+authorizationCode+`","error_description":"rejected"}`)

	directory := t.TempDir()
	var stdout, stderr bytes.Buffer
	deps := LoginDependencies{
		HTTPClient:    token.server.Client(),
		Validator:     stubValidator{identity: siwc.Identity{Subject: "subject-1"}},
		BrowserOpener: callbackOpener(t, "", ""),
	}
	args := []string{
		"--profile-id", "acct-test",
		"--host-record", filepath.Join(directory, "state", "host.json"),
		"--profile-store", filepath.Join(directory, "state", "profiles"),
		"--authorization-endpoint", token.server.URL + "/api/accounts/authorize",
		"--token-endpoint", token.server.URL + "/api/accounts/oauth/token",
		"--timeout", "10s",
	}
	err := RunLogin(context.Background(), &stdout, &stderr, deps, args)
	if err == nil {
		t.Fatal("the command reported a successful sign-in against a provider that rejected the code")
	}
	if got := token.exchangeCount(); got != 1 {
		t.Fatalf("the token endpoint received %d exchanges, want 1: the command never reached the exchange", got)
	}
	// The error class is the classified provider error, not an unrelated
	// failure of the attempt.
	if !strings.Contains(err.Error(), "provider_error") {
		t.Errorf("the command failed with %q, want the classified provider error", err.Error())
	}
	// The operational sink is main's exact rendering.
	fmt.Fprintf(&stderr, "openai-chatgpt-plan login error: %v\n", err)
	rendered := stdout.String() + stderr.String()
	if strings.Contains(rendered, authorizationCode) {
		t.Errorf("the command output discloses the authorization code: %q", rendered)
	}
}

// TestRunLogin_TransportErrorCannotDiscloseTheAuthorizationCode proves that
// a transport failure cannot carry the authorization code into the command's
// operational output.
//
// The token endpoint answers a redirect whose Location carries the code it
// just received. The exchange must neither follow the redirect nor render
// the Location.
func TestRunLogin_TransportErrorCannotDiscloseTheAuthorizationCode(t *testing.T) {
	const authorizationCode = "synthetic-transport-authorization-code"
	token := newFakeTokenServer(t)
	token.answerWithRedirect("unsupported-scheme://destination/?credential=" + url.QueryEscape(authorizationCode))

	directory := t.TempDir()
	var stdout, stderr bytes.Buffer
	deps := LoginDependencies{
		// The ordinary client: the command must not depend on a special
		// transport to be safe.
		HTTPClient:    &http.Client{},
		Validator:     stubValidator{identity: siwc.Identity{Subject: "subject-1"}},
		BrowserOpener: callbackOpener(t, "", ""),
	}
	args := []string{
		"--profile-id", "acct-test",
		"--host-record", filepath.Join(directory, "state", "host.json"),
		"--profile-store", filepath.Join(directory, "state", "profiles"),
		"--authorization-endpoint", token.server.URL + "/api/accounts/authorize",
		"--token-endpoint", token.server.URL + "/api/accounts/oauth/token",
		"--timeout", "10s",
	}
	err := RunLogin(context.Background(), &stdout, &stderr, deps, args)
	if err == nil {
		t.Fatal("the command reported a successful sign-in against a provider that refused the exchange")
	}
	if got := token.exchangeCount(); got != 1 {
		t.Fatalf("the token endpoint received %d exchanges, want 1: the command never reached the exchange", got)
	}
	fmt.Fprintf(&stderr, "openai-chatgpt-plan login error: %v\n", err)
	rendered := stdout.String() + stderr.String()
	if strings.Contains(rendered, authorizationCode) {
		t.Errorf("the command output discloses the authorization code: %q", rendered)
	}
	if strings.Contains(rendered, "unsupported-scheme") {
		t.Errorf("the command output discloses the redirect destination: %q", rendered)
	}
}

// TestRunLogin_ExchangeIsBoundedAgainstAStalledProvider proves that the
// command's exchange ends with the attempt, whatever client the caller
// supplied.
//
// The provider accepts the connection and never answers. The command has to
// end on its own, with the bounded exchange failure, rather than hang.
func TestRunLogin_ExchangeIsBoundedAgainstAStalledProvider(t *testing.T) {
	release := make(chan struct{})
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	// The release runs first: the test server waits for its outstanding
	// handler, and the handler waits for the release.
	defer stalled.Close()
	defer close(release)

	directory := t.TempDir()
	var stdout, stderr bytes.Buffer
	deps := LoginDependencies{
		// A client without a timeout: the attempt's deadline has to be
		// the bound.
		HTTPClient:    &http.Client{},
		Validator:     stubValidator{identity: siwc.Identity{Subject: "subject-1"}},
		BrowserOpener: callbackOpener(t, "", ""),
	}
	args := []string{
		"--profile-id", "acct-test",
		"--host-record", filepath.Join(directory, "state", "host.json"),
		"--profile-store", filepath.Join(directory, "state", "profiles"),
		"--authorization-endpoint", stalled.URL + "/api/accounts/authorize",
		"--token-endpoint", stalled.URL + "/api/accounts/oauth/token",
		"--timeout", "500ms",
	}
	done := make(chan error, 1)
	go func() {
		done <- RunLogin(context.Background(), &stdout, &stderr, deps, args)
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the command reported a successful sign-in against a stalled provider")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("the command failed with %q, want the bounded exchange failure", err.Error())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the command outlived the attempt's deadline")
	}
}

// mixedCallbackOpener returns a browser opener that delivers a callback
// that carries BOTH an OAuth error and an authorization code.
//
// A provider may answer a refusal with the code it received, and the
// connector's own surfaces must not carry it.
func mixedCallbackOpener(t *testing.T, errorCode, errorDescription, code string) func(context.Context, string) error {
	t.Helper()
	return func(_ context.Context, authorizationURL string) error {
		parsed, err := url.Parse(authorizationURL)
		if err != nil {
			return err
		}
		query := parsed.Query()
		values := url.Values{"state": {query.Get("state")}}
		if errorCode != "" {
			values.Set("error", errorCode)
		}
		if errorDescription != "" {
			values.Set("error_description", errorDescription)
		}
		if code != "" {
			values.Set("code", code)
		}
		response, err := http.Get(query.Get("redirect_uri") + "?" + values.Encode())
		if err != nil {
			return err
		}
		defer func() { _ = response.Body.Close() }()
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
}

// TestRunLogin_MixedCallbackCannotDiscloseTheCodeItCarries proves that a
// callback that carries both an OAuth error and an authorization code
// cannot put the code into the command's OPERATIONAL output.
//
// The error class stays VISIBLE: "access_denied" is a decision of the user,
// and the recovery differs per code. The code itself never reaches the
// operator, and no code is exchanged.
func TestRunLogin_MixedCallbackCannotDiscloseTheCodeItCarries(t *testing.T) {
	const authorizationCode = "synthetic-mixed-callback-authorization-code"
	token := newFakeTokenServer(t)

	directory := t.TempDir()
	var stdout, stderr bytes.Buffer
	deps := LoginDependencies{
		HTTPClient:    token.server.Client(),
		Validator:     stubValidator{identity: siwc.Identity{Subject: "subject-1"}},
		BrowserOpener: mixedCallbackOpener(t, "access_denied", "rejected: "+authorizationCode, authorizationCode),
	}
	args := []string{
		"--profile-id", "acct-test",
		"--host-record", filepath.Join(directory, "state", "host.json"),
		"--profile-store", filepath.Join(directory, "state", "profiles"),
		"--authorization-endpoint", token.server.URL + "/api/accounts/authorize",
		"--token-endpoint", token.server.URL + "/api/accounts/oauth/token",
		"--timeout", "10s",
	}
	err := RunLogin(context.Background(), &stdout, &stderr, deps, args)
	if err == nil {
		t.Fatal("the command reported a successful sign-in against a denied callback")
	}
	if !strings.Contains(err.Error(), "access_denied") {
		t.Errorf("the command failed with %q, want the classified provider error", err.Error())
	}
	if got := token.exchangeCount(); got != 0 {
		t.Errorf("the token endpoint received %d exchanges, want 0: a denied callback must not be exchanged", got)
	}
	// The operational sink is main's exact rendering.
	fmt.Fprintf(&stderr, "openai-chatgpt-plan login error: %v\n", err)
	rendered := stdout.String() + stderr.String()
	if strings.Contains(rendered, authorizationCode) {
		t.Errorf("the command output discloses the authorization code: %q", rendered)
	}
	if strings.Contains(rendered, authorizationCode[:26]) {
		t.Errorf("the command output exposes a prefix of the authorization code: %q", rendered)
	}
}

// duplicateCodeCallbackOpener returns a browser opener that delivers a
// callback whose code parameter arrives TWICE.
//
// The query is written raw, because url.Values.Encode would drop the
// repetition: the defect is a parameter that arrives more than once.
func duplicateCodeCallbackOpener(t *testing.T, code string) func(context.Context, string) error {
	t.Helper()
	return func(_ context.Context, authorizationURL string) error {
		parsed, err := url.Parse(authorizationURL)
		if err != nil {
			return err
		}
		query := parsed.Query()
		raw := "state=" + url.QueryEscape(query.Get("state")) +
			"&code=&code=" + url.QueryEscape(code) +
			"&error=access_denied" +
			"&error_description=" + url.QueryEscape("rejected: "+code)
		response, err := http.Get(query.Get("redirect_uri") + "?" + raw)
		if err != nil {
			return err
		}
		defer func() { _ = response.Body.Close() }()
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
}

// TestRunLogin_DuplicateCodeCannotDiscloseTheCredential proves that a
// callback whose code parameter arrives twice cannot put the credential
// into the command's OPERATIONAL output.
//
// The defect was a SAMPLING defect: only the first value of a parameter was
// read, so a callback that carried an empty code and a credential in its
// second code looked like a PURE error. The credential was absent from the
// redaction set, and it reached the error text and the command's stderr.
//
// The class of the provider's failure stays visible, and nothing is
// exchanged.
func TestRunLogin_DuplicateCodeCannotDiscloseTheCredential(t *testing.T) {
	for _, testCase := range []struct {
		name string
		code string
	}{
		{
			name: "a short credential",
			code: "synthetic-duplicate-command-code-0123456789",
		},
		{
			name: "a credential that crosses the description bound",
			code: strings.Repeat("synthetic-bound-crossing-command-code-", 12),
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			token := newFakeTokenServer(t)

			directory := t.TempDir()
			var stdout, stderr bytes.Buffer
			deps := LoginDependencies{
				HTTPClient:    token.server.Client(),
				Validator:     stubValidator{identity: siwc.Identity{Subject: "subject-1"}},
				BrowserOpener: duplicateCodeCallbackOpener(t, testCase.code),
			}
			args := []string{
				"--profile-id", "acct-test",
				"--host-record", filepath.Join(directory, "state", "host.json"),
				"--profile-store", filepath.Join(directory, "state", "profiles"),
				"--authorization-endpoint", token.server.URL + "/api/accounts/authorize",
				"--token-endpoint", token.server.URL + "/api/accounts/oauth/token",
				"--timeout", "10s",
			}
			err := RunLogin(context.Background(), &stdout, &stderr, deps, args)
			if err == nil {
				t.Fatal("the command reported a successful sign-in against an ambiguous callback")
			}
			if !strings.Contains(err.Error(), "access_denied") {
				t.Errorf("the command failed with %q, want the classified provider error", err.Error())
			}
			if got := token.exchangeCount(); got != 0 {
				t.Errorf("the token endpoint received %d exchanges, want 0", got)
			}
			// The operational sink is main's exact rendering.
			fmt.Fprintf(&stderr, "openai-chatgpt-plan login error: %v\n", err)
			rendered := stdout.String() + stderr.String()
			if strings.Contains(rendered, testCase.code) {
				t.Errorf("the command output discloses the credential: %q", rendered)
			}
			if strings.Contains(rendered, testCase.code[:26]) {
				t.Errorf("the command output exposes a prefix of the credential: %q", rendered)
			}
		})
	}
}

// TestOpenBrowser_ReportsAMissingOpener proves that an operator whose PATH
// names no browser launcher is told so, instead of waiting for a browser
// that can never open.
//
// A detached launch hides the failure on its own: the shell backgrounds the
// job and exits successfully, so the command learns that the shell exited
// and never learns that no browser opened the URL.
func TestOpenBrowser_ReportsAMissingOpener(t *testing.T) {
	t.Setenv("PATH", "")
	err := openBrowser(context.Background(), "https://example.invalid/")
	if err == nil {
		t.Fatal("the opener reported success without a browser launcher")
	}
	if strings.Contains(err.Error(), "example.invalid") {
		t.Errorf("the failure discloses the target URL: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "xdg-open") && !strings.Contains(err.Error(), "open") &&
		!strings.Contains(err.Error(), "rundll32") {
		t.Errorf("the failure does not name the missing program: %q", err.Error())
	}
}

// TestRunLogin_ReportsTheManualURLWhenTheLauncherIsMissing proves that the
// operator always holds a usable manual path.
//
// A detached launch cannot report its outcome, so the manual URL is not a
// fallback for a failed launch: it is the operator's path in EVERY attempt.
// The URL is the HINT-FREE one, and it names the registration.
func TestRunLogin_ReportsTheManualURLWhenTheLauncherIsMissing(t *testing.T) {
	fake := newFakeAuthServer(t)
	directory := t.TempDir()
	hostRecord := filepath.Join(directory, "state", "host.json")
	profileStore := filepath.Join(directory, "state", "profiles")
	identity := siwc.Identity{Subject: "subject-1", EmailHint: "synthetic-account@example.invalid"}

	// The registration uses a controlled opener, and it stores the
	// retained identity hints of the account.
	var stdout, stderr bytes.Buffer
	registerArgs := []string{
		"--profile-id", "acct-test",
		"--host-record", hostRecord,
		"--profile-store", profileStore,
		"--authorization-endpoint", fake.server.URL + "/api/accounts/authorize",
		"--token-endpoint", fake.server.URL + "/api/accounts/oauth/token",
		"--timeout", "10s",
	}
	registerDeps := LoginDependencies{
		HTTPClient:    fake.server.Client(),
		Validator:     stubValidator{identity: identity},
		BrowserOpener: callbackOpener(t, "", ""),
	}
	if err := RunLogin(context.Background(), &stdout, &stderr, registerDeps, registerArgs); err != nil {
		t.Fatalf("RunLogin (register): %v (stderr: %s)", err, stderr.String())
	}

	// The returning authorization uses the PRODUCTION opener, and the
	// operator's PATH names no launcher at all.
	t.Setenv("PATH", "")
	stdout.Reset()
	stderr.Reset()
	reauthorizeDeps := LoginDependencies{
		HTTPClient: fake.server.Client(),
		Validator:  stubValidator{identity: identity},
	}
	reauthorizeArgs := []string{
		"--reauthorize",
		"--profile-id", "acct-test",
		"--host-record", hostRecord,
		"--profile-store", profileStore,
		"--authorization-endpoint", fake.server.URL + "/api/accounts/authorize",
		"--token-endpoint", fake.server.URL + "/api/accounts/oauth/token",
		"--timeout", "500ms",
	}
	if err := RunLogin(context.Background(), &stdout, &stderr, reauthorizeDeps, reauthorizeArgs); err == nil {
		t.Fatal("the command reported a successful sign-in without a browser")
	}
	rendered := stdout.String() + stderr.String()
	if !strings.Contains(rendered, "open this URL") {
		t.Errorf("the command did not report the URL to open: %q", rendered)
	}
	if !strings.Contains(rendered, "client_id=oaiapp_issued_1") {
		t.Errorf("the manual URL does not name the registration: %q", rendered)
	}
	if strings.Contains(rendered, "id_token_hint") {
		t.Errorf("the manual URL carries the retained ID token hint: %q", rendered)
	}
	if strings.Contains(rendered, "login_hint") {
		t.Errorf("the manual URL carries the retained account hint: %q", rendered)
	}
	if strings.Contains(rendered, "id-token-1") {
		t.Errorf("the manual URL carries the retained ID token: %q", rendered)
	}
	if strings.Contains(rendered, "synthetic-account@example.invalid") {
		t.Errorf("the manual URL carries the retained account: %q", rendered)
	}
}

// TestRunLogin_OwnSurfacesNeverCarryTheRetainedHints proves that the
// connector's OWN output surfaces never carry the hint-bearing URL.
//
// The launcher's ARGUMENT LIST is the one platform-inherent channel that
// carries it: an argument list is world-readable on a multi-user host, and
// the platform's URL handlers take the URL as an argument and offer no
// argument-free channel. The residual is BOUNDED: the launcher's
// arguments carry the retained hints, and they carry no token and no
// authorization code.
//
// Switching the launcher to the hint-free URL as well would cost the
// provider's account-selector screen. That is an owner decision, and this
// test therefore pins the residual's SCOPE rather than its absence.
func TestRunLogin_OwnSurfacesNeverCarryTheRetainedHints(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the controlled launcher is a POSIX script")
	}
	fake := newFakeAuthServer(t)
	directory := t.TempDir()
	hostRecord := filepath.Join(directory, "state", "host.json")
	profileStore := filepath.Join(directory, "state", "profiles")
	identity := siwc.Identity{Subject: "subject-1", EmailHint: "synthetic-account@example.invalid"}

	// The controlled launcher records the argument list it was given.
	launcherDirectory := t.TempDir()
	argumentFile := filepath.Join(launcherDirectory, "arguments")
	script := "#!/bin/sh\nfor argument in \"$@\"; do printf '%s\\n' \"$argument\" >> '" + argumentFile + "'; done\n"
	if err := os.WriteFile(filepath.Join(launcherDirectory, "xdg-open"), []byte(script), 0o700); err != nil {
		t.Fatalf("write the controlled launcher: %v", err)
	}
	t.Setenv("PATH", launcherDirectory)

	var stdout, stderr bytes.Buffer
	registerArgs := []string{
		"--profile-id", "acct-test",
		"--host-record", hostRecord,
		"--profile-store", profileStore,
		"--authorization-endpoint", fake.server.URL + "/api/accounts/authorize",
		"--token-endpoint", fake.server.URL + "/api/accounts/oauth/token",
		"--timeout", "10s",
	}
	registerDeps := LoginDependencies{
		HTTPClient:    fake.server.Client(),
		Validator:     stubValidator{identity: identity},
		BrowserOpener: callbackOpener(t, "", ""),
	}
	if err := RunLogin(context.Background(), &stdout, &stderr, registerDeps, registerArgs); err != nil {
		t.Fatalf("RunLogin (register): %v (stderr: %s)", err, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	reauthorizeDeps := LoginDependencies{
		HTTPClient: fake.server.Client(),
		Validator:  stubValidator{identity: identity},
	}
	reauthorizeArgs := []string{
		"--reauthorize",
		"--profile-id", "acct-test",
		"--host-record", hostRecord,
		"--profile-store", profileStore,
		"--authorization-endpoint", fake.server.URL + "/api/accounts/authorize",
		"--token-endpoint", fake.server.URL + "/api/accounts/oauth/token",
		"--timeout", "500ms",
	}
	if err := RunLogin(context.Background(), &stdout, &stderr, reauthorizeDeps, reauthorizeArgs); err == nil {
		t.Fatal("the command reported a successful sign-in without a browser")
	}

	// The connector's own surfaces carry neither hint.
	rendered := stdout.String() + stderr.String()
	for _, forbidden := range []string{"id_token_hint", "login_hint", "id-token-1", "synthetic-account@example.invalid"} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("the command output carries %q: %q", forbidden, rendered)
		}
	}
	// The connector's own process arguments carry neither hint either.
	if own, err := os.ReadFile("/proc/self/cmdline"); err == nil {
		for _, forbidden := range []string{"id_token_hint", "login_hint", "id-token-1", "synthetic-account@example.invalid"} {
			if strings.Contains(string(own), forbidden) {
				t.Errorf("the command's own argument list carries %q", forbidden)
			}
		}
	}
	// The residual is bounded: the launcher's arguments carry the retained
	// HINTS - the id_token_hint is the retained ID token - and they carry
	// no bearer token, no refresh token and no authorization code.
	arguments := waitForArguments(t, argumentFile)
	for _, forbidden := range []string{"refresh-token-1", "access-token-1", "synthetic-authorization-code"} {
		if strings.Contains(arguments, forbidden) {
			t.Errorf("the launcher's arguments carry the credential %q: %q", forbidden, arguments)
		}
	}
}

// waitForArguments reads the argument list the controlled launcher recorded.
func waitForArguments(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err == nil && len(raw) > 0 {
			return string(raw)
		}
		if time.Now().After(deadline) {
			t.Fatal("the controlled launcher did not record its arguments")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestOpenBrowser_RefusesACancelledContext proves that a cancelled sign-in
// starts no launcher at all.
//
// The launcher is an owned child process: a command that is already over
// must not leave one behind.
func TestOpenBrowser_RefusesACancelledContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the controlled launcher is a POSIX script")
	}
	directory := t.TempDir()
	marker := filepath.Join(directory, "started")
	script := "#!/bin/sh\nprintf started > '" + marker + "'\n"
	if err := os.WriteFile(filepath.Join(directory, "xdg-open"), []byte(script), 0o700); err != nil {
		t.Fatalf("write the controlled launcher: %v", err)
	}
	t.Setenv("PATH", directory)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := openBrowser(ctx, "https://example.invalid/"); err == nil {
		t.Fatal("the opener started a launcher for a cancelled command")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("the cancelled command started the launcher")
	}
}

// TestOpenBrowser_KeepsNoWorkerBehind proves that the opener leaves no
// worker waiting for the launcher.
//
// The browser may outlive the login, so the command neither waits for the
// launcher nor signals it. It therefore holds no handle to it and keeps no
// goroutine for it.
func TestOpenBrowser_KeepsNoWorkerBehind(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the controlled launcher is a POSIX script")
	}
	directory := t.TempDir()
	marker := filepath.Join(directory, "started")
	finished := filepath.Join(directory, "finished")
	// The launcher stays alive for the whole check, and the sleep it uses is
	// resolved DELIBERATELY: the script runs with a PATH that holds only
	// the controlled launcher, so an unqualified sleep would not be found
	// at all, the shell would report "sleep: not found", and the launcher
	// would exit at once instead of staying alive.
	sleep := resolveExecutable(t, "sleep")
	script := "#!/bin/sh\nprintf started > '" + marker + "'\n" +
		sleep + " 30\nprintf finished > '" + finished + "'\n"
	if err := os.WriteFile(filepath.Join(directory, "xdg-open"), []byte(script), 0o700); err != nil {
		t.Fatalf("write the controlled launcher: %v", err)
	}
	t.Setenv("PATH", directory)

	before := runtime.NumGoroutine()
	if err := openBrowser(context.Background(), "https://example.invalid/"); err != nil {
		t.Fatalf("openBrowser: %v", err)
	}
	// The launcher writes its marker and then stays alive: the command
	// must not be waiting for it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the launcher was not started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The launcher is STILL inside its sleep: it has not reached the end of
	// the script. The fixture therefore really keeps a live launcher around
	// while the command has already returned, and the property under test
	// is the one the fixture claims to exercise.
	if _, err := os.Stat(finished); err == nil {
		t.Fatal("the launcher finished before the check: it did not stay alive")
	}
	// Give the runtime a moment to schedule whatever the opener started.
	time.Sleep(50 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > before+1 {
		t.Errorf("the opener left %d workers behind", after-before)
	}
}

// resolveExecutable returns the ABSOLUTE path of a helper program.
//
// The controlled launchers run with a PATH that holds only the launcher
// itself, so an unqualified name inside a launcher's script is NOT found:
// the shell reports "not found" and the script continues as if the program
// had run. A fixture that relied on such a program would prove nothing, so
// the path is resolved DELIBERATELY, while the real PATH is still in place,
// and the script names the program by its absolute path.
func resolveExecutable(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("resolve %q: %v", name, err)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("the resolved %q is %q, want an absolute path", name, path)
	}
	return path
}
