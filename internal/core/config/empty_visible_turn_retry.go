package config

// EmptyVisibleTurnRetryConfig is the operator opt-in gate for the
// empty-visible-turn corrective retry (issue #1197): with the gate enabled, a
// clean completion that produced no visible output is replaced once by a
// corrective attempt before any canonical frame reaches the client.
//
// The section is disabled by default. The hold bounds reuse the existing
// retention constants, so the gate carries no tunable values yet.
type EmptyVisibleTurnRetryConfig struct {
	// Enabled turns the corrective retry on for turns admitted under the
	// configuration generation that carries this value.
	Enabled bool `yaml:"enabled"`
}

// EffectiveEmptyVisibleTurnRetry resolves the gate for the active generation.
// An omitted or disabled section (or a nil config) resolves to the disabled
// zero value; there are no env/CLI overrides.
func EffectiveEmptyVisibleTurnRetry(cfg *Config) EmptyVisibleTurnRetryConfig {
	if cfg == nil {
		return EmptyVisibleTurnRetryConfig{}
	}
	return cfg.EmptyVisibleTurnRetry
}

// validateEmptyVisibleTurnRetry is the load/reload hook for the gate section.
// Unknown keys and non-boolean values are already rejected by the strict
// decoder, and the gate has no bounded fields to check, so the hook keeps the
// section on the shared effective-resolution path and has nothing to reject.
func validateEmptyVisibleTurnRetry(cfg *Config) error {
	_ = EffectiveEmptyVisibleTurnRetry(cfg)
	return nil
}
