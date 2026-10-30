package siwc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"testing"
	"time"
)

// The third review's shape: a value that ARRIVED is sampled into ABSENCE,
// and absence is PERMISSIVE.
//
// The tests here are the ones that pin the shape down. Every one of them
// fails against an implementation that reads an unusable arrived value as
// though it had not arrived.

// TestOIDCValidator_PublishesARefreshedKeySet proves that a successful
// refresh of the key set is PUBLISHED to the cache.
//
// The provider rotates its keys: the new set publishes a new key id and
// withdraws the old one. The first validation of a token signed with the
// new key refreshes the set, and the SECOND validation of the same token
// must succeed WITHOUT another request - the refreshed set is the
// authority's current set, and the cache holds it.
//
// A token signed with the WITHDRAWN key must be refused: the cache holds
// the new set, and the new set does not publish the old key id.
func TestOIDCValidator_PublishesARefreshedKeySet(t *testing.T) {
	fake := newFakeOIDCServer(t)
	clock := &fakeClock{now: time.Now()}
	validator, err := NewOIDCValidator(OIDCValidatorConfig{
		OIDCClientConfig: OIDCClientConfig{
			DiscoveryURL: fake.server.URL + "/.well-known/openid-configuration",
			HTTPClient:   fake.server.Client(),
			Now:          clock.nowFunc,
		},
	})
	if err != nil {
		t.Fatalf("NewOIDCValidator: %v", err)
	}
	fake.setIssuer(testIssuer)

	withdrawnToken := signRS256(t, fake.key, validHeader(fake.keyID), validClaims(time.Now()))
	if _, err := validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: withdrawnToken},
		TokenExpectation{Nonce: testNonce, Audience: testAudience},
	); err != nil {
		t.Fatalf("the first validation: %v", err)
	}
	if requests := fake.jwksFetches(); requests != 1 {
		t.Fatalf("the key set was fetched %d times, want 1", requests)
	}

	// The provider rotates its keys.
	rotated, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate the rotated key: %v", err)
	}
	fake.setKeySet(rotated, "rotated-key-1")
	rotatedToken := signRS256(t, rotated, validHeader("rotated-key-1"), validClaims(time.Now()))

	// The clock is past the renew interval, so the refresh is admitted.
	clock.advance(defaultRenewInterval + time.Second)
	if _, err := validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: rotatedToken},
		TokenExpectation{Nonce: testNonce, Audience: testAudience},
	); err != nil {
		t.Fatalf("the first validation of the rotated token: %v", err)
	}
	if requests := fake.jwksFetches(); requests != 2 {
		t.Fatalf("the key set was fetched %d times, want 2", requests)
	}

	// The SECOND validation of the same token must work without another
	// request: the refreshed set is in the cache.
	if _, err := validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: rotatedToken},
		TokenExpectation{Nonce: testNonce, Audience: testAudience},
	); err != nil {
		t.Fatalf("the second validation of the rotated token: %v", err)
	}
	if requests := fake.jwksFetches(); requests != 2 {
		t.Errorf("the key set was fetched %d times, want 2 (the refreshed set is cached)", requests)
	}

	// The WITHDRAWN key is not in the current set, so a token signed with
	// it must be refused.
	_, err = validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: withdrawnToken},
		TokenExpectation{Nonce: testNonce, Audience: testAudience},
	)
	requireValidatorClass(t, err, ValidatorClassSignature)
}

// TestDecodeTokenResponse_RefusesAMemberItCannotRead proves that every
// member the token response carries must be readable as the shape it
// carries.
//
// A member that ARRIVED with a shape the connector cannot read makes the
// response unreadable. It is never read as though the response had not
// carried it: an unreadable scope is not an absent scope, and an absent
// scope falls back to the callback's scope.
func TestDecodeTokenResponse_RefusesAMemberItCannotRead(t *testing.T) {
	bodies := []string{
		`{"access_token":5}`,
		`{"access_token":"a","refresh_token":5}`,
		`{"access_token":"a","id_token":[]}`,
		`{"access_token":"a","token_type":{}}`,
		`{"access_token":"a","scope":["openid"]}`,
		`{"access_token":"a","expires_in":"3600"}`,
		`{"access_token":"a","earliest_refresh_at":"soon"}`,
	}
	for _, body := range bodies {
		if _, err := decodeTokenResponse([]byte(body)); err == nil {
			t.Errorf("the decoder accepted an unreadable response: %s", body)
		}
	}
}

// TestDecodeTokenResponse_AcceptsAnAbsentMember proves that an ABSENT
// member stays an omission.
//
// The compatibility decision for a genuinely absent scope is preserved: the
// callback's scope is the fallback for a provider that only reports it
// there.
func TestDecodeTokenResponse_AcceptsAnAbsentMember(t *testing.T) {
	tokens, err := decodeTokenResponse([]byte(`{"access_token":"a"}`))
	if err != nil {
		t.Fatalf("decodeTokenResponse: %v", err)
	}
	if tokens.Scope != "" {
		t.Errorf("the scope = %q, want none", tokens.Scope)
	}
	if tokens.ExpiresIn != 0 {
		t.Errorf("the lifetime = %d, want none", tokens.ExpiresIn)
	}
}

// TestRegister_RefusesAMalformedAuthoritativeScope proves that an
// unreadable token-response scope does not become an absent scope.
//
// The response carries the scope as an ARRAY - a shape the connector
// cannot read - and the callback carries the scope set WITH the direct
// plan-use scope. The registration must not succeed with the plan usage
// enabled: the authoritative grant was never read.
func TestRegister_RefusesAMalformedAuthoritativeScope(t *testing.T) {
	h := newHarness(t)
	h.fake.setTokenBody(
		`{"access_token":"access-token-1","refresh_token":"refresh-token-1","id_token":"id-token-1",` +
			`"token_type":"Bearer","expires_in":3600,"scope":["openid"]}`,
	)
	h.setDelivery(callbackDelivery{
		code:     "auth-code-1",
		clientID: "oaiapp_issued_1",
		scope:    "openid chatgpt.tokens.use.direct",
	})
	_, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"})
	if err == nil {
		t.Fatal("the registration accepted a scope the connector could not read")
	}
	if _, loadErr := h.profiles.Load("acct-test"); loadErr == nil {
		t.Error("the registration was persisted")
	}
}

// TestOIDCClient_ReportsTheThrottledCauseForAnExpiredCache proves that a
// refused fetch reports the THROTTLED cause.
//
// The key set is older than its lifetime, so the connector must read it
// again - and the previous attempt is younger than the renew interval, so
// the read is refused. The failure says so: it is the admission that
// refused the read, and not the provider that could not be reached.
func TestOIDCClient_ReportsTheThrottledCauseForAnExpiredCache(t *testing.T) {
	fake := newFakeOIDCServer(t)
	clock := &fakeClock{now: time.Now()}
	client, err := NewOIDCClient(OIDCClientConfig{
		DiscoveryURL: fake.server.URL + "/.well-known/openid-configuration",
		HTTPClient:   fake.server.Client(),
		Now:          clock.nowFunc,
		// The key set ages out before the renew interval opens, so the
		// cache is expired while the window is still closed.
		JWKSTTL: time.Second,
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

	// The key set ages out, and the previous attempt is inside the renew
	// interval: the read is refused.
	clock.advance(2 * time.Second)
	_, _, err = client.VerificationAuthority(context.Background(), fake.keyID)
	if err == nil {
		t.Fatal("the validation of an expired key cache succeeded")
	}
	if !errors.Is(err, errKeySetThrottled) {
		t.Errorf("the failure = %v, want the throttled cause", err)
	}
	if requests := fake.jwksFetches(); requests != 1 {
		t.Errorf("the key set was fetched %d times, want 1 (the throttled read fetched nothing)", requests)
	}
}

// TestOIDCValidator_RefusesAnAudienceWithANullElement proves that every
// element of an audience array must be a JSON string.
//
// Go's decoder accepts a null element as an empty string, and an empty
// audience entry names nobody: it would only make a malformed audience
// look like a well-formed one. A whole null MEMBER stays an absence -
// the JSON null is the document's explicit "no value".
func TestOIDCValidator_RefusesAnAudienceWithANullElement(t *testing.T) {
	cases := []struct {
		name     string
		audience string
	}{
		{name: "a valid audience and a null", audience: fmt.Sprintf("[%q,null]", testAudience)},
		{name: "a null and another audience", audience: `[null,"other-client"]`},
		{name: "a null alone", audience: `[null]`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fake := newFakeOIDCServer(t)
			validator := newTestValidator(t, fake, testIssuer)
			payload := fmt.Sprintf(
				`{"iss":%q,"aud":%s,"sub":%q,"exp":%d,"nonce":%q}`,
				testIssuer,
				testCase.audience,
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
			requireValidatorClass(t, err, ValidatorClassRejected)
		})
	}
}

// TestOIDCValidator_AcceptsAWholeNullAudience proves that the whole-member
// null stays an ABSENCE.
//
// The JSON null is the document's explicit "no value", and it says what an
// omitted member says: the token names no audience, and the audience check
// refuses it.
func TestOIDCValidator_AcceptsAWholeNullAudience(t *testing.T) {
	fake := newFakeOIDCServer(t)
	validator := newTestValidator(t, fake, testIssuer)
	payload := fmt.Sprintf(
		`{"iss":%q,"aud":null,"sub":%q,"exp":%d,"nonce":%q}`,
		testIssuer,
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
}

// TestDecodeTokenResponse_RefusesAFractionalNumber proves the token
// response's own numeric precision.
//
// The response's lifetime and its earliest refresh time are WHOLE seconds:
// the values are narrowed to an integer here, and a downward rounding
// would move a lifetime or an earliest refresh time silently. The JWT path
// is different on purpose - a JWT NumericDate may carry a fraction, and
// there the fraction is honoured.
func TestDecodeTokenResponse_RefusesAFractionalNumber(t *testing.T) {
	cases := []string{
		`{"access_token":"a","expires_in":3600.9}`,
		`{"access_token":"a","earliest_refresh_at":1700000000.9}`,
	}
	for _, body := range cases {
		if _, err := decodeTokenResponse([]byte(body)); err == nil {
			t.Errorf("the decoder accepted a fractional number: %s", body)
		}
	}
}

// TestOIDCValidator_RefusesAnUnreadableValidityWindow proves that a
// present but unreadable not-before claim is REFUSED.
//
// The not-before claim is optional: an ABSENT one means the token has no
// validity window, and the check is skipped. A present one is a real
// NumericDate, and an unreadable one is a constraint the connector cannot
// check - which is not the same as a constraint the token does not carry.
//
// The quoted number is a number in the PAST on purpose. A quoted number
// is a STRING, and a string is not a NumericDate: the token must be
// refused. The value is in the past so that a reader which accepted the
// quoted number and applied it as a window would ACCEPT the token - the
// case therefore discriminates the rule it names, and not some other
// refusal.
//
// The out-of-range values are checked one at a time, and the CAUSE is
// asserted next to the class: a value beyond the range is UNUSABLE, while
// a value inside the range that lies in the future is not-yet-valid, and
// the two share the class. The negative value is in the past, so a reader
// that skipped the range check would ACCEPT the token: the case
// discriminates the range rule on its own.
func TestOIDCValidator_RefusesAnUnreadableValidityWindow(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{name: "a string", value: `"not-a-date"`},
		{name: "an object", value: `{"year":2026}`},
		{name: "a quoted number", value: `"1"`},
		{name: "a number beyond the range", value: `1e100`},
		{name: "a negative number beyond the range", value: `-1e100`},
		{name: "a boolean", value: `true`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fake := newFakeOIDCServer(t)
			validator := newTestValidator(t, fake, testIssuer)
			payload := fmt.Sprintf(
				`{"iss":%q,"aud":%q,"sub":%q,"exp":%d,"nonce":%q,"nbf":%s}`,
				testIssuer,
				testAudience,
				testSubject,
				time.Now().Add(time.Hour).Unix(),
				testNonce,
				testCase.value,
			)
			token := signRS256Raw(t, fake.key, `{"alg":"RS256","kid":"`+fake.keyID+`"}`, payload)

			_, err := validator.ValidateIDToken(
				context.Background(),
				TokenResponse{IDToken: token},
				TokenExpectation{Nonce: testNonce, Audience: testAudience},
			)
			requireValidatorClass(t, err, ValidatorClassExpiry)
			if !errors.Is(err, errValidityWindowUnusable) {
				t.Errorf("the failure is not the unusable-validity-window cause: %v", err)
			}
		})
	}
}

// TestOIDCValidator_AcceptsAPastValidityWindow proves that a present, PAST
// and valid not-before claim leaves the token usable.
//
// The case is named for what it proves - ACCEPTANCE - and not for the
// fractional precision of the bound: a bound behind the clock cannot
// distinguish a honoured fraction from a dropped one, because the window
// check is "now + leeway < notBefore" and a bound below the clock is
// below that threshold either way.
//
// What it does prove is the interoperability property: a provider that
// sends a valid bound that has already passed does not make the token
// unusable, and a validator that refused every usable present bound
// would break that provider.
func TestOIDCValidator_AcceptsAPastValidityWindow(t *testing.T) {
	fake := newFakeOIDCServer(t)
	validator := newTestValidator(t, fake, testIssuer)
	payload := fmt.Sprintf(
		`{"iss":%q,"aud":%q,"sub":%q,"exp":%d,"nonce":%q,"nbf":%d}`,
		testIssuer,
		testAudience,
		testSubject,
		time.Now().Add(time.Hour).Unix(),
		testNonce,
		time.Now().Add(-time.Hour).Unix(),
	)
	token := signRS256Raw(t, fake.key, `{"alg":"RS256","kid":"`+fake.keyID+`"}`, payload)

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

// TestOIDCValidator_AcceptsAnAbsentValidityWindow proves that an absent
// not-before claim is an omission and not a refusal.
func TestOIDCValidator_AcceptsAnAbsentValidityWindow(t *testing.T) {
	fake := newFakeOIDCServer(t)
	validator := newTestValidator(t, fake, testIssuer)
	payload := fmt.Sprintf(
		`{"iss":%q,"aud":%q,"sub":%q,"exp":%d,"nonce":%q}`,
		testIssuer,
		testAudience,
		testSubject,
		time.Now().Add(time.Hour).Unix(),
		testNonce,
	)
	token := signRS256Raw(t, fake.key, `{"alg":"RS256","kid":"`+fake.keyID+`"}`, payload)

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

// TestOIDCValidator_HonoursAFractionalValidityWindow proves that a
// NumericDate with a FRACTION is honoured.
//
// The JWT specification allows a fractional NumericDate, and truncating a
// future bound would move it into the past.
//
// The construction is the one that discriminates. The clock is injected
// and carries NO leeway, and the bound is a QUARTER OF A SECOND AHEAD of
// it: the exact value refuses the token, while the whole-second truncation
// of that value lands BEHIND the clock and would accept it. A truncating
// implementation therefore fails this test, and so does one that samples
// an unusable window into no constraint.
//
// A bound that is BEHIND the clock cannot discriminate anything: the
// window check is "now + leeway < notBefore", and a bound below the clock
// is below that threshold whether the fraction is honoured or dropped. A
// case for such a bound would pass for the wrong reason - the window
// would have been ignored - so the case is not written.
func TestOIDCValidator_HonoursAFractionalValidityWindow(t *testing.T) {
	fake := newFakeOIDCServer(t)
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	validator, err := NewOIDCValidator(OIDCValidatorConfig{
		OIDCClientConfig: OIDCClientConfig{
			DiscoveryURL: fake.server.URL + "/.well-known/openid-configuration",
			HTTPClient:   fake.server.Client(),
			Now:          clock.nowFunc,
		},
		// A NEGATIVE leeway means no tolerance at all, so the quarter
		// second is not covered by the default tolerance.
		Leeway: -1,
	})
	if err != nil {
		t.Fatalf("NewOIDCValidator: %v", err)
	}
	fake.setIssuer(testIssuer)
	payload := fmt.Sprintf(
		`{"iss":%q,"aud":%q,"sub":%q,"exp":%d,"nonce":%q,"nbf":%s}`,
		testIssuer,
		testAudience,
		testSubject,
		clock.now.Add(time.Hour).Unix(),
		testNonce,
		strconv.FormatFloat(float64(clock.now.Unix())+0.25, 'f', 2, 64),
	)
	token := signRS256Raw(t, fake.key, `{"alg":"RS256","kid":"`+fake.keyID+`"}`, payload)

	_, err = validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: token},
		TokenExpectation{Nonce: testNonce, Audience: testAudience},
	)
	requireValidatorClass(t, err, ValidatorClassExpiry)
}

// TestOIDCValidator_RefusesAQuotedExpiry proves that a QUOTED number is
// not a NumericDate.
//
// Go's decoder accepts a quoted number into a json.Number, and that
// looseness would let a document express a date in a shape the
// specification does not allow.
func TestOIDCValidator_RefusesAQuotedExpiry(t *testing.T) {
	fake := newFakeOIDCServer(t)
	validator := newTestValidator(t, fake, testIssuer)
	payload := fmt.Sprintf(
		`{"iss":%q,"aud":%q,"sub":%q,"exp":"%d","nonce":%q}`,
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
	requireValidatorClass(t, err, ValidatorClassExpiry)
}

// TestOIDCValidator_RefusesAnExpiryBeyondTheRange proves that the range
// is enforced on EVERY value.
//
// A number that is integral but far outside the range is not a date, and
// reading it as one would overflow the conversion. The CAUSE is asserted
// next to the class: an expiry beyond the range is UNUSABLE, while an
// expiry inside the range that lies in the past is expired, and the two
// share the class.
func TestOIDCValidator_RefusesAnExpiryBeyondTheRange(t *testing.T) {
	fake := newFakeOIDCServer(t)
	validator := newTestValidator(t, fake, testIssuer)
	payload := fmt.Sprintf(
		`{"iss":%q,"aud":%q,"sub":%q,"exp":99999999999999999999,"nonce":%q}`,
		testIssuer,
		testAudience,
		testSubject,
		testNonce,
	)
	token := signRS256Raw(t, fake.key, `{"alg":"RS256","kid":"`+fake.keyID+`"}`, payload)

	_, err := validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: token},
		TokenExpectation{Nonce: testNonce, Audience: testAudience},
	)
	requireValidatorClass(t, err, ValidatorClassExpiry)
	if !errors.Is(err, errExpiryUnusable) {
		t.Errorf("the failure is not the unusable-expiry cause: %v", err)
	}
}

// TestOIDCValidator_RefusesAMalformedClaim proves that a claim that
// ARRIVED with a shape the connector cannot read is a malformed token.
//
// The subject, the issuer, the audience and the nonce are all REQUIRED:
// a present one that cannot be read is not read as though the token had
// not carried it.
//
// The malformed claim REPLACES the well-formed one. A payload that carried
// both would be refused for the REPEATED key, and the fixture would prove
// the duplicate rule instead of the malformed-claim rule it names.
func TestOIDCValidator_RefusesAMalformedClaim(t *testing.T) {
	cases := []struct {
		name  string
		claim string
		value any
	}{
		{name: "a numeric subject", claim: "sub", value: 42},
		{name: "a numeric issuer", claim: "iss", value: 42},
		{name: "a numeric audience", claim: "aud", value: 42},
		{name: "an audience of numbers", claim: "aud", value: []int{42}},
		{name: "a numeric nonce", claim: "nonce", value: 42},
		{name: "a numeric email", claim: "email", value: 42},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fake := newFakeOIDCServer(t)
			validator := newTestValidator(t, fake, testIssuer)
			claims := map[string]any{
				"iss":   testIssuer,
				"aud":   testAudience,
				"sub":   testSubject,
				"exp":   time.Now().Add(time.Hour).Unix(),
				"nonce": testNonce,
			}
			claims[testCase.claim] = testCase.value
			payload, err := json.Marshal(claims)
			if err != nil {
				t.Fatalf("marshal the payload: %v", err)
			}
			token := signRS256Raw(t, fake.key, `{"alg":"RS256","kid":"`+fake.keyID+`"}`, string(payload))

			_, err = validator.ValidateIDToken(
				context.Background(),
				TokenResponse{IDToken: token},
				TokenExpectation{Nonce: testNonce, Audience: testAudience},
			)
			requireValidatorClass(t, err, ValidatorClassRejected)
		})
	}
}

// TestOIDCValidator_RefusesAMalformedHeader proves the same for the
// header.
func TestOIDCValidator_RefusesAMalformedHeader(t *testing.T) {
	fake := newFakeOIDCServer(t)
	validator := newTestValidator(t, fake, testIssuer)
	payload := fmt.Sprintf(
		`{"iss":%q,"aud":%q,"sub":%q,"exp":%d,"nonce":%q}`,
		testIssuer,
		testAudience,
		testSubject,
		time.Now().Add(time.Hour).Unix(),
		testNonce,
	)
	token := signRS256Raw(t, fake.key, `{"alg":42,"kid":"`+fake.keyID+`"}`, payload)

	_, err := validator.ValidateIDToken(
		context.Background(),
		TokenResponse{IDToken: token},
		TokenExpectation{Nonce: testNonce, Audience: testAudience},
	)
	requireValidatorClass(t, err, ValidatorClassRejected)
}

// TestOIDCValidator_RefusesAnUnusableProviderKey proves that a key the
// standard library cannot use is MALFORMED provider key material.
//
// crypto/rsa refuses a public exponent above 2^31-1 - the bound is the
// standard library's own, so that its behaviour does not depend on the
// width of int (crypto/internal/fips140/rsa/rsa.go, in the toolchain this
// connector is built with). The key set is therefore refused as malformed,
// and the outcome is that the key set could not be read - NOT a signature
// failure of one token.
func TestOIDCValidator_RefusesAnUnusableProviderKey(t *testing.T) {
	fake := newFakeOIDCServer(t)
	validator := newTestValidator(t, fake, testIssuer)
	// An exponent of 2^40+1: odd, above three, and above the bound the
	// standard library uses.
	exponent := new(big.Int).Lsh(big.NewInt(1), 40)
	exponent.Add(exponent, big.NewInt(1))
	entry := fmt.Sprintf(
		`{"kty":"RSA","kid":%q,"use":"sig","alg":"RS256","n":%q,"e":%q}`,
		fake.keyID,
		base64.RawURLEncoding.EncodeToString(fake.key.N.Bytes()),
		base64.RawURLEncoding.EncodeToString(exponent.Bytes()),
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

// TestParseJWKS_RefusesAKeyItCannotRead proves that a key entry that
// ARRIVED with a shape the connector cannot read refuses the key set.
//
// The key type and the key id are interpreted for every entry, and the
// material for the entries that are candidates.
func TestParseJWKS_RefusesAKeyItCannotRead(t *testing.T) {
	cases := []string{
		`{"keys":[{"kty":42,"kid":"a"}]}`,
		`{"keys":[{"kty":"RSA","kid":42}]}`,
		`{"keys":[{"kty":"RSA","kid":"a","use":42}]}`,
		`{"keys":[{"kty":"RSA","kid":"a","alg":42}]}`,
		`{"keys":[{"kty":"RSA","kid":"a","n":42,"e":"AQAB"}]}`,
		`{"keys":[{"kty":"RSA","kid":"a","n":"AQAB","e":42}]}`,
	}
	for _, document := range cases {
		if _, err := parseJWKS([]byte(document)); err == nil {
			t.Errorf("the key-set reader accepted an unreadable key: %s", document)
		}
	}
}

// TestParseDiscovery_RefusesAMemberItCannotRead proves the same for the
// OIDC configuration.
func TestParseDiscovery_RefusesAMemberItCannotRead(t *testing.T) {
	cases := []string{
		`{"issuer":42,"jwks_uri":"https://auth.example.com/jwks.json"}`,
		`{"issuer":"https://auth.example.com","jwks_uri":42}`,
		`{"issuer":"https://auth.example.com","jwks_uri":"https://auth.example.com/jwks.json","token_endpoint":42}`,
		`{"issuer":"https://auth.example.com","jwks_uri":"https://auth.example.com/jwks.json","revocation_endpoint":42}`,
	}
	for _, document := range cases {
		if _, err := parseDiscovery([]byte(document)); err == nil {
			t.Errorf("the configuration reader accepted an unreadable document: %s", document)
		}
	}
}

// TestJSONMembers_KeepsACaseVariantApart proves that the exact reading
// still holds after the three-way change.
//
// A member whose name differs only in case is a different member, and it
// never overwrites the one the connector asks for.
func TestJSONMembers_KeepsACaseVariantApart(t *testing.T) {
	members, err := jsonMembers([]byte(`{"aud":"exact","AUD":"alias"}`))
	if err != nil {
		t.Fatalf("jsonMembers: %v", err)
	}
	exact := memberString(members, "aud")
	if !exact.usable() || exact.Value != "exact" {
		t.Errorf("the exact member = %q (state %d), want the exact value", exact.Value, exact.State)
	}
	if _, ok := members["AUD"]; !ok {
		t.Error("the case variant was not carried")
	}
}

// TestTokenResponse_ReportsTheEarliestRefresh proves that the documented
// member of the response is read, and that its shape is checked.
func TestTokenResponse_ReportsTheEarliestRefresh(t *testing.T) {
	tokens, err := decodeTokenResponse([]byte(`{"access_token":"a","earliest_refresh_at":1700000000}`))
	if err != nil {
		t.Fatalf("decodeTokenResponse: %v", err)
	}
	if tokens.EarliestRefreshAt != 1700000000 {
		t.Errorf("the earliest refresh = %d, want 1700000000", tokens.EarliestRefreshAt)
	}
}
