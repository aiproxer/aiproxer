package compactioncontinuity

import (
	"fmt"
	"strings"
	"time"
)

// ID is the official standard-distribution feature factory id.
const ID = "compaction-continuity"

const (
	FailureModeFailOpen   = "fail_open"
	FailureModeFailClosed = "fail_closed"

	DefaultExtractorTimeout = 8 * time.Second
	DefaultMaxInputTokens   = 12_000
	DefaultMaxOutputTokens  = 2_000
	DefaultMaxConcurrency   = 2
	DefaultQueueCapacity    = 16
	DefaultBarrierTimeout   = 2 * time.Second
	DefaultPendingResultTTL = 2 * time.Hour
	DefaultMaxCapsuleTokens = 2_500
	DefaultMaxCapsuleBytes  = 1 << 20
	DefaultSourceTTL        = 2 * time.Hour
	DefaultMaxSourceBytes   = 4 << 20
	DefaultResultMaxBytes   = 4 << 20
	DefaultResultMaxCount   = 16
	DefaultMaxBranchEntries = 1024
	DefaultBranchTTL        = 2 * time.Hour
	MaxExtractorTimeout     = 24 * time.Hour
	MaxInputTokens          = 1_000_000
	MaxOutputTokens         = 1_000_000
	MaxConcurrency          = 128
	MaxQueueCapacity        = 4_096
	MaxBarrierTimeout       = 24 * time.Hour
	MaxRetention            = 30 * 24 * time.Hour
	MaxCapsuleTokens        = 100_000
	MaxCapsuleBytes         = 64 << 20
	MaxSourceBytes          = 64 << 20
	MaxResultBytes          = 64 << 20
	MaxResultCount          = 4_096
	MaxBranchEntries        = 65_536
)

// PreserveConfig selects deterministic categories retained in a capsule.
type PreserveConfig struct {
	Plan                 bool `yaml:"plan"`
	UserDecisions        bool `yaml:"user_decisions"`
	Constraints          bool `yaml:"constraints"`
	Rationale            bool `yaml:"rationale"`
	RejectedAlternatives bool `yaml:"rejected_alternatives"`
}

// ExtractorConfig is the immutable semantic-extractor policy captured by a
// generation and copied into each submitted background job.
type ExtractorConfig struct {
	Enabled         bool
	Route           string
	Inherit         bool
	Timeout         time.Duration
	MaxInputTokens  int
	MaxOutputTokens int
	// These aliases mirror the original D18 shape. Worker is the canonical
	// composition group; both views are kept equal after normalization.
	MaxConcurrency int
	QueueCapacity  int
}

// WorkerConfig bounds process-owned background admission.
type WorkerConfig struct {
	MaxConcurrency int
	QueueCapacity  int
}

// BarrierConfig controls the bounded wait for an already-submitted result.
type BarrierConfig struct {
	Timeout time.Duration
}

// CapsuleConfig bounds serialized continuity state.
type CapsuleConfig struct {
	MaxTokens int
	MaxBytes  int
}

// SourceConfig bounds the sanitized source window retained for later turns.
type SourceConfig struct {
	TTL      time.Duration
	MaxBytes int
}

// ResultConfig bounds raw background results before validation and merge.
type ResultConfig struct {
	TTL      time.Duration
	MaxBytes int
	MaxCount int
}

// FailureConfig selects request-time preservation failure behavior.
type FailureConfig struct {
	Mode string
}

// Config is the validated, generation-local compaction-continuity policy.
// All fields are values (no maps, slices, or mutable service handles), so a
// Snapshot is safe to retain for an in-flight submission across reload.
type Config struct {
	Preserve  PreserveConfig
	Extractor ExtractorConfig
	Worker    WorkerConfig
	Barrier   BarrierConfig
	Capsule   CapsuleConfig
	Source    SourceConfig
	Result    ResultConfig
	Failure   FailureConfig

	// Flattened aliases retain the D18 operator-facing names and make retention
	// policy explicit to callers. Normalize keeps aliases equal to their group.
	BarrierTimeout   time.Duration
	PendingResultTTL time.Duration
	MaxCapsuleTokens int
	SourceTTL        time.Duration
	FailureMode      string
	BranchTTL        time.Duration
	MaxBranchEntries int
}

// Prerequisites describes the capabilities available at generation
// composition. The feature package deliberately receives capabilities as
// booleans rather than importing runtime or process-owned service types.
type Prerequisites struct {
	DetectorPreview   bool
	DetectorCommit    bool
	BranchCoordinator bool
	BackgroundAux     bool
}

// Validate returns an error for an already materialized Config. DecodeConfig
// should be preferred for YAML because it distinguishes omitted from explicit
// zero bounds.
func (c Config) Validate() error {
	_, err := normalizeConfig(c, rawConfig{})
	return err
}

// Normalize returns a complete bounded value copy. It is useful to callers
// constructing config in tests or composition code without YAML.
func (c Config) Normalize() (Config, error) {
	return normalizeConfig(c, rawConfig{})
}

// Snapshot returns the immutable per-generation/submission value copy.
func (c Config) Snapshot() Config { return c }

// ValidatePrerequisites applies the enabled-feature dependency gate. Disabled
// registrations never call this function, so they remain a no-op even with
// missing extractor configuration or process capabilities.
func ValidatePrerequisites(c Config, p Prerequisites) error {
	_, err := c.Normalize()
	if err != nil {
		return err
	}
	missing := make([]string, 0, 4)
	if !p.DetectorPreview {
		missing = append(missing, "compaction detector preview")
	}
	if !p.DetectorCommit {
		missing = append(missing, "compaction detector commit")
	}
	if !p.BranchCoordinator {
		missing = append(missing, "branch coordinator")
	}
	if !p.BackgroundAux {
		missing = append(missing, "background auxiliary")
	}
	if len(missing) != 0 {
		return fmt.Errorf("%s: enabled composition prerequisite missing: %s", ID, strings.Join(missing, ", "))
	}
	return nil
}
