package reftoolpolicy

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// ID is the YAML id for the proof feature (design §19 tool-policy).
const ID = "ref-tool-policy"

// Config lists tools removed from the catalog; the reactor also swallows stream events
// for those tool names to prove catalog vs event enforcement.
type Config struct {
	Order *int `yaml:"order"`
	// BlockNames is exact tool names to drop.
	BlockNames []string `yaml:"block_names"`
	// BlockPrefixes: tool names with any of these prefixes are dropped.
	BlockPrefixes []string `yaml:"block_prefixes"`
}

// DecodeConfig decodes the feature YAML.
func DecodeConfig(n yaml.Node) (Config, error) {
	root := n
	switch root.Kind {
	case 0:
		return defaultConfig(), nil
	case yaml.DocumentNode:
		if len(root.Content) == 0 {
			return defaultConfig(), nil
		}
		root = *root.Content[0]
	}
	switch root.Kind {
	case 0, yaml.ScalarNode:
		if root.Kind == yaml.ScalarNode && (root.Tag == "!!null" || root.Value == "" || root.Value == "null") {
			return defaultConfig(), nil
		}
		if root.Kind == 0 {
			return defaultConfig(), nil
		}
		return Config{}, fmt.Errorf("%s: config must be a mapping or null", ID)
	case yaml.MappingNode:
		var cfg Config
		if err := root.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("%s: %w", ID, err)
		}
		if cfg.Order != nil && *cfg.Order < 0 {
			return Config{}, fmt.Errorf("%s: order must be non-negative", ID)
		}
		return cfg, nil
	default:
		return Config{}, fmt.Errorf("%s: config must be a mapping or null", ID)
	}
}

func defaultConfig() Config {
	return Config{
		BlockNames:    []string{"ref_blocked_by_name"},
		BlockPrefixes: []string{"ref_blocked_"},
	}
}
