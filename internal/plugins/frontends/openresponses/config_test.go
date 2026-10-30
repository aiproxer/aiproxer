package openresponses_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"gopkg.in/yaml.v3"
)

func TestConfig_DefaultAndValid(t *testing.T) {
	t.Parallel()

	var node yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &node); err != nil {
		t.Fatal(err)
	}

	cfg, err := openresponses.DecodeConfig(node)
	if err != nil {
		t.Fatalf("unexpected error for default config: %v", err)
	}

	if cfg.Profile != "2026-04-24" {
		t.Errorf("got profile %q, want %q", cfg.Profile, "2026-04-24")
	}
	if cfg.BasePath != "/openresponses/v1" {
		t.Errorf("got base_path %q, want %q", cfg.BasePath, "/openresponses/v1")
	}
	if cfg.Continuation.PersistentStore != "standard" {
		t.Errorf("got persistent_store %q, want %q", cfg.Continuation.PersistentStore, "standard")
	}
	if cfg.Continuation.TTL != "24h" {
		t.Errorf("got ttl %q, want %q", cfg.Continuation.TTL, "24h")
	}
	if cfg.Continuation.MaxChainDepth != 64 {
		t.Errorf("got max_chain_depth %d, want 64", cfg.Continuation.MaxChainDepth)
	}
	if cfg.Continuation.MaxMaterializedBytes != 67108864 {
		t.Errorf("got max_materialized_bytes %d, want 67108864", cfg.Continuation.MaxMaterializedBytes)
	}
	if !cfg.WebSocket.Enabled {
		t.Errorf("got websocket.enabled false, want true")
	}
	if cfg.WebSocket.MaxConnectionAge != "60m" {
		t.Errorf("got max_connection_age %q, want %q", cfg.WebSocket.MaxConnectionAge, "60m")
	}
	if cfg.WebSocket.IdleTimeout != "5m" {
		t.Errorf("got idle_timeout %q, want %q", cfg.WebSocket.IdleTimeout, "5m")
	}
	if cfg.WebSocket.MaxQueuedTurns != 1 {
		t.Errorf("got max_queued_turns %d, want 1", cfg.WebSocket.MaxQueuedTurns)
	}
}

func TestConfig_StrictUnknownFieldsRejection(t *testing.T) {
	t.Parallel()

	unknownTopLevel := `
profile: "2026-04-24"
unknown_field: true
`
	var node1 yaml.Node
	if err := yaml.Unmarshal([]byte(unknownTopLevel), &node1); err != nil {
		t.Fatal(err)
	}
	if _, err := openresponses.DecodeConfig(node1); err == nil {
		t.Error("expected error for unknown top-level field, got nil")
	} else if !strings.Contains(err.Error(), "openresponses") {
		t.Errorf("error %q should contain frontend ID", err.Error())
	}

	unknownSubField := `
profile: "2026-04-24"
continuation:
  ttl: "12h"
  bogus_setting: "bad"
`
	var node2 yaml.Node
	if err := yaml.Unmarshal([]byte(unknownSubField), &node2); err != nil {
		t.Fatal(err)
	}
	if _, err := openresponses.DecodeConfig(node2); err == nil {
		t.Error("expected error for unknown continuation sub-field, got nil")
	}

	unknownWSSubField := `
websocket:
  enabled: true
  invalid_ws_opt: 123
`
	var node3 yaml.Node
	if err := yaml.Unmarshal([]byte(unknownWSSubField), &node3); err != nil {
		t.Fatal(err)
	}
	if _, err := openresponses.DecodeConfig(node3); err == nil {
		t.Error("expected error for unknown websocket sub-field, got nil")
	}
}

func TestConfig_ValidationRejections(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		yaml string
	}{
		{name: "invalid profile", yaml: `profile: "2025-01-01"`},
		{name: "invalid base path", yaml: `base_path: "relative/path"`},
		{name: "invalid ttl", yaml: `continuation: { ttl: "invalid" }`},
		{name: "negative max chain depth", yaml: `continuation: { max_chain_depth: -1 }`},
		{name: "invalid max connection age", yaml: `websocket: { max_connection_age: "90m" }`}, // capped at 60m
		{name: "invalid idle timeout", yaml: `websocket: { idle_timeout: "bad" }`},
		{name: "zero max queued turns", yaml: `websocket: { max_queued_turns: 0 }`},
		{name: "origin with path", yaml: `websocket: { allowed_origins: ["https://example.com/app"] }`},
		{name: "origin with credentials", yaml: `websocket: { allowed_origins: ["https://user:pass@example.com"] }`},
		{name: "unsupported store", yaml: `continuation: { persistent_store: "secret-store" }`},
		{name: "oversized materialization", yaml: `continuation: { max_materialized_bytes: 999999999 }`},
	}

	for _, tc := range cases {
		t.Run(tc.yaml, func(t *testing.T) {
			t.Parallel()
			var node yaml.Node
			if err := yaml.Unmarshal([]byte(tc.yaml), &node); err != nil {
				t.Fatal(err)
			}
			if _, err := openresponses.DecodeConfig(node); err == nil {
				t.Errorf("expected validation error for %s, got nil", tc.yaml)
			}
		})
	}
}

func TestConfig_NullSectionsRetainDefaults(t *testing.T) {
	t.Parallel()

	var node yaml.Node
	if err := yaml.Unmarshal([]byte("continuation: null\nwebsocket: null\n"), &node); err != nil {
		t.Fatal(err)
	}

	cfg, err := openresponses.DecodeConfig(node)
	if err != nil {
		t.Fatalf("null sections should use defaults: %v", err)
	}
	if cfg.Continuation.MaxChainDepth != openresponses.DefaultMaxChainDepth {
		t.Fatalf("max_chain_depth=%d, want default %d", cfg.Continuation.MaxChainDepth, openresponses.DefaultMaxChainDepth)
	}
	if !cfg.WebSocket.Enabled || cfg.WebSocket.MaxQueuedTurns != openresponses.DefaultMaxQueuedTurns {
		t.Fatalf("websocket defaults were not retained: %+v", cfg.WebSocket)
	}
}
