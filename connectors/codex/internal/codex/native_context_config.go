package codex

import (
	"fmt"
	"time"
)

// ContinuityMode defines how reasoning restoration interacts with native compaction.
type ContinuityMode string

const (
	ContinuityRequired   ContinuityMode = "required"
	ContinuityBestEffort ContinuityMode = "best_effort"
	ContinuityDisabled   ContinuityMode = "disabled"
)

const (
	DefaultRetainedMessageTokens int64         = 64000
	DefaultMinSavingsTokens      int64         = 8192
	DefaultStateTTL              time.Duration = 1 * time.Hour
	DefaultMaxEntries            int           = 1024
	DefaultMaxEntryBytes         int           = 1048576 // 1 MB
	DefaultFailureCooldown       time.Duration = 5 * time.Minute
)

const (
	MaxRetainedMessageTokensBound int64         = 512000
	MaxEntriesBound               int           = 10000
	MaxEntryBytesBound            int           = 16777216 // 16 MB
	MaxStateTTLBound              time.Duration = 24 * time.Hour
	MaxFailureCooldownBound       time.Duration = 1 * time.Hour
)

// NativeContextConfig configures experimental native reasoning continuity
// and Responses Compaction V2 for the direct Codex backend connector.
type NativeContextConfig struct {
	Enabled                      bool                   `yaml:"enabled" json:"enabled"`
	RequestEncryptedReasoning    bool                   `yaml:"request_encrypted_reasoning" json:"request_encrypted_reasoning"`
	ReasoningContinuity          ContinuityMode         `yaml:"reasoning_continuity" json:"reasoning_continuity"`
	Compaction                   NativeCompactionConfig `yaml:"compaction" json:"compaction"`
	compactionSet                bool
	requestEncryptedSet          bool
	CompactionSet                bool   `yaml:"-" json:"-"`
	RequestEncryptedReasoningSet bool   `yaml:"-" json:"-"`
	EnabledSet                   bool   `yaml:"-" json:"-"`
	Source                       string `yaml:"-" json:"-"`
}

// DefaultNativeContextConfig returns the complete direct-Codex default mode.
func DefaultNativeContextConfig() NativeContextConfig {
	return NativeContextConfig{
		Enabled: true, RequestEncryptedReasoning: true, ReasoningContinuity: ContinuityRequired,
		Source: "default",
		Compaction: NativeCompactionConfig{
			Enabled: true, RetainedMessageTokens: DefaultRetainedMessageTokens,
			MinSavingsTokens: DefaultMinSavingsTokens, StateTTL: DefaultStateTTL, MaxEntries: DefaultMaxEntries,
			MaxEntryBytes: DefaultMaxEntryBytes, FailureCooldown: DefaultFailureCooldown,
		},
	}
}

// SetCompactionPresentForYAML records that the nested block was supplied.
// It is intentionally private to the connector configuration seam.
func (c *NativeContextConfig) SetCompactionPresentForYAML() {
	if c != nil {
		c.compactionSet = true
		c.CompactionSet = true
	}
}

// SetRequestEncryptedPresentForYAML preserves an explicit YAML false value.
func (c *NativeContextConfig) SetRequestEncryptedPresentForYAML() {
	if c != nil {
		c.requestEncryptedSet = true
		c.RequestEncryptedReasoningSet = true
	}
}

// SetEnabledPresentForYAML records an explicit enabled value, including false.
func (c *NativeContextConfig) SetEnabledPresentForYAML() {
	if c != nil {
		c.EnabledSet = true
	}
}

// HasNonDefaultSettings reports whether the block requests native behavior.
// An explicit enabled:false remains the harmless default-off configuration.
func (c *NativeContextConfig) HasNonDefaultSettings() bool {
	return c != nil && (c.EnabledSet || c.Enabled || c.requestEncryptedSet || c.RequestEncryptedReasoningSet || c.ReasoningContinuity != "" || c.compactionSet || c.CompactionSet || c.Compaction.Enabled)
}

// NativeCompactionConfig configures bounded process-local checkpoint state
// and compaction planning limits.
type NativeCompactionConfig struct {
	Enabled               bool          `yaml:"enabled" json:"enabled"`
	TriggerTokens         int64         `yaml:"trigger_tokens" json:"trigger_tokens"`
	RetainedMessageTokens int64         `yaml:"retained_message_tokens" json:"retained_message_tokens"`
	MinSavingsTokens      int64         `yaml:"min_savings_tokens" json:"min_savings_tokens"`
	StateTTL              time.Duration `yaml:"state_ttl" json:"state_ttl"`
	MaxEntries            int           `yaml:"max_entries" json:"max_entries"`
	MaxEntryBytes         int           `yaml:"max_entry_bytes" json:"max_entry_bytes"`
	FailureCooldown       time.Duration `yaml:"failure_cooldown" json:"failure_cooldown"`
}

// CompactionEnabled returns true if native context and compaction are both enabled.
func (c *NativeContextConfig) CompactionEnabled() bool {
	return c != nil && c.Enabled && c.Compaction.Enabled
}

// EffectiveMode returns the active evaluation mode for diagnostics.
func (c *NativeContextConfig) EffectiveMode() string {
	if c == nil || !c.Enabled {
		return "disabled"
	}
	if c.RequestEncryptedReasoning && c.Compaction.Enabled {
		return "both"
	}
	if c.RequestEncryptedReasoning && !c.Compaction.Enabled {
		return "reasoning_only"
	}
	if !c.RequestEncryptedReasoning && c.Compaction.Enabled {
		return "compaction_only"
	}
	return "neither"
}

// NormalizeAndValidate returns a validated copy of cfg with defaults applied.
func (c NativeContextConfig) NormalizeAndValidate() (NativeContextConfig, error) {
	norm := c
	if !norm.Enabled {
		// The top-level opt-out is complete when no nested compaction block was
		// supplied. Preserve an explicitly enabled nested block as an error below.
		if !norm.compactionSet && !norm.CompactionSet {
			norm.Compaction.Enabled = false
		}
		if norm.Compaction.Enabled {
			return NativeContextConfig{}, fmt.Errorf("%s: compaction.enabled cannot be true when native_context.enabled is false", ID)
		}
		if err := validateNativeContextValues(norm); err != nil {
			return NativeContextConfig{}, err
		}
		return NativeContextConfig{Enabled: false}, nil
	}

	// Omitted fields inherit the complete direct-Codex default. Presence bits
	// keep explicit false values from being mistaken for omitted fields.
	if norm.ReasoningContinuity == "" {
		norm.ReasoningContinuity = ContinuityRequired
	}
	if !norm.requestEncryptedSet && !norm.RequestEncryptedReasoningSet && norm.ReasoningContinuity == ContinuityRequired {
		norm.RequestEncryptedReasoning = true
	}
	if !norm.compactionSet && !norm.CompactionSet {
		// A disabled continuity mode is the established explicit evaluation
		// spelling for a no-reasoning/no-compaction mode in direct Go config.
		if norm.ReasoningContinuity != ContinuityDisabled {
			norm.Compaction.Enabled = true
		}
	}
	if norm.Compaction.Enabled {
		if norm.Compaction.RetainedMessageTokens == 0 {
			norm.Compaction.RetainedMessageTokens = DefaultRetainedMessageTokens
		}
		if norm.Compaction.MinSavingsTokens == 0 {
			norm.Compaction.MinSavingsTokens = DefaultMinSavingsTokens
		}
		if norm.Compaction.StateTTL == 0 {
			norm.Compaction.StateTTL = DefaultStateTTL
		}
		if norm.Compaction.MaxEntries == 0 {
			norm.Compaction.MaxEntries = DefaultMaxEntries
		}
		if norm.Compaction.MaxEntryBytes == 0 {
			norm.Compaction.MaxEntryBytes = DefaultMaxEntryBytes
		}
		if norm.Compaction.FailureCooldown == 0 {
			norm.Compaction.FailureCooldown = DefaultFailureCooldown
		}
	}

	return validateNativeContext(norm)
}

func validateNativeContext(norm NativeContextConfig) (NativeContextConfig, error) {
	// Validation
	switch norm.ReasoningContinuity {
	case "", ContinuityRequired, ContinuityBestEffort, ContinuityDisabled:
	default:
		return NativeContextConfig{}, fmt.Errorf("%s: unknown reasoning_continuity mode %q", ID, norm.ReasoningContinuity)
	}

	if norm.Compaction.TriggerTokens < 0 {
		return NativeContextConfig{}, fmt.Errorf("%s: compaction.trigger_tokens cannot be negative (%d)", ID, norm.Compaction.TriggerTokens)
	}
	if norm.Compaction.RetainedMessageTokens < 0 {
		return NativeContextConfig{}, fmt.Errorf("%s: compaction.retained_message_tokens cannot be negative (%d)", ID, norm.Compaction.RetainedMessageTokens)
	}
	if norm.Compaction.MinSavingsTokens < 0 {
		return NativeContextConfig{}, fmt.Errorf("%s: compaction.min_savings_tokens cannot be negative (%d)", ID, norm.Compaction.MinSavingsTokens)
	}
	if norm.Compaction.StateTTL < 0 {
		return NativeContextConfig{}, fmt.Errorf("%s: compaction.state_ttl cannot be negative (%v)", ID, norm.Compaction.StateTTL)
	}
	if norm.Compaction.MaxEntries < 0 {
		return NativeContextConfig{}, fmt.Errorf("%s: compaction.max_entries cannot be negative (%d)", ID, norm.Compaction.MaxEntries)
	}
	if norm.Compaction.MaxEntryBytes < 0 {
		return NativeContextConfig{}, fmt.Errorf("%s: compaction.max_entry_bytes cannot be negative (%d)", ID, norm.Compaction.MaxEntryBytes)
	}
	if norm.Compaction.FailureCooldown < 0 {
		return NativeContextConfig{}, fmt.Errorf("%s: compaction.failure_cooldown cannot be negative (%v)", ID, norm.Compaction.FailureCooldown)
	}

	// Hard caps
	if norm.Compaction.RetainedMessageTokens > MaxRetainedMessageTokensBound {
		return NativeContextConfig{}, fmt.Errorf("%s: compaction.retained_message_tokens (%d) exceeds safety cap (%d)", ID, norm.Compaction.RetainedMessageTokens, MaxRetainedMessageTokensBound)
	}
	if norm.Compaction.MaxEntries > MaxEntriesBound {
		return NativeContextConfig{}, fmt.Errorf("%s: compaction.max_entries (%d) exceeds safety cap (%d)", ID, norm.Compaction.MaxEntries, MaxEntriesBound)
	}
	if norm.Compaction.MaxEntryBytes > MaxEntryBytesBound {
		return NativeContextConfig{}, fmt.Errorf("%s: compaction.max_entry_bytes (%d) exceeds safety cap (%d)", ID, norm.Compaction.MaxEntryBytes, MaxEntryBytesBound)
	}
	if norm.Compaction.StateTTL > MaxStateTTLBound {
		return NativeContextConfig{}, fmt.Errorf("%s: compaction.state_ttl (%v) exceeds safety cap (%v)", ID, norm.Compaction.StateTTL, MaxStateTTLBound)
	}
	if norm.Compaction.FailureCooldown > MaxFailureCooldownBound {
		return NativeContextConfig{}, fmt.Errorf("%s: compaction.failure_cooldown (%v) exceeds safety cap (%v)", ID, norm.Compaction.FailureCooldown, MaxFailureCooldownBound)
	}

	// Inconsistency checks
	if norm.Compaction.Enabled && norm.ReasoningContinuity == ContinuityRequired && !norm.RequestEncryptedReasoning {
		return NativeContextConfig{}, fmt.Errorf("%s: required reasoning_continuity mode for compaction requires request_encrypted_reasoning", ID)
	}
	if norm.Compaction.TriggerTokens > 0 {
		if norm.Compaction.TriggerTokens < norm.Compaction.MinSavingsTokens {
			return NativeContextConfig{}, fmt.Errorf("%s: compaction.trigger_tokens (%d) must be >= min_savings_tokens (%d)", ID, norm.Compaction.TriggerTokens, norm.Compaction.MinSavingsTokens)
		}
		if norm.Compaction.TriggerTokens <= norm.Compaction.RetainedMessageTokens {
			return NativeContextConfig{}, fmt.Errorf("%s: compaction.trigger_tokens (%d) must be > retained_message_tokens (%d)", ID, norm.Compaction.TriggerTokens, norm.Compaction.RetainedMessageTokens)
		}
	}

	return norm, nil
}

func validateNativeContextValues(norm NativeContextConfig) error {
	_, err := validateNativeContext(norm)
	return err
}

// Diagnostics exposes bounded safe effective mode and numeric settings.
func (c NativeContextConfig) Diagnostics() map[string]any {
	continuity := string(c.ReasoningContinuity)
	switch c.ReasoningContinuity {
	case ContinuityRequired, ContinuityBestEffort, ContinuityDisabled:
	default:
		continuity = "invalid"
	}
	return map[string]any{
		"source":                      c.Source,
		"effective_mode":              c.EffectiveMode(),
		"request_encrypted_reasoning": c.RequestEncryptedReasoning,
		"reasoning_continuity":        continuity,
		"compaction_enabled":          c.Compaction.Enabled,
		"trigger_tokens":              c.Compaction.TriggerTokens,
		"retained_message_tokens":     c.Compaction.RetainedMessageTokens,
		"min_savings_tokens":          c.Compaction.MinSavingsTokens,
		"state_ttl_seconds":           c.Compaction.StateTTL.Seconds(),
		"max_entries":                 c.Compaction.MaxEntries,
		"max_entry_bytes":             c.Compaction.MaxEntryBytes,
		"failure_cooldown_seconds":    c.Compaction.FailureCooldown.Seconds(),
	}
}
