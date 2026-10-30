package config

import (
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// RegistrationsFromConfig maps YAML plugin rows to SDK registrations. The core only
// forwards opaque config nodes; it does not interpret plugin-private schema.
func RegistrationsFromConfig(cfg *Config) []aipsdk.Registration {
	if cfg == nil {
		return nil
	}

	out := []aipsdk.Registration{}

	for _, p := range cfg.Plugins.Frontends {
		out = append(out, aipsdk.Registration{
			ID:          p.InstanceID(),
			FactoryKind: p.FactoryID(),
			Kind:        aipsdk.PluginKindFrontend,
			Enabled:     p.Enabled,
			Config:      aipsdk.ConfigPayload{Node: p.Config},
		})
	}
	for _, p := range cfg.Plugins.Backends {
		out = append(out, aipsdk.Registration{
			ID:          p.InstanceID(),
			FactoryKind: p.FactoryID(),
			Kind:        aipsdk.PluginKindBackend,
			Enabled:     p.Enabled,
			Config:      aipsdk.ConfigPayload{Node: p.Config},
		})
	}
	for _, p := range cfg.Plugins.Features {
		out = append(out, aipsdk.Registration{
			ID:          p.InstanceID(),
			FactoryKind: p.FactoryID(),
			Kind:        aipsdk.PluginKindFeature,
			Enabled:     p.Enabled,
			Config:      aipsdk.ConfigPayload{Node: p.Config},
		})
	}

	return out
}
