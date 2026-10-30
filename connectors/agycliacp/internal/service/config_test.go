package service

import (
	"testing"

	"github.com/aiproxer/aiproxer/connectors/agycliacp/internal/product"
)

func TestConfigWrapperAutoDownloadDefaultsOnAndCanBeDisabled(t *testing.T) {
	t.Parallel()
	cfg, err := ParseConfigYAML(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.toProduct().WrapperAutoDownload {
		t.Fatal("wrapper auto-download should default to enabled")
	}

	cfg, err = ParseConfigYAML([]byte("wrapper_auto_download: false\nwrapper_cache_dir: /tmp/wrappers\n"))
	if err != nil {
		t.Fatal(err)
	}
	productCfg := cfg.toProduct()
	if productCfg.WrapperAutoDownload {
		t.Fatal("wrapper auto-download opt-out was ignored")
	}
	if productCfg.WrapperCacheDir != "/tmp/wrappers" {
		t.Fatalf("cache dir = %q", productCfg.WrapperCacheDir)
	}
}

func TestConfigDefaultTimeoutIsFourHours(t *testing.T) {
	t.Parallel()
	cfg, err := ParseConfigYAML(nil)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.toProduct().TimeoutSeconds
	if got != product.DefaultTimeoutSeconds {
		t.Fatalf("timeout seconds = %d, want %d", got, product.DefaultTimeoutSeconds)
	}
	if product.DefaultTimeoutSeconds != 4*60*60 {
		t.Fatalf("expected four-hour default, got %d", product.DefaultTimeoutSeconds)
	}
}

func TestConfigExplicitTimeoutIsPreserved(t *testing.T) {
	t.Parallel()
	cfg, err := ParseConfigYAML([]byte("timeout_seconds: 30\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.toProduct().TimeoutSeconds; got != 30 {
		t.Fatalf("timeout seconds = %d, want 30", got)
	}
}
