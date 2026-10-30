package codex

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestNativeContextConfig_DefaultsAndEffectiveMode(t *testing.T) {
	t.Parallel()
	t.Run("nil config is disabled", func(t *testing.T) {
		t.Parallel()
		var cfg *NativeContextConfig
		if cfg.CompactionEnabled() {
			t.Errorf("expected CompactionEnabled() == false for nil config")
		}
		if mode := cfg.EffectiveMode(); mode != "disabled" {
			t.Errorf("expected EffectiveMode() == %q, got %q", "disabled", mode)
		}
	})

	t.Run("disabled config", func(t *testing.T) {
		t.Parallel()
		cfg := &NativeContextConfig{Enabled: false}
		if cfg.CompactionEnabled() {
			t.Errorf("expected CompactionEnabled() == false when Enabled == false")
		}
		if mode := cfg.EffectiveMode(); mode != "disabled" {
			t.Errorf("expected EffectiveMode() == %q, got %q", "disabled", mode)
		}
	})

	t.Run("normalization applies reviewed defaults", func(t *testing.T) {
		t.Parallel()
		raw := NativeContextConfig{
			Enabled: true,
		}
		norm, err := raw.NormalizeAndValidate()
		if err != nil {
			t.Fatalf("unexpected error normalizing defaults: %v", err)
		}
		if !norm.RequestEncryptedReasoning {
			t.Errorf("expected RequestEncryptedReasoning default to be true")
		}
		if norm.ReasoningContinuity != ContinuityRequired {
			t.Errorf("expected ReasoningContinuity default to be %q, got %q", ContinuityRequired, norm.ReasoningContinuity)
		}
		if !norm.Compaction.Enabled {
			t.Errorf("omitted compaction must default to enabled")
		}
		if norm.Compaction.RetainedMessageTokens != DefaultRetainedMessageTokens ||
			norm.Compaction.MinSavingsTokens != DefaultMinSavingsTokens ||
			norm.Compaction.StateTTL != DefaultStateTTL ||
			norm.Compaction.MaxEntries != DefaultMaxEntries ||
			norm.Compaction.MaxEntryBytes != DefaultMaxEntryBytes ||
			norm.Compaction.FailureCooldown != DefaultFailureCooldown {
			t.Fatalf("omitted compaction defaults = %+v", norm.Compaction)
		}
		if norm.EffectiveMode() != "both" {
			t.Errorf("expected EffectiveMode() == %q, got %q", "both", norm.EffectiveMode())
		}
	})

	t.Run("explicit compaction applies defaults", func(t *testing.T) {
		t.Parallel()
		cfg := NativeContextConfig{Enabled: true, Compaction: NativeCompactionConfig{Enabled: true}}
		cfg.SetCompactionPresentForYAML()
		cfg.RequestEncryptedReasoning = true
		norm, err := cfg.NormalizeAndValidate()
		if err != nil {
			t.Fatal(err)
		}
		if norm.Compaction.RetainedMessageTokens != DefaultRetainedMessageTokens ||
			norm.Compaction.MinSavingsTokens != DefaultMinSavingsTokens ||
			norm.Compaction.StateTTL != DefaultStateTTL || norm.Compaction.MaxEntries != DefaultMaxEntries ||
			norm.Compaction.MaxEntryBytes != DefaultMaxEntryBytes || norm.Compaction.FailureCooldown != DefaultFailureCooldown {
			t.Fatalf("explicit compaction defaults = %+v", norm.Compaction)
		}
	})

	t.Run("evaluation modes", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name     string
			cfg      NativeContextConfig
			wantMode string
		}{
			{
				name: "neither",
				cfg: NativeContextConfig{
					Enabled:             true,
					ReasoningContinuity: ContinuityDisabled,
					CompactionSet:       true,
				},
				wantMode: "neither",
			},
			{
				name: "reasoning-only",
				cfg: NativeContextConfig{
					Enabled:                   true,
					RequestEncryptedReasoning: true,
					ReasoningContinuity:       ContinuityRequired,
					Compaction:                NativeCompactionConfig{Enabled: false},
					CompactionSet:             true,
				},
				wantMode: "reasoning_only",
			},
			{
				name: "compaction-only",
				cfg: NativeContextConfig{
					Enabled:                   true,
					RequestEncryptedReasoning: false,
					ReasoningContinuity:       ContinuityDisabled,
					Compaction:                NativeCompactionConfig{Enabled: true},
				},
				wantMode: "compaction_only",
			},
			{
				name: "both (full)",
				cfg: NativeContextConfig{
					Enabled:                   true,
					RequestEncryptedReasoning: true,
					ReasoningContinuity:       ContinuityRequired,
					Compaction:                NativeCompactionConfig{Enabled: true},
				},
				wantMode: "both",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				norm, err := tt.cfg.NormalizeAndValidate()
				if err != nil {
					t.Fatalf("unexpected validation error: %v", err)
				}
				if mode := norm.EffectiveMode(); mode != tt.wantMode {
					t.Errorf("expected EffectiveMode() == %q, got %q", tt.wantMode, mode)
				}
			})
		}
	})
}

func TestNativeContextConfig_ExplicitNestedCompactionFalse(t *testing.T) {
	t.Parallel()
	cfg := NativeContextConfig{Enabled: true, Compaction: NativeCompactionConfig{Enabled: false}}
	cfg.SetCompactionPresentForYAML()
	cfg.RequestEncryptedReasoning = true
	norm, err := cfg.NormalizeAndValidate()
	if err != nil {
		t.Fatal(err)
	}
	if norm.Compaction.Enabled {
		t.Fatal("explicit compaction.enabled: false must remain disabled")
	}
	if norm.EffectiveMode() != "reasoning_only" {
		t.Fatalf("effective mode = %q, want reasoning_only", norm.EffectiveMode())
	}
}

func TestNativeContextConfig_NilDirectConfigDefaultsToFullMode(t *testing.T) {
	t.Parallel()
	var cfg *NativeContextConfig
	if cfg != nil {
		t.Fatal("test setup unexpectedly supplied native context")
	}
	norm := DefaultNativeContextConfig()
	if norm.EffectiveMode() != "both" || !norm.Compaction.Enabled || !norm.RequestEncryptedReasoning || norm.ReasoningContinuity != ContinuityRequired {
		t.Fatalf("default native context = %+v", norm)
	}
}

func TestNativeContextConfig_Validation_InvalidAndBounds(t *testing.T) {
	t.Parallel()
	t.Run("negative values rejected", func(t *testing.T) {
		t.Parallel()
		negs := []struct {
			name  string
			field func(*NativeContextConfig)
		}{
			{"negative trigger_tokens", func(c *NativeContextConfig) { c.Compaction.TriggerTokens = -100 }},
			{"negative retained_message_tokens", func(c *NativeContextConfig) { c.Compaction.RetainedMessageTokens = -100 }},
			{"negative min_savings_tokens", func(c *NativeContextConfig) { c.Compaction.MinSavingsTokens = -100 }},
			{"negative state_ttl", func(c *NativeContextConfig) { c.Compaction.StateTTL = -1 * time.Second }},
			{"negative max_entries", func(c *NativeContextConfig) { c.Compaction.MaxEntries = -1 }},
			{"negative max_entry_bytes", func(c *NativeContextConfig) { c.Compaction.MaxEntryBytes = -1 }},
			{"negative failure_cooldown", func(c *NativeContextConfig) { c.Compaction.FailureCooldown = -1 * time.Second }},
		}
		for _, tt := range negs {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				cfg := NativeContextConfig{Enabled: true}
				tt.field(&cfg)
				_, err := cfg.NormalizeAndValidate()
				if err == nil {
					t.Errorf("expected validation error for %s", tt.name)
				}
			})
		}
	})

	t.Run("hard bounds exceeded", func(t *testing.T) {
		t.Parallel()
		overBounds := []struct {
			name  string
			field func(*NativeContextConfig)
		}{
			{"over retained_message_tokens cap", func(c *NativeContextConfig) { c.Compaction.RetainedMessageTokens = 1_000_000 }},
			{"over max_entries cap", func(c *NativeContextConfig) { c.Compaction.MaxEntries = 50_000 }},
			{"over max_entry_bytes cap", func(c *NativeContextConfig) { c.Compaction.MaxEntryBytes = 100 * 1024 * 1024 }},
			{"over state_ttl cap", func(c *NativeContextConfig) { c.Compaction.StateTTL = 48 * time.Hour }},
			{"over failure_cooldown cap", func(c *NativeContextConfig) { c.Compaction.FailureCooldown = 2 * time.Hour }},
		}
		for _, tt := range overBounds {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				cfg := NativeContextConfig{Enabled: true}
				tt.field(&cfg)
				_, err := cfg.NormalizeAndValidate()
				if err == nil {
					t.Errorf("expected validation error for %s", tt.name)
				}
			})
		}
	})

	t.Run("inconsistent settings rejected", func(t *testing.T) {
		t.Parallel()
		inconsistents := []struct {
			name  string
			field func(*NativeContextConfig)
		}{
			{"unknown continuity mode", func(c *NativeContextConfig) { c.ReasoningContinuity = "invalid_mode" }},
			{
				"required continuity compaction without request_encrypted_reasoning",
				func(c *NativeContextConfig) {
					c.RequestEncryptedReasoning = false
					c.RequestEncryptedReasoningSet = true
					c.ReasoningContinuity = ContinuityRequired
					c.Compaction.Enabled = true
				},
			},
			{
				"trigger_tokens less than min_savings_tokens",
				func(c *NativeContextConfig) {
					c.Compaction.TriggerTokens = 1000
					c.Compaction.MinSavingsTokens = 8192
				},
			},
			{
				"trigger_tokens less than or equal to retained_message_tokens",
				func(c *NativeContextConfig) {
					c.Compaction.RetainedMessageTokens = 64000
					c.Compaction.TriggerTokens = 50000
					c.Compaction.MinSavingsTokens = 1000
				},
			},
		}
		for _, tt := range inconsistents {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				cfg := NativeContextConfig{Enabled: true}
				tt.field(&cfg)
				_, err := cfg.NormalizeAndValidate()
				if err == nil {
					t.Errorf("expected validation error for %s", tt.name)
				}
			})
		}
	})
}

func TestNativeContextConfig_Diagnostics(t *testing.T) {
	t.Parallel()
	cfg := NativeContextConfig{
		Enabled:                   true,
		RequestEncryptedReasoning: true,
		ReasoningContinuity:       ContinuityRequired,
		Compaction: NativeCompactionConfig{
			Enabled:               true,
			RetainedMessageTokens: 64000,
			MinSavingsTokens:      8192,
			StateTTL:              1 * time.Hour,
			MaxEntries:            1024,
			MaxEntryBytes:         1048576,
			FailureCooldown:       5 * time.Minute,
		},
	}
	norm, err := cfg.NormalizeAndValidate()
	if err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	diag := norm.Diagnostics()

	expectedKeys := []string{
		"source", "effective_mode", "request_encrypted_reasoning", "reasoning_continuity",
		"compaction_enabled", "trigger_tokens", "retained_message_tokens",
		"min_savings_tokens", "state_ttl_seconds", "max_entries",
		"max_entry_bytes", "failure_cooldown_seconds",
	}

	if len(diag) != len(expectedKeys) {
		t.Fatalf("diagnostics keys = %d, want %d", len(diag), len(expectedKeys))
	}
	for _, k := range expectedKeys {
		if _, ok := diag[k]; !ok {
			t.Errorf("expected key %q in diagnostics", k)
		}
	}
	if diag["effective_mode"] != "both" || diag["reasoning_continuity"] != string(ContinuityRequired) {
		t.Fatalf("diagnostics exposed unexpected mode values: %#v", diag)
	}
	for _, forbidden := range []string{"opaque-prompt-secret", "session-secret", "account-secret", "ciphertext-secret"} {
		if strings.Contains(fmt.Sprint(diag), forbidden) {
			t.Fatalf("diagnostics leaked forbidden value %q: %#v", forbidden, diag)
		}
	}

	unsafe := NativeContextConfig{ReasoningContinuity: "session-secret"}
	if got := unsafe.Diagnostics()["reasoning_continuity"]; got != "invalid" {
		t.Fatalf("invalid continuity diagnostic = %#v, want invalid", got)
	}
}
