package compactioncontinuity

import (
	"fmt"
	"strings"
	"time"
)

func defaultConfig() Config {
	cfg := Config{
		Preserve: PreserveConfig{
			Plan: true, UserDecisions: true, Constraints: true,
			Rationale: true, RejectedAlternatives: true,
		},
		Extractor: ExtractorConfig{
			Timeout: DefaultExtractorTimeout, MaxInputTokens: DefaultMaxInputTokens,
			MaxOutputTokens: DefaultMaxOutputTokens,
		},
		Worker:           WorkerConfig{MaxConcurrency: DefaultMaxConcurrency, QueueCapacity: DefaultQueueCapacity},
		Barrier:          BarrierConfig{Timeout: DefaultBarrierTimeout},
		Capsule:          CapsuleConfig{MaxTokens: DefaultMaxCapsuleTokens, MaxBytes: DefaultMaxCapsuleBytes},
		Source:           SourceConfig{TTL: DefaultSourceTTL, MaxBytes: DefaultMaxSourceBytes},
		Result:           ResultConfig{TTL: DefaultPendingResultTTL, MaxBytes: DefaultResultMaxBytes, MaxCount: DefaultResultMaxCount},
		Failure:          FailureConfig{Mode: FailureModeFailOpen},
		MaxBranchEntries: DefaultMaxBranchEntries,
		BranchTTL:        DefaultBranchTTL,
	}
	return syncAliases(cfg)
}

func syncAliases(cfg Config) Config {
	cfg.Extractor.MaxConcurrency = cfg.Worker.MaxConcurrency
	cfg.Extractor.QueueCapacity = cfg.Worker.QueueCapacity
	cfg.BarrierTimeout = cfg.Barrier.Timeout
	cfg.PendingResultTTL = cfg.Result.TTL
	cfg.MaxCapsuleTokens = cfg.Capsule.MaxTokens
	cfg.SourceTTL = cfg.Source.TTL
	cfg.FailureMode = cfg.Failure.Mode
	return cfg
}

func validatePositive(name string, value, max time.Duration) error {
	if value <= 0 {
		return fmt.Errorf("%s: %s must be positive", ID, name)
	}
	if value > max {
		return fmt.Errorf("%s: %s exceeds maximum %s", ID, name, max)
	}
	return nil
}

func validatePositiveInt(name string, value, max int) error {
	if value <= 0 {
		return fmt.Errorf("%s: %s must be positive", ID, name)
	}
	if value > max {
		return fmt.Errorf("%s: %s exceeds maximum %d", ID, name, max)
	}
	return nil
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func parseDuration(name, raw string) (time.Duration, error) {
	v, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("%s: %s: %w", ID, name, err)
	}
	return v, nil
}
