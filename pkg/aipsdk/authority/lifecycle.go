package authority

import "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"

// Stage identifies where an authority decision runs in the dual-plane lifecycle.
type Stage string

const (
	StageRequestAdmit   Stage = "request_admit"
	StageRequestSettle  Stage = "request_settle"
	StageRequestRelease Stage = "request_release"
	StageAttemptAdmit   Stage = "attempt_admit"
	StageAttemptSettle  Stage = "attempt_settle"
	StageAttemptRelease Stage = "attempt_release"
	StageLeaseAdmit     Stage = "lease_admit"
	StageLeaseRelease   Stage = "lease_release"
)

// IsKnown reports whether s is a documented authority stage.
func (s Stage) IsKnown() bool {
	switch s {
	case StageRequestAdmit, StageRequestSettle, StageRequestRelease,
		StageAttemptAdmit, StageAttemptSettle, StageAttemptRelease,
		StageLeaseAdmit, StageLeaseRelease:
		return true
	}
	return false
}

// Lifecycle aliases metering scopes so authority callers can stay in one import
// when only documenting stage placement.
type LifecycleScope = metering.LifecycleScope

const (
	LifecycleLogicalRequest   = metering.LifecycleLogicalRequest
	LifecycleBackendAttempt   = metering.LifecycleBackendAttempt
	LifecycleAuxiliaryRequest = metering.LifecycleAuxiliaryRequest
)
