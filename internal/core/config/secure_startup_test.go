package config_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func TestValidate_noAuthRequiresExplicitLoopback(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		address string
		wantErr bool
	}{
		{name: "ipv4_loopback", address: "127.0.0.1:8080"},
		{name: "ipv6_loopback", address: "[::1]:8080"},
		{name: "localhost", address: "localhost:8080"},
		{name: "wildcard_colon", address: ":8080", wantErr: true},
		{name: "ipv4_wildcard", address: "0.0.0.0:8080", wantErr: true},
		{name: "ipv6_wildcard", address: "[::]:8080", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{
				Server:     config.ServerConfig{Address: tc.address, AuthMode: config.AuthModeNoAuth},
				Continuity: config.ContinuityConfig{InMemory: true},
			}
			err := config.Validate(cfg)
			if tc.wantErr {
				// Broad binds are rejected by validateAccessAuth / accessmode posture (default
				// single_user), not by the legacy server.auth_mode loopback-only check.
				if err == nil || !strings.Contains(err.Error(), "access.mode") {
					t.Fatalf("want access.mode bind posture error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValidate_externalAuthBroadBindRequiresMultiUserAuth(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "multi_user"},
		Auth:       config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"},
		Server:     config.ServerConfig{Address: "0.0.0.0:8080", AuthMode: config.AuthModeExternal},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "stub", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
}
