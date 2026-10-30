package config_test

import (
	"errors"
	"testing"

	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
	"github.com/aiproxer/aiproxer/internal/core/config"
)

func TestValidate_auth_localAPIKey_requiresKeys(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "single_user"},
		Server:     config.ServerConfig{Address: "127.0.0.1:8080"},
		Auth:       config.AuthConfig{Handler: "local_api_key", RequiredLevel: "api_key", LocalAPIKeys: nil},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    minimalPlugins(),
	}
	err := config.Validate(cfg)
	if err == nil || !errors.Is(err, config.ErrAuthLocalAPIKeysRequired) {
		t.Fatalf("want %v, got %v", config.ErrAuthLocalAPIKeysRequired, err)
	}
}

func TestValidate_auth_localAPIKey_rejectsIncompleteRecord(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Access: config.AccessConfig{Mode: "single_user"},
		Server: config.ServerConfig{Address: "127.0.0.1:8080"},
		Auth: config.AuthConfig{
			Handler:       "local_api_key",
			RequiredLevel: "api_key",
			LocalAPIKeys: []config.AuthLocalAPIKeyRecord{
				{KeyID: "k1", PrincipalID: "p1", Key: ""},
			},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    minimalPlugins(),
	}
	err := config.Validate(cfg)
	if err == nil || !errors.Is(err, coreauth.ErrLocalAPIKeyEmpty) {
		t.Fatalf("want %v, got %v", coreauth.ErrLocalAPIKeyEmpty, err)
	}
}

func TestValidate_auth_localAPIKey_rejectsDuplicateKeyID(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Access: config.AccessConfig{Mode: "single_user"},
		Server: config.ServerConfig{Address: "127.0.0.1:8080"},
		Auth: config.AuthConfig{
			Handler:       "local_api_key",
			RequiredLevel: "api_key",
			LocalAPIKeys: []config.AuthLocalAPIKeyRecord{
				{KeyID: "same", PrincipalID: "a", Key: "secret-a-pad-to-16ch"},
				{KeyID: "same", PrincipalID: "b", Key: "secret-b-pad-to-16ch"},
			},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    minimalPlugins(),
	}
	err := config.Validate(cfg)
	if err == nil || !errors.Is(err, coreauth.ErrDuplicateLocalAPIKeyID) {
		t.Fatalf("want %v, got %v", coreauth.ErrDuplicateLocalAPIKeyID, err)
	}
}
