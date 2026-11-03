package siwc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The ID-token validation.
//
// The fixtures are the tokens the validation must accept and the tokens it
// must refuse. Every fixture signs with the provider's key, so that the
// condition under test is the ONLY thing that differs from a valid token:
// a fixture that failed because its signature did not verify would not
// prove that the condition it names is checked.

// testIssuer is the issuer the fake provider publishes.
const testIssuer = "https://auth.openai.com"

// testAudience is the issued client id the fixtures are addressed to.
const testAudience = "oaiapp_issued_1"

// testNonce is the attempt's nonce the fixtures carry.
const testNonce = "attempt-nonce-1"

// testSubject is the account the fixtures name.
const testSubject = "user-abc123"

// validClaims are the claims of a valid token.
func validClaims(now time.Time) map[string]any {
	return map[string]any{
		"iss":   testIssuer,
		"aud":   testAudience,
		"sub":   testSubject,
		"exp":   now.Add(time.Hour).Unix(),
		"iat":   now.Add(-time.Minute).Unix(),
		"nonce": testNonce,
		"email": "user@example.com",
	}
}

// validHeader is the header of a valid token.
func validHeader(keyID string) map[string]any {
	return map[string]any{"alg": "RS256", "kid": keyID, "typ": "JWT"}
}

// newTestValidator returns a validator against the fake provider.
//
// The fake publishes the given issuer, and the fixtures name it.
func newTestValidator(t *testing.T, fake *fakeOIDCServer, issuer string) *OIDCValidator {
	t.Helper()
	fake.setIssuer(issuer)
	validator, err := NewOIDCValidator(OIDCValidatorConfig{
		OIDCClientConfig: OIDCClientConfig{
			DiscoveryURL:  fake.server.URL + "/.well-known/openid-configuration",
			HTTPClient:    fake.server.Client(),
			RenewInterval: -1,
		},
	})
	if err != nil {
		t.Fatalf("NewOIDCValidator: %v", err)
	}
	return validator
}

// TestOIDCValidator_AcceptsAValidIDToken proves that a token that satisfies
// every constraint yields the validated identity.
func TestOIDCValidator_AcceptsAValidIDToken(t *testing.T) {
	fake := newFakeOIDCServer(t)
	validator := newTestValidator(t, fake, testIssuer)
	now := time.Now()
	token := signRS256(t, fake.key, validHeader(fake.keyID), validClaims(now))

	identity, err := validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: token, Scope: "openid email " + PlanUseScope},
		TokenExpectation{Nonce: testNonce, Audience: testAudience},
	)
	if err != nil {
		t.Fatalf("ValidateIDToken: %v", err)
	}
	if identity.Subject != testSubject {
		t.Errorf("the subject = %q, want %q", identity.Subject, testSubject)
	}
	if identity.EmailHint != "user@example.com" {
		t.Errorf("the email hint = %q, want the reported address", identity.EmailHint)
	}
}

// TestOIDCValidator_RejectsTheTokensItMustReject proves that every
// constraint is checked, and that the failure carries the class that names
// it.
func TestOIDCValidator_RejectsTheTokensItMustReject(t *testing.T) {
	now := time.Now()
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate the other key: %v", err)
	}
	cases := []struct {
		name      string
		wantClass string
		build     func(t *testing.T, fake *fakeOIDCServer) string
	}{
		{
			name:      "a signature that does not verify",
			wantClass: ValidatorClassSignature,
			build: func(t *testing.T, fake *fakeOIDCServer) string {
				t.Helper()
				return signRS256(t, otherKey, validHeader(fake.keyID), validClaims(now))
			},
		},
		{
			name:      "another issuer",
			wantClass: ValidatorClassIssuer,
			build: func(t *testing.T, fake *fakeOIDCServer) string {
				t.Helper()
				claims := validClaims(now)
				claims["iss"] = "https://auth.example.com"
				return signRS256(t, fake.key, validHeader(fake.keyID), claims)
			},
		},
		{
			name:      "another audience",
			wantClass: ValidatorClassAudience,
			build: func(t *testing.T, fake *fakeOIDCServer) string {
				t.Helper()
				claims := validClaims(now)
				claims["aud"] = "oaiapp_another_registration"
				return signRS256(t, fake.key, validHeader(fake.keyID), claims)
			},
		},
		{
			name:      "an expired token",
			wantClass: ValidatorClassExpiry,
			build: func(t *testing.T, fake *fakeOIDCServer) string {
				t.Helper()
				claims := validClaims(now)
				claims["exp"] = now.Add(-time.Hour).Unix()
				return signRS256(t, fake.key, validHeader(fake.keyID), claims)
			},
		},
		{
			name:      "another nonce",
			wantClass: ValidatorClassNonce,
			build: func(t *testing.T, fake *fakeOIDCServer) string {
				t.Helper()
				claims := validClaims(now)
				claims["nonce"] = "another-attempt-nonce"
				return signRS256(t, fake.key, validHeader(fake.keyID), claims)
			},
		},
		{
			name:      "no subject",
			wantClass: ValidatorClassSubject,
			build: func(t *testing.T, fake *fakeOIDCServer) string {
				t.Helper()
				claims := validClaims(now)
				claims["sub"] = "   "
				return signRS256(t, fake.key, validHeader(fake.keyID), claims)
			},
		},
		{
			name:      "an unknown key id",
			wantClass: ValidatorClassSignature,
			build: func(t *testing.T, fake *fakeOIDCServer) string {
				t.Helper()
				return signRS256(t, fake.key, validHeader("key-the-provider-does-not-publish"), validClaims(now))
			},
		},
		{
			name:      "an algorithm the connector does not accept",
			wantClass: ValidatorClassSignature,
			build: func(t *testing.T, fake *fakeOIDCServer) string {
				t.Helper()
				header := validHeader(fake.keyID)
				header["alg"] = "HS256"
				// The secret is the provider's PUBLIC key material: the
				// algorithm-confusion attack, and the reason the
				// algorithm is checked before anything is attempted.
				secret := []byte(publicKeyBytes(t, fake.key))
				return signHS256(t, secret, header, validClaims(now))
			},
		},
		{
			name:      "the unsigned algorithm",
			wantClass: ValidatorClassSignature,
			build: func(t *testing.T, fake *fakeOIDCServer) string {
				t.Helper()
				header := validHeader(fake.keyID)
				header["alg"] = "none"
				signingInput := encodeJWTSegment(t, header) + "." + encodeJWTSegment(t, validClaims(now))
				return signingInput + "."
			},
		},
		{
			name:      "a token that is not a JWT",
			wantClass: ValidatorClassRejected,
			build: func(t *testing.T, fake *fakeOIDCServer) string {
				t.Helper()
				return "not-a-jwt"
			},
		},
		{
			name:      "a token whose payload repeats a claim",
			wantClass: ValidatorClassRejected,
			build: func(t *testing.T, fake *fakeOIDCServer) string {
				t.Helper()
				header := encodeJWTSegment(t, validHeader(fake.keyID))
				payload := `{"iss":"` + testIssuer + `","aud":"` + testAudience + `","aud":"` +
					`oaiapp_another_registration","sub":"` + testSubject + `","exp":` +
					strconv.FormatInt(now.Add(time.Hour).Unix(), 10) + `,"nonce":"` + testNonce + `"}`
				return header + "." + base64RawURL(t, payload) + ".c2ln"
			},
		},
		{
			name:      "a token without an expiry",
			wantClass: ValidatorClassExpiry,
			build: func(t *testing.T, fake *fakeOIDCServer) string {
				t.Helper()
				claims := validClaims(now)
				delete(claims, "exp")
				return signRS256(t, fake.key, validHeader(fake.keyID), claims)
			},
		},
		{
			name:      "a token without a key id",
			wantClass: ValidatorClassSignature,
			build: func(t *testing.T, fake *fakeOIDCServer) string {
				t.Helper()
				header := validHeader(fake.keyID)
				delete(header, "kid")
				return signRS256(t, fake.key, header, validClaims(now))
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fake := newFakeOIDCServer(t)
			validator := newTestValidator(t, fake, testIssuer)
			token := testCase.build(t, fake)

			identity, err := validator.ValidateIDToken(
				context.Background(),
				TokenResponse{IDToken: token},
				TokenExpectation{Nonce: testNonce, Audience: testAudience},
			)
			requireValidatorClass(t, err, testCase.wantClass)
			if identity.Subject != "" {
				t.Errorf("the refused token produced the subject %q", identity.Subject)
			}
			requireNoTokenMaterial(t, err,
				tokenMaterial{name: "ID token", value: token},
				tokenMaterial{name: "attempt nonce", value: testNonce},
				tokenMaterial{name: "account subject", value: testSubject},
			)
		})
	}
}

// TestOIDCValidator_RefusesAnUnacceptedAlgorithmWithoutTheKeySet proves
// that an algorithm the connector does not accept is refused BEFORE the key
// set is consulted.
//
// The property is observable: the fake provider counts its requests, and a
// token that names an unaccepted algorithm must not make the connector
// fetch the key set at all.
func TestOIDCValidator_RefusesAnUnacceptedAlgorithmWithoutTheKeySet(t *testing.T) {
	fake := newFakeOIDCServer(t)
	validator := newTestValidator(t, fake, testIssuer)
	header := validHeader(fake.keyID)
	header["alg"] = "HS256"
	token := signHS256(t, []byte("secret"), header, validClaims(time.Now()))

	_, err := validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: token},
		TokenExpectation{Nonce: testNonce, Audience: testAudience},
	)
	requireValidatorClass(t, err, ValidatorClassSignature)
	if requests := fake.jwksFetches(); requests != 0 {
		t.Errorf("the connector fetched the key set %d times for a refused algorithm", requests)
	}
}

// TestOIDCValidator_RequiresTheExpectedAudience proves that the audience
// the token is checked against is the one the FLOW supplied.
//
// The expectation is the flow's knowledge: a token that names another
// audience is refused, and an expectation without an audience is refused
// as well - a check with nothing to compare with is a check that passes.
func TestOIDCValidator_RequiresTheExpectedAudience(t *testing.T) {
	fake := newFakeOIDCServer(t)
	validator := newTestValidator(t, fake, testIssuer)
	token := signRS256(t, fake.key, validHeader(fake.keyID), validClaims(time.Now()))

	_, err := validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: token},
		TokenExpectation{Nonce: testNonce},
	)
	requireValidatorClass(t, err, ValidatorClassAudience)

	_, err = validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: token},
		TokenExpectation{Audience: testAudience},
	)
	requireValidatorClass(t, err, ValidatorClassNonce)
}

// TestOIDCValidator_AcceptsAnAudienceArray proves that the audience claim
// is read in both of its documented shapes.
func TestOIDCValidator_AcceptsAnAudienceArray(t *testing.T) {
	fake := newFakeOIDCServer(t)
	validator := newTestValidator(t, fake, testIssuer)
	claims := validClaims(time.Now())
	claims["aud"] = []string{"another-audience", testAudience}
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

// TestOIDCValidator_ReadsTheIssuerFromTheProvidersConfiguration proves that
// the issuer is the authority's value and not a constant of the
// connector.
func TestOIDCValidator_ReadsTheIssuerFromTheProvidersConfiguration(t *testing.T) {
	fake := newFakeOIDCServer(t)
	validator := newTestValidator(t, fake, fake.server.URL)
	claims := validClaims(time.Now())
	claims["iss"] = fake.server.URL
	token := signRS256(t, fake.key, validHeader(fake.keyID), claims)

	// The configuration publishes the fake's own address, and the token
	// names it: the token is accepted.
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

// TestOIDCValidator_ReportsAnUnusableKeySet proves that a key set the
// connector cannot use leaves the validation impossible, and that the
// outcome is classified as such.
func TestOIDCValidator_ReportsAnUnusableKeySet(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, fake *fakeOIDCServer)
	}{
		{
			name: "a malformed key set",
			setup: func(t *testing.T, fake *fakeOIDCServer) {
				t.Helper()
				fake.setKeySetBody("this is not a key set")
			},
		},
		{
			name: "an oversized key set",
			setup: func(t *testing.T, fake *fakeOIDCServer) {
				t.Helper()
				fake.setKeySetBody(`{"keys":[` + strings.Repeat(`{"kty":"RSA"},`, 40000) + `{"kty":"RSA"}]}`)
			},
		},
		{
			name: "a key set that names one key id twice",
			setup: func(t *testing.T, fake *fakeOIDCServer) {
				t.Helper()
				entry := `{"kty":"RSA","kid":"` + fake.keyID + `","use":"sig","alg":"RS256","n":"` +
					base64RawURL(t, string(modulusBytes(fake.key))) + `","e":"AQAB"}`
				fake.setKeySetBody(`{"keys":[` + entry + `,` + entry + `]}`)
			},
		},
		{
			name: "a key set that names one key id twice with another algorithm",
			setup: func(t *testing.T, fake *fakeOIDCServer) {
				t.Helper()
				// The second entry is one this connector would SKIP, and
				// the ambiguity it creates is just as real: the document
				// names one key id twice.
				entry := `{"kty":"RSA","kid":"` + fake.keyID + `","use":"sig","alg":"RS256","n":"` +
					base64RawURL(t, string(modulusBytes(fake.key))) + `","e":"AQAB"}`
				other := `{"kty":"RSA","kid":"` + fake.keyID + `","use":"sig","alg":"RS512","n":"` +
					base64RawURL(t, string(modulusBytes(fake.key))) + `","e":"AQAB"}`
				fake.setKeySetBody(`{"keys":[` + entry + `,` + other + `]}`)
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fake := newFakeOIDCServer(t)
			validator := newTestValidator(t, fake, testIssuer)
			testCase.setup(t, fake)
			token := signRS256(t, fake.key, validHeader(fake.keyID), validClaims(time.Now()))

			_, err := validator.ValidateIDToken(
				context.Background(),
				TokenResponse{IDToken: token},
				TokenExpectation{Nonce: testNonce, Audience: testAudience},
			)
			requireValidatorClass(t, err, ValidatorClassUnavailable)
			requireNoTokenMaterial(t, err, tokenMaterial{name: "ID token", value: token})
		})
	}
}

// TestOIDCValidator_ReportsAnUnreachableKeySet proves that a provider that
// cannot be reached leaves the validation impossible.
func TestOIDCValidator_ReportsAnUnreachableKeySet(t *testing.T) {
	fake := newFakeOIDCServer(t)
	validator := newTestValidator(t, fake, testIssuer)
	fake.setKeySetStatus(500)
	token := signRS256(t, fake.key, validHeader(fake.keyID), validClaims(time.Now()))

	_, err := validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: token},
		TokenExpectation{Nonce: testNonce, Audience: testAudience},
	)
	requireValidatorClass(t, err, ValidatorClassUnavailable)
}

// TestOIDCValidator_CachesTheKeySet proves that the key set is fetched once
// for many validations, and that a token naming an unknown key id forces a
// refresh.
func TestOIDCValidator_CachesTheKeySet(t *testing.T) {
	fake := newFakeOIDCServer(t)
	validator := newTestValidator(t, fake, testIssuer)
	token := signRS256(t, fake.key, validHeader(fake.keyID), validClaims(time.Now()))

	for range 3 {
		if _, err := validator.ValidateIDToken(
			context.Background(),
			TokenResponse{IDToken: token},
			TokenExpectation{Nonce: testNonce, Audience: testAudience},
		); err != nil {
			t.Fatalf("ValidateIDToken: %v", err)
		}
	}
	if requests := fake.jwksFetches(); requests != 1 {
		t.Errorf("the key set was fetched %d times, want 1", requests)
	}
	if requests := fake.discoveryFetches(); requests != 1 {
		t.Errorf("the OIDC configuration was fetched %d times, want 1", requests)
	}
}

// TestOIDCValidator_RefreshesTheKeySetForAnUnknownKeyID proves that a
// token naming a key id the cached set does not hold makes the connector
// fetch the key set again.
func TestOIDCValidator_RefreshesTheKeySetForAnUnknownKeyID(t *testing.T) {
	fake := newFakeOIDCServer(t)
	validator := newTestValidator(t, fake, testIssuer)
	token := signRS256(t, fake.key, validHeader(fake.keyID), validClaims(time.Now()))
	if _, err := validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: token},
		TokenExpectation{Nonce: testNonce, Audience: testAudience},
	); err != nil {
		t.Fatalf("ValidateIDToken: %v", err)
	}
	unknown := signRS256(t, fake.key, validHeader("unknown-key"), validClaims(time.Now()))
	if _, err := validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: unknown},
		TokenExpectation{Nonce: testNonce, Audience: testAudience},
	); err == nil {
		t.Fatal("the validation of a token with an unknown key id succeeded")
	}
	if requests := fake.jwksFetches(); requests != 2 {
		t.Errorf("the key set was fetched %d times, want 2 (the cached set and the refresh)", requests)
	}
}

// TestOIDCValidator_NeverRendersTheTokenMaterial proves that the failure of
// a refused token carries no credential material.
//
// The material is checked in the error, in every formatting verb, and in
// the unwrapped cause.
func TestOIDCValidator_NeverRendersTheTokenMaterial(t *testing.T) {
	fake := newFakeOIDCServer(t)
	validator := newTestValidator(t, fake, testIssuer)

	// The access token carries the OpenAI encrypted authentication
	// metadata, and the retained ID-token hint is credential material as
	// well. Neither may reach a diagnostic.
	encryptedMetadata := "encrypted-auth-metadata-value"
	accessToken := signRS256(t, fake.key, validHeader(fake.keyID), map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"encrypted_auth_metadata": encryptedMetadata,
			"per_user_salt":           "per-user-salt-value",
		},
	})
	token := signRS256(t, fake.key, validHeader("unknown-key"), validClaims(time.Now()))

	_, err := validator.ValidateIDToken(
		context.Background(),
		TokenResponse{
			IDToken:      token,
			AccessToken:  accessToken,
			RefreshToken: "refresh-token-value",
		},
		TokenExpectation{Nonce: testNonce, Audience: testAudience},
	)
	if err == nil {
		t.Fatal("the validation of a token with an unknown key id succeeded")
	}
	requireNoTokenMaterial(t, err,
		tokenMaterial{name: "ID token", value: token},
		tokenMaterial{name: "access token", value: accessToken},
		tokenMaterial{name: "encrypted authentication metadata", value: encryptedMetadata},
		tokenMaterial{name: "refresh token", value: "refresh-token-value"},
		tokenMaterial{name: "attempt nonce", value: testNonce},
	)
}

// TestOIDCValidator_ReportsTheProvidersConfiguration proves that the
// discovery document is exposed with the endpoints the flow needs.
//
// The revocation endpoint belongs to the sign-out, and the configuration
// is where the sign-out finds it.
func TestOIDCValidator_ReportsTheProvidersConfiguration(t *testing.T) {
	fake := newFakeOIDCServer(t)
	validator := newTestValidator(t, fake, testIssuer)

	discovery, err := validator.Discovery(context.Background())
	if err != nil {
		t.Fatalf("Discovery: %v", err)
	}
	if discovery.Issuer != testIssuer {
		t.Errorf("the issuer = %q, want %q", discovery.Issuer, testIssuer)
	}
	if !strings.HasSuffix(discovery.JWKSURI, "/.well-known/jwks.json") {
		t.Errorf("the key-set URI = %q, want the published one", discovery.JWKSURI)
	}
	if discovery.RevocationEndpoint == "" {
		t.Error("the configuration carries no revocation endpoint")
	}
	if discovery.TokenEndpoint == "" {
		t.Error("the configuration carries no token endpoint")
	}
}

// TestOIDCValidator_AcceptsATokenWithoutAnEmail proves that the email is a
// hint: a token that carries no email still yields a validated identity.
func TestOIDCValidator_AcceptsATokenWithoutAnEmail(t *testing.T) {
	fake := newFakeOIDCServer(t)
	validator := newTestValidator(t, fake, testIssuer)
	claims := validClaims(time.Now())
	delete(claims, "email")
	token := signRS256(t, fake.key, validHeader(fake.keyID), claims)

	identity, err := validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: token},
		TokenExpectation{Nonce: testNonce, Audience: testAudience},
	)
	if err != nil {
		t.Fatalf("ValidateIDToken: %v", err)
	}
	if identity.EmailHint != "" {
		t.Errorf("the email hint = %q, want none", identity.EmailHint)
	}
}

// FuzzOIDCValidator_RefusesAnything proves that the validation never
// panics and never renders the token it refused.
//
// The property is the SHAPE of the failure: it is a classified rejection
// whose rendering names the class and nothing else. A validator that put
// the token into its own error would break the property.
func FuzzOIDCValidator_RefusesAnything(f *testing.F) {
	f.Add("a.b.c")
	f.Add("")
	f.Add("eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.c2ln")
	f.Add("eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0.")
	f.Add("...")
	f.Add("..")
	f.Add("a.b")
	f.Add("a.b.c.d")
	// The token below is a SUBSTRING of the constant rejection rendering.
	// It is the regression seed of the test-only collision: a containment
	// oracle reports it as a leak, while the constant itself is what the
	// failure must equal.
	f.Add("the ID token was rejected")
	fuzzed := func(t *testing.T, token string) {
		t.Helper()
		fake := newFakeOIDCServer(t)
		validator := newTestValidator(t, fake, testIssuer)
		identity, err := validator.ValidateIDToken(
			context.Background(),
			TokenResponse{IDToken: token},
			TokenExpectation{Nonce: testNonce, Audience: testAudience},
		)
		if err == nil {
			// A token that verifies is a token the fuzzer cannot forge, so
			// an accepted token is a failure of the fixture, not of the
			// validator.
			if identity.Subject == "" {
				t.Fatal("the validation succeeded without an identity")
			}
			return
		}
		var classified *ValidatorError
		if !errors.As(err, &classified) {
			t.Fatalf("the failure is not a classified rejection: %v", err)
		}
		want := validatorRendering(classified.Class)
		if got := err.Error(); got != want {
			t.Fatalf("the rendering = %q, want %q", got, want)
		}
		// The rendering is a CONSTANT and value-free, so the exact
		// equality IS the leak assertion. A containment check on the
		// untrusted token adds nothing and is unsound: a token that is a
		// substring of the rendering would be reported as a leak the
		// rendering does not carry.
	}
	f.Fuzz(fuzzed)
}

// publicKeyBytes returns the DER encoding of the key's public material.
//
// The algorithm-confusion fixture signs with it, so that the token looks
// like it was signed by the provider.
func publicKeyBytes(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	encoded, err := json.Marshal(key.Public())
	if err != nil {
		t.Fatalf("encode the public key: %v", err)
	}
	return encoded
}

// modulusBytes returns the modulus of the key.
func modulusBytes(key *rsa.PrivateKey) []byte {
	return key.N.Bytes()
}
