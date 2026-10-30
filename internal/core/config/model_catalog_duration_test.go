package config_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func TestModelCatalogConfig_UpdateIntervalDuration(t *testing.T) {
	t.Parallel()
	if d, ok := (config.ModelCatalogConfig{}).UpdateIntervalDuration(); ok || d != 0 {
		t.Fatalf("empty: got d=%v ok=%v", d, ok)
	}
	if d, ok := (config.ModelCatalogConfig{UpdateInterval: "1h30m"}).UpdateIntervalDuration(); !ok || d != 90*time.Minute {
		t.Fatalf("1h30m: got d=%v ok=%v", d, ok)
	}
	if d, ok := (config.ModelCatalogConfig{UpdateInterval: "0s"}).UpdateIntervalDuration(); ok || d != 0 {
		t.Fatalf("0s: got d=%v ok=%v", d, ok)
	}
	if d, ok := (config.ModelCatalogConfig{UpdateInterval: "not-a-duration"}).UpdateIntervalDuration(); ok || d != 0 {
		t.Fatalf("invalid: got d=%v ok=%v", d, ok)
	}
}

func TestModelCatalogConfig_FetchTimeoutDuration(t *testing.T) {
	t.Parallel()
	if d, ok := (config.ModelCatalogConfig{}).FetchTimeoutDuration(); ok || d != 0 {
		t.Fatalf("empty: got d=%v ok=%v", d, ok)
	}
	if d, ok := (config.ModelCatalogConfig{FetchTimeout: "5s"}).FetchTimeoutDuration(); !ok || d != 5*time.Second {
		t.Fatalf("5s: got d=%v ok=%v", d, ok)
	}
	if d, ok := (config.ModelCatalogConfig{FetchTimeout: "0"}).FetchTimeoutDuration(); ok || d != 0 {
		t.Fatalf("0: got d=%v ok=%v", d, ok)
	}
}
