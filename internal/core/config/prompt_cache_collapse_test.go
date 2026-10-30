package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func TestCompilePromptCacheCollapse_Defaults(t *testing.T) {
	compiled, err := config.CompilePromptCacheCollapse(config.PromptCacheCollapseConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if compiled.MinPromptTokens() != 8000 {
		t.Fatalf("min_prompt=%d want 8000", compiled.MinPromptTokens())
	}
	if compiled.HealthyRatio() != 0.5 {
		t.Fatalf("healthy=%v want 0.5", compiled.HealthyRatio())
	}
	if compiled.ColdRatio() != 0.1 {
		t.Fatalf("cold=%v want 0.1", compiled.ColdRatio())
	}
	if compiled.ConsecutiveCold() != 5 {
		t.Fatalf("run=%d want 5", compiled.ConsecutiveCold())
	}
	if compiled.MaxTrackedKeys() != 1024 {
		t.Fatalf("keys=%d want 1024", compiled.MaxTrackedKeys())
	}
	if compiled.IdleExpiry() != 2*time.Hour {
		t.Fatalf("idle=%v want 2h", compiled.IdleExpiry())
	}
	var nilCompiled *config.CompiledPromptCacheCollapse
	if nilCompiled.MinPromptTokens() != 8000 {
		t.Fatal("nil receiver must report defaults")
	}
}

func TestCompilePromptCacheCollapse_Overrides(t *testing.T) {
	compiled, err := config.CompilePromptCacheCollapse(config.PromptCacheCollapseConfig{
		MinPromptTokens: new(100),
		HealthyRatio:    new(0.8),
		ColdRatio:       new(0.2),
		ConsecutiveCold: new(3),
		MaxTrackedKeys:  new(16),
		IdleExpiry:      "30m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if compiled.MinPromptTokens() != 100 || compiled.HealthyRatio() != 0.8 ||
		compiled.ColdRatio() != 0.2 || compiled.ConsecutiveCold() != 3 ||
		compiled.MaxTrackedKeys() != 16 || compiled.IdleExpiry() != 30*time.Minute {
		t.Fatalf("overrides not honored: %+v", compiled)
	}
}

func TestCompilePromptCacheCollapse_Bounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*config.PromptCacheCollapseConfig)
	}{
		{"min zero", func(c *config.PromptCacheCollapseConfig) { c.MinPromptTokens = new(0) }},
		{"min negative", func(c *config.PromptCacheCollapseConfig) { c.MinPromptTokens = new(-1) }},
		{"min huge", func(c *config.PromptCacheCollapseConfig) { c.MinPromptTokens = new(1000001) }},
		{"healthy zero", func(c *config.PromptCacheCollapseConfig) { c.HealthyRatio = new(float64(0)) }},
		{"healthy above one", func(c *config.PromptCacheCollapseConfig) { c.HealthyRatio = new(1.5) }},
		{"cold negative", func(c *config.PromptCacheCollapseConfig) { c.ColdRatio = new(-0.1) }},
		{"cold one", func(c *config.PromptCacheCollapseConfig) { c.ColdRatio = new(float64(1)) }},
		{"cold equals healthy", func(c *config.PromptCacheCollapseConfig) { c.ColdRatio = new(0.5) }},
		{"cold above healthy", func(c *config.PromptCacheCollapseConfig) { c.ColdRatio = new(0.9) }},
		{"cold above lowered healthy", func(c *config.PromptCacheCollapseConfig) {
			c.HealthyRatio = new(0.3)
			c.ColdRatio = new(0.3)
		}},
		{"run zero", func(c *config.PromptCacheCollapseConfig) { c.ConsecutiveCold = new(0) }},
		{"run huge", func(c *config.PromptCacheCollapseConfig) { c.ConsecutiveCold = new(1001) }},
		{"keys zero", func(c *config.PromptCacheCollapseConfig) { c.MaxTrackedKeys = new(0) }},
		{"keys huge", func(c *config.PromptCacheCollapseConfig) { c.MaxTrackedKeys = new(1000001) }},
		{"idle malformed", func(c *config.PromptCacheCollapseConfig) { c.IdleExpiry = "bogus" }},
		{"idle zero", func(c *config.PromptCacheCollapseConfig) { c.IdleExpiry = "0s" }},
		{"idle below floor", func(c *config.PromptCacheCollapseConfig) { c.IdleExpiry = "500ms" }},
		{"idle above ceiling", func(c *config.PromptCacheCollapseConfig) { c.IdleExpiry = "721h" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := config.PromptCacheCollapseConfig{}
			tc.mut(&in)
			if _, err := config.CompilePromptCacheCollapse(in); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestValidate_PromptCacheCollapse_FailsWhenMetricsDisabled(t *testing.T) {
	cfg := &config.Config{}
	cfg.Observability.Metrics.Enabled = false
	cfg.Observability.Metrics.PromptCacheCollapse.MinPromptTokens = new(0)
	if err := config.Validate(cfg); err == nil {
		t.Fatal("invalid collapse tuning must fail even when metrics are disabled")
	} else if !strings.Contains(err.Error(), "prompt_cache_collapse") {
		t.Fatalf("error must name the block, got %v", err)
	}
}

func TestValidate_PromptCacheCollapse_ValidOverridesPass(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.Address = "127.0.0.1:8080"
	cfg.Observability.Metrics.Enabled = true
	cfg.Observability.Metrics.Path = "/metrics"
	cfg.Observability.Metrics.PromptCacheCollapse.MinPromptTokens = new(100)
	cfg.Observability.Metrics.PromptCacheCollapse.IdleExpiry = "30m"
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("valid overrides must pass: %v", err)
	}
}

func TestStrictDecode_PromptCacheCollapse_TypedYAML(t *testing.T) {
	raw := []byte("observability:\n  metrics:\n    enabled: true\n    path: /metrics\n    prompt_cache_collapse:\n      min_prompt_tokens: 100\n      healthy_ratio: 0.8\n      cold_ratio: 0.2\n      consecutive_cold: 3\n      max_tracked_keys: 16\n      idle_expiry: 30m\n")
	cfg, cat, err := config.StrictDecode(raw)
	if err != nil {
		t.Fatalf("decode: %v (category %v)", err, cat)
	}
	compiled, err := config.CompilePromptCacheCollapse(cfg.Observability.Metrics.PromptCacheCollapse)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.MinPromptTokens() != 100 || compiled.ConsecutiveCold() != 3 || compiled.IdleExpiry() != 30*time.Minute {
		t.Fatalf("YAML overrides not honored: %+v", compiled)
	}
}

func TestStrictDecode_PromptCacheCollapse_UnknownFieldRejected(t *testing.T) {
	raw := []byte("observability:\n  metrics:\n    prompt_cache_collapse:\n      minn_prompt_tokens: 100\n")
	if _, _, err := config.StrictDecode(raw); err == nil {
		t.Fatal("typo'd collapse field must fail strict decode")
	}
}

func TestCompilePromptCacheCollapse_SeamDigestsDefaultsDisabled(t *testing.T) {
	compiled, err := config.CompilePromptCacheCollapse(config.PromptCacheCollapseConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if compiled.SeamDigestsEnabled() {
		t.Fatal("seam digests must stay off by default: per-request hashing is opt-in")
	}
	var nilCompiled *config.CompiledPromptCacheCollapse
	if nilCompiled.SeamDigestsEnabled() {
		t.Fatal("nil receiver must report seam digests disabled")
	}
}

func TestCompilePromptCacheCollapse_SeamDigestsEnabled(t *testing.T) {
	compiled, err := config.CompilePromptCacheCollapse(config.PromptCacheCollapseConfig{SeamDigestsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if !compiled.SeamDigestsEnabled() {
		t.Fatal("explicit seam_digests_enabled must enable the digest feed")
	}
}

func TestStrictDecode_PromptCacheCollapse_SeamDigestsEnabled(t *testing.T) {
	raw := []byte("observability:\n  metrics:\n    enabled: true\n    path: /metrics\n    prompt_cache_collapse:\n      seam_digests_enabled: true\n")
	cfg, _, err := config.StrictDecode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	compiled, err := config.CompilePromptCacheCollapse(cfg.Observability.Metrics.PromptCacheCollapse)
	if err != nil {
		t.Fatal(err)
	}
	if !compiled.SeamDigestsEnabled() {
		t.Fatal("YAML seam_digests_enabled must enable the digest feed")
	}
}
