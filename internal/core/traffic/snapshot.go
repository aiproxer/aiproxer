package traffic

import (
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	sdktraffic "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

// PortBundleFromSnapshot builds emission ports from a frozen runtime snapshot.
func PortBundleFromSnapshot(snap *extensions.RequestRuntimeSnapshot) sdktraffic.PortBundle {
	return snap.TrafficPortBundle()
}
