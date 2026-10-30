package runtimebundle

import (
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"gopkg.in/yaml.v3"
)

// freezeConfig returns a deep copy of cfg so later mutations of the caller's
// config, slices, maps, or plugin YAML nodes cannot change generation behavior.
func freezeConfig(cfg *config.Config) (*config.Config, error) {
	if cfg == nil {
		return nil, fmt.Errorf("runtimebundle: nil config")
	}
	// Normalize plugin yaml.Node values to non-document nodes so a full-config
	// YAML round-trip succeeds (DocumentNode fields fail yaml.Marshal).
	tmp := *cfg
	tmp.Plugins.Frontends = freezePluginConfigs(cfg.Plugins.Frontends)
	tmp.Plugins.Backends = freezePluginConfigs(cfg.Plugins.Backends)
	tmp.Plugins.Features = freezePluginConfigs(cfg.Plugins.Features)
	raw, err := yaml.Marshal(&tmp)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: freeze config marshal: %w", err)
	}
	var out config.Config
	if err := yaml.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("runtimebundle: freeze config unmarshal: %w", err)
	}
	out.ConfigDir = cfg.ConfigDir
	// Ensure plugin nodes stay non-document after unmarshal.
	out.Plugins.Frontends = freezePluginConfigs(out.Plugins.Frontends)
	out.Plugins.Backends = freezePluginConfigs(out.Plugins.Backends)
	out.Plugins.Features = freezePluginConfigs(out.Plugins.Features)
	return &out, nil
}

// freezePluginConfigs returns a defensive copy of plugin rows including YAML nodes.
func freezePluginConfigs(in []config.PluginConfig) []config.PluginConfig {
	if in == nil {
		return nil
	}
	out := make([]config.PluginConfig, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Config = cloneYAMLNode(in[i].Config)
	}
	return out
}

// freezeRegistrations returns a deep copy of registrations including nested YAML.
func freezeRegistrations(in []aipsdk.Registration) []aipsdk.Registration {
	if in == nil {
		return nil
	}
	out := make([]aipsdk.Registration, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].Config.Node = cloneYAMLNode(in[i].Config.Node)
	}
	return out
}

func cloneYAMLNode(n yaml.Node) yaml.Node {
	n = unwrapYAMLDocument(n)
	raw, err := yaml.Marshal(&n)
	if err != nil {
		return yaml.Node{}
	}
	var out yaml.Node
	if err := yaml.Unmarshal(raw, &out); err != nil {
		return yaml.Node{}
	}
	return unwrapYAMLDocument(out)
}

func unwrapYAMLDocument(n yaml.Node) yaml.Node {
	for n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 || n.Content[0] == nil {
			return yaml.Node{}
		}
		n = *n.Content[0]
	}
	return n
}
