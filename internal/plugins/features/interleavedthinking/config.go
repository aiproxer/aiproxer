package interleavedthinking

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ID is the canonical feature identifier in plugins.features.
const ID = "interleaved-thinking"

// Default values for interleaved thinking configuration.
const (
	DefaultStreamToClient       = "hidden"
	DefaultRegularTurns         = 2
	DefaultMaxMemoBytes         = 16 * 1024
	DefaultMaxInstructionsBytes = 64 * 1024
)

// DecodeConfig decodes a feature-private YAML subtree into Config.
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
	for i := 0; i < len(root.Content); i += 2 {
		k := root.Content[i].Value
		switch k {
		case "enabled", "instructions_file", "instructions", "stream_to_client", "regular_turns_remaining", "max_memo_bytes":
		default:
			return Config{}, fmt.Errorf("%s: unknown config key %q", ID, k)
		}
	}
	var cfg Config
	if err := root.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("%s: %w", ID, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Config carries operator-supplied settings for interleaved thinking.
type Config struct {
	Enabled               bool   `yaml:"enabled"`
	InstructionsFile      string `yaml:"instructions_file"`
	Instructions          string `yaml:"instructions"`
	StreamToClient        string `yaml:"stream_to_client"`
	RegularTurnsRemaining int    `yaml:"regular_turns_remaining"`
	MaxMemoBytes          int    `yaml:"max_memo_bytes"`
}

// EffectiveStreamToClient returns the visibility mode, defaulting to hidden when unset.
func (c Config) EffectiveStreamToClient() string {
	if v := strings.ToLower(strings.TrimSpace(c.StreamToClient)); v != "" {
		return v
	}
	return DefaultStreamToClient
}

// EffectiveRegularTurnsRemaining returns the configured budget or default when <= 0.
func (c Config) EffectiveRegularTurnsRemaining() int {
	if c.RegularTurnsRemaining > 0 {
		return c.RegularTurnsRemaining
	}
	return DefaultRegularTurns
}

// EffectiveMaxMemoBytes returns the configured memo size limit or default when <= 0.
func (c Config) EffectiveMaxMemoBytes() int {
	if c.MaxMemoBytes > 0 {
		return c.MaxMemoBytes
	}
	return DefaultMaxMemoBytes
}

// Validate applies defaults and validates settings when enabled.
func (c *Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	vis := strings.ToLower(strings.TrimSpace(c.StreamToClient))
	switch vis {
	case "":
		vis = DefaultStreamToClient
	case "hidden", "visible":
	default:
		return fmt.Errorf("interleavedthinking: stream_to_client want hidden or visible, got %q", c.StreamToClient)
	}
	c.StreamToClient = vis

	if c.RegularTurnsRemaining == 0 {
		c.RegularTurnsRemaining = DefaultRegularTurns
	} else if c.RegularTurnsRemaining < 0 {
		return fmt.Errorf("interleavedthinking: regular_turns_remaining must be >= 0")
	}

	if c.MaxMemoBytes == 0 {
		c.MaxMemoBytes = DefaultMaxMemoBytes
	} else if c.MaxMemoBytes < 0 {
		return fmt.Errorf("interleavedthinking: max_memo_bytes must be >= 0")
	}

	if strings.Contains(c.InstructionsFile, "\x00") {
		return fmt.Errorf("interleavedthinking: instructions_file must not contain NUL")
	}
	return nil
}
