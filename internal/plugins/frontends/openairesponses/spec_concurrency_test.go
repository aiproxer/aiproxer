package openairesponses

import (
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
)

// TestHandlerSpecConcurrentFirstUse guards the once-only lazy build of the
// frontendpipe.Spec. Before sync.Once, concurrent first requests all passed the
// unsynchronized check-then-write and raced on h.pipe (issue #262, 21 DATA RACE
// reports under the nightly -race gate). Run with -race to catch a regression.
func TestHandlerSpecConcurrentFirstUse(t *testing.T) {
	t.Parallel()
	h := &Handler{DefaultRouteSelector: "stub:gpt-4o-mini"}
	const goroutines = 64
	results := make([]*frontendpipe.Spec[EncodeOptions], goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range results {
		go func(i int) {
			defer wg.Done()
			results[i] = h.spec()
		}(i)
	}
	wg.Wait()

	first := h.spec()
	if first.DefaultRouteSelector != "stub:gpt-4o-mini" {
		t.Fatalf("spec route selector = %q, want stub:gpt-4o-mini", first.DefaultRouteSelector)
	}
	for i, got := range results {
		if got != first {
			t.Fatalf("concurrent spec %d = %p, want the single built spec %p", i, got, first)
		}
	}
}
