package identity_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/identity"
	"gopkg.in/yaml.v3"
)

func TestApplyDefaults_literalProductDefaults(t *testing.T) {
	t.Parallel()
	var cfg identity.Config
	identity.ApplyDefaults(&cfg)

	if cfg.Upstream.UserAgent.Mode != identity.ModeProxy {
		t.Fatalf("user_agent.mode: got %q want proxy", cfg.Upstream.UserAgent.Mode)
	}
	if cfg.Upstream.UserAgent.Value != "" {
		t.Fatalf("user_agent.value must stay empty in proxy mode, got %q", cfg.Upstream.UserAgent.Value)
	}
	if cfg.Upstream.OpenRouter.AppURL.Mode != identity.ModeProxy {
		t.Fatalf("app_url.mode: got %q want proxy", cfg.Upstream.OpenRouter.AppURL.Mode)
	}
	if cfg.Upstream.OpenRouter.AppTitle.Mode != identity.ModeProxy {
		t.Fatalf("app_title.mode: got %q want proxy", cfg.Upstream.OpenRouter.AppTitle.Mode)
	}
	if cfg.Downstream.Server.Mode != identity.ModeProxy {
		t.Fatalf("server.mode: got %q want proxy", cfg.Downstream.Server.Mode)
	}

	up := identity.MergeUpstream(cfg, nil)
	if got := up.UserAgentValue(); got != "aiproxer" {
		t.Fatalf("resolved user_agent: got %q", got)
	}
	if got := up.AppURLValue(); got != "https://github.com/aiproxer/aiproxer" {
		t.Fatalf("resolved app_url: got %q", got)
	}
	if got := up.AppTitleValue(); got != "aiproxer" {
		t.Fatalf("resolved app_title: got %q", got)
	}
	down := identity.EffectiveDownstreamOf(cfg)
	if got := down.ServerValue(); got != "aiproxer" {
		t.Fatalf("resolved server: got %q", got)
	}
}

func TestApplyDefaults_nilSafe(t *testing.T) {
	t.Parallel()
	identity.ApplyDefaults(nil)
}

func TestValidate_normalizesPaddedMixedCaseModeAndResolves(t *testing.T) {
	t.Parallel()
	cfg := identity.Config{
		Upstream: identity.UpstreamPolicy{
			UserAgent: identity.FieldPolicy{Mode: " PrOxY "},
			OpenRouter: identity.OpenRouterPolicy{
				AppURL:   identity.FieldPolicy{Mode: "PROXY"},
				AppTitle: identity.FieldPolicy{Mode: "\tproxy\t"},
			},
		},
		Downstream: identity.DownstreamPolicy{
			Server: identity.FieldPolicy{Mode: " Proxy "},
		},
	}
	if err := identity.Validate(&cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if cfg.Upstream.UserAgent.Mode != identity.ModeProxy {
		t.Fatalf("persisted user_agent.mode: %q", cfg.Upstream.UserAgent.Mode)
	}
	if cfg.Upstream.OpenRouter.AppURL.Mode != identity.ModeProxy {
		t.Fatalf("persisted app_url.mode: %q", cfg.Upstream.OpenRouter.AppURL.Mode)
	}
	if cfg.Upstream.OpenRouter.AppTitle.Mode != identity.ModeProxy {
		t.Fatalf("persisted app_title.mode: %q", cfg.Upstream.OpenRouter.AppTitle.Mode)
	}
	if cfg.Downstream.Server.Mode != identity.ModeProxy {
		t.Fatalf("persisted server.mode: %q", cfg.Downstream.Server.Mode)
	}
	up := identity.MergeUpstream(cfg, nil)
	if got := up.UserAgentValue(); got != "aiproxer" {
		t.Fatalf("resolved user_agent: %q", got)
	}
	if got := up.AppURLValue(); got != "https://github.com/aiproxer/aiproxer" {
		t.Fatalf("resolved app_url: %q", got)
	}
	if got := up.AppTitleValue(); got != "aiproxer" {
		t.Fatalf("resolved app_title: %q", got)
	}
	if got := identity.EffectiveDownstreamOf(cfg).ServerValue(); got != "aiproxer" {
		t.Fatalf("resolved server: %q", got)
	}
}

func TestValidate_persistsTrimmedCustomValues(t *testing.T) {
	t.Parallel()
	cfg := validDefaults(t)
	cfg.Upstream.UserAgent = identity.FieldPolicy{Mode: identity.ModeCustom, Value: "  MyAgent/1  "}
	cfg.Upstream.OpenRouter.AppURL = identity.FieldPolicy{Mode: identity.ModeCustom, Value: "  https://example.com/app  "}
	cfg.Upstream.OpenRouter.AppTitle = identity.FieldPolicy{Mode: identity.ModeCustom, Value: "\tTitle\t"}
	cfg.Downstream.Server = identity.FieldPolicy{Mode: identity.ModeCustom, Value: "  srv  "}
	if err := identity.Validate(&cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if cfg.Upstream.UserAgent.Value != "MyAgent/1" {
		t.Fatalf("user_agent.value: %q", cfg.Upstream.UserAgent.Value)
	}
	if cfg.Upstream.OpenRouter.AppURL.Value != "https://example.com/app" {
		t.Fatalf("app_url.value: %q", cfg.Upstream.OpenRouter.AppURL.Value)
	}
	if cfg.Upstream.OpenRouter.AppTitle.Value != "Title" {
		t.Fatalf("app_title.value: %q", cfg.Upstream.OpenRouter.AppTitle.Value)
	}
	if cfg.Downstream.Server.Value != "srv" {
		t.Fatalf("server.value: %q", cfg.Downstream.Server.Value)
	}
}

func TestValidate_nonCustomWhitespaceValueNormalizesEmpty(t *testing.T) {
	t.Parallel()
	cfg := validDefaults(t)
	cfg.Upstream.UserAgent = identity.FieldPolicy{Mode: identity.ModeProxy, Value: "   "}
	if err := identity.Validate(&cfg); err != nil {
		t.Fatalf("whitespace-only non-custom should normalize: %v", err)
	}
	if cfg.Upstream.UserAgent.Value != "" {
		t.Fatalf("expected empty value, got %q", cfg.Upstream.UserAgent.Value)
	}
}

func TestValidateBackendOverride_matrix(t *testing.T) {
	t.Parallel()
	t.Run("nil_ok", func(t *testing.T) {
		t.Parallel()
		if err := identity.ValidateBackendOverride(nil); err != nil {
			t.Fatalf("nil: %v", err)
		}
	})
	t.Run("empty_ok", func(t *testing.T) {
		t.Parallel()
		if err := identity.ValidateBackendOverride(&identity.BackendOverride{}); err != nil {
			t.Fatalf("empty: %v", err)
		}
	})
	t.Run("explicit_drop_ok", func(t *testing.T) {
		t.Parallel()
		ov := &identity.BackendOverride{
			UserAgent: &identity.FieldPolicy{Mode: identity.ModeDrop},
			OpenRouter: &identity.OpenRouterOverride{
				AppURL:   &identity.FieldPolicy{Mode: identity.ModeDrop},
				AppTitle: &identity.FieldPolicy{Mode: identity.ModeDrop},
			},
		}
		if err := identity.ValidateBackendOverride(ov); err != nil {
			t.Fatalf("drop: %v", err)
		}
	})
	t.Run("custom_url_ok", func(t *testing.T) {
		t.Parallel()
		ov := &identity.BackendOverride{
			OpenRouter: &identity.OpenRouterOverride{
				AppURL: &identity.FieldPolicy{Mode: identity.ModeCustom, Value: "https://example.com/x"},
			},
		}
		if err := identity.ValidateBackendOverride(ov); err != nil {
			t.Fatalf("url: %v", err)
		}
		if ov.OpenRouter.AppURL.Value != "https://example.com/x" {
			t.Fatalf("persisted: %q", ov.OpenRouter.AppURL.Value)
		}
	})
	t.Run("invalid_url", func(t *testing.T) {
		t.Parallel()
		ov := &identity.BackendOverride{
			OpenRouter: &identity.OpenRouterOverride{
				AppURL: &identity.FieldPolicy{Mode: identity.ModeCustom, Value: "https://user:pass@example.com/"},
			},
		}
		if err := identity.ValidateBackendOverride(ov); err == nil {
			t.Fatal("expected userinfo rejection")
		}
	})
	t.Run("custom_missing_value", func(t *testing.T) {
		t.Parallel()
		ov := &identity.BackendOverride{
			UserAgent: &identity.FieldPolicy{Mode: identity.ModeCustom},
		}
		if err := identity.ValidateBackendOverride(ov); err == nil {
			t.Fatal("expected custom value required")
		}
	})
	t.Run("non_custom_with_value", func(t *testing.T) {
		t.Parallel()
		ov := &identity.BackendOverride{
			UserAgent: &identity.FieldPolicy{Mode: identity.ModeDrop, Value: "nope"},
		}
		if err := identity.ValidateBackendOverride(ov); err == nil {
			t.Fatal("expected rejection")
		}
	})
	t.Run("normalizes_mode", func(t *testing.T) {
		t.Parallel()
		ov := &identity.BackendOverride{
			UserAgent: &identity.FieldPolicy{Mode: " DROP "},
		}
		if err := identity.ValidateBackendOverride(ov); err != nil {
			t.Fatalf("validate: %v", err)
		}
		if ov.UserAgent.Mode != identity.ModeDrop {
			t.Fatalf("persisted mode: %q", ov.UserAgent.Mode)
		}
	})
	t.Run("omit_vs_explicit_drop", func(t *testing.T) {
		t.Parallel()
		global := validDefaults(t)
		global.Upstream.UserAgent = identity.FieldPolicy{Mode: identity.ModeCustom, Value: "GlobalUA"}
		ov := &identity.BackendOverride{
			OpenRouter: &identity.OpenRouterOverride{
				AppTitle: &identity.FieldPolicy{Mode: identity.ModeDrop},
			},
		}
		if err := identity.ValidateBackendOverride(ov); err != nil {
			t.Fatalf("validate: %v", err)
		}
		got := identity.MergeUpstream(global, ov)
		if got.UserAgent.Value != "GlobalUA" {
			t.Fatalf("omitted user_agent should inherit: %+v", got.UserAgent)
		}
		if got.AppTitle.Mode != identity.ModeDrop {
			t.Fatalf("explicit drop: %+v", got.AppTitle)
		}
		if ov.OpenRouter.AppURL != nil {
			t.Fatalf("omitted app_url pointer must stay nil")
		}
	})
}

func TestMergeUpstream_zeroGlobalAppliesDefaultsWithoutMutation(t *testing.T) {
	t.Parallel()
	var global identity.Config
	eff := identity.MergeUpstream(global, nil)
	if eff.UserAgent.Mode != identity.ModeProxy {
		t.Fatalf("user_agent.mode: %q", eff.UserAgent.Mode)
	}
	if ua := eff.UserAgentValue(); ua != "aiproxer" {
		t.Fatalf("resolved user_agent: %q", ua)
	}
	if url := eff.AppURLValue(); url != "https://github.com/aiproxer/aiproxer" {
		t.Fatalf("resolved app_url: %q", url)
	}
	if global.Upstream.UserAgent.Mode != "" {
		t.Fatalf("global mutated: %+v", global.Upstream.UserAgent)
	}
}

func TestValidate_modeMatrix(t *testing.T) {
	t.Parallel()
	upstreamModes := []identity.Mode{
		identity.ModeProxy, identity.ModePassthrough, identity.ModeCustom, identity.ModeDrop,
	}
	downstreamModes := []identity.Mode{
		identity.ModeProxy, identity.ModeCustom, identity.ModeDrop,
	}

	for _, mode := range upstreamModes {
		t.Run("upstream_user_agent_"+string(mode), func(t *testing.T) {
			t.Parallel()
			cfg := validDefaults(t)
			cfg.Upstream.UserAgent = policyForMode(mode, "CustomAgent/1.0")
			if err := identity.Validate(&cfg); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
		t.Run("upstream_app_url_"+string(mode), func(t *testing.T) {
			t.Parallel()
			cfg := validDefaults(t)
			cfg.Upstream.OpenRouter.AppURL = policyForMode(mode, "https://example.com/app")
			if err := identity.Validate(&cfg); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
		t.Run("upstream_app_title_"+string(mode), func(t *testing.T) {
			t.Parallel()
			cfg := validDefaults(t)
			cfg.Upstream.OpenRouter.AppTitle = policyForMode(mode, "MyApp")
			if err := identity.Validate(&cfg); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	for _, mode := range downstreamModes {
		t.Run("downstream_server_"+string(mode), func(t *testing.T) {
			t.Parallel()
			cfg := validDefaults(t)
			cfg.Downstream.Server = policyForMode(mode, "CustomServer/1.0")
			if err := identity.Validate(&cfg); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	t.Run("downstream_server_rejects_passthrough", func(t *testing.T) {
		t.Parallel()
		cfg := validDefaults(t)
		cfg.Downstream.Server = identity.FieldPolicy{Mode: identity.ModePassthrough}
		if err := identity.Validate(&cfg); err == nil {
			t.Fatal("expected error for downstream server passthrough")
		}
	})

	t.Run("rejects_unknown_mode", func(t *testing.T) {
		t.Parallel()
		cfg := validDefaults(t)
		cfg.Upstream.UserAgent.Mode = identity.Mode("transparent")
		if err := identity.Validate(&cfg); err == nil {
			t.Fatal("expected error for unknown mode")
		}
	})
}

func TestValidate_customRequiresValue(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		mut  func(*identity.Config)
	}{
		{"user_agent", func(c *identity.Config) {
			c.Upstream.UserAgent = identity.FieldPolicy{Mode: identity.ModeCustom, Value: ""}
		}},
		{"app_url", func(c *identity.Config) {
			c.Upstream.OpenRouter.AppURL = identity.FieldPolicy{Mode: identity.ModeCustom, Value: ""}
		}},
		{"app_title", func(c *identity.Config) {
			c.Upstream.OpenRouter.AppTitle = identity.FieldPolicy{Mode: identity.ModeCustom, Value: ""}
		}},
		{"server", func(c *identity.Config) {
			c.Downstream.Server = identity.FieldPolicy{Mode: identity.ModeCustom, Value: ""}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validDefaults(t)
			tc.mut(&cfg)
			if err := identity.Validate(&cfg); err == nil {
				t.Fatal("expected error for custom without value")
			}
		})
	}
}

func TestValidate_rejectsValueWithNonCustom(t *testing.T) {
	t.Parallel()
	modes := []identity.Mode{identity.ModeProxy, identity.ModePassthrough, identity.ModeDrop}
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			cfg := validDefaults(t)
			cfg.Upstream.UserAgent = identity.FieldPolicy{Mode: mode, Value: "leftover"}
			if err := identity.Validate(&cfg); err == nil {
				t.Fatal("expected error when value set with non-custom mode")
			}
		})
	}
}

func TestValidate_rejectsControlCharacters(t *testing.T) {
	t.Parallel()
	bad := []string{
		"agent\rname",
		"agent\nname",
		"agent\x00name",
		"agent\x01name",
		"agent\x1fname",
	}
	for _, v := range bad {
		t.Run(strings.ReplaceAll(v, "\x00", "NUL"), func(t *testing.T) {
			t.Parallel()
			cfg := validDefaults(t)
			cfg.Upstream.UserAgent = identity.FieldPolicy{Mode: identity.ModeCustom, Value: v}
			if err := identity.Validate(&cfg); err == nil {
				t.Fatalf("expected control-char rejection for %q", v)
			}
		})
	}
}

func TestValidate_bounds(t *testing.T) {
	t.Parallel()
	t.Run("user_agent_too_long", func(t *testing.T) {
		t.Parallel()
		cfg := validDefaults(t)
		cfg.Upstream.UserAgent = identity.FieldPolicy{
			Mode:  identity.ModeCustom,
			Value: strings.Repeat("a", 513),
		}
		if err := identity.Validate(&cfg); err == nil {
			t.Fatal("expected user_agent length error")
		}
	})
	t.Run("app_title_too_long", func(t *testing.T) {
		t.Parallel()
		cfg := validDefaults(t)
		cfg.Upstream.OpenRouter.AppTitle = identity.FieldPolicy{
			Mode:  identity.ModeCustom,
			Value: strings.Repeat("t", 257),
		}
		if err := identity.Validate(&cfg); err == nil {
			t.Fatal("expected app_title length error")
		}
	})
	t.Run("app_url_too_long", func(t *testing.T) {
		t.Parallel()
		cfg := validDefaults(t)
		cfg.Upstream.OpenRouter.AppURL = identity.FieldPolicy{
			Mode:  identity.ModeCustom,
			Value: "https://example.com/" + strings.Repeat("p", 2048),
		}
		if err := identity.Validate(&cfg); err == nil {
			t.Fatal("expected app_url length error")
		}
	})
	t.Run("server_too_long", func(t *testing.T) {
		t.Parallel()
		cfg := validDefaults(t)
		cfg.Downstream.Server = identity.FieldPolicy{
			Mode:  identity.ModeCustom,
			Value: strings.Repeat("s", 513),
		}
		if err := identity.Validate(&cfg); err == nil {
			t.Fatal("expected server length error")
		}
	})
}

func TestValidate_openRouterURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"https_ok", "https://example.com/app", false},
		{"http_ok", "http://example.com/app", false},
		{"relative", "/app", true},
		{"no_scheme", "example.com/app", true},
		{"ftp", "ftp://example.com/app", true},
		{"userinfo", "https://user:pass@example.com/app", true},
		{"fragment", "https://example.com/app#frag", true},
		{"empty_custom", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validDefaults(t)
			cfg.Upstream.OpenRouter.AppURL = identity.FieldPolicy{
				Mode:  identity.ModeCustom,
				Value: tc.url,
			}
			err := identity.Validate(&cfg)
			if tc.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidate_nilConfig(t *testing.T) {
	t.Parallel()
	if err := identity.Validate(nil); err == nil {
		t.Fatal("expected nil config error")
	}
}

func TestValidate_zeroConfigAfterDefaults(t *testing.T) {
	t.Parallel()
	var cfg identity.Config
	identity.ApplyDefaults(&cfg)
	if err := identity.Validate(&cfg); err != nil {
		t.Fatalf("defaults should validate: %v", err)
	}
}

func TestMergeBackendOverride_inheritanceAndExplicitDrop(t *testing.T) {
	t.Parallel()
	global := validDefaults(t)
	global.Upstream.UserAgent = identity.FieldPolicy{Mode: identity.ModeCustom, Value: "GlobalUA/1"}
	global.Upstream.OpenRouter.AppURL = identity.FieldPolicy{Mode: identity.ModeCustom, Value: "https://global.example/"}
	global.Upstream.OpenRouter.AppTitle = identity.FieldPolicy{Mode: identity.ModeCustom, Value: "GlobalTitle"}

	t.Run("nil_override_inherits_all", func(t *testing.T) {
		t.Parallel()
		got := identity.MergeUpstream(global, nil)
		if got.UserAgent != global.Upstream.UserAgent {
			t.Fatalf("user_agent: %+v", got.UserAgent)
		}
		if got.AppURL != global.Upstream.OpenRouter.AppURL {
			t.Fatalf("app_url: %+v", got.AppURL)
		}
		if got.AppTitle != global.Upstream.OpenRouter.AppTitle {
			t.Fatalf("app_title: %+v", got.AppTitle)
		}
	})

	t.Run("omitted_fields_inherit", func(t *testing.T) {
		t.Parallel()
		drop := identity.ModeDrop
		override := &identity.BackendOverride{
			UserAgent: &identity.FieldPolicy{Mode: drop},
		}
		got := identity.MergeUpstream(global, override)
		if got.UserAgent.Mode != identity.ModeDrop {
			t.Fatalf("user_agent mode: %q", got.UserAgent.Mode)
		}
		if got.AppURL != global.Upstream.OpenRouter.AppURL {
			t.Fatalf("app_url should inherit: %+v", got.AppURL)
		}
		if got.AppTitle != global.Upstream.OpenRouter.AppTitle {
			t.Fatalf("app_title should inherit: %+v", got.AppTitle)
		}
	})

	t.Run("explicit_drop_distinct_from_omission", func(t *testing.T) {
		t.Parallel()
		override := &identity.BackendOverride{
			OpenRouter: &identity.OpenRouterOverride{
				AppURL: &identity.FieldPolicy{Mode: identity.ModeDrop},
			},
		}
		got := identity.MergeUpstream(global, override)
		if got.AppURL.Mode != identity.ModeDrop {
			t.Fatalf("app_url should be explicit drop, got %+v", got.AppURL)
		}
		if got.UserAgent != global.Upstream.UserAgent {
			t.Fatalf("user_agent should inherit: %+v", got.UserAgent)
		}
		if got.AppTitle != global.Upstream.OpenRouter.AppTitle {
			t.Fatalf("app_title should inherit: %+v", got.AppTitle)
		}
	})

	t.Run("partial_openrouter_override", func(t *testing.T) {
		t.Parallel()
		override := &identity.BackendOverride{
			OpenRouter: &identity.OpenRouterOverride{
				AppTitle: &identity.FieldPolicy{Mode: identity.ModeCustom, Value: "BackendTitle"},
			},
		}
		got := identity.MergeUpstream(global, override)
		if got.AppTitle.Value != "BackendTitle" {
			t.Fatalf("app_title: %+v", got.AppTitle)
		}
		if got.AppURL != global.Upstream.OpenRouter.AppURL {
			t.Fatalf("app_url should inherit: %+v", got.AppURL)
		}
	})
}

func TestEffectiveDownstream(t *testing.T) {
	t.Parallel()
	cfg := validDefaults(t)
	cfg.Downstream.Server = identity.FieldPolicy{Mode: identity.ModeCustom, Value: "aip-edge"}
	got := identity.EffectiveDownstreamOf(cfg)
	if got.Server.Value != "aip-edge" {
		t.Fatalf("server: %+v", got.Server)
	}
}

func TestEffectiveDownstream_modeDropServerValueEmpty(t *testing.T) {
	t.Parallel()
	cfg := validDefaults(t)
	cfg.Downstream.Server = identity.FieldPolicy{Mode: identity.ModeDrop}
	got := identity.EffectiveDownstreamOf(cfg)
	if got.Server.Mode != identity.ModeDrop {
		t.Fatalf("mode=%q want drop", got.Server.Mode)
	}
	if got.ServerValue() != "" {
		t.Fatalf("ServerValue=%q want empty", got.ServerValue())
	}
}

func TestYAMLDecode_identitySubtree(t *testing.T) {
	t.Parallel()
	const raw = `
upstream:
  user_agent:
    mode: custom
    value: "MyAgent/2.0"
  openrouter:
    app_url:
      mode: custom
      value: "https://example.com/product"
    app_title:
      mode: drop
downstream:
  server:
    mode: drop
`
	var cfg identity.Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	if cfg.Upstream.UserAgent.Mode != identity.ModeCustom || cfg.Upstream.UserAgent.Value != "MyAgent/2.0" {
		t.Fatalf("user_agent: %+v", cfg.Upstream.UserAgent)
	}
	if cfg.Upstream.OpenRouter.AppURL.Value != "https://example.com/product" {
		t.Fatalf("app_url: %+v", cfg.Upstream.OpenRouter.AppURL)
	}
	if cfg.Upstream.OpenRouter.AppTitle.Mode != identity.ModeDrop {
		t.Fatalf("app_title: %+v", cfg.Upstream.OpenRouter.AppTitle)
	}
	if cfg.Downstream.Server.Mode != identity.ModeDrop {
		t.Fatalf("server: %+v", cfg.Downstream.Server)
	}
	if err := identity.Validate(&cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestBackendOverride_yamlPartialPointers(t *testing.T) {
	t.Parallel()
	const raw = `
user_agent:
  mode: drop
openrouter:
  app_title:
    mode: custom
    value: "OnlyTitle"
`
	var ov identity.BackendOverride
	if err := yaml.Unmarshal([]byte(raw), &ov); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	if ov.UserAgent == nil || ov.UserAgent.Mode != identity.ModeDrop {
		t.Fatalf("user_agent: %+v", ov.UserAgent)
	}
	if ov.OpenRouter == nil || ov.OpenRouter.AppTitle == nil {
		t.Fatal("expected openrouter.app_title pointer")
	}
	if ov.OpenRouter.AppURL != nil {
		t.Fatalf("app_url should be omitted/nil, got %+v", ov.OpenRouter.AppURL)
	}
	if ov.OpenRouter.AppTitle.Value != "OnlyTitle" {
		t.Fatalf("app_title: %+v", ov.OpenRouter.AppTitle)
	}
}

func TestAcceptClientAppURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{name: "valid_https", raw: " https://example.com/app ", want: "https://example.com/app", ok: true},
		{name: "valid_http", raw: "http://localhost:8080/", want: "http://localhost:8080/", ok: true},
		{name: "blank", raw: "  ", want: "", ok: false},
		{name: "not_absolute", raw: "/relative", want: "", ok: false},
		{name: "ftp", raw: "ftp://example.com/", want: "", ok: false},
		{name: "userinfo", raw: "https://user:pass@example.com/", want: "", ok: false},
		{name: "fragment", raw: "https://example.com/#frag", want: "", ok: false},
		{name: "control", raw: "https://example.com/\x01", want: "", ok: false},
		{name: "overlong", raw: "https://example.com/" + strings.Repeat("a", identity.MaxAppURLBytes), want: "", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := identity.AcceptClientAppURL(tc.raw)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("AcceptClientAppURL(%q)=(%q,%v) want (%q,%v)", tc.raw, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestAcceptClientAppTitle(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{name: "valid", raw: " My App ", want: "My App", ok: true},
		{name: "blank", raw: "\t", want: "", ok: false},
		{name: "control", raw: "Bad\nTitle", want: "", ok: false},
		{name: "overlong", raw: strings.Repeat("t", identity.MaxAppTitleBytes+1), want: "", ok: false},
		{name: "max_ok", raw: strings.Repeat("t", identity.MaxAppTitleBytes), want: strings.Repeat("t", identity.MaxAppTitleBytes), ok: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := identity.AcceptClientAppTitle(tc.raw)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("AcceptClientAppTitle(%q)=(%q,%v) want (%q,%v)", tc.raw, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func validDefaults(t *testing.T) identity.Config {
	t.Helper()
	var cfg identity.Config
	identity.ApplyDefaults(&cfg)
	if err := identity.Validate(&cfg); err != nil {
		t.Fatalf("fixture defaults invalid: %v", err)
	}
	return cfg
}

func policyForMode(mode identity.Mode, customValue string) identity.FieldPolicy {
	if mode == identity.ModeCustom {
		return identity.FieldPolicy{Mode: mode, Value: customValue}
	}
	return identity.FieldPolicy{Mode: mode}
}
