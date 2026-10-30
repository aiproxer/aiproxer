package config

import (
	"github.com/aiproxer/aiproxer/internal/core/accessmode"
)

// IsExplicitLoopbackListenAddress reports whether raw is a conservative loopback bind
// (127/8, ::1, or localhost), not an all-interfaces or non-loopback address.
func IsExplicitLoopbackListenAddress(raw string) bool {
	c, err := accessmode.ClassifyListenAddress(raw)
	if err != nil {
		return false
	}
	return c.Surface == accessmode.SurfaceLoopback
}
