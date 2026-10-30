package interleavedthinking_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"gopkg.in/yaml.v3"
)

func TestConfig_ParityAndDefaults(t *testing.T) {
	t.Parallel()

	// Verify constant values match design/spec defaults
	if interleavedthinking.DefaultStreamToClient != "hidden" {
		t.Fatalf("DefaultStreamToClient = %q, want hidden", interleavedthinking.DefaultStreamToClient)
	}
	if interleavedthinking.DefaultRegularTurns != 2 {
		t.Fatalf("DefaultRegularTurns = %d, want 2", interleavedthinking.DefaultRegularTurns)
	}
	if interleavedthinking.DefaultMaxMemoBytes != 16*1024 {
		t.Fatalf("DefaultMaxMemoBytes = %d, want 16384", interleavedthinking.DefaultMaxMemoBytes)
	}
	if interleavedthinking.DefaultMaxInstructionsBytes != 64*1024 {
		t.Fatalf("DefaultMaxInstructionsBytes = %d, want 65536", interleavedthinking.DefaultMaxInstructionsBytes)
	}

	// Zero-value config effective values
	var zeroCfg interleavedthinking.Config
	if got := zeroCfg.EffectiveStreamToClient(); got != "hidden" {
		t.Fatalf("zero EffectiveStreamToClient = %q, want hidden", got)
	}
	if got := zeroCfg.EffectiveRegularTurnsRemaining(); got != 2 {
		t.Fatalf("zero EffectiveRegularTurnsRemaining = %d, want 2", got)
	}
	if got := zeroCfg.EffectiveMaxMemoBytes(); got != 16*1024 {
		t.Fatalf("zero EffectiveMaxMemoBytes = %d, want 16384", got)
	}

	// Custom valid values
	customCfg := interleavedthinking.Config{
		Enabled:               true,
		StreamToClient:        "VISIBLE",
		RegularTurnsRemaining: 5,
		MaxMemoBytes:          8192,
	}
	if err := customCfg.Validate(); err != nil {
		t.Fatalf("Validate custom: %v", err)
	}
	if customCfg.StreamToClient != "visible" {
		t.Fatalf("normalized StreamToClient = %q, want visible", customCfg.StreamToClient)
	}
	if got := customCfg.EffectiveStreamToClient(); got != "visible" {
		t.Fatalf("EffectiveStreamToClient = %q, want visible", got)
	}
	if got := customCfg.EffectiveRegularTurnsRemaining(); got != 5 {
		t.Fatalf("EffectiveRegularTurnsRemaining = %d, want 5", got)
	}
	if got := customCfg.EffectiveMaxMemoBytes(); got != 8192 {
		t.Fatalf("EffectiveMaxMemoBytes = %d, want 8192", got)
	}
}

func TestConfig_ValidationErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		cfg     interleavedthinking.Config
		wantErr bool
	}{
		{
			name:    "disabled with invalid fields is ok",
			cfg:     interleavedthinking.Config{Enabled: false, StreamToClient: "invalid"},
			wantErr: false,
		},
		{
			name:    "invalid stream_to_client",
			cfg:     interleavedthinking.Config{Enabled: true, StreamToClient: "invalid-mode"},
			wantErr: true,
		},
		{
			name:    "negative regular turns",
			cfg:     interleavedthinking.Config{Enabled: true, RegularTurnsRemaining: -1},
			wantErr: true,
		},
		{
			name:    "negative max memo bytes",
			cfg:     interleavedthinking.Config{Enabled: true, MaxMemoBytes: -1},
			wantErr: true,
		},
		{
			name:    "instructions file with nul",
			cfg:     interleavedthinking.Config{Enabled: true, InstructionsFile: "path\x00bad"},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestDecodeConfig_Parity(t *testing.T) {
	t.Parallel()

	t.Run("valid custom config with all non-defaults", func(t *testing.T) {
		t.Parallel()
		raw := `
enabled: true
stream_to_client: visible
regular_turns_remaining: 5
max_memo_bytes: 8192
instructions_file: ./custom_thinker.md
`
		var n yaml.Node
		if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
			t.Fatalf("unmarshal yaml: %v", err)
		}
		cfg, err := interleavedthinking.DecodeConfig(n)
		if err != nil {
			t.Fatalf("DecodeConfig failed: %v", err)
		}
		if !cfg.Enabled {
			t.Errorf("got enabled %v, want true", cfg.Enabled)
		}
		if cfg.StreamToClient != "visible" {
			t.Errorf("got stream_to_client %q, want visible", cfg.StreamToClient)
		}
		if cfg.RegularTurnsRemaining != 5 {
			t.Errorf("got regular_turns_remaining %d, want 5", cfg.RegularTurnsRemaining)
		}
		if cfg.MaxMemoBytes != 8192 {
			t.Errorf("got max_memo_bytes %d, want 8192", cfg.MaxMemoBytes)
		}
		if cfg.InstructionsFile != "./custom_thinker.md" {
			t.Errorf("got instructions_file %q, want ./custom_thinker.md", cfg.InstructionsFile)
		}
	})

	t.Run("defaults when enabled without optional fields", func(t *testing.T) {
		t.Parallel()
		raw := `enabled: true`
		var n yaml.Node
		if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
			t.Fatalf("unmarshal yaml: %v", err)
		}
		cfg, err := interleavedthinking.DecodeConfig(n)
		if err != nil {
			t.Fatalf("DecodeConfig failed: %v", err)
		}
		if !cfg.Enabled {
			t.Errorf("got enabled %v, want true", cfg.Enabled)
		}
		if cfg.StreamToClient != interleavedthinking.DefaultStreamToClient {
			t.Errorf("got stream_to_client %q, want %q", cfg.StreamToClient, interleavedthinking.DefaultStreamToClient)
		}
		if cfg.RegularTurnsRemaining != interleavedthinking.DefaultRegularTurns {
			t.Errorf("got regular_turns_remaining %d, want %d", cfg.RegularTurnsRemaining, interleavedthinking.DefaultRegularTurns)
		}
		if cfg.MaxMemoBytes != interleavedthinking.DefaultMaxMemoBytes {
			t.Errorf("got max_memo_bytes %d, want %d", cfg.MaxMemoBytes, interleavedthinking.DefaultMaxMemoBytes)
		}
	})

	t.Run("unknown fields rejected", func(t *testing.T) {
		t.Parallel()
		raw := `
enabled: true
unknown_key: foo
`
		var n yaml.Node
		if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
			t.Fatalf("unmarshal yaml: %v", err)
		}
		_, err := interleavedthinking.DecodeConfig(n)
		if err == nil {
			t.Fatal("expected error for unknown config key")
		}
	})
}
