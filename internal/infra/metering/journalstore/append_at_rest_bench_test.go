package journalstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// The benchmarks in this file measure the steady-state cost of one journal
// write against a journal that already holds history, which is what a
// long-running process pays for every metering fact. Run them with a fixed
// -benchtime (for example -benchtime=200x) so competing builds append the same
// number of facts and therefore observe the same journal growth.

const (
	benchAtRestStreamID = "stream-at-rest"
	benchAtRestNow      = 1_700_000_000
)

func benchAtRestFact(i int) metering.Fact {
	return metering.Fact{
		FactID:      fmt.Sprintf("fact-%d", i),
		StreamID:    benchAtRestStreamID,
		Sequence:    int64(i*2 + 1),
		Kind:        metering.FactKindDelta,
		Perspective: metering.PerspectiveCustomer,
		Boundary:    metering.BoundaryFrontendIngress,
		Lifecycle:   metering.LifecycleLogicalRequest,
		Correlation: metering.Correlation{RequestID: "req-at-rest"},
		Scope:       scope.PrincipalScopeView{PrincipalID: scope.Known("p1")},
		Quantities: []metering.Quantity{{
			Component: metering.ComponentInputToken,
			Unit:      metering.UnitToken,
			Value:     10,
			Present:   true,
		}},
		Source:     metering.SourceObserved,
		Authority:  metering.AuthorityAuthoritative,
		Presence:   metering.PresencePresent,
		RecordedAt: time.Unix(benchAtRestNow, 0).UTC(),
	}
}

// seededAtRestJournal preloads size facts so the timed loop starts from a
// journal that already holds history. Seeding runs before b.Loop.
func seededAtRestJournal(b *testing.B, size int) *MemoryStore {
	b.Helper()
	store, err := NewMemoryStore(MemoryConfig{
		StoreID: "bench-at-rest",
		Now:     func() time.Time { return time.Unix(benchAtRestNow, 0).UTC() },
	})
	if err != nil {
		b.Fatalf("new store: %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })
	for i := 0; i < size; i++ {
		if err := store.SeedSourceKeyForTest(fmt.Sprintf("seed-%d", i), benchAtRestFact(i)); err != nil {
			b.Fatalf("seed %d: %v", i, err)
		}
	}
	return store
}

// BenchmarkMemoryAppendDeltaAtJournalSize appends one delta fact to a journal
// that already holds size facts.
func BenchmarkMemoryAppendDeltaAtJournalSize(b *testing.B) {
	for _, size := range []int{512, 4096} {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			store := seededAtRestJournal(b, size)
			ctx := context.Background()
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				if err := store.Append(ctx, benchAtRestFact(size+i)); err != nil {
					b.Fatalf("append delta: %v", err)
				}
				i++
			}
		})
	}
}

// BenchmarkMemoryAppendCorrectionAtJournalSize appends one correction fact
// that supersedes the first seeded fact, exercising the supersession-graph
// validation path against a journal that already holds size facts.
func BenchmarkMemoryAppendCorrectionAtJournalSize(b *testing.B) {
	for _, size := range []int{512, 4096} {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			store := seededAtRestJournal(b, size)
			ctx := context.Background()
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				corr := benchAtRestFact(size + i)
				corr.FactID = fmt.Sprintf("corr-%d", i)
				corr.Sequence = int64(size+i)*2 + 1
				corr.Kind = metering.FactKindCorrection
				corr.Supersedes = []string{"fact-0"}
				if err := store.Append(ctx, corr); err != nil {
					b.Fatalf("append correction: %v", err)
				}
				i++
			}
		})
	}
}
