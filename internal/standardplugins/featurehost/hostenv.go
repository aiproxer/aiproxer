package featurehost

import (
	"slices"
	"strings"

	sdkfeaturehost "github.com/aiproxer/aiproxer/pkg/aipsdk/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguardhost"
)

// HostEnvironment is a generic process-environment capability for standard
// feature composition. Concrete interpretation stays in the secret-guard
// binding below; generic composition forwards it untouched.
type HostEnvironment interface {
	Lookup(name string) (value string, ok bool)
	Snapshot() []string
}

// withDefaultEnvBinding appends a default env-derived secret-guard host
// binding when env is present and no explicit binding is registered.
// Explicit registrations win wholesale and are never duplicated (R1 dedupe).
func withDefaultEnvBinding(regs []sdkfeaturehost.Registration, env HostEnvironment) []sdkfeaturehost.Registration {
	if env == nil {
		return regs
	}
	for _, reg := range regs {
		if reg.Binding == nil {
			continue
		}
		if strings.TrimSpace(reg.Binding.HostBindingID()) == secretguardhost.BindingID {
			return regs
		}
	}
	return append(slices.Clone(regs), (&secretguardhost.Binding{Environment: env}).Registration())
}
