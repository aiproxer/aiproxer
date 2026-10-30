package standardplugins

import (
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair"
	"gopkg.in/yaml.v3"
)

// ToolCallRepairFeatureID is the standard factory/instance id for tool-call repair.
// Any features config row matching this ID (instance) or FactoryKind suppresses
// default standard-distribution injection. A present row without enabled: true is
// an explicit disabled opt-out under plain-bool PluginConfig.Enabled.
const ToolCallRepairFeatureID = toolcallrepair.ID

type ToolCallRepairInjectOpts struct {
	StandardDistribution bool
}

func EnsureToolCallRepairInConfig(cfg *config.Config, opts ToolCallRepairInjectOpts) error {
	if cfg == nil || !opts.StandardDistribution {
		return nil
	}
	for _, p := range cfg.Plugins.Features {
		if p.FactoryID() == ToolCallRepairFeatureID || p.InstanceID() == ToolCallRepairFeatureID {
			return nil
		}
	}
	cfg.Plugins.Features = append(cfg.Plugins.Features, config.PluginConfig{
		ID:      ToolCallRepairFeatureID,
		Enabled: true,
		Config:  emptyYAMLMapping(),
	})
	return nil
}

func emptyYAMLMapping() yaml.Node {
	return yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
}
