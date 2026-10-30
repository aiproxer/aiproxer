package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func TestLoadFile_modelInventoryDefaults(t *testing.T) {
	t.Parallel()

	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(minimalLoadableYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ModelInventory.EffectiveRefreshEnabled() {
		t.Fatal("expected model_inventory.refresh_enabled default true")
	}
	if d := cfg.ModelInventory.RefreshIntervalDuration(); d != time.Hour {
		t.Fatalf("refresh interval = %v, want 1h", d)
	}
	if d := cfg.ModelInventory.FetchTimeoutDuration(); d != 30*time.Second {
		t.Fatalf("fetch timeout = %v, want 30s", d)
	}
}

func TestLoadFile_modelInventoryCanDisableRefresh(t *testing.T) {
	t.Parallel()

	p := filepath.Join(t.TempDir(), "cfg.yaml")
	body := minimalLoadableYAML + `
model_inventory:
  refresh_enabled: false
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelInventory.EffectiveRefreshEnabled() {
		t.Fatal("expected model_inventory.refresh_enabled false to disable refresh")
	}
}

func TestLoadFile_modelInventoryAcceptsCacheAndHourlyRefresh(t *testing.T) {
	t.Parallel()

	p := filepath.Join(t.TempDir(), "cfg.yaml")
	cache := yamlPath(filepath.Join(t.TempDir(), "backend-models.json"))
	body := minimalLoadableYAML + `
model_inventory:
  cache_path: "` + cache + `"
  refresh_enabled: true
  refresh_interval: 1h
  fetch_timeout: 45s
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelInventory.CachePath != cache {
		t.Fatalf("cache_path = %q, want %q", cfg.ModelInventory.CachePath, cache)
	}
	if d := cfg.ModelInventory.RefreshIntervalDuration(); d != time.Hour {
		t.Fatalf("refresh interval = %v, want 1h", d)
	}
	if d := cfg.ModelInventory.FetchTimeoutDuration(); d != 45*time.Second {
		t.Fatalf("fetch timeout = %v, want 45s", d)
	}
}

func TestLoadFile_modelInventoryRejectsSubHourlyRefresh(t *testing.T) {
	t.Parallel()

	p := filepath.Join(t.TempDir(), "cfg.yaml")
	body := minimalLoadableYAML + `
model_inventory:
  refresh_interval: 59m
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "model_inventory.refresh_interval") {
		t.Fatalf("want model_inventory.refresh_interval error, got %v", err)
	}
}

func TestLoadFile_modelInventoryRejectsInvalidFetchTimeout(t *testing.T) {
	t.Parallel()

	p := filepath.Join(t.TempDir(), "cfg.yaml")
	body := minimalLoadableYAML + `
model_inventory:
  fetch_timeout: nope
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "model_inventory.fetch_timeout") {
		t.Fatalf("want model_inventory.fetch_timeout error, got %v", err)
	}
}

func TestModelInventoryConfig_FetchTimeoutDurationFallsBackForProgrammaticInvalidValue(t *testing.T) {
	t.Parallel()

	cfg := config.ModelInventoryConfig{FetchTimeout: "-1s"}
	if got := cfg.FetchTimeoutDuration(); got != config.DefaultModelInventoryFetchTimeout {
		t.Fatalf("FetchTimeoutDuration() = %v, want %v", got, config.DefaultModelInventoryFetchTimeout)
	}
}

func TestModelInventoryConfig_RefreshIntervalDurationFallsBackForProgrammaticInvalidValue(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"nope", "59m"} {
		cfg := config.ModelInventoryConfig{RefreshInterval: value}
		if got := cfg.RefreshIntervalDuration(); got != config.DefaultModelInventoryRefreshInterval {
			t.Fatalf("RefreshIntervalDuration(%q) = %v, want %v", value, got, config.DefaultModelInventoryRefreshInterval)
		}
	}
}
