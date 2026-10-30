package config_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"gopkg.in/yaml.v3"
)

func TestHTTPHeadersYAML_emptyKeepsDefaults(t *testing.T) {
	t.Parallel()
	var cfg config.Config
	if err := yaml.Unmarshal([]byte("server:\n  address: 127.0.0.1:8080\n"), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := cfg.HTTPHeaders.Effective()
	want := aipsdk.DefaultHTTPHeaders()
	if got.APIKey[0] != want.APIKey[0] || got.Route[0] != want.Route[0] {
		t.Fatalf("defaults: api_key=%v route=%v", got.APIKey, got.Route)
	}
}

func TestHTTPHeadersYAML_aliasesAppendAfterDefaults(t *testing.T) {
	t.Parallel()
	const y = `
http_headers:
  api_key: [X-Gateway-Key]
  route: [X-Custom-Route]
`
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(y), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := cfg.HTTPHeaders.Effective()
	if got.APIKey[0] != aipsdk.HeaderAuthorization {
		t.Fatalf("api_key[0]=%q", got.APIKey[0])
	}
	if got.APIKey[len(got.APIKey)-1] != "X-Gateway-Key" {
		t.Fatalf("api_key last=%q", got.APIKey[len(got.APIKey)-1])
	}
	if got.Route[0] != aipsdk.HeaderRoute || got.Route[1] != "X-Custom-Route" {
		t.Fatalf("route=%v", got.Route)
	}
}

func TestValidate_acceptsHTTPHeaderAlias(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins:     config.PluginsConfig{Backends: []config.PluginConfig{{ID: "b1", Enabled: true}}},
		HTTPHeaders: config.HTTPHeadersConfig{Route: []string{"X-Custom-Route"}},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("valid alias: %v", err)
	}
}

func TestValidate_rejectsInvalidHTTPHeaderAlias(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins:     config.PluginsConfig{Backends: []config.PluginConfig{{ID: "b1", Enabled: true}}},
		HTTPHeaders: config.HTTPHeadersConfig{Route: []string{"X AIProxer"}},
	}
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "http_headers.route") {
		t.Fatalf("want http_headers.route validation error, got %v", err)
	}
}

func TestHTTPHeadersYAML_workspaceAliasesAppendAfterDefault(t *testing.T) {
	t.Parallel()
	const y = `
http_headers:
  workspace: [X-Custom-WS]
`
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(y), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := cfg.HTTPHeaders.Effective()
	if len(got.Workspace) != 2 || got.Workspace[0] != aipsdk.HeaderWorkspace || got.Workspace[1] != "X-Custom-WS" {
		t.Fatalf("workspace=%v", got.Workspace)
	}
}

func TestValidate_rejectsWorkspaceCollisionAcrossFields(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, y, wantErr string }{
		{"api_key", "workspace: [Authorization]", "http_headers.workspace"},
		{"route", "workspace: [X-AIP-Route]", "http_headers.workspace"},
		{"session_id", "workspace: [X-AIP-Session-Id]", "http_headers.workspace"},
		{"resume_token", "workspace: [X-AIP-Resume-Token]", "http_headers.workspace"},
		{"a_leg_id", "workspace: [X-AIP-A-Leg-Id]", "http_headers.workspace"},
		{"session_hint", "workspace: [X-AIP-Session-Hint]", "http_headers.workspace"},
		{"trace", "workspace: [X-Trace-ID]", "http_headers.workspace"},
		{"diagnostics_secret", "workspace: [X-AIP-Diagnostics-Secret]", "http_headers.workspace"},
		{"other_alias", "route: [X-Custom-Route]\nworkspace: [x-custom-route]", "http_headers.workspace"},
		{"reverse_route", "route: [X-AIP-Workspace]", "http_headers.workspace"},
		{"reverse_session", "session_id: [X-AIP-Workspace]", "http_headers.workspace"},
		{"own_default_ok", "workspace: [X-AIP-Workspace, X-Custom-WS]", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var h config.HTTPHeadersConfig
			if err := yaml.Unmarshal([]byte(tc.y), &h); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			cfg := &config.Config{
				Plugins:     config.PluginsConfig{Backends: []config.PluginConfig{{ID: "b1", Enabled: true}}},
				HTTPHeaders: h,
			}
			err := config.Validate(cfg)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("own-default workspace alias must stay valid: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want %s collision error, got %v", tc.wantErr, err)
			}
		})
	}
}
