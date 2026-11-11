package journalstore

import (
	"context"
	"fmt"
	"testing"
)

// BenchmarkMemoryAppendDeltaAtLargeJournal extends the at-rest family to a
// journal size a long-running process actually reaches, so append cost is
// visible as a growth curve rather than a single point: a per-write identity
// scan shows up as ns/op proportional to the stored history.
func BenchmarkMemoryAppendDeltaAtLargeJournal(b *testing.B) {
	for _, size := range []int{4096, 16384} {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			store := seededAtRestJournal(b, size)
			ctx := context.Background()
			b.ReportAllocs()
			for i := 0; b.Loop(); i++ {
				if err := store.Append(ctx, benchAtRestFact(size+i)); err != nil {
					b.Fatalf("append delta: %v", err)
				}
			}
		})
	}
}
