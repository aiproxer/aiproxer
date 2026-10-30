package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"gopkg.in/yaml.v3"
)

// Claim: provisioning accepts names/references only, without leaking rejected values.
// Source: task 3.3 design Stock host schema. Mutant: decode instances with yaml.Decode.
func TestBackendDiscovery_InstanceReferencesStrictAndValueFree(t *testing.T) {
	valid := "instances: {agent: {kind: agentkind, secret_refs: {api_key: {env: TEST_KEY}}, execution_env: [HOME, PATH]}}\n"
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(valid), &n); err != nil {
		t.Fatal(err)
	}
	if _, err := config.DecodeBackendDiscovery(n); err != nil {
		t.Fatal(err)
	}
	for i, raw := range []string{
		"instances: {agent: {kind: agentkind, secret_refs: {api_key: CANARY_LITERAL}}}",
		"instances: {agent: {kind: agentkind, secret_refs: {api_key: {env: TEST_KEY, value: CANARY_LITERAL}}}}",
		"instances: {agent: {kind: agentkind, execution_env: [LD_PRELOAD]}}",
		"instances: {agent: {kind: agentkind, execution_env: [HOME, home]}}",
		"instances: {agent: {kind: agentkind, kind: CANARY_LITERAL}}",
		"instances: {agent: {kind: 12}}",
		"instances: {agent: {kind: &alias agentkind}, other: {kind: *alias}}",
		"instances: {agent: {kind: agentkind, secret_refs: {api_key: {env: 'CANARY_LITERAL${KEY}'}}}}",
		"instances: {agent: {kind: agentkind, CANARY_LITERAL: true}}",
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			var node yaml.Node
			if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
				t.Fatal(err)
			}
			_, err := config.DecodeBackendDiscovery(node)
			if err == nil || strings.Contains(err.Error(), "CANARY_LITERAL") {
				t.Fatalf("want value-free rejection: %v", err)
			}
		})
	}
}

func TestBackendDiscovery_InstanceSchemaBoundsAndDisabledValidation(t *testing.T) {
	if err := config.ValidateBackendInstanceBindings(nil); err == nil || err.Error() != "config: nil" {
		t.Fatalf("nil config must return the discovery validation error: %v", err)
	}
	for _, count := range []int{64, 65} {
		var raw strings.Builder
		raw.WriteString("instances:\n")
		for i := range count {
			fmt.Fprintf(&raw, "  agent%d: {kind: agentkind}\n", i)
		}
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(raw.String()), &node); err != nil {
			t.Fatal(err)
		}
		_, err := config.DecodeBackendDiscovery(node)
		if (err != nil) != (count > 64) {
			t.Fatalf("binding count %d: %v", count, err)
		}
	}
	cfg := &config.Config{Plugins: config.PluginsConfig{
		Backends:         []config.PluginConfig{{ID: "agent", Kind: "agentkind", Enabled: false}},
		BackendDiscovery: config.BackendDiscoveryConfig{Instances: map[string]config.BackendInstanceProvisioning{"agent": {Kind: "agentkind"}}},
	}}
	if err := config.ValidateBackendInstanceBindings(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Plugins.BackendDiscovery.Instances["agent"] = config.BackendInstanceProvisioning{Kind: "wrongkind"}
	if err := config.ValidateBackendInstanceBindings(cfg); err == nil {
		t.Fatal("malformed disabled binding accepted")
	}
}

func TestBackendDiscovery_DefaultsWhenAbsent(t *testing.T) {
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
	bd := cfg.Plugins.BackendDiscovery
	if bd.Enabled || bd.Strict || bd.DevelopmentMode || len(bd.Paths) != 0 {
		t.Fatalf("expected zero defaults, got %+v", bd)
	}
	if len(cfg.Plugins.Backends) != 1 || cfg.Plugins.Backends[0].ID != "stub" {
		t.Fatalf("existing backends changed: %+v", cfg.Plugins.Backends)
	}
}

func TestBackendDiscovery_DecodePathsStrictDevelopment(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
access:
  mode: single_user
continuity:
  in_memory: true
plugins:
  backend_discovery:
    enabled: true
    paths:
      - /srv/company/AIProxer/plugins
    strict: true
    development_mode: true
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
	bd := cfg.Plugins.BackendDiscovery
	if !bd.Enabled || !bd.Strict || !bd.DevelopmentMode {
		t.Fatalf("flags: %+v", bd)
	}
	if len(bd.Paths) != 1 || bd.Paths[0] != "/srv/company/AIProxer/plugins" {
		t.Fatalf("paths: %+v", bd.Paths)
	}
}

func TestBackendDiscovery_RejectsUnknownKeys(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
continuity:
  in_memory: true
plugins:
  backend_discovery:
    enabled: true
    codex_catalog: true
  backends:
    - id: stub
      enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), `unknown config key`) {
		t.Fatalf("want unknown key rejection, got %v", err)
	}
}

func TestBackendDiscovery_ProductionRejectsDevelopmentModeMultiUser(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "multi_user"},
		Server:     config.ServerConfig{Address: "0.0.0.0:8080", AuthMode: config.AuthModeExternal},
		Auth:       config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{
				Enabled:         true,
				Paths:           []string{"/tmp/plugins"},
				DevelopmentMode: true,
			},
			Backends: []config.PluginConfig{{ID: "stub", Enabled: true}},
		},
	}
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "development_mode") {
		t.Fatalf("want production rejection of development_mode, got %v", err)
	}
}

func TestBackendDiscovery_DevelopmentModeRequiresPaths(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "single_user"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{
				Enabled:         true,
				DevelopmentMode: true,
			},
			Backends: []config.PluginConfig{{ID: "stub", Enabled: true}},
		},
	}
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "paths") {
		t.Fatalf("want paths required, got %v", err)
	}
}

func TestBackendDiscovery_ValidateRejectsEmptyPathEntry(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{
				Enabled: true,
				Paths:   []string{"  "},
			},
			Backends: []config.PluginConfig{{ID: "stub", Enabled: true}},
		},
	}
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "paths[0]") {
		t.Fatalf("want empty path rejection, got %v", err)
	}
}

func TestBackendDiscovery_StrictDecodeHelperRejectsUnknown(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("enabled: true\nextra: 1\n"), &n); err != nil {
		t.Fatal(err)
	}
	_, err := config.DecodeBackendDiscovery(n)
	if err == nil || !strings.Contains(err.Error(), `unknown config key`) {
		t.Fatalf("got %v", err)
	}
}

func TestBackendDiscovery_ExistingBackendRowsUnchanged(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
continuity:
  in_memory: true
plugins:
  backends:
    - kind: openrouter
      id: openrouter-primary
      enabled: true
      config:
        api_key: secret-value
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	be := cfg.Plugins.Backends[0]
	if be.Kind != "openrouter" || be.ID != "openrouter-primary" || !be.Enabled {
		t.Fatalf("backend row changed: %+v", be)
	}
	var raw map[string]any
	if err := be.Config.Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if raw["api_key"] != "secret-value" {
		t.Fatalf("opaque config changed: %+v", raw)
	}
}
