package config

import (
	"strings"
	"time"
)

// UpdateIntervalDuration returns a positive parsed model_catalog.update_interval duration.
// Call after successful config validation when the string must be well-formed for enabled refresh paths.
func (mc ModelCatalogConfig) UpdateIntervalDuration() (d time.Duration, ok bool) {
	s := strings.TrimSpace(mc.UpdateInterval)
	if s == "" {
		return 0, false
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
}

// FetchTimeoutDuration returns a positive parsed model_catalog.fetch_timeout duration.
func (mc ModelCatalogConfig) FetchTimeoutDuration() (d time.Duration, ok bool) {
	s := strings.TrimSpace(mc.FetchTimeout)
	if s == "" {
		return 0, false
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
}
