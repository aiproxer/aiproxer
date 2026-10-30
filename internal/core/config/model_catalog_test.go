package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

// minimalLoadableYAML is a smallest config that passes LoadFile + plugin validation.
const minimalLoadableYAML = `
server:
  address: "127.0.0.1:0"
continuity:
  in_memory: true
plugins:
  backends:
    - id: stub
      enabled: true
`

func yamlPath(p string) string {
	return filepath.ToSlash(p)
}

func TestLoadFile_modelCatalog_validEnabledWithCache(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	cache := yamlPath(filepath.Join(t.TempDir(), "catalog-cache.json"))
	body := minimalLoadableYAML + `
model_catalog:
  enabled: true
  external_updates_enabled: false
  cache_path: "` + cache + `"
  update_interval: 1h
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.ModelCatalog.Enabled {
		t.Fatal("expected model_catalog.enabled")
	}
	if cfg.ModelCatalog.ExternalUpdatesEnabled {
		t.Fatal("expected external_updates_enabled false")
	}
	if strings.TrimSpace(cfg.ModelCatalog.CachePath) == "" {
		t.Fatal("expected cache_path")
	}
}

func TestLoadFile_modelCatalog_rejectsEnabledWithoutCachePath(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	body := minimalLoadableYAML + `
model_catalog:
  enabled: true
  external_updates_enabled: false
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "model_catalog.cache_path") {
		t.Fatalf("want cache_path error, got %v", err)
	}
}

func TestLoadFile_modelCatalog_rejectsExternalUpdatesWithoutSource(t *testing.T) {
	t.Parallel()
	cache := yamlPath(filepath.Join(t.TempDir(), "c.json"))
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	body := minimalLoadableYAML + `
model_catalog:
  enabled: false
  external_updates_enabled: true
  update_interval: 30m
  cache_path: "` + cache + `"
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "model_catalog.source_url") {
		t.Fatalf("want source_url error, got %v", err)
	}
}

func TestLoadFile_modelCatalog_rejectsNonHTTPSSource(t *testing.T) {
	t.Parallel()
	cache := yamlPath(filepath.Join(t.TempDir(), "c.json"))
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	body := minimalLoadableYAML + `
model_catalog:
  enabled: true
  external_updates_enabled: true
  update_interval: 30m
  source_url: "ftp://example.com/models.json"
  cache_path: "` + cache + `"
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "model_catalog.source_url") {
		t.Fatalf("want source_url scheme error, got %v", err)
	}
}

func TestLoadFile_modelCatalog_rejectsZeroUpdateIntervalWhenExternalUpdates(t *testing.T) {
	t.Parallel()
	cache := yamlPath(filepath.Join(t.TempDir(), "c.json"))
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	body := minimalLoadableYAML + `
model_catalog:
  enabled: true
  external_updates_enabled: true
  update_interval: 0s
  source_url: "https://example.com/models.json"
  cache_path: "` + cache + `"
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "model_catalog.update_interval") {
		t.Fatalf("want update_interval error, got %v", err)
	}
}

func TestLoadFile_modelCatalog_rejectsBadUpdateIntervalString(t *testing.T) {
	t.Parallel()
	cache := yamlPath(filepath.Join(t.TempDir(), "c.json"))
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	body := minimalLoadableYAML + `
model_catalog:
  enabled: true
  external_updates_enabled: true
  update_interval: not-a-duration
  source_url: "https://example.com/models.json"
  cache_path: "` + cache + `"
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "model_catalog.update_interval") {
		t.Fatalf("want update_interval parse error, got %v", err)
	}
}

func TestLoadFile_modelCatalog_rejectsDiagnosticsPathWithoutLeadingSlash(t *testing.T) {
	t.Parallel()
	cache := yamlPath(filepath.Join(t.TempDir(), "c.json"))
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	body := minimalLoadableYAML + `
model_catalog:
  enabled: true
  cache_path: "` + cache + `"
  diagnostics_path: admin/catalog
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "model_catalog.diagnostics_path") {
		t.Fatalf("want diagnostics_path error, got %v", err)
	}
}

func TestLoadFile_modelCatalog_rejectsDiagnosticsPathOverlappingHealth(t *testing.T) {
	t.Parallel()
	cache := yamlPath(filepath.Join(t.TempDir(), "c.json"))
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	body := minimalLoadableYAML + `
diagnostics:
  health_path: /healthz
model_catalog:
  enabled: true
  cache_path: "` + cache + `"
  diagnostics_path: /healthz/catalog
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "diagnostics") {
		t.Fatalf("want overlap error, got %v", err)
	}
}

func TestLoadFile_modelCatalog_rejectsModelOverrideEmptyModel(t *testing.T) {
	t.Parallel()
	cache := yamlPath(filepath.Join(t.TempDir(), "c.json"))
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	body := minimalLoadableYAML + `
model_catalog:
  enabled: true
  cache_path: "` + cache + `"
  model_overrides:
    - model: ""
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "model_catalog.model_overrides") {
		t.Fatalf("want model_overrides error, got %v", err)
	}
}

func TestLoadFile_modelCatalog_rejectsOverrideNonPositiveContextLimit(t *testing.T) {
	t.Parallel()
	cache := yamlPath(filepath.Join(t.TempDir(), "c.json"))
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	body := minimalLoadableYAML + `
model_catalog:
  enabled: true
  cache_path: "` + cache + `"
  model_overrides:
    - model: "x"
      context_limit_tokens: 0
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "context_limit_tokens") {
		t.Fatalf("want context_limit_tokens error, got %v", err)
	}
}

func TestLoadFile_modelCatalog_rejectsOverrideNonPositiveInputLimit(t *testing.T) {
	t.Parallel()
	cache := yamlPath(filepath.Join(t.TempDir(), "c.json"))
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	body := minimalLoadableYAML + `
model_catalog:
  enabled: true
  cache_path: "` + cache + `"
  model_overrides:
    - model: "x"
      input_limit_tokens: 0
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "input_limit_tokens") {
		t.Fatalf("want input_limit_tokens error, got %v", err)
	}
}

func TestLoadFile_modelCatalog_rejectsOverrideNonPositiveOutputLimit(t *testing.T) {
	t.Parallel()
	cache := yamlPath(filepath.Join(t.TempDir(), "c.json"))
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	body := minimalLoadableYAML + `
model_catalog:
  enabled: true
  cache_path: "` + cache + `"
  model_overrides:
    - model: "x"
      output_limit_tokens: -1
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "output_limit_tokens") {
		t.Fatalf("want output_limit_tokens error, got %v", err)
	}
}

func TestLoadFile_modelCatalog_rejectsBackendOverrideNonPositiveInputLimit(t *testing.T) {
	t.Parallel()
	cache := yamlPath(filepath.Join(t.TempDir(), "c.json"))
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	body := minimalLoadableYAML + `
model_catalog:
  enabled: true
  cache_path: "` + cache + `"
  backend_model_overrides:
    - backend: "b1"
      model: "m1"
      input_limit_tokens: 0
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "input_limit_tokens") {
		t.Fatalf("want input_limit_tokens error, got %v", err)
	}
}

func TestLoadFile_modelCatalog_rejectsBackendOverrideNonPositiveOutputLimit(t *testing.T) {
	t.Parallel()
	cache := yamlPath(filepath.Join(t.TempDir(), "c.json"))
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	body := minimalLoadableYAML + `
model_catalog:
  enabled: true
  cache_path: "` + cache + `"
  backend_model_overrides:
    - backend: "b1"
      model: "m1"
      output_limit_tokens: 0
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "output_limit_tokens") {
		t.Fatalf("want output_limit_tokens error, got %v", err)
	}
}

func TestLoadFile_modelCatalog_rejectsBackendModelOverrideEmptyBackend(t *testing.T) {
	t.Parallel()
	cache := yamlPath(filepath.Join(t.TempDir(), "c.json"))
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	body := minimalLoadableYAML + `
model_catalog:
  enabled: true
  cache_path: "` + cache + `"
  backend_model_overrides:
    - backend: ""
      model: "gpt-4o"
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "model_catalog.backend_model_overrides") {
		t.Fatalf("want backend_model_overrides error, got %v", err)
	}
}

func TestLoadFile_modelCatalog_rejectsBackendModelOverrideEmptyModel(t *testing.T) {
	t.Parallel()
	cache := yamlPath(filepath.Join(t.TempDir(), "c.json"))
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	body := minimalLoadableYAML + `
model_catalog:
  enabled: true
  cache_path: "` + cache + `"
  backend_model_overrides:
    - backend: "b1"
      model: ""
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "model_catalog.backend_model_overrides") {
		t.Fatalf("want backend_model_overrides error, got %v", err)
	}
}

func TestLoadFile_modelCatalog_rejectsBackendOverrideNonPositiveContextLimit(t *testing.T) {
	t.Parallel()
	cache := yamlPath(filepath.Join(t.TempDir(), "c.json"))
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	body := minimalLoadableYAML + `
model_catalog:
  enabled: true
  cache_path: "` + cache + `"
  backend_model_overrides:
    - backend: "b1"
      model: "m1"
      context_limit_tokens: 0
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "context_limit_tokens") {
		t.Fatalf("want context_limit_tokens error, got %v", err)
	}
}

func TestLoadFile_modelCatalog_omittedIsDisabled(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(minimalLoadableYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ModelCatalog.Enabled {
		t.Fatal("expected default catalog disabled")
	}
	if cfg.ModelCatalog.ExternalUpdatesEnabled {
		t.Fatal("expected default external updates disabled")
	}
}

func TestLoadFile_modelCatalog_rejectsMalformedSourceWhenSetWhileDisabled(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	body := minimalLoadableYAML + `
model_catalog:
  enabled: false
  external_updates_enabled: false
  source_url: "::not-a-url"
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "model_catalog.source_url") {
		t.Fatalf("want source_url error, got %v", err)
	}
}

func TestLoadFile_modelCatalog_rejectsInvalidFetchTimeout(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	body := minimalLoadableYAML + `
model_catalog:
  enabled: false
  external_updates_enabled: false
  fetch_timeout: not-a-duration
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "model_catalog.fetch_timeout") {
		t.Fatalf("want fetch_timeout error, got %v", err)
	}
}

func TestLoadFile_modelCatalog_reportsInvalidDurationContainingRequiredWord(t *testing.T) {
	t.Parallel()
	cache := yamlPath(filepath.Join(t.TempDir(), "c.json"))
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	body := minimalLoadableYAML + `
model_catalog:
  enabled: true
  external_updates_enabled: true
  update_interval: required
  source_url: "https://example.com/models.json"
  cache_path: "` + cache + `"
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "model_catalog.update_interval") {
		t.Fatalf("want update_interval error, got %v", err)
	}
	// "required" is a non-empty invalid duration, so it must report an
	// invalid-duration parse failure rather than the empty-value message.
	if !strings.Contains(err.Error(), "invalid duration") {
		t.Fatalf("want invalid-duration error for non-empty input, got %v", err)
	}
}

func TestLoadFile_modelCatalog_rejectsNonPositiveFetchTimeout(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	body := minimalLoadableYAML + `
model_catalog:
  enabled: false
  external_updates_enabled: false
  fetch_timeout: 0s
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "model_catalog.fetch_timeout") {
		t.Fatalf("want fetch_timeout error, got %v", err)
	}
}
