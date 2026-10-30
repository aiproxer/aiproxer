package agentloopguard

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ID is the standard feature factory id and provider identity.
const ID = providerID

const (
	DefaultVerifierRole             = "loop_guard"
	DefaultVerifierTimeoutSeconds   = 4
	DefaultMaxSemanticContinuations = 3
	DefaultNoProgressLimit          = 2
	DefaultMaxProtocolReprompts     = 1

	MaxVerifierRoleLength     = 128
	MaxVerifierTimeoutSeconds = 300
	MaxSemanticContinuations  = 64
	MaxNoProgressLimit        = 64
	MaxMaxProtocolReprompts   = 3
)

// Strategy selects the mutually exclusive ALG completion strategy.
type Strategy string

const (
	StrategyAttemptCompletion Strategy = "attempt_completion"
	StrategySemanticVerifier  Strategy = "semantic_verifier"
)

// ExplicitCompletionPolicy controls the treatment of a trusted normalized
// explicit-completion fact before progress policy evaluation.
type ExplicitCompletionPolicy string

const (
	ExplicitCompletionPolicyTrust  ExplicitCompletionPolicy = "trust"
	ExplicitCompletionPolicyVerify ExplicitCompletionPolicy = "verify"
)

// Config is the generation-local ALG contribution. Verifier and progress
// bounds are retained in the immutable provider configuration and consumed by
// the stateless provider.
type Config struct {
	Enabled                  bool                     `yaml:"enabled"`
	Strategy                 Strategy                 `yaml:"strategy"`
	MaxProtocolReprompts     int                      `yaml:"max_protocol_reprompts"`
	VerifierRole             string                   `yaml:"verifier_role"`
	VerifierTimeoutSeconds   int                      `yaml:"verifier_timeout_seconds"`
	VerifierTimeout          time.Duration            `yaml:"-"`
	MaxSemanticContinuations int                      `yaml:"max_semantic_continuations"`
	NoProgressLimit          int                      `yaml:"no_progress_limit"`
	ExplicitCompletionPolicy ExplicitCompletionPolicy `yaml:"explicit_completion_policy"`
}

// DecodeConfig parses and validates the nested feature YAML block.
func DecodeConfig(n yaml.Node) (Config, error) {
	root := n
	switch root.Kind {
	case 0:
		return (Config{}).Normalize()
	case yaml.DocumentNode:
		if len(root.Content) == 0 {
			return (Config{}).Normalize()
		}
		root = *root.Content[0]
	}
	switch root.Kind {
	case 0:
		return (Config{}).Normalize()
	case yaml.ScalarNode:
		if root.Tag == "!!null" || strings.TrimSpace(root.Value) == "" || root.Value == "null" {
			return (Config{}).Normalize()
		}
		return Config{}, fmt.Errorf("%s: config must be a mapping or null", ID)
	case yaml.MappingNode:
		if err := validateKnownKeys(root); err != nil {
			return Config{}, err
		}
		strategy, err := resolveYAMLStrategy(root)
		if err != nil {
			return Config{}, err
		}
		if err := validateStrategyYAMLPresence(root, strategy); err != nil {
			return Config{}, err
		}
		// The preferred protocol cap is validated from the raw mapping node BEFORE
		// the struct decode, so a malformed value cannot leak through the wrapped
		// decoder error and a float cannot be silently truncated into a valid bound.
		if strategy == StrategyAttemptCompletion {
			if err := validatePreferredRepromptsYAML(root); err != nil {
				return Config{}, err
			}
		}
		var cfg Config
		if err := root.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("%s: %w", ID, err)
		}
		cfg.Strategy = strategy
		if cfg.Enabled {
			if mappingHasKey(root, "verifier_role") && strings.TrimSpace(cfg.VerifierRole) == "" {
				return Config{}, fmt.Errorf("%s: verifier_role must be non-empty when enabled", ID)
			}
			for _, field := range []struct {
				name string
				key  string
				val  int
			}{
				{name: "verifier_timeout_seconds", key: "verifier_timeout_seconds", val: cfg.VerifierTimeoutSeconds},
				{name: "max_semantic_continuations", key: "max_semantic_continuations", val: cfg.MaxSemanticContinuations},
				{name: "no_progress_limit", key: "no_progress_limit", val: cfg.NoProgressLimit},
			} {
				if mappingHasKey(root, field.key) && field.val <= 0 {
					return Config{}, fmt.Errorf("%s: %s must be positive when enabled", ID, field.name)
				}
			}
			if !mappingHasKey(root, "no_progress_limit") {
				cfg.NoProgressLimit = DefaultNoProgressLimit
			}
			if strategy == StrategySemanticVerifier {
				if !mappingHasKey(root, "verifier_role") {
					cfg.VerifierRole = DefaultVerifierRole
				}
				if !mappingHasKey(root, "verifier_timeout_seconds") {
					cfg.VerifierTimeoutSeconds = DefaultVerifierTimeoutSeconds
				}
				if !mappingHasKey(root, "max_semantic_continuations") {
					cfg.MaxSemanticContinuations = DefaultMaxSemanticContinuations
				}
			}
		}
		return cfg.Normalize()
	default:
		return Config{}, fmt.Errorf("%s: config must be a mapping or null", ID)
	}
}

// verifierOnlyKeys are the legacy semantic-verifier settings that the
// preferred explicit-completion strategy must never accept.
var verifierOnlyKeys = []string{
	"verifier_role",
	"verifier_timeout_seconds",
	"max_semantic_continuations",
	"explicit_completion_policy",
}

// resolveStrategy normalizes a programmatic selector. An omitted selector keeps
// the pre-spec semantic-verifier behavior.
func resolveStrategy(selector Strategy) (Strategy, error) {
	raw := string(selector)
	trimmed := Strategy(strings.ToLower(strings.TrimSpace(raw)))
	switch {
	case raw == "":
		return StrategySemanticVerifier, nil
	case trimmed == "":
		return "", fmt.Errorf("%s: strategy must be %s or %s", ID, StrategyAttemptCompletion, StrategySemanticVerifier)
	case trimmed == StrategyAttemptCompletion, trimmed == StrategySemanticVerifier:
		return trimmed, nil
	default:
		return "", fmt.Errorf("%s: strategy must be %s or %s", ID, StrategyAttemptCompletion, StrategySemanticVerifier)
	}
}

// resolveYAMLStrategy resolves the selector from the raw mapping while
// distinguishing an omitted key from an explicitly present but empty or null
// one. It runs before decoding so a type mismatch in a foreign-strategy key
// cannot mask the mutual-exclusion error.
func resolveYAMLStrategy(root yaml.Node) (Strategy, error) {
	raw, ok := mappingValue(root, "strategy")
	if !ok {
		return StrategySemanticVerifier, nil
	}
	if raw.Tag == "!!null" || strings.TrimSpace(raw.Value) == "" {
		return "", fmt.Errorf("%s: strategy must be %s or %s", ID, StrategyAttemptCompletion, StrategySemanticVerifier)
	}
	return resolveStrategy(Strategy(raw.Value))
}

// validateStrategyYAMLPresence rejects strategy-specific keys supplied under the
// other strategy, independently of enabled state and value.
func validateStrategyYAMLPresence(root yaml.Node, strategy Strategy) error {
	inactive := verifierOnlyKeys
	activeName := string(StrategyAttemptCompletion)
	if strategy == StrategySemanticVerifier {
		inactive = []string{"max_protocol_reprompts"}
		activeName = string(StrategySemanticVerifier)
	}
	for _, key := range inactive {
		if mappingHasKey(root, key) {
			return fmt.Errorf("%s: %s is not valid for strategy %s", ID, key, activeName)
		}
	}
	return nil
}

// resolveScalarNode unwraps an alias chain to the underlying node. Aliases are
// the only indirection yaml.v3 leaves in a raw mapping value; the walk is
// iterative and bounded so a self-referential alias cannot loop.
func resolveScalarNode(node yaml.Node) (yaml.Node, bool) {
	for range 16 {
		if node.Kind != yaml.AliasNode {
			return node, true
		}
		if node.Alias == nil {
			return node, false
		}
		node = *node.Alias
	}
	return node, false
}

// validatePreferredRepromptsYAML rejects an explicitly supplied protocol-reprompt
// bound that cannot be honored instead of silently defaulting or coercing it.
//
// The supported value is an integer in 1..MaxMaxProtocolReprompts. yaml.v3
// decodes a YAML float into an int by truncation, so the scalar kind is checked
// before any integer conversion. Every error is a static field/bound message:
// the configured value is never echoed and the raw decoder error is never
// wrapped for this field.
func validatePreferredRepromptsYAML(root yaml.Node) error {
	raw, ok := mappingValue(root, "max_protocol_reprompts")
	if !ok {
		return nil
	}
	value, ok := resolveScalarNode(raw)
	if !ok {
		return fmt.Errorf("%s: max_protocol_reprompts must be an integer", ID)
	}
	if value.Kind != yaml.ScalarNode || value.Tag != "!!int" {
		return fmt.Errorf("%s: max_protocol_reprompts must be an integer", ID)
	}
	var reprompts int
	if err := value.Decode(&reprompts); err != nil {
		return fmt.Errorf("%s: max_protocol_reprompts must be an integer", ID)
	}
	if reprompts < 1 {
		return fmt.Errorf("%s: max_protocol_reprompts must be between 1 and %d", ID, MaxMaxProtocolReprompts)
	}
	if reprompts > MaxMaxProtocolReprompts {
		return fmt.Errorf("%s: max_protocol_reprompts must be between 1 and %d", ID, MaxMaxProtocolReprompts)
	}
	return nil
}

// rejectInactiveProgrammaticFields rejects raw mixed-strategy settings before any
// trimming, deriving, or defaulting, so a value struct cannot silently erase an
// incompatible explicit value.
func rejectInactiveProgrammaticFields(c Config, strategy Strategy) error {
	if strategy == StrategyAttemptCompletion {
		for _, field := range []struct {
			name string
			set  bool
		}{
			{name: "verifier_role", set: c.VerifierRole != ""},
			{name: "verifier_timeout_seconds", set: c.VerifierTimeoutSeconds != 0},
			{name: "verifier_timeout", set: c.VerifierTimeout != 0},
			{name: "max_semantic_continuations", set: c.MaxSemanticContinuations != 0},
			{name: "explicit_completion_policy", set: c.ExplicitCompletionPolicy != ""},
		} {
			if field.set {
				return fmt.Errorf("%s: %s is not valid for strategy %s", ID, field.name, strategy)
			}
		}
		return nil
	}
	if c.MaxProtocolReprompts != 0 {
		return fmt.Errorf("%s: max_protocol_reprompts is not valid for strategy %s", ID, strategy)
	}
	return nil
}

// NormalizeProgrammatic validates a raw programmatic config and fills only the
// active strategy's omitted defaults. Programmatic zero values mean omission
// because Config is a value struct.
func NormalizeProgrammatic(c Config) (Config, error) {
	strategy, err := resolveStrategy(c.Strategy)
	if err != nil {
		return Config{}, err
	}
	if err := rejectInactiveProgrammaticFields(c, strategy); err != nil {
		return Config{}, err
	}
	c.Strategy = strategy
	switch strategy {
	case StrategyAttemptCompletion:
		if c.MaxProtocolReprompts == 0 {
			c.MaxProtocolReprompts = DefaultMaxProtocolReprompts
		}
		if c.NoProgressLimit == 0 {
			c.NoProgressLimit = DefaultNoProgressLimit
		}
	case StrategySemanticVerifier:
		if c.VerifierRole == "" {
			c.VerifierRole = DefaultVerifierRole
		}
		if c.VerifierTimeoutSeconds == 0 {
			c.VerifierTimeoutSeconds = DefaultVerifierTimeoutSeconds
		}
		if c.MaxSemanticContinuations == 0 {
			c.MaxSemanticContinuations = DefaultMaxSemanticContinuations
		}
		if c.NoProgressLimit == 0 {
			c.NoProgressLimit = DefaultNoProgressLimit
		}
	}
	return c.Normalize()
}

// Normalize fills safe defaults and validates all provider configuration bounds.
func (c Config) Normalize() (Config, error) {
	strategy, err := resolveStrategy(c.Strategy)
	if err != nil {
		return Config{}, err
	}
	if err := rejectInactiveProgrammaticFields(c, strategy); err != nil {
		return Config{}, err
	}
	c.Strategy = strategy
	if strategy == StrategyAttemptCompletion {
		return c.normalizePreferred()
	}
	return c.normalizeSemanticVerifier()
}

// normalizePreferred validates the shared breaker and the bounded protocol
// reprompt cap of the explicit-completion strategy.
func (c Config) normalizePreferred() (Config, error) {
	if c.MaxProtocolReprompts < 0 {
		return Config{}, fmt.Errorf("%s: max_protocol_reprompts must be positive", ID)
	}
	if c.MaxProtocolReprompts == 0 {
		c.MaxProtocolReprompts = DefaultMaxProtocolReprompts
	}
	if c.MaxProtocolReprompts > MaxMaxProtocolReprompts {
		return Config{}, fmt.Errorf("%s: max_protocol_reprompts exceeds maximum %d", ID, MaxMaxProtocolReprompts)
	}
	return c.normalizeShared()
}

// normalizeSemanticVerifier preserves the pre-spec legacy normalization.
func (c Config) normalizeSemanticVerifier() (Config, error) {
	c.VerifierRole = strings.TrimSpace(c.VerifierRole)
	if c.VerifierRole == "" {
		if c.Enabled {
			return Config{}, fmt.Errorf("%s: verifier_role must be non-empty when enabled", ID)
		}
		c.VerifierRole = DefaultVerifierRole
	}
	if len(c.VerifierRole) > MaxVerifierRoleLength {
		return Config{}, fmt.Errorf("%s: verifier_role exceeds maximum length %d", ID, MaxVerifierRoleLength)
	}
	if c.VerifierTimeoutSeconds < 0 {
		return Config{}, fmt.Errorf("%s: verifier_timeout_seconds must not be negative", ID)
	}
	if c.VerifierTimeoutSeconds == 0 {
		if c.Enabled {
			return Config{}, fmt.Errorf("%s: verifier_timeout_seconds must be positive when enabled", ID)
		}
		c.VerifierTimeoutSeconds = DefaultVerifierTimeoutSeconds
	}
	if c.VerifierTimeoutSeconds > MaxVerifierTimeoutSeconds {
		return Config{}, fmt.Errorf("%s: verifier_timeout_seconds exceeds maximum %d", ID, MaxVerifierTimeoutSeconds)
	}
	c.VerifierTimeout = time.Duration(c.VerifierTimeoutSeconds) * time.Second
	if c.MaxSemanticContinuations < 0 {
		return Config{}, fmt.Errorf("%s: max_semantic_continuations must be positive when enabled", ID)
	}
	if c.MaxSemanticContinuations == 0 {
		if c.Enabled {
			return Config{}, fmt.Errorf("%s: max_semantic_continuations must be positive when enabled", ID)
		}
		c.MaxSemanticContinuations = DefaultMaxSemanticContinuations
	}
	if c.MaxSemanticContinuations > MaxSemanticContinuations {
		return Config{}, fmt.Errorf("%s: max_semantic_continuations exceeds maximum %d", ID, MaxSemanticContinuations)
	}
	if c.MaxProtocolReprompts != 0 {
		return Config{}, fmt.Errorf("%s: max_protocol_reprompts is not valid for strategy %s", ID, StrategySemanticVerifier)
	}
	policy := ExplicitCompletionPolicy(strings.ToLower(strings.TrimSpace(string(c.ExplicitCompletionPolicy))))
	if policy == "" {
		policy = ExplicitCompletionPolicyTrust
	}
	switch policy {
	case ExplicitCompletionPolicyTrust, ExplicitCompletionPolicyVerify:
		c.ExplicitCompletionPolicy = policy
	default:
		return Config{}, fmt.Errorf("%s: explicit_completion_policy: unknown %q (want trust or verify)", ID, c.ExplicitCompletionPolicy)
	}
	return c.normalizeShared()
}

// normalizeShared validates the no-progress breaker shared by both strategies.
func (c Config) normalizeShared() (Config, error) {
	if c.NoProgressLimit < 0 {
		return Config{}, fmt.Errorf("%s: no_progress_limit must not be negative", ID)
	}
	if c.NoProgressLimit == 0 {
		if c.Enabled {
			return Config{}, fmt.Errorf("%s: no_progress_limit must be positive when enabled", ID)
		}
		c.NoProgressLimit = DefaultNoProgressLimit
	}
	if c.NoProgressLimit > MaxNoProgressLimit {
		return Config{}, fmt.Errorf("%s: no_progress_limit exceeds maximum %d", ID, MaxNoProgressLimit)
	}
	return c, nil
}

// Validate checks an already materialized feature configuration.
func (c Config) Validate() error {
	_, err := c.Normalize()
	return err
}

func mappingHasKey(root yaml.Node, key string) bool {
	if root.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			return true
		}
	}
	return false
}

// mappingValue returns the raw value node for a mapping key.
func mappingValue(root yaml.Node, key string) (yaml.Node, bool) {
	if root.Kind != yaml.MappingNode {
		return yaml.Node{}, false
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			return *root.Content[i+1], true
		}
	}
	return yaml.Node{}, false
}

func validateKnownKeys(root yaml.Node) error {
	known := map[string]struct{}{
		"enabled":                    {},
		"strategy":                   {},
		"max_protocol_reprompts":     {},
		"verifier_role":              {},
		"verifier_timeout_seconds":   {},
		"max_semantic_continuations": {},
		"no_progress_limit":          {},
		"explicit_completion_policy": {},
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		if _, ok := known[key]; !ok {
			return fmt.Errorf("%s: unknown field %q", ID, key)
		}
	}
	return nil
}
