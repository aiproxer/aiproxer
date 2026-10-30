package siwc

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The strict reading of the documents, the authority the keys belong to,
// and the bounds that protect the provider.
//
// Every test here fails against an implementation that does not have the
// property it names, and the mutants that prove it are recorded as
// separate overlay runs.

// TestOIDCValidator_RefusesACaseVariantOfTheAudience proves that the
// audience is read from the EXACT member.
//
// Go's typed JSON decoder matches a field's name case-insensitively, so a
// payload that carries both "aud" and "AUD" decides by itself which value
// the audience check reads. The exact member decides here: the payload
// below carries the WRONG audience under its registered name and the
// RIGHT one under a case alias, and the token is refused.
func TestOIDCValidator_RefusesACaseVariantOfTheAudience(t *testing.T) {
	fake := newFakeOIDCServer(t)
	validator := newTestValidator(t, fake, testIssuer)
	// The payload carries the WRONG audience under its registered name
	// FIRST, and the right one under the case alias afterwards. A decoder
	// that merges the two resolves the collision by the order the
	// document carries, and the alias would win.
	payload := fmt.Sprintf(
		`{"iss":%q,"aud":"oaiapp_another_registration","AUD":%q,"sub":%q,"exp":%d,"nonce":%q}`,
		testIssuer,
		testAudience,
		testSubject,
		time.Now().Add(time.Hour).Unix(),
		testNonce,
	)
	token := signRS256Raw(t, fake.key, `{"alg":"RS256","kid":"`+fake.keyID+`"}`, payload)

	_, err := validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: token},
		TokenExpectation{Nonce: testNonce, Audience: testAudience},
	)
	requireValidatorClass(t, err, ValidatorClassAudience)
	requireNoTokenMaterial(t, err, tokenMaterial{name: "ID token", value: token})
}

// TestOIDCValidator_RefusesACaseVariantOfTheKeyID proves that the key id
// is read from the EXACT member.
//
// The exact key id names a key the provider does not publish, and the
// case alias names the one it does. A decoder that merges the two would
// verify the signature against the published key, and the token would be
// accepted.
func TestOIDCValidator_RefusesACaseVariantOfTheKeyID(t *testing.T) {
	fake := newFakeOIDCServer(t)
	validator := newTestValidator(t, fake, testIssuer)
	// The header carries the UNKNOWN key id under its registered name
	// first, and the published one under the case alias afterwards.
	header := fmt.Sprintf(`{"alg":"RS256","kid":"key-the-provider-does-not-publish","KID":%q}`, fake.keyID)
	payload := fmt.Sprintf(
		`{"iss":%q,"aud":%q,"sub":%q,"exp":%d,"nonce":%q}`,
		testIssuer,
		testAudience,
		testSubject,
		time.Now().Add(time.Hour).Unix(),
		testNonce,
	)
	token := signRS256Raw(t, fake.key, header, payload)

	_, err := validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: token},
		TokenExpectation{Nonce: testNonce, Audience: testAudience},
	)
	requireValidatorClass(t, err, ValidatorClassSignature)
}

// TestOIDCValidator_RefusesAKeySetWithACaseVariantKeyID proves that a key
// set cannot offer a key under a case alias of its key id.
//
// The entry below names its key id only through the alias, so the key set
// holds no key under the published name, and the token cannot be
// verified.
func TestOIDCValidator_RefusesAKeySetWithACaseVariantKeyID(t *testing.T) {
	fake := newFakeOIDCServer(t)
	validator := newTestValidator(t, fake, testIssuer)
	// The entry names its key id only through the case alias.
	entry := fmt.Sprintf(
		`{"kty":"RSA","KID":%q,"use":"sig","alg":"RS256","n":%q,"e":"AQAB"}`,
		fake.keyID,
		base64.RawURLEncoding.EncodeToString(fake.key.N.Bytes()),
	)
	fake.setKeySetBody(`{"keys":[` + entry + `]}`)
	token := signRS256(t, fake.key, validHeader(fake.keyID), validClaims(time.Now()))

	_, err := validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: token},
		TokenExpectation{Nonce: testNonce, Audience: testAudience},
	)
	requireValidatorClass(t, err, ValidatorClassUnavailable)
}

// TestOIDCValidator_RefusesACaseVariantOfTheKeySetURI proves that the
// key set is fetched from the EXACT member of the configuration.
//
// The exact member names a key set that does not hold the signing key,
// and the case alias names the one that does. A decoder that merges the
// two would fetch the alias and verify the token against it.
func TestOIDCValidator_RefusesACaseVariantOfTheKeySetURI(t *testing.T) {
	fake := newFakeOIDCServer(t)
	validator := newTestValidator(t, fake, testIssuer)
	// The configuration carries an unreachable key set under its
	// registered name first, and the real one under the case alias
	// afterwards.
	document := fmt.Sprintf(
		`{"issuer":%q,"jwks_uri":"http://127.0.0.1:1/jwks.json","JWKS_URI":%q}`,
		testIssuer,
		fake.server.URL+"/.well-known/jwks.json",
	)
	fake.setDiscoveryBody(document)
	token := signRS256(t, fake.key, validHeader(fake.keyID), validClaims(time.Now()))

	_, err := validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: token},
		TokenExpectation{Nonce: testNonce, Audience: testAudience},
	)
	requireValidatorClass(t, err, ValidatorClassUnavailable)
}

// TestOIDCValidator_AcceptsAnExtensionClaim proves that the exact reading
// is not a whitelist.
//
// A claim the connector does not recognize is carried and ignored: it is
// not a reason to refuse a token.
func TestOIDCValidator_AcceptsAnExtensionClaim(t *testing.T) {
	fake := newFakeOIDCServer(t)
	validator := newTestValidator(t, fake, testIssuer)
	claims := validClaims(time.Now())
	claims["https://api.openai.com/auth"] = map[string]any{
		"per_user_salt": "salt-value",
	}
	claims["custom_extension_claim"] = "value"
	token := signRS256(t, fake.key, validHeader(fake.keyID), claims)

	identity, err := validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: token},
		TokenExpectation{Nonce: testNonce, Audience: testAudience},
	)
	if err != nil {
		t.Fatalf("ValidateIDToken: %v", err)
	}
	if identity.Subject != testSubject {
		t.Errorf("the subject = %q, want %q", identity.Subject, testSubject)
	}
}

// TestOIDCValidator_RefusesATokenAfterTheKeyAuthorityChanges proves that
// the cached keys belong to the authority that produced them.
//
// The configuration is refreshed and it selects ANOTHER key set. The keys
// of the previous authority are not a fallback: a token that verifies
// against them must be refused, because the connector no longer trusts
// that key.
func TestOIDCValidator_RefusesATokenAfterTheKeyAuthorityChanges(t *testing.T) {
	fake := newFakeOIDCServer(t)
	clock := &fakeClock{now: time.Now()}
	validator, err := NewOIDCValidator(OIDCValidatorConfig{
		OIDCClientConfig: OIDCClientConfig{
			DiscoveryURL:  fake.server.URL + "/.well-known/openid-configuration",
			HTTPClient:    fake.server.Client(),
			Now:           clock.nowFunc,
			DiscoveryTTL:  time.Minute,
			JWKSTTL:       time.Hour,
			RenewInterval: -1,
		},
	})
	if err != nil {
		t.Fatalf("NewOIDCValidator: %v", err)
	}
	fake.setIssuer(testIssuer)
	token := signRS256(t, fake.key, validHeader(fake.keyID), validClaims(time.Now()))

	if _, err := validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: token},
		TokenExpectation{Nonce: testNonce, Audience: testAudience},
	); err != nil {
		t.Fatalf("the first validation: %v", err)
	}

	// The configuration ages out, and it now selects another key set that
	// cannot be reached.
	clock.advance(2 * time.Minute)
	document := fake.discoveryDocument()
	document = strings.Replace(
		document,
		fmt.Sprintf(`"jwks_uri":%q`, fake.server.URL+"/.well-known/jwks.json"),
		fmt.Sprintf(`"jwks_uri":%q`, "http://127.0.0.1:1/jwks.json"),
		1,
	)
	fake.setDiscoveryBody(document)

	_, err = validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: token},
		TokenExpectation{Nonce: testNonce, Audience: testAudience},
	)
	requireValidatorClass(t, err, ValidatorClassUnavailable)
}

// TestOIDCValidator_RefusesATokenAfterTheIssuerChanges proves that the
// issuer and the key set are read from ONE authority.
//
// The refreshed configuration names another issuer AND another key set. A
// token that verifies against the previous key set must be refused, and
// the issuer check must run against the configuration that goes with the
// key.
func TestOIDCValidator_RefusesATokenAfterTheIssuerChanges(t *testing.T) {
	fake := newFakeOIDCServer(t)
	clock := &fakeClock{now: time.Now()}
	validator, err := NewOIDCValidator(OIDCValidatorConfig{
		OIDCClientConfig: OIDCClientConfig{
			DiscoveryURL:  fake.server.URL + "/.well-known/openid-configuration",
			HTTPClient:    fake.server.Client(),
			Now:           clock.nowFunc,
			DiscoveryTTL:  time.Minute,
			JWKSTTL:       time.Hour,
			RenewInterval: -1,
		},
	})
	if err != nil {
		t.Fatalf("NewOIDCValidator: %v", err)
	}
	fake.setIssuer(testIssuer)
	token := signRS256(t, fake.key, validHeader(fake.keyID), validClaims(time.Now()))

	if _, err := validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: token},
		TokenExpectation{Nonce: testNonce, Audience: testAudience},
	); err != nil {
		t.Fatalf("the first validation: %v", err)
	}

	clock.advance(2 * time.Minute)
	fake.setIssuer("https://auth.example.com")
	document := fake.discoveryDocument()
	document = strings.Replace(
		document,
		fmt.Sprintf(`"jwks_uri":%q`, fake.server.URL+"/.well-known/jwks.json"),
		fmt.Sprintf(`"jwks_uri":%q`, "http://127.0.0.1:1/jwks.json"),
		1,
	)
	fake.setDiscoveryBody(document)

	_, err = validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: token},
		TokenExpectation{Nonce: testNonce, Audience: testAudience},
	)
	requireValidatorClass(t, err, ValidatorClassUnavailable)
}

// TestOIDCClient_ThrottlesTheRefreshWithTheDefaultInterval proves that the
// refresh of the key set is RATE LIMITED with the default interval.
//
// The shared fixture disables the limit, so the limit is exercised here
// with the interval the connector actually ships with.
func TestOIDCClient_ThrottlesTheRefreshWithTheDefaultInterval(t *testing.T) {
	fake := newFakeOIDCServer(t)
	clock := &fakeClock{now: time.Now()}
	client, err := NewOIDCClient(OIDCClientConfig{
		DiscoveryURL: fake.server.URL + "/.well-known/openid-configuration",
		HTTPClient:   fake.server.Client(),
		Now:          clock.nowFunc,
	})
	if err != nil {
		t.Fatalf("NewOIDCClient: %v", err)
	}
	fake.setIssuer(testIssuer)

	// The first validation reads the key set.
	if _, _, err := client.VerificationAuthority(context.Background(), fake.keyID); err != nil {
		t.Fatalf("VerificationAuthority: %v", err)
	}
	if requests := fake.jwksFetches(); requests != 1 {
		t.Fatalf("the key set was fetched %d times, want 1", requests)
	}

	// An unknown key id refreshes the set, and the refresh is admitted
	// because the previous attempt is older than the interval.
	clock.advance(defaultRenewInterval + time.Second)
	if _, _, err := client.VerificationAuthority(context.Background(), "unknown-key"); err == nil {
		t.Fatal("the validation of an unknown key id succeeded")
	}
	if requests := fake.jwksFetches(); requests != 2 {
		t.Fatalf("the key set was fetched %d times, want 2", requests)
	}

	// A second unknown key id inside the window is THROTTLED: the set is
	// not fetched again.
	clock.advance(time.Second)
	if _, _, err := client.VerificationAuthority(context.Background(), "unknown-key"); err == nil {
		t.Fatal("the validation of an unknown key id succeeded")
	}
	if requests := fake.jwksFetches(); requests != 2 {
		t.Errorf("the key set was fetched %d times, want 2 (the throttled refresh fetched nothing)", requests)
	}
}

// TestOIDCClient_ThrottlesAFailedRefresh proves that a FAILED refresh
// closes the request window.
//
// The attempt is recorded whether it succeeded or not, so a provider that
// fails does not attract one request per validation.
func TestOIDCClient_ThrottlesAFailedRefresh(t *testing.T) {
	fake := newFakeOIDCServer(t)
	clock := &fakeClock{now: time.Now()}
	client, err := NewOIDCClient(OIDCClientConfig{
		DiscoveryURL: fake.server.URL + "/.well-known/openid-configuration",
		HTTPClient:   fake.server.Client(),
		Now:          clock.nowFunc,
	})
	if err != nil {
		t.Fatalf("NewOIDCClient: %v", err)
	}
	fake.setIssuer(testIssuer)

	if _, _, err := client.VerificationAuthority(context.Background(), fake.keyID); err != nil {
		t.Fatalf("VerificationAuthority: %v", err)
	}
	if requests := fake.jwksFetches(); requests != 1 {
		t.Fatalf("the key set was fetched %d times, want 1", requests)
	}

	// The provider now fails, and the clock is past the renew interval:
	// the refresh is admitted and it fails.
	fake.setKeySetStatus(http.StatusInternalServerError)
	clock.advance(defaultRenewInterval + time.Second)
	_, _, err = client.VerificationAuthority(context.Background(), "unknown-key")
	if err == nil {
		t.Fatal("the validation of an unknown key id succeeded")
	}
	if requests := fake.jwksFetches(); requests != 2 {
		t.Fatalf("the key set was fetched %d times, want 2", requests)
	}

	// The second validation is THROTTLED: the failed attempt closed the
	// window, and the failure it recorded is reported.
	clock.advance(time.Second)
	_, _, err = client.VerificationAuthority(context.Background(), "unknown-key")
	if err == nil {
		t.Fatal("the validation of an unknown key id succeeded")
	}
	if requests := fake.jwksFetches(); requests != 2 {
		t.Errorf("the key set was fetched %d times, want 2 (the failed attempt closed the window)", requests)
	}
	if !errors.Is(err, errOIDCUnavailable) {
		t.Errorf("the throttled failure = %v, want the recorded provider failure", err)
	}
}

// TestOIDCClient_ThrottlesAFailedColdFetch proves that the admission
// applies to a COLD cache as well.
//
// The limiter used to live inside the branch that only a warm cache
// reached, so a cold cache fetched again on every validation.
func TestOIDCClient_ThrottlesAFailedColdFetch(t *testing.T) {
	fake := newFakeOIDCServer(t)
	clock := &fakeClock{now: time.Now()}
	client, err := NewOIDCClient(OIDCClientConfig{
		DiscoveryURL: fake.server.URL + "/.well-known/openid-configuration",
		HTTPClient:   fake.server.Client(),
		Now:          clock.nowFunc,
	})
	if err != nil {
		t.Fatalf("NewOIDCClient: %v", err)
	}
	fake.setIssuer(testIssuer)
	fake.setKeySetStatus(http.StatusInternalServerError)

	if _, _, err := client.VerificationAuthority(context.Background(), fake.keyID); err == nil {
		t.Fatal("the validation succeeded against a failing provider")
	}
	if requests := fake.jwksFetches(); requests != 1 {
		t.Fatalf("the key set was fetched %d times, want 1", requests)
	}

	clock.advance(time.Second)
	if _, _, err := client.VerificationAuthority(context.Background(), fake.keyID); err == nil {
		t.Fatal("the validation succeeded against a failing provider")
	}
	if requests := fake.jwksFetches(); requests != 1 {
		t.Errorf("the key set was fetched %d times, want 1 (the failed attempt closed the window)", requests)
	}
}

// TestOIDCValidator_RefusesAKeySetWithMoreKeysThanTheLimit proves that the
// NUMBER of keys in a key set is bounded.
//
// The document below holds more keys than the limit allows and stays well
// BELOW the byte bound, so the refusal is the key-count limit and not the
// size bound.
func TestOIDCValidator_RefusesAKeySetWithMoreKeysThanTheLimit(t *testing.T) {
	fake := newFakeOIDCServer(t)
	validator := newTestValidator(t, fake, testIssuer)
	modulus := base64.RawURLEncoding.EncodeToString(fake.key.N.Bytes())
	entries := make([]string, 0, maxJWKSKeys+1)
	for index := 0; index <= maxJWKSKeys; index++ {
		entries = append(entries, fmt.Sprintf(
			`{"kty":"RSA","kid":"key-%d","use":"sig","alg":"RS256","n":%q,"e":"AQAB"}`,
			index,
			modulus,
		))
	}
	document := `{"keys":[` + strings.Join(entries, ",") + `]}`
	if len(document) >= maxJWKSBytes {
		t.Fatalf("the fixture is %d bytes, want fewer than the %d byte bound", len(document), maxJWKSBytes)
	}
	if len(entries) <= maxJWKSKeys {
		t.Fatalf("the fixture holds %d keys, want more than the %d key limit", len(entries), maxJWKSKeys)
	}
	fake.setKeySetBody(document)
	token := signRS256(t, fake.key, validHeader("key-0"), validClaims(time.Now()))

	_, err := validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: token},
		TokenExpectation{Nonce: testNonce, Audience: testAudience},
	)
	requireValidatorClass(t, err, ValidatorClassUnavailable)
}

// TestOIDCClient_DetectsAnOversizedDocument proves that an oversized
// document is DETECTED and not truncated.
//
// The document below is a complete, valid JSON document of exactly the
// bound, followed by one byte more. A reader that truncated at the bound
// would see a valid document and report no failure at all, and a reader
// that reported a parse failure would blame the document's shape.
func TestOIDCClient_DetectsAnOversizedDocument(t *testing.T) {
	fake := newFakeOIDCServer(t)
	client, err := NewOIDCClient(OIDCClientConfig{
		DiscoveryURL: fake.server.URL + "/.well-known/openid-configuration",
		HTTPClient:   fake.server.Client(),
	})
	if err != nil {
		t.Fatalf("NewOIDCClient: %v", err)
	}
	valid := fake.keySetDocument()
	padded := valid + strings.Repeat(" ", maxJWKSBytes-len(valid))
	if len(padded) != maxJWKSBytes {
		t.Fatalf("the padded document is %d bytes, want %d", len(padded), maxJWKSBytes)
	}
	fake.setKeySetBody(padded + " ")

	_, err = client.fetchDocument(context.Background(), fake.server.URL+"/.well-known/jwks.json", maxJWKSBytes)
	if !errors.Is(err, errDocumentTooLarge) {
		t.Fatalf("the failure = %v, want the oversized-document failure", err)
	}
}

// TestOIDCClient_DetectsAnOversizedDiscoveryDocument proves the same for
// the OIDC configuration.
func TestOIDCClient_DetectsAnOversizedDiscoveryDocument(t *testing.T) {
	fake := newFakeOIDCServer(t)
	client, err := NewOIDCClient(OIDCClientConfig{
		DiscoveryURL: fake.server.URL + "/.well-known/openid-configuration",
		HTTPClient:   fake.server.Client(),
	})
	if err != nil {
		t.Fatalf("NewOIDCClient: %v", err)
	}
	valid := fake.discoveryDocument()
	padded := valid + strings.Repeat(" ", maxDiscoveryBytes-len(valid))
	if len(padded) != maxDiscoveryBytes {
		t.Fatalf("the padded document is %d bytes, want %d", len(padded), maxDiscoveryBytes)
	}
	fake.setDiscoveryBody(padded + " ")

	_, err = client.fetchDocument(context.Background(), fake.server.URL+"/.well-known/openid-configuration", maxDiscoveryBytes)
	if !errors.Is(err, errDocumentTooLarge) {
		t.Fatalf("the failure = %v, want the oversized-document failure", err)
	}
}

// TestOIDCClient_RefusesAPlaintextDiscoveryURL proves that the source of
// the key set must be AUTHENTICATED.
//
// A plaintext source can be rewritten by anyone on the path, and a
// rewritten discovery document publishes a rewritten key set.
func TestOIDCClient_RefusesAPlaintextDiscoveryURL(t *testing.T) {
	if _, err := NewOIDCClient(OIDCClientConfig{DiscoveryURL: "http://auth.example.com/.well-known/openid-configuration"}); err == nil {
		t.Fatal("the client accepted a plaintext discovery URL")
	}
}

// TestOIDCClient_RefusesAPlaintextKeySetURI proves that the key set the
// configuration publishes must be authenticated too.
//
// A configuration that points at a plaintext key set downgrades the
// authenticity of every key it serves.
func TestOIDCClient_RefusesAPlaintextKeySetURI(t *testing.T) {
	document := `{"issuer":"https://auth.openai.com","jwks_uri":"http://auth.example.com/jwks.json"}`
	if _, err := parseDiscovery([]byte(document)); !errors.Is(err, errInsecureDocumentURL) {
		t.Fatalf("the failure = %v, want the insecure-document failure", err)
	}
}

// TestOIDCClient_AcceptsALoopbackDocumentURL proves that the local test
// server is still reachable.
//
// The exception is narrow: it is the loopback address, spelled as an IP
// literal, and it never leaves the host.
func TestOIDCClient_AcceptsALoopbackDocumentURL(t *testing.T) {
	if _, err := NewOIDCClient(OIDCClientConfig{DiscoveryURL: "http://127.0.0.1:8080/.well-known/openid-configuration"}); err != nil {
		t.Fatalf("the client refused a loopback discovery URL: %v", err)
	}
	if _, err := NewOIDCClient(OIDCClientConfig{DiscoveryURL: "http://localhost:8080/.well-known/openid-configuration"}); err == nil {
		t.Fatal("the client accepted a plaintext discovery URL that is spelled as a name")
	}
}

// TestCheckDirectPlanUsage_DescribesTheOutcomeTruthfully proves that the
// scope outcome is not described as a rejected ID token.
//
// The sign-in is retained and the plan usage is disabled, and a gate that
// classifies the outcome must be able to tell the two apart.
func TestCheckDirectPlanUsage_DescribesTheOutcomeTruthfully(t *testing.T) {
	err := CheckDirectPlanUsage([]string{"openid", "profile", "email"})
	if err == nil {
		t.Fatal("the scope check accepted a scope set without the direct plan-use scope")
	}
	rendered := err.Error()
	if strings.Contains(rendered, "ID token was rejected") {
		t.Errorf("the scope outcome is rendered as a rejected ID token: %q", rendered)
	}
	if errors.Is(err, ErrIDTokenRejected) {
		t.Error("the scope outcome matches the ID-token-rejection sentinel")
	}
	if !errors.Is(err, ErrPlanUsageNotGranted) {
		t.Error("the scope outcome does not carry the plan-usage cause")
	}
	requireValidatorClass(t, err, ValidatorClassScope)
}

// TestValidatorError_ClassifiesTheScopeOutcomeApart proves that the
// classes keep their own meaning.
//
// A token rejection and a scope outcome are DIFFERENT outcomes, and the
// sentinel that names a rejected token must not name a retained sign-in.
func TestValidatorError_ClassifiesTheScopeOutcomeApart(t *testing.T) {
	rejected := NewValidatorError(ValidatorClassSignature, errSignatureInvalid)
	if !errors.Is(rejected, ErrIDTokenRejected) {
		t.Error("a rejected token does not match the ID-token-rejection sentinel")
	}
	scope := NewValidatorError(ValidatorClassScope, ErrPlanUsageNotGranted)
	if errors.Is(scope, ErrIDTokenRejected) {
		t.Error("the scope outcome matches the ID-token-rejection sentinel")
	}
}

// fakeClock is the clock of the tests that need one.
//
// The clock is INJECTED, because the properties under test - the age of
// the cached configuration, the age of the cached key set and the window
// the rate limit opens - are properties of TIME.
type fakeClock struct {
	now time.Time
}

// nowFunc returns the clock's reading.
func (c *fakeClock) nowFunc() time.Time { return c.now }

// advance moves the clock forward.
func (c *fakeClock) advance(duration time.Duration) { c.now = c.now.Add(duration) }
