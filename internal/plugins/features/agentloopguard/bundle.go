package agentloopguard

import (
	"fmt"

	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"gopkg.in/yaml.v3"
)

// BuildBundle assembles the agent-loop-guard standard feature bundle from its
// YAML config node.
func BuildBundle(n yaml.Node) (aipfeature.FeatureBundle, error) {
	cfg, err := DecodeConfig(n)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	if !cfg.Enabled {
		return aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1}, nil
	}
	provider, err := NewConfiguredProvider(cfg)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, ID, provider); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", ID, err)
	}
	if cfg.Strategy == StrategyAttemptCompletion {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneControlToolProvider, ID, NewCompletionToolProvider()); err != nil {
			return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", ID, err)
		}
	}
	return aipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}
