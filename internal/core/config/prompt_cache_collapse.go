package config

import (
	"fmt"
	"time"
)

// Prompt-cache collapse diagnostic tuning. All fields are optional; an omitted
// field selects the documented default, which preserves the historical
// behavior (8000-token minimum, healthy >= 0.5, cold <= 0.1, five consecutive
// colds, 1024 tracked keys, two-hour idle expiry, seam digests off).
//
// The whole block is startup-fixed: the detector is process-owned and a reload
// that changes any effective threshold is rejected as restart-required, so a
// reload can never silently reset process-owned episode history. When metrics
// are disabled no detector runs, but the block is still validated so a latent
// typo cannot surprise a later enablement.
//
// Cost decision for seam digests: producing digests hashes the whole
// model-bound request (O(prompt bytes)) on every model-bound request, for a
// diagnostic that only fires after consecutive cold legs. The gate below keeps
// that cost off by default and bounded when enabled: at most 128 per-item
// SHA-256 hashes per leg (4 KiB), retained for the healthy reference and the
// latest leg per key (8 KiB per key at the default bound, 8 MiB process-wide
// at full occupancy with the default 1024 tracked keys; the ceiling scales
// with configured max_tracked_keys). Only the retained prefix is projected,
// so a huge trajectory pays only for the first 128 entries; each selected
// entry still costs O(entry bytes) to marshal and hash.
const (
	DefaultCollapseMinPromptTokens = 8000
	DefaultCollapseHealthyRatio    = 0.5
	DefaultCollapseColdRatio       = 0.1
	DefaultCollapseConsecutiveCold = 5
	DefaultCollapseMaxTrackedKeys  = 1024
	DefaultCollapseIdleExpiry      = 2 * time.Hour

	minCollapseMinPromptTokens = 1
	maxCollapseMinPromptTokens = 1000000
	minCollapseConsecutiveCold = 1
	maxCollapseConsecutiveCold = 1000
	minCollapseMaxTrackedKeys  = 1
	maxCollapseMaxTrackedKeys  = 1000000
	minCollapseIdleExpiry      = time.Second
	maxCollapseIdleExpiry      = 720 * time.Hour
)

// PromptCacheCollapseConfig carries typed YAML overrides for the
// process-owned prompt-cache collapse diagnostic
// (observability.metrics.prompt_cache_collapse).
type PromptCacheCollapseConfig struct {
	// MinPromptTokens floors the prompt size (input + cache-read tokens) a
	// sample must reach before it can advance an episode. Unset selects the default.
	MinPromptTokens *int `yaml:"min_prompt_tokens"`
	// HealthyRatio is the cache-hit ratio at or above which a sample arms (or
	// re-arms) an episode. Unset selects the default.
	HealthyRatio *float64 `yaml:"healthy_ratio"`
	// ColdRatio is the cache-hit ratio at or below which a sample counts as
	// cold. Must stay strictly below HealthyRatio. Unset selects the default.
	ColdRatio *float64 `yaml:"cold_ratio"`
	// ConsecutiveCold is how many consecutive cold samples trip one episode.
	// Unset selects the default.
	ConsecutiveCold *int `yaml:"consecutive_cold"`
	// MaxTrackedKeys bounds tracked (session, backend, model) keys. Unset
	// selects the default.
	MaxTrackedKeys *int `yaml:"max_tracked_keys"`
	// IdleExpiry is a Go duration string after which an untracked key's
	// episode history is reclaimed. Empty selects the default.
	IdleExpiry string `yaml:"idle_expiry"`
	// SeamDigestsEnabled gates the content-free per-item digest feed for
	// the collapse diagnostic. Off by default: when false no per-request
	// hashing runs and the diagnostic trips exactly as before, without a
	// seam index. When true the proxy hashes the model-bound request that
	// actually reaches the backend (at most 128 SHA-256 item digests per
	// leg) and the diagnostic reports the first divergent item index on
	// a collapse episode. The wire/large-body path carries no canonical
	// call and feeds no digests. The executor additionally requires
	// observability.metrics.enabled, so the flag alone never pays hashing
	// when no detector runs.
	SeamDigestsEnabled bool `yaml:"seam_digests_enabled"`
}

// CompiledPromptCacheCollapse is the validated, default-resolved projection of
// PromptCacheCollapseConfig. It carries no mutable process state.
type CompiledPromptCacheCollapse struct {
	minPromptTokens    int
	healthyRatio       float64
	coldRatio          float64
	consecutiveCold    int
	maxTrackedKeys     int
	idleExpiry         time.Duration
	seamDigestsEnabled bool
}

// CompilePromptCacheCollapse resolves defaults and validates bounds and ratio
// ordering. An explicit zero for a numeric field fails (unset is spelled by
// omitting the field), so a typo can never silently widen or mute the
// diagnostic.
func CompilePromptCacheCollapse(in PromptCacheCollapseConfig) (*CompiledPromptCacheCollapse, error) {
	const prefix = "observability.metrics.prompt_cache_collapse"
	minPrompt := DefaultCollapseMinPromptTokens
	if in.MinPromptTokens != nil {
		minPrompt = *in.MinPromptTokens
	}
	if minPrompt < minCollapseMinPromptTokens || minPrompt > maxCollapseMinPromptTokens {
		return nil, fmt.Errorf("%s.min_prompt_tokens: must be between %d and %d, got %d",
			prefix, minCollapseMinPromptTokens, maxCollapseMinPromptTokens, minPrompt)
	}
	healthy := DefaultCollapseHealthyRatio
	if in.HealthyRatio != nil {
		healthy = *in.HealthyRatio
	}
	if healthy <= 0 || healthy > 1 {
		return nil, fmt.Errorf("%s.healthy_ratio: must be > 0 and <= 1, got %v", prefix, healthy)
	}
	cold := DefaultCollapseColdRatio
	if in.ColdRatio != nil {
		cold = *in.ColdRatio
	}
	if cold < 0 || cold >= 1 {
		return nil, fmt.Errorf("%s.cold_ratio: must be >= 0 and < 1, got %v", prefix, cold)
	}
	if cold >= healthy {
		return nil, fmt.Errorf("%s.cold_ratio: must be < healthy_ratio (%v), got %v", prefix, healthy, cold)
	}
	run := DefaultCollapseConsecutiveCold
	if in.ConsecutiveCold != nil {
		run = *in.ConsecutiveCold
	}
	if run < minCollapseConsecutiveCold || run > maxCollapseConsecutiveCold {
		return nil, fmt.Errorf("%s.consecutive_cold: must be between %d and %d, got %d",
			prefix, minCollapseConsecutiveCold, maxCollapseConsecutiveCold, run)
	}
	maxKeys := DefaultCollapseMaxTrackedKeys
	if in.MaxTrackedKeys != nil {
		maxKeys = *in.MaxTrackedKeys
	}
	if maxKeys < minCollapseMaxTrackedKeys || maxKeys > maxCollapseMaxTrackedKeys {
		return nil, fmt.Errorf("%s.max_tracked_keys: must be between %d and %d, got %d",
			prefix, minCollapseMaxTrackedKeys, maxCollapseMaxTrackedKeys, maxKeys)
	}
	idle, err := parsePositiveDurationOrDefault(in.IdleExpiry, DefaultCollapseIdleExpiry, prefix+".idle_expiry")
	if err != nil {
		return nil, err
	}
	if idle < minCollapseIdleExpiry || idle > maxCollapseIdleExpiry {
		return nil, fmt.Errorf("%s.idle_expiry: must be between %s and %s, got %s",
			prefix, minCollapseIdleExpiry, maxCollapseIdleExpiry, idle)
	}
	return &CompiledPromptCacheCollapse{
		minPromptTokens:    minPrompt,
		healthyRatio:       healthy,
		coldRatio:          cold,
		consecutiveCold:    run,
		maxTrackedKeys:     maxKeys,
		idleExpiry:         idle,
		seamDigestsEnabled: in.SeamDigestsEnabled,
	}, nil
}

// MinPromptTokens returns the resolved minimum prompt size in tokens.
func (c *CompiledPromptCacheCollapse) MinPromptTokens() int {
	if c == nil {
		return DefaultCollapseMinPromptTokens
	}
	return c.minPromptTokens
}

// HealthyRatio returns the resolved healthy hit-ratio threshold.
func (c *CompiledPromptCacheCollapse) HealthyRatio() float64 {
	if c == nil {
		return DefaultCollapseHealthyRatio
	}
	return c.healthyRatio
}

// ColdRatio returns the resolved cold hit-ratio threshold.
func (c *CompiledPromptCacheCollapse) ColdRatio() float64 {
	if c == nil {
		return DefaultCollapseColdRatio
	}
	return c.coldRatio
}

// ConsecutiveCold returns the resolved consecutive-cold trip count.
func (c *CompiledPromptCacheCollapse) ConsecutiveCold() int {
	if c == nil {
		return DefaultCollapseConsecutiveCold
	}
	return c.consecutiveCold
}

// MaxTrackedKeys returns the resolved tracked-key capacity.
func (c *CompiledPromptCacheCollapse) MaxTrackedKeys() int {
	if c == nil {
		return DefaultCollapseMaxTrackedKeys
	}
	return c.maxTrackedKeys
}

// IdleExpiry returns the resolved idle-key expiry.
func (c *CompiledPromptCacheCollapse) IdleExpiry() time.Duration {
	if c == nil {
		return DefaultCollapseIdleExpiry
	}
	return c.idleExpiry
}

// SeamDigestsEnabled reports whether the per-item digest feed runs.
// Off by default; enabling it pays one bounded hash pass per model-bound
// request so collapse episodes can report the first divergent item index.
func (c *CompiledPromptCacheCollapse) SeamDigestsEnabled() bool {
	if c == nil {
		return false
	}
	return c.seamDigestsEnabled
}

// EffectiveSeamDigestsEnabled reports whether the executor should hash the
// model-bound request per B-leg. Both switches must agree: the explicit
// per-feed gate and the metrics switch that owns the collapse detector. An
// invalid collapse block disables the feed rather than failing the caller;
// config validation already rejects it at startup.
//
// This lives in the config package (not the composition root) because it is
// config policy: interpreting typed config into an effective toggle.
func EffectiveSeamDigestsEnabled(cfg *Config) bool {
	if cfg == nil || !cfg.Observability.Metrics.Enabled {
		return false
	}
	compiled, err := CompilePromptCacheCollapse(cfg.Observability.Metrics.PromptCacheCollapse)
	if err != nil {
		return false
	}
	return compiled.SeamDigestsEnabled()
}
