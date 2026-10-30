package compactioncontinuity

import (
	"fmt"
	"strings"
	"time"
)

func normalizeConfig(in Config, raw rawConfig) (Config, error) {
	if err := rejectExplicitZeroBounds(raw); err != nil {
		return Config{}, err
	}
	cfg := in
	defaults := defaultConfig()
	if cfg.Preserve == (PreserveConfig{}) && !raw.preservePresent {
		cfg.Preserve = defaults.Preserve
	}
	if cfg.Extractor.Timeout == 0 {
		cfg.Extractor.Timeout = defaults.Extractor.Timeout
	}
	if cfg.Extractor.MaxInputTokens == 0 {
		cfg.Extractor.MaxInputTokens = defaults.Extractor.MaxInputTokens
	}
	if cfg.Extractor.MaxOutputTokens == 0 {
		cfg.Extractor.MaxOutputTokens = defaults.Extractor.MaxOutputTokens
	}
	if cfg.Worker.MaxConcurrency == 0 {
		cfg.Worker.MaxConcurrency = defaults.Worker.MaxConcurrency
	}
	if cfg.Worker.QueueCapacity == 0 {
		cfg.Worker.QueueCapacity = defaults.Worker.QueueCapacity
	}
	if cfg.Extractor.MaxConcurrency == 0 {
		cfg.Extractor.MaxConcurrency = cfg.Worker.MaxConcurrency
	}
	if cfg.Extractor.QueueCapacity == 0 {
		cfg.Extractor.QueueCapacity = cfg.Worker.QueueCapacity
	}
	if cfg.Worker.MaxConcurrency == defaults.Worker.MaxConcurrency && cfg.Extractor.MaxConcurrency != defaults.Worker.MaxConcurrency {
		cfg.Worker.MaxConcurrency = cfg.Extractor.MaxConcurrency
	}
	if cfg.Worker.QueueCapacity == defaults.Worker.QueueCapacity && cfg.Extractor.QueueCapacity != defaults.Worker.QueueCapacity {
		cfg.Worker.QueueCapacity = cfg.Extractor.QueueCapacity
	}
	if raw.Extractor.MaxConcurrency != nil && raw.Worker.MaxConcurrency != nil && *raw.Extractor.MaxConcurrency != *raw.Worker.MaxConcurrency {
		return Config{}, fmt.Errorf("%s: extractor/worker max_concurrency values disagree", ID)
	}
	if raw.Extractor.QueueCapacity != nil && raw.Worker.QueueCapacity != nil && *raw.Extractor.QueueCapacity != *raw.Worker.QueueCapacity {
		return Config{}, fmt.Errorf("%s: extractor/worker queue_capacity values disagree", ID)
	}
	if cfg.Barrier.Timeout == 0 {
		cfg.Barrier.Timeout = defaults.Barrier.Timeout
	}
	if cfg.Capsule.MaxTokens == 0 {
		cfg.Capsule.MaxTokens = defaults.Capsule.MaxTokens
	}
	if cfg.Capsule.MaxBytes == 0 {
		cfg.Capsule.MaxBytes = defaults.Capsule.MaxBytes
	}
	if cfg.Source.TTL == 0 {
		cfg.Source.TTL = defaults.Source.TTL
	}
	if cfg.Source.MaxBytes == 0 {
		cfg.Source.MaxBytes = defaults.Source.MaxBytes
	}
	if cfg.Result.TTL == 0 {
		cfg.Result.TTL = defaults.Result.TTL
	}
	if cfg.Result.MaxBytes == 0 {
		cfg.Result.MaxBytes = defaults.Result.MaxBytes
	}
	if cfg.Result.MaxCount == 0 {
		cfg.Result.MaxCount = defaults.Result.MaxCount
	}
	if strings.TrimSpace(cfg.Failure.Mode) == "" {
		cfg.Failure.Mode = defaults.Failure.Mode
	}
	if cfg.MaxBranchEntries == 0 {
		cfg.MaxBranchEntries = defaults.MaxBranchEntries
	}

	// Explicit D18 aliases feed their canonical groups. A zero raw alias means
	// omitted; raw presence checks below reject explicit zero/negative values.
	if cfg.BarrierTimeout != 0 {
		if cfg.Barrier.Timeout != defaults.Barrier.Timeout && cfg.Barrier.Timeout != cfg.BarrierTimeout {
			return Config{}, fmt.Errorf("%s: barrier timeout values disagree", ID)
		}
		cfg.Barrier.Timeout = cfg.BarrierTimeout
	}
	if cfg.PendingResultTTL != 0 {
		if cfg.Result.TTL != defaults.Result.TTL && cfg.Result.TTL != cfg.PendingResultTTL {
			return Config{}, fmt.Errorf("%s: result/pending_result_ttl values disagree", ID)
		}
		cfg.Result.TTL = cfg.PendingResultTTL
	}
	if cfg.MaxCapsuleTokens != 0 {
		if cfg.Capsule.MaxTokens != defaults.Capsule.MaxTokens && cfg.Capsule.MaxTokens != cfg.MaxCapsuleTokens {
			return Config{}, fmt.Errorf("%s: capsule token limits disagree", ID)
		}
		cfg.Capsule.MaxTokens = cfg.MaxCapsuleTokens
	}
	if cfg.SourceTTL != 0 {
		if cfg.Source.TTL != defaults.Source.TTL && cfg.Source.TTL != cfg.SourceTTL {
			return Config{}, fmt.Errorf("%s: source ttl values disagree", ID)
		}
		cfg.Source.TTL = cfg.SourceTTL
	}
	if strings.TrimSpace(cfg.FailureMode) != "" {
		if cfg.Failure.Mode != defaults.Failure.Mode && cfg.Failure.Mode != cfg.FailureMode {
			return Config{}, fmt.Errorf("%s: failure mode values disagree", ID)
		}
		cfg.Failure.Mode = strings.TrimSpace(cfg.FailureMode)
	}
	if cfg.BranchTTL == 0 {
		cfg.BranchTTL = maxDuration(cfg.Source.TTL, cfg.Result.TTL)
	}
	if cfg.BranchTTL < maxDuration(cfg.Source.TTL, cfg.Result.TTL) {
		return Config{}, fmt.Errorf("%s: branch ttl must be at least source/result retention", ID)
	}
	if cfg.Result.TTL > cfg.Source.TTL {
		return Config{}, fmt.Errorf("%s: source ttl must be at least pending result ttl", ID)
	}
	if cfg.Extractor.Route == "inherit" {
		cfg.Extractor.Route = ""
		cfg.Extractor.Inherit = true
	}
	cfg.Extractor.Route = strings.TrimSpace(cfg.Extractor.Route)
	if raw.Extractor.Enabled == nil && raw.extractorPresent {
		cfg.Extractor.Enabled = true
	}
	if cfg.Extractor.Enabled && cfg.Extractor.Route == "" && !cfg.Extractor.Inherit {
		return Config{}, fmt.Errorf("%s: extractor requires an explicit route or inherit: true", ID)
	}
	if cfg.Extractor.Route != "" && cfg.Extractor.Inherit {
		return Config{}, fmt.Errorf("%s: extractor route and inherit cannot both be set", ID)
	}
	if err := validatePositive("extractor.timeout", cfg.Extractor.Timeout, MaxExtractorTimeout); err != nil {
		return Config{}, err
	}
	if err := validatePositiveInt("extractor.max_input_tokens", cfg.Extractor.MaxInputTokens, MaxInputTokens); err != nil {
		return Config{}, err
	}
	if err := validatePositiveInt("extractor.max_output_tokens", cfg.Extractor.MaxOutputTokens, MaxOutputTokens); err != nil {
		return Config{}, err
	}
	if err := validatePositiveInt("worker.max_concurrency", cfg.Worker.MaxConcurrency, MaxConcurrency); err != nil {
		return Config{}, err
	}
	if err := validatePositiveInt("worker.queue_capacity", cfg.Worker.QueueCapacity, MaxQueueCapacity); err != nil {
		return Config{}, err
	}
	if err := validatePositive("barrier.timeout", cfg.Barrier.Timeout, MaxBarrierTimeout); err != nil {
		return Config{}, err
	}
	if err := validatePositiveInt("capsule.max_tokens", cfg.Capsule.MaxTokens, MaxCapsuleTokens); err != nil {
		return Config{}, err
	}
	if err := validatePositiveInt("capsule.max_bytes", cfg.Capsule.MaxBytes, MaxCapsuleBytes); err != nil {
		return Config{}, err
	}
	if err := validatePositive("source.ttl", cfg.Source.TTL, MaxRetention); err != nil {
		return Config{}, err
	}
	if err := validatePositiveInt("source.max_bytes", cfg.Source.MaxBytes, MaxSourceBytes); err != nil {
		return Config{}, err
	}
	if err := validatePositive("result.ttl", cfg.Result.TTL, MaxRetention); err != nil {
		return Config{}, err
	}
	if err := validatePositiveInt("result.max_bytes", cfg.Result.MaxBytes, MaxResultBytes); err != nil {
		return Config{}, err
	}
	if err := validatePositiveInt("result.max_count", cfg.Result.MaxCount, MaxResultCount); err != nil {
		return Config{}, err
	}
	if err := validatePositive("branch_ttl", cfg.BranchTTL, MaxRetention); err != nil {
		return Config{}, err
	}
	if err := validatePositiveInt("max_branch_entries", cfg.MaxBranchEntries, MaxBranchEntries); err != nil {
		return Config{}, err
	}
	switch cfg.Failure.Mode {
	case FailureModeFailOpen, FailureModeFailClosed:
	default:
		return Config{}, fmt.Errorf("%s: failure.mode must be %q or %q", ID, FailureModeFailOpen, FailureModeFailClosed)
	}
	return syncAliases(cfg), nil
}

func rejectExplicitZeroBounds(raw rawConfig) error {
	positiveInts := []struct {
		name string
		v    *int
	}{
		{"extractor.max_input_tokens", raw.Extractor.MaxInputTokens},
		{"extractor.max_output_tokens", raw.Extractor.MaxOutputTokens},
		{"extractor.max_concurrency", raw.Extractor.MaxConcurrency},
		{"extractor.queue_capacity", raw.Extractor.QueueCapacity},
		{"worker.max_concurrency", raw.Worker.MaxConcurrency},
		{"worker.queue_capacity", raw.Worker.QueueCapacity},
		{"capsule.max_tokens", raw.Capsule.MaxTokens},
		{"capsule.max_bytes", raw.Capsule.MaxBytes},
		{"source.max_bytes", raw.Source.MaxBytes},
		{"result.max_bytes", raw.Result.MaxBytes},
		{"result.max_count", raw.Result.MaxCount},
		{"max_capsule_tokens", raw.MaxCapsuleTokens},
		{"max_branch_entries", raw.MaxBranchEntries},
	}
	for _, item := range positiveInts {
		if item.v != nil && *item.v <= 0 {
			return fmt.Errorf("%s: %s must be positive", ID, item.name)
		}
	}
	positiveDurations := []struct {
		name string
		v    time.Duration
	}{
		{"extractor.timeout", durationOrZero(raw.Extractor.Timeout)},
		{"barrier.timeout", durationOrZero(raw.Barrier.Timeout)},
		{"source.ttl", durationOrZero(raw.Source.TTL)},
		{"result.ttl", durationOrZero(raw.Result.TTL)},
		{"barrier_timeout", durationOrZero(raw.BarrierTimeout)},
		{"pending_result_ttl", durationOrZero(raw.PendingResultTTL)},
		{"source_ttl", durationOrZero(raw.SourceTTL)},
		{"branch_ttl", durationOrZero(raw.BranchTTL)},
	}
	for _, item := range positiveDurations {
		if item.v < 0 || (item.v == 0 && item.name != "") {
			// An empty duration string means omitted; parseDuration has already
			// rejected malformed non-empty values. Only explicit zero parses to 0.
			switch item.name {
			case "extractor.timeout":
				if raw.Extractor.Timeout == "" {
					continue
				}
			case "barrier.timeout":
				if raw.Barrier.Timeout == "" {
					continue
				}
			case "source.ttl":
				if raw.Source.TTL == "" {
					continue
				}
			case "result.ttl":
				if raw.Result.TTL == "" {
					continue
				}
			case "barrier_timeout":
				if raw.BarrierTimeout == "" {
					continue
				}
			case "pending_result_ttl":
				if raw.PendingResultTTL == "" {
					continue
				}
			case "source_ttl":
				if raw.SourceTTL == "" {
					continue
				}
			case "branch_ttl":
				if raw.BranchTTL == "" {
					continue
				}
			}
			return fmt.Errorf("%s: %s must be positive", ID, item.name)
		}
	}
	return nil
}

func durationOrZero(raw string) time.Duration {
	if strings.TrimSpace(raw) == "" {
		return 0
	}
	v, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return -1
	}
	return v
}
