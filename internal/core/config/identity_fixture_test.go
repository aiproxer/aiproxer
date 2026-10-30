package config_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/identity"
	"gopkg.in/yaml.v3"
)

// ID-147-CFG: Load committed fixture and assert normalized effective policies literally.
func TestLoadFile_ID147_globalOpenRouterOverrideFixture(t *testing.T) {
	t.Parallel()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// internal/core/config -> repo root testdata/identity/
	fixture := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "testdata", "identity", "global_openrouter_override.yaml")
	cfg, err := config.LoadFile(fixture)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}

	if cfg.Identity.Upstream.UserAgent.Mode != identity.ModeCustom {
		t.Fatalf("global user_agent.mode=%q want custom", cfg.Identity.Upstream.UserAgent.Mode)
	}
	if cfg.Identity.Upstream.UserAgent.Value != "FixtureGlobalUA/1" {
		t.Fatalf("global user_agent.value=%q", cfg.Identity.Upstream.UserAgent.Value)
	}
	if cfg.Identity.Upstream.OpenRouter.AppURL.Value != "https://fixture.example/global" {
		t.Fatalf("global app_url=%q", cfg.Identity.Upstream.OpenRouter.AppURL.Value)
	}
	if cfg.Identity.Upstream.OpenRouter.AppTitle.Value != "FixtureGlobalTitle" {
		t.Fatalf("global app_title=%q", cfg.Identity.Upstream.OpenRouter.AppTitle.Value)
	}
	if cfg.Identity.Downstream.Server.Mode != identity.ModeCustom || cfg.Identity.Downstream.Server.Value != "FixtureServer/1" {
		t.Fatalf("downstream server=%+v", cfg.Identity.Downstream.Server)
	}

	var ov *identity.BackendOverride
	for _, b := range cfg.Plugins.Backends {
		if b.ID != "openrouter" {
			continue
		}
		raw, err := yaml.Marshal(b.Config)
		if err != nil {
			t.Fatal(err)
		}
		var wrap struct {
			Identity *identity.BackendOverride `yaml:"identity"`
		}
		if err := yaml.Unmarshal(raw, &wrap); err != nil {
			t.Fatal(err)
		}
		ov = wrap.Identity
		break
	}
	if ov == nil || ov.UserAgent == nil || ov.OpenRouter == nil {
		t.Fatalf("openrouter backend override missing: %+v", ov)
	}
	if ov.UserAgent.Mode != identity.ModeCustom || ov.UserAgent.Value != "FixtureORUA/2" {
		t.Fatalf("override user_agent=%+v", ov.UserAgent)
	}
	if ov.OpenRouter.AppURL == nil || ov.OpenRouter.AppURL.Mode != identity.ModeCustom || ov.OpenRouter.AppURL.Value != "https://fixture.example/or-backend" {
		t.Fatalf("override app_url=%+v", ov.OpenRouter.AppURL)
	}
	if ov.OpenRouter.AppTitle == nil || ov.OpenRouter.AppTitle.Mode != identity.ModeDrop {
		t.Fatalf("override app_title=%+v", ov.OpenRouter.AppTitle)
	}

	eff := identity.MergeUpstream(cfg.Identity, ov)
	if got := eff.UserAgentValue(); got != "FixtureORUA/2" {
		t.Fatalf("merged UA=%q want FixtureORUA/2", got)
	}
	if got := eff.AppURLValue(); got != "https://fixture.example/or-backend" {
		t.Fatalf("merged app_url=%q", got)
	}
	if got := eff.AppTitleValue(); got != "" {
		t.Fatalf("merged app_title=%q want empty (drop)", got)
	}
	if got := identity.EffectiveDownstreamOf(cfg.Identity).ServerValue(); got != "FixtureServer/1" {
		t.Fatalf("server=%q", got)
	}
}
