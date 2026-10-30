//go:build darwin

package processhost

import (
	"context"
	"os"
)

// Listen fails closed on Darwin until private UDS peer-cred/PID binding can be
// proven with an approved exact-launch profile.
func (PlatformChannel) Listen(context.Context, uint64) (Listener, []*os.File, error) {
	return nil, nil, ReasonUnsupportedChannel
}
