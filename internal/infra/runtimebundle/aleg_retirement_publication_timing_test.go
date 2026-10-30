package runtimebundle_test

import (
	"context"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
)

// observerRecordingStore counts retirement-observer registrations on the
// process-owned continuity store. The store composes its observers: the
// process-owned cleanup and the published generation's cleanup register under
// different keys, so neither registration displaces the other.
type observerRecordingStore struct {
	b2bua.Store
	mu       sync.Mutex
	binds    int
	keyed    int
	lastKey  string
	forwards map[string]func(string)
}

func (s *observerRecordingStore) SetALegRetirementObserver(fn func(string)) {
	s.mu.Lock()
	s.binds++
	s.mu.Unlock()
	if inner, ok := s.Store.(b2bua.ALegRetirementObserver); ok {
		inner.SetALegRetirementObserver(fn)
	}
}

// SetKeyedALegRetirementObserver records the keyed registration and forwards
// it to the wrapped store.
func (s *observerRecordingStore) SetKeyedALegRetirementObserver(key string, fn func(string)) {
	s.mu.Lock()
	s.keyed++
	s.lastKey = key
	s.mu.Unlock()
	if inner, ok := s.Store.(b2bua.ALegRetirementObservers); ok {
		inner.SetKeyedALegRetirementObserver(key, fn)
	}
}

func (s *observerRecordingStore) bindCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.binds
}

func (s *observerRecordingStore) keyedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keyed
}

func (s *observerRecordingStore) key() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastKey
}

// TestCompileGeneration_ALegRetirementObserverBindsOnPublish verifies that a
// compiled but unpublished candidate never retargets the process store's
// retirement observers; only publication registers the generation's cleanup,
// exactly once, under its own key.
func TestCompileGeneration_ALegRetirementObserverBindsOnPublish(t *testing.T) {
	t.Parallel()
	cfg := keepwarmLedgerTestConfig()
	ps := mustKeepwarmLedgerProcess(t, cfg)
	rec := &observerRecordingStore{}
	runtimebundle.WrapProcessContinuityForTest(ps, func(inner b2bua.Store) b2bua.Store {
		rec.Store = inner
		return rec
	})

	gen, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	defer func() { _ = gen.Close() }()
	if got := rec.bindCount(); got != 0 {
		t.Fatalf("compile rebound the retirement observer: binds=%d want 0", got)
	}
	if got := rec.keyedCount(); got != 0 {
		t.Fatalf("compile registered a keyed retirement observer: %d want 0", got)
	}

	starter, ok := gen.(interface{ StartPublished(context.Context) error })
	if !ok {
		t.Fatal("generation does not expose StartPublished")
	}
	for range 2 {
		if err := starter.StartPublished(context.Background()); err != nil {
			t.Fatalf("StartPublished: %v", err)
		}
	}
	if got := rec.keyedCount(); got != 1 {
		t.Fatalf("after publication: keyed registrations=%d want 1", got)
	}
	if got := rec.key(); got != b2bua.RetirementObserverGeneration {
		t.Fatalf("after publication: key=%q want %q", got, b2bua.RetirementObserverGeneration)
	}
}
