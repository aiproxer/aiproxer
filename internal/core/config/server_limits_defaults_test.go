package config_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func validServerLimitsPlugins() config.PluginsConfig {
	return config.PluginsConfig{
		Backends: []config.PluginConfig{{ID: "b1", Enabled: true}},
	}
}

func TestValidate_serverLimitsDefaultsApplied(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Server:  config.ServerConfig{},
		Plugins: validServerLimitsPlugins(),
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Server.MaxConcurrentDecodes != config.DefaultMaxConcurrentDecodes {
		t.Fatalf("MaxConcurrentDecodes = %d, want %d", cfg.Server.MaxConcurrentDecodes, config.DefaultMaxConcurrentDecodes)
	}
	if cfg.Server.MaxInflightDecodeBytes != config.DefaultMaxInflightDecodeBytes {
		t.Fatalf("MaxInflightDecodeBytes = %d, want %d", cfg.Server.MaxInflightDecodeBytes, config.DefaultMaxInflightDecodeBytes)
	}
	if cfg.Server.MaxPendingWireEvents != 0 {
		t.Fatalf("MaxPendingWireEvents = %d, want 0 (unlimited; no Validate default)", cfg.Server.MaxPendingWireEvents)
	}
}

func TestValidate_serverInflightDecodeBytesBelowBodyLimitRejected(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Server: config.ServerConfig{
			MaxRequestBodyBytes:    16 << 20,
			MaxInflightDecodeBytes: 8 << 20,
		},
		Plugins: validServerLimitsPlugins(),
	}
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected error when max_inflight_decode_bytes < max_request_body_bytes")
	}
}

func TestValidate_serverLimitsNegativeRejected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		cfg  config.ServerConfig
	}{
		{"concurrent", config.ServerConfig{MaxConcurrentDecodes: -1}},
		{"inflight", config.ServerConfig{MaxInflightDecodeBytes: -1}},
		{"pending", config.ServerConfig{MaxPendingWireEvents: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{Server: tc.cfg, Plugins: validServerLimitsPlugins()}
			if err := config.Validate(cfg); err == nil {
				t.Fatal("expected validation error for negative server limit")
			}
		})
	}
}

func TestValidate_serverLimitsExplicitCustomPreserved(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Server: config.ServerConfig{
			MaxConcurrentDecodes:   8,
			MaxInflightDecodeBytes: 16 << 20,
			MaxPendingWireEvents:   64,
			MaxRequestBodyBytes:    8 << 20,
		},
		Plugins: validServerLimitsPlugins(),
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Server.MaxConcurrentDecodes != 8 || cfg.Server.MaxInflightDecodeBytes != 16<<20 || cfg.Server.MaxPendingWireEvents != 64 {
		t.Fatalf("custom limits mutated: %+v", cfg.Server)
	}
}

func TestServerConfig_effectiveLimitsBeforeValidate(t *testing.T) {
	t.Parallel()

	s := config.ServerConfig{}
	if got := s.EffectiveMaxConcurrentDecodes(); got != config.DefaultMaxConcurrentDecodes {
		t.Fatalf("EffectiveMaxConcurrentDecodes() = %d, want %d", got, config.DefaultMaxConcurrentDecodes)
	}
	if got := s.EffectiveMaxInflightDecodeBytes(); got != config.DefaultMaxInflightDecodeBytes {
		t.Fatalf("EffectiveMaxInflightDecodeBytes() = %d, want %d", got, config.DefaultMaxInflightDecodeBytes)
	}
	if got := s.EffectiveMaxPendingWireEvents(); got != 0 {
		t.Fatalf("EffectiveMaxPendingWireEvents() = %d, want 0 (unlimited)", got)
	}
	if got := s.EffectiveMaxRequestBodyBytesForBudget(); got != 8<<20 {
		t.Fatalf("EffectiveMaxRequestBodyBytesForBudget() = %d, want %d", got, 8<<20)
	}
}
