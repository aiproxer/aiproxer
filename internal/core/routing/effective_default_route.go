package routing

import (
	"github.com/aiproxer/aiproxer/internal/core/config"
)

// Default route selector resolution lives in [config.EffectiveDefaultRouteSelector] and
// [config.WireModelForBackend]; this file keeps only model-alias helpers that depend on routing types.

// ModelAliasRulesFromConfig maps decoded [config.Config.ModelAliases] into [ModelAliasRule] slices
// for alias compilation and validation. It returns nil when cfg is nil or has no entries.
func ModelAliasRulesFromConfig(cfg *config.Config) []ModelAliasRule {
	if cfg == nil || len(cfg.ModelAliases) == 0 {
		return nil
	}
	out := make([]ModelAliasRule, len(cfg.ModelAliases))
	for i, m := range cfg.ModelAliases {
		out[i] = ModelAliasRule{Pattern: m.Pattern, Replacement: m.Replacement}
	}
	return out
}

// ValidateModelAliasesConfig compiles model_aliases from cfg (regexp patterns and static replacement
// selectors). Call after [config.LoadFile] or equivalent decode; kept out of the config package
// to avoid a config<->routing import cycle.
func ValidateModelAliasesConfig(cfg *config.Config) error {
	return ValidateModelAliases(ModelAliasRulesFromConfig(cfg))
}
