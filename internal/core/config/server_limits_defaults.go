package config

// applyDefaultServerLimits normalizes zero decode-admission fields to documented finite defaults.
// MaxPendingWireEvents is left as-is (0 = unlimited).
func applyDefaultServerLimits(cfg *Config) {
	if cfg == nil {
		return
	}
	if cfg.Server.MaxConcurrentDecodes == 0 {
		cfg.Server.MaxConcurrentDecodes = DefaultMaxConcurrentDecodes
	}
	if cfg.Server.MaxInflightDecodeBytes == 0 {
		cfg.Server.MaxInflightDecodeBytes = DefaultMaxInflightDecodeBytes
	}
}
