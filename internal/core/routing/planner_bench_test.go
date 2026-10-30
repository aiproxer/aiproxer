package routing_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
)

func BenchmarkExpandFailover_simpleSelector(b *testing.B) {
	sel, err := routing.Parse("openai:gpt-4o-mini")
	if err != nil {
		b.Fatal(err)
	}
	rng := routing.NewSeededRng(1)
	b.ResetTimer()
	for b.Loop() {
		_, err := routing.ExpandFailover(sel, routing.PlanOptions{Rand: rng})
		if err != nil {
			b.Fatal(err)
		}
	}
}
