package config_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestEffectiveTransportFallbackPolicy_defaultsCompatibility(t *testing.T) {
	t.Parallel()

	got := config.EffectiveTransportFallbackPolicy(nil)
	if got != aipapi.TransportFallbackCompatibility {
		t.Fatalf("got %q", got)
	}
}

func TestEffectiveTransportFallbackPolicy_exactFromRouting(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Routing: config.RoutingConfig{
			Transport: config.RoutingTransportConfig{
				FallbackPolicy: "exact",
			},
		},
	}
	got := config.EffectiveTransportFallbackPolicy(cfg)
	if got != aipapi.TransportFallbackExact {
		t.Fatalf("got %q", got)
	}
}
