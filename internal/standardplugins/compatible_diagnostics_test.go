package standardplugins

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openresponsescompat"
	"gopkg.in/yaml.v3"
)

func TestProjectCompatibleBackendRows_secretSafeAndBounded(t *testing.T) {
	t.Parallel()
	secret := "sk-compatible-diag-secret"
	cfg := compatibleDiagnosticsConfig(t, `backend_prefix: provider123
base_url: https://api.provider123.example/v1
api_key_env_var_root: PROVIDER123_API_KEY
tokenizer: cl100k_base
max_concurrent_requests: 2
models:
  source: inline
  items:
    - canonical_id: provider123/model-a
      native_id: model-a
`)

	rows := ProjectCompatibleBackendRows(cfg)
	if len(rows) != 1 {
		t.Fatalf("rows=%d", len(rows))
	}
	row := rows[0]
	if row.Origin != "built_in_compatible" {
		t.Fatalf("origin=%q", row.Origin)
	}
	if row.InstanceID != "provider123" || row.FactoryKind != CustomOpenAILegacyCompatibleID {
		t.Fatalf("instance=%+v", row)
	}
	if row.Prefix != "provider123" {
		t.Fatalf("prefix=%q", row.Prefix)
	}
	if row.EndpointIdentity != "https://api.provider123.example/v1" {
		t.Fatalf("endpoint=%q", row.EndpointIdentity)
	}
	if !row.AuthConfigured {
		t.Fatal("auth must be configured when env root is present")
	}
	if row.TokenizerID != "cl100k_base" {
		t.Fatalf("tokenizer=%q", row.TokenizerID)
	}
	if row.ConcurrencyPolicy != "limit:2" {
		t.Fatalf("concurrency=%q", row.ConcurrencyPolicy)
	}
	if row.InventoryState != "static_inline" {
		t.Fatalf("inventory=%q", row.InventoryState)
	}
	var forbidden yaml.Node
	if err := yaml.Unmarshal([]byte(`backend_prefix: x
base_url: https://example.test/v1
api_key: `+secret), &forbidden); err != nil {
		t.Fatal(err)
	}
	if _, err := config.DecodeCompatibleModeConfig("", CustomOpenAILegacyCompatibleID, forbidden); err == nil {
		t.Fatal("expected forbidden secret rejection")
	}
	encoded := row.InstanceID + row.EndpointIdentity + row.ConcurrencyPolicy
	if strings.Contains(encoded, secret) {
		t.Fatalf("projection leaked secret")
	}
}

func TestProjectCompatibleBackendInstanceRows_doesNotDuplicateAcrossCompatibleFactories(t *testing.T) {
	t.Parallel()
	cfg := compatibleDiagnosticsConfig(t, `backend_prefix: provider123
base_url: https://api.provider123.example/v1
api_key_env_var_root: PROVIDER123_API_KEY
`)
	rows := ProjectCompatibleBackendInstanceRows(cfg)
	if len(rows) != 1 {
		t.Fatalf("projected instance diagnostics=%d, want 1", len(rows))
	}
	if rows[0].InstanceID != "provider123" {
		t.Fatalf("projected instance=%q", rows[0].InstanceID)
	}
}

func TestProjectCompatibleBackendRows_noAuthRemoteInventory(t *testing.T) {
	t.Parallel()
	cfg := compatibleDiagnosticsConfigAnthropic(t, `backend_prefix: local-compat
base_url: http://127.0.0.1:9
`)
	row := ProjectCompatibleBackendRows(cfg)[0]
	if row.AuthConfigured {
		t.Fatal("no-auth endpoint must report auth_configured=false")
	}
	if row.ConcurrencyPolicy != "default" {
		t.Fatalf("concurrency=%q", row.ConcurrencyPolicy)
	}
	if row.InventoryState != "remote" {
		t.Fatalf("inventory=%q", row.InventoryState)
	}
}

func TestProjectCompatibleBackendRows_openResponsesProfileProvenance(t *testing.T) {
	t.Parallel()
	cfg := compatibleDiagnosticsConfigOpenResponses(t, `backend_prefix: my-or
base_url: https://api.example.test/openresponses/v1
profile: `+openresponsescompat.DefaultProfile+`
models:
  source: inline
  items:
    - canonical_id: my-or/model-a
      native_id: model-a
`)
	rows := ProjectCompatibleBackendRows(cfg)
	if len(rows) != 1 {
		t.Fatalf("rows=%d", len(rows))
	}
	row := rows[0]
	if row.Origin != "built_in_compatible" {
		t.Fatalf("origin=%q", row.Origin)
	}
	if row.InstanceID != "or-prov" || row.FactoryKind != CustomOpenResponsesCompatibleID {
		t.Fatalf("instance/factory provenance = %+v", row)
	}
	if row.Prefix != "my-or" {
		t.Fatalf("prefix=%q", row.Prefix)
	}
	if row.Profile != openresponsescompat.DefaultProfile {
		t.Fatalf("profile=%q, want %q", row.Profile, openresponsescompat.DefaultProfile)
	}
	if row.EndpointIdentity != "https://api.example.test/openresponses/v1" {
		t.Fatalf("endpoint=%q", row.EndpointIdentity)
	}
	if row.InventoryState != "static_inline" {
		t.Fatalf("inventory=%q", row.InventoryState)
	}
}

func TestProjectCompatibleBackendRows_openResponsesUnknownConfigRejected(t *testing.T) {
	t.Parallel()
	cfg := compatibleDiagnosticsConfigOpenResponses(t, `backend_prefix: my-or
base_url: https://api.example.test/openresponses/v1
openrouter_attribution: on
`)
	rows := ProjectCompatibleBackendRows(cfg)
	if len(rows) != 1 {
		t.Fatalf("rows=%d", len(rows))
	}
	if rows[0].ConfigError == "" {
		t.Fatal("expected config error for provider-boundary key")
	}
	if !strings.Contains(rows[0].ConfigError, "openrouter_attribution") {
		t.Fatalf("config error = %q, want key named", rows[0].ConfigError)
	}
}

func compatibleDiagnosticsConfigOpenResponses(t *testing.T, raw string) *config.Config {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	return &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				ID:      "or-prov",
				Kind:    CustomOpenResponsesCompatibleID,
				Enabled: true,
				Config:  node,
			}},
		},
	}
}

func compatibleDiagnosticsConfig(t *testing.T, raw string) *config.Config {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	return &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				ID:      "provider123",
				Kind:    CustomOpenAILegacyCompatibleID,
				Enabled: true,
				Config:  node,
			}},
		},
	}
}

func compatibleDiagnosticsConfigAnthropic(t *testing.T, raw string) *config.Config {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	return &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				ID:      "local-compat",
				Kind:    CustomAnthropicCompatibleID,
				Enabled: true,
				Config:  node,
			}},
		},
	}
}
