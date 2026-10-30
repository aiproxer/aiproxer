package anthropic

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ExposeAipUsageExtensions bool         `yaml:"expose_aip_usage_extensions"`
	ClaudeCodeHybrid         hybridConfig `yaml:"claude_code_hybrid"`
}

type hybridConfig struct {
	Enabled        bool          `yaml:"enabled"`
	ExternalModels []hybridModel `yaml:"external_models"`
}

type hybridModel struct {
	Model           string `yaml:"model"`
	Route           string `yaml:"route"`
	RequireSubagent bool   `yaml:"require_subagent"`
}

// A deliberately smaller vocabulary than routing expressions: no aliases,
// annotations, composites, placeholders or whitespace can enter this mode.
func validHybridIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range []byte(s) {
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || !strings.ContainsRune("._/-", rune(c))) {
			return false
		}
	}
	return true
}

func DecodeConfig(n yaml.Node) (Config, error) {
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return Config{}, nil
		}
		n = *n.Content[0]
	}
	if n.Kind == 0 {
		return Config{}, nil
	}
	if n.Kind == yaml.ScalarNode && (n.Tag == "!!null" || strings.TrimSpace(n.Value) == "" || n.Value == "null") {
		return Config{}, nil
	}
	if n.Kind != yaml.MappingNode {
		return Config{}, fmt.Errorf("anthropic: config must be a mapping or null")
	}
	raw, err := yaml.Marshal(&n)
	if err != nil {
		return Config{}, fmt.Errorf("anthropic: invalid config")
	}
	d := yaml.NewDecoder(bytes.NewReader(raw))
	d.KnownFields(true)
	var cfg Config
	if err := d.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("anthropic: invalid or unknown config key")
	}
	seen := map[string]bool{}
	for _, m := range cfg.ClaudeCodeHybrid.ExternalModels {
		backend, model, ok := strings.Cut(m.Route, ":")
		if len(m.Model) > 256 || !validHybridIdentifier(m.Model) || strings.HasPrefix(strings.ToLower(m.Model), "claude-") || seen[m.Model] {
			return Config{}, fmt.Errorf("anthropic: invalid, native-colliding or duplicate hybrid model")
		}
		if !ok || len(m.Route) > 512 || !validHybridIdentifier(backend) || !validHybridIdentifier(model) {
			return Config{}, fmt.Errorf("anthropic: hybrid route must be an explicit single backend:model leaf")
		}
		seen[m.Model] = true
	}
	if cfg.ClaudeCodeHybrid.Enabled && len(seen) == 0 {
		return Config{}, fmt.Errorf("anthropic: hybrid mode requires an external model")
	}
	return cfg, nil
}
