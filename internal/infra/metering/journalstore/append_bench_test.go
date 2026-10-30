package journalstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// BenchmarkMemoryAppendAndCorrectionReplay measures append + correction replay (16.6).
func BenchmarkMemoryAppendAndCorrectionReplay(b *testing.B) {
	store, err := NewMemoryStore(MemoryConfig{StoreID: "bench-journal"})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	b.ReportAllocs()

	for i := 0; b.Loop(); i++ {
		base := metering.Fact{
			FactID:      fmt.Sprintf("fact-%d", i),
			StreamID:    "stream-bench",
			Sequence:    int64(i*2 + 1),
			Kind:        metering.FactKindDelta,
			Perspective: metering.PerspectiveCustomer,
			Boundary:    metering.BoundaryFrontendIngress,
			Lifecycle:   metering.LifecycleLogicalRequest,
			Correlation: metering.Correlation{RequestID: "req-bench"},
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
			RecordedAt: now,
		}
		if err := store.Append(ctx, base); err != nil {
			b.Fatal(err)
		}
		corr := base
		corr.FactID = fmt.Sprintf("corr-%d", i)
		corr.Sequence = int64(i*2 + 2)
		corr.Kind = metering.FactKindCorrection
		corr.Supersedes = []string{base.FactID}
		corr.Quantities = []metering.Quantity{{
			Component: metering.ComponentInputToken,
			Unit:      metering.UnitToken,
			Value:     12,
			Present:   true,
		}}
		if err := store.Append(ctx, corr); err != nil {
			b.Fatal(err)
		}
	}
}
