package config

// InterleavedConfig controls interleaved thinking (`[thinker]` selectors).
//
// Minimum enablement and configuration values required for route planning.
// Feature-specific defaults, prompts, and instruction loading are owned
// by internal/plugins/features/interleavedthinking.
type InterleavedConfig struct {
	// Enabled turns on interleaved thinking. Disabled by default.
	Enabled bool `yaml:"enabled"`
}

func validateInterleaved(cfg *Config) error {
	return nil
}
