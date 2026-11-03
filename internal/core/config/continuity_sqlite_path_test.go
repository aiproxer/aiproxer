package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

// TestValidate_refusesContinuitySQLitePathWithMemoryStore proves a nonempty
// continuity.sqlite_path is refused when the effective backing is the
// process-local one. Only the sqlite backing reads the path, so accepting it
// next to the memory store created a false expectation of persistence.
func TestValidate_refusesContinuitySQLitePathWithMemoryStore(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "ignored-continuity.db")
	cases := []struct {
		name string
		cfg  config.ContinuityConfig
	}{
		{
			name: "omitted_store",
			cfg:  config.ContinuityConfig{SQLitePath: path},
		},
		{
			name: "memory_store",
			cfg:  config.ContinuityConfig{Store: "memory", SQLitePath: path},
		},
		{
			name: "legacy_in_memory_only",
			cfg:  config.ContinuityConfig{InMemory: true, SQLitePath: path},
		},
		{
			name: "legacy_in_memory_with_memory_store",
			cfg:  config.ContinuityConfig{InMemory: true, Store: "memory", SQLitePath: path},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{
				Continuity: tc.cfg,
				Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "b1", Enabled: true}}},
			}
			err := config.Validate(cfg)
			if err == nil {
				t.Fatal("validate accepted a sqlite_path for the memory store, which ignores it")
			}
			if !strings.Contains(err.Error(), "continuity.sqlite_path") {
				t.Fatalf("error does not name the offending field: %v", err)
			}
			if !strings.Contains(err.Error(), "remove sqlite_path or set store: sqlite") {
				t.Fatalf("error does not name the remedy: %v", err)
			}
		})
	}
}

// TestValidate_acceptsContinuitySQLitePathWithSQLiteStore proves the sibling
// durable backing still reads the path, so the cross-field rule only refuses
// configurations whose effective store would ignore it.
func TestValidate_acceptsContinuitySQLitePathWithSQLiteStore(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Continuity: config.ContinuityConfig{
			Store:      "sqlite",
			SQLitePath: filepath.Join(t.TempDir(), "continuity.db"),
		},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{ID: "b1", Enabled: true}}},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate refused the sqlite backing that reads the path: %v", err)
	}
}

// TestValidate_acceptsWhitespaceContinuitySQLitePathWithMemoryStore proves the
// refusal targets an actual path value: whitespace is not a path the operator
// could have expected to be opened.
func TestValidate_acceptsWhitespaceContinuitySQLitePathWithMemoryStore(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Continuity: config.ContinuityConfig{InMemory: true, SQLitePath: "   "},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "b1", Enabled: true}}},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate refused a whitespace-only sqlite_path: %v", err)
	}
}

// TestLoadFile_refusesContinuitySQLitePathWithMemoryStore proves the loader
// refuses the ignored path before the serving runtime builds the memory store,
// so check-config and startup agree on the configuration.
func TestLoadFile_refusesContinuitySQLitePathWithMemoryStore(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	yml := `
server:
  address: "127.0.0.1:0"
continuity:
  in_memory: true
  store: memory
  sqlite_path: ./data/ignored-continuity.db
plugins:
  backends:
    - id: stub
      enabled: true
`
	if err := os.WriteFile(p, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.LoadFile(p)
	if err == nil {
		t.Fatal("load accepted a sqlite_path that the memory store ignores")
	}
	if !strings.Contains(err.Error(), "continuity.sqlite_path") {
		t.Fatalf("error does not name the offending field: %v", err)
	}
	if !strings.Contains(err.Error(), "remove sqlite_path or set store: sqlite") {
		t.Fatalf("error does not name the remedy: %v", err)
	}
}
