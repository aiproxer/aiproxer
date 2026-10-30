package siwc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// The identity seam of the SIWC flow.
//
// The authorization flow needs the VALIDATED identity of the authorized
// account: the subject is the authoritative identity of a registration, and
// the returning authorization confirms it against the selected registration
// before it replaces any credential.
//
// The validation itself - the signature against OpenAI's published JWKS,
// the issuer, the audience, the expiry and the attempt's nonce - belongs to
// the OIDC phase of the migration. This file defines the seam that the
// authorization flow depends on, so that the flow can be written and proven
// without the validator's implementation, and so that the validator can be
// replaced without touching the flow.

// Identity is the validated identity of one ChatGPT account.
//
// The subject is the provider's stable identifier for the account, and it
// is the authoritative identity of a registration: the store never looks a
// profile up by the email, and the email may be shared by several
// registrations.
//
// The email hint is display metadata. It is a hint, not a key.
type Identity struct {
	// Subject is the validated subject of the account.
	Subject string
	// EmailHint is the email the provider reported, if it reported one.
	EmailHint string
}

// TokenExpectation is what the flow knows about the pending attempt
// BEFORE the token response arrives.
//
// The expectation is the flow's OWN knowledge: the nonce it generated for
// the attempt and the issued client id the exchange carried. It is never
// derived from the token.
//
// The audience is the case that matters. The documentation requires the ID
// token's audience to be checked "against the issued client ID", and the
// issued client id is the flow's: the provider hands it out during the
// registration and the flow retains it with the registration. An audience
// read out of the token and compared with itself would accept a token that
// was minted for ANOTHER registration, and that token's subject would then
// decide which registration is replaced.
//
// The struct has no usable zero value. A validator that receives an empty
// expectation refuses the token, so an omitted expectation can never turn
// into a relaxed check.
type TokenExpectation struct {
	// Nonce is the attempt's OIDC nonce.
	//
	// It is the nonce the flow generated for the attempt and sent with the
	// authorization request. The token's nonce claim must match it.
	Nonce string
	// Audience is the issued client id the ID token must be addressed to.
	//
	// It is the issued client id the flow carried into the code exchange,
	// never the dynamic registration entry point and never a value the
	// token itself carries.
	Audience string
}

// IDTokenValidator validates the ID token of a token response.
//
// The validator is the seam between the authorization flow and the OIDC
// validation. The flow depends on the validated identity, never on the
// token's bytes: an implementation that skipped the validation would let
// an unverified subject decide which registration is replaced.
//
// The expectation carries the attempt's nonce and the issued client id the
// token must be addressed to. A validator that does not compare the token's
// nonce claim with the expected nonce accepts a token that belongs to
// another attempt, and a validator that does not compare the token's
// audience with the expected audience accepts a token that was minted for
// another registration.
//
// CONTRACT FOR THE OIDC PHASE (task 2.4): a validator reports a rejection
// as a *ValidatorError, whose Class names the constraint that failed
// (ValidatorClassSignature, ValidatorClassIssuer, ValidatorClassAudience,
// ValidatorClassExpiry, ValidatorClassNonce, ValidatorClassSubject,
// ValidatorClassScope or ValidatorClassUnavailable). The flow renders the
// class and nothing else, so a validator never puts the token, or a
// fragment of it, into its own error message. A validator that does not
// classify its failure is reported as an unclassified rejection, and its
// message is still never rendered.
//
// The class vocabulary belongs to the package rather than to one
// implementation: the granted-scope check of this package reports its
// outcome with the same vocabulary, because a caller classifies an outcome
// by the class and never by matching on message text.
type IDTokenValidator interface {
	// ValidateIDToken validates the ID token of the token response against
	// the attempt's expectation and returns the validated identity.
	//
	// A validator that receives an empty expectation MUST refuse the
	// token: an expectation that cannot be supplied is not an expectation
	// that may be skipped.
	ValidateIDToken(ctx context.Context, tokens TokenResponse, expected TokenExpectation) (Identity, error)
}

// ErrIDTokenValidationUnavailable reports that the ID-token validation is
// not part of this build.
//
// The validation needs OpenAI's published JWKS, and it is the OIDC phase of
// the migration that owns it. The placeholder exists so that the
// authorization flow has a validator to depend on and so that an operator
// is told what is missing, instead of a flow that accepts an unverified
// identity.
var ErrIDTokenValidationUnavailable = errors.New(
	"siwc: the ID-token validation against OpenAI's JWKS is not part of this connector build",
)

// ErrIDTokenRejected reports that the ID token of a token response was
// rejected.
//
// The rejection is CLASSIFIED and the validator's own text is never
// rendered: an implementation that reports the token, or a fragment of it,
// in its error must not be able to reach an operator- or support-facing
// error.
var ErrIDTokenRejected = errors.New("siwc: the ID token of the authorized account was rejected")

// The classes of a rejected ID token.
//
// The class is preserved, because the recovery differs per class: a token
// that belongs to another attempt is not retryable, while a provider that
// could not be reached might be. A caller classifies an outcome by the
// class, never by matching on message text.
const (
	// ValidatorClassSignature is a token whose signature did not verify.
	ValidatorClassSignature = "signature"
	// ValidatorClassIssuer is a token of another issuer.
	ValidatorClassIssuer = "issuer"
	// ValidatorClassAudience is a token of another audience.
	ValidatorClassAudience = "audience"
	// ValidatorClassExpiry is an expired token.
	ValidatorClassExpiry = "expiry"
	// ValidatorClassNonce is a token that belongs to another attempt.
	ValidatorClassNonce = "nonce"
	// ValidatorClassSubject is a token without a usable subject.
	ValidatorClassSubject = "subject"
	// ValidatorClassScope is a token whose scopes do not authorize the
	// requested use.
	ValidatorClassScope = "scope"
	// ValidatorClassUnavailable is a validation that could not be
	// performed at all.
	ValidatorClassUnavailable = "unavailable"
	// ValidatorClassRejected is a rejection that the validator did not
	// classify.
	ValidatorClassRejected = "rejected"
)

// ValidatorError is the classified failure of an ID-token validation.
//
// The class is preserved and the validator's own message is NOT rendered:
// the message of a validator that rejects a token can carry the token
// itself. The cause is kept for classification only, and the formatter
// below keeps every formatting verb away from it.
type ValidatorError struct {
	// Class names the constraint that failed.
	Class string
	// cause is the validator's own failure. It is never rendered.
	cause error
}

// NewValidatorError returns the classified failure of a validation.
//
// The cause is preserved for classification, and it is never rendered: the
// class is the whole of the operator-facing diagnostic.
func NewValidatorError(class string, cause error) *ValidatorError {
	trimmed := strings.TrimSpace(class)
	if trimmed == "" {
		trimmed = ValidatorClassRejected
	}
	return &ValidatorError{Class: trimmed, cause: cause}
}

// Error renders the classified failure.
// Error renders the classified failure.
//
// The rendering is the WHOLE of the operator-facing diagnostic: it names
// the class and nothing else, so a validator that reported the token in
// its own error could not put it here.
//
// The SCOPE class is rendered as the OUTCOME it is: a granted scope set
// without the direct plan-use scope is a legitimate, AUTHENTICATED
// sign-in whose plan usage is not authorized, and rendering it as a
// rejected ID token would tell the operator something that did not happen.
//
// The UNAVAILABLE class says that the token could not be VALIDATED rather
// than that it was rejected: no identity was accepted, and the provider
// was not reachable or its key set could not be read. The class is the
// shared one - the recovery is the same - and only the wording differs.
func (e *ValidatorError) Error() string {
	return validatorRendering(e.Class)
}

// validatorRendering renders the classified failure of one class.
//
// It is the single source of the rendering, so the type and the tests
// cannot drift apart.
func validatorRendering(class string) string {
	switch class {
	case ValidatorClassScope:
		return "siwc: the granted scopes do not authorize the requested use: " + class
	case ValidatorClassUnavailable:
		return "siwc: the ID token could not be validated: " + class
	default:
		return "siwc: the ID token was rejected: " + class
	}
}

// Unwrap returns the validator's own failure.
//
// The failure is preserved so that a caller can classify it, and it is
// never rendered by this package.
func (e *ValidatorError) Unwrap() error { return e.cause }

// Is reports whether the failure is the classified rejection.
//
// The comparison is by the sentinel, so a caller can classify the outcome
// without matching on message text.
//
// The SCOPE class is NOT an ID-token rejection. A valid ID token without
// the direct plan-use scope is a retained sign-in, and a gate that
// classifies the outcome must not read it as a rejected token: the
// recovery differs - the sign-in is kept and the plan usage is disabled -
// and the two outcomes must stay distinguishable.
func (e *ValidatorError) Is(target error) bool {
	if target == ErrIDTokenRejected {
		return e.Class != ValidatorClassScope
	}
	return false
}

// Format implements fmt.Formatter, so that no formatting verb can reach
// the validator's own failure.
//
// The guarantee holds when fmt can dispatch to this error, so the routes
// where it cannot are the ones ProfileRecord documents: "%T" is safe, "%w"
// and "%p" bypass every method on this type, and an error held in an
// unexported field or supplied as a non-interfaceable reflect.Value is
// rendered from its raw fields.
func (e *ValidatorError) Format(state fmt.State, verb rune) {
	safe := e.Error()
	if verb == 'q' {
		_, _ = io.WriteString(state, strconv.Quote(safe))
		return
	}
	_, _ = io.WriteString(state, safe)
}

// classifyValidatorError turns a validator failure into the flow's
// classified failure.
//
// The validator's own text is never rendered. A validator is an
// implementation detail that task 2.4 owns, and an implementation that
// reported the token in its error would otherwise put the token into an
// operator- or support-facing error.
//
// A validator that classifies its own failure keeps the class; a validator
// that does not is reported as an unclassified rejection. The placeholder
// of this build keeps its own message, because that message names a
// MISSING FEATURE and carries nothing sensitive.
func classifyValidatorError(err error) error {
	if err == nil {
		return nil
	}
	if classified, ok := errors.AsType[*ValidatorError](err); ok {
		return classified
	}
	if errors.Is(err, ErrIDTokenValidationUnavailable) {
		return fmt.Errorf("%w: %w", ErrIDTokenRejected, ErrIDTokenValidationUnavailable)
	}
	return NewValidatorError(ValidatorClassRejected, err)
}

// UnavailableIDTokenValidator is the validator of a build that cannot
// validate an ID token.
//
// It fails closed: it never returns an identity, so no registration is ever
// written from an unvalidated token.
//
// It is the seam's fail-closed implementation, not the connector's
// configuration: a deployment that has no way to reach the provider's key
// set uses it instead of a validator that would accept an unverified
// subject.
type UnavailableIDTokenValidator struct{}

// ValidateIDToken always fails.
func (UnavailableIDTokenValidator) ValidateIDToken(context.Context, TokenResponse, TokenExpectation) (Identity, error) {
	return Identity{}, ErrIDTokenValidationUnavailable
}

// confirmSubject checks that the authorized account is the selected
// registration.
//
// The subject is compared, and only the subject: the email is display
// metadata that two registrations of the same account may share and that a
// provider may not report at all.
//
// The comparison happens BEFORE the credentials are replaced. A sign-in of a
// different account must not take over the selected registration, and it
// must not leave the selected registration holding another account's
// tokens.
//
// The diagnostic names the constraint that failed and never the values: the
// subject is an account identifier, and the comparison happens in a support
// facing path.
func confirmSubject(expected string, identity Identity) error {
	if strings.TrimSpace(expected) == "" {
		return errors.New("siwc: the selected registration carries no validated subject")
	}
	if strings.TrimSpace(identity.Subject) == "" {
		return fmt.Errorf("%w: the authorized identity carries no validated subject", ErrSubjectMismatch)
	}
	if identity.Subject != expected {
		return ErrSubjectMismatch
	}
	return nil
}
