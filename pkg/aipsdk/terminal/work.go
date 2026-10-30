package terminal

import "fmt"

// WorkKind identifies one independently idempotent terminal action
// (requirement 8.1).
type WorkKind string

const (
	WorkKindAppendFact              WorkKind = "append_fact"
	WorkKindSettleRequestProvider   WorkKind = "settle_request_provider"
	WorkKindReleaseRequestProvider  WorkKind = "release_request_provider"
	WorkKindSettleAttemptProvider   WorkKind = "settle_attempt_provider"
	WorkKindReleaseAttemptProvider  WorkKind = "release_attempt_provider"
	WorkKindCompensateProvider      WorkKind = "compensate_provider"
	WorkKindReleaseLeaseSet         WorkKind = "release_lease_set"
	WorkKindAuthoritativeCorrection WorkKind = "authoritative_correction"
)

// AllWorkKinds returns every documented work kind in stable order.
func AllWorkKinds() []WorkKind {
	return []WorkKind{
		WorkKindAppendFact,
		WorkKindSettleRequestProvider,
		WorkKindReleaseRequestProvider,
		WorkKindSettleAttemptProvider,
		WorkKindReleaseAttemptProvider,
		WorkKindCompensateProvider,
		WorkKindReleaseLeaseSet,
		WorkKindAuthoritativeCorrection,
	}
}

// String returns the stable wire/token form of the work kind.
func (k WorkKind) String() string { return string(k) }

// IsKnown reports whether k is a documented work kind.
func (k WorkKind) IsKnown() bool {
	switch k {
	case WorkKindAppendFact, WorkKindSettleRequestProvider, WorkKindReleaseRequestProvider,
		WorkKindSettleAttemptProvider, WorkKindReleaseAttemptProvider, WorkKindCompensateProvider,
		WorkKindReleaseLeaseSet, WorkKindAuthoritativeCorrection:
		return true
	}
	return false
}

// Validate returns an error when k is not a known work kind.
func (k WorkKind) Validate() error {
	if !k.IsKnown() {
		return fmt.Errorf("%w: unknown work kind %q", ErrInvalid, k)
	}
	return nil
}

// RequiresProvider reports whether k is scoped to a stable provider ID.
func (k WorkKind) RequiresProvider() bool {
	switch k {
	case WorkKindSettleRequestProvider, WorkKindReleaseRequestProvider,
		WorkKindSettleAttemptProvider, WorkKindReleaseAttemptProvider,
		WorkKindCompensateProvider:
		return true
	}
	return false
}

// WorkState is a durable terminal-work lifecycle state (requirement 8.2).
// Intent is the first durable record before pending claim eligibility.
type WorkState string

const (
	WorkStateIntent      WorkState = "intent"
	WorkStatePending     WorkState = "pending"
	WorkStateClaimed     WorkState = "claimed"
	WorkStateCompleted   WorkState = "completed"
	WorkStateRetry       WorkState = "retry"
	WorkStateQuarantined WorkState = "quarantined"
)

// AllWorkStates returns every documented work state in stable order.
func AllWorkStates() []WorkState {
	return []WorkState{
		WorkStateIntent,
		WorkStatePending,
		WorkStateClaimed,
		WorkStateCompleted,
		WorkStateRetry,
		WorkStateQuarantined,
	}
}

// IsKnown reports whether s is a documented work state.
func (s WorkState) IsKnown() bool {
	switch s {
	case WorkStateIntent, WorkStatePending, WorkStateClaimed, WorkStateCompleted,
		WorkStateRetry, WorkStateQuarantined:
		return true
	}
	return false
}

// Validate returns an error when s is not a known work state.
func (s WorkState) Validate() error {
	if !s.IsKnown() {
		return fmt.Errorf("%w: unknown work state %q", ErrInvalid, s)
	}
	return nil
}

// IsTerminal reports whether s is a finished work state.
func (s WorkState) IsTerminal() bool {
	return s == WorkStateCompleted || s == WorkStateQuarantined
}
