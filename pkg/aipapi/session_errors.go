package aipapi

import (
	"errors"
)

// ErrSessionDenial is the stable root for secure-session and resume denials before backend work.
var ErrSessionDenial = errors.New("aipapi: session denied")

// SessionDenialCode is a stable machine-readable category for frontends and metrics (not user-facing prose).
type SessionDenialCode string

const (
	SessionDeniedMissingPrincipal      SessionDenialCode = "session_denied_missing_principal"
	SessionDeniedInvalidAuthority      SessionDenialCode = "session_denied_invalid_authority"
	SessionDeniedOwnerMismatch         SessionDenialCode = "session_denied_owner_mismatch"
	SessionDeniedResumeExpired         SessionDenialCode = "session_denied_resume_expired"
	SessionDeniedQuarantined           SessionDenialCode = "session_denied_quarantined"
	SessionDeniedWorkspace             SessionDenialCode = "session_denied_workspace"
	SessionDeniedPolicyUnavailable     SessionDenialCode = "session_denied_policy_unavailable"
	SessionDeniedStorageUnavailable    SessionDenialCode = "session_denied_storage_unavailable"
	SessionDeniedMandatoryAuditFailure SessionDenialCode = "session_denied_mandatory_audit_failure"
)

// SessionDenialError is a typed session denial with a public-safe [SessionDenialError.Error] string
// and optional internal diagnostics that must not appear in Error().
type SessionDenialError struct {
	code           SessionDenialCode
	publicMessage  string
	internalReason string
}

func (e *SessionDenialError) Error() string {
	if e == nil {
		return ""
	}
	if e.publicMessage != "" {
		return e.publicMessage
	}
	return "session denied (" + string(e.code) + ")"
}

func (e *SessionDenialError) Unwrap() error { return ErrSessionDenial }

// Code returns the stable denial category.
func (e *SessionDenialError) Code() SessionDenialCode {
	if e == nil {
		return ""
	}
	return e.code
}

// InternalReason returns operator/diagnostic detail; it is not included in [SessionDenialError.Error].
func (e *SessionDenialError) InternalReason() string {
	if e == nil {
		return ""
	}
	return e.internalReason
}

// PublicMessage returns the client-safe message used by [SessionDenialError.Error] when set.
func (e *SessionDenialError) PublicMessage() string {
	if e == nil {
		return ""
	}
	return e.publicMessage
}

// IsSessionDenial reports whether err is or wraps a *SessionDenialError or [ErrSessionDenial].
func IsSessionDenial(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrSessionDenial) {
		return true
	}
	var sd *SessionDenialError
	return errors.As(err, &sd)
}

// SessionDenialPublicCode returns the stable [SessionDenialCode] for err when it wraps *SessionDenialError.
func SessionDenialPublicCode(err error) string {
	if sd, ok := errors.AsType[*SessionDenialError](err); ok {
		return string(sd.code)
	}
	return ""
}

func newSessionDenial(code SessionDenialCode, public string, internal string) *SessionDenialError {
	return &SessionDenialError{code: code, publicMessage: public, internalReason: internal}
}

// NewSessionDenialMissingPrincipal returns a denial when no trustworthy authenticated principal is available.
func NewSessionDenialMissingPrincipal(internalReason string) error {
	return newSessionDenial(SessionDeniedMissingPrincipal,
		"session requires authenticated identity", internalReason)
}

// NewSessionDenialInvalidAuthority returns a denial for malformed, unrecognized, or non-proxy-issued resume proof.
func NewSessionDenialInvalidAuthority(internalReason string) error {
	return newSessionDenial(SessionDeniedInvalidAuthority,
		"session could not be resumed", internalReason)
}

// NewSessionDenialOwnerMismatch returns a denial when the session owner does not match the authenticated user.
func NewSessionDenialOwnerMismatch(internalReason string) error {
	return newSessionDenial(SessionDeniedOwnerMismatch,
		"session could not be resumed", internalReason)
}

// NewSessionDenialResumeExpired returns a denial when resume is outside the allowed window.
func NewSessionDenialResumeExpired(internalReason string) error {
	return newSessionDenial(SessionDeniedResumeExpired,
		"session can no longer be resumed", internalReason)
}

// NewSessionDenialQuarantined returns a denial when the session was quarantined and cannot be reused.
func NewSessionDenialQuarantined(internalReason string) error {
	return newSessionDenial(SessionDeniedQuarantined,
		"session quarantined; start a new session", internalReason)
}

// NewSessionDenialWorkspace returns a denial when workspace policy rejects the session.
func NewSessionDenialWorkspace(internalReason string) error {
	return newSessionDenial(SessionDeniedWorkspace,
		"session is not allowed for this workspace", internalReason)
}

// NewSessionDenialPolicyUnavailable returns a denial when required per-session policy metadata cannot be loaded.
func NewSessionDenialPolicyUnavailable(internalReason string) error {
	return newSessionDenial(SessionDeniedPolicyUnavailable,
		"session policy is temporarily unavailable", internalReason)
}

// NewSessionDenialStorageUnavailable returns a denial when durable session storage is required but unavailable.
func NewSessionDenialStorageUnavailable(internalReason string) error {
	return newSessionDenial(SessionDeniedStorageUnavailable,
		"session storage is temporarily unavailable", internalReason)
}

// NewSessionDenialMandatoryAuditFailure returns a denial when mandatory audit prerequisites fail before output.
func NewSessionDenialMandatoryAuditFailure(internalReason string) error {
	return newSessionDenial(SessionDeniedMandatoryAuditFailure,
		"session could not be recorded as required by policy", internalReason)
}
