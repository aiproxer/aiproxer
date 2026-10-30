// Package modelsystemprompt selects ordered backend-only system steering from
// immutable model rules. It has no session, routing or persistence authority.
package modelsystemprompt

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// ID is the standard feature registration and persistent producer identity.
const ID = "model-system-prompt"

// Config is the rules-only subtree; enablement belongs to the outer registration.
type Config struct {
	Rules []Rule
}

// Rule matches a logical model and contributes Append verbatim.
type Rule struct {
	ID           string
	ModelPattern string
	Append       string
}

// DecodeConfig strictly decodes the feature subtree without accepting a nested
// enabled flag. Semantic validation and regex compilation are performed by New.
func DecodeConfig(n yaml.Node) (Config, error) {
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return Config{}, nil
		}
		if len(n.Content) != 1 {
			return Config{}, configError("invalid document")
		}
		n = *n.Content[0]
	}
	if n.Kind == 0 || (n.Kind == yaml.ScalarNode && n.Tag == "!!null") {
		return Config{}, nil
	}
	var cfg Config
	err := mapping(n, func(key string, value *yaml.Node) error {
		if key != "rules" {
			return configError("unsupported config field")
		}
		if value.Kind != yaml.SequenceNode {
			return configError("rules must be a sequence")
		}
		if len(value.Content) > maxRules {
			return configError("too many rules")
		}
		for _, entry := range value.Content {
			var rule Rule
			if err := mapping(*entry, func(key string, value *yaml.Node) error {
				if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
					return configError("rule values must be strings")
				}
				switch key {
				case "id":
					rule.ID = value.Value
				case "model_pattern":
					rule.ModelPattern = value.Value
				case "append":
					rule.Append = value.Value
				default:
					return configError("unsupported rule field")
				}
				return nil
			}); err != nil {
				return err
			}
			cfg.Rules = append(cfg.Rules, rule)
		}
		return nil
	})
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func mapping(n yaml.Node, field func(string, *yaml.Node) error) error {
	if n.Kind != yaml.MappingNode || len(n.Content)%2 != 0 {
		return configError("expected mapping")
	}
	seen := make(map[string]bool)
	for i := 0; i < len(n.Content); i += 2 {
		key := n.Content[i]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || seen[key.Value] {
			return configError("invalid or duplicate field")
		}
		seen[key.Value] = true
		if err := field(key.Value, n.Content[i+1]); err != nil {
			return err
		}
	}
	return nil
}

// Never include operator-controlled values in ordinary validation diagnostics.
func configError(reason string) error {
	return fmt.Errorf("%s: %s", ID, reason)
}
