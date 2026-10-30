package keepwarm

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type yamlConfig struct {
	PromptCache struct {
		Keepwarm yamlKeepwarm `yaml:"keepwarm"`
	} `yaml:"prompt_cache"`
}

type yamlKeepwarm struct {
	Enabled                       *bool           `yaml:"enabled"`
	MaxRefreshesPerIdleEpoch      *int            `yaml:"max_refreshes_per_idle_epoch"`
	MaxIdleDuration               string          `yaml:"max_idle_duration"`
	MaxActiveTargets              *int            `yaml:"max_active_targets"`
	MaxConcurrentRenewals         *int            `yaml:"max_concurrent_renewals"`
	RenewTimeout                  string          `yaml:"renew_timeout"`
	ContinueAfterColdRecreate     bool            `yaml:"continue_after_cold_recreate"`
	MaxColdRecreatesPerIdleEpoch  int             `yaml:"max_cold_recreates_per_idle_epoch"`
	MaxProviderTokensPerIdleEpoch *int64          `yaml:"max_provider_tokens_per_idle_epoch"`
	HeuristicOverrides            []yamlHeuristic `yaml:"heuristic_overrides"`
}

type yamlHeuristic struct {
	BackendInstance string `yaml:"backend_instance"`
	CanonicalModel  string `yaml:"canonical_model"`
	Interval        string `yaml:"interval"`
}

// ConfigFromYAML parses only prompt_cache.keepwarm. Provider enrollment is
// intentionally not represented here and therefore cannot be enabled by this
// generic scheduler setting.
func ConfigFromYAML(data []byte) (Config, error) {
	cfg := DefaultConfig()
	var raw yamlConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Config{}, fmt.Errorf("%w: yaml: %v", ErrInvalidConfig, err)
	}
	in := raw.PromptCache.Keepwarm
	if in.Enabled != nil {
		cfg.Enabled = *in.Enabled
	}
	if in.MaxRefreshesPerIdleEpoch != nil {
		cfg.MaxRefreshesPerIdleEpoch = *in.MaxRefreshesPerIdleEpoch
	}
	if in.MaxActiveTargets != nil {
		cfg.MaxActiveTargets = *in.MaxActiveTargets
	}
	if in.MaxConcurrentRenewals != nil {
		cfg.MaxConcurrentRenewals = *in.MaxConcurrentRenewals
	}
	cfg.ContinueAfterColdRecreate = in.ContinueAfterColdRecreate
	cfg.MaxColdRecreatesPerIdleEpoch = in.MaxColdRecreatesPerIdleEpoch
	cfg.MaxProviderTokensPerIdleEpoch = in.MaxProviderTokensPerIdleEpoch
	if in.MaxIdleDuration != "" {
		d, err := time.ParseDuration(in.MaxIdleDuration)
		if err != nil {
			return Config{}, fmt.Errorf("%w: max_idle_duration: %v", ErrInvalidConfig, err)
		}
		cfg.MaxIdleDuration = d
	}
	if in.RenewTimeout != "" {
		d, err := time.ParseDuration(in.RenewTimeout)
		if err != nil {
			return Config{}, fmt.Errorf("%w: renew_timeout: %v", ErrInvalidConfig, err)
		}
		cfg.RenewTimeout = d
	}
	for _, h := range in.HeuristicOverrides {
		d, err := time.ParseDuration(h.Interval)
		if err != nil {
			return Config{}, fmt.Errorf("%w: heuristic interval: %v", ErrInvalidConfig, err)
		}
		cfg.HeuristicOverrides = append(cfg.HeuristicOverrides, HeuristicOverride{BackendInstance: h.BackendInstance, CanonicalModel: h.CanonicalModel, Interval: d})
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func ParseYAML(data []byte) (Config, error) { return ConfigFromYAML(data) }

// DecodeConfig unmarshals a YAML node directly into Config with strict unknown field checks.
func DecodeConfig(n yaml.Node) (Config, error) {
	if n.Kind == 0 {
		return DefaultConfig(), nil
	}
	if err := checkKeepwarmMappingKeys(n); err != nil {
		return Config{}, err
	}
	var in yamlKeepwarm
	if err := n.Decode(&in); err != nil {
		return Config{}, fmt.Errorf("%w: decode keepwarm yaml: %v", ErrInvalidConfig, err)
	}
	cfg := DefaultConfig()
	if in.Enabled != nil {
		cfg.Enabled = *in.Enabled
	}
	if in.MaxRefreshesPerIdleEpoch != nil {
		cfg.MaxRefreshesPerIdleEpoch = *in.MaxRefreshesPerIdleEpoch
	}
	if in.MaxActiveTargets != nil {
		cfg.MaxActiveTargets = *in.MaxActiveTargets
	}
	if in.MaxConcurrentRenewals != nil {
		cfg.MaxConcurrentRenewals = *in.MaxConcurrentRenewals
	}
	cfg.ContinueAfterColdRecreate = in.ContinueAfterColdRecreate
	cfg.MaxColdRecreatesPerIdleEpoch = in.MaxColdRecreatesPerIdleEpoch
	cfg.MaxProviderTokensPerIdleEpoch = in.MaxProviderTokensPerIdleEpoch
	if in.MaxIdleDuration != "" {
		d, err := time.ParseDuration(in.MaxIdleDuration)
		if err != nil {
			return Config{}, fmt.Errorf("%w: max_idle_duration: %v", ErrInvalidConfig, err)
		}
		cfg.MaxIdleDuration = d
	}
	if in.RenewTimeout != "" {
		d, err := time.ParseDuration(in.RenewTimeout)
		if err != nil {
			return Config{}, fmt.Errorf("%w: renew_timeout: %v", ErrInvalidConfig, err)
		}
		cfg.RenewTimeout = d
	}
	for _, h := range in.HeuristicOverrides {
		d, err := time.ParseDuration(h.Interval)
		if err != nil {
			return Config{}, fmt.Errorf("%w: heuristic interval: %v", ErrInvalidConfig, err)
		}
		cfg.HeuristicOverrides = append(cfg.HeuristicOverrides, HeuristicOverride{
			BackendInstance: h.BackendInstance,
			CanonicalModel:  h.CanonicalModel,
			Interval:        d,
		})
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func checkKeepwarmMappingKeys(n yaml.Node) error {
	root := n
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return nil
		}
		root = *root.Content[0]
	}
	if root.Kind == 0 || (root.Kind == yaml.ScalarNode && (root.Tag == "!!null" || strings.TrimSpace(root.Value) == "" || root.Value == "null")) {
		return nil
	}
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("%w: config must be a mapping or null", ErrInvalidConfig)
	}
	for i := 0; i < len(root.Content); i += 2 {
		k := root.Content[i].Value
		switch k {
		case "enabled", "max_refreshes_per_idle_epoch", "max_idle_duration",
			"max_active_targets", "max_concurrent_renewals", "renew_timeout",
			"continue_after_cold_recreate", "max_cold_recreates_per_idle_epoch",
			"max_provider_tokens_per_idle_epoch", "heuristic_overrides":
		default:
			return fmt.Errorf("%w: unknown config key %q", ErrInvalidConfig, k)
		}
		if k == "heuristic_overrides" {
			val := root.Content[i+1]
			if val.Kind == yaml.SequenceNode {
				for _, item := range val.Content {
					if item.Kind == yaml.MappingNode {
						for j := 0; j < len(item.Content); j += 2 {
							hk := item.Content[j].Value
							switch hk {
							case "backend_instance", "canonical_model", "interval":
							default:
								return fmt.Errorf("%w: unknown heuristic_overrides key %q", ErrInvalidConfig, hk)
							}
						}
					}
				}
			}
		}
	}
	return nil
}
