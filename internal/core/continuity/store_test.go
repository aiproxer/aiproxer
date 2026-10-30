package continuity_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/continuity"
)

func TestNewMemoryStoreFromConfig_inMemoryFalse(t *testing.T) {
	t.Parallel()
	// An omitted in_memory flag is not a request for a different backing: the
	// effective store decides, and an omitted store is the memory backing. The
	// configuration validator and the loader defaults resolve it the same way,
	// so this store is the one the serving runtime builds.
	store, err := continuity.NewMemoryStoreFromConfig(config.ContinuityConfig{InMemory: false})
	if err != nil {
		t.Fatalf("omitted in_memory with an omitted store must build the memory store: %v", err)
	}
	if store == nil {
		t.Fatal("nil store")
	}
}

func TestNewMemoryStoreFromConfig_refusesAnotherStore(t *testing.T) {
	t.Parallel()
	_, err := continuity.NewMemoryStoreFromConfig(config.ContinuityConfig{Store: "sqlite"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "is not the memory store") {
		t.Fatalf("error: %v", err)
	}
}

func TestNewMemoryStoreFromConfig_invalidTTL(t *testing.T) {
	t.Parallel()
	_, err := continuity.NewMemoryStoreFromConfig(config.ContinuityConfig{
		InMemory: true,
		TTL:      "not-a-duration",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "continuity.ttl") {
		t.Fatalf("error: %v", err)
	}
}

func TestNewMemoryStoreFromConfig_negativeTTL(t *testing.T) {
	t.Parallel()
	_, err := continuity.NewMemoryStoreFromConfig(config.ContinuityConfig{
		InMemory: true,
		TTL:      "-1h",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "continuity.ttl") {
		t.Fatalf("error: %v", err)
	}
}

func TestNewMemoryStoreFromConfig_rejectsNegativeMaxLegs(t *testing.T) {
	t.Parallel()
	_, err := continuity.NewMemoryStoreFromConfig(config.ContinuityConfig{
		InMemory: true,
		MaxLegs:  -1,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "max_legs") {
		t.Fatalf("error: %v", err)
	}
}

func TestNewMemoryStoreFromConfig_happyPath(t *testing.T) {
	t.Parallel()
	store, err := continuity.NewMemoryStoreFromConfig(config.ContinuityConfig{
		InMemory: true,
		TTL:      "24h",
		MaxLegs:  42,
	})
	if err != nil {
		t.Fatal(err)
	}
	if store == nil {
		t.Fatal("nil store")
	}
}

func TestNewMemoryStoreFromConfig_zeroContinuityConfig(t *testing.T) {
	t.Parallel()
	// The Go zero value is what an omitted continuity block decodes to. It
	// resolves to the memory backing, so the store is built rather than refused.
	store, err := continuity.NewMemoryStoreFromConfig(config.ContinuityConfig{})
	if err != nil {
		t.Fatalf("omitted continuity block must build the memory store: %v", err)
	}
	if store == nil {
		t.Fatal("nil store")
	}
}
