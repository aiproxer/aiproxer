package anthropic_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	"gopkg.in/yaml.v3"
)

func TestDecodeConfig_defaultsFalse(t *testing.T) {
	t.Parallel()
	cfg, err := anthropic.DecodeConfig(yaml.Node{Kind: yaml.DocumentNode})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExposeAipUsageExtensions {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestDecodeConfig_exposeAipUsageExtensions(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(`expose_aip_usage_extensions: true`), &n); err != nil {
		t.Fatal(err)
	}
	cfg, err := anthropic.DecodeConfig(n)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ExposeAipUsageExtensions {
		t.Fatalf("cfg = %+v", cfg)
	}
}
