package refautoappend

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Config is decoded from the feature plugin YAML block.
type Config struct {
	Order    *int   `yaml:"order"`
	FileText string `yaml:"file_text"`
}

// DecodeConfig parses YAML for ref-autoappend-file.
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
		if cfg.FileText == "" {
			cfg = defaultConfigWithOrder(cfg)
		}
		return cfg, nil
	default:
		return Config{}, fmt.Errorf("%s: config must be a mapping or null", ID)
	}
}

func defaultConfig() Config {
	return Config{FileText: defaultFileText}
}

func defaultConfigWithOrder(cfg Config) Config {
	cfg.FileText = defaultFileText
	return cfg
}

const defaultFileText = "\n[ref-autoappend-file]\n"
