package app

import (
	"errors"
	"fmt"
)

// Sentinel application errors.
var (
	ErrNotFound      = errors.New("concurrencyauthority: lease not found")
	ErrUnavailable   = errors.New("concurrencyauthority: unavailable")
	ErrInvalidInput  = errors.New("concurrencyauthority: invalid input")
	ErrSnapshotEmpty = errors.New("concurrencyauthority: rule snapshot unavailable")
)

// Error wraps an operation failure without leaking store details.
type Error struct {
	op  string
	err error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.err == nil {
		return "concurrencyauthority " + e.op
	}
	return fmt.Sprintf("concurrencyauthority %s: %v", e.op, e.err)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// WrapError annotates err with an operation name.
func WrapError(op string, err error) error {
	if err == nil {
		return nil
	}
	return &Error{op: op, err: err}
}
