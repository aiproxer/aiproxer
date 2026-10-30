package frontendconfig

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ExposeAipUsageExtensions bool `yaml:"expose_aip_usage_extensions"`
}

func Decode(n yaml.Node, id string) (Config, error) {
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
		return Config{}, fmt.Errorf("%s: config must be a mapping or null", id)
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "expose_aip_usage_extensions" {
			return Config{}, fmt.Errorf("%s: unknown config key %q", id, root.Content[i].Value)
		}
	}
	var cfg Config
	if err := root.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("%s: %w", id, err)
	}
	return cfg, nil
}
