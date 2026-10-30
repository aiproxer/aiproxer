package controlplane

import (
	"errors"
	"fmt"

	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// Stable sentinel errors for the control-plane capability (requirement 7.4,
// 9.4). Adapters and HTTP routes map these to safe client/operator shapes
// without leaking raw infrastructure details.
var (
	ErrDisabled          = errors.New("controlplane: capability disabled")
	ErrUnavailable       = errors.New("controlplane: capability unavailable")
	ErrDegraded          = errors.New("controlplane: capability degraded")
	ErrInvalidQuery      = errors.New("controlplane: invalid query")
	ErrTooBroad          = errors.New("controlplane: query too broad")
	ErrUnsupportedFilter = errors.New("controlplane: unsupported filter")
	ErrUnsafeEvidence    = errors.New("controlplane: unsafe evidence")
)

// Classify returns the stable ErrorCode for an error chain, or empty when the
// error is nil or does not match a control-plane sentinel. It is the single
// authoritative mapping from internal errors to the SDK error classification
// (requirement 7.4, 9.4).
func Classify(err error) cp.ErrorCode {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrDisabled):
		return cp.ErrCodeDisabled
	case errors.Is(err, ErrUnavailable):
		return cp.ErrCodeUnavailable
	case errors.Is(err, ErrDegraded):
		return cp.ErrCodeDegraded
	case errors.Is(err, ErrInvalidQuery):
		return cp.ErrCodeInvalidQuery
	case errors.Is(err, ErrTooBroad):
		return cp.ErrCodeTooBroad
	case errors.Is(err, ErrUnsupportedFilter):
		return cp.ErrCodeUnsupportedFilter
	case errors.Is(err, ErrUnsafeEvidence):
		return cp.ErrCodeUnsafeEvidence
	}
	return ""
}

// unsupportedFilterError carries the named fields a query requested that
// recorded evidence cannot apply, so HTTP and feature consumers can report
// them explicitly rather than silently widening the query (requirement 2.5,
// 8.6, 9.4).
type unsupportedFilterError struct {
	fields []string
}

func (e *unsupportedFilterError) Error() string {
	return fmt.Sprintf("controlplane: unsupported filter(s): %v", e.fields)
}

func (e *unsupportedFilterError) Unwrap() error { return ErrUnsupportedFilter }

// NewUnsupportedFilterError wraps ErrUnsupportedFilter with the named filter
// fields that recorded evidence cannot apply.
func NewUnsupportedFilterError(fields []string) error {
	if len(fields) == 0 {
		return ErrUnsupportedFilter
	}
	clone := make([]string, len(fields))
	copy(clone, fields)
	return &unsupportedFilterError{fields: clone}
}

// UnsupportedFilterFields returns the named fields carried by an unsupported
// filter error, or nil when the error is not an unsupported filter error.
func UnsupportedFilterFields(err error) []string {
	if target, ok := errors.AsType[*unsupportedFilterError](err); ok {
		return append([]string(nil), target.fields...)
	}
	return nil
}
