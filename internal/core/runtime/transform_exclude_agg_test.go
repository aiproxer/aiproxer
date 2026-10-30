package runtime

import (
	"errors"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestTransformExcludeTracker_allExcludedError(t *testing.T) {
	t.Parallel()
	var tr transformExcludeTracker
	if err := tr.allExcludedError(); err != nil {
		t.Fatalf("empty tracker: %v", err)
	}
	tr.noteTransform(canonicalUnrepresentableReplay)
	tr.noteTransform(canonicalUnrepresentableReplay)
	if !errors.Is(tr.allExcludedError(), aipapi.ErrAllCandidatesUnrepresentableReplay) {
		t.Fatalf("got %v", tr.allExcludedError())
	}
	tr.noteTransform("plugin_local")
	if !errors.Is(tr.allExcludedError(), aipapi.ErrAllCandidatesExcluded) {
		t.Fatalf("mixed reasons: %v", tr.allExcludedError())
	}
	tr.noteOther()
	if err := tr.allExcludedError(); err != nil {
		t.Fatalf("non-transform mix must fall through: %v", err)
	}
}

func TestTransformExcludeTracker_concurrentNotesDeterministic(t *testing.T) {
	t.Parallel()
	var tr transformExcludeTracker
	const n = 200
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			tr.noteTransform(canonicalUnrepresentableReplay)
		}()
	}
	wg.Wait()
	if tr.count != n || tr.unrepresentable != n {
		t.Fatalf("race-lost counts count=%d unrep=%d want %d", tr.count, tr.unrepresentable, n)
	}
	if !errors.Is(tr.allExcludedError(), aipapi.ErrAllCandidatesUnrepresentableReplay) {
		t.Fatalf("classification=%v", tr.allExcludedError())
	}
}
