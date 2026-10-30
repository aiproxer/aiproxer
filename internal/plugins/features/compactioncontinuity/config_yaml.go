package compactioncontinuity

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// DecodeConfig decodes one feature-private YAML subtree. Empty/null config
// receives safe bounded defaults; unknown nested keys are rejected before
// decoding so typos cannot silently widen a billable egress policy.
func DecodeConfig(n yaml.Node) (Config, error) {
	root := unwrap(n)
	if root.Kind == 0 || isNull(root) {
		return defaultConfig(), nil
	}
	if root.Kind != yaml.MappingNode {
		return Config{}, fmt.Errorf("%s: config must be a mapping or null", ID)
	}
	if err := rejectUnknown(root); err != nil {
		return Config{}, err
	}
	var raw rawConfig
	raw.preservePresent = mappingHasNonNull(root, "preserve")
	raw.extractorPresent = mappingHasNonNull(root, "extractor")
	if err := root.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("%s: %w", ID, err)
	}
	cfg, err := raw.toConfig()
	if err != nil {
		return Config{}, err
	}
	return normalizeConfig(cfg, raw)
}

type rawConfig struct {
	Preserve  PreserveConfig `yaml:"preserve"`
	Extractor rawExtractor   `yaml:"extractor"`
	Worker    rawWorker      `yaml:"worker"`
	Barrier   rawBarrier     `yaml:"barrier"`
	Capsule   rawCapsule     `yaml:"capsule"`
	Source    rawSource      `yaml:"source"`
	Result    rawResult      `yaml:"result"`
	Failure   rawFailure     `yaml:"failure"`

	BarrierTimeout   string `yaml:"barrier_timeout"`
	PendingResultTTL string `yaml:"pending_result_ttl"`
	MaxCapsuleTokens *int   `yaml:"max_capsule_tokens"`
	SourceTTL        string `yaml:"source_ttl"`
	FailureMode      string `yaml:"failure_mode"`
	BranchTTL        string `yaml:"branch_ttl"`
	MaxBranchEntries *int   `yaml:"max_branch_entries"`

	extractorPresent bool
	preservePresent  bool
}

type rawExtractor struct {
	Enabled         *bool  `yaml:"enabled"`
	Route           string `yaml:"route"`
	Inherit         bool   `yaml:"inherit"`
	Timeout         string `yaml:"timeout"`
	MaxInputTokens  *int   `yaml:"max_input_tokens"`
	MaxOutputTokens *int   `yaml:"max_output_tokens"`
	MaxConcurrency  *int   `yaml:"max_concurrency"`
	QueueCapacity   *int   `yaml:"queue_capacity"`
}

type rawWorker struct {
	MaxConcurrency *int `yaml:"max_concurrency"`
	QueueCapacity  *int `yaml:"queue_capacity"`
}

type rawBarrier struct {
	Timeout string `yaml:"timeout"`
}

type rawCapsule struct {
	MaxTokens *int `yaml:"max_tokens"`
	MaxBytes  *int `yaml:"max_bytes"`
}

type rawSource struct {
	TTL      string `yaml:"ttl"`
	MaxBytes *int   `yaml:"max_bytes"`
}

type rawResult struct {
	TTL      string `yaml:"ttl"`
	MaxBytes *int   `yaml:"max_bytes"`
	MaxCount *int   `yaml:"max_count"`
}

type rawFailure struct {
	Mode string `yaml:"mode"`
}

func (r rawConfig) toConfig() (Config, error) {
	cfg := defaultConfig()
	// Keep aliases zero while decoding so an omitted flattened key does not
	// overwrite an explicitly configured nested group during normalization.
	cfg.BarrierTimeout, cfg.PendingResultTTL, cfg.MaxCapsuleTokens = 0, 0, 0
	cfg.SourceTTL, cfg.FailureMode, cfg.BranchTTL, cfg.MaxBranchEntries = 0, "", 0, 0
	if r.preservePresent {
		cfg.Preserve = r.Preserve
	}
	cfg.Extractor.Route = strings.TrimSpace(r.Extractor.Route)
	cfg.Extractor.Inherit = r.Extractor.Inherit
	if r.Extractor.Enabled != nil {
		cfg.Extractor.Enabled = *r.Extractor.Enabled
	}
	if r.Extractor.Timeout != "" {
		v, err := parseDuration("extractor.timeout", r.Extractor.Timeout)
		if err != nil {
			return Config{}, err
		}
		cfg.Extractor.Timeout = v
	}
	if r.Extractor.MaxInputTokens != nil {
		cfg.Extractor.MaxInputTokens = *r.Extractor.MaxInputTokens
	}
	if r.Extractor.MaxOutputTokens != nil {
		cfg.Extractor.MaxOutputTokens = *r.Extractor.MaxOutputTokens
	}
	if r.Extractor.MaxConcurrency != nil {
		cfg.Extractor.MaxConcurrency = *r.Extractor.MaxConcurrency
	}
	if r.Extractor.QueueCapacity != nil {
		cfg.Extractor.QueueCapacity = *r.Extractor.QueueCapacity
	}
	if r.Worker.MaxConcurrency != nil {
		cfg.Worker.MaxConcurrency = *r.Worker.MaxConcurrency
	}
	if r.Worker.QueueCapacity != nil {
		cfg.Worker.QueueCapacity = *r.Worker.QueueCapacity
	}
	if r.Barrier.Timeout != "" {
		v, err := parseDuration("barrier.timeout", r.Barrier.Timeout)
		if err != nil {
			return Config{}, err
		}
		cfg.Barrier.Timeout = v
	}
	if r.Capsule.MaxTokens != nil {
		cfg.Capsule.MaxTokens = *r.Capsule.MaxTokens
	}
	if r.Capsule.MaxBytes != nil {
		cfg.Capsule.MaxBytes = *r.Capsule.MaxBytes
	}
	if r.Source.TTL != "" {
		v, err := parseDuration("source.ttl", r.Source.TTL)
		if err != nil {
			return Config{}, err
		}
		cfg.Source.TTL = v
	}
	if r.Source.MaxBytes != nil {
		cfg.Source.MaxBytes = *r.Source.MaxBytes
	}
	if r.Result.TTL != "" {
		v, err := parseDuration("result.ttl", r.Result.TTL)
		if err != nil {
			return Config{}, err
		}
		cfg.Result.TTL = v
	}
	if r.Result.MaxBytes != nil {
		cfg.Result.MaxBytes = *r.Result.MaxBytes
	}
	if r.Result.MaxCount != nil {
		cfg.Result.MaxCount = *r.Result.MaxCount
	}
	if r.Failure.Mode != "" {
		cfg.Failure.Mode = strings.TrimSpace(r.Failure.Mode)
	}
	if r.BarrierTimeout != "" {
		v, err := parseDuration("barrier_timeout", r.BarrierTimeout)
		if err != nil {
			return Config{}, err
		}
		cfg.BarrierTimeout = v
	}
	if r.PendingResultTTL != "" {
		v, err := parseDuration("pending_result_ttl", r.PendingResultTTL)
		if err != nil {
			return Config{}, err
		}
		cfg.PendingResultTTL = v
	}
	if r.MaxCapsuleTokens != nil {
		cfg.MaxCapsuleTokens = *r.MaxCapsuleTokens
	}
	if r.SourceTTL != "" {
		v, err := parseDuration("source_ttl", r.SourceTTL)
		if err != nil {
			return Config{}, err
		}
		cfg.SourceTTL = v
	}
	if r.FailureMode != "" {
		cfg.FailureMode = strings.TrimSpace(r.FailureMode)
	}
	if r.BranchTTL != "" {
		v, err := parseDuration("branch_ttl", r.BranchTTL)
		if err != nil {
			return Config{}, err
		}
		cfg.BranchTTL = v
	}
	if r.MaxBranchEntries != nil {
		cfg.MaxBranchEntries = *r.MaxBranchEntries
	}
	return cfg, nil
}

func unwrap(n yaml.Node) yaml.Node {
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return yaml.Node{}
		}
		return *n.Content[0]
	}
	return n
}

func mappingHasNonNull(root yaml.Node, want string) bool {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == want && !isNull(*root.Content[i+1]) {
			return true
		}
	}
	return false
}

func isNull(n yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && (n.Tag == "!!null" || strings.TrimSpace(n.Value) == "null" || strings.TrimSpace(n.Value) == "")
}

func rejectUnknown(root yaml.Node) error {
	allowed := map[string]bool{
		"preserve": true, "extractor": true, "worker": true, "barrier": true,
		"capsule": true, "source": true, "result": true, "failure": true,
		"barrier_timeout": true, "pending_result_ttl": true, "max_capsule_tokens": true,
		"source_ttl": true, "failure_mode": true, "branch_ttl": true, "max_branch_entries": true,
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		if !allowed[key] {
			return fmt.Errorf("%s: unknown config key %q", ID, key)
		}
		if isNestedConfigKey(key) {
			if err := rejectNested(key, *root.Content[i+1]); err != nil {
				return err
			}
		}
	}
	return nil
}

func isNestedConfigKey(key string) bool {
	switch key {
	case "preserve", "extractor", "worker", "barrier", "capsule", "source", "result", "failure":
		return true
	default:
		return false
	}
}

func rejectNested(prefix string, n yaml.Node) error {
	n = unwrap(n)
	if n.Kind == 0 || isNull(n) {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: %s must be a mapping or null", ID, prefix)
	}
	allowed := map[string]map[string]bool{
		"preserve":  {"plan": true, "user_decisions": true, "constraints": true, "rationale": true, "rejected_alternatives": true},
		"extractor": {"enabled": true, "route": true, "inherit": true, "timeout": true, "max_input_tokens": true, "max_output_tokens": true, "max_concurrency": true, "queue_capacity": true},
		"worker":    {"max_concurrency": true, "queue_capacity": true},
		"barrier":   {"timeout": true},
		"capsule":   {"max_tokens": true, "max_bytes": true},
		"source":    {"ttl": true, "max_bytes": true},
		"result":    {"ttl": true, "max_bytes": true, "max_count": true},
		"failure":   {"mode": true},
	}
	keys := allowed[prefix]
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		if !keys[key] {
			return fmt.Errorf("%s: unknown config key %q", ID, prefix+"."+key)
		}
	}
	return nil
}
