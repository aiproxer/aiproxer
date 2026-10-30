package reftool

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Config is decoded from the feature plugin YAML block.
type Config struct {
	Order  *int   `yaml:"order"`
	Prefix string `yaml:"prefix"`
}

// DecodeConfig parses YAML for ref-tool-prefix.
func DecodeConfig(n yaml.Node) (Config, error) {
	root := n
	switch root.Kind {
	case 0:
		return Config{}, nil
	case yaml.DocumentNode:
		if len(root.Content) == 0 {
			return Config{}, nil
		}
		root = *root.Content[0]
	}
	switch root.Kind {
	case 0, yaml.ScalarNode:
		if root.Kind == yaml.ScalarNode && (root.Tag == "!!null" || root.Value == "" || root.Value == "null") {
			return Config{}, nil
		}
		if root.Kind == 0 {
			return Config{}, nil
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
