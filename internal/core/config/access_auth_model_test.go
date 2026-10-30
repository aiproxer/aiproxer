package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAccessAuthYAML_singleUserLocalNoopShape(t *testing.T) {
	t.Parallel()
	const y = `
access:
  mode: single_user
auth:
  handler: local_noop
  required_level: none
  event_failure_policy: best_effort
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(y), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Access.Mode != "single_user" {
		t.Fatalf("access.mode: got %q", cfg.Access.Mode)
	}
	if cfg.Auth.Handler != "local_noop" {
		t.Fatalf("auth.handler: got %q", cfg.Auth.Handler)
	}
	if cfg.Auth.RequiredLevel != "none" {
		t.Fatalf("auth.required_level: got %q", cfg.Auth.RequiredLevel)
	}
	if cfg.Auth.EventFailurePolicy != "best_effort" {
		t.Fatalf("auth.event_failure_policy: got %q", cfg.Auth.EventFailurePolicy)
	}
	if len(cfg.Auth.LocalAPIKeys) != 0 {
		t.Fatalf("local_api_keys: want empty slice, got len=%d", len(cfg.Auth.LocalAPIKeys))
	}
}

func TestAccessAuthYAML_multiUserAPIKeyShape(t *testing.T) {
	t.Parallel()
	const y = `
access:
  mode: multi_user
auth:
  handler: local_api_key
  required_level: api_key
  event_failure_policy: fail_closed
  local_api_keys:
    - key_id: k1
      principal_id: u1
      key: "not-a-real-secret"
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(y), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Access.Mode != "multi_user" {
		t.Fatalf("access.mode: got %q", cfg.Access.Mode)
	}
	if len(cfg.Auth.LocalAPIKeys) != 1 {
		t.Fatalf("local_api_keys: want 1 entry, got %d", len(cfg.Auth.LocalAPIKeys))
	}
	rec := cfg.Auth.LocalAPIKeys[0]
	if rec.KeyID != "k1" || rec.PrincipalID != "u1" || rec.Key != "not-a-real-secret" {
		t.Fatalf("record: %#v", rec)
	}
}

func TestAccessAuthYAML_remotePlaceholderShape(t *testing.T) {
	t.Parallel()
	const y = `
access:
  mode: multi_user
auth:
  handler: remote
  required_level: api_key_sso
  remote:
    endpoint: "https://auth.example.invalid/v1"
    handler: enterprise
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(y), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Auth.Handler != "remote" {
		t.Fatalf("auth.handler: got %q", cfg.Auth.Handler)
	}
	if cfg.Auth.Remote.Endpoint != "https://auth.example.invalid/v1" {
		t.Fatalf("auth.remote.endpoint: got %q", cfg.Auth.Remote.Endpoint)
	}
	if cfg.Auth.Remote.Handler != "enterprise" {
		t.Fatalf("auth.remote.handler: got %q", cfg.Auth.Remote.Handler)
	}
}

func TestAccessAuthYAML_omittedAccessAuthLeavesZero(t *testing.T) {
	t.Parallel()
	const y = `server:
  address: "127.0.0.1:9"
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(y), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if strings.TrimSpace(cfg.Access.Mode) != "" {
		t.Fatalf("access.mode: want empty, got %q", cfg.Access.Mode)
	}
	if strings.TrimSpace(cfg.Auth.Handler) != "" {
		t.Fatalf("auth.handler: want empty, got %q", cfg.Auth.Handler)
	}
}

func TestEffectiveAuthForAudit_noAuthMerge(t *testing.T) {
	t.Parallel()
	var cfg Config
	h, rl := cfg.EffectiveAuthForAudit()
	if h != "local_noop" || rl != "none" {
		t.Fatalf("nil cfg: got handler=%q required=%q", h, rl)
	}
	cfg2 := Config{Server: ServerConfig{Address: "127.0.0.1:9"}}
	h2, rl2 := cfg2.EffectiveAuthForAudit()
	if h2 != "local_noop" || rl2 != "none" {
		t.Fatalf("empty auth.handler: got handler=%q required=%q", h2, rl2)
	}
}

func TestEffectiveAuthForAudit_externalMapsToRemote(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Server: ServerConfig{Address: "127.0.0.1:9", AuthMode: AuthModeExternal},
	}
	h, rl := cfg.EffectiveAuthForAudit()
	if h != "remote" || rl != "none" {
		t.Fatalf("external: got handler=%q required=%q", h, rl)
	}
}
