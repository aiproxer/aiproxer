package aipapi

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrPolicyDenied is the stable root for policy denials before backend output and for
// active-stream policy denials after output (requirements 5.1, 5.6). It is distinct
// from capability, session, backend, auth, and internal error roots.
var ErrPolicyDenied = errors.New("aipapi: policy denied")

// ErrPolicyFailure is the stable root for policy decision provider failures handled
// through configured fail-open/fail-closed behavior (requirements 6.1, 6.5, 7.2).
var ErrPolicyFailure = errors.New("aipapi: policy failure")

// ErrPolicyMalformed is the stable root for malformed policy decisions: unknown
// stages, unknown outcomes, unknown effects, or illegal outcome/effect pairs
// (requirements 1.5, 6.6, 7.2).
var ErrPolicyMalformed = errors.New("aipapi: malformed policy decision")

// PolicyDecisionErrorKind identifies which stable policy error root a
// PolicyDecisionError wraps. It is the only field besides ClientCategory and
// ClientMessage intended for frontend classification (requirement 5.6).
type PolicyDecisionErrorKind string

const (
	// PolicyErrorKindDenied wraps ErrPolicyDenied.
	PolicyErrorKindDenied PolicyDecisionErrorKind = "policy_denied"
	// PolicyErrorKindFailure wraps ErrPolicyFailure.
	PolicyErrorKindFailure PolicyDecisionErrorKind = "policy_failure"
	// PolicyErrorKindMalformed wraps ErrPolicyMalformed.
	PolicyErrorKindMalformed PolicyDecisionErrorKind = "policy_malformed"
)

// PolicyDecisionError is the stable executor error root for policy denial, failure,
// and malformed decisions (requirements 5.1, 5.4, 5.5, 5.6, 6.1, 6.5, 6.6, 7.2).
//
// Only Stage, ProviderID, ReasonCode, ClientCategory, ClientMessage, and Kind are
// client-safe. Cause is preserved for operator/diagnostic use and must not be rendered
// to clients verbatim when it may carry raw prompts, raw backend payloads, secrets,
// or unsafe claim values.
type PolicyDecisionError struct {
	Kind           PolicyDecisionErrorKind
	Stage          string
	ProviderID     string
	ReasonCode     string
	ClientCategory string
	ClientMessage  string
	Cause          error
}

// Error returns a stable, client-safe message. It never includes Cause text, raw
// prompts, backend payloads, secrets, or unsafe claim values.
func (e *PolicyDecisionError) Error() string {
	if e == nil {
		return ""
	}
	msg := e.ClientMessage
	if msg == "" {
		switch e.Kind {
		case PolicyErrorKindDenied:
			msg = "request denied by policy"
		case PolicyErrorKindFailure:
			msg = "policy decision unavailable"
		case PolicyErrorKindMalformed:
			msg = "policy decision was malformed"
		default:
			msg = "policy decision error"
		}
	}
	return msg
}

// Unwrap returns the stable root error for the kind so errors.Is and errors.As
// classify policy denials, failures, and malformed decisions separately from
// capability, session, backend, auth, and internal errors (requirement 5.6). When a
// cause is present it is also exposed so callers can inspect the underlying failure
// through errors.Is/errors.As without rendering Cause text in Error().
func (e *PolicyDecisionError) Unwrap() []error {
	if e == nil {
		return nil
	}
	root := error(nil)
	switch e.Kind {
	case PolicyErrorKindDenied:
		root = ErrPolicyDenied
	case PolicyErrorKindFailure:
		root = ErrPolicyFailure
	case PolicyErrorKindMalformed:
		root = ErrPolicyMalformed
	}
	if e.Cause == nil {
		if root == nil {
			return nil
		}
		return []error{root}
	}
	if root == nil {
		return []error{e.Cause}
	}
	return []error{root, e.Cause}
}

// MaxClientMessageBytes is the wire-safe byte bound for PolicyDecisionError.ClientMessage
// (design §Evidence Normalization Contract; requirements 5.4, 7.7). It is the single
// source of truth shared with pkg/aipsdk/policydecision so the wire and evidence paths
// cannot diverge.
const MaxClientMessageBytes = 256

// NormalizeClientMessage returns a wire-safe, bounded copy of s: newline/tab and other
// Unicode control characters are removed, the result is trimmed, and it is truncated to
// MaxClientMessageBytes on a UTF-8 rune boundary. Empty input returns "".
//
// It enforces the Decision Contract invariant that ClientMessage is safe for wire use
// (design §Decision Contract) at construction, and is reused by the evidence normalizer
// so observers/logs and client-facing messages apply identical bounds.
func NormalizeClientMessage(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	trimmed := strings.TrimSpace(b.String())
	if len(trimmed) <= MaxClientMessageBytes {
		return trimmed
	}
	cut := MaxClientMessageBytes
	for cut > 0 && !utf8.RuneStart(trimmed[cut]) {
		cut--
	}
	return trimmed[:cut]
}

func newPolicyDecisionError(kind PolicyDecisionErrorKind, stage, providerID, reasonCode, clientCategory, clientMessage string, cause error) *PolicyDecisionError {
	return &PolicyDecisionError{
		Kind:           kind,
		Stage:          stage,
		ProviderID:     providerID,
		ReasonCode:     reasonCode,
		ClientCategory: clientCategory,
		ClientMessage:  NormalizeClientMessage(clientMessage),
		Cause:          cause,
	}
}

// NewPolicyDeniedError returns a stable denial error. clientMessage and clientCategory
// are the only fields intended for frontend rendering; cause is preserved for
// diagnostics only. clientMessage is normalized to the wire-safe bound at construction.
func NewPolicyDeniedError(stage, providerID, reasonCode, clientCategory, clientMessage string, cause error) *PolicyDecisionError {
	return newPolicyDecisionError(PolicyErrorKindDenied, stage, providerID, reasonCode, clientCategory, clientMessage, cause)
}

// NewPolicyFailureError returns a stable policy failure error for fail-closed
// provider failures (requirement 6.1). clientMessage is normalized to the wire-safe
// bound at construction.
func NewPolicyFailureError(stage, providerID, reasonCode, clientCategory, clientMessage string, cause error) *PolicyDecisionError {
	return newPolicyDecisionError(PolicyErrorKindFailure, stage, providerID, reasonCode, clientCategory, clientMessage, cause)
}

// NewPolicyMalformedError returns a stable malformed-policy error for unknown
// stages, unknown outcomes, unknown effects, or illegal outcome/effect pairs
// (requirements 1.5, 6.6). clientMessage is normalized to the wire-safe bound at
// construction.
func NewPolicyMalformedError(stage, providerID, reasonCode, clientCategory, clientMessage string, cause error) *PolicyDecisionError {
	return newPolicyDecisionError(PolicyErrorKindMalformed, stage, providerID, reasonCode, clientCategory, clientMessage, cause)
}

// IsPolicyDecisionError reports whether err is or wraps a *PolicyDecisionError or one
// of the stable policy error roots (requirement 5.6, 7.2).
func IsPolicyDecisionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrPolicyDenied) || errors.Is(err, ErrPolicyFailure) || errors.Is(err, ErrPolicyMalformed) {
		return true
	}
	var pde *PolicyDecisionError
	return errors.As(err, &pde)
}

// IsPolicyDenied reports whether err is or wraps a policy denial.
func IsPolicyDenied(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrPolicyDenied) {
		return true
	}
	if pde, ok := errors.AsType[*PolicyDecisionError](err); ok {
		return pde.Kind == PolicyErrorKindDenied
	}
	return false
}

// IsPolicyFailure reports whether err is or wraps a policy failure.
func IsPolicyFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrPolicyFailure) {
		return true
	}
	if pde, ok := errors.AsType[*PolicyDecisionError](err); ok {
		return pde.Kind == PolicyErrorKindFailure
	}
	return false
}

// IsPolicyMalformed reports whether err is or wraps a malformed policy decision.
func IsPolicyMalformed(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrPolicyMalformed) {
		return true
	}
	if pde, ok := errors.AsType[*PolicyDecisionError](err); ok {
		return pde.Kind == PolicyErrorKindMalformed
	}
	return false
}

// PolicyDecisionErrorKindOf returns the stable kind for err when it wraps a
// *PolicyDecisionError, otherwise the empty kind.
func PolicyDecisionErrorKindOf(err error) PolicyDecisionErrorKind {
	if pde, ok := errors.AsType[*PolicyDecisionError](err); ok {
		return pde.Kind
	}
	return ""
}

// PolicyDecisionErrorFrom returns the *PolicyDecisionError wrapped by err, or nil.
func PolicyDecisionErrorFrom(err error) *PolicyDecisionError {
	if pde, ok := errors.AsType[*PolicyDecisionError](err); ok {
		return pde
	}
	return nil
}
