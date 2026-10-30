package terminal

import "fmt"

// State is a terminal-owner lifecycle state (requirement 7.4).
type State string

const (
	StateOpen           State = "open"
	StateTerminalizing  State = "terminalizing"
	StateWorkPending    State = "work_pending"
	StateSettled        State = "settled"
	StateReleasePending State = "release_pending"
	StateReleased       State = "released"
	StateFailed         State = "failed"
)

// AllStates returns every documented terminal-owner state in stable order.
func AllStates() []State {
	return []State{
		StateOpen, StateTerminalizing, StateWorkPending, StateSettled,
		StateReleasePending, StateReleased, StateFailed,
	}
}

// IsKnown reports whether s is a documented terminal state.
func (s State) IsKnown() bool {
	switch s {
	case StateOpen, StateTerminalizing, StateWorkPending, StateSettled,
		StateReleasePending, StateReleased, StateFailed:
		return true
	}
	return false
}

// Validate returns an error when s is not a known terminal state.
func (s State) Validate() error {
	if !s.IsKnown() {
		return fmt.Errorf("%w: unknown state %q", ErrInvalid, s)
	}
	return nil
}

// IsTerminal reports whether s is a finished owner state.
func (s State) IsTerminal() bool {
	return s == StateReleased || s == StateFailed
}
