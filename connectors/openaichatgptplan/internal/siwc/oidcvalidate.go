package siwc

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The ID-token validation.
//
// The validation is the connector's proof that the account the provider
// authorized is the account the flow is about to record. It is made of
// four steps, in this order:
//
//  1. the token is read as a JWT, and its three parts are understood in
//     full or the token is refused;
//  2. the algorithm the token names is ACCEPTED or the token is refused -
//     before the key set is consulted at all, so a token that names an
//     algorithm this connector does not accept costs no network round trip
//     and is never attempted;
//  3. the signature is verified against the provider's published key set;
//  4. only then are the claims read: a claim of an unverified token is
//     not evidence.
//
// The expectations - the attempt's nonce and the issued client id the token
// must be addressed to - come from the FLOW, never from the token.
//
// # What is never rendered
//
// Every failure is a classified *ValidatorError. The flow renders the
// class and nothing else, and the causes below are CONSTANT and
// value-free, so neither the class nor the cause can carry the token, a
// fragment of it, a claim of it or a key of it into an operator- or
// support-facing error.

// DefaultClockLeeway is the tolerance the expiry and the validity window
// are checked with.
//
// The tolerance is SMALL and it is named: a token that expired seconds ago
// is still accepted, because the provider's clock and this host's clock
// are not the same clock, and a large tolerance would accept a token that
// is genuinely expired.
const DefaultClockLeeway = 5 * time.Second

// maxUnixSeconds bounds a numeric date claim.
//
// A numeric date outside this range is not a date: it is a value this
// connector refuses to interpret, and the bound keeps the conversion to a
// time from overflowing.
const maxUnixSeconds = 1 << 40

// The failures of the ID-token validation.
//
// They are CONSTANT and value-free: an untrusted token must never put a
// fragment of itself into a cause that a caller may unwrap.
var (
	// errTokenMissing reports a token response without an ID token.
	errTokenMissing = errors.New("siwc: the token response carried no ID token")
	// errTokenMalformed reports a token that is not a well-formed JWT.
	errTokenMalformed = errors.New("siwc: the ID token is not a well-formed JWT")
	// errAlgorithmNotAccepted reports a token that names an algorithm this
	// connector does not accept.
	errAlgorithmNotAccepted = errors.New("siwc: the ID token names an algorithm this connector does not accept")
	// errKeyIDMissing reports a token that names no key id.
	errKeyIDMissing = errors.New("siwc: the ID token names no key id")
	// errSignatureInvalid reports a token whose signature did not verify.
	errSignatureInvalid = errors.New("siwc: the ID token's signature did not verify")
	// errIssuerMismatch reports a token of another issuer.
	errIssuerMismatch = errors.New("siwc: the ID token names another issuer")
	// errAudienceMismatch reports a token of another audience.
	errAudienceMismatch = errors.New("siwc: the ID token names another audience")
	// errExpiryMissing reports a token without an expiry.
	errExpiryMissing = errors.New("siwc: the ID token carries no expiry")
	// errExpiryUnusable reports an expiry the connector cannot read.
	errExpiryUnusable = errors.New("siwc: the ID token carries an expiry this connector cannot read")
	// errValidityWindowUnusable reports a validity window the connector
	// cannot read.
	errValidityWindowUnusable = errors.New(
		"siwc: the ID token carries a validity window this connector cannot read",
	)
	// errTokenExpired reports an expired token.
	errTokenExpired = errors.New("siwc: the ID token has expired")
	// errTokenNotYetValid reports a token that is not valid yet.
	errTokenNotYetValid = errors.New("siwc: the ID token is not valid yet")
	// errNonceMismatch reports a token that belongs to another attempt.
	errNonceMismatch = errors.New("siwc: the ID token belongs to another attempt")
	// errSubjectMissing reports a token without a usable subject.
	errSubjectMissing = errors.New("siwc: the ID token carries no subject")
	// errExpectationIncomplete reports an attempt that did not supply the
	// expectation the validation needs.
	errExpectationIncomplete = errors.New("siwc: the attempt did not supply the expected audience and nonce")
)

// OIDCValidatorConfig is the configuration of the ID-token validator.
type OIDCValidatorConfig struct {
	// OIDCClientConfig configures the client that fetches the provider's
	// OIDC configuration and its key set.
	OIDCClientConfig
	// Leeway is the tolerance the expiry and the validity window are
	// checked with. Zero means DefaultClockLeeway. A NEGATIVE value means
	// NO tolerance at all, which the tests use to place a bound exactly
	// on the clock's own second.
	Leeway time.Duration
}

// OIDCValidator validates an ID token against the provider's published key
// set and the attempt's expectation.
//
// The validator is safe for concurrent use.
type OIDCValidator struct {
	client *OIDCClient
	leeway time.Duration
}

// NewOIDCValidator returns the validator of the provider's ID tokens.
func NewOIDCValidator(config OIDCValidatorConfig) (*OIDCValidator, error) {
	client, err := NewOIDCClient(config.OIDCClientConfig)
	if err != nil {
		return nil, err
	}
	leeway := config.Leeway
	if leeway == 0 {
		leeway = DefaultClockLeeway
	}
	if leeway < 0 {
		leeway = 0
	}
	return &OIDCValidator{client: client, leeway: leeway}, nil
}

// Discovery returns the provider's OIDC configuration.
//
// It is the same document the validation uses, and it carries the
// revocation endpoint the sign-out needs.
func (v *OIDCValidator) Discovery(ctx context.Context) (OIDCDiscovery, error) {
	return v.client.Discovery(ctx)
}

// ValidateIDToken validates the ID token of a token response.
//
// The token is refused unless every one of these holds:
//
//   - the attempt supplied its nonce and the issued client id the token
//     must be addressed to;
//   - the token is a well-formed JWT whose header and payload do not
//     repeat a key;
//   - the algorithm the token names is one this connector accepts;
//   - the signature verifies against the provider's published key set;
//   - the issuer is the issuer the provider's configuration names;
//   - the audience names the expected issued client id;
//   - the token has not expired;
//   - the nonce is the attempt's nonce;
//   - the subject is present and usable.
//
// The failure is a classified *ValidatorError, and the class names the
// constraint that failed. The token never appears in the failure.
func (v *OIDCValidator) ValidateIDToken(
	ctx context.Context,
	tokens TokenResponse,
	expected TokenExpectation,
) (Identity, error) {
	// The expectation is checked FIRST. An expectation that cannot be
	// supplied is a defect of the flow, and a flow that supplies none must
	// not have its tokens accepted: the audience check would have nothing
	// to compare with, and a check with nothing to compare with is a
	// check that passes.
	if strings.TrimSpace(expected.Audience) == "" {
		return Identity{}, NewValidatorError(ValidatorClassAudience, errExpectationIncomplete)
	}
	if strings.TrimSpace(expected.Nonce) == "" {
		return Identity{}, NewValidatorError(ValidatorClassNonce, errExpectationIncomplete)
	}
	if strings.TrimSpace(tokens.IDToken) == "" {
		return Identity{}, NewValidatorError(ValidatorClassRejected, errTokenMissing)
	}
	parts, err := parseJWT(tokens.IDToken)
	if err != nil {
		return Identity{}, NewValidatorError(ValidatorClassRejected, err)
	}
	// The algorithm is ACCEPTED or refused, and the refusal happens before
	// the key set is consulted: a token that names an algorithm this
	// connector does not accept must never be attempted, and it must not
	// cost a request to the provider either.
	//
	// A header member that ARRIVES with a shape the connector cannot read
	// is a malformed header, and it is refused as one. It is not read as
	// though the header had not carried it.
	if parts.header.Algorithm.State == memberInvalid {
		return Identity{}, NewValidatorError(ValidatorClassRejected, errTokenMalformed)
	}
	if !parts.header.Algorithm.usable() || parts.header.Algorithm.Value != signingAlgRS256 {
		return Identity{}, NewValidatorError(ValidatorClassSignature, errAlgorithmNotAccepted)
	}
	if parts.header.KeyID.State == memberInvalid {
		return Identity{}, NewValidatorError(ValidatorClassRejected, errTokenMalformed)
	}
	keyID := strings.TrimSpace(parts.header.KeyID.Value)
	if keyID == "" {
		return Identity{}, NewValidatorError(ValidatorClassSignature, errKeyIDMissing)
	}
	// The provider's configuration and the key set it selects are read as
	// ONE snapshot: the issuer the token is checked against and the key
	// its signature is verified with are always the ones the same
	// authority published. A configuration that cannot be read leaves the
	// validation impossible, and that is reported as such.
	discovery, key, err := v.client.VerificationAuthority(ctx, keyID)
	if err != nil {
		if errors.Is(err, errUnknownKey) {
			return Identity{}, NewValidatorError(ValidatorClassSignature, err)
		}
		return Identity{}, NewValidatorError(ValidatorClassUnavailable, err)
	}
	if err := verifyRS256(key, parts); err != nil {
		return Identity{}, NewValidatorError(ValidatorClassSignature, err)
	}
	// The claims are read only after the signature verified. A claim of an
	// unverified token is not evidence, and the order is what makes the
	// difference between a validated identity and an asserted one.
	//
	// Every claim below distinguishes an ABSENT claim from an INVALID one:
	// an absent claim fails the constraint it belongs to, and an invalid
	// one is a malformed token. The two are never read alike.
	now := v.nowSeconds()
	switch parts.claims.Issuer.State {
	case memberInvalid:
		return Identity{}, NewValidatorError(ValidatorClassRejected, errTokenMalformed)
	case memberAbsent:
		return Identity{}, NewValidatorError(ValidatorClassIssuer, errIssuerMismatch)
	}
	if parts.claims.Issuer.Value != discovery.Issuer {
		return Identity{}, NewValidatorError(ValidatorClassIssuer, errIssuerMismatch)
	}
	switch parts.claims.Audience.State {
	case memberInvalid:
		return Identity{}, NewValidatorError(ValidatorClassRejected, errTokenMalformed)
	case memberAbsent:
		return Identity{}, NewValidatorError(ValidatorClassAudience, errAudienceMismatch)
	}
	if !audienceContains(parts.claims.Audience.Value, expected.Audience) {
		return Identity{}, NewValidatorError(ValidatorClassAudience, errAudienceMismatch)
	}
	expiry, expiryState := numericDate(parts.claims.Expiry)
	switch expiryState {
	case memberInvalid:
		return Identity{}, NewValidatorError(ValidatorClassExpiry, errExpiryUnusable)
	case memberAbsent:
		return Identity{}, NewValidatorError(ValidatorClassExpiry, errExpiryMissing)
	}
	if now > expiry+v.leewaySeconds() {
		return Identity{}, NewValidatorError(ValidatorClassExpiry, errTokenExpired)
	}
	// The not-before claim is OPTIONAL: an absent one means the token has
	// no validity window, and the check is skipped. A present one is a
	// REAL NumericDate - fractions included, because truncating a future
	// bound would move it into the past - and an unreadable one is
	// REFUSED rather than skipped: a constraint the connector cannot read
	// is not a constraint the token does not carry.
	switch parts.claims.NotBefore.State {
	case memberInvalid:
		return Identity{}, NewValidatorError(ValidatorClassExpiry, errValidityWindowUnusable)
	case memberUsable:
		notBefore, state := numericDate(parts.claims.NotBefore)
		if state != memberUsable {
			return Identity{}, NewValidatorError(ValidatorClassExpiry, errValidityWindowUnusable)
		}
		if now+v.leewaySeconds() < notBefore {
			return Identity{}, NewValidatorError(ValidatorClassExpiry, errTokenNotYetValid)
		}
	}
	switch parts.claims.Nonce.State {
	case memberInvalid:
		return Identity{}, NewValidatorError(ValidatorClassRejected, errTokenMalformed)
	case memberAbsent:
		return Identity{}, NewValidatorError(ValidatorClassNonce, errNonceMismatch)
	}
	if parts.claims.Nonce.Value == "" ||
		subtle.ConstantTimeCompare([]byte(parts.claims.Nonce.Value), []byte(expected.Nonce)) != 1 {
		return Identity{}, NewValidatorError(ValidatorClassNonce, errNonceMismatch)
	}
	switch parts.claims.Subject.State {
	case memberInvalid:
		return Identity{}, NewValidatorError(ValidatorClassRejected, errTokenMalformed)
	case memberAbsent:
		return Identity{}, NewValidatorError(ValidatorClassSubject, errSubjectMissing)
	}
	subject := strings.TrimSpace(parts.claims.Subject.Value)
	if subject == "" {
		return Identity{}, NewValidatorError(ValidatorClassSubject, errSubjectMissing)
	}
	// The email is a HINT. The documentation names the subject as the
	// account identity, and the email may be shared by several
	// registrations or absent altogether. A present email the connector
	// cannot read is a malformed claim, and it is refused as one.
	if parts.claims.Email.State == memberInvalid {
		return Identity{}, NewValidatorError(ValidatorClassRejected, errTokenMalformed)
	}
	return Identity{Subject: subject, EmailHint: strings.TrimSpace(parts.claims.Email.Value)}, nil
}

// jwtHeader is the header of a JWT.
//
// The members are read by their EXACT names, so a header that carries an
// "ALG" next to its "alg" is read as the "alg" it carries and as a
// separate member that this connector does not use.
//
// Every member carries the state of its presence and shape: a header that
// ARRIVES with a shape the connector cannot read is a malformed header,
// not a header without that member.
type jwtHeader struct {
	// Algorithm is the signature algorithm the token names.
	Algorithm memberValue[string]
	// KeyID is the id of the key the token was signed with.
	KeyID memberValue[string]
}

// jwtClaims are the claims this connector reads.
//
// The audience is kept raw: the claim is a string in one token and an
// array of strings in another, and the shape is decided when the claim is
// read rather than when the token is parsed. A member that carries
// another shape is INVALID, and the token is then refused as malformed.
//
// The claims this connector does not check are not read: the validation
// names the constraints it enforces, and a claim it does not enforce is
// not a claim it needs.
//
// The members are read by their EXACT names. A payload that carries an
// "AUD" next to its "aud" is read as the "aud" it carries: a typed decode
// would match the alias to the same field and let the document decide
// which value the audience check reads, and the audience check is the
// check that keeps a token minted for another registration out.
//
// Every claim carries the state of its presence and shape. A claim that
// ARRIVES with a shape the connector cannot read is a malformed claim,
// and it is refused: it is never read as though the token had not carried
// it. The nbf is the case that matters most - an absent nbf means no
// validity window, and an unreadable one must not mean the same thing.
type jwtClaims struct {
	Issuer    memberValue[string]
	Subject   memberValue[string]
	Audience  memberValue[json.RawMessage]
	Expiry    memberValue[json.Number]
	NotBefore memberValue[json.Number]
	Nonce     memberValue[string]
	Email     memberValue[string]
}

// readJWTHeader reads the header of a JWT by exact member name.
func readJWTHeader(members map[string]json.RawMessage) jwtHeader {
	return jwtHeader{
		Algorithm: memberString(members, "alg"),
		KeyID:     memberString(members, "kid"),
	}
}

// readJWTClaims reads the claims of a JWT by exact member name.
func readJWTClaims(members map[string]json.RawMessage) jwtClaims {
	return jwtClaims{
		Issuer:    memberString(members, "iss"),
		Subject:   memberString(members, "sub"),
		Audience:  readAudience(members),
		Expiry:    memberNumber(members, "exp"),
		NotBefore: memberNumber(members, "nbf"),
		Nonce:     memberString(members, "nonce"),
		Email:     memberString(members, "email"),
	}
}

// readAudience reads the audience claim.
//
// The documented shape is a string or an array of strings. A member that
// carries another shape is INVALID: it ARRIVED, and the connector cannot
// read it as an audience. The token is then refused as MALFORMED - not as
// a token that names another audience, which is what a well-formed
// audience that does not name the expected client id is.
//
// The ELEMENTS of an array are checked one by one: every element must be a
// JSON string. Go's decoder would accept a null element as an empty
// string, and an empty audience entry names nobody - it would only make a
// malformed audience look like a well-formed one.
//
// A WHOLE null member stays an ABSENCE: the JSON null is the document's
// explicit "no value", and it says what an omitted member says.
func readAudience(members map[string]json.RawMessage) memberValue[json.RawMessage] {
	audience := memberRaw(members, "aud")
	if audience.State != memberUsable {
		return audience
	}
	trimmed := strings.TrimSpace(string(audience.Value))
	if trimmed == "" {
		return memberValue[json.RawMessage]{State: memberInvalid}
	}
	switch trimmed[0] {
	case '"':
		var single string
		if err := json.Unmarshal(audience.Value, &single); err != nil {
			return memberValue[json.RawMessage]{State: memberInvalid}
		}
	case '[':
		var elements []json.RawMessage
		if err := json.Unmarshal(audience.Value, &elements); err != nil {
			return memberValue[json.RawMessage]{State: memberInvalid}
		}
		if elements == nil {
			return memberValue[json.RawMessage]{State: memberInvalid}
		}
		for _, element := range elements {
			elementText := strings.TrimSpace(string(element))
			if len(elementText) == 0 || elementText[0] != '"' {
				return memberValue[json.RawMessage]{State: memberInvalid}
			}
			var single string
			if err := json.Unmarshal(element, &single); err != nil {
				return memberValue[json.RawMessage]{State: memberInvalid}
			}
		}
	default:
		return memberValue[json.RawMessage]{State: memberInvalid}
	}
	return audience
}

// jwtParts is a read JWT.
type jwtParts struct {
	header jwtHeader
	claims jwtClaims
	// signingInput is the exact bytes the signature covers.
	signingInput []byte
	// signature is the token's signature.
	signature []byte
}

// parseJWT reads a JWT.
//
// The three parts are ALL read: a token that does not have three parts, or
// whose header or payload repeats a key, is refused. A token that is read
// in part is a token whose meaning this connector would be guessing.
//
// The header and the payload are read by EXACT member name. The typed
// decoder of the standard library matches a field's name case-insensitively,
// and a document that carries both "aud" and "AUD" would then decide which
// value the audience check reads.
func parseJWT(raw string) (jwtParts, error) {
	segments := strings.Split(raw, ".")
	if len(segments) != 3 {
		return jwtParts{}, errTokenMalformed
	}
	headerBytes, err := decodeBase64URL(segments[0])
	if err != nil {
		return jwtParts{}, errTokenMalformed
	}
	payloadBytes, err := decodeBase64URL(segments[1])
	if err != nil {
		return jwtParts{}, errTokenMalformed
	}
	signature, err := decodeBase64URL(segments[2])
	if err != nil {
		return jwtParts{}, errTokenMalformed
	}
	if err := rejectDuplicateJSONKeys(headerBytes); err != nil {
		return jwtParts{}, errTokenMalformed
	}
	if err := rejectDuplicateJSONKeys(payloadBytes); err != nil {
		return jwtParts{}, errTokenMalformed
	}
	headerMembers, err := jsonMembers(headerBytes)
	if err != nil {
		return jwtParts{}, errTokenMalformed
	}
	claimMembers, err := jsonMembers(payloadBytes)
	if err != nil {
		return jwtParts{}, errTokenMalformed
	}
	return jwtParts{
		header:       readJWTHeader(headerMembers),
		claims:       readJWTClaims(claimMembers),
		signingInput: []byte(segments[0] + "." + segments[1]),
		signature:    signature,
	}, nil
}

// verifyRS256 verifies the token's signature against the key.
func verifyRS256(key *rsa.PublicKey, parts jwtParts) error {
	if key == nil {
		return errSignatureInvalid
	}
	digest := sha256.Sum256(parts.signingInput)
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], parts.signature); err != nil {
		return errSignatureInvalid
	}
	return nil
}

// audienceContains reports whether the audience claim names the expected
// audience.
//
// The claim is a string in one token and an array of strings in another,
// and both shapes are read. A claim of another shape names no audience at
// all, and a token whose audience names nothing is a token of another
// audience.
func audienceContains(raw json.RawMessage, expected string) bool {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return false
	}
	switch trimmed[0] {
	case '"':
		var single string
		if err := json.Unmarshal(raw, &single); err != nil {
			return false
		}
		return single == expected
	case '[':
		var many []string
		if err := json.Unmarshal(raw, &many); err != nil {
			return false
		}
		return slices.Contains(many, expected)
	default:
		return false
	}
}

// numericDate reads a JWT NumericDate.
//
// A NumericDate is a number of seconds since the epoch, and the JWT
// specification allows a FRACTION: the value is a number, and truncating
// it would move a future bound into the past. The fraction is therefore
// kept, and the comparison happens in seconds.
//
// The value must be within the range this connector accepts, and the
// range is enforced on EVERY value: a number that is integral but far
// outside the range is not a date, and reading it as one would overflow
// the conversion.
//
// The state is the member's own state for an absent member, and INVALID
// for a value that is not a date this connector can read. A claim that
// ARRIVED and cannot be read is never read as though it had not arrived.
func numericDate(value memberValue[json.Number]) (float64, memberState) {
	if value.State != memberUsable {
		return 0, value.State
	}
	text := strings.TrimSpace(value.Value.String())
	if text == "" {
		return 0, memberInvalid
	}
	seconds, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return 0, memberInvalid
	}
	if seconds > maxUnixSeconds || seconds < -maxUnixSeconds {
		return 0, memberInvalid
	}
	return seconds, memberUsable
}

// nowSeconds returns the clock's reading in seconds since the epoch.
//
// The fraction is kept, because the claims it is compared against may
// carry one.
func (v *OIDCValidator) nowSeconds() float64 {
	now := v.client.now()
	return float64(now.Unix()) + float64(now.Nanosecond())/1e9
}

// leewaySeconds returns the tolerance in seconds.
//
// The tolerance is small and named: a token that expired seconds ago is
// still accepted, because the provider's clock and this host's clock are
// not the same clock, and a large tolerance would accept a token that is
// genuinely expired.
func (v *OIDCValidator) leewaySeconds() float64 {
	return v.leeway.Seconds()
}
