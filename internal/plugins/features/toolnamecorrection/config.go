package toolnamecorrection

import (
	"fmt"
	"path"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"gopkg.in/yaml.v3"
)

// ID is the canonical feature identifier in plugins.features.
const ID = "tool-name-correction"

const (
	// defaultMaxCorrectionsPerTurn is the shipped per-turn correction budget.
	defaultMaxCorrectionsPerTurn = uint8(toolcall.MinSteerTurnBudget)
	// defaultCorrectAfterReasoning corrects attempts that already produced
	// reasoning output unless the operator opts out.
	defaultCorrectAfterReasoning = true
	// maxPassThroughNames bounds pass_through_names.
	maxPassThroughNames = 32
)

// Config is the validated tool-name-correction feature configuration. The zero
// value carries the documented defaults: nothing is decoded straight into it,
// so it has no yaml tags and every field must keep its default at the zero
// value.
type Config struct {
	// MaxCorrectionsPerTurn is the per-turn correction budget. Zero means the
	// shipped default.
	MaxCorrectionsPerTurn uint8
	// SkipAfterReasoning skips correction once reasoning output was produced.
	// Zero (false) corrects after reasoning, matching the YAML default
	// correct_after_reasoning: true.
	SkipAfterReasoning bool
	PassThroughNames   []string
}

// rawConfig mirrors Config through pointers so an absent key stays
// distinguishable from an explicit value: an omitted budget takes the default
// instead of zero, and an omitted reasoning switch keeps its default-on value.
type rawConfig struct {
	MaxCorrectionsPerTurn *int     `yaml:"max_corrections_per_turn"`
	CorrectAfterReasoning *bool    `yaml:"correct_after_reasoning"`
	PassThroughNames      []string `yaml:"pass_through_names"`
}

// DecodeConfig decodes one feature-private YAML subtree, applying defaults and
// rejecting an out-of-range budget, more than [maxPassThroughNames] patterns,
// a malformed pattern, and unknown keys. Errors name the offending field.
func DecodeConfig(n yaml.Node) (Config, error) {
	root := n
	switch root.Kind {
	case 0:
		return defaultConfig(), nil
	case yaml.DocumentNode:
		if len(root.Content) == 0 {
			return defaultConfig(), nil
		}
		root = *root.Content[0]
	}
	switch root.Kind {
	case 0:
		return defaultConfig(), nil
	case yaml.ScalarNode:
		if root.Tag == "!!null" || root.Value == "" || root.Value == "null" {
			return defaultConfig(), nil
		}
		return Config{}, fmt.Errorf("%s: config must be a mapping or null", ID)
	case yaml.MappingNode:
		for i := 0; i < len(root.Content); i += 2 {
			if k := root.Content[i].Value; !allowedConfigKey(k) {
				return Config{}, fmt.Errorf("%s: unknown config key %q", ID, k)
			}
		}
		var raw rawConfig
		if err := root.Decode(&raw); err != nil {
			return Config{}, fmt.Errorf("%s: %w", ID, err)
		}
		return normalizeConfig(raw)
	default:
		return Config{}, fmt.Errorf("%s: config must be a mapping or null", ID)
	}
}

func allowedConfigKey(k string) bool {
	switch k {
	case "max_corrections_per_turn", "correct_after_reasoning", "pass_through_names":
		return true
	default:
		return false
	}
}

func defaultConfig() Config {
	cfg, _ := normalizeConfig(rawConfig{})
	return cfg
}

func normalizeConfig(raw rawConfig) (Config, error) {
	cfg := Config{
		MaxCorrectionsPerTurn: defaultMaxCorrectionsPerTurn,
		SkipAfterReasoning:    !defaultCorrectAfterReasoning,
		PassThroughNames:      raw.PassThroughNames,
	}
	if raw.MaxCorrectionsPerTurn != nil {
		v := *raw.MaxCorrectionsPerTurn
		if v < toolcall.MinSteerTurnBudget || v > toolcall.MaxSteerTurnBudget {
			return Config{}, fmt.Errorf("%s: max_corrections_per_turn must be in [%d,%d]",
				ID, toolcall.MinSteerTurnBudget, toolcall.MaxSteerTurnBudget)
		}
		cfg.MaxCorrectionsPerTurn = uint8(v)
	}
	if raw.CorrectAfterReasoning != nil {
		cfg.SkipAfterReasoning = !*raw.CorrectAfterReasoning
	}
	if len(cfg.PassThroughNames) > maxPassThroughNames {
		return Config{}, fmt.Errorf("%s: pass_through_names must have at most %d patterns",
			ID, maxPassThroughNames)
	}
	for i, p := range cfg.PassThroughNames {
		if _, err := path.Match(p, ""); err != nil {
			return Config{}, fmt.Errorf("%s: pass_through_names[%d] %q is not a valid pattern: %w",
				ID, i, p, err)
		}
	}
	return cfg, nil
}
