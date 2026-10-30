package stream

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func BenchmarkPendingEventQueue_pushPop(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		q := NewPendingEventQueue(0)
		for range 256 {
			if err := q.Push(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "d"}); err != nil {
				b.Fatal(err)
			}
		}
		for range 256 {
			if _, ok := q.PopFront(); !ok {
				b.Fatal("pop")
			}
		}
	}
}
