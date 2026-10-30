package config

import (
	"strings"
	"time"
)

const (
	DefaultModelInventoryRefreshInterval = time.Hour
	DefaultModelInventoryFetchTimeout    = 30 * time.Second
)

type ModelInventoryConfig struct {
	CachePath       string `yaml:"cache_path"`
	RefreshEnabled  *bool  `yaml:"refresh_enabled"`
	RefreshInterval string `yaml:"refresh_interval"`
	FetchTimeout    string `yaml:"fetch_timeout"`
	// DiagnosticsPath registers protected backend model-registry discovery JSON when non-empty
	// (e.g. "/debug/model-registry"). Distinct from model_catalog.diagnostics_path (models.dev).
	DiagnosticsPath string `yaml:"diagnostics_path"`
}

func (mc ModelInventoryConfig) EffectiveRefreshEnabled() bool {
	return mc.RefreshEnabled == nil || *mc.RefreshEnabled
}

func (mc ModelInventoryConfig) RefreshIntervalDuration() time.Duration {
	s := strings.TrimSpace(mc.RefreshInterval)
	if s == "" {
		return DefaultModelInventoryRefreshInterval
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < DefaultModelInventoryRefreshInterval {
		return DefaultModelInventoryRefreshInterval
	}
	return d
}

func (mc ModelInventoryConfig) FetchTimeoutDuration() time.Duration {
	s := strings.TrimSpace(mc.FetchTimeout)
	if s == "" {
		return DefaultModelInventoryFetchTimeout
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return DefaultModelInventoryFetchTimeout
	}
	return d
}
