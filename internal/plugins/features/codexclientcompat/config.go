package codexclientcompat

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const ID = "codex-client-compat"

type Config struct {
	Order *int `yaml:"order"`
}

func DecodeConfig(n yaml.Node) (Config, error) {
	root := n
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return Config{}, nil
		}
		root = *root.Content[0]
	}
	if root.Kind == 0 || (root.Kind == yaml.ScalarNode && (root.Tag == "!!null" || strings.TrimSpace(root.Value) == "" || root.Value == "null")) {
		return Config{}, nil
	}
	if root.Kind != yaml.MappingNode {
		return Config{}, fmt.Errorf("%s: config must be a mapping or null", ID)
	}
	var cfg Config
	if err := root.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("%s: %w", ID, err)
	}
	if cfg.Order != nil && *cfg.Order < 0 {
		return Config{}, fmt.Errorf("%s: order must be non-negative", ID)
	}
	return cfg, nil
}
