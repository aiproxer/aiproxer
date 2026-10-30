package service

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/codex/internal/catalog"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

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
