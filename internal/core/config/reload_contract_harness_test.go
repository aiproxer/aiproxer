package config_test

import (
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
)

// strictDecodeContract delegates to the production StrictDecode path (task 2.1).
func strictDecodeContract(raw []byte) (*config.Config, configsource.Category, error) {
	return config.StrictDecode(raw)
}

func computeEffectiveIdentity(cfg *config.Config) (config.EffectiveIdentity, error) {
	return config.ComputeEffectiveIdentity(cfg)
}
