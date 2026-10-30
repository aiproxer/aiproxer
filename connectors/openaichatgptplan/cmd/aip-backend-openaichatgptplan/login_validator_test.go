package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/siwc"
)

// The login command's own validator.
//
// The command may be given a validator, and the tests use that seam. The
// command ALSO builds the connector's own validator when it is given
// none, and this file proves that the built validator is the real one: it
// consults the provider's OIDC configuration, and it refuses a token that
// does not verify.

// fakeOIDCServer serves the provider's OIDC configuration and its key set.
//
// It stands in for OpenAI's discovery document and its published keys. The
// key set it serves holds no usable key, so a token that reaches the
// verification is refused - and the fact that the configuration was
// consulted at all is what proves the command's own validator ran.
type fakeOIDCServer struct {
	server *httptest.Server

	mu                sync.Mutex
	discoveryRequests int
	jwksRequests      int
}

// newFakeOIDCServer starts the fake OIDC provider.
func newFakeOIDCServer(t *testing.T) *fakeOIDCServer {
	t.Helper()
	fake := &fakeOIDCServer{}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.route))
	t.Cleanup(fake.server.Close)
	return fake
}

// route dispatches the two documents.
func (f *fakeOIDCServer) route(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/openid-configuration"):
		f.mu.Lock()
		f.discoveryRequests++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":   f.server.URL,
			"jwks_uri": f.server.URL + "/.well-known/jwks.json",
		})
	case strings.HasSuffix(r.URL.Path, "/jwks.json"):
		f.mu.Lock()
		f.jwksRequests++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		// The key set holds no usable key: a token that reaches the
		// verification cannot be verified.
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{}})
	default:
		http.NotFound(w, r)
	}
}

// discoveryFetches returns how often the OIDC configuration was fetched.
func (f *fakeOIDCServer) discoveryFetches() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.discoveryRequests
}

// TestRunLogin_UsesTheConnectorsOwnValidator proves that the command
// builds the connector's OWN validator when it is given none.
//
// The command reaches the provider's OIDC configuration, so the validator
// it built is the real one, and it refuses the token the fake provider
// answered with.
func TestRunLogin_UsesTheConnectorsOwnValidator(t *testing.T) {
	fake := newFakeAuthServer(t)
	oidc := newFakeOIDCServer(t)
	directory := t.TempDir()

	// The ID token is a well-formed JWT that no key can verify: the
	// validation therefore reaches the provider's configuration, and the
	// command's own validator is the thing that refuses it.
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"key-1"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(
		`{"iss":"https://auth.example.com","aud":"oaiapp_issued_1","sub":"subject-1",` +
			`"exp":4102444800,"nonce":"nonce-1"}`,
	))
	fake.mu.Lock()
	fake.idToken = header + "." + payload + ".c2ln"
	fake.mu.Unlock()

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
		"--profile-store", filepath.Join(directory, "state", "profiles"),
		"--authorization-endpoint", fake.server.URL + "/api/accounts/authorize",
		"--token-endpoint", fake.server.URL + "/api/accounts/oauth/token",
		"--discovery-endpoint", oidc.server.URL + "/.well-known/openid-configuration",
		"--timeout", "10s",
	}
	err := RunLogin(context.Background(), &stdout, &stderr, deps, args)
	if err == nil {
		t.Fatal("the command accepted a token that cannot be verified")
	}
	if requests := oidc.discoveryFetches(); requests == 0 {
		t.Error("the command did not consult the provider's OIDC configuration")
	}
	if strings.Contains(err.Error(), "not part of this connector build") {
		t.Errorf("the command used the placeholder validator: %v", err)
	}
	var classified *siwc.ValidatorError
	if !errors.As(err, &classified) {
		t.Fatalf("the failure is not a classified rejection: %v", err)
	}
	if classified.Class != siwc.ValidatorClassUnavailable {
		t.Errorf("the failure class = %q, want %q", classified.Class, siwc.ValidatorClassUnavailable)
	}
}

// TestRunLogin_AnnouncesTheDiscoveryOverride proves that the command says
// that the override replaces the trust root.
//
// The notice must not repeat the URL: a URL may carry credentials in its
// userinfo or its query.
func TestRunLogin_AnnouncesTheDiscoveryOverride(t *testing.T) {
	fake := newFakeAuthServer(t)
	oidc := newFakeOIDCServer(t)
	directory := t.TempDir()

	var stdout, stderr bytes.Buffer
	deps := LoginDependencies{
		HTTPClient: fake.server.Client(),
		Validator:  stubValidator{identity: siwc.Identity{Subject: "subject-1"}},
		BrowserOpener: func(context.Context, string) error {
			return fmt.Errorf("the browser is not available")
		},
	}
	args := []string{
		"--profile-id", "acct-test",
		"--host-record", filepath.Join(directory, "state", "host.json"),
		"--profile-store", filepath.Join(directory, "state", "profiles"),
		"--discovery-endpoint", oidc.server.URL + "/.well-known/openid-configuration?token=secret-value",
		"--timeout", "1s",
	}
	_ = RunLogin(context.Background(), &stdout, &stderr, deps, args)

	announced := stderr.String()
	if !strings.Contains(announced, "trust root") {
		t.Errorf("the command did not announce the trust-root replacement: %q", announced)
	}
	if strings.Contains(announced, "secret-value") {
		t.Error("the announcement repeated a secret of the override URL")
	}
	if strings.Contains(announced, oidc.server.URL) {
		t.Error("the announcement repeated the override URL")
	}
}
