package secretguardhost

import (
	"errors"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/featurehost"
)

// BindingID is the stable host binding identity for secret guard host composition.
const BindingID = "secret_guard"

// ErrNilBinding is returned when a secretguardhost binding is nil.
var ErrNilBinding = errors.New("secretguardhost: binding is nil")

// Environment provides environment variable access for secret catalog loading.
type Environment interface {
	Lookup(name string) (value string, ok bool)
	Snapshot() []string
}

// SingleUserOptions configures single-user catalog and matcher options.
type SingleUserOptions struct {
	IncludePopularEnv bool
	IncludeEnv        []string
	ExcludeEnv        []string
	MinSecretBytes    int
	Matcher           MatcherOptions
	MatcherConfigured bool
}

// MatcherOptions controls redaction presentation for the composed static matcher.
type MatcherOptions struct {
	PreserveKnownPrefixes bool
	MaskByte              byte
}

// Binding envelopes host-provided secret-guard configuration for standard featurehost composition.
// It implements featurehost.Binding.
type Binding struct {
	Environment Environment
	SingleUser  SingleUserOptions
}

// HostBindingID returns the stable identifier for secret guard host bindings.
func (b *Binding) HostBindingID() string {
	return BindingID
}

// ValidateHostBinding checks the host binding configuration.
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
