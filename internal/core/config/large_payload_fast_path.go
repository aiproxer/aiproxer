package config

import (
	"fmt"
	"math"
	"strings"
)

const (
	// DefaultLargePayloadThresholdBytes is the evidence-adjustable decoded-size
	// gate for fast-path consideration (design section 3, Task 19).
	DefaultLargePayloadThresholdBytes int64 = 1 << 20
	// DefaultLargePayloadMemorySpoolBytes bounds retained request bytes in Go
	// heap per captured request.
	DefaultLargePayloadMemorySpoolBytes int64 = 64 << 10
	// DefaultLargePayloadMaxInflightSpoolBytes bounds global logical spool
	// reservation across concurrent captures.
	DefaultLargePayloadMaxInflightSpoolBytes int64 = 256 << 20
	// DefaultLargePayloadMaxSemanticFactBytes bounds profile-derived metadata
	// such as normalized part shapes.
	DefaultLargePayloadMaxSemanticFactBytes int64 = 256 << 10
)

// LargePayloadFastPathConfig controls the optional large-payload streaming
// fast path (server.large_payload_fast_path, design section 3).
//
// The feature is default-off: a zero config keeps the existing canonical path
// with no spool/scanner/wire allocation beyond a trivial enabled check
// (Requirements 1, 22).
//
// threshold_bytes controls consideration only; it never proves eligibility and
// never changes the existing MaxRequestBodyBytes admission policy, whose
// defaults stay unchanged (Requirement 2).
//
// max_inflight_spool_bytes is an optimization budget, not a new
// request-admission error source: reservation exhaustion declines to the
// canonical path and never produces a new 413. Canonical fallback may still
// allocate per the pre-existing path, so this budget is not global OOM
// admission (Requirement 20).
//
// Confidentiality: spool files can contain plaintext prompt data. Operators
// must place spool_dir on a protected volume/filesystem; spool paths never
// enter logs/metrics/traces (Requirement 20).
type LargePayloadFastPathConfig struct {
	// Enabled gates the optimization. Default false.
	Enabled bool `yaml:"enabled"`
	// ThresholdBytes is the decoded-size consideration gate. Required > 0 when
	// enabled.
	ThresholdBytes int64 `yaml:"threshold_bytes"`
	// MemorySpoolBytes bounds retained request bytes in Go heap per capture.
	// Required > 0 and <= MaxInflightSpoolBytes when enabled.
	MemorySpoolBytes int64 `yaml:"memory_spool_bytes"`
	// MaxInflightSpoolBytes bounds global logical spool reservation.
	// Required > 0 when enabled. Optimization budget only (see above).
	MaxInflightSpoolBytes int64 `yaml:"max_inflight_spool_bytes"`
	// MaxSemanticFactBytes bounds profile-derived metadata. Required > 0 when
	// enabled.
	MaxSemanticFactBytes int64 `yaml:"max_semantic_fact_bytes"`
	// SpoolDir optionally overrides the spill directory. Empty selects the OS
	// default temp directory. Validated during candidate generation/reload;
	// an invalid value rejects the candidate and preserves last-good.
	SpoolDir string `yaml:"spool_dir"`
}

// EffectiveThresholdBytes returns ThresholdBytes when positive, else the default.
func (c LargePayloadFastPathConfig) EffectiveThresholdBytes() int64 {
	if c.ThresholdBytes > 0 {
		return c.ThresholdBytes
	}
	return DefaultLargePayloadThresholdBytes
}

// EffectiveMemorySpoolBytes returns MemorySpoolBytes when positive, else the default.
func (c LargePayloadFastPathConfig) EffectiveMemorySpoolBytes() int64 {
	if c.MemorySpoolBytes > 0 {
		return c.MemorySpoolBytes
	}
	return DefaultLargePayloadMemorySpoolBytes
}

// EffectiveMaxInflightSpoolBytes returns MaxInflightSpoolBytes when positive, else the default.
func (c LargePayloadFastPathConfig) EffectiveMaxInflightSpoolBytes() int64 {
	if c.MaxInflightSpoolBytes > 0 {
		return c.MaxInflightSpoolBytes
	}
	return DefaultLargePayloadMaxInflightSpoolBytes
}

// EffectiveMaxSemanticFactBytes returns MaxSemanticFactBytes when positive, else the default.
func (c LargePayloadFastPathConfig) EffectiveMaxSemanticFactBytes() int64 {
	if c.MaxSemanticFactBytes > 0 {
		return c.MaxSemanticFactBytes
	}
	return DefaultLargePayloadMaxSemanticFactBytes
}

// EffectiveSpoolDir returns the trimmed spool directory; empty means the OS
// default temp directory.
func (c LargePayloadFastPathConfig) EffectiveSpoolDir() string {
	return strings.TrimSpace(c.SpoolDir)
}

// validateLargePayloadFastPath enforces positive/overflow relationships and
// spool-directory shape during candidate generation/reload. A disabled config
// is always valid so default-off deployments see no behavior change.
func validateLargePayloadFastPath(s ServerConfig) error {
	fp := s.LargePayloadFastPath
	if !fp.Enabled {
		return nil
	}
	if fp.ThresholdBytes <= 0 {
		return fmt.Errorf("server.large_payload_fast_path.threshold_bytes: must be > 0 when enabled")
	}
	if fp.MemorySpoolBytes <= 0 {
		return fmt.Errorf("server.large_payload_fast_path.memory_spool_bytes: must be > 0 when enabled")
	}
	if fp.MaxInflightSpoolBytes <= 0 {
		return fmt.Errorf("server.large_payload_fast_path.max_inflight_spool_bytes: must be > 0 when enabled")
	}
	if fp.MaxSemanticFactBytes <= 0 {
		return fmt.Errorf("server.large_payload_fast_path.max_semantic_fact_bytes: must be > 0 when enabled")
	}
	if fp.MemorySpoolBytes > fp.MaxInflightSpoolBytes {
		return fmt.Errorf("server.large_payload_fast_path.memory_spool_bytes: must be <= max_inflight_spool_bytes")
	}
	if fp.MemorySpoolBytes > math.MaxInt64-fp.MaxSemanticFactBytes {
		return fmt.Errorf("server.large_payload_fast_path: memory plus semantic fact budgets overflow int64")
	}
	if strings.ContainsRune(fp.SpoolDir, 0) {
		return fmt.Errorf("server.large_payload_fast_path.spool_dir: must not contain NUL")
	}
	if fp.SpoolDir != "" && strings.TrimSpace(fp.SpoolDir) == "" {
		return fmt.Errorf("server.large_payload_fast_path.spool_dir: must not be blank when set")
	}
	return nil
}
