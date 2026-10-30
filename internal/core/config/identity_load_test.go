package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/identity"
)

func TestLoadFile_identityDefaults(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
continuity:
  in_memory: true
plugins:
  backends:
    - id: stub
      enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Identity.Upstream.UserAgent.Mode != identity.ModeProxy {
		t.Fatalf("user_agent.mode: %q", cfg.Identity.Upstream.UserAgent.Mode)
	}
	up := identity.MergeUpstream(cfg.Identity, nil)
	if got := up.UserAgentValue(); got != "aiproxer" {
		t.Fatalf("resolved user_agent: %q", got)
	}
	if got := up.AppURLValue(); got != "https://github.com/aiproxer/aiproxer" {
		t.Fatalf("resolved app_url: %q", got)
	}
	if got := identity.EffectiveDownstreamOf(cfg.Identity).ServerValue(); got != "aiproxer" {
		t.Fatalf("resolved server: %q", got)
	}
}

func TestLoadFile_identityCustom(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
identity:
  upstream:
    user_agent:
      mode: custom
      value: "MyProxy/1.0"
    openrouter:
      app_url:
        mode: custom
        value: "https://example.com/my-proxy"
      app_title:
        mode: custom
        value: "My Proxy"
  downstream:
    server:
      mode: drop
continuity:
  in_memory: true
plugins:
  backends:
    - id: stub
      enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Identity.Upstream.UserAgent.Value != "MyProxy/1.0" {
		t.Fatalf("user_agent: %+v", cfg.Identity.Upstream.UserAgent)
	}
	if cfg.Identity.Downstream.Server.Mode != identity.ModeDrop {
		t.Fatalf("server: %+v", cfg.Identity.Downstream.Server)
	}
}

func TestLoadFile_identityRejectsInvalid(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
identity:
  downstream:
    server:
      mode: passthrough
continuity:
  in_memory: true
plugins:
  backends:
    - id: stub
      enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "passthrough") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestValidate_identityZeroConfig(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if cfg.Identity.Upstream.UserAgent.Mode != identity.ModeProxy {
		t.Fatalf("defaults not applied: %+v", cfg.Identity.Upstream.UserAgent)
	}
}

func TestLoadFile_identityServerRejectsCRLF(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
identity:
  downstream:
    server:
      mode: custom
      value: "evil\r\nX-Injected: yes"
continuity:
  in_memory: true
plugins:
  backends:
    - id: stub
      enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil {
		t.Fatal("expected validation error for CR/LF in server value")
	}
	if !strings.Contains(err.Error(), "control") {
		t.Fatalf("unexpected err: %v", err)
	}
}
