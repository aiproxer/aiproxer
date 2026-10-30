package reasoninghost

import (
	"errors"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/featurehost"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

// BindingID is the stable host binding identity for reasoning host composition.
const BindingID = "reasoning"

// ErrNilBinding is returned when a reasoninghost binding is nil.
var ErrNilBinding = errors.New("reasoninghost: binding is nil")

// Binding envelopes host-provided egress policy and secret matcher resolver
// for standard reasoning feature composition. It implements featurehost.Binding.
type Binding struct {
	EgressPolicies  map[string]EgressPolicy
	MatcherResolver sdk.MatcherResolver
}

// HostBindingID returns the stable identifier for reasoning host bindings.
// It is nil-safe and returns BindingID even when the receiver is typed-nil.
func (b *Binding) HostBindingID() string {
	return BindingID
}

// ValidateHostBinding checks the host binding configuration.
// It is nil-safe and returns ErrNilBinding if the receiver is nil.
func (b *Binding) ValidateHostBinding() error {
	if b == nil {
		return ErrNilBinding
	}
	return nil
}

// Registration returns a featurehost.Registration wrapping this binding.
func (b *Binding) Registration() featurehost.Registration {
	return featurehost.Registration{Binding: b}
}
