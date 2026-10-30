package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

// TestLoadFile_omittedContinuityBlock_materializesMemoryStore proves the
// effective configuration of a file without a continuity block names the same
// backing the runtime builds for it. The materialized value is what the loader
// hands to the serving runtime, so it must not stay empty.
func TestLoadFile_omittedContinuityBlock_materializesMemoryStore(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(p, []byte(`
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
	if got := cfg.Continuity.Store; got != config.ContinuityStoreMemory {
		t.Fatalf("continuity.store = %q want %q", got, config.ContinuityStoreMemory)
	}
	if got := config.EffectiveContinuityStore(cfg.Continuity); got != config.ContinuityStoreMemory {
		t.Fatalf("effective continuity store = %q want %q", got, config.ContinuityStoreMemory)
	}
}

// TestEffectiveContinuityStore_inMemoryForcesMemory proves the resolver is the
// pure resolution function: in_memory wins over the store name. The validator
// refuses that combination before it reaches the runtime, so the forced
// process-local backing is never a silent downgrade of a durable store.
func TestEffectiveContinuityStore_inMemoryForcesMemory(t *testing.T) {
	t.Parallel()
	s := config.EffectiveContinuityStore(config.ContinuityConfig{
		InMemory: true,
		Store:    "postgres",
	})
	if s != "memory" {
		t.Fatalf("got %q want memory", s)
	}
}

func TestEffectiveContinuityStore_emptyDefaultsMemory(t *testing.T) {
	t.Parallel()
	s := config.EffectiveContinuityStore(config.ContinuityConfig{InMemory: false})
	if s != "memory" {
		t.Fatalf("got %q want memory", s)
	}
}

func TestEffectiveContinuityStore_postgres(t *testing.T) {
	t.Parallel()
	s := config.EffectiveContinuityStore(config.ContinuityConfig{
		InMemory: false,
		Store:    "postgres",
	})
	if s != "postgres" {
		t.Fatalf("got %q want postgres", s)
	}
}
