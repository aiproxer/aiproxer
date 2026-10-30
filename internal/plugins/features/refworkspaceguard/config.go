package refworkspaceguard

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// ID is the plugin id for ref-workspace-guard.
const ID = "ref-workspace-guard"

// Config controls the static workspace view and state gating keys.
type Config struct {
	Order *int `yaml:"order"`

	ProjectRoot string            `yaml:"project_root"`
	DirtyTree   bool              `yaml:"dirty_tree"`
	Markers     []string          `yaml:"markers"`
	Labels      map[string]string `yaml:"labels"`
}

// DecodeConfig decodes YAML.
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
		if cfg.ProjectRoot == "" {
			return defaultConfigWithOrder(cfg), nil
		}
		return fillLabels(cfg), nil
	default:
		return Config{}, fmt.Errorf("%s: config must be a mapping or null", ID)
	}
}

func defaultConfig() Config {
	return fillLabels(Config{
		ProjectRoot: "/ref/workspace",
		DirtyTree:   true,
		Markers:     []string{".refws"},
	})
}

func defaultConfigWithOrder(cfg Config) Config {
	c := defaultConfig()
	if cfg.Order != nil {
		c.Order = cfg.Order
	}
	return c
}

func fillLabels(cfg Config) Config {
	if cfg.Labels == nil {
		cfg.Labels = map[string]string{}
	}
	if _, ok := cfg.Labels[LabelDenyHeat]; !ok {
		cfg.Labels[LabelDenyHeat] = "1"
	}
	return cfg
}
