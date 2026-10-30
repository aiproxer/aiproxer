package terminal

import "errors"

// Sentinel errors for terminal and terminal-work domain contracts.
var (
	// ErrInvalid indicates an unknown or malformed contract value.
	ErrInvalid = errors.New("terminal: invalid")

	// ErrInvalidTransition indicates a disallowed state-machine transition.
	ErrInvalidTransition = errors.New("terminal: invalid transition")

	// ErrConflict indicates a competing command lost the CAS claim and must
	// observe the winner outcome without re-running effects (requirement 7.3).
	ErrConflict = errors.New("terminal: conflict")

	// ErrOutputCommitted rejects retry/replacement after visible output
	// (requirement 7.5, design D13).
	ErrOutputCommitted = errors.New("terminal: output committed")

	// ErrScopeMismatch rejects a command that is not legal for the owner scope
	// (request-only vs attempt-only terminal commands).
	ErrScopeMismatch = errors.New("terminal: scope mismatch")

	// ErrClaimLeaseHeld indicates another worker still holds a valid claim lease.
	ErrClaimLeaseHeld = errors.New("terminal: claim lease held")

	// ErrNotDue indicates retry work is not yet eligible for claim.
	ErrNotDue = errors.New("terminal: not due")

	// ErrPermanent indicates a non-retryable work failure that must quarantine.
	ErrPermanent = errors.New("terminal: permanent")
)
