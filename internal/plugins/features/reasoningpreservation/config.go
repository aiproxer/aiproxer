package reasoningpreservation

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	ActionObserve = "observe"
	ActionRestore = "restore"

	PolicyLogSkip = "log_skip"
	PolicyReject  = "reject"
)

type RuleConfig struct {
	ID            string   `yaml:"id"`
	Backend       string   `yaml:"backend"`
	ModelKeywords []string `yaml:"model_keywords"`
	Enabled       *bool    `yaml:"enabled"`
}

type StateConfig struct {
	TTL                      time.Duration `yaml:"ttl"`
	MaxTurnsPerSession       int           `yaml:"max_turns_per_session"`
	MaxReasoningBytesPerTurn int           `yaml:"max_reasoning_bytes_per_turn"`
	MaxSessionBytes          int           `yaml:"max_session_bytes"`
}

type Config struct {
	Action            string            `yaml:"action"`
	UseBuiltinCatalog bool              `yaml:"use_builtin_catalog"`
	Rules             []RuleConfig      `yaml:"rules"`
	OnAmbiguous       string            `yaml:"on_ambiguous"`
	OnUnrepresentable string            `yaml:"on_unrepresentable"`
	OnStateError      string            `yaml:"on_state_error"`
	State             StateConfig       `yaml:"state"`
	Compression       CompressionConfig `yaml:"compression"`
}

func DecodeConfig(n yaml.Node) (Config, error) {
	root := n
	switch root.Kind {
	case 0:
		return Config{}, fmt.Errorf("%s: action is required", ID)
	case yaml.DocumentNode:
		if len(root.Content) == 0 {
			return Config{}, fmt.Errorf("%s: action is required", ID)
		}
		root = *root.Content[0]
	}
	switch root.Kind {
	case 0, yaml.ScalarNode:
		if root.Kind == yaml.ScalarNode && (root.Tag == "!!null" || root.Value == "" || root.Value == "null") {
			return Config{}, fmt.Errorf("%s: action is required", ID)
		}
		return Config{}, fmt.Errorf("%s: config must be a mapping", ID)
	case yaml.MappingNode:
		if err := rejectUnknownConfigKeys(root); err != nil {
			return Config{}, err
		}
		var cfg Config
		if err := root.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("%s: %w", ID, err)
		}
		return validateConfig(cfg)
	default:
		return Config{}, fmt.Errorf("%s: config must be a mapping", ID)
	}
}

func rejectUnknownConfigKeys(root yaml.Node) error {
	for i := 0; i < len(root.Content); i += 2 {
		k := root.Content[i].Value
		switch k {
		case "action", "use_builtin_catalog", "rules", "on_ambiguous", "on_unrepresentable", "on_state_error", "state", "compression":
		default:
			return fmt.Errorf("%s: unknown config key %q", ID, k)
		}
		if k == "rules" {
			if err := rejectUnknownRuleKeys(root.Content[i+1]); err != nil {
				return err
			}
		}
		if k == "state" {
			if err := rejectUnknownStateKeys(root.Content[i+1]); err != nil {
				return err
			}
		}
		if k == "compression" {
			if err := rejectUnknownCompressionKeys(root.Content[i+1]); err != nil {
				return err
			}
		}
	}
	return nil
}

func rejectUnknownRuleKeys(n *yaml.Node) error {
	if n == nil {
		return nil
	}
	node := n
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil
		}
		node = node.Content[0]
	}
	if node.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s: rules must be a sequence", ID)
	}
	for _, item := range node.Content {
		if item == nil || item.Kind != yaml.MappingNode {
			return fmt.Errorf("%s: rule must be a mapping", ID)
		}
		for i := 0; i < len(item.Content); i += 2 {
			switch item.Content[i].Value {
			case "id", "backend", "model_keywords", "enabled":
			default:
				return fmt.Errorf("%s: unknown rule key %q", ID, item.Content[i].Value)
			}
		}
	}
	return nil
}

func rejectUnknownStateKeys(n *yaml.Node) error {
	if n == nil {
		return nil
	}
	node := n
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil
		}
		node = node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: state must be a mapping", ID)
	}
	for i := 0; i < len(node.Content); i += 2 {
		switch node.Content[i].Value {
		case "ttl", "max_turns_per_session", "max_reasoning_bytes_per_turn", "max_session_bytes":
		default:
			return fmt.Errorf("%s: unknown state key %q", ID, node.Content[i].Value)
		}
	}
	return nil
}

func validateConfig(cfg Config) (Config, error) {
	switch cfg.Action {
	case ActionObserve, ActionRestore:
	case "":
		return Config{}, fmt.Errorf("%s: action is required", ID)
	default:
		return Config{}, fmt.Errorf("%s: unknown action %q", ID, cfg.Action)
	}
	if cfg.OnAmbiguous != PolicyLogSkip {
		return Config{}, fmt.Errorf("%s: on_ambiguous must be %q", ID, PolicyLogSkip)
	}
	switch cfg.OnUnrepresentable {
	case PolicyReject, PolicyLogSkip:
	default:
		return Config{}, fmt.Errorf("%s: on_unrepresentable must be %q or %q", ID, PolicyReject, PolicyLogSkip)
	}
	switch cfg.OnStateError {
	case PolicyReject, PolicyLogSkip:
	default:
		return Config{}, fmt.Errorf("%s: on_state_error must be %q or %q", ID, PolicyReject, PolicyLogSkip)
	}
	if cfg.State.TTL <= 0 {
		return Config{}, fmt.Errorf("%s: state.ttl must be > 0", ID)
	}
	if cfg.State.MaxTurnsPerSession <= 0 {
		return Config{}, fmt.Errorf("%s: state.max_turns_per_session must be > 0", ID)
	}
	if cfg.State.MaxReasoningBytesPerTurn <= 0 {
		return Config{}, fmt.Errorf("%s: state.max_reasoning_bytes_per_turn must be > 0", ID)
	}
	if cfg.State.MaxSessionBytes <= 0 {
		return Config{}, fmt.Errorf("%s: state.max_session_bytes must be > 0", ID)
	}
	cc, err := validateCompressionConfig(cfg.Compression)
	if err != nil {
		return Config{}, err
	}
	cfg.Compression = cc
	seen := make(map[string]struct{}, len(cfg.Rules))
	for i := range cfg.Rules {
		r := &cfg.Rules[i]
		id := strings.TrimSpace(r.ID)
		if id == "" {
			return Config{}, fmt.Errorf("%s: rules[%d].id is required", ID, i)
		}
		if _, ok := seen[id]; ok {
			return Config{}, fmt.Errorf("%s: duplicate rule id %q", ID, id)
		}
		seen[id] = struct{}{}
		r.ID = id
		backend := strings.TrimSpace(r.Backend)
		if backend == "" {
			return Config{}, fmt.Errorf("%s: rules[%d].backend is required", ID, i)
		}
		r.Backend = backend
		if r.Enabled == nil {
			return Config{}, fmt.Errorf("%s: rules[%d].enabled is required", ID, i)
		}
		for j, kw := range r.ModelKeywords {
			if strings.TrimSpace(kw) == "" {
				return Config{}, fmt.Errorf("%s: rules[%d].model_keywords[%d] must be non-empty", ID, i, j)
			}
			r.ModelKeywords[j] = strings.TrimSpace(kw)
		}
	}
	return cfg, nil
}
