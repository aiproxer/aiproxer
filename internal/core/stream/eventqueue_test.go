package stream_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestPendingEventQueue_ordering(t *testing.T) {
	t.Parallel()
	q := stream.NewPendingEventQueue(0)
	for i := range 5 {
		if err := q.Push(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: string(rune('a' + i))}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 5 {
		ev, ok := q.PopFront()
		if !ok {
			t.Fatalf("pop %d: ok=false", i)
		}
		want := string(rune('a' + i))
		if ev.Delta != want {
			t.Fatalf("pop %d: Delta=%q want %q", i, ev.Delta, want)
		}
	}
	if q.Len() != 0 {
		t.Fatalf("Len after drain = %d", q.Len())
	}
	_, ok := q.PopFront()
	if ok {
		t.Fatal("pop empty: want false")
	}
}

func TestPendingEventQueue_manyPushPop(t *testing.T) {
	t.Parallel()
	q := stream.NewPendingEventQueue(0)
	for round := range 3 {
		for range 500 {
			if err := q.Push(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}); err != nil {
				t.Fatal(err)
			}
		}
		for i := range 500 {
			ev, ok := q.PopFront()
			if !ok || ev.Kind != aipapi.EventTextDelta {
				t.Fatalf("round %d pop %d", round, i)
			}
		}
	}
	if q.Len() != 0 {
		t.Fatalf("final Len = %d", q.Len())
	}
}

func TestDrainPending(t *testing.T) {
	t.Parallel()
	q := stream.NewPendingEventQueue(0)
	if err := q.Push(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := q.Push(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "b"}); err != nil {
		t.Fatal(err)
	}
	got := stream.DrainPending(&q)
	if len(got) != 2 || got[0].Delta != "a" || got[1].Delta != "b" {
		t.Fatalf("got %+v", got)
	}
	if q.Len() != 0 {
		t.Fatalf("queue not empty: Len=%d", q.Len())
	}
	if again := stream.DrainPending(&q); len(again) != 0 {
		t.Fatalf("second drain: %+v", again)
	}
}

func TestPendingEventQueue_negativeMaxLenIsUnbounded(t *testing.T) {
	t.Parallel()
	// NewPendingEventQueue only enforces a cap when maxLen > 0; negative is treated as unbounded
	// (same as 0) so misconfiguration does not spuriously return ErrPendingQueueFull.
	q := stream.NewPendingEventQueue(-1)
	for i := range 5 {
		if err := q.Push(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: string(rune('a' + i))}); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
	}
	if q.Len() != 5 {
		t.Fatalf("Len=%d want 5", q.Len())
	}
}

func TestPendingEventQueue_cap(t *testing.T) {
	t.Parallel()
	q := stream.NewPendingEventQueue(2)
	if err := q.Push(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := q.Push(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := q.Push(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "c"}); err != stream.ErrPendingQueueFull {
		t.Fatalf("want ErrPendingQueueFull, got %v", err)
	}
	ev, ok := q.PopFront()
	if !ok || ev.Delta != "a" {
		t.Fatalf("pop: %+v ok=%v", ev, ok)
	}
	if err := q.Push(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "c"}); err != nil {
		t.Fatal(err)
	}
}
