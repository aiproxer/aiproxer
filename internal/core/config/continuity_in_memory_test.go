package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

// TestValidate_refusesContinuityInMemoryWithDurableStore proves the legacy
// in_memory switch and a durable store cannot be combined. in_memory: true is
// the legacy spelling of "store: memory", so pairing it with a durable backing
// asked for two backings at once and the durable one was silently downgraded
// to the process-local store.
func TestValidate_refusesContinuityInMemoryWithDurableStore(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  config.ContinuityConfig
	}{
		{
			name: "postgres",
			cfg: config.ContinuityConfig{
				InMemory:    true,
				Store:       "postgres",
				PostgresDSN: "postgres://u:p@127.0.0.1:1/aip?sslmode=disable",
			},
		},
		{
			name: "sqlite",
			cfg: config.ContinuityConfig{
				InMemory:   true,
				Store:      "sqlite",
				SQLitePath: filepath.Join(t.TempDir(), "continuity.db"),
			},
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
				t.Fatal("validate accepted the legacy in-memory switch with a durable store")
			}
			if !strings.Contains(err.Error(), "continuity.in_memory") {
				t.Fatalf("error does not name the offending field: %v", err)
			}
			if !strings.Contains(err.Error(), "remove in_memory or set store: memory") {
				t.Fatalf("error does not name the remedy: %v", err)
			}
		})
	}
}

// TestValidate_acceptsContinuityInMemoryWithOmittedOrMemoryStore proves the
// legacy spelling keeps working: in_memory: true with an omitted store and
// with an explicit "store: memory" are the same configuration.
func TestValidate_acceptsContinuityInMemoryWithOmittedOrMemoryStore(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  config.ContinuityConfig
	}{
		{name: "omitted_store", cfg: config.ContinuityConfig{InMemory: true}},
		{name: "memory_store", cfg: config.ContinuityConfig{InMemory: true, Store: "memory"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{
				Continuity: tc.cfg,
				Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "b1", Enabled: true}}},
			}
			if err := config.Validate(cfg); err != nil {
				t.Fatalf("validate refused the legacy in-memory spelling: %v", err)
			}
		})
	}
}

// TestLoadFile_refusesContinuityInMemoryWithDurableStore proves the loader
// refuses the contradictory block, so the serving runtime never starts with a
// durable store that was silently downgraded to the process-local one.
func TestLoadFile_refusesContinuityInMemoryWithDurableStore(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	yml := `
server:
  address: "127.0.0.1:0"
continuity:
  in_memory: true
  store: postgres
  postgres_dsn: "postgres://u:p@127.0.0.1:1/aip?sslmode=disable"
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
		t.Fatal("load accepted the legacy in-memory switch with a durable store")
	}
	if !strings.Contains(err.Error(), "continuity.in_memory") {
		t.Fatalf("error does not name the offending field: %v", err)
	}
}

// TestLoadFile_continuityInMemoryOnly_materializesMemoryStore proves a
// configuration that only sets the legacy in_memory flag still loads and
// resolves to the process-local backing the serving runtime builds for it.
func TestLoadFile_continuityInMemoryOnly_materializesMemoryStore(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	yml := `
server:
  address: "127.0.0.1:0"
continuity:
  in_memory: true
plugins:
  backends:
    - id: stub
      enabled: true
`
	if err := os.WriteFile(p, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Continuity.Store; got != config.ContinuityStoreMemory {
		t.Fatalf("continuity.store = %q want %q", got, config.ContinuityStoreMemory)
	}
	if got := config.EffectiveContinuityStore(cfg.Continuity); got != config.ContinuityStoreMemory {
		t.Fatalf("effective continuity store = %q want %q", got, config.ContinuityStoreMemory)
	}
}
