package secretguard

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"gopkg.in/yaml.v3"
)

// ID is the feature plugin id / factory kind for secrets-guard.
const ID = "secrets-guard"

// Action values for enabled configuration.
const (
	ActionBlock  = "block"
	ActionRedact = "redact"
	ActionLog    = "log"
)

// Audit failure policy values.
const (
	AuditFailClosed = "fail_closed"
	AuditBestEffort = "best_effort"
)

// FailureKindScanLimit is set on Decision when scan_max_bytes is exceeded.
const FailureKindScanLimit = "scan_limit"

// FailureKindUnsupportedJSONToken is set on Decision when a JSON key or scalar
// token matched during redact but cannot be mutated in place.
const FailureKindUnsupportedJSONToken = "unsupported_json_token"

// FailureKindUnrewritableDetectedSecret is set on Decision when BetterLeaks
// finds a secret whose original representation cannot be safely rewritten.
const FailureKindUnrewritableDetectedSecret = sdk.FailureKindUnrewritableDetectedSecret

// Defaults applied by DecodeConfig.
const (
	DefaultScanMaxBytes           = 2 << 20
	MaxScanMaxBytes               = 64 << 20
	DefaultMinSecretBytes         = 8
	DefaultMaskByte               = "*"
	DefaultBetterLeaksConfidence  = "medium"
	DefaultBetterLeaksDecodeDepth = 1
	MaxBetterLeaksDecodeDepth     = 3
	MaxBetterLeaksWorkers         = 64
	DefaultBetterLeaksMaxFindings = 256
	defaultIncludePopular         = true
	defaultPreservePrefix         = true
	defaultAuditFailPolicy        = AuditFailClosed
)

// Config is the secrets-guard feature plugin YAML configuration.
type Config struct {
	Order                   *int                 `yaml:"order"`
	Action                  string               `yaml:"action"`
	AuditFailurePolicy      string               `yaml:"audit_failure_policy"`
	MinSecretBytes          int                  `yaml:"min_secret_bytes"`
	ScanMaxBytes            int                  `yaml:"scan_max_bytes"`
	SingleUser              SingleUserConfig     `yaml:"single_user"`
	Redaction               RedactionConfig      `yaml:"redaction"`
	AutoDiscoveredLocalKeys DetectorToggleConfig `yaml:"auto_discovered_local_keys"`
	BetterLeaks             BetterLeaksConfig    `yaml:"betterleaks"`
}

// DetectorToggleConfig preserves whether the operator supplied enabled. A
// pointer is required because omitted and explicit false have different
// access-mode-dependent defaults.
type DetectorToggleConfig struct {
	Enabled *bool `yaml:"enabled"`
}

// BetterLeaksConfig is the feature-owned, safe BetterLeaks tuning surface.
// Scanner construction and upstream rule-ID validation belong to later
// composition stages; this type only carries validated local policy values.
type BetterLeaksConfig struct {
	Enabled           *bool    `yaml:"enabled"`
	MinimumConfidence string   `yaml:"minimum_confidence"`
	MaxDecodeDepth    *int     `yaml:"max_decode_depth"`
	Workers           *int     `yaml:"workers"`
	DisableRules      []string `yaml:"disable_rules"`
	IsolateRules      []string `yaml:"isolate_rules"`
}

// SingleUserConfig configures single-user environment inventory hints (composition uses these later).
type SingleUserConfig struct {
	IncludePopularEnv bool     `yaml:"include_popular_env"`
	IncludeEnv        []string `yaml:"include_env"`
	ExcludeEnv        []string `yaml:"exclude_env"`
}

// RedactionConfig controls mask presentation hints for operators / future matcher wiring.
type RedactionConfig struct {
	MaskByte              string `yaml:"mask_byte"`
	PreserveKnownPrefixes bool   `yaml:"preserve_known_prefixes"`
}

// DecodeConfig parses and validates secrets-guard YAML. Action is required.
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
		if root.Kind == 0 {
			return Config{}, fmt.Errorf("%s: action is required", ID)
		}
		return Config{}, fmt.Errorf("%s: config must be a mapping or null", ID)
	case yaml.MappingNode:
		if err := validateDetectorMappings(root); err != nil {
			return Config{}, err
		}
		var cfg Config
		// Defaults that YAML false/empty should be able to override after decode.
		cfg.SingleUser.IncludePopularEnv = defaultIncludePopular
		cfg.Redaction.PreserveKnownPrefixes = defaultPreservePrefix
		if err := root.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("%s: %w", ID, err)
		}
		return validateAndFill(cfg)
	default:
		return Config{}, fmt.Errorf("%s: config must be a mapping or null", ID)
	}
}

func validateDetectorMappings(root yaml.Node) error {
	for _, key := range []string{"auto_discovered_local_keys", "betterleaks"} {
		node, ok := mappingValue(root, key)
		if !ok || (node.Kind == yaml.ScalarNode && (node.Tag == "!!null" || strings.TrimSpace(node.Value) == "")) {
			continue
		}
		if node.Kind != yaml.MappingNode {
			return fmt.Errorf("%s: %s must be a mapping", ID, key)
		}
		if enabled, ok := mappingValue(node, "enabled"); ok && (enabled.Kind != yaml.ScalarNode || enabled.Tag == "!!null" || enabled.Tag != "!!bool") {
			return fmt.Errorf("%s: %s.enabled must be a boolean", ID, key)
		}
		if key == "betterleaks" {
			for _, field := range []string{"minimum_confidence", "max_decode_depth", "workers"} {
				value, present := mappingValue(node, field)
				if present && (value.Kind != yaml.ScalarNode || value.Tag == "!!null" || strings.TrimSpace(value.Value) == "") {
					return fmt.Errorf("%s: betterleaks.%s must be explicitly configured when present", ID, field)
				}
			}
		}
	}
	return nil
}

func mappingValue(node yaml.Node, key string) (yaml.Node, bool) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return *node.Content[i+1], true
		}
	}
	return yaml.Node{}, false
}

func validateAndFill(cfg Config) (Config, error) {
	cfg.Action = strings.TrimSpace(cfg.Action)
	if cfg.Action == "" {
		return Config{}, fmt.Errorf("%s: action is required", ID)
	}
	switch cfg.Action {
	case ActionBlock, ActionRedact, ActionLog:
	default:
		return Config{}, fmt.Errorf("%s: unknown action %q (want block|redact|log)", ID, cfg.Action)
	}

	cfg.AuditFailurePolicy = strings.TrimSpace(cfg.AuditFailurePolicy)
	if cfg.AuditFailurePolicy == "" {
		cfg.AuditFailurePolicy = defaultAuditFailPolicy
	}
	switch cfg.AuditFailurePolicy {
	case AuditFailClosed, AuditBestEffort:
	default:
		return Config{}, fmt.Errorf("%s: unknown audit_failure_policy %q (want fail_closed|best_effort)", ID, cfg.AuditFailurePolicy)
	}

	if cfg.Order != nil && *cfg.Order < 0 {
		return Config{}, fmt.Errorf("%s: order must be non-negative", ID)
	}
	if cfg.MinSecretBytes <= 0 {
		cfg.MinSecretBytes = DefaultMinSecretBytes
	}
	if cfg.ScanMaxBytes <= 0 {
		cfg.ScanMaxBytes = DefaultScanMaxBytes
	}
	if cfg.ScanMaxBytes > MaxScanMaxBytes {
		return Config{}, fmt.Errorf("%s: scan_max_bytes exceeds maximum %d", ID, MaxScanMaxBytes)
	}

	mask := cfg.Redaction.MaskByte
	if mask == "" {
		mask = DefaultMaskByte
	}
	if err := validateMaskByte(mask); err != nil {
		return Config{}, err
	}
	cfg.Redaction.MaskByte = mask

	var err error
	cfg.BetterLeaks, err = normalizeBetterLeaksConfig(cfg.BetterLeaks)
	if err != nil {
		return Config{}, err
	}

	// Detect explicit false for include_popular_env / preserve_known_prefixes:
	// yaml.Decode already applied values; zero-value bool is false when key absent
	// but we pre-set defaults to true before Decode, so absent keys remain true.
	return cfg, nil
}

func normalizeBetterLeaksConfig(cfg BetterLeaksConfig) (BetterLeaksConfig, error) {
	cfg.MinimumConfidence = strings.TrimSpace(cfg.MinimumConfidence)
	if cfg.MinimumConfidence != "" && cfg.MinimumConfidence != "low" && cfg.MinimumConfidence != "medium" && cfg.MinimumConfidence != "high" {
		return BetterLeaksConfig{}, fmt.Errorf("%s: betterleaks.minimum_confidence must be low, medium, or high", ID)
	}

	if cfg.MaxDecodeDepth != nil && (*cfg.MaxDecodeDepth < 0 || *cfg.MaxDecodeDepth > MaxBetterLeaksDecodeDepth) {
		return BetterLeaksConfig{}, fmt.Errorf("%s: betterleaks.max_decode_depth must be between 0 and %d", ID, MaxBetterLeaksDecodeDepth)
	}
	if cfg.Workers != nil && (*cfg.Workers < 0 || *cfg.Workers > MaxBetterLeaksWorkers) {
		return BetterLeaksConfig{}, fmt.Errorf("%s: betterleaks.workers must be 0 or between 1 and %d", ID, MaxBetterLeaksWorkers)
	}

	var err error
	cfg.DisableRules, err = normalizeRuleSelectors("disable_rules", cfg.DisableRules)
	if err != nil {
		return BetterLeaksConfig{}, err
	}
	cfg.IsolateRules, err = normalizeRuleSelectors("isolate_rules", cfg.IsolateRules)
	if err != nil {
		return BetterLeaksConfig{}, err
	}
	if len(cfg.DisableRules) > 0 && len(cfg.IsolateRules) > 0 {
		return BetterLeaksConfig{}, fmt.Errorf("%s: betterleaks.disable_rules and betterleaks.isolate_rules are mutually exclusive", ID)
	}
	return cfg, nil
}

func normalizeRuleSelectors(field string, values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		id := strings.TrimSpace(raw)
		if id == "" {
			return nil, fmt.Errorf("%s: betterleaks.%s contains an empty rule ID", ID, field)
		}
		for _, r := range id {
			if unicode.IsSpace(r) || unicode.IsControl(r) {
				return nil, fmt.Errorf("%s: betterleaks.%s contains an invalid rule ID", ID, field)
			}
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func validateMaskByte(mask string) error {
	if utf8.RuneCountInString(mask) != 1 {
		return fmt.Errorf("%s: redaction.mask_byte must be a single ASCII byte", ID)
	}
	r, size := utf8.DecodeRuneInString(mask)
	if r == utf8.RuneError && size == 1 {
		return fmt.Errorf("%s: redaction.mask_byte must be a single ASCII byte", ID)
	}
	if size != 1 || r > 127 {
		return fmt.Errorf("%s: redaction.mask_byte must be a single ASCII byte", ID)
	}
	return nil
}

// HasSingleUserKey reports whether raw YAML is a mapping that contains the
// single_user key (document wrappers are unwrapped). Used by composition;
// DecodeConfig does not enforce access-mode rules.
func HasSingleUserKey(n yaml.Node) bool {
	root := unwrapYAMLRoot(n)
	if root.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "single_user" {
			return true
		}
	}
	return false
}

// ValidateAccessMode rejects single_user config when the effective access mode
// is multi-user. Composition calls this; DecodeConfig does not.
func ValidateAccessMode(cfg Config, multiUser bool, raw yaml.Node) error {
	if multiUser && cfg.AutoDiscoveredLocalKeys.Enabled != nil && *cfg.AutoDiscoveredLocalKeys.Enabled {
		return fmt.Errorf("%s: auto_discovered_local_keys.enabled cannot be true in multi_user mode", ID)
	}
	if multiUser && HasSingleUserKey(raw) {
		return fmt.Errorf("%s: single_user is invalid in multi_user mode", ID)
	}
	return nil
}

// CompositionOptions extracts inventory and presentation options from a
// validated Config for composition-time wiring (env catalog / redaction hints).
func CompositionOptions(cfg Config) (includePopular bool, include, exclude []string, minBytes int, auditPolicy string, maskByte byte, preservePrefix bool) {
	includePopular = cfg.SingleUser.IncludePopularEnv
	include = cfg.SingleUser.IncludeEnv
	exclude = cfg.SingleUser.ExcludeEnv
	minBytes = cfg.MinSecretBytes
	auditPolicy = cfg.AuditFailurePolicy
	preservePrefix = cfg.Redaction.PreserveKnownPrefixes
	mask := cfg.Redaction.MaskByte
	if mask == "" {
		mask = DefaultMaskByte
	}
	maskByte = mask[0]
	return includePopular, include, exclude, minBytes, auditPolicy, maskByte, preservePrefix
}

func unwrapYAMLRoot(n yaml.Node) yaml.Node {
	root := n
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return yaml.Node{}
		}
		root = *root.Content[0]
	}
	return root
}
