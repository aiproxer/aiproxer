package terminal

import "slices"

// AllowedScopes returns the terminal owner scopes that may claim with c.
//
// Conservative mapping from design Single Terminal Ownership / D8:
//   - request-only: frontend encoder failure, completion-gate replacement
//   - attempt-only: parallel loser, swallowed attempt, pre-backend denial, backend-open failure
//   - both: normal finish, partial/error, cancel, close, timeout, panic, EOF
func (c Command) AllowedScopes() []Scope {
	switch c {
	case CommandFrontendEncoderFailure, CommandGateReplacement:
		return []Scope{ScopeRequest}
	case CommandParallelLoser, CommandSwallowedAttempt,
		CommandPreBackendDenial, CommandBackendOpenFailure:
		return []Scope{ScopeAttempt}
	case CommandNormalFinish, CommandPartialError, CommandCancel, CommandClose,
		CommandTimeout, CommandPanic, CommandEOF:
		return []Scope{ScopeRequest, ScopeAttempt}
	default:
		return nil
	}
}

// AllowsScope reports whether c may compete on the given owner scope.
func (c Command) AllowsScope(s Scope) bool {
	return slices.Contains(c.AllowedScopes(), s)
}
