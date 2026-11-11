package journalstore_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

type identityReplayCase struct {
	name  string
	setup func(t *testing.T, store *journalstore.MemoryStore)
	next  func() metering.Fact
	want  error
}

// The identity index keeps the collision rule reachable without rescanning the
// journal. These cases pin what an index can silently break: a row seeded under
// an explicit legacy key, duplicate identities, trimmed identity comparison, and
// identifiers that embed a separator.
func TestMemoryStore_IdentityReplayClassifiesLikeTheJournalScan(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []identityReplayCase{
		{
			name: "legacy seeded row replay",
			setup: func(t *testing.T, store *journalstore.MemoryStore) {
				t.Helper()
				if err := store.SeedSourceKeyForTest("legacy-key", validFact("seeded", "stream-index", 1)); err != nil {
					t.Fatal(err)
				}
			},
			next: func() metering.Fact { return validFact("seeded", "stream-index", 1) },
		},
		{
			name: "legacy seeded row collision",
			setup: func(t *testing.T, store *journalstore.MemoryStore) {
				t.Helper()
				if err := store.SeedSourceKeyForTest("legacy-key", validFact("seeded", "stream-index", 1)); err != nil {
					t.Fatal(err)
				}
			},
			next: func() metering.Fact { return validFact("seeded", "stream-index", 2) },
			want: journalstore.ErrIdentityCollision,
		},
		{
			// Both rows carry the same trimmed identity but distinct source keys,
			// so the identity decides which row classifies the append: the first
			// stored one, exactly as the former journal scan returned it.
			name: "duplicate identity keeps the first row",
			setup: func(t *testing.T, store *journalstore.MemoryStore) {
				t.Helper()
				if err := store.SeedSourceKeyForTest("first-key", validFact("dup", "stream-index", 1)); err != nil {
					t.Fatal(err)
				}
				second := validFact("dup", "stream-index", 2)
				second.Correlation.RequestID = "req-2"
				if err := store.SeedSourceKeyForTest("second-key", second); err != nil {
					t.Fatal(err)
				}
			},
			next: func() metering.Fact { return validFact("dup", "stream-index", 1) },
		},
		{
			name: "trimmed identity collides",
			setup: func(t *testing.T, store *journalstore.MemoryStore) {
				t.Helper()
				if err := store.Append(ctx, validFact("fact-trim", "stream-index", 1)); err != nil {
					t.Fatal(err)
				}
				padded := validFact("fact-trim", "stream-index", 2)
				padded.StreamID = "  stream-index  "
				padded.FactID = " fact-trim "
				padded.Correlation.RequestID = "req-padded"
				if err := store.SeedSourceKeyForTest("padded-key", padded); err != nil {
					t.Fatal(err)
				}
			},
			next: func() metering.Fact { return validFact("fact-trim", "stream-index", 3) },
			want: journalstore.ErrIdentityCollision,
		},
		{
			name: "identifier embedding a separator stays distinct",
			setup: func(t *testing.T, store *journalstore.MemoryStore) {
				t.Helper()
				if err := store.Append(ctx, validFact("fact-a", "stream-index", 1)); err != nil {
					t.Fatal(err)
				}
			},
			next: func() metering.Fact { return validFact("fact-a", "stream-index\x00fact-a", 2) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "mem-identity/" + tc.name})
			if err != nil {
				t.Fatal(err)
			}
			tc.setup(t, store)
			err = store.Append(ctx, tc.next())
			if tc.want == nil {
				if err != nil {
					t.Fatalf("append: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("append got %v want %v", err, tc.want)
			}
		})
	}
}

// TestMemoryStore_IdentityReplayPreservesErrorDetail keeps the collision
// message's stored/new sequence evidence, which callers put in diagnostics.
func TestMemoryStore_IdentityReplayPreservesErrorDetail(t *testing.T) {
	t.Parallel()
	store, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "mem-identity-error"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.Append(ctx, validFact("fact-detail", "stream-detail", 1)); err != nil {
		t.Fatal(err)
	}
	err = store.Append(ctx, validFact("fact-detail", "stream-detail", 2))
	if !errors.Is(err, journalstore.ErrIdentityCollision) {
		t.Fatalf("collision got %v", err)
	}
	if want := "stored_seq=1 new_seq=2"; !strings.Contains(err.Error(), want) {
		t.Fatalf("collision detail lost %q in %q", want, err.Error())
	}
}
