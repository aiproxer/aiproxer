package standardplugins

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"gopkg.in/yaml.v3"
)

func frontendDiagnosticsConfig(t *testing.T, id string, enabled bool, raw string) *config.Config {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	return &config.Config{
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{
				ID:      id,
				Kind:    "openresponses",
				Enabled: enabled,
				Config:  node,
			}},
		},
	}
}

func TestProjectOpenResponsesFrontendRows_sanitizedProfileOriginPathTransport(t *testing.T) {
	t.Parallel()
	cfg := frontendDiagnosticsConfig(t, "or-fe", true, `
profile: 2026-04-24
base_path: /openresponses/v1
continuation:
  persistent_store: standard
  ttl: 24h
websocket:
  enabled: true
  max_connection_age: 60m
  idle_timeout: 5m
  max_queued_turns: 1
  allowed_origins:
    - https://app.example.test
    - https://dev.example.test
`)

	rows := ProjectOpenResponsesFrontendRows(cfg)
	if len(rows) != 1 {
		t.Fatalf("rows=%d", len(rows))
	}
	row := rows[0]
	if row.Origin != "client_facing" {
		t.Fatalf("origin=%q", row.Origin)
	}
	if row.InstanceID != "or-fe" || row.FactoryKind != "openresponses" {
		t.Fatalf("instance/factory provenance = %+v", row)
	}
	if !row.Enabled {
		t.Fatal("enabled must be projected")
	}
	if row.Profile != "2026-04-24" {
		t.Fatalf("profile=%q", row.Profile)
	}
	if row.BasePath != "/openresponses/v1" {
		t.Fatalf("base_path=%q", row.BasePath)
	}
	if !row.WebSocketEnabled {
		t.Fatal("websocket_enabled must be true")
	}
	if row.ContinuationStore != "standard" {
		t.Fatalf("continuation_store=%q", row.ContinuationStore)
	}
	if row.ContinuationTTL != "24h" {
		t.Fatalf("continuation_ttl=%q", row.ContinuationTTL)
	}
	if len(row.AllowedOrigins) != 2 || row.AllowedOrigins[0] != "https://app.example.test" {
		t.Fatalf("allowed_origins=%v", row.AllowedOrigins)
	}
	if row.Conformance != "profile:2026-04-24" {
		t.Fatalf("conformance=%q", row.Conformance)
	}
	wantCaps := []string{"ordered_items", "streaming", "tools", "compaction", "websocket"}
	if len(row.Capabilities) != len(wantCaps) {
		t.Fatalf("capabilities=%v, want %v", row.Capabilities, wantCaps)
	}
	for i := range wantCaps {
		if row.Capabilities[i] != wantCaps[i] {
			t.Fatalf("capabilities=%v, want %v", row.Capabilities, wantCaps)
		}
	}
	if len(row.RouteClaims) != 3 {
		t.Fatalf("route_claims=%v, want 3 (create/compact/ws)", row.RouteClaims)
	}
	for _, c := range row.RouteClaims {
		if c != "POST /openresponses/v1/responses" &&
			c != "POST /openresponses/v1/responses/compact" &&
			c != "GET /openresponses/v1/responses" {
			t.Fatalf("unexpected route claim %q", c)
		}
	}
}

func TestProjectOpenResponsesFrontendRows_webSocketDisabledOmitsWebSocketCapabilityAndClaim(t *testing.T) {
	t.Parallel()
	cfg := frontendDiagnosticsConfig(t, "or-no-ws", true, `
profile: 2026-04-24
base_path: /openresponses/v1
websocket:
  enabled: false
`)
	rows := ProjectOpenResponsesFrontendRows(cfg)
	if len(rows) != 1 {
		t.Fatalf("rows=%d", len(rows))
	}
	row := rows[0]
	if row.WebSocketEnabled {
		t.Fatal("websocket_enabled must be false")
	}
	for _, c := range row.Capabilities {
		if c == "websocket" {
			t.Fatalf("websocket capability must be omitted when disabled: %v", row.Capabilities)
		}
	}
	for _, c := range row.RouteClaims {
		if c == "GET /openresponses/v1/responses" {
			t.Fatalf("ws route claim must be omitted when disabled: %v", row.RouteClaims)
		}
	}
}

func TestProjectOpenResponsesFrontendRows_unknownConfigRejectedWithInstanceIdentity(t *testing.T) {
	t.Parallel()
	cfg := frontendDiagnosticsConfig(t, "or-bad", true, `
profile: 2026-04-24
base_path: /openresponses/v1
sniffing: enabled
`)
	rows := ProjectOpenResponsesFrontendRows(cfg)
	if len(rows) != 1 {
		t.Fatalf("rows=%d", len(rows))
	}
	if rows[0].ConfigError == "" {
		t.Fatal("expected config error for unknown field")
	}
	if !strings.Contains(rows[0].ConfigError, "sniffing") || !strings.Contains(rows[0].ConfigError, "or-bad") {
		t.Fatalf("config error must name unknown field + instance identity: %q", rows[0].ConfigError)
	}
	if rows[0].Conformance != "invalid" {
		t.Fatalf("conformance=%q, want invalid", rows[0].Conformance)
	}
}

func TestProjectOpenResponsesFrontendRows_disabledRowStillProjected(t *testing.T) {
	t.Parallel()
	cfg := frontendDiagnosticsConfig(t, "or-disabled", false, `{}`)
	rows := ProjectOpenResponsesFrontendRows(cfg)
	if len(rows) != 1 {
		t.Fatalf("rows=%d", len(rows))
	}
	if rows[0].Enabled {
		t.Fatal("disabled row must project enabled=false")
	}
	if rows[0].ConfigError != "" {
		t.Fatalf("default config must not error: %q", rows[0].ConfigError)
	}
}

func TestProjectOpenResponsesFrontendRows_ignoresOtherFrontends(t *testing.T) {
	t.Parallel()
	cfg := frontendDiagnosticsConfig(t, "openai-responses", true, `{}`)
	cfg.Plugins.Frontends[0].Kind = "openai-responses"
	rows := ProjectOpenResponsesFrontendRows(cfg)
	if len(rows) != 0 {
		t.Fatalf("rows=%d, want 0 for non-openresponses frontend", len(rows))
	}
}
