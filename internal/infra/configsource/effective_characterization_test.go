package configsource_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"gopkg.in/yaml.v3"
)

func TestEffectiveStandardFeatureInjectionCharacterization(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{Kind: "openai-codex", ID: "codex-primary", Enabled: true}}}}
	if err := standardplugins.EnsureToolCallRepairInConfig(cfg, standardplugins.ToolCallRepairInjectOpts{
		StandardDistribution: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := standardplugins.EnsureReasoningOutputPreservationInConfig(cfg, standardplugins.ReasoningOutputPreservationInjectOpts{
		StandardDistribution: true,
	}); err != nil {
		t.Fatal(err)
	}
	var sawRepair, sawReasoning bool
	for _, f := range cfg.Plugins.Features {
		switch f.FactoryID() {
		case standardplugins.ToolCallRepairFeatureID:
			sawRepair = f.Enabled
		case standardplugins.ReasoningOutputPreservationFeatureID:
			sawReasoning = f.Enabled
		}
	}
	if !sawRepair || !sawReasoning {
		t.Fatalf("standard injection missing: repair=%v reasoning=%v features=%v", sawRepair, sawReasoning, cfg.Plugins.Features)
	}
}

func TestEffectivePrefixValidationCharacterization(t *testing.T) {
	t.Parallel()
	rows := []config.PluginConfig{
		mustCustomRow(t, "a", "shared"),
		mustCustomRow(t, "b", "shared"),
	}
	err := standardplugins.ValidateCompatibleManifestOwnership(rows, nil)
	if err == nil {
		t.Fatal("expected prefix collision error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "prefix") {
		t.Fatalf("want prefix attribution, got %v", err)
	}
}

func TestEffectiveFullBuildOnlyRejectionSeam(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	body := `
server:
  address: "127.0.0.1:0"
continuity:
  in_memory: true
plugins:
  backends:
    - kind: ` + standardplugins.CustomOpenAILegacyCompatibleID + `
      id: a
      enabled: true
      config:
        backend_prefix: shared
        base_url: http://127.0.0.1:9
    - kind: ` + standardplugins.CustomOpenAILegacyCompatibleID + `
      id: b
      enabled: true
      config:
        backend_prefix: shared
        base_url: http://127.0.0.1:9
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile should accept structural YAML: %v", err)
	}
	err = standardplugins.ValidateCompatibleManifestOwnership(cfg.Plugins.Backends, nil)
	if err == nil {
		t.Fatal("full-build seam must reject custom-compatible prefix collision")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "prefix") {
		t.Fatalf("want prefix failure class, got %v", err)
	}
}

func mustCustomRow(t *testing.T, id, prefix string) config.PluginConfig {
	t.Helper()
	raw := []byte(`
kind: ` + standardplugins.CustomOpenAILegacyCompatibleID + `
id: ` + id + `
enabled: true
config:
  backend_prefix: ` + prefix + `
  base_url: http://127.0.0.1:9
`)
	var row config.PluginConfig
	if err := yaml.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	return row
}
