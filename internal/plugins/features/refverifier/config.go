package refverifier

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// ID is the YAML id for the completion+aux proof plugin.
const ID = "ref-verifier-stub"

// Config is optional: auxiliary role and replacement delta.
type Config struct {
	Order *int   `yaml:"order"`
	Role  string `yaml:"aux_role"`
	// SteerText is the replacement assistant text when aux succeeds.
	SteerText string `yaml:"steer_text"`
}

// DecodeConfig parses the feature block.
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
		return fillRole(cfg), nil
	default:
		return Config{}, fmt.Errorf("%s: config must be a mapping or null", ID)
	}
}

func defaultConfig() Config { return fillRole(Config{SteerText: "verifier-ok"}) }

func fillRole(c Config) Config {
	if c.Role == "" {
		c.Role = "verifier"
	}
	if c.SteerText == "" {
		c.SteerText = "verifier-ok"
	}
	return c
}
