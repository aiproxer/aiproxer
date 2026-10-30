package diagnostics

import (
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/discovery"
)

// DiscoveryFromConfig maps typed plugins.backend_discovery onto discovery.Config.
// When discovery is disabled, the returned config has no roots (Inspect skips Discover).
func DiscoveryFromConfig(bd config.BackendDiscoveryConfig) discovery.Config {
	if !bd.Enabled {
		return discovery.Config{}
	}
	paths := append([]string(nil), bd.Paths...)
	if bd.DevelopmentMode {
		return discovery.Config{
			ExplicitPaths: paths,
			Development:   true,
		}
	}
	return discovery.Config{
		ExplicitPaths:           paths,
		IncludeUpstreamDefaults: true,
	}
}
