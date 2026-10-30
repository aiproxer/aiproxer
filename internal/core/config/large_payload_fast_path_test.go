package config_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func validLargePayloadFastPath() config.LargePayloadFastPathConfig {
	return config.LargePayloadFastPathConfig{
		Enabled:               true,
		ThresholdBytes:        1 << 20,
		MemorySpoolBytes:      64 << 10,
		MaxInflightSpoolBytes: 256 << 20,
		MaxSemanticFactBytes:  256 << 10,
		SpoolDir:              "",
	}
}

func configWithFastPath(fp config.LargePayloadFastPathConfig) *config.Config {
	return &config.Config{
		Server: config.ServerConfig{LargePayloadFastPath: fp},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
		},
	}
}

func TestLargePayloadFastPath_DefaultOff(t *testing.T) {
	t.Parallel()

	cfg := configWithFastPath(config.LargePayloadFastPathConfig{})
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate zero config: %v", err)
	}
	if cfg.Server.LargePayloadFastPath.Enabled {
		t.Fatal("LargePayloadFastPath must default to disabled")
	}
	if cfg.Server.MaxRequestBodyBytes != 0 {
		t.Fatalf("MaxRequestBodyBytes mutated to %d, want 0 (handler default)", cfg.Server.MaxRequestBodyBytes)
	}
	if got := cfg.Server.EffectiveMaxRequestBodyBytesForBudget(); got != 8<<20 {
		t.Fatalf("EffectiveMaxRequestBodyBytesForBudget = %d, want %d", got, 8<<20)
	}
}

func TestLargePayloadFastPath_DisabledIgnoresInvalidFields(t *testing.T) {
	t.Parallel()

	cfg := configWithFastPath(config.LargePayloadFastPathConfig{
		Enabled:               false,
		ThresholdBytes:        -1,
		MemorySpoolBytes:      -2,
		MaxInflightSpoolBytes: -3,
		MaxSemanticFactBytes:  -4,
		SpoolDir:              "x\x00y",
	})
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("disabled fast path must not fail validation (OFF = no behavior change), got: %v", err)
	}
}

func TestLargePayloadFastPath_EnabledRequiresPositive(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*config.LargePayloadFastPathConfig)
		want   string
	}{
		{"threshold_zero", func(c *config.LargePayloadFastPathConfig) { c.ThresholdBytes = 0 }, "threshold_bytes"},
		{"threshold_negative", func(c *config.LargePayloadFastPathConfig) { c.ThresholdBytes = -1 }, "threshold_bytes"},
		{"memory_zero", func(c *config.LargePayloadFastPathConfig) { c.MemorySpoolBytes = 0 }, "memory_spool_bytes"},
		{"memory_negative", func(c *config.LargePayloadFastPathConfig) { c.MemorySpoolBytes = -1 }, "memory_spool_bytes"},
		{"inflight_zero", func(c *config.LargePayloadFastPathConfig) { c.MaxInflightSpoolBytes = 0 }, "max_inflight_spool_bytes"},
		{"inflight_negative", func(c *config.LargePayloadFastPathConfig) { c.MaxInflightSpoolBytes = -1 }, "max_inflight_spool_bytes"},
		{"semantic_zero", func(c *config.LargePayloadFastPathConfig) { c.MaxSemanticFactBytes = 0 }, "max_semantic_fact_bytes"},
		{"semantic_negative", func(c *config.LargePayloadFastPathConfig) { c.MaxSemanticFactBytes = -1 }, "max_semantic_fact_bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fp := validLargePayloadFastPath()
			tc.mutate(&fp)
			if err := config.Validate(configWithFastPath(fp)); err == nil {
				t.Fatal("expected validation error for non-positive fast-path budget")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q must mention %q", err.Error(), tc.want)
			}
		})
	}
}

func TestLargePayloadFastPath_OverflowRelationships(t *testing.T) {
	t.Parallel()

	t.Run("memory_above_inflight", func(t *testing.T) {
		t.Parallel()
		fp := validLargePayloadFastPath()
		fp.MemorySpoolBytes = fp.MaxInflightSpoolBytes + 1
		if err := config.Validate(configWithFastPath(fp)); err == nil {
			t.Fatal("expected error when memory_spool_bytes > max_inflight_spool_bytes")
		} else if !strings.Contains(err.Error(), "memory_spool_bytes") {
			t.Fatalf("error %q must mention memory_spool_bytes", err.Error())
		}
	})

	t.Run("fact_sum_overflow", func(t *testing.T) {
		t.Parallel()
		fp := validLargePayloadFastPath()
		fp.MemorySpoolBytes = int64(1) << 62
		fp.MaxInflightSpoolBytes = int64(1) << 62
		fp.MaxSemanticFactBytes = int64(1) << 62
		if err := config.Validate(configWithFastPath(fp)); err == nil {
			t.Fatal("expected error when memory+semantic fact budgets overflow int64")
		}
	})
}

func TestLargePayloadFastPath_SpoolDir(t *testing.T) {
	t.Parallel()

	t.Run("empty_uses_os_temp", func(t *testing.T) {
		t.Parallel()
		if err := config.Validate(configWithFastPath(validLargePayloadFastPath())); err != nil {
			t.Fatalf("empty spool_dir must be valid (OS temp), got: %v", err)
		}
	})

	t.Run("explicit_dir", func(t *testing.T) {
		t.Parallel()
		fp := validLargePayloadFastPath()
		fp.SpoolDir = "/var/lib/aip/spool"
		if err := config.Validate(configWithFastPath(fp)); err != nil {
			t.Fatalf("explicit spool_dir must be valid, got: %v", err)
		}
	})

	t.Run("nul_rejected", func(t *testing.T) {
		t.Parallel()
		fp := validLargePayloadFastPath()
		fp.SpoolDir = "a\x00b"
		if err := config.Validate(configWithFastPath(fp)); err == nil {
			t.Fatal("expected error for NUL in spool_dir")
		} else if !strings.Contains(err.Error(), "spool_dir") {
			t.Fatalf("error %q must mention spool_dir", err.Error())
		}
	})

	t.Run("whitespace_only_rejected", func(t *testing.T) {
		t.Parallel()
		fp := validLargePayloadFastPath()
		fp.SpoolDir = "   "
		if err := config.Validate(configWithFastPath(fp)); err == nil {
			t.Fatal("expected error for whitespace-only spool_dir")
		}
	})
}

func TestLargePayloadFastPath_StrictDecodeYAML(t *testing.T) {
	t.Parallel()

	raw := []byte("server:\n" +
		"  address: \"127.0.0.1:0\"\n" +
		"  large_payload_fast_path:\n" +
		"    enabled: true\n" +
		"    threshold_bytes: 1048576\n" +
		"    memory_spool_bytes: 65536\n" +
		"    max_inflight_spool_bytes: 268435456\n" +
		"    max_semantic_fact_bytes: 262144\n" +
		"    spool_dir: \"\"\n" +
		"logging:\n" +
		"  level: \"info\"\n")
	cfg, cat, err := config.StrictDecode(raw)
	if err != nil {
		t.Fatalf("StrictDecode: %v", err)
	}
	if cat != config.CategoryOK {
		t.Fatalf("category = %q, want ok", cat)
	}
	fp := cfg.Server.LargePayloadFastPath
	if !fp.Enabled || fp.ThresholdBytes != 1048576 || fp.MemorySpoolBytes != 65536 ||
		fp.MaxInflightSpoolBytes != 268435456 || fp.MaxSemanticFactBytes != 262144 {
		t.Fatalf("decoded fast path mismatch: %+v", fp)
	}
}

func TestLargePayloadFastPath_InvalidReloadPreservesLastGood(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	good := []byte("server:\n" +
		"  address: \"127.0.0.1:0\"\n" +
		"  large_payload_fast_path:\n" +
		"    enabled: true\n" +
		"    threshold_bytes: 1048576\n" +
		"    memory_spool_bytes: 65536\n" +
		"    max_inflight_spool_bytes: 268435456\n" +
		"    max_semantic_fact_bytes: 262144\n" +
		"logging:\n" +
		"  level: \"info\"\n")
	lastGood, err := config.LoadEffective(ctx, good, config.LoadEffectiveOptions{})
	if err != nil {
		t.Fatalf("LoadEffective good candidate: %v", err)
	}

	bad := []byte("server:\n" +
		"  address: \"127.0.0.1:0\"\n" +
		"  large_payload_fast_path:\n" +
		"    enabled: true\n" +
		"    threshold_bytes: 0\n" +
		"    memory_spool_bytes: 65536\n" +
		"    max_inflight_spool_bytes: 268435456\n" +
		"    max_semantic_fact_bytes: 262144\n" +
		"logging:\n" +
		"  level: \"info\"\n")
	if _, err := config.LoadEffective(ctx, bad, config.LoadEffectiveOptions{}); err == nil {
		t.Fatal("expected invalid candidate to fail LoadEffective")
	}

	if !lastGood.Config.Server.LargePayloadFastPath.Enabled {
		t.Fatal("last-good generation must stay enabled after invalid reload")
	}
	if got := lastGood.Config.Server.LargePayloadFastPath.ThresholdBytes; got != 1048576 {
		t.Fatalf("last-good threshold mutated to %d", got)
	}
}

func TestLargePayloadFastPath_MaxRequestBodyBytesUntouched(t *testing.T) {
	t.Parallel()

	fp := validLargePayloadFastPath()
	cfg := configWithFastPath(fp)
	cfg.Server.MaxRequestBodyBytes = 4 << 20
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.Server.MaxRequestBodyBytes != 4<<20 {
		t.Fatalf("MaxRequestBodyBytes mutated to %d", cfg.Server.MaxRequestBodyBytes)
	}

	def := configWithFastPath(validLargePayloadFastPath())
	def.Server.MaxRequestBodyBytes = 0
	if err := config.Validate(def); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if def.Server.MaxRequestBodyBytes != 0 {
		t.Fatalf("zero MaxRequestBodyBytes must stay zero, got %d", def.Server.MaxRequestBodyBytes)
	}
}

func TestLargePayloadFastPath_EffectiveDefaults(t *testing.T) {
	t.Parallel()

	var zero config.LargePayloadFastPathConfig
	if got := zero.EffectiveThresholdBytes(); got != config.DefaultLargePayloadThresholdBytes {
		t.Fatalf("EffectiveThresholdBytes = %d, want %d", got, config.DefaultLargePayloadThresholdBytes)
	}
	if got := zero.EffectiveMemorySpoolBytes(); got != config.DefaultLargePayloadMemorySpoolBytes {
		t.Fatalf("EffectiveMemorySpoolBytes = %d, want %d", got, config.DefaultLargePayloadMemorySpoolBytes)
	}
	if got := zero.EffectiveMaxInflightSpoolBytes(); got != config.DefaultLargePayloadMaxInflightSpoolBytes {
		t.Fatalf("EffectiveMaxInflightSpoolBytes = %d, want %d", got, config.DefaultLargePayloadMaxInflightSpoolBytes)
	}
	if got := zero.EffectiveMaxSemanticFactBytes(); got != config.DefaultLargePayloadMaxSemanticFactBytes {
		t.Fatalf("EffectiveMaxSemanticFactBytes = %d, want %d", got, config.DefaultLargePayloadMaxSemanticFactBytes)
	}

	set := validLargePayloadFastPath()
	if got := set.EffectiveThresholdBytes(); got != 1<<20 {
		t.Fatalf("EffectiveThresholdBytes = %d, want %d", got, 1<<20)
	}
	if got := set.EffectiveSpoolDir(); got != "" {
		t.Fatalf("EffectiveSpoolDir = %q, want empty (OS temp)", got)
	}
	set.SpoolDir = "  /tmp/aip  "
	if got := set.EffectiveSpoolDir(); got != "/tmp/aip" {
		t.Fatalf("EffectiveSpoolDir = %q, want trimmed", got)
	}
}
