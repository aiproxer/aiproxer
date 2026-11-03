package service

import (
	"strings"
	"testing"
)

func TestParseConfigYAML_DefaultsAndBounds(t *testing.T) {
	t.Parallel()
	cfg, err := ParseConfigYAML([]byte(`
executable: /opt/claude/bin/claude
admin_policy: honor
default_workspace: /tmp
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CredentialSource != "host_login" || cfg.AdminPolicy != "honor" ||
		cfg.MaxRuntimes != DefaultMaxRuntimes || cfg.TurnWaitSeconds != DefaultTurnWaitSeconds ||
		cfg.PendingTTLSeconds != DefaultPendingTTLSeconds || cfg.IdleTimeoutSeconds != DefaultIdleTimeoutSeconds ||
		cfg.StrictSemantics {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestParseConfigYAML_ZeroBoundsUseDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := ParseConfigYAML([]byte(`
executable: /opt/claude
admin_policy: honor
max_runtimes: 0
turn_wait_seconds: 0
pending_ttl_seconds: 0
idle_timeout_seconds: 0
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxRuntimes != DefaultMaxRuntimes || cfg.TurnWaitSeconds != DefaultTurnWaitSeconds ||
		cfg.PendingTTLSeconds != DefaultPendingTTLSeconds || cfg.IdleTimeoutSeconds != DefaultIdleTimeoutSeconds {
		t.Fatalf("zero bounds were not defaulted: %+v", cfg)
	}
}

func TestParseConfigYAML_RejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"missing executable": `admin_policy: honor`,
		"relative executable": `executable: bin/claude
admin_policy: honor`,
		"unknown key": `executable: /opt/claude
admin_policy: honor
node_script: /tmp/hijack.mjs`,
		"bad admin policy": `executable: /opt/claude
admin_policy: ignore`,
		"bad credential source": `executable: /opt/claude
admin_policy: honor
credential_source: gateway`,
		"relative workspace": `executable: /opt/claude
admin_policy: honor
default_workspace: relative/path`,
		"max runtimes too high": `executable: /opt/claude
admin_policy: honor
max_runtimes: 65`,
		"negative wait": `executable: /opt/claude
admin_policy: honor
turn_wait_seconds: -1`,
		"pending too high": `executable: /opt/claude
admin_policy: honor
pending_ttl_seconds: 86401`,
		"multiple documents": `executable: /opt/claude
admin_policy: honor
---
executable: /opt/other`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseConfigYAML([]byte(raw)); err == nil {
				t.Fatalf("expected rejection for %q", name)
			}
		})
	}
}

func TestParseConfigYAML_RejectsOversize(t *testing.T) {
	t.Parallel()
	_, err := ParseConfigYAML([]byte("executable: /opt/claude\n# " + strings.Repeat("x", 64<<10)))
	if err == nil {
		t.Fatal("expected oversize rejection")
	}
}

func TestBuildEnv_IsolatesUnapprovedHostVariables(t *testing.T) {
	t.Parallel()
	env, err := buildEnv([]string{
		"PATH=/usr/bin",
		"HOME=/home/test",
		"ANTHROPIC_BASE_URL=http://127.0.0.1:8080",
		"NODE_OPTIONS=--require /tmp/hijack.js",
		"CLAUDE_CODE_SIMPLE=1",
		"SECRET_TOKEN=leak",
	}, "", "host_login")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	for _, forbidden := range []string{"ANTHROPIC_BASE_URL", "NODE_OPTIONS", "CLAUDE_CODE_SIMPLE", "SECRET_TOKEN"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("environment leaked %s: %v", forbidden, env)
		}
	}
	for _, required := range []string{"PATH=/usr/bin", "HOME=/home/test", "CLAUDE_CODE_DISABLE_CLAUDE_MDS=1", "ENABLE_TOOL_SEARCH=false"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("environment missing %s: %v", required, env)
		}
	}
}

func TestBuildEnv_APKeyIsExplicitAndEncodedOnce(t *testing.T) {
	t.Parallel()
	env, err := buildEnv([]string{"PATH=/usr/bin", "HOME=/home/test"}, "sk-ant-test", "api_key")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range env {
		if entry == "ANTHROPIC_API_KEY=sk-ant-test" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("api key entries = %d, env=%v", count, env)
	}
}

func TestBuildEnv_HostLoginRequiresHome(t *testing.T) {
	t.Parallel()
	if _, err := buildEnv([]string{"PATH=/usr/bin"}, "", "host_login"); err == nil {
		t.Fatal("expected missing home rejection")
	}
}
