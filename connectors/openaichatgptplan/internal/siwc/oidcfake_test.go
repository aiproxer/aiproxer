package siwc

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The fake OIDC provider of this package's tests.
//
// It stands in for OpenAI's OIDC configuration and its published key set.
// The documents it serves are the real SHAPE of the provider's documents -
// a discovery document that names the issuer and the key set, and a key
// set that names an RSA key with its modulus and exponent - so that the
// validation is exercised against the wire the connector reads.
//
// The server COUNTS its requests, because two properties of the
// validation are properties of the traffic: a token that names an
// algorithm the connector does not accept must be refused without the key
// set being fetched at all, and a key set that is cached must be fetched
// once.

var (
	testKeyOnce  sync.Once
	testKeyValue *rsa.PrivateKey
	testKeyError error
)

// testRSAKey returns the RSA key the tests sign with.
//
// The key is generated ONCE for the test binary: a 2048-bit key takes
// long enough to generate that a per-test key would dominate the run.
func testRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	testKeyOnce.Do(func() {
		testKeyValue, testKeyError = rsa.GenerateKey(rand.Reader, 2048)
	})
	if testKeyError != nil {
		t.Fatalf("generate the test key: %v", testKeyError)
	}
	return testKeyValue
}

// fakeOIDCServer serves the provider's OIDC configuration and key set.
type fakeOIDCServer struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	keyID  string
	// issuer is the issuer the configuration publishes. Empty means the
	// fake's own address.
	issuer string

	mu                sync.Mutex
	discoveryRequests int
	jwksRequests      int
	// discoveryBody overrides the discovery document. Empty means the
	// documented one.
	discoveryBody string
	// jwksBody overrides the key set. Empty means the documented one.
	jwksBody string
	// jwksStatus is the status the key-set endpoint answers with. Zero
	// means 200.
	jwksStatus int
}

// newFakeOIDCServer starts the fake OIDC provider.
func newFakeOIDCServer(t *testing.T) *fakeOIDCServer {
	t.Helper()
	fake := &fakeOIDCServer{key: testRSAKey(t), keyID: "test-key-1"}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.route))
	t.Cleanup(fake.server.Close)
	return fake
}

// route dispatches the two documents.
func (f *fakeOIDCServer) route(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		f.serveDiscovery(w)
	case "/.well-known/jwks.json":
		f.serveKeySet(w)
	default:
		http.NotFound(w, r)
	}
}

// serveDiscovery answers the OIDC configuration.
func (f *fakeOIDCServer) serveDiscovery(w http.ResponseWriter) {
	f.mu.Lock()
	f.discoveryRequests++
	body := f.discoveryBody
	f.mu.Unlock()
	if body == "" {
		body = f.discoveryDocument()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// serveKeySet answers the key set.
func (f *fakeOIDCServer) serveKeySet(w http.ResponseWriter) {
	f.mu.Lock()
	f.jwksRequests++
	body := f.jwksBody
	status := f.jwksStatus
	f.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	if body == "" {
		body = f.keySetDocument()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// discoveryDocument is the OIDC configuration the fake publishes.
//
// The issuer is the fake's own address by default, and the issuer of a
// token is compared with it: a token that names another issuer is refused.
func (f *fakeOIDCServer) discoveryDocument() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	issuer := f.issuer
	if issuer == "" {
		issuer = f.server.URL
	}
	return fmt.Sprintf(
		`{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q,"revocation_endpoint":%q,"userinfo_endpoint":%q}`,
		issuer,
		f.server.URL+"/api/accounts/authorize",
		f.server.URL+"/api/accounts/oauth/token",
		f.server.URL+"/.well-known/jwks.json",
		f.server.URL+"/api/accounts/oauth/revoke",
		f.server.URL+"/api/accounts/oauth/userinfo",
	)
}

// setIssuer replaces the issuer the configuration publishes.
func (f *fakeOIDCServer) setIssuer(issuer string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issuer = issuer
}

// keySetDocument is the key set the fake publishes.
func (f *fakeOIDCServer) keySetDocument() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	exponent := big.NewInt(int64(f.key.E)).Bytes()
	return fmt.Sprintf(
		`{"keys":[{"kty":"RSA","kid":%q,"use":"sig","alg":"RS256","n":%q,"e":%q}]}`,
		f.keyID,
		base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()),
		base64.RawURLEncoding.EncodeToString(exponent),
	)
}

// setKeySet replaces the key the fake publishes.
//
// It is the provider's key ROTATION: the new set publishes a new key id,
// and the key the old set published is withdrawn.
func (f *fakeOIDCServer) setKeySet(key *rsa.PrivateKey, keyID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.key = key
	f.keyID = keyID
}

// discoveryFetches returns how often the OIDC configuration was fetched.
func (f *fakeOIDCServer) discoveryFetches() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.discoveryRequests
}

// jwksFetches returns how often the key set was fetched.
func (f *fakeOIDCServer) jwksFetches() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.jwksRequests
}

// setDiscoveryBody replaces the OIDC configuration the fake publishes.
func (f *fakeOIDCServer) setDiscoveryBody(body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.discoveryBody = body
}

// setKeySetBody replaces the key set the fake publishes.
func (f *fakeOIDCServer) setKeySetBody(body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jwksBody = body
}

// setKeySetStatus replaces the status the key-set endpoint answers with.
func (f *fakeOIDCServer) setKeySetStatus(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jwksStatus = status
}

// encodeJWTSegment encodes one part of a JWT.
func encodeJWTSegment(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode a JWT segment: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// base64RawURL encodes raw bytes the way a JWT encodes them.
func base64RawURL(t *testing.T, raw string) string {
	t.Helper()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// signRS256 signs a JWT with the RSA key.
func signRS256(t *testing.T, key *rsa.PrivateKey, header, claims map[string]any) string {
	t.Helper()
	signingInput := encodeJWTSegment(t, header) + "." + encodeJWTSegment(t, claims)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign the JWT: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// signHS256 signs a JWT with a symmetric key.
//
// It is the algorithm-confusion fixture: a token that names HS256 and is
// signed with the provider's PUBLIC key material must be refused, and it
// must be refused before the key set is consulted.
func signHS256(t *testing.T, secret []byte, header, claims map[string]any) string {
	t.Helper()
	signingInput := encodeJWTSegment(t, header) + "." + encodeJWTSegment(t, claims)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// signRS256Raw signs a JWT whose header and payload are RAW JSON.
//
// The member order of a document matters for the case-collision
// fixtures: a decoder that merges a member name with a case variant of it
// resolves the collision by the order the document carries, so the fixture
// must be able to place the collision where it wants it. A map cannot
// express that - the encoder sorts the names - so those fixtures build
// the document as text.
func signRS256Raw(t *testing.T, key *rsa.PrivateKey, header, payload string) string {
	t.Helper()
	signingInput := base64.RawURLEncoding.EncodeToString([]byte(header)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(payload))
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign the JWT: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// requireValidatorClass asserts the class of a classified failure.
func requireValidatorClass(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("the validation succeeded, want the %q class", want)
	}
	var classified *ValidatorError
	if !errors.As(err, &classified) {
		t.Fatalf("the failure is not a classified rejection: %v", err)
	}
	if classified.Class != want {
		t.Fatalf("the failure class = %q, want %q", classified.Class, want)
	}
}

// tokenMaterial is one piece of credential material a failure must not
// carry.
//
// The name is a CATEGORY. The value is the material itself, and it is
// never reported: a test failure that repeats the secret it found is a
// second copy of that secret, and the guarantee this helper exists for is
// that no copy leaves the code.
type tokenMaterial struct {
	name  string
	value string
}

// requireNoTokenMaterial asserts that a failure carries no token material.
//
// Every rendering a caller may use is checked, and the UNWRAPPED cause is
// checked as well: the cause is kept for classification, and it must not
// carry the token either.
//
// The failure message names the CATEGORY and never the offending bytes.
func requireNoTokenMaterial(t *testing.T, err error, materials ...tokenMaterial) {
	t.Helper()
	if err == nil {
		return
	}
	renderings := []string{err.Error()}
	for _, verb := range []string{"%v", "%s", "%q", "%+v", "%#v"} {
		renderings = append(renderings, fmt.Sprintf(verb, err))
	}
	if cause := errors.Unwrap(err); cause != nil {
		renderings = append(renderings, cause.Error())
	}
	for _, rendering := range renderings {
		for _, material := range materials {
			if material.value == "" {
				continue
			}
			if strings.Contains(rendering, material.value) {
				t.Errorf("the failure carries token material (category %q)", material.name)
			}
		}
	}
}
