package featurehost

import (
	"errors"
	"fmt"
	"strings"
)

// MaxBindingIDLength defines the upper bound for a host binding identifier.
const MaxBindingIDLength = 128

// Sentinels returned by Validate.
var (
	ErrNilBinding       = errors.New("featurehost: registration binding is nil")
	ErrEmptyBindingID   = errors.New("featurehost: host binding ID is empty")
	ErrBindingIDTooLong = errors.New("featurehost: host binding ID exceeds maximum length")
	ErrDuplicateBinding = errors.New("featurehost: duplicate host binding ID")
	ErrInvalidBinding   = errors.New("featurehost: invalid host binding")
)

// Binding is the startup-only interface implemented by trusted host bindings.
// Concrete implementations must be typed, self-contained, and nil-safe.
type Binding interface {
	HostBindingID() string
	ValidateHostBinding() error
}

// Registration envelopes a trusted host binding for host-to-feature startup composition.
type Registration struct {
	Binding Binding
}

// Validate verifies a slice of host-feature registrations:
// - returns ErrNilBinding if any registration has a nil binding;
// - returns ErrEmptyBindingID if HostBindingID is empty or only whitespace;
// - returns ErrBindingIDTooLong if HostBindingID exceeds MaxBindingIDLength;
// - returns ErrInvalidBinding wrapping the underlying error if ValidateHostBinding fails;
// - returns ErrDuplicateBinding if more than one registration shares the same HostBindingID.
//
// Slice validation is defensive and deterministic.
func Validate(regs []Registration) error {
	if len(regs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(regs))
	for i, reg := range regs {
		if reg.Binding == nil {
			return fmt.Errorf("%w: index %d", ErrNilBinding, i)
		}
		// Validate host binding first so typed-nil receivers can report
		// validation failure safely before any further inspection.
		if err := reg.Binding.ValidateHostBinding(); err != nil {
			return fmt.Errorf("%w at index %d: %w", ErrInvalidBinding, i, err)
		}
		id := strings.TrimSpace(reg.Binding.HostBindingID())
		if id == "" {
			return fmt.Errorf("%w: index %d", ErrEmptyBindingID, i)
		}
		if len(id) > MaxBindingIDLength {
			return fmt.Errorf("%w %q: index %d", ErrBindingIDTooLong, id, i)
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("%w %q: index %d", ErrDuplicateBinding, id, i)
		}
		seen[id] = struct{}{}
	}
	return nil
}
