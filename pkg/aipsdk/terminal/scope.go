package terminal

import "fmt"

// Scope identifies whether a terminal owner covers a logical request or a
// backend attempt (design: per-attempt owners may finish before request owner).
type Scope string

const (
	ScopeRequest Scope = "request"
	ScopeAttempt Scope = "attempt"
)

// IsKnown reports whether s is a documented terminal scope.
func (s Scope) IsKnown() bool {
	switch s {
	case ScopeRequest, ScopeAttempt:
		return true
	}
	return false
}

// Validate returns an error when s is not a known terminal scope.
func (s Scope) Validate() error {
	if !s.IsKnown() {
		return fmt.Errorf("%w: unknown scope %q", ErrInvalid, s)
	}
	return nil
}
