package journalstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// TestMemoryStore_SupersessionValidationOnlyForSupersedingKinds pins the memory
// journal to the durable store's rule: only corrections and replacements walk
// the supersession graph, and the walk still rejects missing, foreign-stream,
// and cyclic targets when they do.
func TestMemoryStore_SupersessionValidationOnlyForSupersedingKinds(t *testing.T) {
	t.Parallel()
	s, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "mem-supersede"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Append(ctx, validFact("base", "stream-s", 1)); err != nil {
		t.Fatal(err)
	}
	// A non-superseding fact never consults the supersession graph, so appending
	// it over existing history must succeed.
	if err := s.Append(ctx, validFact("delta", "stream-s", 2)); err != nil {
		t.Fatalf("delta append over existing history: %v", err)
	}

	missing := validFact("missing-target", "stream-s", 3)
	missing.Kind = metering.FactKindCorrection
	missing.Supersedes = []string{"absent"}
	if err := s.Append(ctx, missing); !errors.Is(err, journalstore.ErrSupersessionTarget) {
		t.Fatalf("missing target got %v want ErrSupersessionTarget", err)
	}

	foreign := validFact("foreign-target", "stream-s", 4)
	foreign.Kind = metering.FactKindCorrection
	foreign.Supersedes = []string{"other-stream-fact"}
	if err := s.Append(ctx, foreign); !errors.Is(err, journalstore.ErrSupersessionTarget) {
		t.Fatalf("foreign target got %v want ErrSupersessionTarget", err)
	}

	valid := validFact("valid-corr", "stream-s", 5)
	valid.Kind = metering.FactKindCorrection
	valid.Supersedes = []string{"base"}
	if err := s.Append(ctx, valid); err != nil {
		t.Fatalf("valid correction: %v", err)
	}
}

// TestMemoryStore_SupersessionCycleRejected proves the memory journal still
// refuses a correction that would close a cycle through earlier edges.
func TestMemoryStore_SupersessionCycleRejected(t *testing.T) {
	t.Parallel()
	s, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "mem-cycle"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Seed the forward edge future -> seed so appending "future" as a correction
	// of "seed" would close a cycle. SeedSourceKeyForTest indexes legacy rows
	// without graph validation, which is the only way to create such a shape.
	seed := validFact("seed", "stream-cycle", 1)
	seed.Kind = metering.FactKindCorrection
	seed.Supersedes = []string{"future"}
	if err := s.SeedSourceKeyForTest("seed-key", seed); err != nil {
		t.Fatal(err)
	}
	closing := validFact("future", "stream-cycle", 2)
	closing.Kind = metering.FactKindCorrection
	closing.Supersedes = []string{"seed"}
	if err := s.Append(ctx, closing); !errors.Is(err, journalstore.ErrSupersessionCycle) {
		t.Fatalf("cycle got %v want ErrSupersessionCycle", err)
	}
}
