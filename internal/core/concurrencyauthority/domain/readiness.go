package domain

// ReadinessState classifies concurrency-authority readiness.
type ReadinessState string

const (
	ReadinessStateReady       ReadinessState = "ready"
	ReadinessStateDegraded    ReadinessState = "degraded"
	ReadinessStateUnavailable ReadinessState = "unavailable"
	ReadinessStateDisabled    ReadinessState = "disabled"
)

// IsKnown reports whether s is a documented readiness state.
func (s ReadinessState) IsKnown() bool {
	switch s {
	case ReadinessStateReady, ReadinessStateDegraded, ReadinessStateUnavailable, ReadinessStateDisabled:
		return true
	default:
		return false
	}
}

// Readiness reports whether the concurrency plane can enforce leases.
type Readiness struct {
	State  ReadinessState
	Reason string
}
