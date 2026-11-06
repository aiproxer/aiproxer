package frontendconfig

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ExposeAipUsageExtensions bool `yaml:"expose_aip_usage_extensions"`
	// NamespaceTools is the operator policy for OpenAI Responses namespace tool
	// declarations ("project" or "reject"). Only frontends that implement
	// namespace tool projection accept it.
	NamespaceTools string `yaml:"namespace_tools"`
}

func Decode(n yaml.Node, id string) (Config, error) {
	return decode(n, id, false)
}

// DecodeWithToolNamespacePolicy allows the namespace_tools key for a frontend
// that implements namespace tool projection. Other frontends keep rejecting it
// rather than accepting an operator knob that does nothing.
func DecodeWithToolNamespacePolicy(n yaml.Node, id string) (Config, error) {
	return decode(n, id, true)
}

func decode(n yaml.Node, id string, allowNamespaceTools bool) (Config, error) {
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
		key := root.Content[i].Value
		if key == "expose_aip_usage_extensions" {
			continue
		}
		if allowNamespaceTools && key == "namespace_tools" {
			continue
		}
		return Config{}, fmt.Errorf("%s: unknown config key %q", id, key)
	}
	var cfg Config
	if err := root.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("%s: %w", id, err)
	}
	if !allowNamespaceTools && cfg.NamespaceTools != "" {
		return Config{}, fmt.Errorf("%s: unknown config key %q", id, "namespace_tools")
	}
	switch cfg.NamespaceTools {
	case "", "project", "reject":
	default:
		return Config{}, fmt.Errorf("%s: namespace_tools must be %q or %q, got %q", id, "project", "reject", cfg.NamespaceTools)
	}
	return cfg, nil
}
