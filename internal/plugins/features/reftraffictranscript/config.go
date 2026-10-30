package reftraffictranscript

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// ID is the YAML id for the traffic proof feature.
const ID = "ref-traffic-transcript"

// Config for redaction pattern and default ordering.
type Config struct {
	// RedactSubstrings are removed from observer path bodies (in order). Empty uses default.
	RedactSubstrings []string `yaml:"redact_substrings"`
	Order            *int     `yaml:"order"`
}

// DecodeConfig parses the feature YAML.
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
		if len(cfg.RedactSubstrings) == 0 {
			cfg.RedactSubstrings = append([]string(nil), defaultConfig().RedactSubstrings...)
		}
		return cfg, nil
	default:
		return Config{}, fmt.Errorf("%s: config must be a mapping or null", ID)
	}
}

func defaultConfig() Config {
	return Config{RedactSubstrings: []string{defaultSecret}}
}

const defaultSecret = "REF_SECRET"

// DefaultConfig returns the default settings (for tests and smoke checks).
func DefaultConfig() Config { return defaultConfig() }
