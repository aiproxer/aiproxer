package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func TestControlPlaneLoadFileDisabledByDefault(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// Minimal config with no control_plane section; must load with capability disabled.
	if err := os.WriteFile(path, []byte("server:\n  address: \"127.0.0.1:8080\"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("load disabled config: %v", err)
	}
	if cfg.ControlPlane.Enabled {
		t.Fatalf("control_plane must be disabled by default after load")
	}
	if cfg.ControlPlane.Store != "" {
		t.Fatalf("store must be empty when disabled, got %q", cfg.ControlPlane.Store)
	}
}

func TestControlPlaneLoadFileEnabledMemory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `server:
  address: "127.0.0.1:8080"
control_plane:
  enabled: true
  store: memory
  recording_policy: best_effort
  redaction_default: standard
  query:
    enabled: true
    path_prefix: "/admin/control-plane"
    default_page_size: 50
    max_page_size: 250
    max_time_window: "12h"
  retention:
    enabled: true
    window: "720h"
  required_categories: ["auth", "session", "audit"]
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("load enabled config: %v", err)
	}
	if !cfg.ControlPlane.Enabled {
		t.Fatalf("control_plane must be enabled")
	}
	if cfg.ControlPlane.Store != "memory" {
		t.Fatalf("store lost: %q", cfg.ControlPlane.Store)
	}
	if cfg.ControlPlane.Query.DefaultPageSize != 50 || cfg.ControlPlane.Query.MaxPageSize != 250 {
		t.Fatalf("query bounds lost: %d/%d", cfg.ControlPlane.Query.DefaultPageSize, cfg.ControlPlane.Query.MaxPageSize)
	}
	if cfg.ControlPlane.Retention.Window != "720h" {
		t.Fatalf("retention window lost: %q", cfg.ControlPlane.Retention.Window)
	}
	if len(cfg.ControlPlane.RequiredCategories) != 3 {
		t.Fatalf("required categories lost: %d", len(cfg.ControlPlane.RequiredCategories))
	}
}

func TestControlPlaneLoadFileRejectsInvalidStore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := "server:\n  address: \"127.0.0.1:8080\"\ncontrol_plane:\n  enabled: true\n  store: redis\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := config.LoadFile(path); err == nil {
		t.Fatalf("invalid store must be rejected at load time")
	}
}
