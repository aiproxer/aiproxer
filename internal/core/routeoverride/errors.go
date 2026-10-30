package routeoverride

import (
	"errors"
)

var (
	// ErrNotFound reports that the target A-leg does not exist.
	ErrNotFound = errors.New("routeoverride: a-leg not found")
	// ErrInvalidSelector reports a selector that fails normalization or bounds.
	ErrInvalidSelector = errors.New("routeoverride: invalid selector")
	// ErrRevisionExhausted reports that the next revision would overflow int64.
	ErrRevisionExhausted = errors.New("routeoverride: revision exhausted")
	// ErrUnavailable reports that the override store cannot complete the operation.
	ErrUnavailable = errors.New("routeoverride: store unavailable")
	// ErrNotImplemented reports that a standard continuity adapter has not yet
	// gained the optional override capability.
	ErrNotImplemented = errors.New("routeoverride: store capability is not implemented")
)
