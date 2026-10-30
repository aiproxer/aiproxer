package aipsdk

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// PluginKind identifies a plugin family exposed through the composition root.
type PluginKind string

const (
	PluginKindFrontend PluginKind = "frontend"
	PluginKindBackend  PluginKind = "backend"
	PluginKindFeature  PluginKind = "feature"
)

// ConfigPayload keeps plugin-private configuration opaque to the core.
type ConfigPayload struct {
	Node yaml.Node
}

// Registration describes a plugin available to the composition root.
type Registration struct {
	// ID is the runtime instance identifier (routing / duplicate detection within PluginKind).
	ID string
	// FactoryKind selects the bundled registry factory (e.g. openai-responses). When empty,
	// ID is used as the factory key for backward compatibility.
	FactoryKind string
	Kind        PluginKind
	Config      ConfigPayload
	Enabled     bool
}

// RegistryFactoryKey returns the registry lookup key for this registration.
func (r Registration) RegistryFactoryKey() string {
	if s := strings.TrimSpace(r.FactoryKind); s != "" {
		return s
	}
	return r.ID
}
