package config_test

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	"github.com/aiproxer/aiproxer/internal/core/config"
)

func minimalPlugins() config.PluginsConfig {
	return config.PluginsConfig{
		Backends: []config.PluginConfig{{ID: "stub", Enabled: true}},
	}
}

func TestValidate_accessAuth_singleUserRejectsBroadBind(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "0.0.0.0:8080", AuthMode: config.AuthModeExternal},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    minimalPlugins(),
	}
	err := config.Validate(cfg)
	if err == nil || !errors.Is(err, accessmode.ErrSingleUserBroadBind) {
		t.Fatalf("want %v, got %v", accessmode.ErrSingleUserBroadBind, err)
	}
}

func TestValidate_accessAuth_multiUserRejectsNoAuthLegacy(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "multi_user"},
		Server:     config.ServerConfig{Address: "127.0.0.1:8080", AuthMode: config.AuthModeNoAuth},
		Auth:       config.AuthConfig{Handler: "local_api_key", RequiredLevel: "api_key"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    minimalPlugins(),
	}
	err := config.Validate(cfg)
	if err == nil || !errors.Is(err, accessmode.ErrMultiUserIncompatibleNoAuth) {
		t.Fatalf("want %v, got %v", accessmode.ErrMultiUserIncompatibleNoAuth, err)
	}
}

func TestValidate_accessAuth_multiUserAllowsBroadWithAuth(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "multi_user"},
		Server:     config.ServerConfig{Address: "0.0.0.0:8080", AuthMode: config.AuthModeExternal},
		Auth:       config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    minimalPlugins(),
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestValidate_accessAuth_rejectsRemoteAPIKeySSOWithoutLocalKeys(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "multi_user"},
		Server:     config.ServerConfig{Address: "0.0.0.0:8080", AuthMode: config.AuthModeExternal},
		Auth:       config.AuthConfig{Handler: "remote", RequiredLevel: "api_key_sso"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    minimalPlugins(),
	}
	err := config.Validate(cfg)
	if err == nil || !errors.Is(err, config.ErrAuthLocalAPIKeysRequiredForRemoteSSO) {
		t.Fatalf("want %v, got %v", config.ErrAuthLocalAPIKeysRequiredForRemoteSSO, err)
	}
}

func TestValidate_accessAuth_allowsRemoteAPIKeySSOWithLocalKeys(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Access: config.AccessConfig{Mode: "multi_user"},
		Server: config.ServerConfig{Address: "0.0.0.0:8080", AuthMode: config.AuthModeExternal},
		Auth: config.AuthConfig{
			Handler:       "remote",
			RequiredLevel: "api_key_sso",
			LocalAPIKeys: []config.AuthLocalAPIKeyRecord{
				{KeyID: "k1", PrincipalID: "p1", Key: "test-local-api-key-16"},
			},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    minimalPlugins(),
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestValidate_accessAuth_rejectsUnknownEventDelivery(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "single_user"},
		Server:     config.ServerConfig{Address: "127.0.0.1:8080"},
		Auth:       config.AuthConfig{EventDelivery: "nope"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    minimalPlugins(),
	}
	err := config.Validate(cfg)
	if err == nil || !errors.Is(err, config.ErrInvalidAuthEventDelivery) {
		t.Fatalf("want %v, got %v", config.ErrInvalidAuthEventDelivery, err)
	}
}

func TestValidate_accessAuth_rejectsUnknownAccessMode(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "solo"},
		Server:     config.ServerConfig{Address: "127.0.0.1:8080"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    minimalPlugins(),
	}
	err := config.Validate(cfg)
	if err == nil || !errors.Is(err, accessmode.ErrUnknownAccessMode) {
		t.Fatalf("want %v, got %v", accessmode.ErrUnknownAccessMode, err)
	}
}
