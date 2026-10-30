package submitnoop

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// HookConfig is decoded from the feature plugin YAML config for submit-noop.
type HookConfig struct {
	// Order overrides hook sort order (default DefaultHookOrder).
	Order *int `yaml:"order"`
	// LifecycleProbe, when true, registers a no-op Lifecycle for bootstrap integration tests.
	LifecycleProbe bool `yaml:"lifecycle_probe"`
}

// DecodeHookConfig parses and validates submit-noop YAML. Empty or null config is allowed.
func DecodeHookConfig(n yaml.Node) (HookConfig, error) {
	root := n
	switch root.Kind {
	case 0:
		return HookConfig{}, nil
	case yaml.DocumentNode:
		if len(root.Content) == 0 {
			return HookConfig{}, nil
		}
		root = *root.Content[0]
	}

	switch root.Kind {
	case 0:
		return HookConfig{}, nil
	case yaml.ScalarNode:
		if root.Tag == "!!null" || root.Value == "" || root.Value == "null" {
			return HookConfig{}, nil
		}
		return HookConfig{}, fmt.Errorf("submit-noop: config must be a mapping or null")
	case yaml.MappingNode:
		for i := 0; i < len(root.Content); i += 2 {
			k := root.Content[i].Value
			if k != "order" && k != "lifecycle_probe" {
				return HookConfig{}, fmt.Errorf("submit-noop: unknown config key %q", k)
			}
		}
		var cfg HookConfig
		if err := root.Decode(&cfg); err != nil {
			return HookConfig{}, fmt.Errorf("submit-noop: %w", err)
		}
		if cfg.Order != nil && *cfg.Order < 0 {
			return HookConfig{}, fmt.Errorf("submit-noop: order must be non-negative")
		}
		return cfg, nil
	default:
		return HookConfig{}, fmt.Errorf("submit-noop: config must be a mapping or null")
	}
}
