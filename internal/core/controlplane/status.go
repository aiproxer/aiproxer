package controlplane

import (
	"sync"
	"time"

	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// Status tracks the operator-visible capability state for the control-plane
// capability (requirement 7.1, 7.2, 7.3). It stores only bounded safe reason
// codes and timestamps, never raw infrastructure errors.
//
// State transition rules enforced here:
//   - Disabled always wins: once disabled, failure reports do not revive or
//     degrade the capability until SetReady/SetUnavailable is called.
//   - RecordFailure on a ready capability transitions to degraded with the
//     supplied bounded reason code and last failure time.
//   - SetUnavailable transitions to unavailable regardless of prior degraded
//     state, because a missing backing capability is more severe than a
//     partial failure.
type Status struct {
	noCopy  noCopy
	mu      sync.RWMutex
	current cp.CapabilityStatus
}

// noCopy may be embedded in structs that must not be copied after first use.
// The Lock/Unlock methods make go vet's copylocks check flag accidental copies.
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}

// NewStatus returns a Status seeded with the supplied initial snapshot.
func NewStatus(initial cp.CapabilityStatus) *Status {
	return &Status{current: initial}
}

// Snapshot returns a defensive copy of the current capability status.
func (s *Status) Snapshot() cp.CapabilityStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// SetReady transitions the capability to ready with the supplied recording
// policy and clears any prior failure reason.
func (s *Status) SetReady(policy cp.RecordingPolicy, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current.State = cp.CapabilityReady
	s.current.Reason = cp.ReasonNone
	s.current.LastFailureAt = time.Time{}
	s.current.RecordingPolicy = policy
}

// RecordFailure transitions a ready capability to degraded with the supplied
// bounded reason code and last failure time. It does not override disabled
// or unavailable states (requirement 7.5).
func (s *Status) RecordFailure(reason cp.ReasonCode, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current.State == cp.CapabilityDisabled || s.current.State == cp.CapabilityUnavailable {
		return
	}
	s.current.State = cp.CapabilityDegraded
	s.current.Reason = reason
	s.current.LastFailureAt = at
}

// SetUnavailable transitions the capability to unavailable with the supplied
// bounded reason code and last failure time.
func (s *Status) SetUnavailable(reason cp.ReasonCode, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current.State = cp.CapabilityUnavailable
	s.current.Reason = reason
	s.current.LastFailureAt = at
}

// Disable transitions the capability to disabled and clears prior failure
// reason. Disabled is terminal until an explicit SetReady/SetUnavailable call.
func (s *Status) Disable(at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current.State = cp.CapabilityDisabled
	s.current.Reason = cp.ReasonDisabled
	s.current.LastFailureAt = at
	s.current.RecordingPolicy = cp.RecordingDisabled
}
