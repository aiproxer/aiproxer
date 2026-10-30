package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/siwc"
)

// The WHOLE query of a callback, at the command level.
//
// The command prints the failure of the attempt on its standard error, and
// the operator - or a support transcript - reads it there. A callback whose
// query the parser cannot read must therefore not reach the command's
// output with a credential in it.
//
// The defect was a LOSSY parse: r.URL.Query() discards the parser's error,
// so a parameter with an invalid percent escape or an unescaped semicolon
// was silently dropped, and a credential the provider echoed in the dropped
// parameter was absent from the redaction set of the error that followed.

// malformedCallbackOpener returns a browser opener that delivers a callback
// whose query the parser cannot read.
//
// The query is written RAW, because the defect is about a parameter the
// parser drops: url.Values.Encode would encode the malformation away.
func malformedCallbackOpener(t *testing.T, credential, malformation string) func(context.Context, string) error {
	t.Helper()
	return func(_ context.Context, authorizationURL string) error {
		parsed, err := url.Parse(authorizationURL)
		if err != nil {
			return err
		}
		query := parsed.Query()
		raw := "state=" + url.QueryEscape(query.Get("state")) +
			"&code=&code=" + credential + malformation +
			"&error=access_denied" +
			"&error_description=" + url.QueryEscape("rejected: "+credential)
		response, err := http.Get(query.Get("redirect_uri") + "?" + raw)
		if err != nil {
			return err
		}
		defer func() { _ = response.Body.Close() }()
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
}

// TestRunLogin_MalformedCallbackCannotDiscloseTheCredential proves that a
// callback whose query the parser cannot read cannot put a credential into
// the command's OPERATIONAL output.
//
// The operational sink is main's exact rendering, and the credential is
// absent from it - not whole, and not as a prefix. Nothing is exchanged and
// nothing is persisted.
func TestRunLogin_MalformedCallbackCannotDiscloseTheCredential(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		credential string
	}{
		{
			name:       "a full credential",
			credential: "synthetic-malformed-command-code-0123456789",
		},
		{
			name:       "a short credential",
			credential: "synthetic-short-code-abc",
		},
		{
			// The description is longer than the bound, so a credential
			// that is not redacted leaves a LONG PREFIX of itself behind.
			name:       "a credential that crosses the description bound",
			credential: strings.Repeat("synthetic-bound-crossing-command-code-", 12),
		},
	} {
		for _, malformation := range []struct {
			name  string
			value string
		}{
			{name: "an invalid percent escape", value: "%ZZ"},
			{name: "an unescaped semicolon", value: ";ignored"},
		} {
			t.Run(testCase.name+": "+malformation.name, func(t *testing.T) {
				token := newFakeTokenServer(t)
				directory := t.TempDir()
				profileStore := filepath.Join(directory, "state", "profiles")

				var stdout, stderr bytes.Buffer
				deps := LoginDependencies{
					HTTPClient: token.server.Client(),
					Validator:  stubValidator{identity: siwc.Identity{Subject: "subject-1"}},
					BrowserOpener: malformedCallbackOpener(
						t,
						testCase.credential,
						malformation.value,
					),
				}
				args := []string{
					"--profile-id", "acct-test",
					"--host-record", filepath.Join(directory, "state", "host.json"),
					"--profile-store", profileStore,
					"--authorization-endpoint", token.server.URL + "/api/accounts/authorize",
					"--token-endpoint", token.server.URL + "/api/accounts/oauth/token",
					"--timeout", "10s",
				}
				err := RunLogin(context.Background(), &stdout, &stderr, deps, args)
				if err == nil {
					t.Fatal("the command reported a successful sign-in against a malformed callback")
				}
				if strings.Contains(err.Error(), testCase.credential) {
					t.Errorf("the command failed with %q, which discloses the credential", err.Error())
				}
				if len(testCase.credential) >= 26 && strings.Contains(err.Error(), testCase.credential[:26]) {
					t.Errorf("the command failed with %q, which discloses a prefix of the credential", err.Error())
				}
				// The refusal is the connector's own: it names the
				// malformation and it carries no provider text.
				if !strings.Contains(strings.ToLower(err.Error()), "malformed") {
					t.Errorf("the command failed with %q, which does not name the malformation", err.Error())
				}
				if strings.Contains(err.Error(), "rejected") {
					t.Errorf("the command failed with %q, which carries the provider's description", err.Error())
				}
				if got := token.exchangeCount(); got != 0 {
					t.Errorf("the token endpoint received %d exchanges, want 0", got)
				}
				// The operational sink is main's exact rendering.
				fmt.Fprintf(&stderr, "openai-chatgpt-plan login error: %v\n", err)
				rendered := stdout.String() + stderr.String()
				if strings.Contains(rendered, testCase.credential) {
					t.Errorf("the command output discloses the credential: %q", rendered)
				}
				if len(testCase.credential) >= 26 && strings.Contains(rendered, testCase.credential[:26]) {
					t.Errorf("the command output exposes a prefix of the credential: %q", rendered)
				}
				// Nothing was persisted.
				store, storeErr := siwc.NewProfileStore(profileStore)
				if storeErr != nil {
					t.Fatalf("NewProfileStore: %v", storeErr)
				}
				summaries, listErr := store.List()
				if listErr != nil {
					t.Fatalf("List: %v", listErr)
				}
				if len(summaries) != 0 {
					t.Errorf("the command persisted %d registrations, want 0", len(summaries))
				}
			})
		}
	}
}

// TestRunLogin_WellFormedAccessDeniedKeepsItsObservability proves that the
// refusal of a malformed query does not swallow the observability of a
// WELL-FORMED one.
//
// The provider's error code is a class of failure and the recovery differs
// per code, so the class stays visible on the command's error.
func TestRunLogin_WellFormedAccessDeniedKeepsItsObservability(t *testing.T) {
	token := newFakeTokenServer(t)
	directory := t.TempDir()

	var stdout, stderr bytes.Buffer
	deps := LoginDependencies{
		HTTPClient:    token.server.Client(),
		Validator:     stubValidator{identity: siwc.Identity{Subject: "subject-1"}},
		BrowserOpener: callbackOpener(t, "access_denied", "the user declined the sign-in"),
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
	if !strings.Contains(err.Error(), "the user declined the sign-in") {
		t.Errorf("the command failed with %q, want the provider's reason", err.Error())
	}
	if got := token.exchangeCount(); got != 0 {
		t.Errorf("the token endpoint received %d exchanges, want 0", got)
	}
}
