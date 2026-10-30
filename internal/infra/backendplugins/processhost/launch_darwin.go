//go:build darwin

package processhost

import (
	"context"
)

// Launch fails closed on Darwin until protected staging path launch can prove
// identity preservation through process creation without substitution.
func (PlatformLauncher) Launch(context.Context, LaunchSpec) (Process, error) {
	return nil, ReasonUnsupportedBinding
}
