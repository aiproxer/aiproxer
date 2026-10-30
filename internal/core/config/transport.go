package config

import (
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type RoutingTransportConfig struct {
	FallbackPolicy string `yaml:"fallback_policy"`
}

func EffectiveTransportFallbackPolicy(cfg *Config) aipapi.TransportFallbackPolicy {
	if cfg == nil {
		return aipapi.TransportFallbackCompatibility
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Routing.Transport.FallbackPolicy)) {
	case string(aipapi.TransportFallbackExact):
		return aipapi.TransportFallbackExact
	default:
		return aipapi.TransportFallbackCompatibility
	}
}
