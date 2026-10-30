// Package aipapidenial maps secure-session domain errors to canonical [pkg/aipapi] session denials.
package aipapidenial

import (
	"errors"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// MapToSessionDenial maps secure-session domain errors to canonical aipapi session denials.
// Callers classify with errors.As(*aipapi.SessionDenialError) or aipapi.SessionDenialPublicCode.
func MapToSessionDenial(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, domain.ErrMissingPrincipal):
		return aipapi.NewSessionDenialMissingPrincipal("secure_session: missing principal")
	case errors.Is(err, domain.ErrSessionNotFound):
		return aipapi.NewSessionDenialInvalidAuthority("secure_session: session not found")
	case errors.Is(err, domain.ErrInvalidResumeToken):
		return aipapi.NewSessionDenialInvalidAuthority("secure_session: invalid resume token")
	case errors.Is(err, domain.ErrOwnerMismatch):
		return aipapi.NewSessionDenialOwnerMismatch("secure_session: owner mismatch")
	case errors.Is(err, domain.ErrResumeExpired):
		return aipapi.NewSessionDenialResumeExpired("secure_session: resume expired")
	case errors.Is(err, domain.ErrSessionQuarantined):
		return aipapi.NewSessionDenialQuarantined("secure_session: session quarantined")
	case errors.Is(err, domain.ErrWorkspaceDenied):
		return aipapi.NewSessionDenialWorkspace("secure_session: workspace denied")
	case errors.Is(err, domain.ErrPolicyUnavailable):
		return aipapi.NewSessionDenialPolicyUnavailable("secure_session: policy unavailable")
	case errors.Is(err, domain.ErrWorkspaceUnresolved):
		return aipapi.NewSessionDenialWorkspace("secure_session: workspace could not be resolved")
	case errors.Is(err, domain.ErrStorageUnavailable):
		return aipapi.NewSessionDenialStorageUnavailable("secure_session: storage unavailable")
	case errors.Is(err, domain.ErrMandatoryAuditFailure):
		return aipapi.NewSessionDenialMandatoryAuditFailure("secure_session: mandatory audit failure")
	case errors.Is(err, domain.ErrDuplicateSessionID):
		return aipapi.NewSessionDenialInvalidAuthority("secure_session: duplicate session id")
	case errors.Is(err, domain.ErrDuplicateFingerprint):
		return aipapi.NewSessionDenialInvalidAuthority("secure_session: duplicate resume fingerprint")
	case errors.Is(err, domain.ErrTranscriptDisabled):
		return aipapi.NewSessionDenialInvalidAuthority("secure_session: transcript disabled for session")
	default:
		return aipapi.NewSessionDenialInvalidAuthority("secure_session: unexpected denial")
	}
}
