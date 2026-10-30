package domain

import "errors"

// Domain errors for secure-session policy and storage.
// The composition root sets runtime.Executor.SessionDenialMapper (e.g. aipapidenial.MapToSessionDenial) to
// translate these to aipapi session denials for clients; domain and app return these sentinels unchanged.
var (
	ErrSessionNotFound       = errors.New("securesession: session not found")
	ErrDuplicateSessionID    = errors.New("securesession: duplicate session id")
	ErrDuplicateFingerprint  = errors.New("securesession: duplicate resume fingerprint")
	ErrInvalidResumeToken    = errors.New("securesession: invalid resume token")
	ErrOwnerMismatch         = errors.New("securesession: owner mismatch")
	ErrResumeExpired         = errors.New("securesession: resume window expired")
	ErrWorkspaceDenied       = errors.New("securesession: workspace denied")
	ErrPolicyUnavailable     = errors.New("securesession: policy unavailable")
	ErrStorageUnavailable    = errors.New("securesession: storage unavailable")
	ErrMandatoryAuditFailure = errors.New("securesession: mandatory audit failure")
	ErrMissingPrincipal      = errors.New("securesession: missing principal")
	ErrTranscriptDisabled    = errors.New("securesession: transcript capture disabled for session")
	// ErrWorkspaceUnresolved is returned when workspace resolution failed under fail-closed policy
	// (Req 11.6: do not fail open into an ambiguous empty workspace for secure-session turns).
	ErrWorkspaceUnresolved = errors.New("securesession: workspace could not be resolved")
	// ErrSessionQuarantined is returned when a quarantined session is reused (resume or pre-dispatch).
	// Clients must start a new session; the mapped denial is protocol-safe and secret-free.
	ErrSessionQuarantined = errors.New("securesession: session quarantined")
	// ErrInvalidQuarantineInput is returned when a quarantine transition request is incomplete or invalid.
	ErrInvalidQuarantineInput = errors.New("securesession: invalid quarantine input")
	// ErrQuarantineUnimplemented is a Phase-1 compile stub sentinel returned by stores that have
	// not yet implemented Store.Quarantine (Phase 5). Production paths must not convert this to allow.
	ErrQuarantineUnimplemented = errors.New("securesession: Quarantine not implemented")
)
