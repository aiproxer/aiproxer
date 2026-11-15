package service

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connectors/codex/internal/catalog"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// TestServiceConfig_HTTPDocumentedRuntimeKnobs pins that every documented
// Codex HTTP runtime knob survives the strict YAML decoder and reaches the
// existing codex.Config fields instead of being rejected as unknown.
func TestServiceConfig_HTTPDocumentedRuntimeKnobs(t *testing.T) {
	t.Parallel()

	raw := []byte(`
default_reasoning_effort: "high"
gpt55_downgrade_disabled: true
gpt55_downgrade_source_model: "gpt-5.5"
gpt55_downgrade_target_model: "gpt-5.4"
early_session_verbosity_bump_disabled: true
early_session_verbosity_bump_turns: 4
mid_session_verbosity_bump_disabled: true
mid_session_verbosity_bump_frequency: 12
websocket_fallback_cooldown_seconds: 45.5
`)
	cfg, err := ParseConfigYAML(FactoryKindHTTP, raw)
	if err != nil {
		t.Fatalf("documented runtime knobs rejected: %v", err)
	}
	converted, err := cfg.toCodexHTTP(nil, "token")
	if err != nil {
		t.Fatalf("toCodexHTTP: %v", err)
	}
	if converted.DefaultReasoningEffort != "high" {
		t.Fatalf("DefaultReasoningEffort=%q want high", converted.DefaultReasoningEffort)
	}
	if !converted.GPT55DowngradeDisabled {
		t.Fatal("GPT55DowngradeDisabled not wired")
	}
	if converted.GPT55DowngradeSourceModel != "gpt-5.5" {
		t.Fatalf("GPT55DowngradeSourceModel=%q", converted.GPT55DowngradeSourceModel)
	}
	if converted.GPT55DowngradeTargetModel != "gpt-5.4" {
		t.Fatalf("GPT55DowngradeTargetModel=%q", converted.GPT55DowngradeTargetModel)
	}
	if !converted.EarlySessionVerbosityBumpDisabled {
		t.Fatal("EarlySessionVerbosityBumpDisabled not wired")
	}
	if converted.EarlySessionVerbosityBumpTurns != 4 {
		t.Fatalf("EarlySessionVerbosityBumpTurns=%d want 4", converted.EarlySessionVerbosityBumpTurns)
	}
	if !converted.MidSessionVerbosityBumpDisabled {
		t.Fatal("MidSessionVerbosityBumpDisabled not wired")
	}
	if converted.MidSessionVerbosityBumpFrequency != 12 {
		t.Fatalf("MidSessionVerbosityBumpFrequency=%d want 12", converted.MidSessionVerbosityBumpFrequency)
	}
	if want := 45*time.Second + 500*time.Millisecond; converted.WebSocketFallbackCooldown != want {
		t.Fatalf("WebSocketFallbackCooldown=%v want %v", converted.WebSocketFallbackCooldown, want)
	}
}

// TestServiceConfig_HTTPOmittedRuntimeKnobsPreserveDefaults keeps the service
// layer out of default ownership: runtime normalization in codex.New still
// supplies five/ten-turn bumps and the 300-second fallback cooldown.
func TestServiceConfig_HTTPOmittedRuntimeKnobsPreserveDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := ParseConfigYAML(FactoryKindHTTP, nil)
	if err != nil {
		t.Fatalf("empty HTTP YAML rejected: %v", err)
	}
	converted, err := cfg.toCodexHTTP(nil, "token")
	if err != nil {
		t.Fatalf("toCodexHTTP: %v", err)
	}
	if converted.DefaultReasoningEffort != "" {
		t.Fatalf("DefaultReasoningEffort=%q want empty", converted.DefaultReasoningEffort)
	}
	if converted.GPT55DowngradeDisabled {
		t.Fatal("GPT55DowngradeDisabled must stay false when omitted")
	}
	if converted.GPT55DowngradeSourceModel != "" || converted.GPT55DowngradeTargetModel != "" {
		t.Fatalf("downgrade models must stay empty: %q -> %q", converted.GPT55DowngradeSourceModel, converted.GPT55DowngradeTargetModel)
	}
	if converted.EarlySessionVerbosityBumpDisabled || converted.EarlySessionVerbosityBumpTurns != 0 {
		t.Fatalf("early bump must stay zero-valued: disabled=%v turns=%d", converted.EarlySessionVerbosityBumpDisabled, converted.EarlySessionVerbosityBumpTurns)
	}
	if converted.MidSessionVerbosityBumpDisabled || converted.MidSessionVerbosityBumpFrequency != 0 {
		t.Fatalf("mid bump must stay zero-valued: disabled=%v frequency=%d", converted.MidSessionVerbosityBumpDisabled, converted.MidSessionVerbosityBumpFrequency)
	}
	if converted.WebSocketFallbackCooldown != 0 {
		t.Fatalf("WebSocketFallbackCooldown=%v want zero so codex.New applies its default", converted.WebSocketFallbackCooldown)
	}
	if converted.NativeContext == nil || !converted.NativeContext.Enabled {
		t.Fatalf("native context default lost: %+v", converted.NativeContext)
	}
}

func TestServiceConfig_StillRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	if _, err := ParseConfigYAML(FactoryKindHTTP, []byte("unexpected_knob: true\n")); err == nil {
		t.Fatal("strict decoder must reject unknown YAML keys")
	}
}

// TestServiceConfig_ShippedExampleSubtreeParses reproduces the openai-codex
// connector subtree from config/examples/opencode-codex.yaml. ParseConfigYAML
// accepts only that subtree, never the full host document.
func TestServiceConfig_ShippedExampleSubtreeParses(t *testing.T) {
	t.Parallel()

	raw := []byte(`
default_reasoning_effort: "medium"
native_context:
  enabled: false
  request_encrypted_reasoning: false
  reasoning_continuity: disabled
  compaction:
    enabled: false
`)
	cfg, err := ParseConfigYAML(FactoryKindHTTP, raw)
	if err != nil {
		t.Fatalf("shipped example subtree rejected: %v", err)
	}
	converted, err := cfg.toCodexHTTP(nil, "token")
	if err != nil {
		t.Fatalf("toCodexHTTP: %v", err)
	}
	if converted.DefaultReasoningEffort != "medium" {
		t.Fatalf("DefaultReasoningEffort=%q want medium", converted.DefaultReasoningEffort)
	}
	if converted.NativeContext == nil || converted.NativeContext.Enabled {
		t.Fatalf("native_context opt-out lost: %+v", converted.NativeContext)
	}
}

func TestServiceConfig_AppServerRejectsNativeContext(t *testing.T) {
	t.Parallel()

	yamlWithNativeContext := []byte(`
base_url: "https://chatgpt.com/backend-api/codex"
native_context:
  enabled: true
  request_encrypted_reasoning: true
  reasoning_continuity: required
  compaction:
    enabled: true
`)

	t.Run("app server rejects native context YAML", func(t *testing.T) {
		t.Parallel()
		cfg, err := ParseConfigYAML(FactoryKindAppServer, yamlWithNativeContext)
		if err != nil {
			t.Fatalf("unexpected YAML parse error: %v", err)
		}
		_, err = cfg.toAppServer(nil, catalog.SourceShippedFallback, nil)
		if err == nil {
			t.Errorf("expected error when native_context is configured for openai-codex-app-server")
		}
	})

	t.Run("http connector accepts valid native context YAML", func(t *testing.T) {
		t.Parallel()
		cfg, err := ParseConfigYAML(FactoryKindHTTP, yamlWithNativeContext)
		if err != nil {
			t.Fatalf("unexpected YAML parse error: %v", err)
		}
		if cfg.NativeContext == nil || !cfg.NativeContext.Enabled {
			t.Fatalf("expected parsed NativeContext to be enabled")
		}
		codexCfg, err := cfg.toCodexHTTP(nil, "")
		if err != nil {
			t.Fatalf("unexpected error converting to codex.Config: %v", err)
		}
		if codexCfg.NativeContext == nil || !codexCfg.NativeContext.Enabled {
			t.Errorf("expected codexCfg.NativeContext to be populated and enabled")
		}
	})

	t.Run("app server accepts omitted/default native context", func(t *testing.T) {
		t.Parallel()
		for _, raw := range []string{"", "native_context: {}\n"} {
			cfg, err := ParseConfigYAML(FactoryKindAppServer, []byte(raw))
			if err != nil {
				t.Fatalf("unexpected YAML parse error: %v", err)
			}
			if _, err := cfg.toAppServer(nil, catalog.SourceShippedFallback, nil); err != nil {
				t.Fatalf("default-off app-server config rejected: %v", err)
			}
		}
	})

	t.Run("app server rejects explicit disabled native context", func(t *testing.T) {
		t.Parallel()
		cfg, err := ParseConfigYAML(FactoryKindAppServer, []byte("native_context:\n  enabled: false\n"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cfg.toAppServer(nil, catalog.SourceShippedFallback, nil); err == nil {
			t.Fatal("explicit app-server native_context was accepted")
		}
	})

	t.Run("app server rejects every non-default native context setting", func(t *testing.T) {
		t.Parallel()
		for _, raw := range []string{
			"native_context:\n  enabled: true\n",
			"native_context:\n  request_encrypted_reasoning: false\n",
			"native_context:\n  reasoning_continuity: disabled\n",
			"native_context:\n  compaction:\n    enabled: false\n",
		} {
			cfg, err := ParseConfigYAML(FactoryKindAppServer, []byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cfg.toAppServer(nil, catalog.SourceShippedFallback, nil); err == nil {
				t.Fatalf("non-default native_context setting was accepted: %q", raw)
			}
		}
	})

	t.Run("invalid HTTP native context reaches Configure error", func(t *testing.T) {
		t.Parallel()
		_, err := New().Configure(context.Background(), backendplugin.ConfigureRequest{
			FactoryKind: FactoryKindHTTP,
			ConfigYAML:  []byte("base_url: https://example.invalid\ncatalog_enabled: false\nnative_context:\n  enabled: false\n  compaction:\n    trigger_tokens: -1\n"),
			Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte("test-token")}},
		})
		if err == nil {
			t.Fatal("expected invalid native context to fail Configure")
		}
	})

	t.Run("YAML distinguishes omitted and explicit compaction false", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name           string
			raw            string
			wantCompaction bool
		}{
			{name: "omitted", raw: "native_context:\n  enabled: true\n", wantCompaction: true},
			{name: "explicit false", raw: "native_context:\n  enabled: true\n  compaction:\n    enabled: false\n", wantCompaction: false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				cfg, err := ParseConfigYAML(FactoryKindHTTP, []byte(tc.raw))
				if err != nil {
					t.Fatal(err)
				}
				converted, err := cfg.toCodexHTTP(nil, "")
				if err != nil {
					t.Fatal(err)
				}
				norm, err := converted.NativeContext.NormalizeAndValidate()
				if err != nil {
					t.Fatal(err)
				}
				if norm.Compaction.Enabled != tc.wantCompaction {
					t.Fatalf("compaction enabled = %v, want %v", norm.Compaction.Enabled, tc.wantCompaction)
				}
			})
		}
	})

	t.Run("omitted direct HTTP YAML is full native context", func(t *testing.T) {
		t.Parallel()
		cfg, err := ParseConfigYAML(FactoryKindHTTP, nil)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.NativeContext == nil || !cfg.NativeContext.Enabled {
			t.Fatalf("parsed config = %+v, want default native context", cfg)
		}
		converted, err := cfg.toCodexHTTP(nil, "")
		if err != nil {
			t.Fatal(err)
		}
		if converted.NativeContext == nil || converted.NativeContext.EffectiveMode() != "both" {
			t.Fatalf("converted native context = %+v", converted.NativeContext)
		}
	})

	t.Run("explicit request_encrypted_reasoning false is not defaulted", func(t *testing.T) {
		t.Parallel()
		cfg, err := ParseConfigYAML(FactoryKindHTTP, []byte("native_context:\n  enabled: true\n  request_encrypted_reasoning: false\n  reasoning_continuity: disabled\n  compaction:\n    enabled: false\n"))
		if err != nil {
			t.Fatal(err)
		}
		converted, err := cfg.toCodexHTTP(nil, "")
		if err != nil {
			t.Fatal(err)
		}
		if converted.NativeContext.RequestEncryptedReasoning {
			t.Fatal("explicit request_encrypted_reasoning: false was defaulted to true")
		}
	})

	t.Run("explicit native context false is a complete opt-out without nested block", func(t *testing.T) {
		t.Parallel()
		cfg, err := ParseConfigYAML(FactoryKindHTTP, []byte("native_context:\n  enabled: false\n"))
		if err != nil {
			t.Fatal(err)
		}
		converted, err := cfg.toCodexHTTP(nil, "")
		if err != nil {
			t.Fatalf("explicit enabled:false was rejected: %v", err)
		}
		if converted.NativeContext == nil || converted.NativeContext.Enabled {
			t.Fatalf("converted native context = %+v, want disabled", converted.NativeContext)
		}
	})
}
